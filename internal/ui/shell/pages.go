package shell

import (
	"context"
	"strconv"

	"github.com/egoist/mygo/ui"

	"researchos/internal/ports"
	"researchos/internal/project"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/tokens"
)

// pageTitle is the name of a destination in the breadcrumb and page header.
func pageTitle(kind ports.RouteKind) string {
	switch kind {
	case app.RouteHome:
		return "Home"
	case app.RouteProjects:
		return "Projects"
	case app.RouteInbox:
		return "Inbox"
	case app.RoutePluginMarket:
		return "Plugin Market"
	case ports.RouteOverview:
		return "Overview"
	case app.RouteLiterature:
		return "Literature"
	case app.RouteNotes:
		return "Research Notes"
	case app.RouteIdeas:
		return "Ideas"
	case app.RouteExperiments:
		return "Experiments"
	case app.RouteKanban:
		return "Kanban"
	case app.RouteWeeklyReview:
		return "Weekly Review"
	case ports.RouteSettings:
		return "Settings"
	case ports.RouteTrash:
		return "Recycle Bin"
	case app.RouteDiagnostics:
		return "Diagnostics"
	case app.RouteConsistency:
		return "File Consistency"
	case app.RouteHelp:
		return "Help / About"
	case ports.RouteNote:
		return "Note"
	case ports.RoutePaper:
		return "Paper"
	case ports.RouteIdea:
		return "Idea"
	case ports.RouteExperiment:
		return "Experiment"
	case ports.RouteRun:
		return "Run"
	case ports.RouteTask:
		return "Task"
	case ports.RouteWeeklyReport:
		return "Weekly Report"
	case ports.RouteExport:
		return "Export"
	}
	return "ResearchOS"
}

// pageTitleOf returns the title of a route, with its entity name when the
// route names one.
func (s *Shell) pageTitle(route ports.Route) string {
	return pageTitle(route.Kind)
}

// downstream names the module that owns a destination not yet built.
func downstream(kind ports.RouteKind) string {
	switch kind {
	case app.RouteLiterature:
		return "The Literature module (SPEC-04)"
	case app.RouteNotes:
		return "The Research Notes module (SPEC-05)"
	case app.RouteIdeas:
		return "The Ideas module (SPEC-06)"
	case app.RouteExperiments:
		return "The Experiments module (SPEC-07)"
	case app.RouteKanban:
		return "The Kanban module (SPEC-08)"
	case app.RouteWeeklyReview:
		return "The Weekly Review module (SPEC-09)"
	case app.RoutePluginMarket:
		return "The plugin system"
	case app.RouteInbox:
		return "The Inbox module"
	case ports.RouteOverview, ports.RouteProject:
		return "The Projects module (SPEC-03)"
	case ports.RouteSettings, ports.RouteTrash, app.RouteDiagnostics, app.RouteConsistency:
		return "The system pages milestone"
	}
	return "A later module"
}

// PlaceholderPage is the honest stand-in for a destination whose module is not
// built yet: it names the module and never fakes its content (§13).
func PlaceholderPage(kind ports.RouteKind) Page {
	return func(v *components.UI, r ports.Route) {
		v.PageContainer(components.PageContainerOptions{Children: func() {
			v.PageHeader(components.PageHeaderOptions{
				Title:       pageTitle(kind),
				Description: "This destination is part of the application shell and is not filled yet.",
			})
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.FileText,
				Title:  pageTitle(kind) + " is not available yet",
				Reason: downstream(kind) + " builds this page. The shell, navigation and workspace are ready.",
			})
		}})
	}
}

// DefaultPages returns the pages the shell can render today: the shell's own
// Home, Projects and Help, the project overview, and honest placeholders for
// the modules and system pages that later milestones fill.
func DefaultPages(ctx context.Context, a *app.App) map[ports.RouteKind]Page {
	projects := &projectsPage{}
	pages := map[ports.RouteKind]Page{
		app.RouteHome:       homePage(ctx, a),
		app.RouteHelp:       helpPage(a),
		app.RouteProjects:   projects.view(ctx, a),
		ports.RouteOverview: overviewPage(ctx, a),
	}
	for _, kind := range []ports.RouteKind{
		app.RouteInbox,
		app.RoutePluginMarket,
		app.RouteLiterature,
		app.RouteNotes,
		app.RouteIdeas,
		app.RouteExperiments,
		app.RouteKanban,
		app.RouteWeeklyReview,
		ports.RouteSettings,
		ports.RouteTrash,
		app.RouteDiagnostics,
		app.RouteConsistency,
	} {
		pages[kind] = PlaceholderPage(kind)
	}
	return pages
}

