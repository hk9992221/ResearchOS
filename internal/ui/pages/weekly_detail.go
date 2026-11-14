package pages

// The Weekly Report detail is the SPEC-09 §6.2 work area: the seven-section
// Markdown of one report, its activity digest and its validation findings, with
// the draft → confirmed lifecycle, autosave and export.
//
// The three tabs of §6.1 are 报告 / 活动汇总 / 校验. The report tab splits into a
// collapsible outline and the editor (draft) or the rendered view (confirmed).
// Autosave is the 3 second debounce of §6.2 and carries the content hash the
// service compares, so an edit made elsewhere comes back as a conflict instead
// of a silent overwrite. IME composition pauses the timer.

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"

	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/formula"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
	"researchos/internal/weeklyreview"
)

// weeklySaveDelay is the §6.2 debounce: a save runs three seconds after the
// last edit.
const weeklySaveDelay = 3 * time.Second

// weeklyOutlineWidth is the first pane of the outline split.
const weeklyOutlineWidth float32 = 240

// The two views of a draft body.
const (
	weeklyModeEdit = iota
	weeklyModePreview
)

// weeklyDigestRow is one digest entry with its rendered section.
type weeklyDigestRow struct {
	Section weeklyreview.SectionKey
	Item    weeklyreview.DigestItem
}

// WeeklyReportView renders one report.
type WeeklyReportView struct {
	Services *Services
	shared   *weeklyShared

	mu        sync.Mutex
	loading   bool
	loadedID  string
	project   string
	version   int
	report    *weeklyreview.WeeklyReport
	digest    *weeklyreview.ActivityDigest
	problems  []string
	needReset bool

	saving   bool
	saveErr  string
	conflict bool
	savedAt  time.Time

	regenerating bool
	regenErr     string
	regenOld     string

	confirming bool
	confirmErr string
	reverting  bool
	revertErr  string

	exporting  bool
	exportErr  string
	exportPath string

	// Frame-only state: the tab, the editor, the dialogs and the local ack
	// set. Locally acknowledged fingerprints are sent with Confirm, which is
	// the only call that persists acks (SPEC-09 §5.3 Confirm).
	tab            int
	ed             *editor.State
	edFor          string
	mode           int
	lastVersion    uint64
	lastChange     time.Time
	composing      bool
	wasFocused     bool
	savedPending   bool
	showOutline    bool
	outlineOpen    bool
	splitSize      *float32
	provOpen       bool
	anchor         string
	anchorVer      uint64
	acks           map[string]bool
	ackNotes       map[string]string
	ackFor         string
	ackNoteInput   string
	confirmOpen    bool
	revertOpen     bool
	regenOpen      bool
	diffOpen       bool
	diffState      *editor.DiffState
	insertOpen     bool
	exportOpen     bool
	exportDir      string
	exportFmtLabel string
	exportFront    bool
	exportReady    bool
	exportOver     bool
	exportHandled  string
	md             *markdown.Options
	expanded       map[string]bool

	// skills back the regenerate dialog's picker and editor.
	skills          weeklyreview.SkillSet
	skillsLoading   bool
	skillsLoaded    bool
	regenSkillID    string
	regenSkillLabel string
	skillEditor     weeklySkillEditor
}

// Page renders one frame of the report detail.
func (d *WeeklyReportView) Page(v *components.UI, r ports.Route) {
	a := d.Services.App
	if a == nil {
		return
	}
	if d.Services.Weekly == nil {
		d.unavailable(v)
		return
	}
	project := a.Project()
	if project == nil {
		d.needsProject(v, a)
		return
	}
	if r.EntityID == "" {
		a.Navigate(ports.Route{Kind: app.RouteWeeklyReview})
		return
	}
	d.prepare()
	d.load(a, project.ID, r.EntityID)
	report := d.reportSnapshot()
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       weeklyHeader(report),
			Description: weeklyPeriodLine(report),
			Actions: func() {
				if v.Button(i18n.T("pages.weekly_review.btn_back"), components.ButtonOptions{
					Variant: components.VariantGhost,
					Icon:    icons.Back,
				}).Clicked() {
					d.saveNow(a, project.ID)
					a.Navigate(ports.Route{Kind: app.RouteWeeklyReview})
				}
				if report != nil {
					if v.Button(i18n.T("pages.weekly_review.btn_export"), components.ButtonOptions{
						Variant: components.VariantSecondary,
						Icon:    icons.ExternalLink,
					}).Clicked() {
						d.exportOpen = true
						d.exportErr = ""
						d.exportPath = ""
					}
					if report.Status == weeklyreview.StatusDraft && project.ArchivedAt == nil {
						if v.Button(i18n.T("pages.weekly_review.btn_confirm"), components.ButtonOptions{
							Variant:  components.VariantPrimary,
							Icon:     icons.Check,
							Disabled: d.unresolvedCount() > 0 || d.confirming,
						}).Clicked() {
							d.openConfirm(a)
						}
					}
					if report.Status == weeklyreview.StatusConfirmed && project.ArchivedAt == nil {
						if v.Button(i18n.T("pages.weekly_review.btn_revert"), components.ButtonOptions{
							Variant: components.VariantSecondary,
						}).Clicked() {
							d.revertOpen = true
							d.mu.Lock()
							d.revertErr = ""
							d.mu.Unlock()
						}
					}
				}
			},
		})
		if project.ArchivedAt != nil {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("pages.weekly_review.archived_title"),
				Message: i18n.T("pages.weekly_review.archived_detail"),
			})
		}
		d.banners(v)
		if report == nil {
			if d.blank() {
				v.Skeleton(components.SkeletonOptions{Lines: 8})
			}
			return
		}
		if r.Params["action"] == "export" && d.exportHandled != report.ID {
			d.exportOpen = true
			d.exportHandled = report.ID
		}
		v.Badge(weeklyStatusText(report.Status), weeklyStatusTone(report.Status))
		v.Tabs(&d.tab,
			i18n.T("pages.weekly_review.tab_report"),
			i18n.T("pages.weekly_review.tab_digest"),
			i18n.T("pages.weekly_review.tab_checks"),
		)
		switch d.tab {
		case 1:
			d.digestTab(v, a, report)
		case 2:
			d.checksTab(v, a, report, project.ArchivedAt == nil)
		default:
			d.reportTab(v, a, project.ID, report, project.ArchivedAt == nil)
		}
		d.ackDialog(v)
		d.confirmDialog(v, a, project.ID)
		d.revertDialog(v, a, project.ID)
		d.regenDialog(v, a, project.ID)
		d.diffDialog(v, a, project.ID)
		d.insertRefDialog(v)
		d.exportDialog(v, a, project.ID, report)
	}})
}

func (d *WeeklyReportView) unavailable(v *components.UI) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.weekly_review.detail_title")})
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.WeeklyReview,
			Title:  i18n.T("pages.weekly_review.disabled_title"),
			Reason: i18n.T("pages.weekly_review.disabled_reason"),
		})
	}})
}

func (d *WeeklyReportView) needsProject(v *components.UI, a *app.App) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.weekly_review.detail_title"),
			Description: i18n.T("pages.weekly_review.needs_project_desc"),
		})
		v.EmptyState(components.EmptyStateOptions{
			Icon:        icons.WeeklyReview,
			Title:       i18n.T("pages.weekly_review.needs_project_title"),
			Reason:      i18n.T("pages.weekly_review.needs_project_reason"),
			ActionLabel: i18n.T("pages.weekly_review.btn_projects"),
			OnAction:    func() { a.Navigate(ports.Route{Kind: app.RouteProjects}) },
		})
	}})
}

func (d *WeeklyReportView) prepare() {
	if d.acks == nil {
		d.acks = map[string]bool{}
	}
	if d.ackNotes == nil {
		d.ackNotes = map[string]string{}
	}
	if d.expanded == nil {
		d.expanded = map[string]bool{}
	}
	if d.splitSize == nil {
		size := weeklyOutlineWidth
		d.splitSize = &size
		d.showOutline = true
		d.outlineOpen = true
	}
	if !d.exportReady {
		d.exportFront = true
		d.exportReady = true
	}
	if d.md == nil {
		d.md = &markdown.Options{
			Extensions: parser.Default | parser.ExtDollarMath,
			Math:       formula.Block,
			InlineMath: formula.Inline,
			OnLink:     func(url string) { weeklyOpenRef(d.Services.App, url) },
		}
	}
}

func (d *WeeklyReportView) blank() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.loading && d.report == nil
}

func (d *WeeklyReportView) reportSnapshot() *weeklyreview.WeeklyReport {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.report
}

func (d *WeeklyReportView) digestSnapshot() *weeklyreview.ActivityDigest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.digest
}

