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

	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/project"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
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

	// projectsFolded hides the project module entries nested under the
	// Projects nav item; the chevron toggles it in place.
	projectsFolded bool
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
			// The work area takes the height the toolbar and the task strip
			// leave: without a bounded height a page's scroll view and an
			// inspector's body would grow past the window, which clips the
			// last controls instead of scrolling them into view.
			ui.Column(v.C).FillWidth().Grow(1).Clip().Children(func() {
				// The page area is the window without the sidebar and the
				// separator, and without the inspector when it shows. Pages
				// that divide their width over their content (the kanban
				// board) read it from v.ContentWidth rather than counting
				// the whole window as their own.
				area := width - sidebarWidth - 1
				if showInspector {
					s.splitArea(v, area, inspector)
				} else {
					s.content(v, area)
				}
			})
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
			v.NavSection(i18n.T("shell.nav.section_workspace"), collapsed, func() {
				s.navItems(v, collapsed, []navEntry{
					{app.RouteHome, icons.Home, false},
				})
				s.projectsGroup(v, collapsed)
				s.navItems(v, collapsed, []navEntry{
					{app.RouteInbox, icons.Inbox, false},
					{app.RoutePluginMarket, icons.PluginMarket, false},
				})
			})

			v.Spacer()

			ui.Column(v.C).Gap(tokens.Space1).Children(func() {
				s.navItems(v, collapsed, []navEntry{
					{ports.RouteSettings, icons.Settings, false},
					{app.RouteHelp, icons.Help, false},
				})
			})
		})
	})
}

// projectsGroup is the Projects entry of the workspace section. When a
// project is open its module pages nest under it, and the chevron folds
// them away without leaving the project.
func (s *Shell) projectsGroup(v *components.UI, collapsed bool) {
	proj := s.app.Project()
	// The rail has no room for a disclosure control: it keeps showing
	// the module icons while a project is open.
	expanded := proj != nil && (!s.projectsFolded || collapsed)
	options := components.NavItemOptions{
		ID:        string(app.RouteProjects),
		Icon:      icons.Projects,
		Label:     pageTitle(app.RouteProjects),
		Selected:  s.route().Kind == app.RouteProjects,
		Collapsed: collapsed,
		OnClick:   func() { s.navigate(ports.Route{Kind: app.RouteProjects}) },
	}
	switch {
	case collapsed || proj == nil:
		v.NavItem(options)
	default:
		ui.Row(v.C).FillWidth().AlignItems(ui.Center).Children(func() {
			options.Grow = true
			v.NavItem(options)
			chevron, tip := icons.ChevronDown, i18n.T("shell.nav.projects_fold")
			if s.projectsFolded {
				chevron, tip = icons.ChevronRight, i18n.T("shell.nav.projects_expand")
			}
			v.IconButton(chevron, tip, components.IconButtonOptions{
				Variant: components.VariantGhost,
				Size:    components.SizeSmall,
				OnClick: func() { s.projectsFolded = !s.projectsFolded },
			})
		})
	}
	if !expanded {
		return
	}
	if collapsed {
		s.navItems(v, collapsed, projectEntries())
		return
	}
	ui.Column(v.C).FillWidth().Gap(tokens.Space1).
		Padding(0, 0, 0, tokens.Space4).
		Children(func() {
			s.navItems(v, false, projectEntries())
		})
}

// projectEntries are the module destinations of the open project.
func projectEntries() []navEntry {
	return []navEntry{
		{ports.RouteOverview, icons.Overview, true},
		{app.RouteLiterature, icons.Literature, true},
		{app.RouteNotes, icons.Note, true},
		{app.RouteIdeas, icons.Ideas, true},
		{app.RouteExperiments, icons.Experiments, true},
		{app.RouteKanban, icons.Kanban, true},
		{app.RouteWeeklyReview, icons.WeeklyReview, true},
	}
}

// navEntry is one destination of the sidebar. Its label is the destination's
// name from pageTitle, so the sidebar, the breadcrumb and the command palette
// never drift apart.
type navEntry struct {
	kind    ports.RouteKind
	icon    *icons.Icon
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
			Label:     pageTitle(e.kind),
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
		v.IconButton(icons.Sidebar, i18n.T("shell.topbar.toggle_sidebar"), components.IconButtonOptions{
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
				Placeholder(i18n.T("shell.topbar.search_placeholder")).
				Label(i18n.T("shell.topbar.search_label"))
			field.Bind(&s.search)
			if field.Changed() {
				s.app.SetSearch(query)
			}
			if field.Submitted() {
				s.paletteQuery = query
				s.app.SetPalette(true)
			}
		}

		v.IconButton(icons.Command, i18n.T("shell.palette.title"), components.IconButtonOptions{
			Variant: components.VariantGhost,
			Size:    components.SizeSmall,
			OnClick: func() { s.togglePalette() },
		})

		// Appearance control: the label names the current mode and the menu
		// offers the three modes at once, so the choice is readable and does
		// not require cycling through states to find the wanted one.
		mode := s.provider.Mode()
		v.MenuButton(themeLabel(mode), func(m *ui.Menu) {
			for _, candidate := range appearanceModes {
				item := m.Item(themeLabel(candidate)).ID(string(candidate)).Radio(candidate == mode)
				if item.Chosen() {
					s.setTheme(candidate)
				}
			}
		}).Label(i18n.T("shell.topbar.theme_label")).Tooltip(i18n.T("shell.topbar.theme_tooltip"))
	})
}

// content renders the current page inside the main area (§6.5). The shell
// keeps the frame; only this area changes on navigation.
func (s *Shell) content(v *components.UI, width float32) {
	v.ContentWidth = width
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
		s.content(v, total)
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
		s.content(v, s.mainSize)
	}, func() {
		s.inspector(v, insp)
	}).Fill()
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
				title = i18n.T("shell.inspector.default_title")
			}
			ui.Text(v.C, title).
				Grow(1).
				FontSize(tokens.Typography.SectionTitle.Size).
				FontWeight(int(tokens.Typography.SectionTitle.Weight))
			v.IconButton(icons.Close, i18n.T("shell.inspector.close"), components.IconButtonOptions{
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
			Title:  i18n.T("shell.inspector.empty_title"),
			Reason: i18n.T("shell.inspector.empty_reason"),
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

// appearanceModes are the themes the appearance menu offers, in the order it
// lists them.
var appearanceModes = []theme.Mode{theme.System, theme.Light, theme.Dark}

// setTheme switches the appearance, persists it and reports it through a
// toast. The current route and page data are untouched.
func (s *Shell) setTheme(next theme.Mode) {
	if next == s.provider.Mode() {
		return
	}
	if err := s.provider.SetMode(s.ctx, next); err != nil {
		s.app.Toast(app.LevelDanger, i18n.T("shell.theme.toast_save_fail_title"), errtext.Text(err))
		return
	}
	s.app.Toast(app.LevelInfo,
		i18n.Tf("shell.theme.toast_switch_title", map[string]string{"mode": themeLabel(next)}),
		i18n.T("shell.theme.toast_switch_message"))
}

// cycleTheme advances Light → Dark → System → Light, for the command palette,
// and reports it through a toast.
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
	s.setTheme(next)
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
			s.app.Toast(app.LevelDanger, i18n.T("shell.projects.toast_list_fail"), errtext.Text(err))
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
		return i18n.T("shell.theme.dark")
	case theme.System:
		return i18n.T("shell.theme.system")
	}
	return i18n.T("shell.theme.light")
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
