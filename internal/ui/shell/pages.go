package shell

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/confirm"
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
		app.RouteHome:       homePage(ctx, a, projects),
		app.RouteHelp:       helpPage(a),
		app.RouteProjects:   projects.view(ctx, a),
		ports.RouteOverview: overviewPage(ctx, a, projects),
	}
	for _, kind := range []ports.RouteKind{
		app.RouteInbox,
		app.RoutePluginMarket,
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
func homePage(ctx context.Context, a *app.App, page *projectsPage) Page {
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
					Icon:        icons.Projects,
					Title:       i18n.T("shell.projects.empty_title"),
					Reason:      i18n.T("shell.projects.empty_reason_home"),
					ActionLabel: i18n.T("shell.projects.btn_create"),
					OnAction:    func() { requestCreate(a) },
				})
			default:
				v.Card(components.CardOptions{Title: i18n.T("shell.home.card_recent")}, func() {
					projectTable(v, ctx, a, page, state, list, len(list))
				})
			}
		}})
	}
}

// projectsPage is the project list of the shell. Opening is shell navigation;
// creation, editing, archiving and deletion run the SPEC-03 pipelines through
// the App inside managed tasks, so the frame never waits on the disk and the
// task strip shows progress.
type projectsPage struct {
	state ui.ListState

	mu sync.Mutex
	// creating, saving, preparing and deleting are cleared by the goroutine of
	// their task, so they sit under mu, together with the impact plan the
	// prepare task produces and the request token last handled.
	creating  bool
	saving    bool
	preparing bool
	deleting  bool
	plan      *project.DeletePlan
	planErr   error
	requested string

	// Everything below is written only while a frame is built, so a rejected
	// name leaves the typed values in place for the next attempt.
	createOpen bool
	create     projectFields
	editOpen   bool
	editID     string
	edit       projectFields
	deleteOpen bool
	deleteID   string
	// deleteName is what the user types to confirm the deletion, deleteTitle
	// the name of the pending project, and deleteErr why the last confirmation
	// was refused.
	deleteName  string
	deleteTitle string
	deleteErr   string
}

// projectFields is a project's identity as a dialog holds it: the four text
// fields SPEC-03 §5.3 lets a user edit.
type projectFields struct {
	name    string
	summary string
	goals   string
	focus   string
}

// fieldsOf is the stored row in dialog form.
func fieldsOf(p project.Project) projectFields {
	return projectFields{name: p.Name, summary: p.Summary, goals: p.Goals, focus: p.FocusMD}
}

// patch asks the service to write all four fields of one project. Fields that
// did not change are no-ops there and emit no event (§5.3).
func (f projectFields) patch(id string) project.UpdateInput {
	name, summary, goals, focus := f.name, f.summary, f.goals, f.focus
	return project.UpdateInput{ID: id, Name: &name, Summary: &summary, Goals: &goals, Focus: &focus}
}

// Route parameters ask the Projects page for one of its dialogs. The value is
// a one-shot token, so repainting the page never reopens a dialog the user
// already closed; editing and deleting carry the project they act on.
const (
	createProjectParam = "create_project"
	editProjectParam   = "edit_project"
	deleteProjectParam = "delete_project"
)

// requestDialog navigates to the Projects page asking for one dialog. Keeping
// the forms there gives the project forms and their service calls one owner,
// whichever page the request came from.
func requestDialog(a *app.App, param, id string) {
	a.Navigate(ports.Route{
		Kind:   app.RouteProjects,
		Params: map[string]string{param: id + "/" + strconv.FormatInt(time.Now().UnixNano(), 10)},
	})
}

// requestCreate asks for the creation dialog.
func requestCreate(a *app.App) { requestDialog(a, createProjectParam, "") }

// take reports the first use of a request token.
func (p *projectsPage) take(token string) bool {
	if token == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if token == p.requested {
		return false
	}
	p.requested = token
	return true
}