// banners reports load failures, the unsaved write, the conflict and the
// machine-immutable confirmed state.
func (d *WeeklyReportView) banners(v *components.UI) {
	d.mu.Lock()
	problems := append([]string(nil), d.problems...)
	saveErr, conflict := d.saveErr, d.conflict
	report := d.report
	d.mu.Unlock()
	for _, problem := range problems {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.weekly_review.banner_problem"),
			Message: problem,
		})
	}
	if conflict {
		v.Banner(components.BannerOptions{
			Tone:        components.Warning,
			Title:       i18n.T("pages.weekly_review.conflict_title"),
			Message:     i18n.T("pages.weekly_review.conflict_message"),
			ActionLabel: i18n.T("pages.weekly_review.btn_reload_report"),
			OnAction: func() {
				if project := d.Services.App.Project(); project != nil {
					d.reload(project.ID)
				}
			},
		})
	} else if saveErr != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.weekly_review.state_unsaved"),
			Message: saveErr,
		})
	}
	if report != nil && report.DeletedAt != nil {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.weekly_review.deleted_title"),
			Message: i18n.T("pages.weekly_review.deleted_message"),
		})
	}
	// The generation record keeps the degradation readable after the dialog
	// has closed: AI unset or a failed model call leaves ai_status=failed.
	if report != nil {
		if gen := report.Meta.Generation; gen != nil && gen.AIStatus == "failed" && gen.Error != "" {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("pages.weekly_review.gen_note_title"),
				Message: gen.Error,
			})
		}
	}
}

// ---------------------------------------------------------------------------
// The report tab
// ---------------------------------------------------------------------------

func (d *WeeklyReportView) reportTab(v *components.UI, a *app.App, projectID string, report *weeklyreview.WeeklyReport, writable bool) {
	editable := report.Status == weeklyreview.StatusDraft && writable && report.DeletedAt == nil
	d.ensureEditor(report)

	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.Center).Children(func() {
		if editable {
			ui.Segmented(v.C, &d.mode,
				i18n.T("pages.weekly_review.mode_edit"),
				i18n.T("pages.weekly_review.mode_preview"),
			).Label(i18n.T("pages.weekly_review.mode_label"))
		} else {
			v.Badge(i18n.T("pages.weekly_review.badge_readonly"), components.Neutral)
		}
		ui.Spacer(v.C)
		if text, tone := d.saveState(editable); text != "" {
			v.StatusIndicator(tone, text)
		}
		if d.showSaveHint() {
			ui.Text(v.C, i18n.T("pages.weekly_review.save_shortcut")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted))
		}
	})
	if editable {
		if v.C.Shortcut(ui.Cmd, ui.KeyS) {
			d.saveNow(a, projectID)
		}
		if v.C.Shortcut(ui.Cmd, ui.KeyEnter) {
			d.openConfirm(a)
		}
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
			if v.Button(d.regenLabel(report), components.ButtonOptions{
				Variant:  components.VariantSecondary,
				Icon:     icons.Refresh,
				Disabled: d.regenerating,
				Loading:  d.regenerating,
			}).Clicked() {
				d.regenOpen = true
				d.mu.Lock()
				d.regenErr = ""
				d.regenSkillID, d.regenSkillLabel = "", ""
				if gen := d.report.Meta.Generation; gen != nil {
					d.regenSkillID, d.regenSkillLabel = gen.SkillID, gen.SkillName
				}
				d.mu.Unlock()
				d.skillsLoad(a, projectID)
			}
			if v.Button(i18n.T("pages.weekly_review.btn_insert_ref"), components.ButtonOptions{
				Variant: components.VariantGhost,
			}).Clicked() {
				d.insertOpen = true
			}
			ui.Spacer(v.C)
			if v.Button(i18n.T("pages.weekly_review.btn_outline"), components.ButtonOptions{
				Variant: components.VariantGhost,
				Icon:    icons.Sidebar,
			}).Clicked() {
				d.showOutline = !d.showOutline
			}
		})
	}

	headings := weeklyHeadings(report.ContentMD)
	body := func() {
		if editable && d.mode == weeklyModeEdit && d.ed != nil {
			view := editor.View(v.C, d.ed).FillWidth().Height(editorHeight(v))
			focused := view.Focused()
			d.composing = view.Composing()
			if d.wasFocused && !focused {
				d.saveNow(a, projectID)
			}
			d.wasFocused = focused
			d.tick(v, a, projectID)
			return
		}
		content := weeklyRefMarkdown(report.ContentMD, d.refSet(report))
		if editable {
			d.md.Anchor = d.anchor
			d.md.AnchorVersion = d.anchorVer
			markdown.View(v.C, content, d.md).FillWidth()
			d.tick(v, a, projectID)
			return
		}
		d.md.Anchor = d.anchor
		d.md.AnchorVersion = d.anchorVer
		markdown.View(v.C, content, d.md).FillWidth()
	}
	if d.showOutline && len(headings) > 0 {
		v.SplitPanel(components.SplitPanelOptions{
			Size:       d.splitSize,
			ShowSecond: true,
			First: func() {
				ui.Collapsible(v.C, i18n.T("pages.weekly_review.outline_title"), &d.outlineOpen, func() {
					for _, heading := range headings {
						if v.Button(heading, components.ButtonOptions{
							Variant: components.VariantGhost,
							Size:    components.SizeSmall,
							Icon:    icons.ChevronRight,
						}).Clicked() {
							d.anchor = markdown.Slug(heading)
							d.anchorVer++
							d.tab = 0
						}
					}
				})
			},
			Second: body,
		})
	} else {
		body()
	}
	d.provenance(v, report)
	d.metrics(v, report)
}

// ensureEditor builds the editor for the loaded report and adopts content the
// service replaced (a reload, or a regeneration this page caused).
func (d *WeeklyReportView) ensureEditor(report *weeklyreview.WeeklyReport) {
	need := d.ed == nil || d.edFor != report.ID
	d.mu.Lock()
	reset := d.needReset
	if reset {
		d.needReset = false
	}
	d.mu.Unlock()
	if need {
		d.ed = &editor.State{Buffer: editor.NewBuffer(report.ContentMD)}
		d.ed.Buffer.SetLanguage(editor.LanguageFor("report.md"))
		d.ed.Buffer.MarkSaved()
		d.edFor = report.ID
		d.lastVersion = d.ed.Buffer.Version()
		d.tab = 0
		d.mode = weeklyModeEdit
		d.acks = map[string]bool{}
		d.ackNotes = map[string]string{}
		d.expanded = map[string]bool{}
		d.diffOpen = false
		d.anchor = ""
		return
	}
	if reset && d.ed != nil && d.ed.Buffer.String() != report.ContentMD {
		// A regeneration or reload replaced the body; the buffer follows and
		// an undone edit is not offered, because the service's content wins.
		d.ed.Buffer.Reset(report.ContentMD)
		d.ed.Buffer.MarkSaved()
		d.lastVersion = d.ed.Buffer.Version()
	}
}

func (d *WeeklyReportView) regenLabel(report *weeklyreview.WeeklyReport) string {
	if strings.TrimSpace(report.ContentMD) == "" {
		return i18n.T("pages.weekly_review.btn_regen_empty")
	}
	return i18n.T("pages.weekly_review.btn_regen")
}

func (d *WeeklyReportView) showSaveHint() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.report != nil && d.report.Status == weeklyreview.StatusDraft
}

// saveState is the one line that says whether the buffer is stored.
func (d *WeeklyReportView) saveState(editable bool) (string, components.Tone) {
	d.mu.Lock()
	saving, saveErr, conflict, savedAt := d.saving, d.saveErr, d.conflict, d.savedAt
	d.mu.Unlock()
	if !editable {
		return "", components.Neutral
	}
	switch {
	case saving:
		return i18n.T("pages.weekly_review.state_saving"), components.Info
	case conflict:
		return i18n.T("pages.weekly_review.state_conflict"), components.Warning
	case saveErr != "":
		return i18n.T("pages.weekly_review.state_unsaved"), components.Danger
	case d.ed != nil && d.ed.Buffer.Modified():
		return i18n.T("pages.weekly_review.state_unsaved"), components.Warning
	case !savedAt.IsZero():
		return i18n.T("pages.weekly_review.state_saved"), components.Success
	}
	return "", components.Neutral
}

// tick detects edits and schedules the debounced save; an IME composition
// pauses the timer.
func (d *WeeklyReportView) tick(v *components.UI, a *app.App, projectID string) {
	if d.ed == nil {
		return
	}
	d.mu.Lock()
	saving, conflict, saveErr := d.saving, d.conflict, d.saveErr
	d.mu.Unlock()
	if saving || conflict {
		d.savedPending = false
		return
	}
	version := d.ed.Buffer.Version()
	if version != d.lastVersion {
		d.lastVersion = version
		d.lastChange = v.C.Now()
		d.savedPending = false
		if saveErr != "" {
			d.mu.Lock()
			d.saveErr = ""
			d.mu.Unlock()
		}
	}
	if !d.ed.Buffer.Modified() || saveErr != "" || d.composing {
		d.savedPending = false
		return
	}
	left := weeklySaveDelay - v.C.Now().Sub(d.lastChange)
	if left > 0 {
		if !d.savedPending {
			d.savedPending = true
			v.C.After(left)
		}
		return
	}
	d.savedPending = false
	d.save(a, projectID)
}

