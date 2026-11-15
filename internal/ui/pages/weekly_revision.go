package pages

// Section-level revision of a weekly report (SPEC-09 §5.3 局部修订): the user
// selects the part they are unhappy with, or picks a section in the outline,
// writes a temporary instruction and gets only that section rewritten. The
// dialog shows the same generation presence as a full regeneration — phase,
// percent, elapsed time and the companion — and the task strip shows it too,
// because the call runs as a task.
//
// The instruction is cleared once the rewrite succeeds and never leaves the
// page: the service prompt carries it for one call, nothing is written to a
// skill or to settings.

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"researchos/internal/i18n"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
	"researchos/internal/weeklyreview"
)

// weeklyRevision is the revision dialog's state. The textarea writes notes on
// the frame goroutine; the task owns the progress and is read through its
// snapshot, so a render between deltas always sees a consistent state.
type weeklyRevision struct {
	open    bool
	section weeklyreview.SectionKey
	excerpt string
	notes   string
	err     string
	running bool
	failed  bool
	// doneAt is when the rewrite succeeded. The dialog holds the completion
	// frame — 100%, the happy companion — for a moment before closing.
	doneAt time.Time
	taskID int64
	task   *app.Task
}

// revisionDoneHold is how long the completion state stays on screen.
const revisionDoneHold = 1100 * time.Millisecond

// revisionSnapshot copies the state a frame renders from.
func (d *WeeklyReportView) revisionSnapshot() weeklyRevision {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.revision
}

// openRevision opens the dialog for one section, with the selection as the
// optional focus excerpt.
func (d *WeeklyReportView) openRevision(section weeklyreview.SectionKey, excerpt string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.revision = weeklyRevision{
		open:    true,
		section: section,
		excerpt: strings.TrimSpace(excerpt),
	}
}

// revisionDialog builds the revision dialog: the excerpt, the temporary
// instruction, and while it runs the live progress panel in place of the
// form.
func (d *WeeklyReportView) revisionDialog(v *components.UI, a *app.App, projectID string) {
	state := d.revisionSnapshot()
	if !state.open {
		return
	}
	done := !state.running && !state.failed && !state.doneAt.IsZero()
	if done {
		left := revisionDoneHold - time.Since(state.doneAt)
		if left <= 0 {
			d.closeRevision(a)
			return
		}
		v.C.After(left)
	}
	title := weeklyreview.SectionTitle(state.section)
	p := v.T.Palette
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title: i18n.T("pages.weekly_review.revise_title"),
		Description: i18n.Tf("pages.weekly_review.revise_desc", map[string]string{
			"title": title,
		}),
		Width: 620,
		Actions: func() {
			if done {
				return
			}
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				d.closeRevision(a)
				open = false
				return
			}
			disabled := state.running || (strings.TrimSpace(d.revision.notes) == "" && state.excerpt == "")
			if v.Button(i18n.T("pages.weekly_review.revise_submit"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  state.running,
				Disabled: disabled,
			}).Clicked() {
				d.runRevision(a, projectID)
			}
		},
	}, func() {
		ui.Row(v.C).FillWidth().Gap(tokens.Space2).AlignItems(ui.Center).Children(func() {
			v.Badge(title, components.Neutral)
			if state.running {
				ui.Text(v.C, i18n.T("pages.weekly_review.revise_running")).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(p.TextMuted))
			}
		})
		if state.running || state.failed || done {
			opts := components.GenProgressOptions{
				Phase:         components.GenPhaseFailed,
				Percent:       -1,
				Steps:         []components.GenStep{{Label: title, State: components.GenStepActive}},
				Index:         1,
				Total:         1,
				Current:       title,
				CompanionSize: 88,
			}
			if state.task != nil {
				opts = state.task.Snapshot().GenProgress()
				opts.CompanionSize = 88
			}
			if state.failed {
				opts.Phase = components.GenPhaseFailed
			}
			v.GenProgress(opts)
		}
		if state.excerpt != "" && !done {
			ui.Column(v.C).FillWidth().Gap(tokens.Space1).Children(func() {
				ui.Text(v.C, i18n.T("pages.weekly_review.revise_excerpt_label")).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(p.TextSecondary))
				ui.Text(v.C, revisionExcerptText(state.excerpt)).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(p.TextPrimary)).
					Padding(tokens.Space2, tokens.Space3).
					Radius(tokens.RadiusMD).
					Background(mygo.Color(p.BgSubtle))
			})
		}
		if state.running || done {
			// Nothing to type while the one call in flight is being
			// written, or while the completion frame shows.
			return
		}
		v.Textarea(&d.revision.notes, components.FieldOptions{
			Label:       i18n.T("pages.weekly_review.revise_notes_label"),
			Placeholder: i18n.T("pages.weekly_review.revise_notes_placeholder"),
			AutoFocus:   true,
		}).Lines(3, 8)
		if state.err != "" {
			ui.Text(v.C, state.err).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(p.StatusDanger))
		}
	})
	if !open {
		d.closeRevision(a)
	}
}

