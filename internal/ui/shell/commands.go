package shell

import (
	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/theme"
)

// command is one entry of the command palette.
type command struct {
	id       string
	title    string
	hint     string
	keywords string
	icon     *icons.Icon
	run      func()
}

// commands returns the commands the palette offers for the current state:
// navigation, project switching, the theme, the sidebar, the inspector and the
// system destinations.
func (s *Shell) commands() []command {
	var out []command
	add := func(kind ports.RouteKind, icon *icons.Icon, project bool) {
		title := pageTitle(kind)
		out = append(out, command{
			id:       "go:" + string(kind),
			title:    i18n.Tf("shell.command.goto", map[string]string{"page": title}),
			hint:     i18n.T("shell.command.hint_page"),
			keywords: title,
			icon:     icon,
			run: func() {
				r := ports.Route{Kind: kind}
				if project {
					if p := s.app.Project(); p != nil {
						r.ProjectID = p.ID
					}
				}
				s.navigate(r)
			},
		})
	}

	add(app.RouteHome, icons.Home, false)
	add(app.RouteProjects, icons.Projects, false)
	add(app.RouteInbox, icons.Inbox, false)
	add(app.RoutePluginMarket, icons.PluginMarket, false)
	if p := s.app.Project(); p != nil {
		add(ports.RouteOverview, icons.Overview, true)
		add(app.RouteLiterature, icons.Literature, true)
		add(app.RouteNotes, icons.Note, true)
		add(app.RouteIdeas, icons.Ideas, true)
		add(app.RouteExperiments, icons.Experiments, true)
		add(app.RouteKanban, icons.Kanban, true)
		add(app.RouteWeeklyReview, icons.WeeklyReview, true)
	}
	add(ports.RouteSettings, icons.Settings, false)
	add(ports.RouteTrash, icons.Trash, false)
	add(app.RouteDiagnostics, icons.Diagnostics, false)
	add(app.RouteConsistency, icons.Scan, false)
	add(app.RouteHelp, icons.Help, false)

	// Projects, newest first, open from the palette.
	list, _, _ := s.app.ProjectList()
	for _, p := range sortedProjects(list) {
		p := p
		out = append(out, command{
			id:       "project:" + p.ID,
			title:    i18n.Tf("shell.command.open_project", map[string]string{"name": p.Name}),
			hint:     i18n.T("shell.command.hint_project"),
			keywords: p.Name,
			icon:     icons.Projects,
			run:      func() { openProject(s.app, s.ctx, p.ID) },
		})
	}

	// A new project is created by the Projects page; the palette only navigates
	// there with a one-shot request, so the form has a single owner.
	out = append(out, command{
		id:       "action:new_project",
		title:    i18n.T("shell.command.new_project"),
		hint:     i18n.T("shell.command.hint_action"),
		keywords: i18n.T("shell.command.keywords_new_project"),
		icon:     icons.Plus,
		run:      func() { requestCreate(s.app) },
	})

	next := nextTheme(s.provider.Mode())
	out = append(out, command{
		id:       "action:theme",
		title:    i18n.Tf("shell.command.switch_theme", map[string]string{"mode": themeLabel(next)}),
		hint:     i18n.T("shell.command.hint_action"),
		keywords: i18n.T("shell.command.keywords_theme"),
		icon:     icons.Settings,
		run:      func() { s.cycleTheme() },
	})

	sidebarTitle := i18n.T("shell.command.sidebar_collapse")
	if s.app.SidebarCollapsed() {
		sidebarTitle = i18n.T("shell.command.sidebar_expand")
	}
	out = append(out, command{
		id:       "action:sidebar",
		title:    sidebarTitle,
		hint:     i18n.T("shell.command.hint_action"),
		keywords: i18n.T("shell.command.keywords_sidebar"),
		icon:     icons.Sidebar,
		run: func() {
			if err := s.app.ToggleSidebar(s.ctx); err != nil {
				s.app.Toast(app.LevelWarning, i18n.T("shell.sidebar.toast_save_fail"), errtext.Text(err))
			}
		},
	})

	if s.app.Inspector().Open {
		out = append(out, command{
			id:       "action:inspector-close",
			title:    i18n.T("shell.inspector.close"),
			hint:     i18n.T("shell.command.hint_action"),
			keywords: i18n.T("shell.command.keywords_inspector_close"),
			icon:     icons.Close,
			run:      func() { s.app.HideInspector() },
		})
	} else {
		out = append(out, command{
			id:       "action:inspector-open",
			title:    i18n.T("shell.command.inspector_open"),
			hint:     i18n.T("shell.command.hint_action"),
			keywords: i18n.T("shell.command.keywords_inspector_open"),
			icon:     icons.Sidebar,
			run:      func() { s.app.SetInspectorOpen(true) },
		})
	}

	return out
}

// runCommand closes the palette and runs the command.
func (s *Shell) runCommand(c command) {
	s.app.SetPalette(false)
	if c.run != nil {
		c.run()
	}
}

// nextTheme is the mode the theme control switches to.
func nextTheme(m theme.Mode) theme.Mode {
	switch m {
	case theme.Light:
		return theme.Dark
	case theme.Dark:
		return theme.System
	}
	return theme.Light
}
