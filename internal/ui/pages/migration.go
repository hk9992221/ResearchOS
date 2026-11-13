package pages

import (
	"context"
	"strconv"
	"sync"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/diag"
	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
	"researchos/internal/workspace"
)

// Migration is the workspace migration page of §16.3: plan a copy to a new
// root, run it without blocking the UI, show its phase and cancel it at a safe
// boundary.
type Migration struct {
	Services *Services

	mu      sync.Mutex
	target  string
	exports bool
	plan    *workspace.MigratePlan
	entry   string
	task    *app.Task
	stage   string
	done    string
	cancel  string
	bytes   string
	files   string

	schemaGlobal   diag.DBReport
	schemaProjects []diag.ProjectReport
	schemaLoaded   bool
	schemaLoading  bool
	schemaErr      error
}

// Page renders the migration flow.
func (m *Migration) Page(v *components.UI, r ports.Route) {
	if m.Services.Workspace == nil {
		v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth, Children: func() {
			v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.migration.title")})
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.FolderOpen,
				Title:  i18n.T("pages.migration.empty_no_ws_title"),
				Reason: i18n.T("pages.migration.empty_no_ws_reason"),
			})
		}})
		return
	}
	if m.target == "" && m.Services.Setup != nil {
		m.target = m.Services.Setup.CurrentRoot()
	}
	m.settle()
	m.mu.Lock()
	plan, task, stage, done, cancel, bytesText, filesText := m.plan, m.task, m.stage, m.done, m.cancel, m.bytes, m.files
	m.mu.Unlock()
	var taskErr error
	if task != nil {
		if snap := task.Snapshot(); snap.Finished {
			taskErr = snap.Err
		}
	}

	v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth * 1.3, Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.migration.title"),
			Description: i18n.T("pages.migration.description"),
		})
		if done != "" {
			v.Banner(components.BannerOptions{Tone: components.Success, Title: i18n.T("pages.migration.banner_done"), Message: done})
		}
		if taskErr != nil {
			v.Banner(components.BannerOptions{Tone: components.Danger, Title: i18n.T("pages.migration.banner_failed"), Message: errtext.Text(taskErr)})
		}
		if cancel != "" {
			v.Banner(components.BannerOptions{Tone: components.Warning, Title: i18n.T("pages.migration.banner_cancel_blocked"), Message: cancel})
		}
		if task != nil && !task.Snapshot().Finished {
			v.Card(components.CardOptions{Title: i18n.T("pages.migration.card_migrating")}, func() {
				ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
					ui.Text(v.C, stage).FontSize(tokens.Typography.Body.Size)
					v.Progress(components.ProgressOptions{Value: -1, Label: i18n.T("pages.migration.progress_in_progress")})
					if bytesText != "" || filesText != "" {
						ui.Row(v.C).Gap(tokens.SectionGap).Children(func() {
							ui.Text(v.C, filesText).Font(tokens.SystemFonts.Mono)
							ui.Text(v.C, bytesText).Font(tokens.SystemFonts.Mono)
						})
					}
					v.Button(i18n.T("pages.migration.btn_cancel"), components.ButtonOptions{
						Variant: components.VariantDanger,
						OnClick: func() { m.cancelMigration() },
					})
				})
			})
			return
		}
		v.Card(components.CardOptions{Title: i18n.T("pages.migration.card_new_location")}, func() {
			ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
				in := v.Input(&m.target, components.FieldOptions{
					Label:       i18n.T("pages.migration.field_target_label"),
					Description: i18n.T("pages.migration.field_target_desc"),
					Placeholder: "E:/ResearchOS-Workspace",
				})
				if in.Changed() {
					m.mu.Lock()
					m.plan = nil
					m.mu.Unlock()
				}
				ui.Row(v.C).Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
					v.Button(i18n.T("pages.common.btn_browse"), components.ButtonOptions{
						Variant: components.VariantSecondary,
						Icon:    icons.FolderOpen,
						OnClick: func() { m.browse() },
					})
					v.Checkbox(&m.exports, i18n.T("pages.migration.check_include_exports"), components.ToggleOptions{})
					v.Button(i18n.T("pages.migration.btn_plan"), components.ButtonOptions{
						Variant:  components.VariantPrimary,
						Disabled: m.target == "",
						OnClick:  func() { m.planMigration() },
					})
				})
			})
		})
		v.Card(components.CardOptions{Title: i18n.T("pages.migration.card_schema"), Description: i18n.T("pages.migration.card_schema_desc")}, func() {
			ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
				if !m.schemaLoaded {
					v.Button(i18n.T("pages.migration.btn_check_schema"), components.ButtonOptions{
						Icon:    icons.Scan,
						Loading: m.schemaLoading,
						OnClick: func() { m.checkSchema() },
					})
					if m.schemaErr != nil {
						v.StatusIndicator(components.Danger, errtext.Text(m.schemaErr))
					}
					return
				}
				m.row(v, i18n.T("pages.migration.row_global_db"), i18n.Tf("pages.migration.schema_version", map[string]string{
					"current": strconv.Itoa(m.schemaGlobal.SchemaVersion),
					"target":  strconv.Itoa(m.schemaGlobal.TargetVersion),
				}))
				for _, p := range m.schemaProjects {
					version := i18n.T("pages.common.status_unavailable")
					if p.DB != nil {
						version = i18n.Tf("pages.migration.schema_version", map[string]string{
							"current": strconv.Itoa(p.DB.SchemaVersion),
							"target":  strconv.Itoa(p.DB.TargetVersion),
						})
					}
					m.row(v, i18n.Tf("pages.migration.row_project", map[string]string{"id": p.ProjectID}), version)
				}
			})
		})

		if plan != nil {
			v.Card(components.CardOptions{Title: i18n.T("pages.migration.card_plan"), Description: i18n.Tf("pages.migration.plan_paths", map[string]string{
				"source": plan.SourceRoot,
				"target": plan.TargetRoot,
			})}, func() {
				ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
					m.row(v, i18n.T("pages.migration.row_files"), strconv.Itoa(plan.Files))
					m.row(v, i18n.T("pages.migration.row_bytes"), humanBytes(plan.Bytes))
					m.row(v, i18n.T("pages.migration.row_verification"), i18n.T("pages.migration.verification_value"))
					m.row(v, i18n.T("pages.migration.row_old_folder"), i18n.T("pages.migration.old_folder_value"))
					v.Button(i18n.T("pages.migration.btn_start"), components.ButtonOptions{
						Variant: components.VariantDanger,
						OnClick: func() { m.start() },
					})
				})
			})
		}
	}})
}