// closeRevision closes the dialog and stops a running call: the stream's
// context is canceled, so nothing is stored for an abandoned rewrite.
func (d *WeeklyReportView) closeRevision(a *app.App) {
	d.mu.Lock()
	taskID := d.revision.taskID
	running := d.revision.running
	d.revision.open = false
	d.revision.notes = ""
	d.revision.err = ""
	d.mu.Unlock()
	if running && a != nil {
		a.CancelTask(taskID)
	}
}

// runRevision starts the section rewrite as a task, so the strip and the
// dialog show the same progress.
func (d *WeeklyReportView) runRevision(a *app.App, projectID string) {
	d.mu.Lock()
	state := d.revision
	report := d.report
	if state.running || report == nil || d.Services.Weekly == nil {
		d.mu.Unlock()
		return
	}
	notes := strings.TrimSpace(d.revision.notes)
	if notes == "" && state.excerpt == "" {
		d.revision.err = i18n.T("pages.weekly_review.revise_need_notes")
		d.mu.Unlock()
		return
	}
	section := state.section
	excerpt := state.excerpt
	reportID := report.ID
	base := report.ContentHash
	old := report.ContentMD
	skillID := ""
	if gen := report.Meta.Generation; gen != nil {
		skillID = gen.SkillID
	}
	buffer := ""
	if d.ed != nil {
		buffer = d.ed.Buffer.String()
	}
	d.revision.running = true
	d.revision.failed = false
	d.revision.err = ""
	d.mu.Unlock()

	api := d.Services.Weekly
	title := weeklyreview.SectionTitle(section)
	task := a.RunTask(i18n.T("pages.weekly_review.task_revise"), func(ctx context.Context, t *app.Task) error {
		fail := func(err error) {
			d.mu.Lock()
			d.revision.running = false
			d.revision.failed = true
			d.revision.err = weeklyRevisionError(err)
			d.mu.Unlock()
			a.Toast(app.LevelDanger, i18n.T("pages.weekly_review.toast_revise_fail"), weeklyRevisionError(err))
			a.Notify()
		}
		t.SetPhase(app.TaskPhaseThinking)
		t.Steps(0, 1, title)
		t.Progress(4, 100)
		t.Stage(i18n.T("pages.weekly_review.stage_save"))
		if buffer != "" && buffer != old {
			updated, err := api.UpdateContent(ctx, projectID, weeklyreview.UpdateContentInput{
				ReportID: reportID, ContentMD: buffer, ExpectedHash: base, Actor: weeklyreview.ActorUser,
			})
			if err != nil {
				fail(err)
				return nil
			}
			base = updated.ContentHash
		}
		t.Stage(i18n.T("pages.weekly_review.stage_revise"))
		progress := func(_ int, _ int, key weeklyreview.SectionKey) {
			t.SetPhase(app.TaskPhaseThinking)
			t.Steps(0, 1, weeklyreview.SectionTitle(key))
			t.Progress(6, 100)
			a.Notify()
		}
		var (
			runes int
			phase app.TaskPhase
			mu    sync.Mutex
		)
		delta := func(_ weeklyreview.SectionKey, text string) {
			mu.Lock()
			runes += utf8.RuneCountInString(text)
			// 700 streamed runes fill most of one section; the store closes
			// the rest.
			pct := 0.08 + min(float64(runes)/700, 0.84)
			flip := phase != app.TaskPhaseWriting
			phase = app.TaskPhaseWriting
			mu.Unlock()
			t.Progress(int(pct*100), 100)
			if flip {
				t.SetPhase(app.TaskPhaseWriting)
				a.Notify()
			}
		}
		outcome, err := api.RegenerateSection(ctx, projectID, weeklyreview.SectionRevisionInput{
			ReportID:     reportID,
			Section:      section,
			FocusText:    excerpt,
			Instructions: notes,
			ExpectedHash: base,
			SkillID:      skillID,
			Actor:        weeklyreview.ActorUser,
			Progress:     progress,
			Delta:        delta,
		})
		switch {
		case outcome != nil && outcome.Report != nil:
			d.mu.Lock()
			d.report = outcome.Report
			d.revision.running = false
			d.revision.doneAt = time.Now()
			d.revision.excerpt = ""
			d.revision.notes = ""
			d.revision.err = ""
			d.needReset = true
			d.mu.Unlock()
			t.Steps(1, 1, title)
			t.Progress(100, 100)
			a.Toast(app.LevelSuccess, i18n.T("pages.weekly_review.toast_revised"), title)
			d.shared.bump()
		case ctx.Err() != nil:
			// Canceled: no write happened, so the draft is what it was.
			d.mu.Lock()
			d.revision.running = false
			d.mu.Unlock()
			a.Toast(app.LevelInfo, i18n.T("pages.weekly_review.toast_revise_canceled"), "")
			d.reload(projectID)
		case err != nil:
			fail(err)
		default:
			d.mu.Lock()
			d.revision.running = false
			d.mu.Unlock()
		}
		a.Notify()
		return nil
	})
	d.mu.Lock()
	d.revision.taskID = task.ID()
	d.revision.task = task
	d.mu.Unlock()
}

