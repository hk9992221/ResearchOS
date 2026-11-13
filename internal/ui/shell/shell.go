// Package shell builds the ResearchOS AppShell: the single frame every page
// renders inside (ui_plan.md §15). It owns the application sidebar, the top
// toolbar, the main content area, the optional inspector, the command palette
// and the global feedback toasts.
//
// View is a pure view function: given the app state and the theme provider it
// draws one frame, so the same shell renders in a MyGo window and headless
// through ui.NewTester / ui.Render. It never opens a database and never joins
// workspace paths.
package shell

import (
	"context"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/egoist/mygo/ui"

	"researchos/internal/ports"
	"researchos/internal/project"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/theme"
	"researchos/internal/ui/tokens"
)

// Page builds the content of one route inside the shell's main area. The
// shell already provides the sidebar, toolbar and inspector, so a page builds
// only its own PageHeader and content.
type Page func(v *components.UI, r ports.Route)

// Config binds the shell to the app state, the theme provider and the page
// registry.
type Config struct {
	App   *app.App
	Theme *theme.Provider
	Pages map[ports.RouteKind]Page
	// Ctx is used by actions that call services. Nil means
	// context.Background.
	Ctx context.Context
}

// Shell is the application frame. Create it once and give Shell.View to the
// window's Content: the frame, the navigation and the toolbar stay the same
// across page switches, and only the main content area rebuilds.
type Shell struct {
	app      *app.App
	provider *theme.Provider
	pages    map[ports.RouteKind]Page
	ctx      context.Context

	search    ui.Handle
	crumb     int
	mainSize  float32
	lastTotal float32

	paletteQuery string
	paletteSel   int
	projectsOnce atomic.Bool
}

// New builds a shell. App and Theme are required: without them the frame
// cannot resolve navigation or design tokens.
func New(cfg Config) *Shell {
	if cfg.App == nil || cfg.Theme == nil {
		panic("shell: Config needs App and Theme")
	}
	ctx := cfg.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return &Shell{
		app:      cfg.App,
		provider: cfg.Theme,
		pages:    cfg.Pages,
		ctx:      ctx,
		crumb:    -1,
	}
}

// App returns the application state the shell renders.
func (s *Shell) App() *app.App { return s.app }

// View renders one frame. It applies the resolved theme first, as every
// component expects (mygo.Apply), then builds the shell around the current
// page.
func (s *Shell) View(c *ui.Context) {
	mygo.Apply(c, s.provider)
	v := components.New(c, s.provider.Theme())
	v.Ctx = s.ctx

	width, _ := c.Size()
	s.handleKeys(c)
	s.loadProjectsIfNeeded()

	narrow := width < breakpointNormal
	collapsed := s.app.SidebarCollapsed() || width < breakpointNormal
	inspector := s.app.Inspector()
	showInspector := inspector.Open && width >= breakpointNormal
	sidebarWidth := s.sidebarWidth(c, collapsed)

	ui.Row(c).Fill().Children(func() {
		s.sidebar(v, collapsed, sidebarWidth)
		ui.Box(c).Width(1).FillHeight().Background(mygo.Color(v.T.Palette.BorderDefault))
		ui.Column(c).Fill().Children(func() {
			s.topBar(v, narrow)
			ui.Divider(c)
			s.taskStrip(v)
			if showInspector {
				s.splitArea(v, width-sidebarWidth, inspector)
			} else {
				s.content(v)
			}
		})
	})

	s.palette(v)
	s.toasts(v)
}

// sidebarWidth animates the navigation width between its two designed sizes.
// MyGo's Animate respects the desktop's reduced-motion setting and asks for
// frames while the width moves.
func (s *Shell) sidebarWidth(c *ui.Context, collapsed bool) float32 {
	target := components.SidebarWidth
	if collapsed {
		target = components.SidebarCollapsedWidth
	}
	probe := ui.Box(c)
	return probe.Animate("shell:sidebar-width", target, tokens.PanelDuration)
}