// saveNow flushes the buffer at once (blur, leave, Ctrl+S).
func (d *WeeklyReportView) saveNow(a *app.App, projectID string) {
	if d.ed == nil || !d.ed.Buffer.Modified() {
		return
	}
	d.save(a, projectID)
}

// save writes the buffer through UpdateContent with the content hash as the
// concurrency token.
func (d *WeeklyReportView) save(a *app.App, projectID string) {
	d.mu.Lock()
	report := d.report
	if d.saving || report == nil || d.ed == nil || report.Status != weeklyreview.StatusDraft || report.DeletedAt != nil || !d.ed.Buffer.Modified() {
		d.mu.Unlock()
		return
	}
	text := d.ed.Buffer.String()
	version := d.ed.Buffer.Version()
	base := report.ContentHash
	reportID := report.ID
	d.saving = true
	d.saveErr = ""
	d.mu.Unlock()

	api := d.Services.Weekly
	a.RunTask(i18n.T("pages.weekly_review.task_save"), func(ctx context.Context, t *app.Task) error {
		updated, err := api.UpdateContent(ctx, projectID, weeklyreview.UpdateContentInput{
			ReportID:     reportID,
			ContentMD:    text,
			ExpectedHash: base,
			Actor:        weeklyreview.ActorUser,
		})
		d.mu.Lock()
		d.saving = false
		if d.loadedID != reportID {
			d.mu.Unlock()
			if err != nil {
				a.Toast(app.LevelDanger, i18n.T("pages.weekly_review.toast_save_fail"), weeklyError(err))
			}
			d.shared.bump()
			a.Notify()
			return nil
		}
		switch {
		case err != nil && weeklyreview.IsCode(err, weeklyreview.ErrContentConflict):
			d.conflict = true
			d.saveErr = ""
		case err != nil:
			d.saveErr = weeklyError(err)
		default:
			d.report = updated
			d.conflict = false
			d.saveErr = ""
			d.savedAt = time.Now()
		}
		d.mu.Unlock()
		if err != nil {
			if weeklyreview.IsCode(err, weeklyreview.ErrContentConflict) {
				a.Toast(app.LevelWarning, i18n.T("pages.weekly_review.toast_conflict"), i18n.T("pages.weekly_review.conflict_message"))
			} else {
				a.Toast(app.LevelDanger, i18n.T("pages.weekly_review.toast_save_fail"), weeklyError(err))
			}
		} else if d.ed != nil && d.ed.Buffer.Version() == version {
			d.ed.Buffer.MarkSaved()
			d.shared.bump()
		}
		a.Notify()
		return nil
	})
}

// reload re-reads the report and adopts it, for the conflict banner.
func (d *WeeklyReportView) reload(projectID string) {
	a := d.Services.App
	d.mu.Lock()
	reportID := d.loadedID
	// A reload is explicit (the conflict banner): it must read even when the
	// shared generation and the project did not change.
	if !d.loading {
		d.loadedID = ""
	}
	d.mu.Unlock()
	if reportID == "" {
		return
	}
	d.load(a, projectID, reportID)
}

// load reads the report and its digest once per report and shared generation.
func (d *WeeklyReportView) load(a *app.App, projectID, reportID string) {
	d.mu.Lock()
	need := !d.loading && (d.loadedID != reportID || d.project != projectID || d.version != d.shared.version())
	if d.project != projectID {
		d.project = projectID
		d.version = 0
	}
	d.mu.Unlock()
	if !need {
		return
	}
	api := d.Services.Weekly
	gen := d.shared.version()
	d.mu.Lock()
	if d.loading {
		d.mu.Unlock()
		return
	}
	d.loading = true
	d.mu.Unlock()

	a.RunTask(i18n.T("pages.weekly_review.task_load_report"), func(ctx context.Context, t *app.Task) error {
		report, err := api.Get(ctx, projectID, reportID)
		var digest *weeklyreview.ActivityDigest
		if err == nil && report != nil {
			t.Stage(i18n.T("pages.weekly_review.stage_collect"))
			digest, _ = api.CollectActivities(ctx, projectID, weeklyreview.CollectInput{Period: report.Period})
		}
		d.mu.Lock()
		d.loading = false
		if err != nil {
			d.problems = append(d.problems[:0], weeklyError(err))
		} else {
			changed := d.report == nil || d.report.ContentHash != report.ContentHash
			d.report = report
			d.digest = digest
			d.loadedID = reportID
			d.problems = nil
			d.version = gen
			d.needReset = changed
		}
		d.mu.Unlock()
		a.Notify()
		return nil
	})
}

// refSet is the set of refs the report recorded in meta.refs.
func (d *WeeklyReportView) refSet(report *weeklyreview.WeeklyReport) map[string]bool {
	if len(report.Meta.Refs) == 0 {
		return nil
	}
	out := make(map[string]bool, len(report.Meta.Refs))
	for _, ref := range report.Meta.Refs {
		out[ref.Type+":"+ref.ID] = true
	}
	return out
}

// provenance is the collapsible generation record of §6.2.
func (d *WeeklyReportView) provenance(v *components.UI, report *weeklyreview.WeeklyReport) {
	meta := report.Meta
	ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
		if weeklyManual(meta.Generation) {
			v.Badge(i18n.T("pages.weekly_review.badge_human"), components.Accent)
		}
		if meta.Coverage != nil && meta.Coverage.Truncated {
			v.Badge(i18n.T("pages.weekly_review.badge_truncated"), components.Warning)
		}
	})
	ui.Collapsible(v.C, i18n.T("pages.weekly_review.provenance_title"), &d.provOpen, func() {
		rows := weeklyProvenanceRows(report)
		for _, row := range rows {
			ui.Row(v.C).Gap(tokens.SectionGap).Children(func() {
				ui.Text(v.C, row[0]).Width(180).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(v.T.Palette.TextSecondary))
				ui.Text(v.C, row[1]).
					FontSize(tokens.Typography.Secondary.Size).
					Font(tokens.SystemFonts.Mono)
			})
		}
	})
}

// metrics is the small count card row above the body.
func (d *WeeklyReportView) metrics(v *components.UI, report *weeklyreview.WeeklyReport) {
	var events, refs, violations int
	if report.Meta.Coverage != nil {
		events = report.Meta.Coverage.EventCount
	}
	refs = len(report.Meta.Refs)
	if report.Meta.Checks != nil {
		violations = len(report.Meta.Checks.Violations)
	}
	ui.Row(v.C).FillWidth().Wrap().Gap(tokens.ComponentGap).Children(func() {
		d.metric(v, i18n.T("pages.weekly_review.metric_events"), strconv.Itoa(events))
		d.metric(v, i18n.T("pages.weekly_review.metric_refs"), strconv.Itoa(refs))
		d.metric(v, i18n.T("pages.weekly_review.metric_checks"), strconv.Itoa(violations))
		if report.ConfirmedAt != nil {
			d.metric(v, i18n.T("pages.weekly_review.metric_confirmed"), report.ConfirmedAt.Format("2006-01-02 15:04"))
		}
	})
}

func (d *WeeklyReportView) metric(v *components.UI, label, value string) {
	ui.Column(v.C).Padding(tokens.Space2, tokens.Space3).
		Gap(tokens.Space1).
		Background(mygo.Color(v.T.Palette.BgSubtle)).
		Radius(tokens.RadiusMD).
		Children(func() {
			ui.Text(v.C, value).FontSize(tokens.Typography.CardTitle.Size).
				FontWeight(int(tokens.WeightSemibold))
			ui.Text(v.C, label).FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted))
		})
}

// ---------------------------------------------------------------------------
// The digest tab
// ---------------------------------------------------------------------------

