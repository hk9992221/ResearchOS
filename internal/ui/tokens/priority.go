package tokens

// The task board's priorities (SPEC-08 §4.3). The colors are semantic: they
// carry the severity a reader already expects, so the dot and the 3 DIP bar
// need no legend.
//
//	High   red     urgent, must be seen first
//	Medium yellow  worth a look before the rest
//	Low    green   done first, or cheap
//	None   blue    unset: a neutral marker, not a quiet priority
//
// Each appearance resolves its own steps. The Light ones are darker and more
// saturated so a dot still reads on a white card; the Dark ones are pastels of
// the same hues on the dark surface. Every step keeps at least 4.5:1 against
// the card it sits on, so the dot never becomes a smudge.
const (
	PriorityHigh   = "high"
	PriorityMedium = "medium"
	PriorityLow    = "low"
	PriorityNone   = "none"
)

var (
	// priorityLight, against White:
	//   high   #C11515  H 0°,   S 80%, L 42%  6.20:1
	//   medium #9A6804  H 40°,  S 95%, L 31%  4.82:1
	//   low    #197B0A  H 112°, S 85%, L 26%  5.41:1
	//   none   #1D4ED8  H 224°, S 76%, L 48%  6.70:1
	//
	// The yellow is the one step that cannot be light: pure yellow on white is
	// about 1.1:1, so it sits at L 31% and reads as a dark gold. It is still
	// unmistakably yellow beside the red, and it keeps the same hue when the
	// card is dark.
	priorityLightHigh   = Hex("#C11515")
	priorityLightMedium = Hex("#9A6804")
	priorityLightLow    = Hex("#197B0A")
	priorityLightNone   = Hex("#1D4ED8")

	// priorityDark, against DarkSurface (#181B22):
	//   high   #FDA29B  H 4°,   S 96%, L 80%  8.88:1
	//   medium #FDE68A  H 48°,  S 97%, L 77%  13.84:1
	//   low    #A9F391  H 105°, S 80%, L 76%  13.06:1
	//   none   #84CAFF  H 206°, S 100%, L 76%  9.74:1
	//
	// Same hues as pastels, at 76-80% lightness so the four dots weigh the
	// same on the card. Green is the brightest hue at equal lightness, which
	// is why it carries the most contrast.
	priorityDarkHigh   = Hex("#FDA29B")
	priorityDarkMedium = Hex("#FDE68A")
	priorityDarkLow    = Hex("#A9F391")
	priorityDarkNone   = Hex("#84CAFF")
)

// Priority returns the color of the priority kind — PriorityHigh,
// PriorityMedium, PriorityLow or PriorityNone — in the theme's appearance. An
// unset priority (PriorityNone or the empty string the database stores) is
// blue; any other unknown kind is the secondary text color, as
// Palette.Status does for an unknown status.
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
		case PriorityNone, "":
			return priorityDarkNone
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
	case PriorityNone, "":
		return priorityLightNone
	}
	return t.Palette.TextSecondary
}
