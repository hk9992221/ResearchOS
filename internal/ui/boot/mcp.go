package boot

// This file is the composition root of the SPEC-11 MCP server. The desktop
// application is the only process that opens the databases, so the server runs
// inside it and `researchos-mcp --stdio` is a thin bridge to this listener
// (§5.8). Two things make that work: every module's migration band must be
// registered before the workspace opens (Wire refuses to start when the MCP
// runtime tables are missing), and the listener must be released before the
// workspace closes.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"researchos/internal/ai/gateway"
	aimigrations "researchos/internal/ai/migrations"
	"researchos/internal/export"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/settings"
	"researchos/internal/foundation/storage"
	fworkspace "researchos/internal/foundation/workspace"
	ideasmigrations "researchos/internal/ideas/migrations"
	kanbanmigrations "researchos/internal/kanban/migrations"
	literaturemigrations "researchos/internal/literature/migrations"
	"researchos/internal/mcp"
	"researchos/internal/mcpbind"
	"researchos/internal/mcpfacade"
	mcpstoremigrations "researchos/internal/mcpstore/migrations"
	notesmigrations "researchos/internal/notes/migrations"
	"researchos/internal/overview"
	"researchos/internal/project"
	"researchos/internal/search"
	searchmigrations "researchos/internal/search/migrations"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/pages"
)

// mcpStopGrace is how long the listener may take to return after its context
// is cancelled; beyond it the workspace closes anyway and the goroutine keeps
// its own context cancelled.
const mcpStopGrace = 3 * time.Second

// moduleMigrations merges the migration band of every module into the set the
// workspace installs at Init/Open. The foundation baseline is added by the
// workspace itself; these are the module tables (papers, kanban, MCP runtime,
// …) that the services and the diagnostic target version both need.
func moduleMigrations() (map[storage.Kind][]storage.Migration, error) {
	bands := []struct {
		name string
		fn   func() (map[storage.Kind][]storage.Migration, error)
	}{
		{"ai", aimigrations.All},
		{"ideas", ideasmigrations.All},
		{"kanban", kanbanmigrations.All},
		{"literature", literaturemigrations.All},
		{"mcpstore", mcpstoremigrations.All},
		{"notes", notesmigrations.All},
		{"search", searchmigrations.All},
	}
	out := make(map[storage.Kind][]storage.Migration)
	for _, band := range bands {
		set, err := band.fn()
		if err != nil {
			return nil, fmt.Errorf("boot: 注册 %s 迁移: %w", band.name, err)
		}
		for kind, list := range set {
			out[kind] = append(out[kind], list...)
		}
	}
	return out, nil
}

// appLogger adapts the standard logger to the workspace and MCP logger ports.
// The desktop build has no log sink of its own, so stderr is where a background
// service failure still reaches the user.
type appLogger struct{}

func (appLogger) Debug(string, ...any) {}

func (appLogger) Info(msg string, kv ...any) { log.Printf("INFO  %s %v", msg, kv) }

func (appLogger) Warn(msg string, kv ...any) { log.Printf("WARN  %s %v", msg, kv) }

func (appLogger) Error(msg string, kv ...any) { log.Printf("ERROR %s %v", msg, kv) }

// LogCall implements the SPEC-02 gateway logger: one line per AI call with
// metadata only, never prompts, bodies or keys (§5.8.8).
func (appLogger) LogCall(m gateway.CallMeta) {
	if m.OK {
		log.Printf("AI    %s provider=%s model=%s attempts=%d elapsed=%s",
			m.Operation, m.ProviderName, m.Model, m.Attempts, m.Elapsed)
		return
	}
	log.Printf("AIERR %s provider=%s model=%s attempts=%d elapsed=%s kind=%s status=%d",
		m.Operation, m.ProviderName, m.Model, m.Attempts, m.Elapsed, m.ErrorKind, m.StatusCode)
}

// newSearch builds the SPEC-10 retrieval service shared by the MCP tools and
// the Literature page's post-commit index hook. Keyword recall works without
// an embedding provider; vector recall reports vector_unavailable
// (SPEC-10 §5.1).
func newSearch(ws *fworkspace.Workspace) (*search.Service, error) {
	return search.New(ws, search.Options{Clock: clock.System{}, Logger: appLogger{}})
}

