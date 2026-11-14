package pages

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/reconcile"
	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
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
		v.PageContainer(components.PageContainerOptions{Children: func() {
			v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.consistency.title")})
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Scan,
				Title:  i18n.T("pages.common.empty_no_project_title"),
				Reason: i18n.T("pages.consistency.empty_no_project_reason"),
			})
		}})
		return
	}
	s.load(projectID)
	s.mu.Lock()
	report, loaded, loading, scanErr := s.report, s.loaded, s.loading, s.scanErr
	results, applyErr, applying := s.results, s.applyErr, s.applying
	s.mu.Unlock()

	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.consistency.title"),
			Description: i18n.T("pages.consistency.description"),
		})
		if scanErr != nil {
			v.Banner(components.BannerOptions{Tone: components.Danger, Title: i18n.T("pages.consistency.banner_scan_failed"), Message: errtext.Text(scanErr)})
		}
		if applyErr != nil {
			v.Banner(components.BannerOptions{Tone: components.Danger, Title: i18n.T("pages.consistency.banner_repair_failed"), Message: errtext.Text(applyErr)})
		}
		v.Toolbar(i18n.T("pages.consistency.toolbar_label"), func() {
			v.Button(i18n.T("pages.consistency.btn_rescan"), components.ButtonOptions{
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
		v.Card(components.CardOptions{Title: i18n.Tf("pages.consistency.card_orphans", map[string]string{"count": strconv.Itoa(len(report.Orphans))}), Description: i18n.T("pages.consistency.card_orphans_desc")}, func() {
			ui.Column(v.C).Gap(tokens.ComponentGap).Children(func() {
				if len(report.Orphans) == 0 {
					v.StatusIndicator(components.Success, i18n.T("pages.consistency.orphans_none"))
				}
				for _, o := range report.Orphans {
					orphan := o
					ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
						ui.Text(v.C, orphan.RelPath).Grow(1).Font(tokens.SystemFonts.Mono)
						ui.Text(v.C, components.HumanBytes(orphan.Size)).FontFeatures("tnum")
						v.Button(i18n.T("pages.consistency.btn_register"), components.ButtonOptions{
							Size: components.SizeSmall,
							OnClick: func() {
								s.apply(projectID, []reconcile.Action{{Kind: reconcile.ActionAdopt, RelPath: orphan.RelPath}})
							},
						})
					})
				}
				if len(report.Orphans) > 1 {
					v.Button(i18n.T("pages.consistency.btn_register_all"), components.ButtonOptions{
						Variant: components.VariantSecondary,
						Size:    components.SizeSmall,
						OnClick: func() { s.adoptAll(projectID) },
					})
				}
			})
		})

		v.Card(components.CardOptions{Title: i18n.Tf("pages.consistency.card_missing", map[string]string{"count": strconv.Itoa(len(report.Missing))}), Description: i18n.T("pages.consistency.card_missing_desc")}, func() {
			ui.Column(v.C).Gap(tokens.ComponentGap).Children(func() {
				if len(report.Missing) == 0 {
					v.StatusIndicator(components.Success, i18n.T("pages.consistency.missing_none"))
				}
				for _, m := range report.Missing {
					missing := m
					ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
						ui.Text(v.C, missing.Title).Grow(1)
						ui.Text(v.C, missing.FilePath).Font(tokens.SystemFonts.Mono)
						v.Button(i18n.T("pages.consistency.btn_recreate"), components.ButtonOptions{
							Size: components.SizeSmall,
							OnClick: func() {
								s.apply(projectID, []reconcile.Action{{Kind: reconcile.ActionRecreate, NoteID: missing.NoteID}})
							},
						})
						v.Button(i18n.T("pages.consistency.btn_mark_broken"), components.ButtonOptions{
							Size: components.SizeSmall,
							OnClick: func() {
								s.apply(projectID, []reconcile.Action{{Kind: reconcile.ActionMarkBroken, NoteID: missing.NoteID}})
							},
						})
						v.Button(i18n.T("pages.consistency.btn_reattach"), components.ButtonOptions{
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
					v.Button(i18n.T("pages.consistency.btn_mark_all_broken"), components.ButtonOptions{
						Variant: components.VariantSecondary,
						Size:    components.SizeSmall,
						OnClick: func() { s.markAllBroken(projectID) },
					})
				}
			})
		})

		v.Card(components.CardOptions{Title: i18n.Tf("pages.consistency.card_modified", map[string]string{"count": strconv.Itoa(len(report.Modified))}), Description: i18n.T("pages.consistency.card_modified_desc")}, func() {
			ui.Column(v.C).Gap(tokens.ComponentGap).Children(func() {
				if len(report.Modified) == 0 {
					v.StatusIndicator(components.Success, i18n.T("pages.consistency.modified_none"))
				}
				for _, f := range report.Modified {
					file := f
					ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
						ui.Text(v.C, file.FilePath).Grow(1).Font(tokens.SystemFonts.Mono)
						v.Button(i18n.T("pages.consistency.btn_accept_edit"), components.ButtonOptions{
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
			v.Card(components.CardOptions{Title: i18n.T("pages.consistency.card_results")}, func() {
				ui.Column(v.C).Gap(tokens.ComponentGap).Children(func() {
					for _, item := range results.Items {
						tone := components.Success
						text := i18n.Tf("pages.consistency.result_applied", map[string]string{"kind": actionKindLabel(item.Action.Kind)})
						if item.Err != nil {
							tone = components.Danger
							text = i18n.Tf("pages.consistency.result_failed", map[string]string{
								"kind":  actionKindLabel(item.Action.Kind),
								"error": errtext.Text(item.Err),
							})
						}
						v.StatusIndicator(tone, text)
					}
				})
			})
		}
	}})
}

// actionKindLabel names a reconcile action kind for display. A kind the map
// does not name, as one added by a newer build, falls back to the raw kind.
func actionKindLabel(kind reconcile.ActionKind) string {
	switch kind {
	case reconcile.ActionAdopt:
		return i18n.T("pages.consistency.kind_adopt")
	case reconcile.ActionAcceptExternalEdit:
		return i18n.T("pages.consistency.kind_accept_external_edit")
	case reconcile.ActionMarkBroken:
		return i18n.T("pages.consistency.kind_mark_broken")
	case reconcile.ActionReattach:
		return i18n.T("pages.consistency.kind_reattach")
	case reconcile.ActionRecreate:
		return i18n.T("pages.consistency.kind_recreate")
	}
	return string(kind)
}

// reattachDialog asks for the relative path of the recovered file.
func (s *Consistency) reattachDialog(v *components.UI, projectID string) {
	open := s.reattachOpen
	if !open {
		return
	}
	v.Dialog(&s.reattachOpen, components.DialogOptions{
		Title:       i18n.T("pages.consistency.dialog_reattach_title"),
		Description: i18n.T("pages.consistency.dialog_reattach_desc"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				s.reattachOpen = false
			}
			if v.Button(i18n.T("pages.consistency.dialog_reattach_btn"), components.ButtonOptions{Variant: components.VariantPrimary}).Clicked() {
				note := s.reattachNote
				path := s.reattachPath
				s.reattachOpen = false
				s.apply(projectID, []reconcile.Action{{Kind: reconcile.ActionReattach, NoteID: note, NewRelPath: path}})
			}
		},
	}, func() {
		v.Input(&s.reattachPath, components.FieldOptions{Label: i18n.T("pages.consistency.dialog_reattach_field"), Placeholder: "notes/folding-log.md"})
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
	s.Services.App.RunTask(i18n.T("pages.consistency.task_scan"), func(ctx context.Context, t *app.Task) error {
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
	s.Services.App.RunTask(i18n.T("pages.consistency.task_repair"), func(ctx context.Context, t *app.Task) error {
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
		return i18n.T("pages.consistency.scan_never")
	}
	return i18n.Tf("pages.consistency.scan_time", map[string]string{"time": t.Format("2006-01-02 15:04")})
}
