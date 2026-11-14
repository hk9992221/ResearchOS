package components

import (
	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// ReadingWidth is the width a long text column should not exceed
// (ui_plan.md §6.5 asks for 760–900px).
const ReadingWidth float32 = 860

// PageContainerOptions configures the page's content area.
type PageContainerOptions struct {
	// MaxWidth caps the content at a reading measure (ReadingWidth). Only a
	// page that is itself long-form prose sets it: a page of data — a
	// settings form, a list, a board, a table — leaves it zero and fills the
	// width the window gives it.
	MaxWidth float32
	Children func()
}

// PageContainer applies the design system's 24px page margin and an optional
// reading measure, and lays its content out in a column. A page without
// MaxWidth fills the work area at every window size; a capped page sits in
// the middle of the work area rather than hugging its left edge.
func (u *UI) PageContainer(o PageContainerOptions) ui.Element {
	col := ui.Column(u.C).FillWidth().Padding(tokens.PagePadding).Gap(tokens.PageBlockGap)
	if o.MaxWidth > 0 {
		col.MaxWidth(o.MaxWidth).Margin(0, ui.Auto)
	}
	col.Children(o.Children)
	return col
}

// ToolbarRowOptions configures ToolbarRow.
type ToolbarRowOptions struct {
	// Align places the controls across the row; zero centers them. A row
	// that mixes fields, whose labels sit above their controls, with buttons
	// uses End, so the buttons line up with the control boxes rather than
	// with the labels.
	Align ui.Align
	// Content builds the row's controls.
	Content func()
}

// ToolbarRow lays controls out in one row at the top of a card or page, with
// one gap between them, wrapping when the window is narrow. A button in it
// gives Align: ui.End (or ui.Center, for compact controls), since a button
// otherwise hugs its content from the start of its parent, which in a row is
// the top.
//
//	u.ToolbarRow(ToolbarRowOptions{Align: ui.End, Content: func() {
//		u.Select(&f.provider, SelectOptions{Label: ..., Width: 280})
//		u.Button(label, ButtonOptions{Align: ui.End})
//	}})
func (u *UI) ToolbarRow(o ToolbarRowOptions) ui.Element {
	row := ui.Row(u.C).
		FillWidth().
		Gap(tokens.ComponentGap).
		Wrap()
	if o.Align == 0 {
		row.AlignItems(ui.Center)
	} else {
		row.AlignItems(o.Align)
	}
	return row.Children(o.Content)
}

// PageHeaderOptions configures the header every page starts with
// (ui_plan.md §6.5).
type PageHeaderOptions struct {
	// Breadcrumbs is the path to this page, optional.
	Breadcrumbs []string
	// BreadcrumbChosen receives a click on an ancestor; nil makes the trail
	// read-only.
	BreadcrumbChosen *int
	Title            string
	Description      string
	// Actions builds the page-level controls, laid out at the end of the
	// title row.
	Actions func()
}

// PageHeader builds the breadcrumb, page title, optional description and page
// actions in the order the design system prescribes.
func (u *UI) PageHeader(o PageHeaderOptions) ui.Element {
	p := u.T.Palette
	return ui.Column(u.C).FillWidth().Gap(tokens.Space2).Children(func() {
		if len(o.Breadcrumbs) > 0 {
			u.Breadcrumb(o.Breadcrumbs, o.BreadcrumbChosen)
		}
		ui.Row(u.C).Gap(tokens.Space4).AlignItems(ui.Center).Children(func() {
			ui.Text(u.C, o.Title).
				Grow(1).
				FontSize(tokens.Typography.PageTitle.Size).
				FontWeight(int(tokens.Typography.PageTitle.Weight)).
				TextColor(mygo.Color(p.TextPrimary))
			if o.Actions != nil {
				ui.Row(u.C).Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(o.Actions)
			}
		})
		if o.Description != "" {
			ui.Text(u.C, o.Description).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(p.TextSecondary)).
				MaxWidth(ReadingWidth)
		}
	})
}

// SplitPanelOptions configures an optionally split work area: main content on
// one side, an inspector on the other (ui_plan.md §6.6).
type SplitPanelOptions struct {
	// Size is the first pane's width (or height, when Vertical) in DIPs,
	// and holds the divider position between frames. Keep it in the page's
	// state. Nil keeps a default in the component itself.
	Size *float32
	// Percent interprets Size as a percentage when true.
	Percent  bool
	Vertical bool
	// ShowSecond shows the second pane. When false the first fills the area
	// and the divider disappears.
	ShowSecond bool
	First      func()
	Second     func()
}

// SplitPanel builds a resizable two-pane layout. The second pane is optional:
// closing the inspector renders the first pane alone, without a divider.
func (u *UI) SplitPanel(o SplitPanelOptions) ui.Element {
	if !o.ShowSecond {
		col := ui.Column(u.C).Fill()
		col.Children(o.First)
		return col
	}
	split := func(size *float32) ui.Element {
		if o.Percent {
			if o.Vertical {
				return ui.SplitVerticalPercent(u.C, size, o.First, o.Second)
			}
			return ui.SplitPercent(u.C, size, o.First, o.Second)
		}
		if o.Vertical {
			return ui.SplitVertical(u.C, size, o.First, o.Second)
		}
		return ui.Split(u.C, size, o.First, o.Second)
	}
	if o.Size != nil {
		return split(o.Size)
	}
	// No caller state: keep the divider position on the split's own
	// element, so dragging still works and survives frames.
	box := ui.Column(u.C).Fill()
	size := ui.Local(box, "split-size", func() float32 { return InspectorWidth })
	box.Children(func() { split(size) })
	return box
}

// ToolbarHeight is the top toolbar height of ui_plan.md §6.4.
const ToolbarHeight float32 = 52

// SidebarWidth and SidebarCollapsedWidth are the navigation widths of
// ui_plan.md §6.3.
const (
	SidebarWidth          float32 = 232
	SidebarCollapsedWidth float32 = 64
)

// InspectorWidth is the default inspector width of ui_plan.md §6.6.
const InspectorWidth float32 = 320

// TopBar lays out the top toolbar at its designed height with a bottom
// border. It is a layout primitive for UI-04's AppShell; content comes from
// the caller.
func (u *UI) TopBar(content func()) ui.Element {
	p := u.T.Palette
	return ui.Row(u.C).
		FillWidth().
		Height(ToolbarHeight).
		Padding(0, tokens.PagePadding).
		Gap(tokens.ComponentGap).
		AlignItems(ui.Center).
		Background(mygo.Color(p.BgCanvas)).
		Children(func() {
			ui.Row(u.C).Fill().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(content)
		})
}