// openDialogs opens the dialogs the route parameters ask for. An edit needs
// the stored row, which the shell already holds in the list it renders.
func (p *projectsPage) openDialogs(params map[string]string, a *app.App) {
	if p.take(params[createProjectParam]) {
		p.create = projectFields{}
		p.createOpen = true
	}
	if token := params[editProjectParam]; p.take(token) {
		id, _, _ := strings.Cut(token, "/")
		if row, ok := findProject(a, id); ok {
			p.edit, p.editID = fieldsOf(row), id
			p.editOpen = true
		}
	}
	if token := params[deleteProjectParam]; p.take(token) {
		id, _, _ := strings.Cut(token, "/")
		if row, ok := findProject(a, id); ok {
			p.askDelete(a, row)
		}
	}
}

// findProject looks one project up in the cached list the shell renders.
func findProject(a *app.App, id string) (project.Project, bool) {
	list, _, _ := a.ProjectList()
	for _, p := range list {
		if p.ID == id {
			return p, true
		}
	}
	return project.Project{}, false
}

func (p *projectsPage) view(ctx context.Context, a *app.App) Page {
	p.state = *components.NewTableState()
	return func(v *components.UI, r ports.Route) {
		p.openDialogs(r.Params, a)
		p.mu.Lock()
		creating := p.creating
		p.mu.Unlock()

		list, loaded, listErr := a.ProjectList()
		v.PageContainer(components.PageContainerOptions{Children: func() {
			v.PageHeader(components.PageHeaderOptions{
				Title:       pageTitle(app.RouteProjects),
				Description: i18n.T("shell.projects.description"),
				Actions: func() {
					if v.Button(i18n.T("shell.projects.btn_create"), components.ButtonOptions{
						Variant:  components.VariantPrimary,
						Icon:     icons.Plus,
						Disabled: creating,
						Label:    i18n.T("shell.projects.btn_create_aria"),
					}).Clicked() {
						requestCreate(a)
					}
				},
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
					Icon:        icons.Projects,
					Title:       i18n.T("shell.projects.empty_title"),
					Reason:      i18n.T("shell.projects.empty_reason_list"),
					ActionLabel: i18n.T("shell.projects.btn_create"),
					OnAction: func() {
						requestCreate(a)
					},
				})
			default:
				v.Card(components.CardOptions{}, func() {
					projectTable(v, ctx, a, p, &p.state, list, len(list))
				})
			}
			p.dialogs(v, a)
		}})
	}
}

// form renders the creation dialog while it is open. The fields are page state,
// so a rejected name leaves the typed values in place for the next attempt.
func (p *projectsPage) form(v *components.UI, a *app.App) {
	if !p.createOpen {
		return
	}
	v.Dialog(&p.createOpen, components.DialogOptions{
		Title:       i18n.T("shell.projects.dialog_title"),
		Description: i18n.T("shell.projects.dialog_desc"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				p.createOpen = false
			}
			created := v.Button(i18n.T("shell.projects.btn_confirm"), components.ButtonOptions{
				Variant: components.VariantPrimary,
			})
			if created.Clicked() {
				p.createOpen = false
				p.submit(a, p.create)
			}
		},
	}, func() { p.formFields(v, &p.create, false) })
}

// formFields builds the identity fields both dialogs share. The focus text
// belongs to a project that exists: Create takes name, summary and goals only
// (§5.3).
func (p *projectsPage) formFields(v *components.UI, f *projectFields, withFocus bool) {
	ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
		v.Input(&f.name, components.FieldOptions{
			Label:       i18n.T("shell.projects.field_name"),
			Placeholder: i18n.T("shell.projects.field_name_hint"),
			AutoFocus:   true,
		})
		v.Input(&f.summary, components.FieldOptions{
			Label:       i18n.T("shell.projects.field_summary"),
			Description: i18n.T("shell.projects.field_summary_desc"),
		})
		v.Textarea(&f.goals, components.FieldOptions{
			Label:       i18n.T("shell.projects.field_goals"),
			Description: i18n.T("shell.projects.field_goals_desc"),
		})
		if withFocus {
			v.Textarea(&f.focus, components.FieldOptions{
				Label:       i18n.T("shell.projects.field_focus"),
				Description: i18n.T("shell.projects.field_focus_desc"),
			})
		}
	})
}

