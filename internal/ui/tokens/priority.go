package tokens

// The task board's priorities (SPEC-08 §4.3). A priority is not a status: the
// board's red, amber and blue must not be the ones a failure, a warning or a
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
	// priorityLight: #C11515, #B45309 and #1D4ED8, at 6.20:1, 5.02:1 and
	// 6.70:1 against White.
	priorityLightHigh   = Hex("#C11515")
	priorityLightMedium = Hex("#B45309")
	priorityLightLow    = Hex("#1D4ED8")
	// priorityDark: #FDA29B, #FDBA74 and #84CAFF, at 8.88:1, 10.22:1 and
	// 9.74:1 against the dark surface.
	priorityDarkHigh   = Hex("#FDA29B")
	priorityDarkMedium = Hex("#FDBA74")
	priorityDarkLow    = Hex("#84CAFF")
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