func (d *WeeklyReportView) digestTab(v *components.UI, a *app.App, report *weeklyreview.WeeklyReport) {
	digest := d.digestSnapshot()
	if digest == nil || weeklyDigestEmpty(digest) {
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.WeeklyReview,
			Title:  i18n.T("pages.weekly_review.digest_empty_title"),
			Reason: i18n.T("pages.weekly_review.digest_empty_reason"),
		})
		return
	}
	counts := weeklyDayCounts(digest)
	ui.Row(v.C).FillWidth().Wrap().Gap(tokens.ComponentGap).Children(func() {
		d.metric(v, i18n.T("pages.weekly_review.metric_events"), strconv.Itoa(weeklyEventTotal(digest)))
		d.metric(v, i18n.T("pages.weekly_review.metric_blocked"), strconv.Itoa(len(digest.Blocked)))
		d.metric(v, i18n.T("pages.weekly_review.metric_planned"), strconv.Itoa(len(digest.Planned)))
	})
	// Filing actions (created / imported / deleted) are not progress. They
	// are listed here, apart from the day-by-day progress rows, so nothing
	// silently disappears.
	if n := len(digest.Housekeeping); n > 0 {
		open := d.expanded["housekeeping"]
		ui.Collapsible(v.C, i18n.Tf("pages.weekly_review.digest_housekeeping_title", map[string]string{
			"n": strconv.Itoa(n),
		}), &open, func() {
			ui.Text(v.C, i18n.T("pages.weekly_review.digest_housekeeping_hint")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted))
			entries := digest.Housekeeping
			table := v.Table(components.TableOptions{
				Columns: []components.Column{
					{Title: i18n.T("pages.weekly_review.col_time"), Width: 120},
					{Title: i18n.T("pages.weekly_review.col_entity"), MinWidth: 140},
					{Title: i18n.T("pages.weekly_review.col_summary")},
				},
				Rows: len(entries),
				Key:  func(i int) any { return "housekeeping:" + strconv.Itoa(i) },
				Cell: func(row, col int) {
					item := entries[row]
					switch col {
					case 0:
						ui.Text(v.C, item.FirstAt.Format("01-02 15:04")).FontFeatures("tnum")
					case 1:
						ui.Text(v.C, weeklyEntityLabel(item.Ref.Type)+" "+shortWeeklyID(item.Ref.ID)).SingleLine()
					case 2:
						ui.Text(v.C, item.Summary).MaxLines(2)
					}
				},
			})
			table.Height(visibleWeeklyRows(len(entries)))
		})
		d.expanded["housekeeping"] = open
	}
	if digest.Truncated || len(digest.Warnings) > 0 {
		message := strings.Join(digest.Warnings, "；")
		if digest.Truncated {
			message = i18n.T("pages.weekly_review.digest_truncated")
		}
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.weekly_review.digest_warning_title"),
			Message: message,
		})
	}
	if days := report.Period.Days(); days > 0 && days <= 31 && len(counts) > 0 {
		d.trend(v, counts)
	}

	rows := weeklyDigestRows(digest)
	for _, day := range sortedWeeklyDays(rows) {
		day := day
		label := i18n.Tf("pages.weekly_review.digest_day", map[string]string{
			"date":  day,
			"count": strconv.Itoa(len(rows[day])),
		})
		if _, ok := d.expanded[day]; !ok {
			d.expanded[day] = true
		}
		open := d.expanded[day]
		ui.Collapsible(v.C, label, &open, func() {
			entries := rows[day]
			table := v.Table(components.TableOptions{
				Columns: []components.Column{
					{Title: i18n.T("pages.weekly_review.col_time"), Width: 76},
					{Title: i18n.T("pages.weekly_review.col_section"), Width: 128},
					{Title: i18n.T("pages.weekly_review.col_kind"), Width: 110},
					{Title: i18n.T("pages.weekly_review.col_entity")},
					{Title: i18n.T("pages.weekly_review.col_summary")},
					{Title: i18n.T("pages.weekly_review.col_actor"), Width: 76},
				},
				Rows: len(entries),
				Key: func(i int) any {
					return entries[i].Item.Ref.Type + ":" + entries[i].Item.Ref.ID + ":" + strconv.Itoa(i)
				},
				Cell: func(row, col int) {
					entry := entries[row]
					switch col {
					case 0:
						ui.Text(v.C, entry.Item.FirstAt.Format("15:04")).FontFeatures("tnum")
					case 1:
						ui.Text(v.C, weeklySectionTitle(entry.Section)).SingleLine()
					case 2:
						ui.Text(v.C, entry.Item.Kind).SingleLine()
					case 3:
						label := weeklyEntityLabel(entry.Item.Ref.Type) + " " + shortWeeklyID(entry.Item.Ref.ID)
						if kind, ok := weeklyEntityRoute(entry.Item.Ref.Type); ok {
							if v.Button(label, components.ButtonOptions{
								Variant: components.VariantGhost,
								Size:    components.SizeSmall,
							}).Clicked() {
								a.Navigate(ports.Route{Kind: kind, EntityID: entry.Item.Ref.ID})
							}
						} else {
							ui.Text(v.C, label).SingleLine()
						}
					case 4:
						text := entry.Item.Summary
						if entry.Item.Detail != "" {
							text += " — " + entry.Item.Detail
						}
						ui.Text(v.C, text).MaxLines(2)
					case 5:
						ui.Text(v.C, weeklyActorText(entry.Item.Actor))
					}
				},
			})
			table.Height(visibleWeeklyRows(len(entries)))
		})
		d.expanded[day] = open
	}
	if len(digest.Blocked) > 0 {
		v.Card(components.CardOptions{Title: i18n.T("pages.weekly_review.digest_blocked_title")}, func() {
			for _, task := range digest.Blocked {
				ui.Text(v.C, task.Title+" · "+task.ColumnName).
					FontSize(tokens.Typography.Secondary.Size)
			}
		})
	}
	if len(digest.Planned) > 0 {
		v.Card(components.CardOptions{Title: i18n.T("pages.weekly_review.digest_planned_title")}, func() {
			for _, task := range digest.Planned {
				ui.Text(v.C, task.Title+" · "+task.ColumnName+" · "+task.Origin).
					FontSize(tokens.Typography.Secondary.Size)
			}
		})
	}
}

// trend is the optional daily bar chart from the digest's per-day counts. It
// runs no query of its own (§6.1) and is display-only.
func (d *WeeklyReportView) trend(v *components.UI, counts map[string]int) {
	days := sortedWeeklyDaysCount(counts)
	labels := make([]string, len(days))
	values := make([]float64, len(days))
	max := 0
	for i, day := range days {
		labels[i] = day
		values[i] = float64(counts[day])
		if counts[day] > max {
			max = counts[day]
		}
	}
	if max == 0 {
		return
	}
	x := chart.NewBand(labels...).Padding(0.3, 0.15)
	y := chart.NewLinear(0, float64(max)).Nice(4)
	chart.Plot(v.C, chart.Axes{Left: y.Ticks(4), Bottom: x.Ticks()}, func(area ui.Element) {
		for i, value := range values {
			chart.Rect(v.C, x.Band(i), y.Span(0, value)).Radius(4).
				Background(mygo.Color(v.T.Palette.AccentDefault))
		}
	}).Label(i18n.T("pages.weekly_review.trend_label")).Height(180).Gap(6)
}

// weeklyDigestRows flattens the digest's sections.
func weeklyDigestRows(digest *weeklyreview.ActivityDigest) map[string][]weeklyDigestRow {
	loc := time.Local
	if digest.Timezone != "" {
		if parsed, err := time.LoadLocation(digest.Timezone); err == nil {
			loc = parsed
		}
	}
	out := map[string][]weeklyDigestRow{}
	for _, section := range digest.Sections {
		for _, item := range section.Items {
			at := item.FirstAt
			if at.IsZero() {
				at = item.LastAt
			}
			if at.IsZero() {
				continue
			}
			day := at.In(loc).Format("2006-01-02")
			out[day] = append(out[day], weeklyDigestRow{Section: section.Key, Item: item})
		}
	}
	return out
}

func sortedWeeklyDays(rows map[string][]weeklyDigestRow) []string {
	days := make([]string, 0, len(rows))
	for day := range rows {
		days = append(days, day)
	}
	sort.Strings(days)
	return days
}

// weeklyDayCounts counts entries per calendar day for the trend chart.
func weeklyDayCounts(digest *weeklyreview.ActivityDigest) map[string]int {
	counts := map[string]int{}
	for day, rows := range weeklyDigestRows(digest) {
		counts[day] = len(rows)
	}
	return counts
}

func sortedWeeklyDaysCount(counts map[string]int) []string {
	days := make([]string, 0, len(counts))
	for day := range counts {
		days = append(days, day)
	}
	sort.Strings(days)
	return days
}

func weeklyEventTotal(digest *weeklyreview.ActivityDigest) int {
	total := 0
	for _, count := range digest.Stats.CountsByType {
		total += count
	}
	return total
}

func weeklyDigestEmpty(digest *weeklyreview.ActivityDigest) bool {
	if len(digest.Blocked) > 0 || len(digest.Planned) > 0 {
		return false
	}
	for _, section := range digest.Sections {
		if len(section.Items) > 0 {
			return false
		}
	}
	return len(digest.Housekeeping) == 0
}

