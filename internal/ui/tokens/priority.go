package tokens

// The task board's priorities (SPEC-08 §4.3). A priority is not a status: the
// board's red, green and blue must not be the ones a failure, a warning or a
// link carries, so they get steps of their own.
//
// Each appearance resolves its own steps. The Light ones are darker and more
// saturated so a dot or a card's 3 DIP bar still reads on a white card; the
// Dark ones keep the pastel tints that read on the dark surface. Both keep
// every step above 4.5:1 against the card they sit on.
const (
	PriorityHigh   = "high"
	PriorityMedium = "medium"
	PriorityLow    = "low"
)

var (
	// priorityLight: #C11515, #197B0A and #1D4ED8, at 6.20:1, 5.41:1 and
	// 6.70:1 against White.
	//
	// The hues are 0°, 112° and 224°, so the nearest pair is 112° apart — the
	// widest a fixed red and blue allow. Red and blue are unmoved; the green
	// sits at the midpoint of the larger arc between them, which is what
	// maximises the smallest of the three pairwise gaps. Amber sat 26° from
	// the red and read as the same hue at dot size.
	priorityLightHigh   = Hex("#C11515") // H 0°,  S 80%, L 42%
	priorityLightMedium = Hex("#197B0A") // H 112°, S 85%, L 26%
	priorityLightLow    = Hex("#1D4ED8") // H 224°, S 76%, L 48%
	// priorityDark: #FDA29B, #A9F391 and #84CAFF, at 8.88:1, 13.06:1 and
	// 9.74:1 against the dark surface.
	//
	// The same hues, as pastels: 4°, 105° and 206°, nearest pair 101°. Green
	// is the brightest hue at equal lightness, so it carries more contrast
	// than its neighbours; it is kept at their lightness (76% against 80% and
	// 76%) so the three dots weigh the same on the card.
	priorityDarkHigh   = Hex("#FDA29B") // H 4°,   S 96%, L 80%
	priorityDarkMedium = Hex("#A9F391") // H 105°, S 80%, L 76%
	priorityDarkLow    = Hex("#84CAFF") // H 206°, S 100%, L 76%
)

// Priority returns the color of the priority kind — PriorityHigh,
// PriorityMedium or PriorityLow — in the theme's appearance. An unknown kind
// is the secondary text color, as Palette.Status does for an unknown status.
//
// A priority is never color alone: every place that paints one also names it
// (ui_plan.md §2.3).
func (t Theme) Priority(kind string) Color {
	if t.Dark {
		switch kind {
		case PriorityHigh:
			return priorityDarkHigh
		case PriorityMedium:
			return priorityDarkMedium
		case PriorityLow:
			return priorityDarkLow
		}
		return t.Palette.TextSecondary
	}
	switch kind {
	case PriorityHigh:
		return priorityLightHigh
	case PriorityMedium:
		return priorityLightMedium
	case PriorityLow:
		return priorityLightLow
	}
	return t.Palette.TextSecondary
}
