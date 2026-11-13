package shell

import (
	"researchos/internal/ports"
	"researchos/internal/ui/app"
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
	add := func(kind ports.RouteKind, title string, icon *icons.Icon, project bool) {
		out = append(out, command{
			id:       "go:" + string(kind),
			title:    "Go to " + title,
			hint:     "Page",
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

	add(app.RouteHome, "Home", icons.Home, false)
	add(app.RouteProjects, "Projects", icons.Projects, false)
	add(app.RouteInbox, "Inbox", icons.Inbox, false)
	add(app.RoutePluginMarket, "Plugin Market", icons.PluginMarket, false)
	if p := s.app.Project(); p != nil {
		add(ports.RouteOverview, "Overview", icons.Overview, true)
		add(app.RouteLiterature, "Literature", icons.Literature, true)
		add(app.RouteNotes, "Research Notes", icons.Note, true)
		add(app.RouteIdeas, "Ideas", icons.Ideas, true)
		add(app.RouteExperiments, "Experiments", icons.Experiments, true)
		add(app.RouteKanban, "Kanban", icons.Kanban, true)
		add(app.RouteWeeklyReview, "Weekly Review", icons.WeeklyReview, true)
	}
	add(ports.RouteSettings, "Settings", icons.Settings, false)
	add(ports.RouteTrash, "Recycle Bin", icons.Trash, false)
	add(app.RouteDiagnostics, "Diagnostics", icons.Diagnostics, false)
	add(app.RouteConsistency, "File Consistency", icons.Scan, false)
	add(app.RouteHelp, "Help / About", icons.Help, false)

	// Projects, newest first, open from the palette.
	list, _, _ := s.app.ProjectList()
	for _, p := range sortedProjects(list) {
		p := p
		out = append(out, command{
			id:       "project:" + p.ID,
			title:    "Open project " + p.Name,
			hint:     "Project",
			keywords: p.Name,
			icon:     icons.Projects,
			run:      func() { openProject(s.app, s.ctx, p.ID) },
		})
	}

	next := nextTheme(s.provider.Mode())
	out = append(out, command{
		id:       "action:theme",
		title:    "Switch theme to " + themeLabel(next),
		hint:     "Action",
		keywords: "theme light dark system appearance",
		icon:     icons.Settings,
		run:      func() { s.cycleTheme() },
	})

	sidebarTitle := "Collapse sidebar"
	if s.app.SidebarCollapsed() {
		sidebarTitle = "Expand sidebar"
	}
	out = append(out, command{
		id:       "action:sidebar",
		title:    sidebarTitle,
		hint:     "Action",
		keywords: "sidebar navigation collapse expand rail",
		icon:     icons.Sidebar,
		run: func() {
			if err := s.app.ToggleSidebar(s.ctx); err != nil {
				s.app.Toast(app.LevelWarning, "Sidebar state not saved", err.Error())
			}
		},
	})

	if s.app.Inspector().Open {
		out = append(out, command{
			id:       "action:inspector-close",
			title:    "Close inspector",
			hint:     "Action",
			keywords: "inspector detail panel close",
			icon:     icons.Close,
			run:      func() { s.app.HideInspector() },
		})
	} else {
		out = append(out, command{
			id:       "action:inspector-open",
			title:    "Open inspector",
			hint:     "Action",
			keywords: "inspector detail panel open",
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