// sidebar builds the application navigation (§6.3): the workspace switcher,
// the global entries, the project entries when a project is open, and the
// bottom entries. Labels hide in the collapsed rail; icons, tooltips and
// accessible names remain.
func (s *Shell) sidebar(v *components.UI, collapsed bool, width float32) {
	p := v.T.Palette
	bar := ui.Column(v.C).
		FillHeight().
		Width(width).
		Padding(tokens.Space2, tokens.Space2).
		Gap(tokens.SectionGap).
		Background(mygo.Color(p.BgSubtle)).
		Clip()
	bar.Children(func() {
		// Workspace switcher: the workspace is fixed for a window, so this
		// identifies it and returns Home.
		workspace := v.NavItem(components.NavItemOptions{
			ID:        "workspace",
			Icon:      icons.Overview,
			Label:     s.app.WorkspaceName(),
			Collapsed: collapsed,
			OnClick:   func() { s.navigate(ports.Route{Kind: app.RouteHome}) },
		})
		_ = workspace

		ui.Column(v.C).FillHeight().Gap(tokens.SectionGap).Children(func() {
			v.NavSection("Workspace", collapsed, func() {
				s.navItems(v, collapsed, []navEntry{
					{app.RouteHome, icons.Home, "Home", false},
					{app.RouteProjects, icons.Projects, "Projects", false},
					{app.RouteInbox, icons.Inbox, "Inbox", false},
					{app.RoutePluginMarket, icons.PluginMarket, "Plugin Market", false},
				})
			})

			if proj := s.app.Project(); proj != nil {
				v.NavSection("Project", collapsed, func() {
					s.navItems(v, collapsed, []navEntry{
						{ports.RouteOverview, icons.Overview, "Overview", true},
						{app.RouteLiterature, icons.Literature, "Literature", true},
						{app.RouteNotes, icons.Note, "Research Notes", true},
						{app.RouteIdeas, icons.Ideas, "Ideas", true},
						{app.RouteExperiments, icons.Experiments, "Experiments", true},
						{app.RouteKanban, icons.Kanban, "Kanban", true},
						{app.RouteWeeklyReview, icons.WeeklyReview, "Weekly Review", true},
					})
				})
			}

			v.Spacer()

			ui.Column(v.C).Gap(tokens.Space1).Children(func() {
				s.navItems(v, collapsed, []navEntry{
					{ports.RouteSettings, icons.Settings, "Settings", false},
					{app.RouteHelp, icons.Help, "Help / About", false},
				})
			})
		})
	})
}

// navEntry is one destination of the sidebar.
type navEntry struct {
	kind    ports.RouteKind
	icon    *icons.Icon
	label   string
	project bool
}

// navItems builds a group of entries, marking the current route selected.
func (s *Shell) navItems(v *components.UI, collapsed bool, entries []navEntry) {
	current := s.route()
	for _, e := range entries {
		e := e
		selected := current.Kind == e.kind
		v.NavItem(components.NavItemOptions{
			ID:        string(e.kind),
			Icon:      e.icon,
			Label:     e.label,
			Selected:  selected,
			Collapsed: collapsed,
			OnClick: func() {
				r := ports.Route{Kind: e.kind}
				if e.project {
					if p := s.app.Project(); p != nil {
						r.ProjectID = p.ID
					}
				}
				s.navigate(r)
			},
		})
	}
}