// editForm renders the edit dialog (PRJ-02). The name never moves the folder:
// the directory keeps the project id, so a rename touches only the row.
func (p *projectsPage) editForm(v *components.UI, a *app.App) {
	if !p.editOpen {
		return
	}
	v.Dialog(&p.editOpen, components.DialogOptions{
		Title:       i18n.T("shell.projects.edit_dialog_title"),
		Description: i18n.T("shell.projects.edit_dialog_desc"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				p.editOpen = false
			}
			saved := v.Button(i18n.T("shell.projects.btn_save"), components.ButtonOptions{
				Variant: components.VariantPrimary,
			})
			if saved.Clicked() {
				p.editOpen = false
				p.save(a, p.editID, p.edit)
			}
		},
	}, func() { p.formFields(v, &p.edit, true) })
}

// dialogs renders the project forms. At most one of them is open at a time,
// and each takes its input from the page state, so a dialog that survives a
// repaint keeps what the user typed.
func (p *projectsPage) dialogs(v *components.UI, a *app.App) {
	p.form(v, a)
	p.editForm(v, a)
	p.deleteForm(v, a)
}

// submit runs the creation pipeline as a task. The dialog is already closed, so
// the task never touches dialog state: its only writes are the creating flag
// and the toasts the shell renders.
func (p *projectsPage) submit(a *app.App, f projectFields) {
	p.mu.Lock()
	if p.creating {
		p.mu.Unlock()
		return
	}
	p.creating = true
	p.mu.Unlock()

	a.RunTask(i18n.T("shell.projects.task_create"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("shell.projects.stage_create"))
		created, err := a.CreateProject(ctx, project.CreateInput{Name: f.name, Summary: f.summary, Goals: f.goals})
		p.mu.Lock()
		p.creating = false
		p.mu.Unlock()
		switch {
		case err != nil && created == nil:
			a.Toast(app.LevelDanger, i18n.T("shell.projects.toast_create_fail"), errtext.Text(err))
			return err
		case err != nil:
			// The project exists; only its Activity event is missing (§5.4).
			a.Toast(app.LevelWarning, i18n.Tf("shell.projects.toast_created", map[string]string{"name": created.Name}),
				errtext.Text(err))
		default:
			a.Toast(app.LevelInfo, i18n.Tf("shell.projects.toast_created", map[string]string{"name": created.Name}), "")
		}
		return nil
	})
}

// save edits a project (PRJ-02). The dialog is already closed, so the task
// writes only its flag and the toasts the shell renders.
func (p *projectsPage) save(a *app.App, id string, f projectFields) {
	p.mu.Lock()
	if p.saving {
		p.mu.Unlock()
		return
	}
	p.saving = true
	p.mu.Unlock()

	a.RunTask(i18n.T("shell.projects.task_edit"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("shell.projects.stage_edit"))
		_, err := a.UpdateProject(ctx, f.patch(id))
		p.mu.Lock()
		p.saving = false
		p.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("shell.projects.toast_edit_fail"), errtext.Text(err))
			return err
		}
		a.Toast(app.LevelSuccess, i18n.T("shell.projects.toast_edited"), "")
		return nil
	})
}

// archive flips PRJ-03's state. It is reversible and keeps every byte, so it
// needs no confirmation token; the §5.8 guard then rejects content writes on
// an archived project.
func (p *projectsPage) archive(a *app.App, row project.Project) {
	toArchive := row.Status != project.StatusArchived
	task, done := i18n.T("shell.projects.task_archive"), i18n.T("shell.projects.toast_archived")
	if !toArchive {
		task, done = i18n.T("shell.projects.task_unarchive"), i18n.T("shell.projects.toast_unarchived")
	}
	name := row.Name
	a.RunTask(task, func(ctx context.Context, t *app.Task) error {
		if err := a.SetArchived(ctx, row.ID, toArchive); err != nil {
			a.Toast(app.LevelDanger, i18n.T("shell.projects.toast_archive_fail"), errtext.Text(err))
			return err
		}
		a.Toast(app.LevelSuccess, i18n.Tf(done, map[string]string{"name": name}), "")
		return nil
	})
}

