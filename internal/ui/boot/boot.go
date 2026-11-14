// Package boot is the composition root of the ResearchOS desktop app: it loads
// the configuration, opens the workspace, builds the services, the theme
// provider and the AppShell, and exposes the pure view the window draws.
//
// The same view renders in the GPU-native MyGo window and, through
// ui.NewTester, headless in tests: nothing here depends on a live window.
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	mg "github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"researchos/internal/adapters"
	"researchos/internal/ai/wire"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/config"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/diag"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/externalrefs"
	"researchos/internal/foundation/reconcile"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/settings"
	"researchos/internal/foundation/storage"
	"researchos/internal/foundation/trash"
	fworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/project"
	"researchos/internal/search"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/pages"
	"researchos/internal/ui/shell"
	"researchos/internal/ui/theme"
	"researchos/internal/workspace"
)

// Runtime is the running application state: a configured workspace with its
// shell, or the first-run wizard before one is chosen.
type Runtime struct {
	mu       sync.Mutex
	config   config.Store
	ws       *fworkspace.Workspace
	provider *theme.Provider
	app      *app.App
	shell    *shell.Shell
	services pages.Services
	mcp      *mcpService
	// search and literature are the SPEC-10 and SPEC-04 services of the open
	// workspace; both hold database handles and are released on a switch.
	search     *search.Service
	literature *literatureModule
	// ai is the SPEC-02 stack of the open workspace. It borrows global.db, so
	// dropping the reference on a switch releases it with the workspace; a
	// failed build leaves it nil and every module degrades (see ai.go).
	ai *aiModule

	// setupApp drives the wizard's tasks before a workspace exists.
	setupApp *app.App
	wizard   *pages.Wizard

	confirm       confirm.Service
	dialogOpen    func() ([]string, error)
	setupErr      string
	setupServices pages.Services
}

// New loads the platform configuration and starts the wizard or the shell.
func New(ctx context.Context) (*Runtime, error) {
	store, err := config.NewStore()
	if err != nil {
		return nil, err
	}
	return NewWithConfig(ctx, store)
}

// NewWithConfig is New with an injected configuration store, the seam tests
// use.
func NewWithConfig(ctx context.Context, store config.Store) (*Runtime, error) {
	r := &Runtime{
		config:     store,
		provider:   theme.New(),
		confirm:    confirm.New(confirm.Options{}),
		dialogOpen: dialogOpen,
	}

	// The language is settled before the wizard or the shell builds its first
	// frame: pages read i18n.T while they render, and the configuration is
	// the only source of it. A corrupt configuration falls back to the
	// operating system, as a language setting must not keep the app from
	// starting.
	cfg, loadErr := store.Load(ctx)
	if loadErr == nil {
		i18n.SetLanguage(cfg.Language)
	} else {
		i18n.SetLanguage("")
	}

	r.setupApp = app.New(app.Options{WorkspaceName: i18n.T("app.name")})
	r.setupApp.Changed(mygo.InvalidateAll())
	r.setupServices = r.pageServices(nil, nil, r.setupApp, nil, nil, nil, nil, nil, nil)
	r.services = r.setupServices
	r.wizard = &pages.Wizard{Services: &r.setupServices}

	if loadErr == nil && cfg.WorkspaceRoot != "" {
		if openErr := r.open(ctx, cfg.WorkspaceRoot); openErr != nil {
			r.setupErr = openErr.Error()
		}
	} else if loadErr != nil {
		r.setupErr = loadErr.Error()
	}
	return r, nil
}

// Flush writes the note editor's unsaved buffer before the window closes.
// The window's close callback runs it on the main thread (SPEC-05 §5.7); it
// runs synchronously, because a task would not finish before the process
// exits.
func (r *Runtime) Flush() {
	r.mu.Lock()
	flush := r.services.NoteFlush
	r.mu.Unlock()
	flush.Run()
}

// Provider returns the theme provider, which outlives workspaces.
func (r *Runtime) Provider() *theme.Provider { return r.provider }

// App returns the shell's application state, or the wizard's state before a
// workspace opens.
func (r *Runtime) App() *app.App {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.app != nil {
		return r.app
	}
	return r.setupApp
}