// topBar builds the 52px toolbar (§6.4): sidebar toggle, breadcrumb, global
// search and the command palette entry. It stays minimal on purpose.
func (s *Shell) topBar(v *components.UI, narrow bool) {
	v.TopBar(func() {
		v.IconButton(icons.Sidebar, "Toggle sidebar", components.IconButtonOptions{
			Variant: components.VariantGhost,
			Size:    components.SizeSmall,
			OnClick: func() {
				_ = s.app.ToggleSidebar(s.ctx)
			},
		})

		items, kinds := s.breadcrumb()
		if len(items) > 0 {
			last := len(items) - 1
			s.crumb = last
			chain := v.Breadcrumb(items, &s.crumb)
			if chain.Changed() && s.crumb >= 0 && s.crumb < last {
				s.navigate(kinds[s.crumb])
			}
		}

		v.Spacer()

		if !narrow {
			query := s.app.Search()
			field := ui.SearchField(v.C, &query).
				Width(260).
				Placeholder("Search (Ctrl+K)").
				Label("Global search")
			field.Bind(&s.search)
			if field.Changed() {
				s.app.SetSearch(query)
			}
			if field.Submitted() {
				s.paletteQuery = query
				s.app.SetPalette(true)
			}
		}

		v.IconButton(icons.Command, "Command palette", components.IconButtonOptions{
			Variant: components.VariantGhost,
			Size:    components.SizeSmall,
			OnClick: func() { s.togglePalette() },
		})

		// Theme control: shows the current mode, so the state is readable
		// without an icon that could be mistaken for a system setting.
		v.Button(themeLabel(s.provider.Mode()), components.ButtonOptions{
			Variant: components.VariantGhost,
			Size:    components.SizeSmall,
			Tooltip: "Switch theme: Light, Dark or System",
			OnClick: func() { s.cycleTheme() },
		})
	})
}

// content renders the current page inside the main area (§6.5). The shell
// keeps the frame; only this area changes on navigation.
func (s *Shell) content(v *components.UI) {
	route := s.route()
	page, ok := s.pages[route.Kind]
	if !ok {
		page = PlaceholderPage(route.Kind)
	}
	ui.Column(v.C).Fill().Clip().Children(func() {
		ui.Scroll(v.C).Fill().Children(func() {
			page(v, route)
		})
	})
}

// splitArea lays the main content and the inspector side by side (§6.6). The
// inspector is the second pane and never overlays the work area. Its width is
// kept in the shell; dragging the divider updates it.
func (s *Shell) splitArea(v *components.UI, total float32, insp app.Inspector) {
	if total < inspectorWidth(insp)+tokens.Space12*6 {
		// Not enough room for both panes: keep the main work area, as the
		// design says the inspector must not squeeze it.
		s.content(v)
		return
	}
	targetMain := total - inspectorWidth(insp)
	if s.mainSize == 0 {
		s.mainSize = targetMain
	}
	// The user dragged the divider if the first pane moved while the room
	// available stayed the same. A moving sidebar changes the room, so it
	// never looks like a drag.
	if s.lastTotal != 0 && absf(total-s.lastTotal) < 0.5 && absf(s.mainSize-targetMain) > 0.5 {
		w := total - s.mainSize
		if w < tokens.Space12*5 {
			w = tokens.Space12 * 5
		}
		if w > tokens.Space12*10 {
			w = tokens.Space12 * 10
		}
		s.app.SetInspectorWidth(w)
		insp = s.app.Inspector()
	}
	s.lastTotal = total
	s.mainSize = total - inspectorWidth(insp)

	size := s.mainSize
	ui.Split(v.C, &size, func() {
		s.content(v)
	}, func() {
		s.inspector(v, insp)
	})
	s.mainSize = size
}

// inspector builds the shared detail pane. Modules fill it through
// App.ShowInspector; they never build their own pane.
func (s *Shell) inspector(v *components.UI, insp app.Inspector) {
	p := v.T.Palette
	col := ui.Column(v.C).
		Fill().
		Width(inspectorWidth(insp)).
		Padding(tokens.PanelPadding).
		Gap(tokens.RelatedGap).
		Background(mygo.Color(p.BgSurface))
	col.Children(func() {
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
			title := insp.Title
			if title == "" {
				title = "Inspector"
			}
			ui.Text(v.C, title).
				Grow(1).
				FontSize(tokens.Typography.SectionTitle.Size).
				FontWeight(int(tokens.Typography.SectionTitle.Weight))
			v.IconButton(icons.Close, "Close inspector", components.IconButtonOptions{
				Variant: components.VariantGhost,
				Size:    components.SizeSmall,
				OnClick: func() { s.app.HideInspector() },
			})
		})
		v.Divider()
		if insp.Body != nil {
			insp.Body(v)
			return
		}
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Search,
			Title:  "Nothing selected",
			Reason: "Select a row to see its properties, relations and sources here.",
		})
	})
}

