package components

import (
	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// CardOptions configures a card: a titled group of related content
// (ui_plan.md §10.2).
type CardOptions struct {
	Title       string
	Description string
	// Actions builds the controls at the end of the title row.
	Actions func()
	// Collapse makes the title row a disclosure that folds the body away;
	// nil keeps the body always shown.
	Collapse *CollapseOptions
	// Padding defaults to 16.
	Padding float32
	// MaxWidth limits the card's width; zero fills the parent.
	MaxWidth float32
}

// CollapseOptions makes a card's title row a disclosure, as SwiftUI's
// DisclosureGroup: a click on the row, or Enter or Space while it has the
// focus, folds the body, and the chevron at the end of the row points down
// while the body shows and right while it is folded.
type CollapseOptions struct {
	// Open is the state the title row toggles: true shows the body. The
	// page owns the pointer, so the state outlives the frame.
	Open *bool
	// Label names the toggle for assistive technology, in the terms of the
	// action it performs now, such as "收起模型缓存" while open.
	Label string
}

// Card builds a surface panel with a border and the design system's radius.
// Cards group independent units of information; they do not wrap every
// paragraph.
func (u *UI) Card(o CardOptions, body func()) ui.Element {
	p := u.T.Palette
	pad := o.Padding
	if pad == 0 {
		pad = tokens.PanelPadding
	}
	card := ui.Column(u.C).
		Padding(pad).
		Gap(tokens.RelatedGap).
		Background(mygo.Color(p.BgSurface)).
		Border(1, mygo.Color(p.BorderDefault)).
		Radius(tokens.RadiusLG)
	if o.MaxWidth > 0 {
		card.MaxWidth(o.MaxWidth)
	}
	card.Children(func() {
		title := func() {
			ui.Column(u.C).Grow(1).Gap(tokens.Space1).Children(func() {
				if o.Title != "" {
					ui.Text(u.C, o.Title).
						FontSize(tokens.Typography.CardTitle.Size).
						FontWeight(int(tokens.Typography.CardTitle.Weight))
				}
				if o.Description != "" {
					u.caption(o.Description)
				}
			})
		}
		actions := func() {
			if o.Actions != nil {
				ui.Row(u.C).Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(o.Actions)
			}
		}
		if o.Collapse == nil || o.Collapse.Open == nil {
			if o.Title != "" || o.Description != "" || o.Actions != nil {
				ui.Column(u.C).Gap(tokens.Space1).Children(func() {
					ui.Row(u.C).Gap(tokens.Space3).AlignItems(ui.Center).Children(func() {
						title()
						actions()
					})
				})
			}
			body()
			return
		}
		parts := ui.CollapsibleBase(u.C, o.Collapse.Open)
		row := parts.Trigger.FillWidth().Gap(tokens.Space3).AlignItems(ui.Center)
		if o.Collapse.Label != "" {
			row = row.Label(o.Collapse.Label)
		}
		row.Children(func() {
			title()
			actions()
			chevron := icons.ChevronDown
			if !*o.Collapse.Open {
				chevron = icons.ChevronRight
			}
			u.Icon(chevron, icons.SizeDefault).TextColor(mygo.Color(p.TextSecondary))
		})
		// Hidden while folded, so the body is not built at all.
		parts.Panel(body).Gap(tokens.RelatedGap)
	})
	return card
}

// Column describes a table column (ui_plan.md §10.1).
type Column struct {
	Title string
	// ID identifies the column for sorting; Title when empty.
	ID       string
	Width    float32
	MinWidth float32
	Align    ui.Align
	Sortable bool
}

// TableOptions configures a data table.
type TableOptions struct {
	Columns []Column
	Rows    int
	// Cell builds the content of one cell. A table's cells are 14px body
	// text by default when built with u.Text.
	Cell func(row, col int)
	// State keeps sort, scroll and selection across frames. It is required
	// for sortable columns and for Selection; a nil state makes the table
	// display-only.
	State *ui.ListState
	// Key returns a row's identity, which Selection holds. It is required
	// when Selection is set; without it rows are keyed by index.
	Key func(row int) any
	// Selection enables multi-select. Create one with SelectionKeys and
	// keep it in the page's state.
	Selection ui.Selector
	// RowTint optionally paints a row's cells to show a row state, such as
	// a deleted or damaged row. Text or a StatusIndicator must say the
	// state too; color alone is never enough.
	RowTint func(row int) (tokens.Color, bool)
}

// MyGo's table cell padding, in DIPs: 1.5 spacing units above and below,
// 2.5 on either side. A row tint extends over it.
const (
	tableCellPadV float32 = 6
	tableCellPadH float32 = 10
)

// NewTableState returns a table state ready for sortable columns.
func NewTableState() *ui.ListState {
	return &ui.ListState{Sort: &ui.SortOrder{}}
}

// NewSelection returns a multi-select set for a table keyed by K.
func NewSelection[K comparable]() *ui.Selection[K] {
	return &ui.Selection[K]{}
}

// Table builds a token-styled data table: 44px rows with 14px text, sortable
// headers, hover and selection states, and row tints for damaged or deleted
// rows. Column resizing, reordering and keyboard navigation come from MyGo.
func (u *UI) Table(o TableOptions) ui.Element {
	cols := make([]ui.TableColumn, len(o.Columns))
	sortable := false
	for i, c := range o.Columns {
		cols[i] = ui.TableColumn{
			Title:    c.Title,
			ID:       c.ID,
			Width:    c.Width,
			MinWidth: c.MinWidth,
			Align:    c.Align,
			Sortable: c.Sortable,
		}
		sortable = sortable || c.Sortable
	}
	state := o.State
	if state != nil {
		if o.Selection != nil {
			state.Selection = o.Selection
		}
		if o.Key != nil && state.Key == nil {
			state.Key = o.Key
		}
		if sortable && state.Sort == nil {
			state.Sort = &ui.SortOrder{}
		}
	}
	// A table's rows are at least 44px: the cell padding of 6px above and
	// below plus the 32px content height of the design system's row.
	return ui.Table(u.C, state, cols, o.Rows, func(row, col int) {
		cell := ui.Row(u.C).Fill().MinHeight(tokens.Space8).AlignItems(ui.Center)
		if o.RowTint != nil {
			if tint, ok := o.RowTint(row); ok && tint != tokens.Transparent {
				// Extend the tint over MyGo's cell padding, which its
				// Table does not expose: 1.5 spacing units vertically and
				// 2.5 horizontally. The negative margin and matching
				// padding keep the content where it was.
				cell.Margin(-tableCellPadV, -tableCellPadH).
					Padding(tableCellPadV, tableCellPadH).
					Background(mygo.Color(tint))
			}
		}
		cell.Children(func() { o.Cell(row, col) })
	})
}

// ListOptions configures a virtualized list.
type ListOptions struct {
	Rows int
	// Row builds one row.
	Row func(i int)
	// State keeps the choice and the scroll position across frames.
	State *ui.ListState
	// Selection enables multi-select of rows keyed by K.
	Selection ui.Selector
	// Key returns a row's identity, which Selection holds.
	Key func(row int) any
}

// List builds a token-styled list of rows, 40px high with body text. Rows are
// virtualized: a list of a million rows builds only what is on screen.
func (u *UI) List(o ListOptions) ui.Element {
	state := o.State
	if state != nil {
		if o.Selection != nil {
			state.Selection = o.Selection
		}
		if o.Key != nil && state.Key == nil {
			state.Key = o.Key
		}
	}
	return ui.List(u.C, state, o.Rows, func(i int) {
		ui.Row(u.C).
			Fill().
			MinHeight(tokens.Space8).
			Padding(tokens.Space1, tokens.Space3).
			AlignItems(ui.Center).
			Children(func() { o.Row(i) })
	})
}

// Badge builds a short status label. The text carries the meaning; the tone
// color only reinforces it.
func (u *UI) Badge(text string, tone Tone) ui.Element {
	return ui.Badge(u.C, text).
		Padding(tokens.Space1, tokens.Space2).
		Radius(tokens.RadiusSM).
		Background(mygo.Color(u.background(tone))).
		TextColor(mygo.Color(u.foreground(tone))).
		FontSize(tokens.Typography.Caption.Size).
		FontWeight(int(tokens.WeightMedium))
}

// StatusIndicator builds a colored dot with a text label, for row and panel
// states. text is required: a status is never expressed by color alone
// (ui_plan.md §2.3), so an empty text falls back to the tone's name.
func (u *UI) StatusIndicator(tone Tone, text string) ui.Element {
	if text == "" {
		text = label(tone)
	}
	fg := mygo.Color(u.foreground(tone))
	return ui.Row(u.C).
		Gap(tokens.Space2).
		AlignItems(ui.Center).
		Children(func() {
			ui.Box(u.C).Size(tokens.Space2, tokens.Space2).Radius(tokens.Space1).Background(fg)
			ui.Text(u.C, text).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(fg)
		})
}

// AvatarDefaultSize is the default avatar size, 32px.
const AvatarDefaultSize = tokens.Space8

// Avatar builds an initials avatar for a person or thing. image is nil for
// the initials fallback, which is what the system pages use.
func (u *UI) Avatar(name string, size float32) ui.Element {
	if size <= 0 {
		size = AvatarDefaultSize
	}
	return ui.Avatar(u.C, name, nil).Size(size, size)
}

// Divider builds a one-pixel separator in the default border color.
func (u *UI) Divider() ui.Element {
	return ui.Divider(u.C)
}