func weeklyActorText(actor string) string {
	switch actor {
	case "mcp":
		return i18n.T("pages.weekly_review.actor_mcp")
	case "ai":
		return i18n.T("pages.weekly_review.actor_ai")
	case "user":
		return i18n.T("pages.weekly_review.actor_user")
	}
	return actor
}

// ---------------------------------------------------------------------------
// The validation tab
// ---------------------------------------------------------------------------

func (d *WeeklyReportView) checksTab(v *components.UI, a *app.App, report *weeklyreview.WeeklyReport, writable bool) {
	var checks *weeklyreview.CheckState
	if report.Meta.Checks != nil {
		checks = report.Meta.Checks
	}
	d.mu.Lock()
	confirming, confirmErr, revertErr := d.confirming, d.confirmErr, d.revertErr
	d.mu.Unlock()
	if checks == nil {
		// Never validated: claiming a pass here is what made the confirm
		// dialog contradict itself. Say what is known instead.
		v.Banner(components.BannerOptions{
			Tone:    components.Neutral,
			Title:   i18n.T("pages.weekly_review.checks_not_run_title"),
			Message: i18n.T("pages.weekly_review.checks_not_run_message"),
		})
	} else if len(checks.Violations) == 0 {
		v.Banner(components.BannerOptions{
			Tone:    components.Success,
			Title:   i18n.T("pages.weekly_review.checks_clean_title"),
			Message: i18n.T("pages.weekly_review.checks_clean_message"),
		})
	} else {
		errors, warnings := weeklySeverityCounts(checks.Violations)
		unresolved := d.unresolvedCount()
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).Wrap().Children(func() {
			v.Badge(i18n.Tf("pages.weekly_review.checks_errors", map[string]string{"n": strconv.Itoa(errors)}), components.Danger)
			v.Badge(i18n.Tf("pages.weekly_review.checks_warnings", map[string]string{"n": strconv.Itoa(warnings)}), components.Warning)
			v.Badge(i18n.Tf("pages.weekly_review.checks_unresolved", map[string]string{"n": strconv.Itoa(unresolved)}), components.Neutral)
		})
		for _, violation := range checks.Violations {
			violation := violation
			acked := violation.Ack != nil || d.acks[violation.Fingerprint]
			ui.Column(v.C).FillWidth().Padding(tokens.Space3).
				Gap(tokens.Space1).
				Background(mygo.Color(v.T.Palette.BgSurface)).
				Border(1, mygo.Color(v.T.Palette.BorderDefault)).
				Radius(tokens.RadiusLG).
				Children(func() {
					ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
						tone := components.Warning
						if violation.Severity == weeklyreview.SeverityError {
							tone = components.Danger
						}
						v.Badge(i18n.T("pages.weekly_review.severity_"+violation.Severity), tone)
						ui.Text(v.C, weeklySectionTitle(violation.Section)).
							FontSize(tokens.Typography.Caption.Size).
							TextColor(mygo.Color(v.T.Palette.TextSecondary))
						ui.Spacer(v.C)
						if acked {
							v.Badge(i18n.T("pages.weekly_review.ack_done"), components.Success)
						} else if writable && report.Status == weeklyreview.StatusDraft {
							if v.Button(i18n.T("pages.weekly_review.btn_ack"), components.ButtonOptions{
								Variant: components.VariantSecondary,
								Size:    components.SizeSmall,
							}).Clicked() {
								d.ackFor = violation.Fingerprint
								d.ackNoteInput = d.ackNotes[violation.Fingerprint]
							}
						}
						if v.Button(i18n.T("pages.weekly_review.btn_locate"), components.ButtonOptions{
							Variant: components.VariantGhost,
							Size:    components.SizeSmall,
						}).Clicked() {
							if heading := weeklySectionHeading(report.ContentMD, violation.Section); heading != "" {
								d.anchor = markdown.Slug(heading)
								d.anchorVer++
								d.tab = 0
							}
						}
					})
					ui.Text(v.C, violation.Message).FontSize(tokens.Typography.Secondary.Size)
					ui.Text(v.C, i18n.Tf("pages.weekly_review.rule_line", map[string]string{
						"rule": violation.Rule, "fingerprint": shortWeeklyID(violation.Fingerprint),
					})).Font(tokens.SystemFonts.Mono).
						FontSize(tokens.Typography.Caption.Size).
						TextColor(mygo.Color(v.T.Palette.TextMuted))
					if violation.Ack != nil && violation.Ack.Note != "" {
						ui.Text(v.C, i18n.Tf("pages.weekly_review.ack_note_line", map[string]string{"note": violation.Ack.Note})).
							FontSize(tokens.Typography.Caption.Size).
							TextColor(mygo.Color(v.T.Palette.TextSecondary))
					}
				})
		}
	}

	// The confirm gate: unresolved findings block the button, with the reason
	// spelled out instead of a silent disabled control.
	unresolved := d.unresolvedCount()
	ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
		if report.Status == weeklyreview.StatusDraft {
			disabled := unresolved > 0 || !writable || confirming
			if v.Button(i18n.T("pages.weekly_review.btn_confirm"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Icon:     icons.Check,
				Disabled: disabled,
				Loading:  confirming,
			}).Clicked() {
				d.openConfirm(a)
			}
			if unresolved > 0 {
				ui.Text(v.C, i18n.Tf("pages.weekly_review.confirm_blocked", map[string]string{"n": strconv.Itoa(unresolved)})).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(v.T.Palette.StatusWarning))
			}
		}
		if report.Status == weeklyreview.StatusConfirmed && writable {
			if v.Button(i18n.T("pages.weekly_review.btn_revert"), components.ButtonOptions{
				Variant: components.VariantSecondary,
			}).Clicked() {
				d.revertOpen = true
			}
		}
	})
	if confirmErr != "" {
		v.Banner(components.BannerOptions{Tone: components.Danger, Title: i18n.T("pages.weekly_review.confirm_failed"), Message: confirmErr})
	}
	if revertErr != "" {
		v.Banner(components.BannerOptions{Tone: components.Danger, Title: i18n.T("pages.weekly_review.revert_failed"), Message: revertErr})
	}
}

func weeklySeverityCounts(list []weeklyreview.Violation) (int, int) {
	errors, warnings := 0, 0
	for _, v := range list {
		if v.Severity == weeklyreview.SeverityError {
			errors++
		} else {
			warnings++
		}
	}
	return errors, warnings
}

// unresolvedCount counts the findings without an ack, local or stored.
func (d *WeeklyReportView) unresolvedCount() int {
	d.mu.Lock()
	report := d.report
	d.mu.Unlock()
	if report == nil || report.Meta.Checks == nil {
		return 0
	}
	n := 0
	for _, violation := range report.Meta.Checks.Violations {
		if violation.Ack == nil && !d.acks[violation.Fingerprint] {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Dialogs
// ---------------------------------------------------------------------------

func (d *WeeklyReportView) ackDialog(v *components.UI) {
	if d.ackFor == "" {
		return
	}
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       i18n.T("pages.weekly_review.ack_title"),
		Description: i18n.T("pages.weekly_review.ack_desc"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				d.ackFor = ""
			}
			if v.Button(i18n.T("pages.weekly_review.btn_ack"), components.ButtonOptions{
				Variant: components.VariantPrimary,
			}).Clicked() {
				d.acks[d.ackFor] = true
				if strings.TrimSpace(d.ackNoteInput) != "" {
					d.ackNotes[d.ackFor] = strings.TrimSpace(d.ackNoteInput)
				}
				d.ackFor = ""
				d.ackNoteInput = ""
			}
		},
	}, func() {
		v.Input(&d.ackNoteInput, components.FieldOptions{
			Label:       i18n.T("pages.weekly_review.ack_note"),
			Placeholder: i18n.T("pages.weekly_review.ack_note_hint"),
		})
	})
	if !open {
		d.ackFor = ""
	}
}

// openConfirm saves the draft first (a confirm of unsaved text would validate
// the old body) and opens the confirmation dialog.
func (d *WeeklyReportView) openConfirm(a *app.App) {
	project := a.Project()
	if project == nil {
		return
	}
	d.saveNow(a, project.ID)
	d.confirmOpen = true
	d.mu.Lock()
	d.confirmErr = ""
	d.mu.Unlock()
}