// askDelete opens the delete wizard and starts the read-only impact count of
// §5.4. Nothing is removed before the plan is shown, the project name retyped
// and a token issued.
func (p *projectsPage) askDelete(a *app.App, row project.Project) {
	p.mu.Lock()
	if p.preparing || p.deleting {
		p.mu.Unlock()
		return
	}
	p.preparing = true
	p.plan, p.planErr = nil, nil
	p.mu.Unlock()

	p.deleteOpen = true
	p.deleteID = row.ID
	p.deleteTitle = row.Name
	p.deleteName, p.deleteErr = "", ""

	a.RunTask(i18n.T("shell.projects.task_plan_delete"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("shell.projects.stage_plan_delete"))
		plan, err := a.PrepareProjectDelete(ctx, row.ID)
		p.mu.Lock()
		p.preparing = false
		p.plan, p.planErr = plan, err
		p.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("shell.projects.toast_plan_fail"), errtext.Text(err))
		}
		return err
	})
}

// deleteForm is the wizard: the impact plan, what may stop the deletion, the
// name the user must retype, and the one click that authorizes it.
func (p *projectsPage) deleteForm(v *components.UI, a *app.App) {
	if !p.deleteOpen {
		return
	}
	p.mu.Lock()
	plan, planErr := p.plan, p.planErr
	busy := p.preparing || p.deleting
	p.mu.Unlock()

	// The service compares the typed name against the stored row, so the plan
	// is the better target of the check: the row was read when it was clicked.
	target := p.deleteTitle
	if plan != nil {
		target = plan.ProjectName
	}
	ready := plan != nil && !busy && len(plan.Blockers) == 0 &&
		strings.TrimSpace(p.deleteName) == strings.TrimSpace(target)

	v.Dialog(&p.deleteOpen, components.DialogOptions{
		Title:       i18n.Tf("shell.projects.delete_dialog_title", map[string]string{"name": p.deleteTitle}),
		Description: i18n.T("shell.projects.delete_dialog_desc"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				p.deleteOpen = false
			}
			confirmed := v.Button(i18n.T("shell.projects.btn_delete_confirm"), components.ButtonOptions{
				Variant:  components.VariantDanger,
				Disabled: !ready,
			})
			if confirmed.Clicked() {
				p.confirmDelete(v, a, target)
			}
		},
	}, func() { p.deleteBody(v, plan, planErr, target) })
}

// deleteBody shows what goes with the project and why the button is still dark.
func (p *projectsPage) deleteBody(v *components.UI, plan *project.DeletePlan, planErr error, target string) {
	ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
		switch {
		case plan != nil:
			p.planRows(v, plan)
		case planErr != nil:
			v.Banner(components.BannerOptions{
				Tone:    components.Danger,
				Title:   i18n.T("shell.projects.banner_plan_fail"),
				Message: errtext.Text(planErr),
			})
		default:
			v.Skeleton(components.SkeletonOptions{Lines: 4})
		}
		if plan != nil && plan.ExportRecommended {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("shell.projects.banner_no_export"),
				Message: i18n.T("shell.projects.banner_no_export_desc"),
			})
		}
		for _, b := range blockers(plan) {
			v.Banner(components.BannerOptions{
				Tone:    components.Danger,
				Title:   i18n.T("shell.projects.banner_blocked"),
				Message: b,
			})
		}
		if p.deleteErr != "" {
			v.Banner(components.BannerOptions{
				Tone:    components.Danger,
				Title:   i18n.T("shell.projects.banner_confirm_fail"),
				Message: p.deleteErr,
			})
		}
		if plan != nil {
			v.Input(&p.deleteName, components.FieldOptions{
				Label:       i18n.T("shell.projects.field_confirm_name"),
				Description: i18n.Tf("shell.projects.field_confirm_name_desc", map[string]string{"name": target}),
				Placeholder: target,
				AutoFocus:   true,
			})
		}
	})
}

// blockers names the reasons the deletion must wait, in the interface's
// language where one is known and as the service said it otherwise.
func blockers(plan *project.DeletePlan) []string {
	if plan == nil {
		return nil
	}
	out := make([]string, 0, len(plan.Blockers))
	for _, b := range plan.Blockers {
		key := "shell.projects.blocker_" + b.Code
		if text := i18n.T(key); text != key {
			b.Message = text
		}
		out = append(out, b.Message)
	}
	return out
}