// WorkspaceRoot returns the open workspace root, or "".
func (r *Runtime) WorkspaceRoot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ws == nil {
		return ""
	}
	return r.ws.Root()
}

// View renders one frame: the shell when a workspace is open, the first-run
// wizard otherwise.
func (r *Runtime) View(c *ui.Context) {
	r.mu.Lock()
	sh := r.shell
	provider := r.provider
	wizard := r.wizard
	r.mu.Unlock()

	if sh != nil {
		sh.View(c)
		return
	}
	mygo.Apply(c, provider)
	if wizard == nil {
		return
	}
	wizard.Page(components.New(c, provider.Theme()), ports.Route{})
}

// servicesFor returns the current page services.
func (r *Runtime) servicesFor() pages.Services {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.services
}

// open opens an existing workspace, or initializes the folder when it is not a
// workspace yet, then installs the shell.
func (r *Runtime) open(ctx context.Context, path string) error {
	// The MCP listener holds the database handles of the workspace it was
	// started with, so it must be released before a new one opens.
	r.stopMCP()

	migrations, err := moduleMigrations()
	if err != nil {
		return err
	}
	opts := fworkspace.Options{Config: r.config, Confirm: r.confirm, Migrations: migrations}
	ws, err := fworkspace.Open(ctx, path, opts)
	if errors.Is(err, errs.ErrNotWorkspace) {
		ws, err = fworkspace.Init(ctx, path, opts)
	}
	if err != nil {
		return err
	}

	provider := r.provider
	settingsStore := settings.NewWorkspace(ws.Global().DB())
	if err := provider.Load(ctx, settingsStore); err != nil {
		_ = ws.Close()
		return err
	}

	trashAdapter := adapters.NewTrash(ws, r.confirm, clock.System{})
	projects := project.New(project.Options{Workspace: ws, Settings: settingsStore, Trash: trashAdapter})
	// The SPEC-02 stack is optional: without it every module keeps its manual
	// path and reports AI as disabled, so a failed build must not keep the
	// workspace from opening.
	var ai *aiModule
	if stack, aiErr := wire.New(wire.Options{
		Global:            ws.Global().DB(),
		Clock:             ws.Clock(),
		WorkspaceSettings: settingsStore,
		Logger:            appLogger{},
	}); aiErr != nil {
		appLogger{}.Warn("boot: AI 服务栈不可用", "err", aiErr)
	} else {
		ai = newAIModule(ws, stack)
	}
	a := app.New(app.Options{
		Projects:       projects,
		WorkspaceName:  workspaceName(ws.Root()),
		PersistSidebar: provider.SetSidebarCollapsed,
	})
	a.Changed(mygo.InvalidateAll())

	svc := workspace.New(ws)
	searchSvc, searchErr := newSearch(ws)
	if searchErr != nil {
		// Retrieval is an optional collaborator: without it the SPEC-10 tools
		// stay unpublished and imports skip the index hook, but every other
		// module still works (§SPEC-11 2.2).
		appLogger{}.Warn("boot: 检索模块不可用", "err", searchErr)
	}
	lit := newLiteratureModule(ws, searchSvc)
	pagesService := r.pageServices(ws, svc, a, migrations, lit, lit, newNotesModule(ws), ai, settingsStore)
	host := startMCP(ws, projects, settingsStore, r.confirm, searchSvc, ai)

	pageSet := shell.DefaultPages(ctx, a)
	for kind, page := range pages.System(pagesService) {
		pageSet[kind] = page
	}
	for kind, page := range pages.Research(pagesService) {
		pageSet[kind] = page
	}
	sh := shell.New(shell.Config{App: a, Theme: provider, Pages: pageSet, Ctx: ctx})
	r.mu.Lock()
	prev, prevSearch, prevLit := r.ws, r.search, r.literature
	r.ws = ws
	r.app = a
	r.shell = sh
	r.services = pagesService
	r.mcp = host
	r.search = searchSvc
	r.literature = lit
	r.ai = ai
	r.setupErr = ""
	r.mu.Unlock()
	mygo.InvalidateAll()()
	if prev != nil {
		// The module services hold handles of the previous workspace's
		// databases, so they close before the workspace does. The AI stack
		// borrows global.db and owns no handle of its own: dropping the
		// reference above releases it with the workspace.
		_ = prevLit.Close()
		if prevSearch != nil {
			_ = prevSearch.Close()
		}
		_ = prev.Close()
	}
	return nil
}