func (d *WeeklyReportView) confirmDialog(v *components.UI, a *app.App, projectID string) {
	if !d.confirmOpen {
		return
	}
	d.mu.Lock()
	confirming, errText := d.confirming, d.confirmErr
	d.mu.Unlock()
	unresolved := d.unresolvedViolations()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       i18n.T("pages.weekly_review.confirm_title"),
		Description: i18n.T("pages.weekly_review.confirm_desc"),
		Width:       560,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				d.confirmOpen = false
			}
			if v.Button(i18n.T("pages.weekly_review.btn_confirm"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  confirming,
				Disabled: confirming || len(unresolved) > 0,
			}).Clicked() {
				d.confirm(a, projectID)
			}
		},
	}, func() {
		if len(unresolved) > 0 {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("pages.weekly_review.confirm_blocked_title"),
				Message: i18n.Tf("pages.weekly_review.confirm_blocked", map[string]string{"n": strconv.Itoa(len(unresolved))}),
			})
			for _, violation := range unresolved {
				ui.Text(v.C, "• "+weeklySectionTitle(violation.Section)+" · "+violation.Message).
					FontSize(tokens.Typography.Secondary.Size)
			}
			ui.Text(v.C, i18n.T("pages.weekly_review.confirm_blocked_hint")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted))
		} else if errText == "" {
			// "Everything is acknowledged" is only true while no confirm has
			// failed; the service re-validates and may still refuse.
			ui.Text(v.C, i18n.T("pages.weekly_review.confirm_ready")).
				FontSize(tokens.Typography.Secondary.Size)
		}
		if errText != "" {
			ui.Text(v.C, errText).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !confirming {
		d.confirmOpen = false
	}
}

// unresolvedViolations are the findings still without an ack.
func (d *WeeklyReportView) unresolvedViolations() []weeklyreview.Violation {
	d.mu.Lock()
	report := d.report
	d.mu.Unlock()
	if report == nil || report.Meta.Checks == nil {
		return nil
	}
	var out []weeklyreview.Violation
	for _, violation := range report.Meta.Checks.Violations {
		if violation.Ack == nil && !d.acks[violation.Fingerprint] {
			out = append(out, violation)
		}
	}
	return out
}

// adoptUnresolved replaces the local check state with the findings Confirm
// refused with, so the dialog and the checks tab show the same list the
// service just computed. The caller must hold d.mu.
func (d *WeeklyReportView) adoptUnresolved(err error) bool {
	se := weeklyreview.AsServiceError(err)
	if se == nil || se.Code != weeklyreview.ErrUnresolvedViolations || d.report == nil {
		return false
	}
	list, ok := se.Details["violations"].([]weeklyreview.Violation)
	if !ok {
		return false
	}
	meta := d.report.Meta
	meta.Checks = &weeklyreview.CheckState{CheckedAt: time.Now(), Violations: list}
	d.report.Meta = meta
	live := make(map[string]bool, len(list))
	for _, violation := range list {
		live[violation.Fingerprint] = true
	}
	for fp := range d.acks {
		if !live[fp] {
			delete(d.acks, fp)
			delete(d.ackNotes, fp)
		}
	}
	return true
}

// confirm sends the local acks with the confirmation; the service persists
// them in meta.checks in the same transaction (SPEC-09 §5.3).
func (d *WeeklyReportView) confirm(a *app.App, projectID string) {
	d.mu.Lock()
	report := d.report
	if d.confirming || report == nil {
		d.mu.Unlock()
		return
	}
	reportID := report.ID
	d.confirming = true
	d.confirmErr = ""
	d.mu.Unlock()

	fingerprints := make([]string, 0, len(d.acks))
	notes := make(map[string]string, len(d.ackNotes))
	for fp, on := range d.acks {
		if !on {
			continue
		}
		fingerprints = append(fingerprints, fp)
		if note := strings.TrimSpace(d.ackNotes[fp]); note != "" {
			notes[fp] = note
		}
	}
	api := d.Services.Weekly
	a.RunTask(i18n.T("pages.weekly_review.task_confirm"), func(ctx context.Context, t *app.Task) error {
		updated, err := api.Confirm(ctx, projectID, weeklyreview.ConfirmInput{
			ReportID:        reportID,
			Actor:           weeklyreview.ActorUser,
			AckFingerprints: fingerprints,
			AckNotes:        notes,
		})
		d.mu.Lock()
		d.confirming = false
		if err != nil {
			// The service re-validates against a freshly collected digest,
			// so its unresolved set can be larger than the one this frame
			// showed. Adopt it instead of dead-ending the dialog.
			if d.adoptUnresolved(err) {
				d.confirmErr = ""
			} else {
				d.confirmErr = weeklyError(err)
			}
		} else {
			d.report = updated
			d.confirmOpen = false
			d.acks = map[string]bool{}
			d.ackNotes = map[string]string{}
		}
		d.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.weekly_review.toast_confirm_fail"), weeklyError(err))
		} else {
			a.Toast(app.LevelSuccess, i18n.T("pages.weekly_review.toast_confirmed"), "")
			d.shared.bump()
		}
		a.Notify()
		return nil
	})
}

func (d *WeeklyReportView) revertDialog(v *components.UI, a *app.App, projectID string) {
	if !d.revertOpen {
		return
	}
	d.mu.Lock()
	reverting, errText := d.reverting, d.revertErr
	d.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       i18n.T("pages.weekly_review.revert_title"),
		Description: i18n.T("pages.weekly_review.revert_desc"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				d.revertOpen = false
			}
			if v.Button(i18n.T("pages.weekly_review.btn_revert"), components.ButtonOptions{
				Variant:  components.VariantSecondary,
				Loading:  reverting,
				Disabled: reverting,
			}).Clicked() {
				d.revert(a, projectID)
			}
		},
	}, func() {
		if errText != "" {
			ui.Text(v.C, errText).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !reverting {
		d.revertOpen = false
	}
}

func (d *WeeklyReportView) revert(a *app.App, projectID string) {
	d.mu.Lock()
	report := d.report
	if d.reverting || report == nil {
		d.mu.Unlock()
		return
	}
	reportID := report.ID
	d.reverting = true
	d.revertErr = ""
	d.mu.Unlock()
	api := d.Services.Weekly
	a.RunTask(i18n.T("pages.weekly_review.task_revert"), func(ctx context.Context, t *app.Task) error {
		updated, err := api.RevertToDraft(ctx, projectID, weeklyreview.RevertInput{
			ReportID: reportID, Actor: weeklyreview.ActorUser,
		})
		d.mu.Lock()
		d.reverting = false
		if err != nil {
			d.revertErr = weeklyError(err)
		} else {
			d.report = updated
			d.revertOpen = false
		}
		d.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.weekly_review.toast_revert_fail"), weeklyError(err))
		} else {
			a.Toast(app.LevelSuccess, i18n.T("pages.weekly_review.toast_reverted"), "")
			d.shared.bump()
		}
		a.Notify()
		return nil
	})
}

// skillsLoad reads the writing skills for the regenerate dialog, once.
func (d *WeeklyReportView) skillsLoad(a *app.App, projectID string) {
	api := d.Services.Weekly
	if api == nil || a == nil {
		return
	}
	d.mu.Lock()
	if d.skillsLoading {
		d.mu.Unlock()
		return
	}
	d.skillsLoading = true
	d.mu.Unlock()
	a.RunTask(i18n.T("pages.weekly_review.skill_task_load"), func(ctx context.Context, t *app.Task) error {
		set, err := api.Skills(ctx, projectID)
		d.mu.Lock()
		d.skillsLoading = false
		if err == nil {
			d.skills = set
			d.skillsLoaded = true
		}
		d.mu.Unlock()
		a.Notify()
		return nil
	})
}

// skillSetSnapshot is the frame's copy of the loaded skills.
func (d *WeeklyReportView) skillSetSnapshot() weeklyreview.SkillSet {
	d.mu.Lock()
	defer d.mu.Unlock()
	set := d.skills
	set.Skills = append([]weeklyreview.Skill(nil), d.skills.Skills...)
	return set
}