// homePage shows the workspace at a glance and the projects to open.
func homePage(ctx context.Context, a *app.App) Page {
	state := components.NewTableState()
	return func(v *components.UI, r ports.Route) {
		list, loaded, listErr := a.ProjectList()
		v.PageContainer(components.PageContainerOptions{Children: func() {
			v.PageHeader(components.PageHeaderOptions{
				Title:       "Home",
				Description: "Your research workspace at a glance.",
			})
			if listErr != nil {
				v.Banner(components.BannerOptions{
					Tone:    components.Danger,
					Title:   "The project list could not be loaded",
					Message: listErr.Error(),
				})
			}
			ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
				v.Card(components.CardOptions{Title: "Workspace", Description: "Current root"}, func() {
					ui.Text(v.C, a.WorkspaceName()).FontSize(tokens.Typography.SectionTitle.Size)
				}).Grow(1)
				v.Card(components.CardOptions{Title: "Projects", Description: "Registered in this workspace"}, func() {
					ui.Text(v.C, strconv.Itoa(len(list))).FontSize(tokens.Typography.PageTitle.Size)
				}).Grow(1)
			})
			switch {
			case !loaded:
				v.Skeleton(components.SkeletonOptions{Lines: 4})
			case len(list) == 0:
				v.EmptyState(components.EmptyStateOptions{
					Icon:   icons.Projects,
					Title:  "No projects yet",
					Reason: "Projects keep literature, notes, ideas and experiments together. Creating projects arrives with the Projects module (SPEC-03).",
				})
			default:
				v.Card(components.CardOptions{Title: "Recent projects"}, func() {
					projectTable(v, ctx, a, state, list, len(list))
				})
			}
		}})
	}
}

// projectsPage is the read-only project list of the shell. Project management
// belongs to SPEC-03; opening a project is shell navigation.
type projectsPage struct {
	state ui.ListState
}

func (p *projectsPage) view(ctx context.Context, a *app.App) Page {
	p.state = *components.NewTableState()
	return func(v *components.UI, r ports.Route) {
		list, loaded, listErr := a.ProjectList()
		v.PageContainer(components.PageContainerOptions{Children: func() {
			v.PageHeader(components.PageHeaderOptions{
				Title:       "Projects",
				Description: "Open a project to see its navigation. Project management arrives with SPEC-03.",
			})
			if listErr != nil {
				v.Banner(components.BannerOptions{Tone: components.Danger, Title: "The project list could not be loaded", Message: listErr.Error()})
			}
			switch {
			case !loaded:
				v.Skeleton(components.SkeletonOptions{Lines: 5})
			case len(list) == 0:
				v.EmptyState(components.EmptyStateOptions{
					Icon:   icons.Projects,
					Title:  "No projects yet",
					Reason: "This workspace has no registered projects. Creating projects arrives with the Projects module (SPEC-03).",
				})
			default:
				v.Card(components.CardOptions{}, func() {
					projectTable(v, ctx, a, &p.state, list, len(list))
				})
			}
		}})
	}
}

// projectTable renders the project rows with an Open action.
func projectTable(v *components.UI, ctx context.Context, a *app.App, state *ui.ListState, list []project.Project, n int) {
	state.Key = func(i int) any { return list[i].ID }
	cols := []components.Column{
		{Title: "Name", Sortable: true},
		{Title: "Status", Width: 110, Sortable: true},
		{Title: "Created", Width: 140, Sortable: true},
		{Title: "", Width: 100, Align: ui.End},
	}
	v.Table(components.TableOptions{
		Columns: cols,
		Rows:    n,
		State:   state,
		Cell: func(row, col int) {
			p := list[row]
			switch col {
			case 0:
				ui.Text(v.C, p.Name).SingleLine()
			case 1:
				status := "Active"
				if p.Status == project.StatusArchived {
					status = "Archived"
				}
				v.StatusIndicator(statusTone(p.Status), status)
			case 2:
				ui.Text(v.C, p.CreatedAt.Format("2006-01-02")).FontFeatures("tnum")
			case 3:
				v.Button("Open", components.ButtonOptions{
					Variant: components.VariantGhost,
					Size:    components.SizeSmall,
					Label:   "Open " + p.Name,
					OnClick: func() { openProject(a, ctx, p.ID) },
				})
			}
		},
	})
}