// Close stops the MCP server and the module services, then closes the
// workspace.
func (r *Runtime) Close() error {
	r.stopMCP()
	r.mu.Lock()
	ws := r.ws
	lit := r.literature
	searchSvc := r.search
	r.ws = nil
	r.shell = nil
	r.mcp = nil
	r.literature = nil
	r.search = nil
	r.ai = nil
	r.mu.Unlock()
	_ = lit.Close()
	if searchSvc != nil {
		_ = searchSvc.Close()
	}
	if ws == nil {
		return nil
	}
	return ws.Close()
}

// stopMCP releases the MCP listener of the previous workspace, if any. It runs
// before that workspace's handles are closed, because the server queries them.
func (r *Runtime) stopMCP() {
	r.mu.Lock()
	host := r.mcp
	r.mcp = nil
	r.mu.Unlock()
	host.stop()
}

// mcpStatus implements the diagnostics view of the server; nil host means no
// workspace is open, which the page reports as stopped without an error.
func (r *Runtime) mcpStatus() pages.MCPStatus {
	r.mu.Lock()
	host := r.mcp
	r.mu.Unlock()
	return host.status()
}

// Inspect implements pages.SetupAPI.
func (r *Runtime) Inspect(ctx context.Context, root string) (pages.SetupInspection, error) {
	return inspectFolder(root)
}

// Initialize implements pages.SetupAPI.
func (r *Runtime) Initialize(ctx context.Context, root string) error {
	return r.openAndSave(ctx, root)
}

// Open implements pages.SetupAPI.
func (r *Runtime) Open(ctx context.Context, root string) error {
	return r.openAndSave(ctx, root)
}

// CurrentRoot implements pages.SetupAPI.
func (r *Runtime) CurrentRoot() string { return r.WorkspaceRoot() }

// UseWorkspace opens or initializes a workspace and saves the configuration.
// It is the synchronous seam tests use.
func (r *Runtime) UseWorkspace(ctx context.Context, path string) error {
	return r.openAndSave(ctx, path)
}

func (r *Runtime) openAndSave(ctx context.Context, root string) error {
	if err := r.open(ctx, root); err != nil {
		r.setSetupErr(err.Error())
		return err
	}
	if err := r.saveConfig(ctx, root); err != nil {
		r.setSetupErr(i18n.Tf("boot.error.config_not_saved", map[string]string{"error": errtext.Text(err)}))
		return err
	}
	r.setSetupErr("")
	return nil
}

func (r *Runtime) saveConfig(ctx context.Context, root string) error {
	cfg, err := r.config.Load(ctx)
	if err != nil {
		return err
	}
	cfg.SchemaVersion = 1
	cfg.WorkspaceRoot = root
	return r.config.Save(ctx, cfg)
}

func (r *Runtime) setSetupErr(text string) {
	r.mu.Lock()
	r.setupErr = text
	r.mu.Unlock()
	mygo.InvalidateAll()()
}

// pageServices builds the service set the pages use. Before a workspace opens
// only the setup service is available. migrations is the registered module band
// so the self check compares against the real target version, and the MCP
// status reaches the diagnostics page through the runtime.
func (r *Runtime) pageServices(ws *fworkspace.Workspace, appWS *workspace.Service, a *app.App, migrations map[storage.Kind][]storage.Migration, lit pages.LiteratureAPI, paperAPI pages.PaperAPI, noteAPI pages.NotesAPI, ai *aiModule, settingsStore *settings.Store) pages.Services {
	s := pages.Services{
		App:          a,
		Setup:        r,
		Config:       r.config,
		Confirm:      r.confirm,
		ChooseFolder: r.chooseFolder,
		OnMigrated:   r.onMigrated,
		MCPStatus:    r.mcpStatus,
		// The notes page installs its editor's close-time flush here.
		NoteFlush: &pages.NoteFlush{},
	}
	if ws == nil {
		return s
	}
	s.Workspace = appWS
	s.Scan = ws.Scan
	s.Literature = lit
	s.Paper = paperAPI
	s.Notes = noteAPI
	s.Kanban = newKanbanModule(ws)
	// The stack is nil when the AI build failed: every module then runs its
	// documented manual path instead of failing construction. ready answers the
	// local "is a chat provider assigned" check, so a workspace without one
	// shows the disabled entry instead of an enabled one that fails.
	stack := ai.stackOrNil()
	ready := ai.chatReady
	s.Experiments = newExperimentsModule(ws, stack, ready)
	s.Weekly = newWeeklyModule(ws, stack, settingsStore, ready)
	s.Ideas = newIdeasModule(ws, stack, ready)
	s.Trash = &systemTrash{ws: ws, confirm: r.confirm}
	s.Reconcile = &noteReconcile{ws: ws}
	s.Diagnostics = &selfCheck{ws: ws, migrations: migrations}
	s.External = &externalRefs{ws: ws}
	return s
}

