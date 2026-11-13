package components_test

import (
	"image"
	"testing"

	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/components"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/theme"
	"researchos/internal/ui/tokens"
)

// tester builds a headless window whose frame applies the provider's theme
// (as the app's root view does) and then runs the test's view.
func tester(t *testing.T, w, h int, view func(u *components.UI)) (*ui.Tester, *theme.Provider) {
	t.Helper()
	p := theme.New()
	tt := ui.NewTester(func(c *ui.Context) {
		mygo.Apply(c, p)
		view(components.New(c, p.Theme()))
	}, w, h)
	return tt, p
}

// bounds returns the visible box of the accessible element named name, which
// is where a component really is on screen.
func bounds(t *testing.T, tt *ui.Tester, name string) ui.Rect {
	t.Helper()
	info, ok := tt.Access(name)
	if !ok {
		t.Fatalf("no accessible element named %q; texts %q", name, tt.Texts())
	}
	return info.Bounds
}

func at(img *image.RGBA, x, y float32) [4]uint8 {
	i := img.PixOffset(int(x), int(y))
	return [4]uint8{img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]}
}

// wantColor fails unless got is want within tol per channel.
func wantColor(t *testing.T, label string, got [4]uint8, want tokens.Color, tol uint8) {
	t.Helper()
	diff := func(a, b uint8) int {
		if a > b {
			return int(a - b)
		}
		return int(b - a)
	}
	if diff(got[0], want.R) > int(tol) || diff(got[1], want.G) > int(tol) || diff(got[2], want.B) > int(tol) {
		t.Errorf("%s = rgb(%d,%d,%d), want %s", label, got[0], got[1], got[2], want)
	}
}

// click hits the center of the accessible element named name.
func click(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	r := bounds(t, tt, name)
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
}
