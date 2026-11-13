// Package boot is the composition root of the ResearchOS desktop app: it loads
// the configuration, opens the workspace, builds the services, the theme
// provider and the AppShell, and exposes the pure view the window draws.
//
// The same view renders in the GPU-native MyGo window and, through
// ui.NewTester, headless in tests: nothing here depends on a live window.
package boot

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	mg "github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"researchos/internal/adapters"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/config"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/settings"
	"researchos/internal/foundation/workspace"
	"researchos/internal/project"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/shell"
	"researchos/internal/ui/theme"
	"researchos/internal/ui/tokens"
)

// Runtime is the running application state: either a configured workspace
// with its shell, or the honest setup screen shown before one is chosen.
type Runtime struct {
	mu       sync.Mutex
	config   config.Store
	ws       *workspace.Workspace
	provider *theme.Provider
	app      *app.App
	shell    *shell.Shell

	setupPath string
	setupErr  string
	busy      bool
	confirm   confirm.Service

	// dialogOpen opens the native directory picker. It is a field so tests
	// can inject a chooser.
	dialogOpen func() ([]string, error)
}

// New loads the platform configuration and opens the configured workspace, if
// there is one.
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
	cfg, err := store.Load(ctx)
	if err != nil {
		r.setupErr = err.Error()
		return r, nil
	}
	if cfg.WorkspaceRoot == "" {
		return r, nil
	}
	if err := r.open(ctx, cfg.WorkspaceRoot); err != nil {
		r.setupPath = cfg.WorkspaceRoot
		r.setupErr = err.Error()
	}
	return r, nil
}

// Provider returns the theme provider, which outlives workspaces.
func (r *Runtime) Provider() *theme.Provider { return r.provider }

// App returns the shell's application state, or nil before a workspace opens.
func (r *Runtime) App() *app.App {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.app
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

// View renders one frame: the shell when a workspace is open, the setup screen
// otherwise. It applies the theme first, so the setup screen is themed too.
func (r *Runtime) View(c *ui.Context) {
	r.mu.Lock()
	sh := r.shell
	provider := r.provider
	setupPath := r.setupPath
	setupErr := r.setupErr
	busy := r.busy
	r.mu.Unlock()

	if sh != nil {
		sh.View(c)
		return
	}
	r.setupView(c, provider, setupPath, setupErr, busy)
}

// setupView is the minimal no-workspace state: it names the missing
// workspace, offers the native directory chooser and a validated manual path.
// UI-05 replaces it with the full first-run wizard.
func (r *Runtime) setupView(c *ui.Context, provider *theme.Provider, path, errText string, busy bool) {
	mygo.Apply(c, provider)
	v := components.New(c, provider.Theme())
	v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth * 1.2, Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       "Welcome to ResearchOS",
			Description: "Choose the folder that will hold your research workspace: the database, papers, projects and exports live inside it.",
		})
		if errText != "" {
			v.Banner(components.BannerOptions{
				Tone:    components.Danger,
				Title:   "The workspace could not be opened",
				Message: errText,
			})
		}
		v.Card(components.CardOptions{Title: "Workspace folder"}, func() {
			ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
				in := v.Input(&path, components.FieldOptions{
					Label:       "Workspace path",
					Description: "A new folder is initialized; an existing workspace is opened as it is.",
					Placeholder: "F:/ResearchOS-Workspace",
					Disabled:    busy,
				})
				if in.Changed() {
					r.setSetupPath(path)
				}
				ui.Row(v.C).Gap(tokens.ComponentGap).Children(func() {
					v.Button("Choose folder…", components.ButtonOptions{
						Variant: components.VariantPrimary,
						Icon:    icons.FolderOpen,
						Loading: busy,
						OnClick: func() { r.chooseFolder() },
					})
					v.Button("Use this folder", components.ButtonOptions{
						Variant:  components.VariantSecondary,
						Disabled: busy,
						OnClick:  func() { r.useWorkspace(path) },
					})
				})
				if busy {
					v.Progress(components.ProgressOptions{Value: -1, Label: "Opening the workspace"})
				}
			})
		})
	}})
}