// handleKeys installs the global shortcuts of §9.3.
func (s *Shell) handleKeys(c *ui.Context) {
	if c.Shortcut(ui.Cmd, ui.KeyK) {
		s.search.Focus(c)
	}
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyP) {
		s.togglePalette()
	}
	if c.Shortcut(0, ui.KeyEscape) {
		switch {
		case s.app.PaletteOpen():
			s.app.SetPalette(false)
		case s.app.Inspector().Open:
			s.app.HideInspector()
		}
	}
}

// breadcrumb returns the path to the current page and the route each ancestor
// navigates to.
func (s *Shell) breadcrumb() ([]string, []ports.Route) {
	route := s.route()
	items := []string{s.app.WorkspaceName()}
	kinds := []ports.Route{{Kind: app.RouteHome}}
	if p := s.app.Project(); p != nil {
		items = append(items, p.Name)
		kinds = append(kinds, ports.Route{Kind: ports.RouteOverview, ProjectID: p.ID})
	}
	if title := s.pageTitle(route); title != "" {
		items = append(items, title)
		kinds = append(kinds, route)
	}
	return items, kinds
}

// route returns the current route.
func (s *Shell) route() ports.Route { return s.app.Route() }

// navigate sets the route and closes transient overlays.
func (s *Shell) navigate(r ports.Route) {
	s.app.SetPalette(false)
	s.app.Navigate(r)
}

// cycleTheme advances Light → Dark → System → Light, persists it and reports
// it through a toast. The current route and page data are untouched.
func (s *Shell) cycleTheme() {
	next := theme.Dark
	switch s.provider.Mode() {
	case theme.Light:
		next = theme.Dark
	case theme.Dark:
		next = theme.System
	default:
		next = theme.Light
	}
	if err := s.provider.SetMode(s.ctx, next); err != nil {
		s.app.Toast(app.LevelDanger, "Theme not saved", err.Error())
		return
	}
	s.app.Toast(app.LevelInfo, "Theme: "+themeLabel(next), "The interface switched without reloading.")
}

// loadProjectsIfNeeded fetches the project list in the background once, so the
// Home and Projects pages can render it without blocking the frame.
func (s *Shell) loadProjectsIfNeeded() {
	switch s.route().Kind {
	case app.RouteHome, app.RouteProjects:
	default:
		return
	}
	if _, ok, _ := s.app.ProjectList(); ok {
		return
	}
	if !s.projectsOnce.CompareAndSwap(false, true) {
		return
	}
	go func() {
		if err := s.app.RefreshProjects(s.ctx); err != nil {
			s.app.Toast(app.LevelDanger, "Could not list projects", err.Error())
		}
	}()
}

// absf returns the absolute value of a float32.
func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// themeLabel names a theme mode in the interface.
func themeLabel(m theme.Mode) string {
	switch m {
	case theme.Dark:
		return "Dark"
	case theme.System:
		return "System"
	}
	return "Light"
}

// inspectorWidth returns the pane width, within the designed range.
func inspectorWidth(insp app.Inspector) float32 {
	w := insp.Width
	if w <= 0 {
		w = components.InspectorWidth
	}
	if w < tokens.Space12*5 {
		w = tokens.Space12 * 5
	}
	if w > tokens.Space12*10 {
		w = tokens.Space12 * 10
	}
	return w
}

// breakpointNormal is the width below which, per §6.7, the sidebar collapses
// to its rail and the inspector stays closed. 1440 is the full-layout
// reference width; above 1200 the inspector is available on demand.
const breakpointNormal float32 = 1200

// sortedProjects returns the project list newest first, as the pages show it.
func sortedProjects(list []project.Project) []project.Project {
	out := append([]project.Project(nil), list...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// matches filters a command by its title and keywords, case-insensitively.
func matches(query, title, keywords string) bool {
	if query == "" {
		return true
	}
	q := strings.ToLower(strings.TrimSpace(query))
	return strings.Contains(strings.ToLower(title), q) || strings.Contains(strings.ToLower(keywords), q)
}