// onMigrated updates the display name and the configuration after a migration.
func (r *Runtime) onMigrated(ctx context.Context, newRoot string) {
	if r.app != nil {
		r.app.SetWorkspaceName(workspaceName(newRoot))
	}
	_ = r.saveConfig(ctx, newRoot)
}

// chooseFolder opens the operating system's directory picker. It runs from a
// click action, after the frame is built.
func (r *Runtime) chooseFolder() (string, error) {
	chosen, err := r.dialogOpen()
	if err != nil || len(chosen) == 0 {
		return "", err
	}
	return chosen[0], nil
}

// dialogOpen is the native directory picker, kept behind a variable so the
// wizard stays testable without a window.
var dialogOpen = func() ([]string, error) {
	return mg.Dialog.Open(mg.OpenDialogOptions{Title: i18n.T("boot.dialog.select_workspace"), Directory: true})
}

// inspectFolder classifies a candidate folder read-only, using the marker and
// the global database, without opening or creating anything.
func inspectFolder(root string) (pages.SetupInspection, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return pages.SetupInspection{}, err
	}
	insp := pages.SetupInspection{Root: abs}
	st, err := os.Stat(abs)
	if errors.Is(err, os.ErrNotExist) {
		insp.State = pages.SetupEmpty
		insp.Message = i18n.T("boot.inspect.not_exist")
		return insp, nil
	}
	if err != nil {
		return insp, err
	}
	if !st.IsDir() {
		return insp, errors.New(i18n.Tf("boot.error.not_a_folder", map[string]string{"path": abs}))
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		return insp, err
	}
	nonEmpty := false
	for _, e := range entries {
		switch e.Name() {
		case ".DS_Store", "desktop.ini", "Thumbs.db":
			continue
		}
		nonEmpty = true
		break
	}
	if !nonEmpty {
		insp.State = pages.SetupEmpty
		insp.Message = i18n.T("boot.inspect.empty")
		return insp, nil
	}

	marker := filepath.Join(abs, fworkspace.MarkerName)
	globalDB := filepath.Join(abs, "global.db")
	markerBody, markerErr := os.ReadFile(marker)
	globalInfo, globalErr := os.Stat(globalDB)
	hasGlobal := globalErr == nil && !globalInfo.IsDir()

	switch {
	case markerErr == nil:
		var m struct {
			App           string `json:"app"`
			SchemaVersion int    `json:"schema_version"`
		}
		if json.Unmarshal(markerBody, &m) != nil || m.App == "" || m.SchemaVersion <= 0 {
			insp.State = pages.SetupMarkerCorrupt
			insp.Message = i18n.T("boot.inspect.marker_corrupt")
			return insp, nil
		}
		if !hasGlobal {
			insp.State = pages.SetupMarkerCorrupt
			insp.Message = i18n.T("boot.inspect.global_missing")
			return insp, nil
		}
		insp.State = pages.SetupExisting
		insp.Message = i18n.T("boot.inspect.existing")
		return insp, nil
	case errors.Is(markerErr, os.ErrNotExist):
		if hasGlobal {
			insp.State = pages.SetupMarkerMissing
			insp.Message = i18n.T("boot.inspect.marker_missing")
			return insp, nil
		}
		insp.State = pages.SetupForeign
		insp.Message = i18n.T("boot.inspect.foreign")
		return insp, nil
	default:
		return insp, markerErr
	}
}