// chooseFolder opens the operating system's directory chooser. It runs from
// the button's click action, after the frame is built.
func (r *Runtime) chooseFolder() {
	chosen, err := r.dialogOpen()
	if err != nil {
		r.fail("The folder chooser failed: " + err.Error())
		return
	}
	if len(chosen) == 0 {
		return
	}
	r.useWorkspace(chosen[0])
}

// dialogOpen is the operating system's directory picker, kept behind a
// variable so the setup screen stays testable without a window.
var dialogOpen = func() ([]string, error) {
	return mg.Dialog.Open(mg.OpenDialogOptions{
		Title:     "Select workspace folder",
		Directory: true,
	})
}

// useWorkspace opens or initializes a workspace off the UI thread.
func (r *Runtime) useWorkspace(path string) {
	if path == "" {
		r.fail("Choose a folder first.")
		return
	}
	r.setSetupPath(path)
	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		return
	}
	r.busy = true
	r.setupPath = path
	r.setupErr = ""
	r.mu.Unlock()
	mygo.InvalidateAll()()

	go func() {
		ctx := context.Background()
		if err := r.open(ctx, path); err != nil {
			r.finishSetup(err.Error())
			return
		}
		cfg, err := r.config.Load(ctx)
		if err != nil {
			r.finishSetup(err.Error())
			return
		}
		cfg.SchemaVersion = 1
		cfg.WorkspaceRoot = path
		if err := r.config.Save(ctx, cfg); err != nil {
			r.finishSetup("The workspace opened, but the configuration could not be saved: " + err.Error())
			return
		}
		r.finishSetup("")
	}()
}

// UseWorkspace is useWorkspace for callers that want to wait, as tests do.
func (r *Runtime) UseWorkspace(ctx context.Context, path string) error {
	if err := r.open(ctx, path); err != nil {
		r.finishSetup(err.Error())
		return err
	}
	cfg, err := r.config.Load(ctx)
	if err != nil {
		return err
	}
	cfg.SchemaVersion = 1
	cfg.WorkspaceRoot = path
	if err := r.config.Save(ctx, cfg); err != nil {
		return err
	}
	r.finishSetup("")
	return nil
}

// open opens an existing workspace, or initializes the folder when it is not a
// workspace yet.
func (r *Runtime) open(ctx context.Context, path string) error {
	opts := workspace.Options{
		Config:  r.config,
		Confirm: r.confirm,
	}
	ws, err := workspace.Open(ctx, path, opts)
	if errors.Is(err, errs.ErrNotWorkspace) {
		ws, err = workspace.Init(ctx, path, opts)
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

	trash := adapters.NewTrash(ws, r.confirm, clock.System{})
	projects := project.New(project.Options{Workspace: ws, Settings: settingsStore, Trash: trash})
	a := app.New(app.Options{
		Projects:       projects,
		WorkspaceName:  workspaceName(ws.Root()),
		PersistSidebar: provider.SetSidebarCollapsed,
	})
	a.Changed(mygo.InvalidateAll())
	sh := shell.New(shell.Config{
		App:   a,
		Theme: provider,
		Pages: shell.DefaultPages(ctx, a),
		Ctx:   ctx,
	})

	r.mu.Lock()
	prev := r.ws
	r.ws = ws
	r.app = a
	r.shell = sh
	r.setupErr = ""
	r.busy = false
	r.mu.Unlock()
	mygo.InvalidateAll()()
	if prev != nil {
		_ = prev.Close()
	}
	return nil
}

// setSetupPath keeps the typed path across frames.
func (r *Runtime) setSetupPath(path string) {
	r.mu.Lock()
	r.setupPath = path
	r.mu.Unlock()
}

// finishSetup records the outcome and repaints.
func (r *Runtime) finishSetup(errText string) {
	r.mu.Lock()
	r.busy = false
	r.setupErr = errText
	r.mu.Unlock()
	mygo.InvalidateAll()()
}

// fail shows an error on the setup screen.
func (r *Runtime) fail(errText string) {
	r.finishSetup(errText)
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
