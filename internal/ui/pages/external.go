package pages

import (
	"researchos/internal/foundation/externalrefs"
	"researchos/internal/ui/components"
)

// ExternalWarning renders the inline warning of §16.6 for a broken external
// reference: the original path, the reason, and the relocate entry. A healthy
// reference renders nothing, so one broken folder never blocks the page.
func ExternalWarning(v *components.UI, st externalrefs.Status, onRelocate func()) {
	if st.Exists {
		return
	}
	message := st.Path
	if st.Reason != "" {
		message += " · " + st.Reason
	}
	v.Banner(components.BannerOptions{
		Tone:        components.Warning,
		Title:       "Referenced folder not found",
		Message:     message,
		ActionLabel: "Relocate",
		OnAction:    onRelocate,
	})
}