// mcpAppVersion is reported by server_status and the MCP tab. The export
// package uses the same version for its manifest.
const mcpAppVersion = "0.1.0"

// mcpStartWait bounds how long a settings change waits for the listeners to
// bind. Binding is local; a longer wait would only hide a stuck startup.
const mcpStartWait = 5 * time.Second

var (
	errMCPClosed       = pages.ErrMCPClosed
	errMCPUnavailable  = pages.ErrMCPUnavailable
	errMCPStartTimeout = errors.New("MCP 服务器启动超时")
	errMCPStartFailed  = errors.New("MCP 服务器未能启动")
)

// mcpRun is one listener lifetime. ready closes when the stdio (and, if
// enabled, HTTP) listeners are bound; done closes when Start returns.
type mcpRun struct {
	cancel context.CancelFunc
	ready  chan struct{}
	done   chan struct{}
}

// mcpManager owns the workspace-scoped MCP server. It builds the mcpbind.App
// once, starts only when mcp.enabled is true, and stops/restarts on settings
// changes. The manager is bound to one workspace: a page from an older
// workspace gets errMCPClosed instead of reaching the next workspace.
type mcpManager struct {
	ws       *fworkspace.Workspace
	settings *settings.Store
	version  string

	// applyMu serializes SaveMCPSettings and close, so a start and a stop
	// cannot interleave.
	applyMu sync.Mutex

	mu        sync.Mutex
	closed    bool
	app       *mcpbind.App
	buildErr  error
	socket    string
	socketErr error
	values    mcp.SettingsValues
	loadErr   error
	running   bool
	startErr  error
	run       *mcpRun

	workspaceRoot string
	modules       []string
	protocols     []string
	toolCount     int
}

var _ pages.MCPAPI = (*mcpManager)(nil)

// startMCP builds the manager and, when the persisted settings enable it,
// starts the server in the background. A failure is recorded on the manager;
// the desktop application stays usable and the MCP page/diagnostics show why
// the server is down.
func startMCP(ws *fworkspace.Workspace, projects *project.Service, settingsStore *settings.Store, confirmations confirm.Service, searchSvc *search.Service, ai *aiModule, overviewSvc *overview.Service) *mcpManager {
	m := &mcpManager{ws: ws, settings: settingsStore, version: mcpAppVersion}
	if ws != nil {
		m.workspaceRoot = ws.Root()
	}
	if m.settings == nil && ws != nil && ws.Global() != nil {
		m.settings = settings.NewWorkspace(ws.Global().DB()).WithClock(ws.Clock())
	}
	if socket, err := mcp.SocketPath(); err != nil {
		m.socketErr = err
	} else {
		m.socket = socket
	}
	loadCtx, loadCancel := context.WithTimeout(context.Background(), 3*time.Second)
	m.values, m.loadErr = mcp.LoadSettings(loadCtx, m.settings)
	loadCancel()

	facade := mcpfacade.New(mcpfacade.Options{
		Project:   projects,
		Overview:  overviewSvc,
		Export:    export.New(export.Options{Workspace: ws}),
		Workspace: ws,
	})
	wireOpts := mcpbind.WireOptions{
		Workspace:    ws,
		Clock:        ws.Clock(),
		Logger:       appLogger{},
		Version:      mcpAppVersion,
		Confirm:      confirmations,
		Settings:     m.settings,
		Facade:       facade,
		Search:       searchSvc,
		ProjectAdmin: projects,
	}
	if ai.available() {
		// The stack is workspace-scoped; the bindings need one output service
		// per project database, so they resolve it through this factory. The
		// readiness probe keeps the tool gates and server_status truthful
		// when no chat provider is assigned yet.
		wireOpts.AIFor = ai.servicesFor
		wireOpts.AIReady = ai.chatReady
	}
	bound, err := mcpbind.Wire(wireOpts)
	if err != nil {
		m.buildErr = err
	} else {
		m.app = bound
	}
	m.refreshStatus()

	if m.app != nil && m.loadErr == nil && m.values.Enabled {
		go m.initialStart()
	}
	return m
}