// workspaceName is the display name of a workspace root. The composition root
// derives it; UI code never handles paths itself.
func workspaceName(root string) string {
	if root == "" {
		return i18n.T("app.name")
	}
	base := filepath.Base(root)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return root
	}
	return base
}

// systemTrash adapts the project trash to the page interface. It runs in the
// composition root, so no UI code ever holds a database handle.
type systemTrash struct {
	ws      *fworkspace.Workspace
	confirm confirm.Service
}

func (t *systemTrash) List(ctx context.Context, projectID string) ([]pages.TrashItem, error) {
	adapter := adapters.NewTrash(t.ws, t.confirm, clock.System{})
	items, err := adapter.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]pages.TrashItem, len(items))
	for i, it := range items {
		out[i] = pages.TrashItem{Type: it.EntityType, ID: it.EntityID, Title: it.Title, DeletedAt: it.DeletedAt}
	}
	return out, nil
}

func (t *systemTrash) Restore(ctx context.Context, projectID string, refs []pages.TrashRef) error {
	adapter := adapters.NewTrash(t.ws, t.confirm, clock.System{})
	for _, r := range refs {
		if err := adapter.Restore(ctx, projectID, r.Type, r.ID, ports.ActorUser); err != nil {
			return err
		}
	}
	return nil
}

func (t *systemTrash) Purge(ctx context.Context, projectID string, refs []pages.TrashRef, token confirm.Token) error {
	adapter := adapters.NewTrash(t.ws, t.confirm, clock.System{})
	items := make([]ports.TrashRef, len(refs))
	for i, r := range refs {
		items[i] = ports.TrashRef{EntityType: r.Type, EntityID: r.ID}
	}
	return adapter.Purge(ctx, projectID, items, token)
}

func (t *systemTrash) Empty(ctx context.Context, projectID string, token confirm.Token) (int, error) {
	handle, err := t.ws.Project(ctx, projectID)
	if err != nil {
		return 0, err
	}
	svc := trash.New(projectID, handle.DB(), t.ws.Paths(), handle.Events(), t.confirm, clock.System{})
	res, err := svc.Empty(ctx, trash.EmptyRequest{Confirm: token, Actor: "user"})
	if err != nil {
		return 0, err
	}
	return res.Purged, nil
}

// noteReconcile adapts the consistency service.
type noteReconcile struct{ ws *fworkspace.Workspace }

func (n *noteReconcile) Scan(ctx context.Context, projectID string) (reconcile.Report, error) {
	return reconcile.Scan(ctx, n.ws, projectID)
}

func (n *noteReconcile) Apply(ctx context.Context, projectID string, actions []reconcile.Action) (reconcile.ApplyResult, error) {
	return reconcile.Apply(ctx, n.ws, projectID, actions)
}

// selfCheck adapts the diagnostic service. The registered module band is
// injected here so the page does not have to know about migrations: without it
// diag would compare against the foundation baseline only (§4.8.1).
type selfCheck struct {
	ws         *fworkspace.Workspace
	migrations map[storage.Kind][]storage.Migration
}

func (s *selfCheck) Run(ctx context.Context, opts diag.Options) (diag.Report, error) {
	opts.Migrations = s.migrations
	return diag.Run(ctx, s.ws, opts)
}

// externalRefs adapts the external-reference service.
type externalRefs struct{ ws *fworkspace.Workspace }

func (e *externalRefs) CheckOwner(ctx context.Context, projectID string, ownerType, ownerID string) ([]externalrefs.Status, error) {
	handle, err := e.ws.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	svc := externalrefs.New(handle.DB(), handle.Events(), clock.System{})
	return svc.CheckOwner(ctx, projectID, schema.EntityType(ownerType), ownerID)
}

func (e *externalRefs) Relocate(ctx context.Context, req externalrefs.RelocateRequest) (externalrefs.RelocateResult, error) {
	handle, err := e.ws.Project(ctx, req.ProjectID)
	if err != nil {
		return externalrefs.RelocateResult{}, err
	}
	svc := externalrefs.New(handle.DB(), handle.Events(), clock.System{})
	return svc.Relocate(ctx, req, "user")
}
