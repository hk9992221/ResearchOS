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
	// MaxWidth limits the content; zero fills the window.
	MaxWidth float32
	Children func()
}

// PageContainer applies the design system's 24px page margin and an optional
// maximum width, and lays its content out in a column.
func (u *UI) PageContainer(o PageContainerOptions) ui.Element {
	col := ui.Column(u.C).Fill().Padding(tokens.PagePadding).Gap(tokens.PageBlockGap)
	if o.MaxWidth > 0 {
		col.MaxWidth(o.MaxWidth)
	}
	col.Children(o.Children)
	return col
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
	return ui.Column(u.C).Fill().Gap(tokens.Space2).Children(func() {
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