// deleteEntityOrder is the §5.4 counting order of the plan rows.
var deleteEntityOrder = []string{
	"note", "idea", "experiment", "run", "task", "evidence", "weekly_report",
}

// planRows is the impact preview: what the project holds, what becomes of its
// literature copies, and how much space leaves the workspace.
func (p *projectsPage) planRows(v *components.UI, plan *project.DeletePlan) {
	ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
		counted := false
		for _, kind := range deleteEntityOrder {
			n := plan.EntityCounts[kind]
			if n == 0 {
				continue
			}
			counted = true
			p.planRow(v, i18n.T("shell.projects.entity_"+kind), strconv.Itoa(n))
		}
		if !counted {
			ui.Text(v.C, i18n.T("shell.projects.plan_empty")).
				FontSize(tokens.Typography.Secondary.Size)
		}
		p.planRow(v, i18n.T("shell.projects.plan_trashed"), strconv.Itoa(plan.TrashedCount))
		p.planRow(v, i18n.T("shell.projects.plan_size"), components.HumanBytes(plan.DirBytes))
		papers := i18n.T("shell.projects.plan_papers_unknown")
		if !plan.PaperImpactUnknown {
			papers = i18n.Tf("shell.projects.plan_papers_value", map[string]string{
				"removed": strconv.Itoa(len(plan.ExclusivePaperIDs)),
				"kept":    strconv.Itoa(len(plan.SharedPaperIDs)),
			})
		}
		p.planRow(v, i18n.T("shell.projects.plan_papers"), papers)
	})
}

// planRow is one label and value of the preview.
func (p *projectsPage) planRow(v *components.UI, label, value string) {
	ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).Children(func() {
		ui.Text(v.C, label).FontSize(tokens.Typography.Secondary.Size).Grow(1)
		ui.Text(v.C, value).FontFeatures("tnum")
	})
}

// confirmDelete issues the one-time token from inside the dialog's confirm
// click, which is what makes it proof of a human in the app (SPEC-01 §5.8),
// and hands it to the delete task. A failure leaves the dialog open with its
// reason beside the field.
func (p *projectsPage) confirmDelete(v *components.UI, a *app.App, target string) {
	p.deleteErr = ""
	svc := a.Confirmation()
	if svc == nil {
		p.deleteErr = i18n.T("shell.projects.delete_unavailable")
		return
	}
	token, err := svc.Issue(v.Context(), confirm.Scope{
		Kind:      confirm.KindDeleteProject,
		ProjectID: p.deleteID,
	})
	if err != nil {
		p.deleteErr = errtext.Text(err)
		return
	}
	p.deleteOpen = false
	p.runDelete(a, p.deleteID, target, token)
}

// runDelete executes the §5.4 pipeline. The service closes the session and the
// App drops it with the route, so deleting the open project lands the shell on
// the list.
func (p *projectsPage) runDelete(a *app.App, id, name string, token confirm.Token) {
	p.mu.Lock()
	if p.deleting {
		p.mu.Unlock()
		return
	}
	p.deleting = true
	p.mu.Unlock()

	a.RunTask(i18n.T("shell.projects.task_delete"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("shell.projects.stage_delete"))
		res, err := a.DeleteProject(ctx, project.PermanentDeleteRequest{
			ProjectID:   id,
			ConfirmName: name,
			Token:       token,
		})
		p.mu.Lock()
		p.deleting = false
		p.plan = nil
		p.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("shell.projects.toast_delete_fail"), errtext.Text(err))
			return err
		}
		if res != nil && len(res.Warnings) > 0 {
			a.Toast(app.LevelWarning, i18n.T("shell.projects.toast_deleted"), strings.Join(res.Warnings, "\n"))
			return nil
		}
		a.Toast(app.LevelSuccess, i18n.T("shell.projects.toast_deleted"), "")
		return nil
	})
}

