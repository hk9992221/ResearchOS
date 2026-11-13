package components

import (
	"github.com/egoist/mygo/ui"
)

// Tabs builds the page's tab bar. The chosen index selects the page built
// below the tabs; arrows and Home/End move the choice.
func (u *UI) Tabs(selected *int, labels ...string) ui.Element {
	return ui.Tabs(u.C, selected, labels...)
}

// Breadcrumb builds the path to the current page. Items but the last are
// links; a click writes its index to chosen, and Changed on the returned
// element reports it. A nil chosen makes the trail read-only.
func (u *UI) Breadcrumb(items []string, chosen *int) ui.Element {
	if len(items) == 0 {
		return ui.Element{}
	}
	if chosen == nil {
		var local int
		chosen = &local
	}
	return ui.Breadcrumbs(u.C, items, chosen).Label("Breadcrumb")
}

// Toolbar builds a row of controls that take no face until hovered, with
// overflow into a menu when they do not fit. label names it for assistive
// technology.
func (u *UI) Toolbar(label string, fn func()) ui.Element {
	t := ui.Toolbar(u.C, fn)
	if label != "" {
		t.Label(label)
	}
	return t
}

// Spacer pushes the controls built after it to the end of a toolbar row.
func (u *UI) Spacer() ui.Element {
	return ui.Spacer(u.C)
}
