package components

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
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

// NavItemOptions configures one navigation entry of the application sidebar.
type NavItemOptions struct {
	// ID identifies the destination; the shell compares it with the current
	// route.
	ID       string
	Icon     *icons.Icon
	Label    string
	Selected bool
	// Collapsed draws the icon only, with the label as its tooltip and
	// accessible name. The rail is 64px wide: the label must stay readable
	// for assistive technology.
	Collapsed bool
	OnClick   func()
}

// NavItem builds a navigation row with the design system's selected, hover,
// pressed and focus states. It is a button, so Enter and Space activate it.
func (u *UI) NavItem(o NavItemOptions) ui.Element {
	p := u.T.Palette
	item := ui.ButtonBase(u.C).
		Key(o.ID).
		FillWidth().
		Height(tokens.Space8).
		Padding(tokens.Space1, tokens.Space2).
		Radius(tokens.RadiusMD).
		TextColor(mygo.Color(p.TextSecondary)).
		Gap(tokens.Space2).
		AlignItems(ui.Center).
		Label(o.Label)
	if o.Collapsed {
		item.Justify(ui.Center).Tooltip(o.Label)
	}
	if o.Selected {
		item.Background(mygo.Color(p.AccentSubtle)).
			TextColor(mygo.Color(p.AccentDefault))
	} else if item.Pressed() {
		item.Background(mygo.Color(p.BgSubtle))
	} else if item.Hovered() {
		item.Background(mygo.Color(p.BgHover))
	}
	if o.OnClick != nil {
		item.OnClick(o.OnClick)
	}
	item.Children(func() {
		u.Icon(o.Icon, icons.SizeNav)
		if !o.Collapsed {
			ui.Text(u.C, o.Label).
				FontSize(tokens.Typography.Body.Size).
				FontWeight(int(tokens.WeightMedium)).
				SingleLine()
		}
	})
	return item
}

// NavSection builds a titled group of navigation entries. The title hides in
// the collapsed rail; each entry keeps its tooltip.
func (u *UI) NavSection(title string, collapsed bool, body func()) ui.Element {
	col := ui.Column(u.C).FillWidth().Gap(tokens.Space1)
	col.Children(func() {
		if !collapsed {
			ui.Text(u.C, strings.ToUpper(title)).
				Padding(tokens.Space1, tokens.Space2).
				FontSize(tokens.Typography.Caption.Size).
				FontWeight(int(tokens.WeightSemibold)).
				TextColor(mygo.Color(u.T.Palette.TextMuted))
		}
		body()
	})
	return col
}

// CommandItemOptions configures one row of the command palette.
type CommandItemOptions struct {
	ID          string
	Title       string
	Hint        string
	Icon        *icons.Icon
	Highlighted bool
	OnClick     func()
}

// CommandItem builds a palette row: icon, command title, and a hint of what it
// does. It is a button, so Enter and Space run it.
func (u *UI) CommandItem(o CommandItemOptions) ui.Element {
	p := u.T.Palette
	row := ui.ButtonBase(u.C).
		Key(o.ID).
		FillWidth().
		Height(tokens.Space9).
		Padding(tokens.Space1, tokens.Space2).
		Radius(tokens.RadiusMD).
		TextColor(mygo.Color(p.TextPrimary)).
		Gap(tokens.Space2).
		AlignItems(ui.Center).
		Label(o.Title)
	if o.Highlighted {
		row.Background(mygo.Color(p.AccentSubtle)).TextColor(mygo.Color(p.AccentDefault))
	} else if row.Hovered() {
		row.Background(mygo.Color(p.BgHover))
	}
	if o.OnClick != nil {
		row.OnClick(o.OnClick)
	}
	row.Children(func() {
		u.Icon(o.Icon, icons.SizeDefault)
		ui.Text(u.C, o.Title).FontSize(tokens.Typography.Body.Size).SingleLine().Grow(1)
		if o.Hint != "" {
			ui.Text(u.C, o.Hint).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.TextMuted))
		}
	})
	return row
}