// initialStart runs the first start off the workspace-open path, then asks the
// window to repaint so the page sees the final running/error state.
func (m *mcpManager) initialStart() {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	if m.isClosed() {
		return
	}
	_ = m.startRun()
	mygo.InvalidateAll()()
}

// startRun starts the listeners and waits until they are bound, fail, or the
// bounded start window expires. It never reports running on a failed Start.
func (m *mcpManager) startRun() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errMCPClosed
	}
	app := m.app
	if app == nil {
		err := m.buildErr
		if err == nil {
			err = errMCPUnavailable
		}
		m.mu.Unlock()
		return err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	run := &mcpRun{cancel: cancel, ready: make(chan struct{}), done: make(chan struct{})}
	m.run = run
	m.running = false
	m.startErr = nil
	m.mu.Unlock()

	go func() {
		err := app.StartWithReady(runCtx, func() { close(run.ready) })
		cancel()
		m.mu.Lock()
		if m.run == run {
			m.run = nil
			m.running = false
			if err != nil && !errors.Is(err, context.Canceled) {
				m.startErr = err
			}
		}
		m.mu.Unlock()
		close(run.done)
		mygo.InvalidateAll()()
	}()

	timer := time.NewTimer(mcpStartWait)
	defer timer.Stop()
	select {
	case <-run.ready:
		m.mu.Lock()
		if m.run == run {
			m.running = true
			m.startErr = nil
		}
		m.mu.Unlock()
		return nil
	case <-run.done:
		m.mu.Lock()
		err := m.startErr
		m.mu.Unlock()
		if err == nil {
			err = errMCPStartFailed
		}
		return err
	case <-timer.C:
		m.mu.Lock()
		if m.run == run {
			m.startErr = errMCPStartTimeout
		}
		m.mu.Unlock()
		m.stopRun()
		return errMCPStartTimeout
	}
}

// stopRun cancels the current listeners and waits the shutdown grace. A caller
// that wants to restart or disable clears startErr separately.
func (m *mcpManager) stopRun() {
	m.mu.Lock()
	run := m.run
	m.run = nil
	m.running = false
	m.mu.Unlock()
	if run == nil {
		return
	}
	run.cancel()
	timer := time.NewTimer(mcpStopGrace)
	defer timer.Stop()
	select {
	case <-run.done:
	case <-timer.C:
	}
}

// apply makes the persisted settings describe the running server. Access mode
// changes are applied through Host.Refresh; listener shape changes restart.
func (m *mcpManager) apply(ctx context.Context, values mcp.SettingsValues) error {
	m.mu.Lock()
	old := m.values
	wasRunning := m.running
	m.values = values
	m.startErr = nil
	m.mu.Unlock()

	if !values.Enabled {
		m.stopRun()
		m.refreshStatus()
		return nil
	}
	httpShape := old.HTTPEnabled != values.HTTPEnabled ||
		(values.HTTPEnabled && old.HTTPAddr != values.HTTPAddr)
	if !wasRunning || httpShape {
		m.stopRun()
		err := m.startRun()
		m.refreshStatus()
		return err
	}
	if old.AccessMode != values.AccessMode {
		m.mu.Lock()
		app := m.app
		m.mu.Unlock()
		if app != nil {
			app.Host.Refresh(ctx)
		}
	}
	m.refreshStatus()
	return nil
}

// GetMCPSettings reads the persisted mcp.* values.
func (m *mcpManager) GetMCPSettings(ctx context.Context) (mcp.SettingsValues, error) {
	if m.isClosed() {
		return m.currentValues(), errMCPClosed
	}
	return mcp.LoadSettings(ctx, m.settings)
}

