// Package mygo adapts ResearchOS design tokens to the MyGo native UI toolkit
// (github.com/egoist/mygo/ui). It is the only UI package that imports the
// toolkit's types for styling: everything above it takes tokens.
//
// The mapping is deliberately thin. Tokens the toolkit's theme has no field
// for (bg.subtle is SurfacePressed, text.muted, border.strong, status.info
// and accent.subtle remain available to components) keep their semantic name
// in tokens and are applied per element by the component layer.
package mygo

import (
	mg "github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/theme"
	"researchos/internal/ui/tokens"
)

// Style maps a resolved token theme onto MyGo's theme.
func Style(th tokens.Theme) *ui.Theme {
	p := th.Palette
	out := &ui.Theme{
		Dark: th.Dark,

		Background:     mgColor(p.BgCanvas),
		Surface:        mgColor(p.BgSurface),
		SurfaceHover:   mgColor(p.BgHover),
		SurfacePressed: mgColor(p.BgSubtle),
		Border:         mgColor(p.BorderDefault),

		Text:      mgColor(p.TextPrimary),
		TextMuted: mgColor(p.TextSecondary),

		Accent:        mgColor(p.AccentDefault),
		AccentHover:   mgColor(p.AccentHover),
		AccentPressed: mgColor(p.AccentHover), // the design system has no pressed step
		AccentText:    mgColor(p.AccentDefault.ReadableText()),

		Danger:  mgColor(p.StatusDanger),
		Warning: mgColor(p.StatusWarning),
		Success: mgColor(p.StatusSuccess),

		Selection: mgColor(p.AccentSubtle),
		Focus:     mgColor(p.AccentDefault),

		Radius:   tokens.RadiusDefault,
		Spacing:  tokens.Space1,
		FontSize: th.Type.Body.Size,
		Font:     th.Fonts.UI(),
	}
	// Inverse and InverseText stay zero: MyGo then derives tooltips and
	// toasts from Text and Background, which keeps them legible in both
	// appearances without inventing colors the design system does not define.
	return out
}

// mgColor converts a token color to the toolkit's color type.
func mgColor(c tokens.Color) ui.Color {
	return ui.RGBA(c.R, c.G, c.B, float32(c.A)/255)
}

// Apply installs the provider's current theme for this frame and mirrors the
// desktop's reduced-motion preference into it. Call it at the top of the root
// view: a mode change made anywhere, by a click or a background task, takes
// effect on the next frame, without recreating the window, the router or any
// page state.
func Apply(c *ui.Context, p *theme.Provider) {
	if c == nil || p == nil {
		return
	}
	p.SetReduceMotion(c.Preferences().ReduceMotion)
	c.SetTheme(Style(p.Theme()))
}

// SyncSystem records the desktop's current appearance and reduced-motion
// setting. Call it from the app's ready callback; MyGo panics when its theme
// is read on the main goroutine before the app runs.
func SyncSystem(p *theme.Provider) {
	if p == nil {
		return
	}
	p.SetSystemDark(mg.Theme.IsDark())
	p.SetReduceMotion(mg.Theme.Preferences().ReduceMotion)
}

// WatchSystem follows the desktop's appearance while the app runs, so System
// mode switches with the OS, and asks the windows to repaint through
// invalidate, which must be safe from any goroutine (Window.Invalidate is).
// It returns a function that stops watching. Call it after the app is ready.
func WatchSystem(p *theme.Provider, invalidate func()) (stop func()) {
	if p == nil {
		return func() {}
	}
	SyncSystem(p)
	return mg.Theme.OnUpdated(func() {
		SyncSystem(p)
		if invalidate != nil {
			invalidate()
		}
	})
}

// InvalidateAll returns an invalidate function that repaints every window of
// the app. Main and background goroutines may call it: MyGo marshals window
// calls to the main thread.
func InvalidateAll() func() {
	return func() {
		for _, w := range mg.Windows() {
			w.Invalidate()
		}
	}
}