// regenDialog explains the overwrite before GenerateDraft(Replace=true) runs.
func (d *WeeklyReportView) regenDialog(v *components.UI, a *app.App, projectID string) {
	if !d.regenOpen {
		return
	}
	if d.skillEditor.open {
		d.skillEditor.dialog(v, a, d.Services, projectID, func(updated weeklyreview.SkillSet) {
			d.mu.Lock()
			d.skills = updated
			d.mu.Unlock()
			d.regenSkillLabel, d.regenSkillID = "", ""
			a.Notify()
		})
		return
	}
	d.mu.Lock()
	regenerating, errText := d.regenerating, d.regenErr
	d.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       i18n.T("pages.weekly_review.regen_title"),
		Description: i18n.T("pages.weekly_review.regen_desc"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				d.regenOpen = false
			}
			if v.Button(i18n.T("pages.weekly_review.btn_regen_run"), components.ButtonOptions{
				Variant:  components.VariantDanger,
				Loading:  regenerating,
				Disabled: regenerating,
			}).Clicked() {
				d.regenerate(a, projectID)
			}
		},
	}, func() {
		ui.Text(v.C, i18n.T("pages.weekly_review.regen_note")).
			FontSize(tokens.Typography.Secondary.Size).
			TextColor(mygo.Color(v.T.Palette.TextSecondary))
		if d.Services.Weekly.AIEnabled(projectID) {
			weeklySkillPicker(v, a, d.Services, projectID, d.skillSetSnapshot(), &d.regenSkillLabel, &d.regenSkillID, &d.skillEditor)
		}
		if errText != "" {
			ui.Text(v.C, errText).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !regenerating {
		d.regenOpen = false
	}
}

// regenerate stores the unsaved edits first, then replaces the body with a new
// draft. The service replaces in place, so the previous body is kept here for
// the DiffViewer and for the explicit undo below it (documented design).
func (d *WeeklyReportView) regenerate(a *app.App, projectID string) {
	d.mu.Lock()
	report := d.report
	if d.regenerating || report == nil {
		d.mu.Unlock()
		return
	}
	reportID := report.ID
	skillID := d.regenSkillID
	old := report.ContentMD
	base := report.ContentHash
	buffer := ""
	if d.ed != nil {
		buffer = d.ed.Buffer.String()
	}
	d.regenerating = true
	d.regenErr = ""
	d.mu.Unlock()

	api := d.Services.Weekly
	a.RunTask(i18n.T("pages.weekly_review.task_regenerate"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("pages.weekly_review.stage_save"))
		if buffer != old {
			updated, err := api.UpdateContent(ctx, projectID, weeklyreview.UpdateContentInput{
				ReportID: reportID, ContentMD: buffer, ExpectedHash: base, Actor: weeklyreview.ActorUser,
			})
			if err != nil {
				d.mu.Lock()
				d.regenerating = false
				d.regenErr = weeklyError(err)
				d.mu.Unlock()
				a.Toast(app.LevelDanger, i18n.T("pages.weekly_review.toast_regen_fail"), weeklyError(err))
				a.Notify()
				return nil
			}
			old = updated.ContentMD
		}
		t.Stage(i18n.T("pages.weekly_review.stage_generate"))
		t.Progress(30, 100)
		// The dialog's picker wins; without a choice the report keeps the
		// skill it was written with, and a report generated before skills
		// existed falls back to the workspace default.
		if skillID == "" && report.Meta.Generation != nil {
			skillID = report.Meta.Generation.SkillID
		}
		outcome, err := api.GenerateDraft(ctx, projectID, weeklyreview.GenerateInput{
			ReportID: reportID, Replace: true, Actor: weeklyreview.ActorUser,
			SkillID: skillID, Progress: weeklySectionProgress(t),
		})
		degraded := outcome != nil && outcome.Report != nil && weeklyDegraded(err)
		switch {
		case outcome != nil && outcome.Report != nil && (err == nil || degraded):
			fresh := outcome.Report
			d.mu.Lock()
			d.regenerating = false
			d.report = fresh
			d.regenOpen = false
			d.needReset = true
			d.regenOld = old
			if old != fresh.ContentMD {
				d.diffState = &editor.DiffState{
					Old: editor.NewBuffer(old),
					New: editor.NewBuffer(fresh.ContentMD),
				}
				d.diffOpen = true
			}
			if degraded {
				d.regenErr = weeklyError(err)
			}
			d.mu.Unlock()
			if degraded {
				a.Toast(app.LevelWarning, i18n.T("pages.weekly_review.toast_generated_skeleton"), weeklyError(err))
			} else {
				a.Toast(app.LevelSuccess, i18n.T("pages.weekly_review.toast_regenerated"), fresh.Title)
			}
			d.shared.bump()
		case err != nil:
			d.mu.Lock()
			d.regenerating = false
			d.regenErr = weeklyError(err)
			d.mu.Unlock()
			a.Toast(app.LevelDanger, i18n.T("pages.weekly_review.toast_regen_fail"), weeklyError(err))
		default:
			d.mu.Lock()
			d.regenerating = false
			d.mu.Unlock()
		}
		t.Progress(100, 100)
		a.Notify()
		return nil
	})
}

// diffDialog shows the replaced body against the previous one and offers the
// undo path the in-place generation needs.
func (d *WeeklyReportView) diffDialog(v *components.UI, a *app.App, projectID string) {
	if !d.diffOpen {
		return
	}
	d.mu.Lock()
	diff := d.diffState
	old := d.regenOld
	d.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       i18n.T("pages.weekly_review.diff_title"),
		Description: i18n.T("pages.weekly_review.diff_desc"),
		Width:       900,
		Actions: func() {
			if v.Button(i18n.T("pages.weekly_review.btn_keep_new"), components.ButtonOptions{
				Variant: components.VariantPrimary,
			}).Clicked() {
				d.diffOpen = false
			}
			if v.Button(i18n.T("pages.weekly_review.btn_undo_regen"), components.ButtonOptions{
				Variant: components.VariantSecondary,
			}).Clicked() {
				d.undoRegen(a, projectID, old)
			}
		},
	}, func() {
		if diff != nil {
			editor.DiffView(v.C, diff).FillWidth().Height(editorHeight(v))
		}
	})
	if !open {
		d.diffOpen = false
	}
}

// undoRegen restores the body captured before the regeneration.
func (d *WeeklyReportView) undoRegen(a *app.App, projectID, old string) {
	d.mu.Lock()
	report := d.report
	if report == nil {
		d.mu.Unlock()
		return
	}
	reportID := report.ID
	hash := report.ContentHash
	d.mu.Unlock()
	api := d.Services.Weekly
	a.RunTask(i18n.T("pages.weekly_review.task_undo_regen"), func(ctx context.Context, t *app.Task) error {
		updated, err := api.UpdateContent(ctx, projectID, weeklyreview.UpdateContentInput{
			ReportID: reportID, ContentMD: old, ExpectedHash: hash, Actor: weeklyreview.ActorUser,
		})
		d.mu.Lock()
		if err != nil {
			d.regenErr = weeklyError(err)
		} else {
			d.report = updated
			d.diffOpen = false
			d.needReset = true
		}
		d.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.weekly_review.toast_regen_fail"), weeklyError(err))
		} else {
			a.Toast(app.LevelSuccess, i18n.T("pages.weekly_review.toast_undo_regen"), "")
			d.shared.bump()
		}
		a.Notify()
		return nil
	})
}

// insertRefDialog lists the digest's entities so a reference is picked, never
// typed. The chosen reference is appended to the body as a bullet.
func (d *WeeklyReportView) insertRefDialog(v *components.UI) {
	if !d.insertOpen {
		return
	}
	type refChoice struct {
		Type string
		ID   string
		Text string
	}
	var choices []refChoice
	seen := map[string]bool{}
	if digest := d.digestSnapshot(); digest != nil {
		add := func(ref weeklyreview.EntityRef, text string) {
			key := ref.Type + ":" + ref.ID
			if seen[key] {
				return
			}
			seen[key] = true
			choices = append(choices, refChoice{Type: ref.Type, ID: ref.ID, Text: text})
		}
		for _, section := range digest.Sections {
			for _, item := range section.Items {
				add(item.Ref, item.Summary)
			}
		}
		for _, blocked := range digest.Blocked {
			add(blocked.Ref, blocked.Title)
		}
		for _, planned := range digest.Planned {
			add(planned.Ref, planned.Title)
		}
	}
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       i18n.T("pages.weekly_review.insert_ref_title"),
		Description: i18n.T("pages.weekly_review.insert_ref_desc"),
		Width:       560,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				d.insertOpen = false
			}
		},
	}, func() {
		if len(choices) == 0 {
			ui.Text(v.C, i18n.T("pages.weekly_review.insert_ref_empty")).
				FontSize(tokens.Typography.Secondary.Size)
			return
		}
		v.List(components.ListOptions{
			Rows: len(choices),
			Row: func(i int) {
				choice := choices[i]
				if v.Button(weeklyEntityLabel(choice.Type)+" · "+shortWeeklyID(choice.ID)+" — "+choice.Text,
					components.ButtonOptions{
						Variant: components.VariantGhost,
						Size:    components.SizeSmall,
					}).Clicked() {
					d.insertRef(choice.Type, choice.ID)
					d.insertOpen = false
				}
			},
		}).Height(240)
	})
	if !open {
		d.insertOpen = false
	}
}

// insertRef appends one [[ref:type:id]] bullet to the editor buffer.
func (d *WeeklyReportView) insertRef(typ, id string) {
	if d.ed == nil {
		return
	}
	text := "\n- [[ref:" + typ + ":" + id + "]]\n"
	end := d.ed.Buffer.Len()
	d.ed.Buffer.Edit(editor.Edit{Start: end, End: end, Text: text})
}

