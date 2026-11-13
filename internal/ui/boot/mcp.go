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
	"fmt"
	"log"
	"sync"
	"time"

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

// newSearch builds the SPEC-10 retrieval service shared by the MCP tools and
// the Literature page's post-commit index hook. Keyword recall works without
// an embedding provider; vector recall reports vector_unavailable
// (SPEC-10 §5.1).
func newSearch(ws *fworkspace.Workspace) (*search.Service, error) {
	return search.New(ws, search.Options{Clock: clock.System{}, Logger: appLogger{}})
}

// mcpService is the MCP server bound to one open workspace. A workspace switch
// stops it before the previous workspace handle is closed, because the server
// holds database handles.
type mcpService struct {
	socket string
	cancel context.CancelFunc
	done   chan struct{}

	mu  sync.Mutex
	err error
}

// startMCP wires and launches the server. A failure is recorded rather than
// returned: the desktop application stays usable without the bridge, and the
// diagnostics page reports why the server is down. searchSvc may be nil, in
// which case the SPEC-11 §2.2 rule applies and the search tools stay
// unpublished.
func startMCP(ws *fworkspace.Workspace, projects *project.Service, settingsStore *settings.Store, confirmations confirm.Service, searchSvc *search.Service) *mcpService {
	host := &mcpService{done: make(chan struct{})}
	socket, err := mcp.SocketPath()
	if err != nil {
		host.fail(err)
		return host
	}
	host.socket = socket

	facade := mcpfacade.New(mcpfacade.Options{
		Project:   projects,
		Overview:  overview.New(overview.Options{Workspace: ws}),
		Export:    export.New(export.Options{Workspace: ws}),
		Workspace: ws,
	})
	bound, err := mcpbind.Wire(mcpbind.WireOptions{
		Workspace:    ws,
		Clock:        ws.Clock(),
		Logger:       appLogger{},
		Confirm:      confirmations,
		Settings:     settingsStore,
		Facade:       facade,
		Search:       searchSvc,
		ProjectAdmin: projects,
	})
	if err != nil {
		host.fail(err)
		return host
	}

	runCtx, cancel := context.WithCancel(context.Background())
	host.cancel = cancel
	go func() {
		defer close(host.done)
		if err := bound.Start(runCtx); err != nil {
			host.fail(err)
			mygo.InvalidateAll()()
		}
	}()
	return host
}

// fail records a start-up failure. The done channel is closed when nothing was
// started, so status reports "stopped" rather than a server that never ran.
func (m *mcpService) fail(err error) {
	m.mu.Lock()
	m.err = err
	m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
	select {
	case <-m.done:
	default:
		close(m.done)
	}
}

// stop asks the server to shut down and waits for the listener to return, so
// the workspace can close its databases afterwards.
func (m *mcpService) stop() {
	if m == nil {
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	timer := time.NewTimer(mcpStopGrace)
	defer timer.Stop()
	select {
	case <-m.done:
	case <-timer.C:
	}
}

// status reports the server for the diagnostics page.
func (m *mcpService) status() pages.MCPStatus {
	if m == nil {
		return pages.MCPStatus{}
	}
	m.mu.Lock()
	err := m.err
	m.mu.Unlock()
	running := err == nil
	select {
	case <-m.done:
		running = false
	default:
	}
	st := pages.MCPStatus{Running: running, Socket: m.socket}
	if err != nil {
		st.Error = errtext.Text(err)
	}
	return st
}
