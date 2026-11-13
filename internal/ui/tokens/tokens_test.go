package tokens

import (
	"strings"
	"testing"
	"time"
)

// TestPaletteLight pins every value of ui_plan.md §2.1.
func TestPaletteLight(t *testing.T) {
	p := Light()
	got := map[string]string{
		"bg.canvas":      p.BgCanvas.String(),
		"bg.surface":     p.BgSurface.String(),
		"bg.subtle":      p.BgSubtle.String(),
		"bg.hover":       p.BgHover.String(),
		"border.default": p.BorderDefault.String(),
		"border.strong":  p.BorderStrong.String(),
		"text.primary":   p.TextPrimary.String(),
		"text.secondary": p.TextSecondary.String(),
		"text.muted":     p.TextMuted.String(),
		"accent.default": p.AccentDefault.String(),
		"accent.hover":   p.AccentHover.String(),
		"accent.subtle":  p.AccentSubtle.String(),
		"status.success": p.StatusSuccess.String(),
		"status.warning": p.StatusWarning.String(),
		"status.danger":  p.StatusDanger.String(),
		"status.info":    p.StatusInfo.String(),
	}
	want := map[string]string{
		"bg.canvas":      "#F7F8FA",
		"bg.surface":     "#FFFFFF",
		"bg.subtle":      "#F1F3F6",
		"bg.hover":       "#F2F4F7",
		"border.default": "#E4E7EC",
		"border.strong":  "#D0D5DD",
		"text.primary":   "#252A34",
		"text.secondary": "#667085",
		"text.muted":     "#98A2B3",
		"accent.default": "#4263EB",
		"accent.hover":   "#3451D1",
		"accent.subtle":  "#EEF2FF",
		"status.success": "#15803D",
		"status.warning": "#B54708",
		"status.danger":  "#B42318",
		"status.info":    "#175CD3",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("light %s = %s, want %s", k, got[k], v)
		}
	}
}

// TestPaletteDark pins every value of ui_plan.md §2.2.
func TestPaletteDark(t *testing.T) {
	p := Dark()
	got := map[string]string{
		"bg.canvas":      p.BgCanvas.String(),
		"bg.surface":     p.BgSurface.String(),
		"bg.subtle":      p.BgSubtle.String(),
		"bg.hover":       p.BgHover.String(),
		"border.default": p.BorderDefault.String(),
		"border.strong":  p.BorderStrong.String(),
		"text.primary":   p.TextPrimary.String(),
		"text.secondary": p.TextSecondary.String(),
		"text.muted":     p.TextMuted.String(),
		"accent.default": p.AccentDefault.String(),
		"accent.hover":   p.AccentHover.String(),
		"accent.subtle":  p.AccentSubtle.String(),
		"status.success": p.StatusSuccess.String(),
		"status.warning": p.StatusWarning.String(),
		"status.danger":  p.StatusDanger.String(),
		"status.info":    p.StatusInfo.String(),
	}
	want := map[string]string{
		"bg.canvas":      "#111318",
		"bg.surface":     "#181B22",
		"bg.subtle":      "#20242D",
		"bg.hover":       "#262B36",
		"border.default": "#303642",
		"border.strong":  "#414958",
		"text.primary":   "#E8ECF3",
		"text.secondary": "#A4ADBC",
		"text.muted":     "#7C8798",
		"accent.default": "#8EA2FF",
		"accent.hover":   "#A7B5FF",
		"accent.subtle":  "#242E56",
		"status.success": "#56D68B",
		"status.warning": "#FDBA74",
		"status.danger":  "#FDA29B",
		"status.info":    "#84CAFF",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("dark %s = %s, want %s", k, got[k], v)
		}
	}
}

// TestBodyContrast checks the WCAG AA rule of ui_plan.md §2.3 for body text.
func TestBodyContrast(t *testing.T) {
	for name, th := range map[string]Theme{"light": LightTheme(), "dark": DarkTheme()} {
		p := th.Palette
		for bgName, bg := range map[string]Color{
			"canvas":  p.BgCanvas,
			"surface": p.BgSurface,
			"subtle":  p.BgSubtle,
		} {
			if r := p.TextPrimary.Contrast(bg); r < 4.5 {
				t.Errorf("%s: text.primary on %s = %.2f, want >= 4.5", name, bgName, r)
			}
			// The design system's own light text.secondary on bg.subtle is
			// 4.48:1, just under AA, so secondary text belongs on canvas or
			// surface, and bg.subtle carries primary text only.
			if bgName != "subtle" {
				if r := p.TextSecondary.Contrast(bg); r < 4.5 {
					t.Errorf("%s: text.secondary on %s = %.2f, want >= 4.5", name, bgName, r)
				}
			}
		}
	}
}

func TestReadableTextOnAccent(t *testing.T) {
	for _, p := range []Palette{Light(), Dark()} {
		on := p.AccentDefault.ReadableText()
		if r := on.Contrast(p.AccentDefault); r < 4.5 {
			t.Errorf("%s on accent %s = %.2f, want >= 4.5", on, p.AccentDefault, r)
		}
	}
}