// exportDialog is §6.4: Markdown or HTML, directory, front matter and
// overwrite.
func (d *WeeklyReportView) exportDialog(v *components.UI, a *app.App, projectID string, report *weeklyreview.WeeklyReport) {
	if !d.exportOpen {
		return
	}
	d.mu.Lock()
	exporting, errText, path := d.exporting, d.exportErr, d.exportPath
	d.mu.Unlock()
	mdLabel := i18n.T("pages.weekly_review.export_format_md")
	htmlLabel := i18n.T("pages.weekly_review.export_format_html")
	if d.exportFmtLabel != mdLabel && d.exportFmtLabel != htmlLabel {
		d.exportFmtLabel = mdLabel
	}
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       i18n.T("pages.weekly_review.export_title"),
		Description: i18n.T("pages.weekly_review.export_desc"),
		Width:       560,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				d.exportOpen = false
			}
			if v.Button(i18n.T("pages.weekly_review.btn_export_run"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  exporting,
				Disabled: exporting,
			}).Clicked() {
				format := "md"
				if d.exportFmtLabel == htmlLabel {
					format = "html"
				}
				d.export(a, projectID, report, format)
			}
		},
	}, func() {
		if report.Status == weeklyreview.StatusDraft {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("pages.weekly_review.export_draft_title"),
				Message: i18n.T("pages.weekly_review.export_draft_message"),
			})
		}
		v.Select(&d.exportFmtLabel, components.SelectOptions{
			Label:   i18n.T("pages.weekly_review.field_format"),
			Options: []string{mdLabel, htmlLabel},
		})
		ext := ".md"
		if d.exportFmtLabel == htmlLabel {
			ext = ".html"
		}
		dir := v.Input(&d.exportDir, components.FieldOptions{
			Label:       i18n.T("pages.weekly_review.field_dir"),
			Placeholder: i18n.T("pages.weekly_review.field_dir_hint"),
		})
		if v.Button(i18n.T("pages.weekly_review.btn_choose_dir"), components.ButtonOptions{
			Variant: components.VariantGhost,
			Icon:    icons.FolderOpen,
		}).Clicked() {
			if d.Services.ChooseFolder != nil {
				if chosen, err := d.Services.ChooseFolder(); err == nil && chosen != "" {
					d.exportDir = chosen
				}
			}
		}
		_ = dir
		v.Checkbox(&d.exportFront, i18n.T("pages.weekly_review.field_front_matter"), components.ToggleOptions{})
		v.Checkbox(&d.exportOver, i18n.T("pages.weekly_review.field_overwrite"), components.ToggleOptions{})
		ui.Text(v.C, i18n.Tf("pages.weekly_review.export_filename", map[string]string{
			"name": report.Period.StartDate() + "_" + report.Period.EndDate() + "_weekly-report" + ext,
		})).Font(tokens.SystemFonts.Mono).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted))
		if errText != "" {
			ui.Text(v.C, errText).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
		if path != "" {
			ui.Text(v.C, i18n.Tf("pages.weekly_review.export_done", map[string]string{"path": path})).
				FontSize(tokens.Typography.Secondary.Size)
			ui.Row(v.C).Gap(tokens.ComponentGap).Children(func() {
				if v.Button(i18n.T("pages.weekly_review.btn_reveal"), components.ButtonOptions{
					Variant: components.VariantSecondary,
					Icon:    icons.FolderOpen,
				}).Clicked() {
					dir := weeklyDirOf(path)
					if err := d.Services.Weekly.RevealFolder(dir); err != nil {
						a.Toast(app.LevelDanger, i18n.T("pages.weekly_review.toast_reveal_fail"), weeklyError(err))
					}
				}
				if v.Button(i18n.T("pages.weekly_review.btn_copy_md"), components.ButtonOptions{
					Variant: components.VariantSecondary,
					Icon:    icons.Copy,
				}).Clicked() {
					v.C.WriteClipboard(report.ContentMD)
					a.Toast(app.LevelSuccess, i18n.T("pages.weekly_review.toast_copied"), "")
				}
			})
		}
	})
	if !open && !exporting {
		d.exportOpen = false
	}
}

func (d *WeeklyReportView) export(a *app.App, projectID string, report *weeklyreview.WeeklyReport, format string) {
	if report == nil {
		return
	}
	d.mu.Lock()
	if d.exporting {
		d.mu.Unlock()
		return
	}
	d.exporting = true
	d.exportErr = ""
	d.exportPath = ""
	d.mu.Unlock()
	front := d.exportFront
	dir := strings.TrimSpace(d.exportDir)
	overwrite := d.exportOver
	reportID := report.ID
	api := d.Services.Weekly
	a.RunTask(i18n.T("pages.weekly_review.task_export"), func(ctx context.Context, t *app.Task) error {
		result, err := api.Export(ctx, projectID, weeklyreview.ExportInput{
			ReportID:    reportID,
			Format:      format,
			Dir:         dir,
			Overwrite:   overwrite,
			FrontMatter: &front,
			Actor:       weeklyreview.ActorUser,
		})
		d.mu.Lock()
		d.exporting = false
		if err != nil {
			d.exportErr = weeklyError(err)
		} else if result != nil {
			d.exportPath = result.Path
		}
		d.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.weekly_review.toast_export_fail"), weeklyError(err))
		} else if result != nil {
			a.Toast(app.LevelSuccess, i18n.T("pages.weekly_review.toast_exported"), result.Path)
		}
		a.Notify()
		return nil
	})
}

// weeklyDirOf returns the directory part of a path without importing filepath
// into the view: the report service always returns an absolute OS path.
func weeklyDirOf(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return path
}

// weeklyManual reports whether the generation record marks a human-written
// body. The service stores mode=manual for CreateReport and for skeletono-only
// generation; UpdateContent does not write the marker, so this badge reflects
// the last generation, not later edits (documented gap).
func weeklyManual(gen *weeklyreview.GenerationMeta) bool {
	if gen == nil {
		return true
	}
	return gen.Mode == "manual" || gen.AIStatus == "skipped"
}

// weeklyProvenanceRows is the collapsible provenance table.
func weeklyProvenanceRows(report *weeklyreview.WeeklyReport) [][2]string {
	gen := report.Meta.Generation
	rows := [][2]string{
		{i18n.T("pages.weekly_review.prov_status"), string(report.Status)},
		{i18n.T("pages.weekly_review.prov_updated"), report.UpdatedAt.Format("2006-01-02 15:04")},
		{i18n.T("pages.weekly_review.prov_content_hash"), report.ContentHash},
	}
	if gen != nil {
		rows = append(rows,
			[2]string{i18n.T("pages.weekly_review.prov_mode"), gen.Mode},
			[2]string{i18n.T("pages.weekly_review.prov_ai_status"), gen.AIStatus},
			[2]string{i18n.T("pages.weekly_review.prov_provider"), gen.ProviderID},
			[2]string{i18n.T("pages.weekly_review.prov_model"), gen.ModelID},
			[2]string{i18n.T("pages.weekly_review.prov_prompt"), gen.PromptVersion},
			[2]string{i18n.T("pages.weekly_review.prov_template"), gen.TemplateID + " v" + strconv.Itoa(gen.TemplateVersion)},
			[2]string{i18n.T("pages.weekly_review.prov_attempts"), strconv.Itoa(gen.Attempts)},
		)
		if n := len(gen.AIOutputIDs); n > 0 {
			rows = append(rows, [2]string{i18n.T("pages.weekly_review.prov_ai_calls"), strconv.Itoa(n)})
		}
		if gen.SkillName != "" || gen.SkillID != "" {
			name := gen.SkillName
			if name == "" {
				name = gen.SkillID
			}
			rows = append(rows, [2]string{i18n.T("pages.weekly_review.prov_skill"), name})
		}
		if gen.Notes != "" {
			rows = append(rows, [2]string{i18n.T("pages.weekly_review.prov_notes"), gen.Notes})
		}
		if gen.Error != "" {
			rows = append(rows, [2]string{i18n.T("pages.weekly_review.prov_error"), gen.Error})
		}
	}
	if coverage := report.Meta.Coverage; coverage != nil {
		rows = append(rows,
			[2]string{i18n.T("pages.weekly_review.prov_events"), strconv.Itoa(coverage.EventCount)},
			[2]string{i18n.T("pages.weekly_review.prov_timezone"), coverage.Timezone},
			[2]string{i18n.T("pages.weekly_review.prov_digest_hash"), coverage.DigestHash},
		)
		if coverage.Truncated {
			rows = append(rows, [2]string{i18n.T("pages.weekly_review.prov_truncated"), i18n.T("pages.weekly_review.yes")})
		}
	}
	return rows
}

// weeklyHeader is the report title, or the loading placeholder.
func weeklyHeader(report *weeklyreview.WeeklyReport) string {
	if report == nil {
		return i18n.T("pages.weekly_review.detail_title")
	}
	return report.Title
}

func weeklyPeriodLine(report *weeklyreview.WeeklyReport) string {
	if report == nil {
		return ""
	}
	return i18n.Tf("pages.weekly_review.detail_period", map[string]string{
		"start": report.Period.StartDate(),
		"end":   report.Period.EndDate(),
	})
}
