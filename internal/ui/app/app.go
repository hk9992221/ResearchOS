// Package app is the UI application-state layer of SPEC-01: the current
// navigation target, the open project, the sidebar and inspector state, the
// command palette, the global search text and the feedback queue.
//
// It holds state and calls the service layer. It never opens SQLite, never
// joins workspace paths and never draws: the AppShell reads this state and
// renders it, so the same state drives a real window and a headless render.
package app

import (
	"context"
	"sync"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/confirm"
	"researchos/internal/ports"
	"researchos/internal/project"
	"researchos/internal/ui/components"
)

// UI destinations that ports.RouteKind does not enumerate yet. Business
// modules add their pages under these kinds; the module route kinds ports
// already defines (RouteNote, RoutePaper, ...) are used as they are.
const (
	RouteHome         ports.RouteKind = "home"
	RouteProjects     ports.RouteKind = "projects"
	RouteInbox        ports.RouteKind = "inbox"
	RoutePluginMarket ports.RouteKind = "plugin_market"
	RouteLiterature   ports.RouteKind = "literature"
	RouteNotes        ports.RouteKind = "notes"
	RouteIdeas        ports.RouteKind = "ideas"
	RouteExperiments  ports.RouteKind = "experiments"
	RouteKanban       ports.RouteKind = "kanban"
	RouteWeeklyReview ports.RouteKind = "weekly_review"
	RouteDiagnostics  ports.RouteKind = "diagnostics"
	RouteMigration    ports.RouteKind = "migration"
	RouteConsistency  ports.RouteKind = "consistency"
	RouteHelp         ports.RouteKind = "help"
)

// Level is the meaning of a feedback message.
type Level int

const (
	// LevelInfo reports a completed action without a problem.
	LevelInfo Level = iota
	// LevelSuccess reports a completed operation.
	LevelSuccess
	// LevelWarning reports something the user should notice.
	LevelWarning
	// LevelDanger reports a failure.
	LevelDanger
)

// Toast is one global feedback message. It shows until it expires or the user
// dismisses it; failures stay longer than successes.
type Toast struct {
	ID      int64
	Level   Level
	Title   string
	Message string
	// Expires is when the toast disappears by itself.
	Expires time.Time
}

// Inspector is the optional detail pane of the shell (§6.6). Body renders the
// selected object; the shell provides the frame, the title and the close
// control, so modules never build a second inspector.
type Inspector struct {
	Open  bool
	Width float32
	Title string
	// Body builds the detail from the current app state. The shell calls it
	// each frame inside the pane, with that frame's component context, so it
	// must not capture frame values. It may be nil while nothing is selected.
	Body func(v *components.UI)
}

// Options configures an App.
type Options struct {
	// Projects is the project service. It may be nil in a window without a
	// workspace; project navigation then stays empty.
	Projects *project.Service
	// Confirm issues the one-time tokens that authorize destructive
	// operations (SPEC-01 §5.8). The delete wizard needs it: without it the
	// wizard shows its plan but cannot confirm.
	Confirm confirm.Service
	// WorkspaceName is the display name of the current workspace, computed by
	// the composition root. The UI never derives it from a path itself.
	WorkspaceName string
	// Clock supplies timestamps. Nil means clock.System.
	Clock clock.Clock
	// PersistSidebar stores the sidebar state, normally
	// theme.Provider.SetSidebarCollapsed. Nil keeps it in memory.
	PersistSidebar func(ctx context.Context, collapsed bool) error
	// OnChange asks the host to repaint, as mygo.InvalidateAll. It runs
	// without the App's lock and may be nil in tests.
	OnChange func()
}

// App is safe for concurrent use: a click, a background task and the view
// reading state may run at once.
type App struct {
	projects      *project.Service
	confirm       confirm.Service
	workspaceName string
	clock         clock.Clock
	persist       func(ctx context.Context, collapsed bool) error
	onChange      func()

	mu        sync.Mutex
	route     ports.Route
	proj      *project.Project
	sidebar   bool
	inspector Inspector
	palette   bool
	search    string
	toasts    []Toast
	tasks     []*Task
	taskSeq   int64
	list      []project.Project
	listOK    bool
	listErr   error
	toastSeq  int64
}

