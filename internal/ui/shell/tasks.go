package shell

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
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
			if !t.Canceling {
				ui.Spinner(v.C).Size(tokens.Space3, tokens.Space3)
			}
			stage := t.Stage
			if stage == "" {
				stage = t.Name
			} else if t.Name != "" {
				stage = t.Name + " · " + stage
			}
			ui.Text(v.C, stage).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(p.TextPrimary))
			if t.Total > 0 {
				v.Progress(components.ProgressOptions{
					Value: float64(t.Done) / float64(t.Total),
					Label: fmt.Sprintf("%d of %d", t.Done, t.Total),
				}).Width(tokens.Space12 * 5)
			}
			if !t.Canceling {
				v.Button("Cancel", components.ButtonOptions{
					Variant: components.VariantGhost,
					Size:    components.SizeSmall,
					OnClick: func() { s.app.CancelTask(t.ID) },
				})
			} else {
				ui.Text(v.C, "Canceling…").
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(p.TextMuted))
			}
			ui.Text(v.C, "·").TextColor(mygo.Color(p.TextMuted))
		}
		for _, t := range failed {
			t := t
			v.StatusIndicator(components.Danger, failureText(t))
		}
		v.Spacer()
		if len(failed) > 0 {
			v.Button("Clear", components.ButtonOptions{
				Variant: components.VariantGhost,
				Size:    components.SizeSmall,
				OnClick: func() { s.app.ClearTasks() },
			})
		}
	})
}

// failureText names a failed task and its reason, shortened for the strip.
func failureText(t app.TaskSnapshot) string {
	msg := t.Name + " failed"
	if t.Err != nil {
		msg += ": " + t.Err.Error()
	}
	const limit = 120
	if len(msg) > limit {
		return msg[:limit] + "…"
	}
	return msg
}