// SaveMCPSettings validates and persists the patch, then starts, stops or
// refreshes the server. The returned values include the attempted patch even
// when validation or startup fails, so the form can show what was submitted.
func (m *mcpManager) SaveMCPSettings(ctx context.Context, patch pages.MCPSettingsPatch) (mcp.SettingsValues, error) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	if m.isClosed() {
		return m.currentValues(), errMCPClosed
	}
	v, _ := mcp.LoadSettings(ctx, m.settings)
	if patch.Enabled != nil {
		v.Enabled = *patch.Enabled
	}
	if patch.HTTPEnabled != nil {
		v.HTTPEnabled = *patch.HTTPEnabled
	}
	if patch.HTTPAddr != nil {
		v.HTTPAddr = strings.TrimSpace(*patch.HTTPAddr)
	}
	if patch.AccessMode != nil {
		v.AccessMode = *patch.AccessMode
	}
	if patch.RetentionDays != nil {
		v.RetentionDays = *patch.RetentionDays
	}
	if err := mcp.ValidateListenAddr(v.HTTPAddr); err != nil {
		return v, err
	}
	switch v.AccessMode {
	case mcp.AccessReadOnly, mcp.AccessReadWrite:
	default:
		return v, fmt.Errorf("mcp.access_mode 必须是 %s 或 %s", mcp.AccessReadOnly, mcp.AccessReadWrite)
	}
	if v.RetentionDays < mcp.MinRetentionDays || v.RetentionDays > mcp.MaxRetentionDays {
		return v, fmt.Errorf("mcp.retention_days 取值范围 %d–%d", mcp.MinRetentionDays, mcp.MaxRetentionDays)
	}
	if err := m.persist(ctx, v); err != nil {
		return v, err
	}
	m.mu.Lock()
	m.values = v
	m.loadErr = nil
	m.mu.Unlock()
	return v, m.apply(ctx, v)
}

// persist writes the exposed mcp.* keys. Keys the UI does not own, such as the
// confirmation TTL, are left untouched.
func (m *mcpManager) persist(ctx context.Context, v mcp.SettingsValues) error {
	if m.settings == nil {
		return errMCPUnavailable
	}
	entries := []struct {
		key, value string
	}{
		{mcp.KeyEnabled, strconv.FormatBool(v.Enabled)},
		{mcp.KeyHTTPEnabled, strconv.FormatBool(v.HTTPEnabled)},
		{mcp.KeyHTTPAddr, v.HTTPAddr},
		{mcp.KeyAccessMode, v.AccessMode},
		{mcp.KeyRetentionDays, strconv.Itoa(v.RetentionDays)},
	}
	for _, e := range entries {
		if err := m.settings.Put(ctx, e.key, e.value); err != nil {
			return err
		}
	}
	return nil
}

// refreshStatus caches the module list, tool count and protocol versions. It
// is called at build and on every apply; the page's Status call stays cheap.
func (m *mcpManager) refreshStatus() {
	m.mu.Lock()
	app := m.app
	m.mu.Unlock()
	var modules, protocols []string
	count := 0
	if app != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		modules = app.Host.ReadyModules(ctx)
		cancel()
		count = len(app.Host.Names())
		protocols = app.Server.Versions()
	}
	m.mu.Lock()
	m.modules = modules
	m.protocols = protocols
	m.toolCount = count
	m.mu.Unlock()
}

