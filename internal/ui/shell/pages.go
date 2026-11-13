package shell

import (
	"context"
	"strconv"

	"github.com/egoist/mygo/ui"

	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/project"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/tokens"
)

// pageTitle is the name of a destination in the breadcrumb, the page header,
// the sidebar and the command palette.
func pageTitle(kind ports.RouteKind) string {
	switch kind {
	case app.RouteHome:
		return i18n.T("shell.page.home")
	case app.RouteProjects:
		return i18n.T("shell.page.projects")
	case app.RouteInbox:
		return i18n.T("shell.page.inbox")
	case app.RoutePluginMarket:
		return i18n.T("shell.page.plugin_market")
	case ports.RouteOverview:
		return i18n.T("shell.page.overview")
	case app.RouteLiterature:
		return i18n.T("shell.page.literature")
	case app.RouteNotes:
		return i18n.T("shell.page.notes")
	case app.RouteIdeas:
		return i18n.T("shell.page.ideas")
	case app.RouteExperiments:
		return i18n.T("shell.page.experiments")
	case app.RouteKanban:
		return i18n.T("shell.page.kanban")
	case app.RouteWeeklyReview:
		return i18n.T("shell.page.weekly_review")
	case ports.RouteSettings:
		return i18n.T("shell.page.settings")
	case ports.RouteTrash:
		return i18n.T("shell.page.trash")
	case app.RouteDiagnostics:
		return i18n.T("shell.page.diagnostics")
	case app.RouteConsistency:
		return i18n.T("shell.page.consistency")
	case app.RouteHelp:
		return i18n.T("shell.page.help")
	case ports.RouteNote:
		return i18n.T("shell.page.note")
	case ports.RoutePaper:
		return i18n.T("shell.page.paper")
	case ports.RouteIdea:
		return i18n.T("shell.page.idea")
	case ports.RouteExperiment:
		return i18n.T("shell.page.experiment")
	case ports.RouteRun:
		return i18n.T("shell.page.run")
	case ports.RouteTask:
		return i18n.T("shell.page.task")
	case ports.RouteWeeklyReport:
		return i18n.T("shell.page.weekly_report")
	case ports.RouteExport:
		return i18n.T("shell.page.export")
	}
	return i18n.T("app.name")
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
		return i18n.T("shell.downstream.literature")
	case app.RouteNotes:
		return i18n.T("shell.downstream.notes")
	case app.RouteIdeas:
		return i18n.T("shell.downstream.ideas")
	case app.RouteExperiments:
		return i18n.T("shell.downstream.experiments")
	case app.RouteKanban:
		return i18n.T("shell.downstream.kanban")
	case app.RouteWeeklyReview:
		return i18n.T("shell.downstream.weekly_review")
	case app.RoutePluginMarket:
		return i18n.T("shell.downstream.plugins")
	case app.RouteInbox:
		return i18n.T("shell.downstream.inbox")
	case ports.RouteOverview, ports.RouteProject:
		return i18n.T("shell.downstream.projects")
	case ports.RouteSettings, ports.RouteTrash, app.RouteDiagnostics, app.RouteConsistency:
		return i18n.T("shell.downstream.system_pages")
	}
	return i18n.T("shell.downstream.default")
}

