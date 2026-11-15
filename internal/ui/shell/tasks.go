package shell

import (
	"strconv"

	"github.com/egoist/mygo/ui"

	"researchos/internal/i18n"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// taskStrip shows the running long operation under the toolbar, with its
// stage, progress and a cancel entry where the operation supports one. It is
// the global task indicator of §17; terminal failures stay visible until the
// user clears them.
func (s *Shell) taskStrip(v *components.UI) {
	tasks := s.app.Tasks()
	if len(tasks) == 0 {
		return
	}
	var running, failed []app.TaskSnapshot
	for _, t := range tasks {
		switch {
		case !t.Finished:
			running = append(running, t)
		case t.State == app.TaskFailed:
			failed = append(failed, t)
		}
	}
	if len(running) == 0 && len(failed) == 0 {
		return
	}
	p := v.T.Palette
	row := ui.Row(v.C).
		FillWidth().
		Padding(tokens.Space1, tokens.PagePadding).
		Gap(tokens.ComponentGap).
		AlignItems(ui.Center).
		Background(mygo.Color(p.BgSubtle))
	row.Children(func() {
		for _, t := range running {
			if t.StepTotal > 0 {
				// A staged operation gets the presence panel: the
				// companion, the phase the stream is in, the section ticks
				// and the elapsed time, instead of a spinner and a bar that
				// only ever says "somewhere".
				opts := t.GenProgress()
				opts.Title = t.Name
				opts.Compact = true
				opts.CompanionSize = 26
				v.GenProgress(opts)
			} else {
				if !t.Canceling {
					ui.Spinner(v.C).Size(tokens.Space3, tokens.Space3)
				}
				stage := t.Stage
				if stage == "" {
					stage = t.Name
				} else if t.Name != "" {
					stage = i18n.Tf("shell.task.strip_stage", map[string]string{
						"name":  t.Name,
						"stage": stage,
					})
				}
				ui.Text(v.C, stage).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(p.TextPrimary))
				if t.Total > 0 {
					v.Progress(components.ProgressOptions{
						Value: float64(t.Done) / float64(t.Total),
						Label: i18n.Tf("shell.task.progress_count", map[string]string{
							"done":  strconv.Itoa(t.Done),
							"total": strconv.Itoa(t.Total),
						}),
					}).Width(tokens.Space12 * 5)
				}
			}
			if !t.Canceling {
				v.Button(i18n.T("shell.task.btn_cancel"), components.ButtonOptions{
					Variant: components.VariantGhost,
					Size:    components.SizeSmall,
					OnClick: func() { s.app.CancelTask(t.ID) },
				})
			} else {
				ui.Text(v.C, i18n.T("shell.task.canceling")).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(p.TextMuted))
			}
			ui.Text(v.C, i18n.T("shell.task.separator")).TextColor(mygo.Color(p.TextMuted))
		}
		for _, t := range failed {
			t := t
			v.StatusIndicator(components.Danger, failureText(t))
		}
		v.Spacer()
		if len(failed) > 0 {
			v.Button(i18n.T("shell.task.btn_clear"), components.ButtonOptions{
				Variant: components.VariantGhost,
				Size:    components.SizeSmall,
				OnClick: func() { s.app.ClearTasks() },
			})
		}
	})
}

// failureText names a failed task and its reason, shortened for the strip. The
// cut counts runes, so a Chinese message never ends in half a character.
func failureText(t app.TaskSnapshot) string {
	msg := i18n.Tf("shell.task.failed", map[string]string{"task": t.Name})
	if t.Err != nil {
		msg = i18n.Tf("shell.task.failed_detail", map[string]string{
			"task": t.Name,
			"err":  errtext.Text(t.Err),
		})
	}
	const limit = 120
	runes := []rune(msg)
	if len(runes) > limit {
		return string(runes[:limit]) + i18n.T("shell.task.ellipsis")
	}
	return msg
}
