package mcpbind

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"researchos/internal/ai/output"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/settings"
	"researchos/internal/foundation/workspace"
	"researchos/internal/mcp"
	"researchos/internal/mcpfacade"
	"researchos/internal/mcpstore"
	"researchos/internal/project"
)

// WireOptions is the application composition surface for the MCP server.
type WireOptions struct {
	Workspace *workspace.Workspace
	Clock     clock.Clock
	Logger    mcp.Logger
	// Version is the application version reported by server_status.
	Version string

	Confirm  confirm.Service
	Settings *settings.Store

	AI      output.GenerationService
	Outputs output.AIOutputService

	// Facade is the SPEC-03 project allow-list.
	Facade *mcpfacade.Service
	// ProjectAdmin is only used by the in-app confirmation executor (the
	// permanent-delete path); it is never part of the tool surface.
	ProjectAdmin *project.Service
	// Runtime overrides the built-in status provider.
	Runtime mcp.Runtime
	// Limiter overrides the per-connection rate limiter. Nil uses 60/min with
	// a burst of 10 (tests raise it to exercise many calls).
	Limiter *mcp.Limiter
}

// App is the assembled MCP server.
type App struct {
	Host          *mcp.Host
	Server        *mcp.Server
	Notifier      *mcp.Notifier
	Confirmations *mcpstore.Confirmations
	Jobs          *mcpstore.Jobs
	Clients       *mcpstore.ClientService
	Store         *mcpstore.Store
	Executor      *ConfirmationExecutor
	Deps          Deps

	clock clock.Clock
}

