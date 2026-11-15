package components

import (
	"github.com/egoist/mygo/ui"

	"researchos/internal/i18n"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// bannerIcon is the icon that states the tone of a banner or empty state.
func bannerIcon(t Tone) *icons.Icon {
	switch t {
	case Success:
		return icons.Success
	case Warning:
		return icons.Warning
	case Danger:
		return icons.Failure
	case Info:
		return icons.Info
	}
	return icons.Info
}

// BannerOptions configures an inline banner: a note inside the page, never a
// modal, used for missing note files and invalid external references
// (SPEC-01 §6).
type BannerOptions struct {
	Tone Tone
	// Title states the problem in one line.
	Title string
	// Message explains it and what the user can do.
	Message string
	// Icon overrides the tone's icon.
	Icon *icons.Icon
	// ActionLabel and OnAction add the recovery action, as "Relocate".
	ActionLabel string
	OnAction    func()
}

// Banner builds an inline banner with a tinted background row. The text and
// the icon carry the meaning; the tone color only reinforces it.
func (u *UI) Banner(o BannerOptions) ui.Element {
	icon := o.Icon
	if icon == nil {
		icon = bannerIcon(o.Tone)
	}
	fg := u.foreground(o.Tone)
	row := ui.Row(u.C).
		FillWidth().
		Padding(tokens.Space3, tokens.PanelPadding).
		Gap(tokens.RelatedGap).
		AlignItems(ui.Start).
		Background(mygo.Color(u.background(o.Tone))).
		Border(1, mygo.Color(fg.Mix(u.T.Palette.BgSurface, 0.55))).
		Radius(tokens.RadiusLG)
	row.Children(func() {
		u.Icon(icon, icons.SizeAction).TextColor(mygo.Color(fg))
		ui.Column(u.C).Grow(1).Gap(tokens.Space1).Children(func() {
			if o.Title != "" {
				ui.Text(u.C, o.Title).
					FontSize(tokens.Typography.Body.Size).
					FontWeight(int(tokens.WeightSemibold)).
					TextColor(mygo.Color(u.T.Palette.TextPrimary))
			}
			if o.Message != "" {
				ui.Text(u.C, o.Message).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(u.T.Palette.TextSecondary))
			}
		})
		if o.ActionLabel != "" {
			u.Button(o.ActionLabel, ButtonOptions{Variant: VariantGhost, Size: SizeSmall, OnClick: o.OnAction})
		}
	})
	return row
}

// EmptyStateOptions configures an empty state. All three parts are required by
// ui_plan.md §10.3: what is missing, why, and what the user can do next.
type EmptyStateOptions struct {
	Icon   *icons.Icon
	Title  string
	Reason string
	// ActionLabel and OnAction offer the next step. A UI without an action
	// still explains why the area is empty.
	ActionLabel string
	OnAction    func()
}

// EmptyStateMinimumHeight is the floor of an empty state: the explanation
// centers in a block tall enough to read as the page's content, rather than
// hugging the toolbar above it.
const EmptyStateMinimumHeight float32 = 360

// EmptyState builds a centered explanation of an empty area. A bare blank
// region is never an acceptable empty state.
func (u *UI) EmptyState(o EmptyStateOptions) ui.Element {
	p := u.T.Palette
	icon := o.Icon
	if icon == nil {
		icon = icons.FileText
	}
	return ui.Column(u.C).
		FillWidth().
		MinHeight(EmptyStateMinimumHeight).
		Padding(tokens.PageBlockGap).
		Gap(tokens.RelatedGap).
		AlignItems(ui.Center).
		Justify(ui.Center).
		Children(func() {
			u.Icon(icon, tokens.Space6).TextColor(mygo.Color(p.TextMuted))
			if o.Title != "" {
				ui.Text(u.C, o.Title).
					FontSize(tokens.Typography.CardTitle.Size).
					FontWeight(int(tokens.WeightSemibold)).
					TextColor(mygo.Color(p.TextPrimary))
			}
			if o.Reason != "" {
				ui.Text(u.C, o.Reason).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(p.TextSecondary)).
					MaxWidth(tokens.Space12 * 8)
			}
			if o.ActionLabel != "" {
				// The button hugs its content, so the row centers it: a
				// column child would take the whole width and snap left.
				ui.Row(u.C).Justify(ui.Center).Children(func() {
					u.Button(o.ActionLabel, ButtonOptions{Variant: VariantPrimary, Size: SizeMedium, Icon: icons.Plus, OnClick: o.OnAction})
				})
			}
		})
}

