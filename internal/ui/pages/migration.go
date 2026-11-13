package pages

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/diag"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
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
			v.PageHeader(components.PageHeaderOptions{Title: "Move workspace"})
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.FolderOpen,
				Title:  "No workspace is open",
				Reason: "Open or set up a workspace before moving it.",
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
			Title:       "Move workspace",
			Description: "Copy the workspace into an empty folder, verify every file, then switch to the copy. The old folder is kept as a backup.",
		})
		if done != "" {
			v.Banner(components.BannerOptions{Tone: components.Success, Title: "Migration finished", Message: done})
		}
		if taskErr != nil {
			v.Banner(components.BannerOptions{Tone: components.Danger, Title: "Migration failed", Message: taskErr.Error()})
		}
		if cancel != "" {
			v.Banner(components.BannerOptions{Tone: components.Warning, Title: "Cancel not possible", Message: cancel})
		}
		if task != nil && !task.Snapshot().Finished {
			v.Card(components.CardOptions{Title: "Migrating"}, func() {
				ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
					ui.Text(v.C, stage).FontSize(tokens.Typography.Body.Size)
					v.Progress(components.ProgressOptions{Value: -1, Label: "Migration in progress"})
					if bytesText != "" || filesText != "" {
						ui.Row(v.C).Gap(tokens.SectionGap).Children(func() {
							ui.Text(v.C, filesText).Font(tokens.SystemFonts.Mono)
							ui.Text(v.C, bytesText).Font(tokens.SystemFonts.Mono)
						})
					}
					v.Button("Cancel migration", components.ButtonOptions{
						Variant: components.VariantDanger,
						OnClick: func() { m.cancelMigration() },
					})
				})
			})
			return
		}
		v.Card(components.CardOptions{Title: "New location"}, func() {
			ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
				in := v.Input(&m.target, components.FieldOptions{
					Label:       "Target folder",
					Description: "It must be empty or not exist yet. Migration never merges two workspaces.",
					Placeholder: "E:/ResearchOS-Workspace",
				})
				if in.Changed() {
					m.mu.Lock()
					m.plan = nil
					m.mu.Unlock()
				}
				ui.Row(v.C).Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
					v.Button("Browse…", components.ButtonOptions{
						Variant: components.VariantSecondary,
						Icon:    icons.FolderOpen,
						OnClick: func() { m.browse() },
					})
					v.Checkbox(&m.exports, "Include exports", components.ToggleOptions{})
					v.Button("Plan migration", components.ButtonOptions{
						Variant:  components.VariantPrimary,
						Disabled: m.target == "",
						OnClick:  func() { m.planMigration() },
					})
				})
			})
		})
		v.Card(components.CardOptions{Title: "Schema versions", Description: "A backup is written before a database migrates."}, func() {
			ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
				if !m.schemaLoaded {
					v.Button("Check schema", components.ButtonOptions{
						Icon:    icons.Scan,
						Loading: m.schemaLoading,
						OnClick: func() { m.checkSchema() },
					})
					if m.schemaErr != nil {
						v.StatusIndicator(components.Danger, m.schemaErr.Error())
					}
					return
				}
				m.row(v, "Global database", fmt.Sprintf("version %d / %d", m.schemaGlobal.SchemaVersion, m.schemaGlobal.TargetVersion))
				for _, p := range m.schemaProjects {
					version := "unavailable"
					if p.DB != nil {
						version = fmt.Sprintf("version %d / %d", p.DB.SchemaVersion, p.DB.TargetVersion)
					}
					m.row(v, "Project "+p.ProjectID, version)
				}
			})
		})

		if plan != nil {
			v.Card(components.CardOptions{Title: "Plan", Description: plan.SourceRoot + " → " + plan.TargetRoot}, func() {
				ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
					m.row(v, "Files", strconv.Itoa(plan.Files))
					m.row(v, "Bytes", humanBytes(plan.Bytes))
					m.row(v, "Verification", "Full SHA-256 of every file")
					m.row(v, "Old folder", "Kept as a backup")
					v.Button("Start migration", components.ButtonOptions{
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
	m.Services.App.RunTask("Plan workspace migration", func(ctx context.Context, t *app.Task) error {
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
	task := m.Services.App.RunTask("Migrate workspace", func(ctx context.Context, t *app.Task) error {
		ch, err := m.Services.Workspace.Migrate(ctx, plan.ID)
		if err != nil {
			return err
		}
		for p := range ch {
			m.mu.Lock()
			m.stage = phaseLabel(p.Phase)
			m.files = fmt.Sprintf("%d / %d files", p.FilesDone, p.FilesTotal)
			m.bytes = fmt.Sprintf("%s / %s", humanBytes(p.BytesDone), humanBytes(p.BytesTotal))
			m.mu.Unlock()
			t.Stage(phaseLabel(p.Phase))
			t.Progress(p.FilesDone, p.FilesTotal)
			if p.Err != nil {
				return p.Err
			}
		}
		m.mu.Lock()
		m.done = "The workspace now lives at " + plan.TargetRoot + ". The previous folder was kept as a backup."
		m.stage = ""
		m.mu.Unlock()
		if m.Services.OnMigrated != nil {
			m.Services.OnMigrated(ctx, plan.TargetRoot)
		}
		return nil
	})
	m.mu.Lock()
	m.task = task
	m.stage = "Starting"
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
		m.cancel = err.Error()
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
	m.Services.App.RunTask("Check schema versions", func(ctx context.Context, t *app.Task) error {
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
		return "Probing the target"
	case "lock":
		return "Locking the workspace"
	case "copy":
		return "Copying files"
	case "verify":
		return "Verifying the copy"
	case "commit":
		return "Switching to the new root"
	case "reopen":
		return "Reopening the workspace"
	case "done":
		return "Done"
	}
	return phase
}
