package components

import (
	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// ButtonVariant selects the visual weight of a button (ui_plan.md §7.1).
type ButtonVariant int

const (
	// VariantSecondary is the default button: a surface with a border.
	VariantSecondary ButtonVariant = iota
	// VariantPrimary is the main action of a page or dialog.
	VariantPrimary
	// VariantGhost has no face until hovered, for toolbars and dense rows.
	VariantGhost
	// VariantDanger performs a destructive action and must confirm first.
	VariantDanger
)

// ButtonSize selects a control height from the 4px grid.
type ButtonSize int

const (
	// SizeMedium is the default: 32px high, 14px text.
	SizeMedium ButtonSize = iota
	// SizeSmall is for dense toolbars and table rows: 28px high, 13px text.
	SizeSmall
	// SizeLarge is for primary page actions: 36px high, 16px text.
	SizeLarge
)

// ButtonOptions configures a button. The zero value is a medium secondary
// button, which is what most pages need.
type ButtonOptions struct {
	Variant ButtonVariant
	Size    ButtonSize
	// Icon goes before the label.
	Icon *icons.Icon
	// Tooltip explains the action on hover; icon-only buttons need it.
	Tooltip string
	// Label names the button for assistive technology, for a button that
	// shows only an icon.
	Label    string
	Disabled bool
	// Loading disables the button and overlays a spinner without moving the
	// label: the button keeps its size while the action runs.
	Loading bool
	// Width fixes the button's width; zero lets the content size it.
	Width   float32
	OnClick func()

	// iconOnly makes a square button of one size, with no horizontal
	// padding beyond what centers its icon. IconButton sets it.
	iconOnly bool
}

// buttonMetrics are the token-derived measurements of one size.
type buttonMetrics struct {
	padV, padH float32
	fontSize   float32
	minHeight  float32
	iconSize   float32
}

func buttonMetricsFor(s ButtonSize) buttonMetrics {
	switch s {
	case SizeSmall:
		return buttonMetrics{padV: tokens.Space1, padH: tokens.Space2, fontSize: tokens.Typography.Secondary.Size, minHeight: tokens.Space7, iconSize: icons.SizeDefault}
	case SizeLarge:
		return buttonMetrics{padV: tokens.Space2, padH: tokens.Space4, fontSize: tokens.Typography.CardTitle.Size, minHeight: tokens.Space9, iconSize: icons.SizeAction}
	}
	return buttonMetrics{padV: tokens.Space1, padH: tokens.Space3, fontSize: tokens.Typography.Body.Size, minHeight: tokens.Space8, iconSize: icons.SizeDefault}
}

// buttonColors resolves the state colors of a variant from the tokens.
func (u *UI) buttonColors(v ButtonVariant) (bg, hover, pressed, fg, border tokens.Color) {
	p := u.T.Palette
	switch v {
	case VariantPrimary:
		return p.AccentDefault, p.AccentHover, p.AccentHover.Mix(p.TextPrimary, 0.12), p.AccentDefault.ReadableText(), p.AccentDefault
	case VariantGhost:
		return tokens.Transparent, p.BgHover, p.BgSubtle, p.TextPrimary, tokens.Transparent
	case VariantDanger:
		hover := p.StatusDanger.Mix(p.TextPrimary, 0.12)
		return p.StatusDanger, hover, hover, p.StatusDanger.ReadableText(), p.StatusDanger
	}
	return p.BgSurface, p.BgHover, p.BgSubtle, p.TextPrimary, p.BorderDefault
}

// Button builds a button whose face follows the design tokens. Hover, press,
// focus, disabled and loading come from MyGo's headless base, so the states
// are the same as every other control's.
func (u *UI) Button(text string, o ButtonOptions) ui.Element {
	m := buttonMetricsFor(o.Size)
	if o.iconOnly {
		m.padH = 0
	}
	bg, hover, pressed, fg, border := u.buttonColors(o.Variant)

	btn := ui.ButtonBase(u.C)
	btn.Padding(m.padV, m.padH).
		MinHeight(m.minHeight).
		Radius(tokens.RadiusMD).
		Border(1, mygo.Color(border)).
		Background(mygo.Color(bg)).
		TextColor(mygo.Color(fg)).
		Gap(tokens.Space2).
		AlignItems(ui.Center).
		Justify(ui.Center)
	if o.iconOnly {
		btn.Width(m.minHeight)
	} else if o.Width > 0 {
		btn.Width(o.Width)
	}
	if o.Tooltip != "" {
		btn.Tooltip(o.Tooltip)
	}
	if o.Label != "" {
		btn.Label(o.Label)
	}
	if o.Disabled || o.Loading {
		btn.Disabled(true)
	}
	if o.Loading {
		btn.Busy(true)
	}
	if o.OnClick != nil {
		btn.OnClick(o.OnClick)
	}

	if btn.Pressed() {
		btn.Background(mygo.Color(pressed))
	} else if btn.Hovered() {
		btn.Background(mygo.Color(hover))
	}

	btn.Children(func() {
		content := func() {
			if o.Icon != nil {
				u.Icon(o.Icon, m.iconSize)
			}
			if text != "" {
				label := ui.Text(u.C, text).FontSize(m.fontSize).FontWeight(int(tokens.WeightMedium)).SingleLine()
				if o.Loading {
					// Clear the label without hiding it from assistive
					// technology or from the layout: the spinner draws over
					// the room the label keeps.
					label.TextColor(mygo.Color(tokens.Transparent))
				}
			}
		}
		content()
		if o.Loading {
			// The spinner is drawn over the label, which keeps its room:
			// the button does not change size when an action starts.
			ui.Row(u.C).
				Absolute().Top(0).Bottom(0).Start(0).End(0).
				Justify(ui.Center).AlignItems(ui.Center).
				Children(func() {
					ui.Spinner(u.C).Size(m.iconSize, m.iconSize)
				})
		}
	})
	return btn
}

// IconButtonOptions configures an icon-only button.
type IconButtonOptions struct {
	Variant ButtonVariant
	Size    ButtonSize
	// Disabled and Loading behave as ButtonOptions.
	Disabled bool
	Loading  bool
	OnClick  func()
}

// IconButton builds a square button showing only an icon. label names it for
// assistive technology and doubles as its tooltip, as ui_plan.md §5 requires:
// an icon button without a name is unusable with a screen reader.
func (u *UI) IconButton(icon *icons.Icon, label string, o IconButtonOptions) ui.Element {
	return u.Button("", ButtonOptions{
		Variant:  o.Variant,
		Size:     o.Size,
		Icon:     icon,
		Label:    label,
		Tooltip:  label,
		Disabled: o.Disabled,
		Loading:  o.Loading,
		iconOnly: true,
		OnClick:  o.OnClick,
	})
}
