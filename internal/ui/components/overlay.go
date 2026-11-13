package components

import (
	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/confirm"
	"researchos/internal/i18n"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// Tooltip attaches an explanatory tip to an anchor. Tips are for
// explanation; an icon-only control still needs an accessible label.
func (u *UI) Tooltip(anchor ui.Element, text string) ui.Element {
	if text == "" {
		return anchor
	}
	return ui.TooltipBase(u.C, anchor, func(tip ui.Element) {
		tip.Padding(tokens.Space1, tokens.Space2).
			Radius(tokens.RadiusMD).
			Background(mygo.Color(u.T.Palette.TextPrimary)).
			TextColor(mygo.Color(u.T.Palette.BgSurface)).
			Children(func() {
				ui.Text(u.C, text).FontSize(tokens.Typography.Caption.Size)
			})
	})
}

// Popover shows body beside anchor while *open is true. Escape or a press
// outside closes it, setting *open to false.
func (u *UI) Popover(anchor ui.Element, open *bool, body func()) ui.Element {
	return ui.PopoverBase(u.C, anchor, open, func(panel ui.Element) {
		panel.Padding(tokens.Space3).
			Gap(tokens.ComponentGap).
			Radius(tokens.RadiusLG).
			Background(mygo.Color(u.T.Palette.BgSurface)).
			Border(1, mygo.Color(u.T.Palette.BorderDefault)).
			Shadow(0, 6, 20, 0, mygo.Color(tokens.Shadow))
		body()
	})
}

// DialogOptions configures a modal dialog (ui_plan.md §7.1).
type DialogOptions struct {
	Title       string
	Description string
	// Width of the panel; zero uses 480px.
	Width float32
	// Actions builds the footer controls; they are laid out at the end.
	Actions func()
}

// Dialog builds a modal dialog over a dimmed window. It keeps the keyboard
// while open, Tab cycles inside it, Escape closes it, and the focus returns
// to the control that opened it when it closes.
func (u *UI) Dialog(open *bool, o DialogOptions, body func()) ui.Element {
	width := o.Width
	if width <= 0 {
		width = tokens.Space12 * 10
	}
	return ui.DialogBase(u.C, open, func(backdrop, panel ui.Element) {
		backdrop.Background(mygo.Color(tokens.Scrim))
		panel.Width(width).
			Padding(tokens.Space6).
			Gap(tokens.RelatedGap).
			Radius(tokens.RadiusXL).
			Background(mygo.Color(u.T.Palette.BgSurface)).
			Border(1, mygo.Color(u.T.Palette.BorderDefault)).
			Shadow(0, 8, 24, 0, mygo.Color(tokens.Shadow))
		ui.Column(u.C).Gap(tokens.Space1).Children(func() {
			if o.Title != "" {
				ui.Text(u.C, o.Title).
					FontSize(tokens.Typography.SectionTitle.Size).
					FontWeight(int(tokens.Typography.SectionTitle.Weight)).
					TextColor(mygo.Color(u.T.Palette.TextPrimary))
			}
			if o.Description != "" {
				ui.Text(u.C, o.Description).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(u.T.Palette.TextSecondary))
			}
		})
		body()
		if o.Actions != nil {
			ui.Row(u.C).Gap(tokens.ComponentGap).Justify(ui.End).Children(o.Actions)
		}
	})
}

// ConfirmOptions configures a confirmation dialog for a destructive action.
// It collects the human confirmation; the confirm service then issues the
// token the service layer requires (SPEC-01 §6).
type ConfirmOptions struct {
	Open    *bool
	Title   string
	Message string
	// ConfirmLabel names the destructive action, as "Permanently delete".
	// It defaults to "Confirm".
	ConfirmLabel string
	// CancelLabel defaults to "Cancel".
	CancelLabel string
	Destructive bool
	// Service issues the confirmation token. It must not be nil: without
	// it the dialog refuses to confirm, and the destructive action cannot
	// be authorized.
	Service confirm.Service
	// Scope binds the token to the operation and targets.
	Scope confirm.Scope
	// OnConfirmed runs once the token is issued, before the caller consumes
	// it in the service call. The dialog is already closed at that point.
	OnConfirmed func(confirm.Token)
}

// confirmState is the local state of one confirmation dialog.
type confirmState struct {
	err string
}

// ConfirmDialog builds the system's destructive confirmation: a message, a
// cancel, and the destructive button last. The token is issued only from this
// in-app click, and the caller consumes it in the service call (the services
// trust the token, never the dialog).
func (u *UI) ConfirmDialog(o ConfirmOptions) ui.Element {
	confirmLabel := o.ConfirmLabel
	if confirmLabel == "" {
		confirmLabel = i18n.T("components.btn.confirm")
	}
	cancelLabel := o.CancelLabel
	if cancelLabel == "" {
		cancelLabel = i18n.T("common.cancel")
	}
	variant := VariantPrimary
	if o.Destructive {
		variant = VariantDanger
	}
	return ui.DialogBase(u.C, o.Open, func(backdrop, panel ui.Element) {
		st := ui.Local(panel, "confirm", func() confirmState { return confirmState{} })
		backdrop.Background(mygo.Color(tokens.Scrim))
		panel.Width(tokens.Space12*10).
			Padding(tokens.Space6).
			Gap(tokens.RelatedGap).
			Radius(tokens.RadiusXL).
			Background(mygo.Color(u.T.Palette.BgSurface)).
			Border(1, mygo.Color(u.T.Palette.BorderDefault)).
			Shadow(0, 8, 24, 0, mygo.Color(tokens.Shadow))

		ui.Text(u.C, o.Title).
			FontSize(tokens.Typography.SectionTitle.Size).
			FontWeight(int(tokens.Typography.SectionTitle.Weight)).
			TextColor(mygo.Color(u.T.Palette.TextPrimary))
		if o.Message != "" {
			ui.Text(u.C, o.Message).
				FontSize(tokens.Typography.Body.Size).
				TextColor(mygo.Color(u.T.Palette.TextSecondary))
		}
		if st.err != "" {
			ui.Text(u.C, st.err).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(u.T.Palette.StatusDanger))
		}
		ui.Row(u.C).Gap(tokens.ComponentGap).Justify(ui.End).Children(func() {
			if u.Button(cancelLabel, ButtonOptions{Variant: VariantSecondary, Size: SizeMedium}).Clicked() {
				*o.Open = false
			}
			if o.Service == nil {
				u.Button(confirmLabel, ButtonOptions{Variant: variant, Size: SizeMedium, Disabled: true})
				return
			}
			if u.Button(confirmLabel, ButtonOptions{Variant: variant, Size: SizeMedium}).Clicked() {
				tok, err := o.Service.Issue(u.Context(), o.Scope)
				if err != nil {
					st.err = i18n.Tf("components.confirm.issue_failed", map[string]string{
						"error": errtext.Text(err),
					})
					return
				}
				st.err = ""
				*o.Open = false
				if o.OnConfirmed != nil {
					o.OnConfirmed(tok)
				}
			}
		})
	})
}