// weeklyRevisionError is the sentence the revision dialog shows. The service
// message names the section and the model failure, which the generic
// ErrAIUnavailable wording (a whole-draft skeleton) would misdescribe.
func weeklyRevisionError(err error) string {
	if se := weeklyreview.AsServiceError(err); se != nil && se.Message != "" {
		return se.Message
	}
	return weeklyError(err)
}

// revisionExcerptText keeps a long selection readable in the dialog: the
// prompt still gets the whole excerpt.
func revisionExcerptText(text string) string {
	const limit = 320
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + i18n.T("shell.task.ellipsis")
}

// weeklySelectionExcerpt returns the editor's selected text, trimmed, or "".
func (d *WeeklyReportView) weeklySelectionExcerpt() string {
	if d.ed == nil {
		return ""
	}
	return strings.TrimSpace(d.ed.SelectedText())
}

// weeklySelectionStart is the smaller offset of the newest selection, which
// names the section the selection belongs to.
func (d *WeeklyReportView) weeklySelectionStart() int {
	if d.ed == nil {
		return 0
	}
	sel := d.ed.Selection()
	return min(sel.Anchor, sel.Head)
}

// weeklySectionAt maps a byte offset in the draft to the section it belongs
// to: the last heading above it, or, above the first heading, the first one
// below.
func weeklySectionAt(content string, offset int) (weeklyreview.SectionKey, bool) {
	offset = max(0, min(offset, len(content)))
	var above, below weeklyreview.SectionKey
	pos := 0
	for pos <= len(content) {
		at := pos
		end := strings.IndexByte(content[pos:], '\n')
		var line string
		if end < 0 {
			line = content[pos:]
			pos = len(content) + 1
		} else {
			line = content[pos : pos+end]
			pos += end + 1
		}
		key, ok := weeklySectionKeyOf(line)
		if !ok {
			continue
		}
		if at <= offset {
			above = key
		} else if below == "" {
			below = key
		}
	}
	if above != "" {
		return above, true
	}
	if below != "" {
		return below, true
	}
	return "", false
}

// weeklySectionKeyOf maps a heading line or an outline title to its template
// section. "## 3. 理论研究进展", "## 理论研究进展" and the bare title the
// outline shows all resolve to the same key.
func weeklySectionKeyOf(line string) (weeklyreview.SectionKey, bool) {
	text := strings.TrimSpace(line)
	if strings.HasPrefix(text, "###") {
		return "", false
	}
	if strings.HasPrefix(text, "##") {
		text = strings.TrimSpace(strings.TrimPrefix(text, "##"))
	}
	text = strings.TrimSpace(strings.TrimLeft(text, "0123456789"))
	text = strings.TrimLeft(text, ".、,，:：)）]】")
	text = strings.TrimSpace(text)
	text = strings.NewReplacer("（", "(", "）", ")").Replace(text)
	for _, sec := range weeklyreview.DefaultTemplate().Sections {
		if text == sec.Title {
			return sec.Key, true
		}
	}
	return "", false
}