// PlaceholderPage is the honest stand-in for a destination whose module is not
// built yet: it names the module and never fakes its content (§13).
func PlaceholderPage(kind ports.RouteKind) Page {
	return func(v *components.UI, r ports.Route) {
		v.PageContainer(components.PageContainerOptions{Children: func() {
			v.PageHeader(components.PageHeaderOptions{
				Title:       pageTitle(kind),
				Description: i18n.T("shell.placeholder.description"),
			})
			v.EmptyState(components.EmptyStateOptions{
				Icon:  icons.FileText,
				Title: i18n.Tf("shell.placeholder.title", map[string]string{"page": pageTitle(kind)}),
				Reason: i18n.Tf("shell.placeholder.reason", map[string]string{
					"module": downstream(kind),
				}),
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
				Title:       pageTitle(app.RouteHome),
				Description: i18n.T("shell.home.description"),
			})
			if listErr != nil {
				v.Banner(components.BannerOptions{
					Tone:    components.Danger,
					Title:   i18n.T("shell.projects.banner_load_fail"),
					Message: errtext.Text(listErr),
				})
			}
			ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
				v.Card(components.CardOptions{
					Title:       i18n.T("shell.home.card_workspace"),
					Description: i18n.T("shell.home.card_workspace_desc"),
				}, func() {
					ui.Text(v.C, a.WorkspaceName()).FontSize(tokens.Typography.SectionTitle.Size)
				}).Grow(1)
				v.Card(components.CardOptions{
					Title:       i18n.T("shell.home.card_projects"),
					Description: i18n.T("shell.home.card_projects_desc"),
				}, func() {
					ui.Text(v.C, strconv.Itoa(len(list))).FontSize(tokens.Typography.PageTitle.Size)
				}).Grow(1)
			})
			switch {
			case !loaded:
				v.Skeleton(components.SkeletonOptions{Lines: 4})
			case len(list) == 0:
				v.EmptyState(components.EmptyStateOptions{
					Icon:   icons.Projects,
					Title:  i18n.T("shell.projects.empty_title"),
					Reason: i18n.T("shell.projects.empty_reason_home"),
				})
			default:
				v.Card(components.CardOptions{Title: i18n.T("shell.home.card_recent")}, func() {
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
				Title:       pageTitle(app.RouteProjects),
				Description: i18n.T("shell.projects.description"),
			})
			if listErr != nil {
				v.Banner(components.BannerOptions{
					Tone:    components.Danger,
					Title:   i18n.T("shell.projects.banner_load_fail"),
					Message: errtext.Text(listErr),
				})
			}
			switch {
			case !loaded:
				v.Skeleton(components.SkeletonOptions{Lines: 5})
			case len(list) == 0:
				v.EmptyState(components.EmptyStateOptions{
					Icon:   icons.Projects,
					Title:  i18n.T("shell.projects.empty_title"),
					Reason: i18n.T("shell.projects.empty_reason_list"),
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
		{Title: i18n.T("shell.table.col_name"), Sortable: true},
		{Title: i18n.T("shell.table.col_status"), Width: 110, Sortable: true},
		{Title: i18n.T("shell.table.col_created"), Width: 140, Sortable: true},
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
				status := i18n.T("shell.project_status.active")
				if p.Status == project.StatusArchived {
					status = i18n.T("shell.project_status.archived")
				}
				v.StatusIndicator(statusTone(p.Status), status)
			case 2:
				ui.Text(v.C, p.CreatedAt.Format("2006-01-02")).FontFeatures("tnum")
			case 3:
				v.Button(i18n.T("shell.projects.btn_open"), components.ButtonOptions{
					Variant: components.VariantGhost,
					Size:    components.SizeSmall,
					Label:   i18n.Tf("shell.projects.btn_open_aria", map[string]string{"name": p.Name}),
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
				v.PageHeader(components.PageHeaderOptions{Title: pageTitle(ports.RouteOverview)})
				v.EmptyState(components.EmptyStateOptions{
					Icon:   icons.Projects,
					Title:  i18n.T("shell.overview.empty_no_project_title"),
					Reason: i18n.T("shell.overview.empty_no_project_reason"),
				})
			}})
			return
		}
		v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth * 1.4, Children: func() {
			v.PageHeader(components.PageHeaderOptions{
				Title:       p.Name,
				Description: p.Summary,
				Actions: func() {
					v.Button(i18n.T("shell.overview.btn_close"), components.ButtonOptions{
						Variant: components.VariantGhost,
						Size:    components.SizeSmall,
						OnClick: func() { closeProject(a, ctx, p.ID) },
					})
				},
			})
			ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
				v.Card(components.CardOptions{Title: i18n.T("shell.overview.card_goals")}, func() {
					text := p.Goals
					if text == "" {
						text = i18n.T("shell.overview.goals_empty")
					}
					ui.Text(v.C, text).FontSize(tokens.Typography.Body.Size)
				}).Grow(1)
				v.Card(components.CardOptions{Title: i18n.T("shell.overview.card_focus")}, func() {
					text := p.FocusMD
					if text == "" {
						text = i18n.T("shell.overview.focus_empty")
					}
					ui.Text(v.C, text).FontSize(tokens.Typography.Body.Size)
				}).Grow(1)
			})
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Overview,
				Title:  i18n.T("shell.overview.stats_title"),
				Reason: i18n.T("shell.overview.stats_reason"),
			})
		}})
	}
}

// helpPage lists the shell's keyboard shortcuts and workspace identity. Key
// names are the same in every language; only their descriptions translate.
func helpPage(a *app.App) Page {
	return func(v *components.UI, r ports.Route) {
		v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth, Children: func() {
			v.PageHeader(components.PageHeaderOptions{
				Title:       pageTitle(app.RouteHelp),
				Description: i18n.T("shell.help.description"),
			})
			v.Card(components.CardOptions{Title: i18n.T("shell.help.card_keyboard")}, func() {
				for _, row := range [][2]string{
					{"Ctrl/Cmd + K", i18n.T("shell.help.keydesc_1")},
					{"Ctrl/Cmd + Shift + P", i18n.T("shell.help.keydesc_2")},
					{"Esc", i18n.T("shell.help.keydesc_3")},
					{"Tab / Shift + Tab", i18n.T("shell.help.keydesc_4")},
					{"Enter / Space", i18n.T("shell.help.keydesc_5")},
				} {
					ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
						ui.Text(v.C, row[0]).Font(tokens.SystemFonts.Mono).Width(220)
						ui.Text(v.C, row[1]).FontSize(tokens.Typography.Secondary.Size)
					})
				}
			})
			v.Card(components.CardOptions{Title: i18n.T("shell.home.card_workspace")}, func() {
				ui.Text(v.C, a.WorkspaceName()).FontSize(tokens.Typography.Body.Size)
				ui.Text(v.C, i18n.T("shell.help.workspace_note")).
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
			a.Toast(app.LevelDanger, i18n.T("shell.projects.toast_open_fail"), errtext.Text(err))
		}
	}()
}

// closeProject closes the open project off the UI thread.
func closeProject(a *app.App, ctx context.Context, id string) {
	go func() {
		if err := a.CloseProject(ctx); err != nil {
			a.Toast(app.LevelDanger, i18n.T("shell.projects.toast_close_fail"), errtext.Text(err))
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
