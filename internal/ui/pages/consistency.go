package pages

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/reconcile"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// Consistency is the file consistency panel of §16.5.
type Consistency struct {
	Services *Services

	mu       sync.Mutex
	loading  bool
	loaded   bool
	report   reconcile.Report
	scanErr  error
	results  reconcile.ApplyResult
	applyErr error
	applying bool

	reattachOpen bool
	reattachNote string
	reattachPath string
}

// Page renders the three difference groups and their repairs.
func (s *Consistency) Page(v *components.UI, r ports.Route) {
	projectID := ""
	if s.Services.App != nil {
		if p := s.Services.App.Project(); p != nil {
			projectID = p.ID
		}
	}
	if projectID == "" {
		v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth, Children: func() {
			v.PageHeader(components.PageHeaderOptions{Title: "File Consistency"})
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Scan,
				Title:  "No project is open",
				Reason: "The consistency check compares one project's note files with its database. Open a project first.",
			})
		}})
		return
	}
	s.load(projectID)
	s.mu.Lock()
	report, loaded, loading, scanErr := s.report, s.loaded, s.loading, s.scanErr
	results, applyErr, applying := s.results, s.applyErr, s.applying
	s.mu.Unlock()

	v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth * 1.4, Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       "File Consistency",
			Description: "Compares note files on disk with the database. Nothing is changed until you choose a repair.",
		})
		if scanErr != nil {
			v.Banner(components.BannerOptions{Tone: components.Danger, Title: "The scan failed", Message: scanErr.Error()})
		}
		if applyErr != nil {
			v.Banner(components.BannerOptions{Tone: components.Danger, Title: "Some repairs failed", Message: applyErr.Error()})
		}
		v.Toolbar("Consistency actions", func() {
			v.Button("Scan again", components.ButtonOptions{
				Icon:    icons.Refresh,
				Loading: loading || applying,
				OnClick: func() { s.rescan(projectID) },
			})
			v.Spacer()
			ui.Text(v.C, scanTime(report.GeneratedAt)).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextSecondary))
		})

		if !loaded && loading {
			v.Skeleton(components.SkeletonOptions{Lines: 6})
			return
		}
		v.Card(components.CardOptions{Title: fmt.Sprintf("Orphans (%d)", len(report.Orphans)), Description: "Files on disk with no note row."}, func() {
			ui.Column(v.C).Gap(tokens.ComponentGap).Children(func() {
				if len(report.Orphans) == 0 {
					v.StatusIndicator(components.Success, "No orphan files")
				}
				for _, o := range report.Orphans {
					orphan := o
					ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
						ui.Text(v.C, orphan.RelPath).Grow(1).Font(tokens.SystemFonts.Mono)
						ui.Text(v.C, humanBytes(orphan.Size)).FontFeatures("tnum")
						v.Button("Register", components.ButtonOptions{
							Size: components.SizeSmall,
							OnClick: func() {
								s.apply(projectID, []reconcile.Action{{Kind: reconcile.ActionAdopt, RelPath: orphan.RelPath}})
							},
						})
					})
				}
				if len(report.Orphans) > 1 {
					v.Button("Register all orphans", components.ButtonOptions{
						Variant: components.VariantSecondary,
						Size:    components.SizeSmall,
						OnClick: func() { s.adoptAll(projectID) },
					})
				}
			})
		})

		v.Card(components.CardOptions{Title: fmt.Sprintf("Missing (%d)", len(report.Missing)), Description: "Note rows whose body file is gone."}, func() {
			ui.Column(v.C).Gap(tokens.ComponentGap).Children(func() {
				if len(report.Missing) == 0 {
					v.StatusIndicator(components.Success, "No missing files")
				}
				for _, m := range report.Missing {
					missing := m
					ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
						ui.Text(v.C, missing.Title).Grow(1)
						ui.Text(v.C, missing.FilePath).Font(tokens.SystemFonts.Mono)
						v.Button("Recreate", components.ButtonOptions{
							Size: components.SizeSmall,
							OnClick: func() {
								s.apply(projectID, []reconcile.Action{{Kind: reconcile.ActionRecreate, NoteID: missing.NoteID}})
							},
						})
						v.Button("Mark broken", components.ButtonOptions{
							Size: components.SizeSmall,
							OnClick: func() {
								s.apply(projectID, []reconcile.Action{{Kind: reconcile.ActionMarkBroken, NoteID: missing.NoteID}})
							},
						})
						v.Button("Reattach…", components.ButtonOptions{
							Size: components.SizeSmall,
							OnClick: func() {
								s.reattachOpen = true
								s.reattachNote = missing.NoteID
								s.reattachPath = missing.FilePath
							},
						})
					})
				}
				if len(report.Missing) > 1 {
					v.Button("Mark all missing as broken", components.ButtonOptions{
						Variant: components.VariantSecondary,
						Size:    components.SizeSmall,
						OnClick: func() { s.markAllBroken(projectID) },
					})
				}
			})
		})

		v.Card(components.CardOptions{Title: fmt.Sprintf("Modified (%d)", len(report.Modified)), Description: "The body file changed outside ResearchOS."}, func() {
			ui.Column(v.C).Gap(tokens.ComponentGap).Children(func() {
				if len(report.Modified) == 0 {
					v.StatusIndicator(components.Success, "No external modifications")
				}
				for _, f := range report.Modified {
					file := f
					ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
						ui.Text(v.C, file.FilePath).Grow(1).Font(tokens.SystemFonts.Mono)
						v.Button("Accept external edit", components.ButtonOptions{
							Size: components.SizeSmall,
							OnClick: func() {
								s.apply(projectID, []reconcile.Action{{Kind: reconcile.ActionAcceptExternalEdit, NoteID: file.NoteID}})
							},
						})
					})
				}
			})
		})

		s.reattachDialog(v, projectID)

		if len(results.Items) > 0 {
			v.Card(components.CardOptions{Title: "Repair results"}, func() {
				ui.Column(v.C).Gap(tokens.ComponentGap).Children(func() {
					for _, item := range results.Items {
						tone := components.Success
						text := "Applied " + string(item.Action.Kind)
						if item.Err != nil {
							tone = components.Danger
							text = "Failed " + string(item.Action.Kind) + ": " + item.Err.Error()
						}
						v.StatusIndicator(tone, text)
					}
				})
			})
		}
	}})
}