func (m *Migration) row(v *components.UI, label, value string) {
	p := v.T.Palette
	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
		ui.Text(v.C, label).Width(140).FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(p.TextSecondary))
		ui.Text(v.C, value).Grow(1).FontSize(tokens.Typography.Body.Size).TextColor(mygo.Color(p.TextPrimary))
	})
}

func (m *Migration) browse() {
	if m.Services.ChooseFolder == nil {
		return
	}
	if dir, err := m.Services.ChooseFolder(); err == nil && dir != "" {
		m.mu.Lock()
		m.target = dir
		m.plan = nil
		m.mu.Unlock()
	}
}

// planMigration probes the target without touching it.
func (m *Migration) planMigration() {
	m.Services.App.RunTask(i18n.T("pages.migration.task_plan"), func(ctx context.Context, t *app.Task) error {
		m.mu.Lock()
		target := m.target
		exports := m.exports
		m.mu.Unlock()
		plan, err := m.Services.Workspace.PlanMigration(ctx, workspace.MigrateOptions{
			NewRoot:        target,
			IncludeExports: exports,
			KeepSource:     true,
		})
		if err != nil {
			return err
		}
		m.mu.Lock()
		m.plan = plan
		m.done = ""
		m.cancel = ""
		m.mu.Unlock()
		return nil
	})
}