// New returns an App on the Home route with the inspector closed.
func New(o Options) *App {
	clk := o.Clock
	if clk == nil {
		clk = clock.System{}
	}
	return &App{
		projects:      o.Projects,
		confirm:       o.Confirm,
		workspaceName: o.WorkspaceName,
		clock:         clk,
		persist:       o.PersistSidebar,
		onChange:      o.OnChange,
		route:         ports.Route{Kind: RouteHome},
		inspector:     Inspector{Width: 320},
	}
}

// Route returns the current navigation target.
func (a *App) Route() ports.Route {
	a.mu.Lock()
	defer a.mu.Unlock()
	return copyRoute(a.route)
}

// Navigate makes r the current route. A project page without a project id
// takes the open project's id. Params are copied so a caller cannot mutate the
// navigation state afterwards.
func (a *App) Navigate(r ports.Route) {
	a.mu.Lock()
	if r.ProjectID == "" && a.proj != nil {
		r.ProjectID = a.proj.ID
	}
	a.route = copyRoute(r)
	a.mu.Unlock()
	a.changed()
}

// Project returns the open project, or nil.
func (a *App) Project() *project.Project {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.proj == nil {
		return nil
	}
	cp := *a.proj
	return &cp
}

// WorkspaceName returns the display name of the workspace.
func (a *App) WorkspaceName() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workspaceName
}

// SetWorkspaceName updates the display name, as after a workspace migration.
func (a *App) SetWorkspaceName(name string) {
	a.mu.Lock()
	a.workspaceName = name
	a.mu.Unlock()
	a.changed()
}

// SetProject sets the open project context without a service call, for
// composition roots and tests. OpenProject is the normal path.
func (a *App) SetProject(p *project.Project) {
	a.mu.Lock()
	if p == nil {
		a.proj = nil
	} else {
		cp := *p
		a.proj = &cp
	}
	a.mu.Unlock()
	a.changed()
}

// OpenProject opens the project through the project service and switches to
// its Overview. It returns the service error unchanged.
func (a *App) OpenProject(ctx context.Context, id string) error {
	if a.projects == nil {
		return ErrNoProjectService
	}
	sess, err := a.projects.Open(ctx, id)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.proj = &sess.Project
	a.route = ports.Route{Kind: ports.RouteOverview, ProjectID: sess.Project.ID}
	a.mu.Unlock()
	a.changed()
	return nil
}

// CreateProject runs the SPEC-03 §5.4 creation pipeline. Create already makes
// the new project the active session, so the app mirrors that outcome, switches
// to its Overview and reloads the cached list. The returned project is non-nil
// whenever the registration row exists: an ErrEventMissing companion reports a
// committed project whose Activity event is missing (§5.4 cross-database rule),
// which the caller must surface without discarding the project.
func (a *App) CreateProject(ctx context.Context, in project.CreateInput) (*project.Project, error) {
	if a.projects == nil {
		return nil, ErrNoProjectService
	}
	created, err := a.projects.Create(ctx, in)
	if created == nil {
		return nil, err
	}
	cp := *created
	a.mu.Lock()
	a.proj = &cp
	a.route = ports.Route{Kind: ports.RouteOverview, ProjectID: cp.ID}
	a.mu.Unlock()
	a.changed()
	_ = a.RefreshProjects(ctx)
	return created, err
}

// UpdateProject edits name/summary/goals/focus_md (PRJ-02, §5.3). The stored
// row is the source of truth: the open session and the cached list mirror it,
// so the shell shows the new values without a reload. A no-op update returns
// the project unchanged and writes no event.
func (a *App) UpdateProject(ctx context.Context, in project.UpdateInput) (*project.Project, error) {
	if a.projects == nil {
		return nil, ErrNoProjectService
	}
	in.Actor = ports.ActorUser
	updated, err := a.projects.Update(ctx, in)
	if err != nil {
		return nil, err
	}
	a.mirrorProject(updated)
	_ = a.RefreshProjects(ctx)
	return updated, nil
}

// SetArchived archives or restores a project (PRJ-03, §5.3). Archiving keeps
// every byte and makes the project read-only; the open session drops to
// read-only as well. A running export or migration holds the locks and the
// call fails with ErrBusy.
func (a *App) SetArchived(ctx context.Context, id string, archived bool) error {
	if a.projects == nil {
		return ErrNoProjectService
	}
	var err error
	if archived {
		err = a.projects.Archive(ctx, id, ports.ActorUser)
	} else {
		err = a.projects.Unarchive(ctx, id, ports.ActorUser)
	}
	if err != nil {
		return err
	}
	current, err := a.projects.Get(ctx, id)
	if err == nil {
		a.mirrorProject(current)
	}
	_ = a.RefreshProjects(ctx)
	return nil
}

