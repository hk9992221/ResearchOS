package shell

import (
	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/components"
	"researchos/internal/ui/tokens"
)

// toasts draws the global feedback queue at the bottom right, above the page.
// It is the single place success and failure messages appear (§15.9).
func (s *Shell) toasts(v *components.UI) {
	list := s.app.Toasts()
	if len(list) == 0 {
		return
	}
	ui.Column(v.C).
		Absolute().
		End(tokens.PagePadding).
		Bottom(tokens.PagePadding).
		Width(tokens.Space12 * 7).
		Gap(tokens.Space2).
		Children(func() {
			for _, t := range list {
				t := t
				var action *components.ToastAction
				if t.Action != nil {
					action = &components.ToastAction{
						Label:   t.Action.Label,
						OnClick: t.Action.OnClick,
					}
				}
				v.ToastCard(components.ToastOptions{
					Tone:    toastTone(t.Level),
					Title:   t.Title,
					Message: t.Message,
					Action:  action,
					OnDismiss: func() {
						s.app.DismissToast(t.ID)
					},
				})
			}
		})
}