// projectTable renders the project rows: opening on the left, then the
// lifecycle of PRJ-02, PRJ-03 and the permanent delete of §5.4.
func projectTable(v *components.UI, ctx context.Context, a *app.App, page *projectsPage, state *ui.ListState, list []project.Project, n int) {
	state.Key = func(i int) any { return list[i].ID }
	cols := []components.Column{
		{Title: i18n.T("shell.table.col_name"), Sortable: true},
		{Title: i18n.T("shell.table.col_status"), Width: 110, Sortable: true},
		{Title: i18n.T("shell.table.col_created"), Width: 140, Sortable: true},
		{Title: "", Width: 380, Align: ui.End},
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
				archived := p.Status == project.StatusArchived
				ui.Row(v.C).Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
					if v.Button(i18n.T("shell.projects.btn_folder"), components.ButtonOptions{
						Variant: components.VariantSecondary,
						Size:    components.SizeSmall,
						Icon:    icons.FolderOpen,
						Label:   i18n.Tf("shell.projects.btn_folder_aria", map[string]string{"name": p.Name}),
					}).Clicked() {
						openProjectFolder(a, p.ID)
					}
					if v.Button(i18n.T("shell.projects.btn_open"), components.ButtonOptions{
						Variant: components.VariantSecondary,
						Size:    components.SizeSmall,
						Icon:    icons.ExternalLink,
						Label:   i18n.Tf("shell.projects.btn_open_aria", map[string]string{"name": p.Name}),
					}).Clicked() {
						openProject(a, ctx, p.ID)
					}
					if v.IconButton(icons.Edit,
						i18n.Tf("shell.projects.btn_edit_aria", map[string]string{"name": p.Name}),
						components.IconButtonOptions{
							Variant: components.VariantGhost,
							Size:    components.SizeSmall,
						}).Clicked() {
						requestDialog(a, editProjectParam, p.ID)
					}
					archiveIcon, archiveKey := icons.Archive, "shell.projects.btn_archive_aria"
					if archived {
						archiveIcon, archiveKey = icons.ArchiveRestore, "shell.projects.btn_unarchive_aria"
					}
					if v.IconButton(archiveIcon,
						i18n.Tf(archiveKey, map[string]string{"name": p.Name}),
						components.IconButtonOptions{
							Variant: components.VariantGhost,
							Size:    components.SizeSmall,
						}).Clicked() {
						page.archive(a, p)
					}
					if v.IconButton(icons.Trash,
						i18n.Tf("shell.projects.btn_delete_aria", map[string]string{"name": p.Name}),
						components.IconButtonOptions{
							Variant: components.VariantGhost,
							Size:    components.SizeSmall,
						}).Clicked() {
						requestDialog(a, deleteProjectParam, p.ID)
					}
				})
			}
		},
	})
}

// overviewPage shows the open project's identity; statistics belong to
// SPEC-03.
func overviewPage(ctx context.Context, a *app.App, page *projectsPage) Page {
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
		archived := p.Status == project.StatusArchived
		v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth * 1.4, Children: func() {
			v.PageHeader(components.PageHeaderOptions{
				Title:       p.Name,
				Description: p.Summary,
				Actions: func() {
					v.Button(i18n.T("shell.overview.btn_open_folder"), components.ButtonOptions{
						Variant: components.VariantGhost,
						Size:    components.SizeSmall,
						Icon:    icons.FolderOpen,
						OnClick: func() { openProjectFolder(a, p.ID) },
					})
					v.Button(i18n.T("shell.overview.btn_edit"), components.ButtonOptions{
						Variant: components.VariantGhost,
						Size:    components.SizeSmall,
						Icon:    icons.Edit,
						OnClick: func() { requestDialog(a, editProjectParam, p.ID) },
					})
					archiveLabel, archiveIcon := i18n.T("shell.overview.btn_archive"), icons.Archive
					if archived {
						archiveLabel, archiveIcon = i18n.T("shell.overview.btn_unarchive"), icons.ArchiveRestore
					}
					v.Button(archiveLabel, components.ButtonOptions{
						Variant: components.VariantGhost,
						Size:    components.SizeSmall,
						Icon:    archiveIcon,
						OnClick: func() { page.archive(a, *p) },
					})
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