// ErrorStateOptions configures an error state. It names what failed, why in
// readable terms, what the user can do about it, and optionally the error
// code for a diagnostic report (ui_plan.md §10.4).
type ErrorStateOptions struct {
	Title      string
	Cause      string
	Suggestion string
	Code       string
	RetryLabel string
	OnRetry    func()
}

// ErrorState builds a readable error state with a recovery action.
func (u *UI) ErrorState(o ErrorStateOptions) ui.Element {
	p := u.T.Palette
	return ui.Column(u.C).
		Fill().
		Padding(tokens.PageBlockGap).
		Gap(tokens.RelatedGap).
		AlignItems(ui.Center).
		Justify(ui.Center).
		Children(func() {
			u.Icon(icons.Warning, tokens.Space6).TextColor(mygo.Color(p.StatusDanger))
			if o.Title != "" {
				ui.Text(u.C, o.Title).
					FontSize(tokens.Typography.CardTitle.Size).
					FontWeight(int(tokens.WeightSemibold)).
					TextColor(mygo.Color(p.TextPrimary))
			}
			if o.Cause != "" {
				ui.Text(u.C, o.Cause).
					FontSize(tokens.Typography.Body.Size).
					TextColor(mygo.Color(p.TextPrimary)).
					MaxWidth(tokens.Space12 * 8)
			}
			if o.Suggestion != "" {
				ui.Text(u.C, o.Suggestion).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(p.TextSecondary)).
					MaxWidth(tokens.Space12 * 8)
			}
			if o.Code != "" {
				ui.Text(u.C, i18n.Tf("components.error_state.code", map[string]string{"code": o.Code})).
					FontSize(tokens.Typography.Caption.Size).
					Font(tokens.SystemFonts.Mono).
					TextColor(mygo.Color(p.TextMuted))
			}
			if o.RetryLabel != "" {
				u.Button(o.RetryLabel, ButtonOptions{Variant: VariantSecondary, Size: SizeMedium, Icon: icons.Refresh, OnClick: o.OnRetry})
			}
		})
}

// SkeletonOptions configures a loading placeholder. Give it the size the
// content will take: a skeleton must not move the layout when it is replaced.
type SkeletonOptions struct {
	// Lines draws that many text lines; Width and Height shape one block
	// when Lines is 0.
	Lines  int
	Width  float32
	Height float32
}

// Skeleton builds the loading placeholder of a block or of a paragraph. It
// reserves exactly the space the content will use.
func (u *UI) Skeleton(o SkeletonOptions) ui.Element {
	bg := mygo.Color(u.T.Palette.BgSubtle)
	block := func(w, h float32) {
		b := ui.Box(u.C).Height(h).Radius(tokens.RadiusMD).Background(bg)
		if w > 0 {
			b.Width(w)
		} else {
			b.FillWidth()
		}
	}
	if o.Lines <= 0 {
		h := o.Height
		if h <= 0 {
			h = tokens.Space4
		}
		col := ui.Column(u.C)
		col.Children(func() { block(o.Width, h) })
		return col
	}
	h := o.Height
	if h <= 0 {
		h = tokens.Space3
	}
	return ui.Column(u.C).Gap(tokens.ComponentGap).Children(func() {
		for range o.Lines {
			block(o.Width, h)
		}
	})
}

// ProgressOptions configures a progress bar.
type ProgressOptions struct {
	// Value is 0 to 1, or negative for activity of unknown length.
	Value float64
	Label string
}

// Progress builds a progress bar in the accent color. Long tasks always show
// progress or, when the total is unknown, an indeterminate bar (ui_plan.md
// §17).
func (u *UI) Progress(o ProgressOptions) ui.Element {
	p := ui.Progress(u.C, o.Value)
	if o.Label != "" {
		p.Label(o.Label)
	}
	return p
}