// reattachDialog asks for the relative path of the recovered file.
func (s *Consistency) reattachDialog(v *components.UI, projectID string) {
	open := s.reattachOpen
	if !open {
		return
	}
	v.Dialog(&s.reattachOpen, components.DialogOptions{
		Title:       "Reattach note file",
		Description: "Enter the path of the recovered file, relative to the project's notes folder.",
		Actions: func() {
			if v.Button("Cancel", components.ButtonOptions{}).Clicked() {
				s.reattachOpen = false
			}
			if v.Button("Reattach", components.ButtonOptions{Variant: components.VariantPrimary}).Clicked() {
				note := s.reattachNote
				path := s.reattachPath
				s.reattachOpen = false
				s.apply(projectID, []reconcile.Action{{Kind: reconcile.ActionReattach, NoteID: note, NewRelPath: path}})
			}
		},
	}, func() {
		v.Input(&s.reattachPath, components.FieldOptions{Label: "Relative path", Placeholder: "notes/folding-log.md"})
	})
}

// load scans once.
func (s *Consistency) load(projectID string) {
	s.mu.Lock()
	if s.loading || s.loaded {
		s.mu.Unlock()
		return
	}
	s.loading = true
	s.mu.Unlock()
	s.Services.App.RunTask("Scan note files", func(ctx context.Context, t *app.Task) error {
		report, err := s.Services.Reconcile.Scan(ctx, projectID)
		s.mu.Lock()
		s.loading = false
		s.loaded = true
		if err != nil {
			s.scanErr = err
		} else {
			s.report = report
			s.scanErr = nil
		}
		s.mu.Unlock()
		return err
	})
}

// rescan forces a fresh scan.
func (s *Consistency) rescan(projectID string) {
	s.mu.Lock()
	s.loaded = false
	s.scanErr = nil
	s.results = reconcile.ApplyResult{}
	s.mu.Unlock()
	s.load(projectID)
}

// adoptAll registers every orphan in one apply call.
func (s *Consistency) adoptAll(projectID string) {
	s.mu.Lock()
	orphans := append([]reconcile.Orphan(nil), s.report.Orphans...)
	s.mu.Unlock()
	actions := make([]reconcile.Action, len(orphans))
	for i, o := range orphans {
		actions[i] = reconcile.Action{Kind: reconcile.ActionAdopt, RelPath: o.RelPath}
	}
	s.apply(projectID, actions)
}

// markAllBroken marks every missing note as broken.
func (s *Consistency) markAllBroken(projectID string) {
	s.mu.Lock()
	missing := append([]reconcile.Missing(nil), s.report.Missing...)
	s.mu.Unlock()
	actions := make([]reconcile.Action, len(missing))
	for i, m := range missing {
		actions[i] = reconcile.Action{Kind: reconcile.ActionMarkBroken, NoteID: m.NoteID}
	}
	s.apply(projectID, actions)
}

// apply runs repairs and keeps partial results.
func (s *Consistency) apply(projectID string, actions []reconcile.Action) {
	if len(actions) == 0 {
		return
	}
	s.mu.Lock()
	s.applying = true
	s.applyErr = nil
	s.mu.Unlock()
	s.Services.App.RunTask("Repair note files", func(ctx context.Context, t *app.Task) error {
		res, err := s.Services.Reconcile.Apply(ctx, projectID, actions)
		s.mu.Lock()
		s.applying = false
		s.results = res
		if err != nil {
			s.applyErr = err
		}
		s.mu.Unlock()
		if err == nil && len(res.Items) > 0 {
			s.mu.Lock()
			s.loaded = false
			s.mu.Unlock()
			s.load(projectID)
		}
		return err
	})
}

// scanTime formats the last scan time.
func scanTime(t time.Time) string {
	if t.IsZero() {
		return "Not scanned yet"
	}
	return "Last scan: " + t.Format("2006-01-02 15:04")
}