// Status returns the settings snapshot and the live running/error state.
func (m *mcpManager) Status() pages.MCPStatus {
	if m == nil {
		return pages.MCPStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := pages.MCPStatus{
		Running:       m.running,
		Socket:        m.socket,
		Enabled:       m.values.Enabled,
		AccessMode:    m.values.AccessMode,
		HTTPEnabled:   m.values.HTTPEnabled,
		HTTPAddr:      m.values.HTTPAddr,
		Version:       m.version,
		Protocols:     append([]string(nil), m.protocols...),
		Modules:       append([]string(nil), m.modules...),
		ToolCount:     m.toolCount,
		WorkspaceRoot: m.workspaceRoot,
	}
	if m.values.HTTPEnabled && m.values.HTTPAddr != "" {
		st.Endpoint = "http://" + m.values.HTTPAddr + "/mcp"
	}
	err := m.startErr
	if err == nil {
		err = m.loadErr
	}
	if err == nil {
		err = m.buildErr
	}
	if err == nil {
		err = m.socketErr
	}
	if m.closed {
		err = errMCPClosed
	}
	if err != nil {
		st.Error = errtext.Text(err)
	}
	return st
}

// Tools returns the registered catalog. It is static for the workspace and
// never invokes a tool.
func (m *mcpManager) Tools() []pages.MCPToolInfo {
	m.mu.Lock()
	app := m.app
	m.mu.Unlock()
	if app == nil {
		return nil
	}
	names := app.Host.Names()
	out := make([]pages.MCPToolInfo, 0, len(names))
	for _, name := range names {
		binding, ok := app.Host.Binding(name)
		if !ok {
			continue
		}
		spec := binding.Spec
		out = append(out, pages.MCPToolInfo{
			Name:        spec.Name,
			Module:      string(spec.Module),
			Category:    string(spec.Category),
			Title:       spec.Title,
			Description: spec.Description,
			ReadOnly:    spec.Annotations.ReadOnlyHint,
			Destructive: spec.Annotations.DestructiveHint,
		})
	}
	return out
}

// Clients lists the HTTP/stdio client tokens. The views never include hashes.
func (m *mcpManager) Clients(ctx context.Context) ([]mcp.Client, error) {
	m.mu.Lock()
	app, closed := m.app, m.closed
	m.mu.Unlock()
	if closed {
		return nil, errMCPClosed
	}
	if app == nil || app.Clients == nil {
		return nil, errMCPUnavailable
	}
	return app.Clients.List(ctx)
}

// CreateClient creates one client and returns the one-time plaintext token.
func (m *mcpManager) CreateClient(ctx context.Context, name string) (mcp.Client, string, error) {
	m.mu.Lock()
	app, closed := m.app, m.closed
	m.mu.Unlock()
	if closed {
		return mcp.Client{}, "", errMCPClosed
	}
	if app == nil || app.Clients == nil {
		return mcp.Client{}, "", errMCPUnavailable
	}
	return app.Clients.Create(ctx, name, mcp.TransportHTTP)
}

// RotateClient replaces a client token and returns the new plaintext once.
func (m *mcpManager) RotateClient(ctx context.Context, id string) (string, error) {
	m.mu.Lock()
	app, closed := m.app, m.closed
	m.mu.Unlock()
	if closed {
		return "", errMCPClosed
	}
	if app == nil || app.Clients == nil {
		return "", errMCPUnavailable
	}
	return app.Clients.Rotate(ctx, id)
}

// RevokeClient removes a client and invalidates its token.
func (m *mcpManager) RevokeClient(ctx context.Context, id string) error {
	m.mu.Lock()
	app, closed := m.app, m.closed
	m.mu.Unlock()
	if closed {
		return errMCPClosed
	}
	if app == nil || app.Clients == nil {
		return errMCPUnavailable
	}
	return app.Clients.Revoke(ctx, id)
}

// SetClientEnabled enables or disables one client token.
func (m *mcpManager) SetClientEnabled(ctx context.Context, id string, enabled bool) error {
	m.mu.Lock()
	app, closed := m.app, m.closed
	m.mu.Unlock()
	if closed {
		return errMCPClosed
	}
	if app == nil || app.Clients == nil {
		return errMCPUnavailable
	}
	return app.Clients.SetEnabled(ctx, id, enabled)
}

// BridgeCommand returns the absolute researchos-mcp next to the running
// executable. When that file is missing (development builds), it falls back to
// the documented PATH name "researchos-mcp".
func (m *mcpManager) BridgeCommand() (string, error) {
	if exe, err := os.Executable(); err == nil {
		name := "researchos-mcp"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		candidate := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "researchos-mcp", nil
}

// close stops the listener and waits for an in-flight apply to leave. It runs
// before the workspace closes, because the server holds database handles.
func (m *mcpManager) close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.mu.Unlock()
	// Cancel an in-flight start first; otherwise applyMu's holder could sit in
	// its five-second start window.
	m.stopRun()
	m.applyMu.Lock()
	m.applyMu.Unlock()
	m.mu.Lock()
	m.running = false
	m.startErr = nil
	m.mu.Unlock()
}

func (m *mcpManager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func (m *mcpManager) currentValues() mcp.SettingsValues {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.values
}
