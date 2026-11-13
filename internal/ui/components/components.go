// Package components is the common component layer of the ResearchOS UI: the
// buttons, inputs, tables, dialogs and layout primitives every module builds
// its pages from (ui_plan.md §7).
//
// A UI carries the frame's toolkit context and the resolved design tokens, so
// components read colors, type and spacing from tokens and never hardcode
// them. Components are built on MyGo's headless bases where one exists: the
// base owns pointer, keyboard, focus, disabled and accessibility behavior,
// the component owns its look. Themes are re-resolved every frame, so Light,
// Dark and System switching needs no rebuild (AC-UI-04, AC-UI-02).
//
// A page builds one UI per frame:
//
//	u := components.New(c, provider.Theme())
//	if u.Button("New project", components.ButtonOptions{Variant: components.Primary}).Clicked() {
//		app.create()
//	}
package components

import (
	"context"

	"github.com/egoist/mygo/ui"

	"researchos/internal/i18n"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// UI is one frame's context and resolved tokens. Build it at the top of the
// view, after mygo.Apply installed the toolkit theme.
type UI struct {
	C *ui.Context
	T tokens.Theme
	// Ctx is the context long-running service calls made by a component's
	// callbacks use, such as issuing a confirmation token. Nil means
	// context.Background.
	Ctx context.Context
}

// New returns the component context of a frame.
func New(c *ui.Context, t tokens.Theme) *UI {
	return &UI{C: c, T: t}
}

// Context returns UI.Ctx, or context.Background when it is nil.
func (u *UI) Context() context.Context {
	if u.Ctx != nil {
		return u.Ctx
	}
	return context.Background()
}

// Palette returns the frame's semantic colors.
func (u *UI) Palette() tokens.Palette { return u.T.Palette }

// text returns body text in the frame's type scale.
func (u *UI) text(s string) ui.Element {
	return ui.Text(u.C, s).FontSize(u.T.Type.Body.Size).FontWeight(int(u.T.Type.Body.Weight))
}

// caption returns small supporting text, muted as the design system asks.
func (u *UI) caption(s string) ui.Element {
	return ui.Text(u.C, s).FontSize(u.T.Type.Caption.Size).TextColor(mygo.Color(u.T.Palette.TextSecondary))
}

// Icon returns a registry icon at the design system's size scale, in the text
// color inherited from its parent. Zero size means the default 16px.
func (u *UI) Icon(i *icons.Icon, size float32) ui.Element {
	if i == nil {
		return ui.Element{}
	}
	if size <= 0 {
		size = icons.SizeDefault
	}
	return ui.Icon(u.C, i.SVG()).FontSize(size)
}

// Tone is the semantic meaning of a badge, banner or status.
type Tone int

const (
	// Neutral is supporting information without a status.
	Neutral Tone = iota
	// Accent draws attention to a new or active item.
	Accent
	// Success reports a completed or healthy state.
	Success
	// Warning reports something that needs attention but is not broken.
	Warning
	// Danger reports an error or a destructive state.
	Danger
	// Info reports neutral system information.
	Info
)

// foreground is the text and icon color of the tone.
func (u *UI) foreground(t Tone) tokens.Color {
	p := u.T.Palette
	switch t {
	case Success:
		return p.StatusSuccess
	case Warning:
		return p.StatusWarning
	case Danger:
		return p.StatusDanger
	case Info:
		return p.StatusInfo
	case Accent:
		return p.AccentDefault
	}
	return p.TextSecondary
}

// background is a subtle fill of the tone, for badges and banners: the
// surface tinted toward the tone, never the tone itself — dark text on the
// full-strength warning or danger color is unreadable.
func (u *UI) background(t Tone) tokens.Color {
	p := u.T.Palette
	switch t {
	case Success, Warning, Danger, Info:
		return p.BgSurface.Mix(u.foreground(t), 0.12)
	case Accent:
		return p.AccentSubtle
	}
	return p.BgSubtle
}

// label returns the text the tone stands for, so a status is never carried by
// color alone (ui_plan.md §2.3).
func label(t Tone) string {
	switch t {
	case Success:
		return i18n.T("components.tone.success")
	case Warning:
		return i18n.T("components.tone.warning")
	case Danger:
		return i18n.T("components.tone.danger")
	case Info:
		return i18n.T("components.tone.info")
	case Accent:
		return i18n.T("components.tone.accent")
	}
	return i18n.T("components.tone.info")
}
