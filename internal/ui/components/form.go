package components

import (
	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// FieldOptions labels a form control and explains its state. Description is
// shown below the control; Error marks it invalid, shows the message in the
// theme's danger color and is read by assistive technology with the control.
type FieldOptions struct {
	Label       string
	Placeholder string
	Description string
	Error       string
	Disabled    bool
	ReadOnly    bool
	AutoFocus   bool
	// Compact drops the visible label for a control in a toolbar row, where
	// the placeholder and the neighbouring controls carry the meaning. The
	// label still names the control for assistive technology.
	Compact bool
	// Password masks the input, for write-only key material. The value is
	// still reported to the caller; it is never shown back to the user.
	Password bool
}

// Input builds a single-line text input. The label names it for assistive
// technology and a click on it focuses the input. The returned element is
// the input itself: Changed and Submitted report its state, which the field
// wrapper around it does not carry.
func (u *UI) Input(value *string, o FieldOptions) ui.Element {
	in := func() ui.Element {
		e := ui.TextInput(u.C, value).
			Placeholder(o.Placeholder).
			MinHeight(tokens.Space8).
			Disabled(o.Disabled).
			ReadOnly(o.ReadOnly)
		if o.Password {
			e.Password()
		}
		if o.AutoFocus {
			e.AutoFocus()
		}
		return e
	}
	return u.field(o, in)
}

// Textarea builds a multi-line text input. The returned element is the
// textarea itself, so Changed reports its state.
func (u *UI) Textarea(value *string, o FieldOptions) ui.Element {
	in := func() ui.Element {
		e := ui.TextArea(u.C, value).
			Placeholder(o.Placeholder).
			Disabled(o.Disabled).
			ReadOnly(o.ReadOnly)
		if o.AutoFocus {
			e.AutoFocus()
		}
		return e
	}
	return u.field(o, in)
}

// SelectOptions configures a drop-down.
type SelectOptions struct {
	Label       string
	Description string
	Error       string
	Disabled    bool
	Options     []string
	// Compact drops the field label and description for a toolbar filter,
	// showing only the control sized for a toolbar row.
	Compact bool
	// Width of a compact select; zero uses the content width.
	Width float32
}

// Select builds a drop-down choosing one of options into *selected. The
// returned element is the drop-down itself, so Changed reports a new choice
// even when a field label wraps it.
func (u *UI) Select(selected *string, o SelectOptions) ui.Element {
	if o.Compact {
		e := ui.Select(u.C, selected, o.Options).
			MinHeight(tokens.Space8).
			Disabled(o.Disabled)
		if o.Label != "" {
			e.Label(o.Label)
		}
		if o.Width > 0 {
			e.Width(o.Width)
		}
		return e
	}
	in := func() ui.Element {
		return ui.Select(u.C, selected, o.Options).
			MinHeight(tokens.Space8).
			Disabled(o.Disabled)
	}
	return u.field(FieldOptions{
		Label:       o.Label,
		Description: o.Description,
		Error:       o.Error,
		Disabled:    o.Disabled,
	}, in)
}

// SearchFieldWidth is the width of a search field in a page toolbar, so the
// toolbars of the modules line up with each other.
const SearchFieldWidth float32 = 280

// SearchOptions configures a search field.
type SearchOptions struct {
	// Label names the search for assistive technology, as "Search papers".
	Label       string
	Placeholder string
	Disabled    bool
	// Compact drops the visible label for a search field in a toolbar row.
	Compact bool
}

// SearchInput builds the design system's search field: a magnifying glass, the
// text, and a clear button while it holds any. Query Changed and Submitted on
// the returned element to filter or search.
func (u *UI) SearchInput(query *string, o SearchOptions) ui.Element {
	in := func() ui.Element {
		e := ui.SearchField(u.C, query).
			MinHeight(tokens.Space8).
			Disabled(o.Disabled)
		if o.Placeholder != "" {
			e.Placeholder(o.Placeholder)
		}
		return e
	}
	return u.field(FieldOptions{Label: o.Label, Disabled: o.Disabled, Compact: o.Compact}, in)
}

// ToggleOptions configures a check box, radio group or switch.
type ToggleOptions struct {
	// Description explains what the control changes.
	Description string
	// Error marks the control invalid and shows the message.
	Error    string
	Disabled bool
}

// Checkbox builds a check box with its own label text. The element is the
// control itself, not the wrapper around it: MyGo reports Changed on the
// widget, so a caller that reads it needs the control back.
func (u *UI) Checkbox(checked *bool, text string, o ToggleOptions) ui.Element {
	var ctrl ui.Element
	build := func() { ctrl = ui.Checkbox(u.C, checked, text).Disabled(o.Disabled) }
	if o.Description == "" && o.Error == "" {
		build()
		return ctrl
	}
	u.toggle(o, build)
	return ctrl
}

// Switch builds an on/off switch, used for settings that apply at once. It
// returns the control, as Checkbox does, so Changed is readable.
func (u *UI) Switch(on *bool, label string, o ToggleOptions) ui.Element {
	var ctrl ui.Element
	build := func() {
		if label != "" {
			ctrl = ui.Field(u.C, label, func() {
				ctrl = ui.Switch(u.C, on).Disabled(o.Disabled)
			})
			return
		}
		ctrl = ui.Switch(u.C, on).Disabled(o.Disabled)
	}
	if o.Description == "" && o.Error == "" {
		build()
		return ctrl
	}
	u.toggle(o, build)
	return ctrl
}

// RadioOption is one choice of a radio group.
type RadioOption[T comparable] struct {
	Value    T
	Label    string
	Disabled bool
}

// RadioOptions configures a radio group.
type RadioOptions struct {
	Label string
	// Row lays the choices out horizontally instead of in a column.
	Row      bool
	Disabled bool
}

// Radio builds one radio button, for a choice that stands alone or is laid
// out by the caller. Prefer RadioGroup for a real group: it is one stop of
// Tab and the arrows move its choice.
func (u *UI) Radio[T comparable](selected *T, value T, label string, disabled bool) ui.Element {
	return ui.Radio(u.C, selected, value, label).Disabled(disabled)
}

// RadioGroup builds a group of radio buttons choosing one value. The group is
// one stop of Tab, and the arrows move the choice.
func (u *UI) RadioGroup[T comparable](selected *T, choices []RadioOption[T], o RadioOptions) ui.Element {
	g := ui.RadioGroup(u.C, func() {
		for _, c := range choices {
			ui.Radio(u.C, selected, c.Value, c.Label).Disabled(o.Disabled || c.Disabled)
		}
	})
	if o.Row {
		g.Row().Gap(tokens.Space4)
	}
	if o.Label != "" {
		g.Label(o.Label)
	}
	return g
}

// toggle draws a control with its optional description and error, which a
// check box, switch or radio group shows below itself.
func (u *UI) toggle(o ToggleOptions, control func()) ui.Element {
	col := ui.Column(u.C).Gap(tokens.Space1)
	col.Children(func() {
		control()
		if o.Description != "" {
			u.caption(o.Description)
		}
		if o.Error != "" {
			ui.Text(u.C, o.Error).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(u.T.Palette.StatusDanger))
		}
	})
	return col
}

// field wraps a control in a labeled field, or draws the control with its
// description and error when there is no label. The field draws the error in
// the danger color and marks the control invalid for assistive technology.
// The returned element is the control, not the wrapper: mygo reports
// Changed and Submitted on the widget's own state, which the field node
// around it never carries.
func (u *UI) field(o FieldOptions, control func() ui.Element) ui.Element {
	var ctrl ui.Element
	build := func() { ctrl = control() }
	if o.Compact {
		// No visible label: the control keeps the accessible name, and the
		// row it sits in explains it.
		build()
		if o.Label != "" {
			ctrl.Label(o.Label)
		}
		return ctrl
	}
	if o.Label == "" {
		u.toggle(ToggleOptions{Description: o.Description, Error: o.Error}, build)
		return ctrl
	}
	f := ui.Field(u.C, o.Label, build)
	if o.Description != "" {
		f.Description(o.Description)
	}
	if o.Error != "" {
		f.Error(o.Error)
	}
	return ctrl
}
