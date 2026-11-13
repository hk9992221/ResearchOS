package components_test

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/theme"
	"researchos/internal/ui/tokens"
)

// buttonBox returns where a button is on screen, from the accessibility tree.
func buttonBox(t *testing.T, tt *ui.Tester, name string) ui.Rect {
	t.Helper()
	info, ok := tt.AccessByRole(ui.RoleButton, name)
	if !ok {
		t.Fatalf("no button named %q; texts %q", name, tt.Texts())
	}
	return info.Bounds
}

func TestButtonVariantsUseTokens(t *testing.T) {
	tt, _ := tester(t, 420, 380, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PageBlockGap).Gap(tokens.Space5).Children(func() {
			u.Button("Primary", components.ButtonOptions{Variant: components.VariantPrimary})
			u.Button("Secondary", components.ButtonOptions{Variant: components.VariantSecondary})
			u.Button("Ghost", components.ButtonOptions{Variant: components.VariantGhost})
			u.Button("Danger", components.ButtonOptions{Variant: components.VariantDanger})
		})
	})
	img := tt.Image()
	p := tokens.Light()
	for _, c := range []struct {
		name string
		want tokens.Color
	}{
		{"Primary", p.AccentDefault},
		{"Secondary", p.BgSurface},
		{"Ghost", p.BgCanvas},
		{"Danger", p.StatusDanger},
	} {
		r := buttonBox(t, tt, c.name)
		wantColor(t, c.name, at(img, r.X+4, r.Y+r.H/2), c.want, 3)
	}
}

func TestButtonHoverAndPressColors(t *testing.T) {
	tt, _ := tester(t, 300, 200, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PageBlockGap).Children(func() {
			u.Button("Secondary", components.ButtonOptions{Variant: components.VariantSecondary})
		})
	})
	p := tokens.Light()
	r := buttonBox(t, tt, "Secondary")
	x, y := r.X+r.W/2, r.Y+r.H/2

	tt.Move(x, y)
	tt.Frame()
	wantColor(t, "hover", at(tt.Image(), r.X+4, y), p.BgHover, 3)

	tt.Press(x, y)
	tt.Frame()
	wantColor(t, "pressed", at(tt.Image(), r.X+4, y), p.BgSubtle, 3)
	tt.Release(x, y)
}

func TestButtonFocusRingIsVisible(t *testing.T) {
	tt, _ := tester(t, 300, 200, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PageBlockGap).Children(func() {
			u.Button("Focus me", components.ButtonOptions{Variant: components.VariantSecondary})
		})
	})
	r := buttonBox(t, tt, "Focus me")
	cy := r.Y + r.H/2
	before := at(tt.Image(), r.X-2, cy)

	tt.Key(0, ui.KeyTab)
	tt.Frame()
	after := at(tt.Image(), r.X-2, cy)
	if before == after {
		t.Fatalf("no visible focus ring: pixel at the button edge stayed %v", before)
	}
	wantColor(t, "focus ring", after, tokens.Light().AccentDefault, 24)
	info, ok := tt.AccessByRole(ui.RoleButton, "Focus me")
	if !ok || !info.Focused {
		t.Errorf("button is not focused: %+v", info)
	}
}

func TestButtonStates(t *testing.T) {
	var disabledClicks, loadingClicks int
	tt, _ := tester(t, 320, 260, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PageBlockGap).Gap(tokens.Space3).Children(func() {
			u.Button("Disabled", components.ButtonOptions{Disabled: true, OnClick: func() { disabledClicks++ }})
			u.Button("Loading", components.ButtonOptions{Loading: true, OnClick: func() { loadingClicks++ }})
		})
	})
	for _, name := range []string{"Disabled", "Loading"} {
		info, ok := tt.AccessByRole(ui.RoleButton, name)
		if !ok {
			t.Fatalf("no button %q", name)
		}
		if !info.Disabled {
			t.Errorf("%s button is not disabled", name)
		}
	}
	if info, _ := tt.AccessByRole(ui.RoleButton, "Loading"); !info.Busy {
		t.Error("loading button is not marked busy for assistive technology")
	}
	click(t, tt, "Disabled")
	click(t, tt, "Loading")
	if disabledClicks != 0 || loadingClicks != 0 {
		t.Errorf("disabled/loading clicks reached the action: %d, %d", disabledClicks, loadingClicks)
	}
}

// TestButtonLoadingKeepsItsSize checks the §8 rule that loading does not move
// the layout: the label keeps its room under the spinner.
func TestButtonLoadingKeepsItsSize(t *testing.T) {
	idle, _ := tester(t, 320, 160, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PageBlockGap).Children(func() {
			u.Button("Save workspace", components.ButtonOptions{})
		})
	})
	loading, _ := tester(t, 320, 160, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PageBlockGap).Children(func() {
			u.Button("Save workspace", components.ButtonOptions{Loading: true})
		})
	})
	ir := buttonBox(t, idle, "Save workspace")
	lr := buttonBox(t, loading, "Save workspace")
	if ir.W != lr.W || ir.H != lr.H {
		t.Errorf("loading button = %vx%v, idle = %vx%v: the layout moved", lr.W, lr.H, ir.W, ir.H)
	}
}

func TestButtonThemeSwitchReResolves(t *testing.T) {
	tt, p := tester(t, 320, 200, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PageBlockGap).Children(func() {
			u.Button("Primary", components.ButtonOptions{Variant: components.VariantPrimary})
		})
	})
	r := buttonBox(t, tt, "Primary")
	x, y := r.X+4, r.Y+r.H/2
	wantColor(t, "light primary", at(tt.Image(), x, y), tokens.Light().AccentDefault, 3)

	if err := p.SetMode(context.Background(), theme.Dark); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	wantColor(t, "dark primary", at(tt.Image(), x, y), tokens.Dark().AccentDefault, 3)
}

func TestIconButtonHasTooltip(t *testing.T) {
	tt, _ := tester(t, 320, 160, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PageBlockGap).Children(func() {
			u.IconButton(icons.Refresh, "Refresh", components.IconButtonOptions{})
		})
	})
	info, ok := tt.AccessByRole(ui.RoleButton, "Refresh")
	if !ok {
		t.Fatalf("icon button has no accessible name; texts %q", tt.Texts())
	}
	if d := info.Bounds.W - info.Bounds.H; d < -1 || d > 1 {
		t.Errorf("icon button is %vx%v, want a square", info.Bounds.W, info.Bounds.H)
	}
	tt.Move(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
	tt.Frame()
	if !tt.HasText("Refresh") {
		t.Errorf("no tooltip on the icon button; texts %q", tt.Texts())
	}
}