// start runs the copy in a task and mirrors the progress channel into it.
func (m *Migration) start() {
	m.mu.Lock()
	plan := m.plan
	m.mu.Unlock()
	if plan == nil {
		return
	}
	task := m.Services.App.RunTask(i18n.T("pages.migration.task_migrate"), func(ctx context.Context, t *app.Task) error {
		ch, err := m.Services.Workspace.Migrate(ctx, plan.ID)
		if err != nil {
			return err
		}
		for p := range ch {
			m.mu.Lock()
			m.stage = phaseLabel(p.Phase)
			m.files = i18n.Tf("pages.migration.progress_files", map[string]string{
				"done":  strconv.Itoa(p.FilesDone),
				"total": strconv.Itoa(p.FilesTotal),
			})
			m.bytes = i18n.Tf("pages.migration.progress_bytes", map[string]string{
				"done":  humanBytes(p.BytesDone),
				"total": humanBytes(p.BytesTotal),
			})
			m.mu.Unlock()
			t.Stage(phaseLabel(p.Phase))
			t.Progress(p.FilesDone, p.FilesTotal)
			if p.Err != nil {
				return p.Err
			}
		}
		m.mu.Lock()
		m.done = i18n.Tf("pages.migration.done_message", map[string]string{"path": plan.TargetRoot})
		m.stage = ""
		m.mu.Unlock()
		if m.Services.OnMigrated != nil {
			m.Services.OnMigrated(ctx, plan.TargetRoot)
		}
		return nil
	})
	m.mu.Lock()
	m.task = task
	m.stage = i18n.T("pages.migration.stage_starting")
	m.done = ""
	m.cancel = ""
	m.mu.Unlock()
}

// cancelMigration asks the service to stop before the commit. A migration past
// commit cannot be canceled; the message says so.
func (m *Migration) cancelMigration() {
	m.mu.Lock()
	plan := m.plan
	task := m.task
	m.mu.Unlock()
	if plan == nil {
		return
	}
	if err := m.Services.Workspace.CancelMigration(context.Background(), plan.ID); err != nil {
		m.mu.Lock()
		m.cancel = errtext.Text(err)
		m.mu.Unlock()
		return
	}
	if task != nil {
		task.Cancel()
	}
}

// checkSchema reads the database versions through the diagnostic service.
func (m *Migration) checkSchema() {
	if m.Services.Diagnostics == nil {
		return
	}
	m.mu.Lock()
	m.schemaLoading = true
	m.mu.Unlock()
	m.Services.App.RunTask(i18n.T("pages.migration.task_check_schema"), func(ctx context.Context, t *app.Task) error {
		rep, err := m.Services.Diagnostics.Run(ctx, diag.Options{})
		m.mu.Lock()
		m.schemaLoading = false
		m.schemaLoaded = err == nil
		m.schemaGlobal = rep.Global
		m.schemaProjects = rep.Projects
		m.schemaErr = err
		m.mu.Unlock()
		return err
	})
}

// settle mirrors a finished task's error into the page state.
func (m *Migration) settle() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.task == nil {
		return
	}
	snap := m.task.Snapshot()
	if !snap.Finished {
		return
	}
	if snap.Err != nil && m.done == "" {
		m.done = ""
		m.cancel = ""
		m.stage = ""
	}
}

func phaseLabel(phase string) string {
	switch phase {
	case "probe":
		return i18n.T("pages.migration.phase_probe")
	case "lock":
		return i18n.T("pages.migration.phase_lock")
	case "copy":
		return i18n.T("pages.migration.phase_copy")
	case "verify":
		return i18n.T("pages.migration.phase_verify")
	case "commit":
		return i18n.T("pages.migration.phase_commit")
	case "reopen":
		return i18n.T("pages.migration.phase_reopen")
	case "done":
		return i18n.T("pages.migration.phase_done")
	}
	return phase
}