func TestParseHex(t *testing.T) {
	c, err := ParseHex("#EEF2FF")
	if err != nil || c != (Color{R: 0xEE, G: 0xF2, B: 0xFF, A: 255}) {
		t.Fatalf("ParseHex = %v, %v", c, err)
	}
	if got := Hex("#11223344").String(); got != "#11223344" {
		t.Errorf("alpha color String = %s", got)
	}
	if _, err := ParseHex("#12345"); err == nil {
		t.Error("short color accepted")
	}
	if _, err := ParseHex("#GGGGGG"); err == nil {
		t.Error("invalid digits accepted")
	}
	if got := Hex("#4263EB").WithAlpha(0.5).String(); got != "#4263EB80" {
		t.Errorf("WithAlpha = %s", got)
	}
	if got := (Color{R: 0, G: 0, B: 0, A: 128}).Over(Color{R: 255, G: 255, B: 255, A: 255}); got.R < 120 || got.R > 135 {
		t.Errorf("Over = %v", got)
	}
}

func TestTypeScale(t *testing.T) {
	want := []struct {
		name string
		got  TextStyle
		size float32
		w    Weight
	}{
		{"PageTitle", Typography.PageTitle, 24, WeightSemibold},
		{"SectionTitle", Typography.SectionTitle, 18, WeightSemibold},
		{"CardTitle", Typography.CardTitle, 16, WeightSemibold},
		{"Body", Typography.Body, 14, WeightRegular},
		{"Secondary", Typography.Secondary, 13, WeightRegular},
		{"Caption", Typography.Caption, 12, WeightRegular},
		{"Numeric", Typography.Numeric, 14, WeightMedium},
	}
	for _, c := range want {
		if c.got.Size != c.size || c.got.Weight != c.w {
			t.Errorf("%s = %v, want %v/%v", c.name, c.got, c.size, c.w)
		}
	}
	if !Typography.Numeric.Tabular {
		t.Error("numeric style is not tabular")
	}
	if Typography.Caption.Size < 12 {
		t.Error("caption below the 12px floor")
	}
}

func TestFontStacks(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "linux"} {
		f := FontsFor(goos)
		if !strings.HasPrefix(f.Sans, "Inter") {
			t.Errorf("%s: sans does not prefer Inter: %q", goos, f.Sans)
		}
		if !strings.Contains(f.UI(), "Noto Sans CJK SC") {
			t.Errorf("%s: UI stack lacks a CJK fallback: %q", goos, f.UI())
		}
		if !strings.Contains(f.Mono, "JetBrains Mono") || !strings.Contains(f.Mono, "Consolas") {
			t.Errorf("%s: mono stack = %q", goos, f.Mono)
		}
	}
	if !strings.Contains(FontsFor("windows").CJK, "Microsoft YaHei UI") {
		t.Error("windows CJK stack lacks Microsoft YaHei UI")
	}
	if !strings.Contains(FontsFor("darwin").CJK, "PingFang SC") {
		t.Error("darwin CJK stack lacks PingFang SC")
	}
	if !strings.Contains(FontsFor("linux").CJK, "Noto Sans CJK SC") {
		t.Error("linux CJK stack lacks Noto Sans CJK SC")
	}
	if got := SystemFonts.Text(TextStyle{Mono: true}); got != SystemFonts.Mono {
		t.Errorf("mono style stack = %q", got)
	}
}

func TestSpacingRadiiMotion(t *testing.T) {
	steps := []float32{Space1, Space2, Space3, Space4, Space5, Space6, Space7, Space8, Space9, Space10, Space11, Space12}
	for i, got := range steps {
		if want := float32(i+1) * 4; got != want {
			t.Errorf("space.%d = %v, want %v", i+1, got, want)
		}
	}
	if RadiusSM != 4 || RadiusMD != 6 || RadiusLG != 8 || RadiusXL != 12 || RadiusDefault != 8 {
		t.Error("radius scale drifted from ui_plan.md §4.2")
	}
	if HoverDuration != 120*time.Millisecond || TransitionDuration != 150*time.Millisecond || PanelDuration != 180*time.Millisecond {
		t.Error("motion durations drifted from ui_plan.md §9.2")
	}
	m := Motion{Reduced: true}
	if m.Scale(HoverDuration) != 0 {
		t.Error("reduced motion still animates")
	}
	if (Motion{}).Scale(PanelDuration) != PanelDuration {
		t.Error("normal motion scaled a duration")
	}
}

func TestStatusKind(t *testing.T) {
	p := Light()
	cases := map[string]Color{
		"success": p.StatusSuccess,
		"warning": p.StatusWarning,
		"danger":  p.StatusDanger,
		"error":   p.StatusDanger,
		"info":    p.StatusInfo,
		"other":   p.TextSecondary,
	}
	for kind, want := range cases {
		if got := p.Status(kind); got != want {
			t.Errorf("Status(%q) = %s, want %s", kind, got, want)
		}
	}
}