// PrepareProjectDelete returns the read-only impact plan the delete wizard
// shows before anything is confirmed (§5.4). It changes nothing and issues no
// token.
func (a *App) PrepareProjectDelete(ctx context.Context, id string) (*project.DeletePlan, error) {
	if a.projects == nil {
		return nil, ErrNoProjectService
	}
	return a.projects.PrepareDelete(ctx, id)
}

// DeleteProject runs the confirmed permanent delete (§5.4). The token must
// come from an in-app confirmation and ConfirmName must equal the project
// name, or the service answers ErrDeleteNotConfirmed. A deleted project that
// was open leaves no session behind: the app returns to the Projects list.
func (a *App) DeleteProject(ctx context.Context, req project.PermanentDeleteRequest) (*project.DeleteResult, error) {
	if a.projects == nil {
		return nil, ErrNoProjectService
	}
	res, err := a.projects.PermanentDelete(ctx, req)
	if p := a.Project(); p != nil && p.ID == req.ProjectID {
		a.mu.Lock()
		a.proj = nil
		a.route = ports.Route{Kind: RouteProjects}
		a.mu.Unlock()
		a.changed()
	}
	_ = a.RefreshProjects(ctx)
	return res, err
}

// Confirmation returns the token service the destructive dialogs issue
// through. It is nil in a window without a workspace.
func (a *App) Confirmation() confirm.Service {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.confirm
}

// mirrorProject replaces the cached open project with the stored row, so an
// edit or a status change shows in the shell without reopening the project.
func (a *App) mirrorProject(p *project.Project) {
	if p == nil {
		return
	}
	a.mu.Lock()
	if a.proj != nil && a.proj.ID == p.ID {
		cp := *p
		a.proj = &cp
	}
	a.mu.Unlock()
	a.changed()
}

// CloseProject closes the session and returns to the Projects list.
func (a *App) CloseProject(ctx context.Context) error {
	if a.projects != nil && a.Project() != nil {
		if err := a.projects.Close(ctx, a.Project().ID); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.proj = nil
	a.route = ports.Route{Kind: RouteProjects}
	a.mu.Unlock()
	a.changed()
	return nil
}

// Projects lists the registered projects. It returns an empty slice when no
// project service is wired.
func (a *App) Projects(ctx context.Context, q project.ListQuery) ([]project.Project, error) {
	if a.projects == nil {
		return nil, nil
	}
	return a.projects.List(ctx, q)
}

// RefreshProjects loads the project list into the cache the shell renders, so
// a frame never waits on the database. Both groups load: an archived project
// stays in the list, where it can be restored or deleted (§5.3). Loaded reports
// whether a load ran; Err is its outcome.
func (a *App) RefreshProjects(ctx context.Context) error {
	var (
		list []project.Project
		err  error
	)
	if a.projects != nil {
		list, err = a.projects.List(ctx, project.ListQuery{IncludeArchived: true})
	}
	a.mu.Lock()
	a.list = list
	a.listOK = true
	a.listErr = err
	a.mu.Unlock()
	a.changed()
	return err
}

// ProjectDir returns the absolute path of a project's directory inside the
// workspace, so the shell can show or open it in the system file manager.
func (a *App) ProjectDir(id string) (string, error) {
	if a.projects == nil {
		return "", ErrNoProjectService
	}
	return a.projects.ProjectDir(id)
}

// ProjectList returns the cached project list, whether it has loaded, and the
// error of the last load.
func (a *App) ProjectList() ([]project.Project, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]project.Project(nil), a.list...), a.listOK, a.listErr
}

// SidebarCollapsed reports the persisted sidebar state.
func (a *App) SidebarCollapsed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sidebar
}

// SetSidebarCollapsed records the sidebar state and persists it. The in-memory
// state changes even when persisting fails; the error reports the write.
func (a *App) SetSidebarCollapsed(ctx context.Context, collapsed bool) error {
	a.mu.Lock()
	if a.sidebar == collapsed {
		a.mu.Unlock()
		return nil
	}
	a.sidebar = collapsed
	a.mu.Unlock()
	a.changed()
	if a.persist == nil {
		return nil
	}
	return a.persist(ctx, collapsed)
}

