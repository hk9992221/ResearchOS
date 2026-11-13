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
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/config"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/diag"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/externalrefs"
	"researchos/internal/foundation/reconcile"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/settings"
	"researchos/internal/foundation/trash"
	fworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/ports"
	"researchos/internal/project"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
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
	r.setupApp = app.New(app.Options{WorkspaceName: "ResearchOS"})
	r.setupApp.Changed(mygo.InvalidateAll())
	r.setupServices = r.pageServices(nil, nil, r.setupApp)
	r.services = r.setupServices
	r.wizard = &pages.Wizard{Services: &r.setupServices}

	cfg, err := store.Load(ctx)
	if err == nil && cfg.WorkspaceRoot != "" {
		if openErr := r.open(ctx, cfg.WorkspaceRoot); openErr != nil {
			r.setupErr = openErr.Error()
		}
	} else if err != nil {
		r.setupErr = err.Error()
	}
	return r, nil
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
	opts := fworkspace.Options{Config: r.config, Confirm: r.confirm}
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
	a := app.New(app.Options{
		Projects:       projects,
		WorkspaceName:  workspaceName(ws.Root()),
		PersistSidebar: provider.SetSidebarCollapsed,
	})
	a.Changed(mygo.InvalidateAll())

	svc := workspace.New(ws)
	pagesService := r.pageServices(ws, svc, a)

	pageSet := shell.DefaultPages(ctx, a)
	for kind, page := range pages.System(pagesService) {
		pageSet[kind] = page
	}
	sh := shell.New(shell.Config{App: a, Theme: provider, Pages: pageSet, Ctx: ctx})
	r.mu.Lock()
	prev := r.ws
	r.ws = ws
	r.app = a
	r.shell = sh
	r.services = pagesService
	r.setupErr = ""
	r.mu.Unlock()
	mygo.InvalidateAll()()
	if prev != nil {
		_ = prev.Close()
	}
	return nil
}

// Close closes the workspace.
func (r *Runtime) Close() error {
	r.mu.Lock()
	ws := r.ws
	r.ws = nil
	r.shell = nil
	r.mu.Unlock()
	if ws == nil {
		return nil
	}
	return ws.Close()
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
		r.setSetupErr("The workspace opened, but the configuration could not be saved: " + err.Error())
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
// only the setup service is available.
func (r *Runtime) pageServices(ws *fworkspace.Workspace, appWS *workspace.Service, a *app.App) pages.Services {
	s := pages.Services{
		App:          a,
		Setup:        r,
		Config:       r.config,
		Confirm:      r.confirm,
		ChooseFolder: r.chooseFolder,
		OnMigrated:   r.onMigrated,
	}
	if ws == nil {
		return s
	}
	s.Workspace = appWS
	s.Scan = ws.Scan
	s.Trash = &systemTrash{ws: ws, confirm: r.confirm}
	s.Reconcile = &noteReconcile{ws: ws}
	s.Diagnostics = &selfCheck{ws: ws}
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
	return mg.Dialog.Open(mg.OpenDialogOptions{Title: "Select workspace folder", Directory: true})
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
		insp.Message = "The folder does not exist and will be created."
		return insp, nil
	}
	if err != nil {
		return insp, err
	}
	if !st.IsDir() {
		return insp, errors.New(abs + " is not a folder")
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
		insp.Message = "The folder is empty and can be initialized."
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
			insp.Message = "The .researchos marker exists but cannot be read as a ResearchOS marker."
			return insp, nil
		}
		if !hasGlobal {
			insp.State = pages.SetupMarkerCorrupt
			insp.Message = "The marker is present but global.db is missing."
			return insp, nil
		}
		insp.State = pages.SetupExisting
		insp.Message = "This is an existing ResearchOS workspace."
		return insp, nil
	case errors.Is(markerErr, os.ErrNotExist):
		if hasGlobal {
			insp.State = pages.SetupMarkerMissing
			insp.Message = "global.db exists but the .researchos marker is missing."
			return insp, nil
		}
		insp.State = pages.SetupForeign
		insp.Message = "The folder is not empty and is not a ResearchOS workspace."
		return insp, nil
	default:
		return insp, markerErr
	}
}

// workspaceName is the display name of a workspace root. The composition root
// derives it; UI code never handles paths itself.
func workspaceName(root string) string {
	if root == "" {
		return "ResearchOS"
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

// selfCheck adapts the diagnostic service.
type selfCheck struct{ ws *fworkspace.Workspace }

func (s *selfCheck) Run(ctx context.Context, opts diag.Options) (diag.Report, error) {
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