// Wire builds the runtime stores, tool host, protocol server and confirmation
// executor. Migrations must already be registered (append migrations.MustAll()
// to workspace.Options.Migrations); Wire verifies the runtime tables exist so a
// missing registration fails loudly instead of at the first tool call.
func Wire(opts WireOptions) (*App, error) {
	if opts.Workspace == nil || opts.Workspace.Global() == nil {
		return nil, errors.New("mcpbind: wire requires an open workspace")
	}
	c := opts.Clock
	if c == nil {
		c = opts.Workspace.Clock()
	}
	if c == nil {
		c = clock.System{}
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	global := opts.Workspace.Global().DB()
	for _, table := range []string{"mcp_confirmation", "mcp_job", "mcp_client", "mcp_idempotency"} {
		var count int
		if err := global.QueryRowContext(context.Background(),
			`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			return nil, fmt.Errorf("mcpbind: inspect %s: %w", table, err)
		}
		if count == 0 {
			return nil, fmt.Errorf("mcpbind: runtime table %s is missing; register mcpstore/migrations.MustAll() before opening the workspace", table)
		}
	}

	settingsStore := opts.Settings
	if settingsStore == nil {
		settingsStore = settings.NewWorkspace(global).WithClock(c)
	}
	ttl := func(ctx context.Context) time.Duration {
		values, err := mcp.LoadSettings(ctx, settingsStore)
		if err != nil {
			return mcpstore.DefaultTTL
		}
		return values.ConfirmationTTL
	}
	store := mcpstore.New(opts.Workspace.Global(), mcpstore.Options{Clock: c, ConfirmationTTL: ttl, Logger: opts.Logger})
	confirmations := mcpstore.NewConfirmations(store)
	jobs := mcpstore.NewJobs(store)
	clients := mcpstore.NewClientService(store)

	if _, err := confirmations.RecoverAtStartup(context.Background()); err != nil {
		return nil, err
	}
	if err := jobs.RecoverAtStartup(context.Background()); err != nil {
		return nil, err
	}

	deps := Deps{
		Workspace:     opts.Workspace,
		Clock:         c,
		Logger:        opts.Logger,
		Version:       opts.Version,
		Confirm:       opts.Confirm,
		Settings:      settingsStore,
		AI:            opts.AI,
		Outputs:       opts.Outputs,
		Confirmations: confirmations,
		Jobs:          jobs,
		Facade:        opts.Facade,
		Infra:         &Infra{},
	}
	notifier := mcp.NewNotifier()
	runtimeRef := &runtimeRef{}
	deps.Runtime = runtimeRef
	host, err := mcp.NewHost(mcp.HostOptions{
		Clock:         c,
		Settings:      settingsStore,
		Broker:        confirmations,
		Jobs:          jobs,
		Idempotency:   store,
		Gate:          deps.Gate(),
		Runtime:       opts.Runtime,
		Limiter:       limiterOrDefault(opts.Limiter, c),
		Notifier:      notifier,
		Logger:        opts.Logger,
		ServerName:    "ResearchOS",
		ServerVersion: opts.Version,
		Providers:     Providers(deps),
	})
	if err != nil {
		return nil, err
	}
	runtimeRef.inner = RuntimeStatus{Host: host, Deps: deps, Version: opts.Version}
	if opts.Runtime != nil {
		runtimeRef.inner = opts.Runtime
	}
	server := mcp.NewServer(mcp.ServerOptions{
		Host:          host,
		Notifier:      notifier,
		Clock:         c,
		ServerName:    "ResearchOS",
		ServerVersion: opts.Version,
		Logger:        opts.Logger,
	})
	executor := NewConfirmationExecutor(confirmations, deps, opts.Confirm, opts.ProjectAdmin, opts.Logger)
	return &App{
		Host:          host,
		Server:        server,
		Notifier:      notifier,
		Confirmations: confirmations,
		Jobs:          jobs,
		Clients:       clients,
		Store:         store,
		Executor:      executor,
		Deps:          deps,
		clock:         c,
	}, nil
}

// Start launches the notification watcher, the local IPC listener for the stdio
// bridge, and (when mcp.http.enabled) the loopback HTTP listener. It blocks
// until ctx ends.
func (a *App) Start(ctx context.Context) error {
	values, err := mcp.LoadSettings(ctx, a.Deps.Settings)
	if err != nil {
		return err
	}
	if !values.Enabled {
		return errors.New("mcpbind: MCP server is disabled by mcp.enabled")
	}
	go a.Host.WatchReady(ctx, 5*time.Second)
	if _, err := a.PurgeRetention(ctx); err != nil && a.Deps.Logger != nil {
		a.Deps.Logger.Warn("mcp: retention sweep failed", "err", err)
	}

	socket, err := mcp.SocketPath()
	if err != nil {
		return err
	}
	key, err := mcp.LoadOrCreateKey()
	if err != nil {
		return err
	}
	_ = os.Remove(socket)
	ipcListener, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("mcpbind: listen stdio bridge: %w", err)
	}
	defer func() {
		_ = ipcListener.Close()
		_ = os.Remove(socket)
	}()
	errCh := make(chan error, 2)
	go func() {
		errCh <- mcp.ServeIPC(ctx, ipcListener, mcp.IPCServerOptions{Server: a.Server, Key: key, Clock: a.clock, Logger: a.Deps.Logger})
	}()

	var httpServer *http.Server
	if values.HTTPEnabled {
		listener, err := mcp.Listen(values.HTTPAddr)
		if err != nil {
			return err
		}
		httpServer = &http.Server{
			Handler:           mcp.NewHTTPHandler(a.Server, a.Clients, a.Deps.Logger),
			ReadHeaderTimeout: 10 * time.Second,
			MaxHeaderBytes:    64 << 10,
		}
		go func() {
			errCh <- httpServer.Serve(listener)
		}()
	}

	select {
	case <-ctx.Done():
		if httpServer != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = httpServer.Shutdown(shutdownCtx)
			cancel()
		}
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			return err
		}
		return nil
	}
}

// PurgeRetention removes settled runtime rows older than mcp.retention_days
// (SPEC-11 §5.5.1, §5.6). Activity rows are never touched.
func (a *App) PurgeRetention(ctx context.Context) (int, error) {
	values, err := mcp.LoadSettings(ctx, a.Deps.Settings)
	if err != nil {
		return 0, err
	}
	cutoff := a.clock.Now().AddDate(0, 0, -values.RetentionDays)
	return a.Store.Purge(ctx, cutoff)
}

// RuntimeStatus is the built-in server_status provider.
type RuntimeStatus struct {
	Host    *mcp.Host
	Deps    Deps
	Version string
}

// Status implements mcp.Runtime.
func (r RuntimeStatus) Status(ctx context.Context) (mcp.Status, error) {
	values, err := mcp.LoadSettings(ctx, r.Deps.Settings)
	if err != nil {
		return mcp.Status{}, err
	}
	modules := []string{}
	if r.Host != nil {
		modules = r.Host.ReadyModules(ctx)
	}
	zoteroOK := false
	if r.Deps.Workspace != nil && r.Deps.Workspace.Global() != nil {
		if status, err := r.Deps.zoteroConnection().Status(ctx); err == nil && status.DataDir != nil {
			zoteroOK = true
		}
	}
	root := ""
	if r.Deps.Workspace != nil {
		root = r.Deps.Workspace.Root()
	}
	return mcp.Status{
		AppVersion:      r.Version,
		MCPVersions:     []string{mcp.ProtocolVersion20260728, mcp.ProtocolVersion20251125},
		AccessMode:      values.AccessMode,
		WorkspaceRoot:   root,
		Modules:         modules,
		AIAvailable:     r.Deps.AI != nil,
		ZoteroAvailable: zoteroOK,
		Transports:      []string{mcp.TransportStdio, mcp.TransportHTTP},
	}, nil
}

// DefaultRuntime returns the status provider the host uses.
func (a *App) DefaultRuntime() mcp.Runtime { return a.Deps.Runtime }

func limiterOrDefault(limiter *mcp.Limiter, c clock.Clock) *mcp.Limiter {
	if limiter != nil {
		return limiter
	}
	return mcp.NewLimiter(60, 10, c)
}

// runtimeRef late-binds the status provider so it can see the assembled host.
type runtimeRef struct {
	inner mcp.Runtime
}

// Status implements mcp.Runtime.
func (r *runtimeRef) Status(ctx context.Context) (mcp.Status, error) {
	if r == nil || r.inner == nil {
		return mcp.Status{}, errors.New("mcpbind: runtime status is not wired")
	}
	return r.inner.Status(ctx)
}