// Spinner builds a small activity indicator, for buttons and inline status.
func (u *UI) Spinner(size float32) ui.Element {
	if size <= 0 {
		size = icons.SizeDefault
	}
	return ui.Spinner(u.C).Size(size, size)
}

// ToastAction is the one optional control a toast offers beneath its message,
// drawn as a link: an accent-colored, hoverable line the whole of which is the
// press target. It carries no frame of its own, so it reads as part of the
// message rather than as a second action bar.
type ToastAction struct {
	// Label is the visible text, normally a path the caller may open.
	Label string
	// OnClick runs on click or on Enter and Space.
	OnClick func()
}

// ToastOptions configures one global feedback message.
type ToastOptions struct {
	Tone    Tone
	Title   string
	Message string
	// Action is drawn under the message when set; nil leaves the toast as a
	// plain message.
	Action *ToastAction
	// OnDismiss closes the toast.
	OnDismiss func()
}

// ToastCard builds one feedback message: a surface panel with the tone icon,
// the title, the message and a close button. The panel floats above the page
// but never takes the keyboard focus by itself.
func (u *UI) ToastCard(o ToastOptions) ui.Element {
	p := u.T.Palette
	fg := u.foreground(o.Tone)
	row := ui.Row(u.C).
		FillWidth().
		Padding(tokens.Space3, tokens.PanelPadding).
		Gap(tokens.RelatedGap).
		AlignItems(ui.Start).
		Background(mygo.Color(p.BgSurface)).
		Border(1, mygo.Color(fg.Mix(p.BgSurface, 0.55))).
		Radius(tokens.RadiusLG).
		Shadow(0, 4, 12, 0, mygo.Color(tokens.Shadow))
	row.Children(func() {
		u.Icon(bannerIcon(o.Tone), icons.SizeAction).TextColor(mygo.Color(fg))
		ui.Column(u.C).Grow(1).Gap(tokens.Space1).Children(func() {
			if o.Title != "" {
				ui.Text(u.C, o.Title).
					FontSize(tokens.Typography.Body.Size).
					FontWeight(int(tokens.WeightSemibold)).
					TextColor(mygo.Color(p.TextPrimary))
			}
			if o.Message != "" {
				ui.Text(u.C, o.Message).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(p.TextSecondary))
			}
			if o.Action != nil && o.Action.Label != "" && o.Action.OnClick != nil {
				u.toastAction(o.Action)
			}
		})
		if o.OnDismiss != nil {
			u.IconButton(icons.Close, i18n.T("components.toast.dismiss"), IconButtonOptions{
				Variant: VariantGhost,
				Size:    SizeSmall,
				OnClick: o.OnDismiss,
			})
		}
	})
	return row
}

// toastAction draws the toast's optional link. The accent color, the hover
// tint and the underline while hovered carry the affordance; the icon says the
// click leaves the window. It is a button, so Enter and Space activate it as a
// click does, and its accessible name is the label itself.
func (u *UI) toastAction(a *ToastAction) {
	p := u.T.Palette
	link := ui.ButtonBase(u.C).
		FillWidth().
		Padding(tokens.Space1, tokens.Space1).
		Radius(tokens.RadiusSM).
		Gap(tokens.Space1).
		AlignItems(ui.Center).
		Cursor(ui.CursorPointer).
		TextColor(mygo.Color(p.AccentDefault)).
		Label(a.Label)
	hovered := link.Hovered()
	if hovered {
		link.Background(mygo.Color(p.AccentSubtle))
	}
	link.OnClick(a.OnClick)
	link.Children(func() {
		u.Icon(icons.ExternalLink, tokens.Typography.Secondary.Size)
		label := ui.Text(u.C, a.Label).
			Font(tokens.SystemFonts.Mono).
			FontSize(tokens.Typography.Secondary.Size).
			Grow(1).
			SingleLine().
			TruncateMiddle()
		if hovered {
			label.Underline()
		}
	})
}