// overviewPage shows the open project's identity; statistics belong to
// SPEC-03.
func overviewPage(ctx context.Context, a *app.App) Page {
	return func(v *components.UI, r ports.Route) {
		p := a.Project()
		if p == nil {
			v.PageContainer(components.PageContainerOptions{Children: func() {
				v.PageHeader(components.PageHeaderOptions{Title: "Overview"})
				v.EmptyState(components.EmptyStateOptions{
					Icon:   icons.Projects,
					Title:  "No project is open",
					Reason: "Open a project from Home or Projects to see its overview.",
				})
			}})
			return
		}
		v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth * 1.4, Children: func() {
			v.PageHeader(components.PageHeaderOptions{
				Title:       p.Name,
				Description: p.Summary,
				Actions: func() {
					v.Button("Close project", components.ButtonOptions{
						Variant: components.VariantGhost,
						Size:    components.SizeSmall,
						OnClick: func() { closeProject(a, ctx, p.ID) },
					})
				},
			})
			ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
				v.Card(components.CardOptions{Title: "Goals"}, func() {
					text := p.Goals
					if text == "" {
						text = "No goals written yet."
					}
					ui.Text(v.C, text).FontSize(tokens.Typography.Body.Size)
				}).Grow(1)
				v.Card(components.CardOptions{Title: "Focus"}, func() {
					text := p.FocusMD
					if text == "" {
						text = "Nothing in focus."
					}
					ui.Text(v.C, text).FontSize(tokens.Typography.Body.Size)
				}).Grow(1)
			})
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Overview,
				Title:  "Project statistics are not ready",
				Reason: "Note, idea, experiment and task counts arrive with the Projects module (SPEC-03).",
			})
		}})
	}
}

// helpPage lists the shell's keyboard shortcuts and workspace identity.
func helpPage(a *app.App) Page {
	return func(v *components.UI, r ports.Route) {
		v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth, Children: func() {
			v.PageHeader(components.PageHeaderOptions{
				Title:       "Help / About",
				Description: "ResearchOS desktop research workspace.",
			})
			v.Card(components.CardOptions{Title: "Keyboard"}, func() {
				for _, row := range [][2]string{
					{"Ctrl/Cmd + K", "Focus the global search"},
					{"Ctrl/Cmd + Shift + P", "Open the command palette"},
					{"Esc", "Close the palette, inspector or dialog"},
					{"Tab / Shift + Tab", "Move the focus"},
					{"Enter / Space", "Activate the focused control"},
				} {
					ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
						ui.Text(v.C, row[0]).Font(tokens.SystemFonts.Mono).Width(220)
						ui.Text(v.C, row[1]).FontSize(tokens.Typography.Secondary.Size)
					})
				}
			})
			v.Card(components.CardOptions{Title: "Workspace"}, func() {
				ui.Text(v.C, a.WorkspaceName()).FontSize(tokens.Typography.Body.Size)
				ui.Text(v.C, "Theme and sidebar preferences are stored in the workspace settings.").
					FontSize(tokens.Typography.Caption.Size)
			})
		}})
	}
}

// openProject opens a project off the UI thread: the click returns at once and
// the shell repaints when the session is ready.
func openProject(a *app.App, ctx context.Context, id string) {
	go func() {
		if err := a.OpenProject(ctx, id); err != nil {
			a.Toast(app.LevelDanger, "Could not open the project", err.Error())
		}
	}()
}

// closeProject closes the open project off the UI thread.
func closeProject(a *app.App, ctx context.Context, id string) {
	go func() {
		if err := a.CloseProject(ctx); err != nil {
			a.Toast(app.LevelDanger, "Could not close the project", err.Error())
		}
	}()
}

// statusTone maps a project status to a status tone.
func statusTone(s project.Status) components.Tone {
	if s == project.StatusArchived {
		return components.Neutral
	}
	return components.Success
}