// ToggleSidebar flips the sidebar state.
func (a *App) ToggleSidebar(ctx context.Context) error {
	return a.SetSidebarCollapsed(ctx, !a.SidebarCollapsed())
}

// Inspector returns the inspector state.
func (a *App) Inspector() Inspector {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inspector
}

// ShowInspector opens the pane with a title and a body builder.
func (a *App) ShowInspector(title string, body func(v *components.UI)) {
	a.mu.Lock()
	a.inspector.Open = true
	a.inspector.Title = title
	a.inspector.Body = body
	a.mu.Unlock()
	a.changed()
}

// SetInspectorWidth records the width the splitter left the pane at.
func (a *App) SetInspectorWidth(w float32) {
	if w <= 0 {
		return
	}
	a.mu.Lock()
	a.inspector.Width = w
	a.mu.Unlock()
}

// HideInspector closes the pane.
func (a *App) HideInspector() {
	a.mu.Lock()
	a.inspector.Open = false
	a.mu.Unlock()
	a.changed()
}

// SetInspectorOpen opens or closes the pane without changing its content.
func (a *App) SetInspectorOpen(open bool) {
	a.mu.Lock()
	a.inspector.Open = open
	a.mu.Unlock()
	a.changed()
}

// PaletteOpen reports whether the command palette shows.
func (a *App) PaletteOpen() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.palette
}

// SetPalette opens or closes the command palette.
func (a *App) SetPalette(open bool) {
	a.mu.Lock()
	if a.palette == open {
		a.mu.Unlock()
		return
	}
	a.palette = open
	a.mu.Unlock()
	a.changed()
}

// TogglePalette flips the command palette.
func (a *App) TogglePalette() { a.SetPalette(!a.PaletteOpen()) }

// Search returns the global search text of the top bar.
func (a *App) Search() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.search
}

// SetSearch records the global search text. It does not repaint: typing
// already builds a frame.
func (a *App) SetSearch(s string) {
	a.mu.Lock()
	a.search = s
	a.mu.Unlock()
}

// Toast adds a feedback message and returns its id. Oldest messages are
// dropped beyond four, so a burst cannot cover the window.
func (a *App) Toast(level Level, title, message string) int64 {
	a.mu.Lock()
	a.toastSeq++
	t := Toast{
		ID:      a.toastSeq,
		Level:   level,
		Title:   title,
		Message: message,
		Expires: a.clock.Now().Add(toastTTL(level)),
	}
	a.toasts = append(a.toasts, t)
	if len(a.toasts) > maxToasts {
		a.toasts = append([]Toast(nil), a.toasts[len(a.toasts)-maxToasts:]...)
	}
	a.mu.Unlock()
	a.changed()
	return t.ID
}

// DismissToast removes one toast.
func (a *App) DismissToast(id int64) {
	a.mu.Lock()
	for i, t := range a.toasts {
		if t.ID == id {
			a.toasts = append(a.toasts[:i], a.toasts[i+1:]...)
			break
		}
	}
	a.mu.Unlock()
	a.changed()
}

// Toasts returns the live toasts, dropping the expired ones.
func (a *App) Toasts() []Toast {
	now := a.clock.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	live := a.toasts[:0]
	for _, t := range a.toasts {
		if now.Before(t.Expires) {
			live = append(live, t)
		}
	}
	a.toasts = live
	return append([]Toast(nil), live...)
}

// Notify asks the host to repaint without changing state, for page state the
// App does not own.
func (a *App) Notify() { a.changed() }

// Changed registers the repaint hook after construction; the composition root
// uses it once the window exists.
func (a *App) Changed(fn func()) {
	a.mu.Lock()
	a.onChange = fn
	a.mu.Unlock()
}

// changed asks for a repaint without holding the lock.
func (a *App) changed() {
	a.mu.Lock()
	fn := a.onChange
	a.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func copyRoute(r ports.Route) ports.Route {
	if r.Params == nil {
		return r
	}
	cp := make(map[string]string, len(r.Params))
	for k, v := range r.Params {
		cp[k] = v
	}
	r.Params = cp
	return r
}

// toastTTL keeps failures visible longer than successes.
func toastTTL(l Level) time.Duration {
	switch l {
	case LevelDanger:
		return 12 * time.Second
	case LevelWarning:
		return 9 * time.Second
	}
	return 6 * time.Second
}

const maxToasts = 4
