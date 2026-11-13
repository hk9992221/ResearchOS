package mygo

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/theme"
	"researchos/internal/ui/tokens"
)

func TestStyleLight(t *testing.T) {
	p := tokens.Light()
	s := Style(tokens.LightTheme())
	checks := []struct {
		name string
		got  ui.Color
		want tokens.Color
	}{
		{"Background", s.Background, p.BgCanvas},
		{"Surface", s.Surface, p.BgSurface},
		{"SurfaceHover", s.SurfaceHover, p.BgHover},
		{"SurfacePressed", s.SurfacePressed, p.BgSubtle},
		{"Border", s.Border, p.BorderDefault},
		{"Text", s.Text, p.TextPrimary},
		{"TextMuted", s.TextMuted, p.TextSecondary},
		{"Accent", s.Accent, p.AccentDefault},
		{"AccentHover", s.AccentHover, p.AccentHover},
		{"Danger", s.Danger, p.StatusDanger},
		{"Warning", s.Warning, p.StatusWarning},
		{"Success", s.Success, p.StatusSuccess},
		{"Selection", s.Selection, p.AccentSubtle},
		{"Focus", s.Focus, p.AccentDefault},
	}
	for _, c := range checks {
		if c.got != mgColor(c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, mgColor(c.want))
		}
	}
	if s.Dark {
		t.Error("light style is dark")
	}
	if s.Radius != tokens.RadiusDefault {
		t.Errorf("Radius = %v, want %v", s.Radius, tokens.RadiusDefault)
	}
	if s.Spacing != tokens.Space1 {
		t.Errorf("Spacing = %v, want %v", s.Spacing, tokens.Space1)
	}
	if s.FontSize != tokens.Typography.Body.Size {
		t.Errorf("FontSize = %v, want %v", s.FontSize, tokens.Typography.Body.Size)
	}
	if s.Font != tokens.SystemFonts.UI() {
		t.Errorf("Font = %q, want %q", s.Font, tokens.SystemFonts.UI())
	}
	if s.AccentText != mgColor(tokens.Color{R: 0xFF, G: 0xFF, B: 0xFF, A: 255}) {
		t.Errorf("AccentText = %v, want white on the light accent", s.AccentText)
	}
}

func TestStyleDark(t *testing.T) {
	p := tokens.Dark()
	s := Style(tokens.DarkTheme())
	if !s.Dark {
		t.Error("dark style is light")
	}
	if s.Background != mgColor(p.BgCanvas) || s.Surface != mgColor(p.BgSurface) {
		t.Error("dark surfaces not mapped")
	}
	if s.Accent != mgColor(p.AccentDefault) || s.Selection != mgColor(p.AccentSubtle) {
		t.Error("dark accent not mapped")
	}
	// On a light-blue dark accent, near-black text is the readable choice.
	if s.AccentText != mgColor(tokens.Color{R: 0x11, G: 0x13, B: 0x18, A: 255}) {
		t.Errorf("AccentText = %v, want near-black on the dark accent", s.AccentText)
	}
}

// TestRuntimeSwitchKeepsNavigation proves AC-UI-02: switching theme takes
// effect on the next frame without reloading the app or losing navigation.
func TestRuntimeSwitchKeepsNavigation(t *testing.T) {
	p := theme.New()
	nav := ""
	var canvases []uint8
	view := func(c *ui.Context) {
		Apply(c, p)
		canvases = append(canvases, c.Theme().Background.R)
		ui.Sidebar(c, &nav, func() {
			ui.SidebarItem(c, "home", nil, "Home")
			ui.SidebarItem(c, "projects", nil, "Projects")
		}).Width(232)
		ui.Text(c, "Current page")
	}
	tt := ui.NewTester(view, 900, 600)
	if err := tt.Click("Projects"); err != nil {
		t.Fatalf("click: %v", err)
	}
	if nav != "projects" {
		t.Fatalf("nav = %q, want projects", nav)
	}
	if err := p.SetMode(context.Background(), theme.Dark); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if nav != "projects" {
		t.Errorf("theme switch lost navigation: %q", nav)
	}
	if got := canvases[len(canvases)-1]; got != tokens.Dark().BgCanvas.R {
		t.Errorf("background R = %#x after switching to dark, want %#x", got, tokens.Dark().BgCanvas.R)
	}
	if err := p.SetMode(context.Background(), theme.Light); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := canvases[len(canvases)-1]; got != tokens.Light().BgCanvas.R {
		t.Errorf("background R = %#x after switching to light, want %#x", got, tokens.Light().BgCanvas.R)
	}
	if nav != "projects" {
		t.Errorf("navigation after two switches = %q", nav)
	}
}

func TestReducedMotionReachesToolkit(t *testing.T) {
	p := theme.New()
	var dur []float32
	view := func(c *ui.Context) {
		Apply(c, p)
		dur = append(dur, float32(p.Theme().Motion.Scale(tokens.PanelDuration)))
		ui.Text(c, "frame")
	}
	tt := ui.NewTesterWith(view, 400, 300, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	if len(dur) == 0 || dur[len(dur)-1] != 0 {
		t.Fatalf("reduced motion not applied: %v", dur)
	}
	if p.Theme().Motion.Scale(tokens.PanelDuration) != 0 {
		t.Error("provider did not record reduced motion")
	}
	tt.SetPreferences(ui.Preferences{})
	tt.Frame()
	if p.Theme().Motion.Scale(tokens.PanelDuration) != tokens.PanelDuration {
		t.Error("provider did not clear reduced motion")
	}
}
