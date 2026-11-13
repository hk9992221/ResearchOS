// Package tokens defines the ResearchOS design tokens: the semantic colors,
// type scale, font stacks, spacing, radii and motion of the UI Design System
// (docs/ui_plan.md, Parts A and B).
//
// Tokens are toolkit-agnostic on purpose. Business code never hardcodes a
// color: it reads the resolved tokens through internal/ui/theme and passes
// them to components, and the MyGo adapter maps them onto the toolkit's
// theme.
package tokens

import (
	"fmt"
	"math"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Color is a non-premultiplied 8-bit sRGB color.
type Color struct {
	R, G, B, A uint8
}

// Transparent is the zero color.
var Transparent = Color{}

// Hex parses #RRGGBB or #RRGGBBAA into a Color. It is meant for the constant
// token tables below: an invalid literal is a programming error, so it panics.
func Hex(s string) Color {
	c, err := ParseHex(s)
	if err != nil {
		panic(err)
	}
	return c
}

// ParseHex parses #RRGGBB or #RRGGBBAA into a Color.
func ParseHex(s string) (Color, error) {
	h := strings.TrimPrefix(s, "#")
	if len(h) != 6 && len(h) != 8 {
		return Color{}, fmt.Errorf("tokens: color %q is not #RRGGBB or #RRGGBBAA", s)
	}
	var v [4]uint64
	for i := range len(h) / 2 {
		n, err := strconv.ParseUint(h[2*i:2*i+2], 16, 8)
		if err != nil {
			return Color{}, fmt.Errorf("tokens: color %q: %w", s, err)
		}
		v[i] = n
	}
	c := Color{R: uint8(v[0]), G: uint8(v[1]), B: uint8(v[2]), A: 255}
	if len(h) == 8 {
		c.A = uint8(v[3])
	}
	return c, nil
}

// String returns the color as #RRGGBB, or #RRGGBBAA when it is not opaque.
func (c Color) String() string {
	if c.A == 255 {
		return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
	}
	return fmt.Sprintf("#%02X%02X%02X%02X", c.R, c.G, c.B, c.A)
}

// WithAlpha returns the color at the given opacity, 0 to 1.
func (c Color) WithAlpha(a float32) Color {
	c.A = uint8(math.Round(float64(clamp01(a)) * 255))
	return c
}

// Over composites c over an opaque background bg, as translucent surfaces are
// painted over their parent.
func (c Color) Over(bg Color) Color {
	a := float64(c.A) / 255
	mix := func(f, b uint8) uint8 {
		return uint8(math.Round(float64(f)*a + float64(b)*(1-a)))
	}
	return Color{R: mix(c.R, bg.R), G: mix(c.G, bg.G), B: mix(c.B, bg.B), A: 255}
}

// Mix blends c toward o by t, 0 keeping c and 1 returning o, in sRGB
// space: it derives hover and tint shades no design token names.
func (c Color) Mix(o Color, t float32) Color {
	t = clamp01(t)
	mix := func(a, b uint8) uint8 {
		return uint8(math.Round(float64(a)*(1-float64(t)) + float64(b)*float64(t)))
	}
	return Color{R: mix(c.R, o.R), G: mix(c.G, o.G), B: mix(c.B, o.B), A: mix(c.A, o.A)}
}

// Luminance returns the WCAG 2.x relative luminance of the color's RGB.
func (c Color) Luminance() float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// Contrast returns the WCAG contrast ratio between two colors, 1 to 21.
func (c Color) Contrast(b Color) float64 {
	l1, l2 := c.Luminance(), b.Luminance()
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

// ReadableText returns the color of black or white text with the higher
// contrast on c, for text drawn on an accent or status fill.
func (c Color) ReadableText() Color {
	black := Color{R: 0x11, G: 0x13, B: 0x18, A: 255}
	white := Color{R: 0xFF, G: 0xFF, B: 0xFF, A: 255}
	if black.Contrast(c) >= white.Contrast(c) {
		return black
	}
	return white
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Palette is the semantic color set of one appearance. Field names carry the
// token paths of the design system: BgCanvas is bg.canvas, AccentDefault is
// accent.default, and so on.
type Palette struct {
	BgCanvas  Color // bg.canvas: the application background
	BgSurface Color // bg.surface: content panels, cards, inputs
	BgSubtle  Color // bg.subtle: secondary background, pressed fills
	BgHover   Color // bg.hover: hovered rows and list items

	BorderDefault Color // border.default: the usual separator and outline
	BorderStrong  Color // border.strong: emphasized outlines, focused panels

	TextPrimary   Color // text.primary: body text and headings
	TextSecondary Color // text.secondary: supporting information
	TextMuted     Color // text.muted: timestamps, placeholders, disabled text

	AccentDefault Color // accent.default: primary actions, selection, links
	AccentHover   Color // accent.hover: hovered accent
	AccentSubtle  Color // accent.subtle: selected-row and badge background

	StatusSuccess Color // status.success
	StatusWarning Color // status.warning
	StatusDanger  Color // status.danger: errors and destructive actions
	StatusInfo    Color // status.info
}

// Light is the Light theme palette of ui_plan.md §2.1.
func Light() Palette {
	return Palette{
		BgCanvas:      Hex("#F7F8FA"),
		BgSurface:     Hex("#FFFFFF"),
		BgSubtle:      Hex("#F1F3F6"),
		BgHover:       Hex("#F2F4F7"),
		BorderDefault: Hex("#E4E7EC"),
		BorderStrong:  Hex("#D0D5DD"),
		TextPrimary:   Hex("#252A34"),
		TextSecondary: Hex("#667085"),
		TextMuted:     Hex("#98A2B3"),
		AccentDefault: Hex("#4263EB"),
		AccentHover:   Hex("#3451D1"),
		AccentSubtle:  Hex("#EEF2FF"),
		StatusSuccess: Hex("#15803D"),
		StatusWarning: Hex("#B54708"),
		StatusDanger:  Hex("#B42318"),
		StatusInfo:    Hex("#175CD3"),
	}
}

// Dark is the Dark theme palette of ui_plan.md §2.2.
func Dark() Palette {
	return Palette{
		BgCanvas:      Hex("#111318"),
		BgSurface:     Hex("#181B22"),
		BgSubtle:      Hex("#20242D"),
		BgHover:       Hex("#262B36"),
		BorderDefault: Hex("#303642"),
		BorderStrong:  Hex("#414958"),
		TextPrimary:   Hex("#E8ECF3"),
		TextSecondary: Hex("#A4ADBC"),
		TextMuted:     Hex("#7C8798"),
		AccentDefault: Hex("#8EA2FF"),
		AccentHover:   Hex("#A7B5FF"),
		AccentSubtle:  Hex("#242E56"),
		StatusSuccess: Hex("#56D68B"),
		StatusWarning: Hex("#FDBA74"),
		StatusDanger:  Hex("#FDA29B"),
		StatusInfo:    Hex("#84CAFF"),
	}
}

// Weight is a font weight, as OpenType weights run from 100 to 900.
type Weight int

const (
	WeightRegular  Weight = 400
	WeightMedium   Weight = 500
	WeightSemibold Weight = 600
)

// TextStyle is one role of the type scale: its size in px, its weight, and
// whether it is set in tabular figures or the monospaced family.
type TextStyle struct {
	Size    float32
	Weight  Weight
	Tabular bool
	Mono    bool
}

// TypeScale is the type hierarchy of ui_plan.md §3.2.
type TypeScale struct {
	PageTitle    TextStyle // 24/600: page title
	SectionTitle TextStyle // 18/600: section title
	CardTitle    TextStyle // 16/600: card title
	Body         TextStyle // 14/400: body, table cells, default text
	Secondary    TextStyle // 13/400: supporting information
	Caption      TextStyle // 12/400: timestamps, hints, tags
	Numeric      TextStyle // 14/500 tabular: metrics and numbers
}

// Typography is the default type scale.
var Typography = TypeScale{
	PageTitle:    TextStyle{Size: 24, Weight: WeightSemibold},
	SectionTitle: TextStyle{Size: 18, Weight: WeightSemibold},
	CardTitle:    TextStyle{Size: 16, Weight: WeightSemibold},
	Body:         TextStyle{Size: 14, Weight: WeightRegular},
	Secondary:    TextStyle{Size: 13, Weight: WeightRegular},
	Caption:      TextStyle{Size: 12, Weight: WeightRegular},
	Numeric:      TextStyle{Size: 14, Weight: WeightMedium, Tabular: true},
}

// Fonts are the font family lists of ui_plan.md §3.1, as toolkit font stacks:
// each list names the preferred family first and the families that stand in
// for it, so an unavailable font falls through instead of breaking the UI.
type Fonts struct {
	// Sans is the interface font: Inter, then the system sans-serif.
	Sans string
	// CJK is the CJK face for the platform.
	CJK string
	// Mono is the code face: JetBrains Mono, then Cascadia Code or Consolas.
	Mono string
}

// FontsFor returns the font stacks that suit the given GOOS.
func FontsFor(goos string) Fonts {
	cjk := "Noto Sans CJK SC"
	switch goos {
	case "windows":
		cjk = "Microsoft YaHei UI, Microsoft YaHei, Noto Sans CJK SC"
	case "darwin":
		cjk = "PingFang SC, Noto Sans CJK SC"
	}
	return Fonts{
		Sans: "Inter, Segoe UI, Helvetica Neue, Arial",
		CJK:  cjk,
		Mono: "JetBrains Mono, Cascadia Code, Consolas, monospace",
	}
}

// SystemFonts is the font stack of the machine the program runs on.
var SystemFonts = FontsFor(runtime.GOOS)

// UI returns the full interface stack: the Latin faces first, then the CJK
// faces, as MyGo and browsers pick the first family each glyph needs.
func (f Fonts) UI() string { return joinFamilies(f.Sans, f.CJK) }

// Text returns the stack for a text style: the monospaced family for code,
// the interface stack otherwise.
func (f Fonts) Text(s TextStyle) string {
	if s.Mono {
		return f.Mono
	}
	return f.UI()
}

func joinFamilies(lists ...string) string {
	var parts []string
	for _, l := range lists {
		for _, p := range strings.Split(l, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
	}
	return strings.Join(parts, ", ")
}

// Spacing is the 4px scale of ui_plan.md §4.1: space.N is N×4. §4.1 names
// the steps the design system uses; the full series exists so a layout never
// needs an off-grid number.
const (
	Space1  float32 = 4
	Space2  float32 = 8
	Space3  float32 = 12
	Space4  float32 = 16
	Space5  float32 = 20
	Space6  float32 = 24
	Space7  float32 = 28
	Space8  float32 = 32
	Space9  float32 = 36
	Space10 float32 = 40
	Space11 float32 = 44
	Space12 float32 = 48
)

// Layout defaults of ui_plan.md §4.1: the gaps the design system prescribes.
const (
	PagePadding  = Space6 // page margin: 24
	PanelPadding = Space4 // content panel padding: 16
	ComponentGap = Space2 // gap between controls: 8
	RelatedGap   = Space3 // gap between related groups: 12
	SectionGap   = Space6 // gap between content blocks: 24
	PageBlockGap = Space8 // gap between page regions: 32
)

// Radii of ui_plan.md §4.2.
const (
	RadiusSM      float32 = 4  // small tags
	RadiusMD      float32 = 6  // inputs, buttons
	RadiusLG      float32 = 8  // panels and cards
	RadiusXL      float32 = 12 // dialogs and larger overlays
	RadiusDefault         = RadiusLG
)

// Motion durations of ui_plan.md §9.2.
const (
	HoverDuration      = 120 * time.Millisecond
	TransitionDuration = 150 * time.Millisecond
	PanelDuration      = 180 * time.Millisecond
)

// Motion is the motion policy of the active appearance. Reduced asks to
// respect the desktop's reduced-motion setting: transitions then happen at
// once, and only indeterminate activity indicators keep moving.
type Motion struct {
	Reduced bool
}

// Scale returns d, or 0 when reduced motion is asked for.
func (m Motion) Scale(d time.Duration) time.Duration {
	if m.Reduced {
		return 0
	}
	return d
}

// Overlay neutrals. The design system defines no scrim or shadow colors, so
// these are derived from bg.canvas and pure black rather than invented hues:
// they keep dialogs and popovers legible in both appearances.
var (
	// Scrim dims the window behind a modal dialog: bg.canvas at 40%.
	Scrim = Color{R: 0x11, G: 0x13, B: 0x18, A: 102}
	// Shadow is the light drop shadow under dialogs and popovers: black at
	// 18%.
	Shadow = Color{R: 0x00, G: 0x00, B: 0x00, A: 46}
)

// Theme is a fully resolved appearance: the palette, type scale, font stacks
// and motion policy the UI paints with.
type Theme struct {
	Dark    bool
	Palette Palette
	Type    TypeScale
	Fonts   Fonts
	Motion  Motion
}

// LightTheme returns the Light appearance with the default type and fonts.
func LightTheme() Theme {
	return Theme{Palette: Light(), Type: Typography, Fonts: SystemFonts}
}

// DarkTheme returns the Dark appearance with the default type and fonts.
func DarkTheme() Theme {
	return Theme{Palette: Dark(), Type: Typography, Fonts: SystemFonts, Dark: true}
}

// WithMotion returns the theme with the given motion policy.
func (t Theme) WithMotion(m Motion) Theme {
	t.Motion = m
	return t
}

// Status returns the semantic color of a status kind: success, warning,
// danger or info. Status text must always carry a label or icon too, never
// color alone.
func (p Palette) Status(kind string) Color {
	switch kind {
	case "success":
		return p.StatusSuccess
	case "warning":
		return p.StatusWarning
	case "danger", "error":
		return p.StatusDanger
	case "info":
		return p.StatusInfo
	}
	return p.TextSecondary
}
