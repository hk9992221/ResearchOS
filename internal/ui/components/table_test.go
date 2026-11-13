package components_test

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/components"
	"researchos/internal/ui/tokens"
)

func tableFixture(t *testing.T, state *ui.ListState, sel ui.Selector) (*ui.Tester, *ui.ListState, ui.Selector) {
	t.Helper()
	cols := []components.Column{
		{Title: "Name", Sortable: true},
		{Title: "Status", Width: 120, Sortable: true},
	}
	tt, _ := tester(t, 760, 460, func(u *components.UI) {
		ui.Column(u.C).Fill().Padding(tokens.PagePadding).Children(func() {
			u.Table(components.TableOptions{
				Columns:   cols,
				Rows:      8,
				State:     state,
				Selection: sel,
				Key:       func(row int) any { return fmt.Sprintf("row-%d", row) },
				Cell: func(row, col int) {
					if col == 0 {
						ui.Text(u.C, fmt.Sprintf("row-%d", row)).SingleLine()
					} else {
						ui.Text(u.C, "ok").SingleLine()
					}
				},
			}).Grow(1)
		})
	})
	return tt, state, sel
}

func TestTableRowsAreFortyFourPixels(t *testing.T) {
	tt, _, _ := tableFixture(t, components.NewTableState(), nil)
	r0, ok := tt.Find("row-0")
	if !ok {
		t.Fatalf("row-0 not built; texts %q", tt.Texts())
	}
	r1, ok := tt.Find("row-1")
	if !ok {
		t.Fatalf("row-1 not built; texts %q", tt.Texts())
	}
	if d := r1.Y - r0.Y; d < 43 || d > 45 {
		t.Errorf("row pitch = %.1fpx, want 44px (§10.1)", d)
	}
}

func TestTableSortWritesState(t *testing.T) {
	state := components.NewTableState()
	tt, _, _ := tableFixture(t, state, nil)
	if err := tt.Click("Name"); err != nil {
		t.Fatalf("click header: %v", err)
	}
	if state.Sort == nil || state.Sort.Column != "Name" {
		t.Fatalf("sort = %+v, want Name", state.Sort)
	}
	as, ok := tt.AccessByRole(ui.RoleColumnHeader, "Name")
	if !ok {
		t.Fatalf("no column header named Name; texts %q", tt.Texts())
	}
	if as.Sort == ui.SortNone {
		t.Error("accessibility tree does not report the sort order")
	}
}

func TestTableMultiSelect(t *testing.T) {
	sel := components.NewSelection[string]()
	tt, _, _ := tableFixture(t, components.NewTableState(), sel)
	if err := tt.Click("row-0"); err != nil {
		t.Fatalf("click row: %v", err)
	}
	if err := tt.ClickWith(ui.Cmd, "row-2"); err != nil {
		t.Fatalf("ctrl-click row: %v", err)
	}
	if got := sel.Len(); got != 2 {
		t.Fatalf("selection = %d rows, want 2", got)
	}
	if !sel.Has("row-0") || !sel.Has("row-2") {
		t.Errorf("selection holds %v", selTests(sel))
	}
}

func selTests(sel *ui.Selection[string]) []string {
	var out []string
	for k := range sel.All() {
		out = append(out, k)
	}
	return out
}

func TestTableRowTintUsesTokens(t *testing.T) {
	state := components.NewTableState()
	tt, _ := tester(t, 760, 320, func(u *components.UI) {
		ui.Column(u.C).Fill().Padding(tokens.PagePadding).Children(func() {
			u.Table(components.TableOptions{
				Columns: []components.Column{{Title: "Name"}},
				Rows:    2,
				State:   state,
				Cell: func(row, col int) {
					ui.Text(u.C, fmt.Sprintf("tinted-%d", row)).SingleLine()
				},
				RowTint: func(row int) (tokens.Color, bool) {
					if row == 0 {
						return u.T.Palette.StatusDanger.Mix(u.T.Palette.BgSurface, 0.12), true
					}
					return tokens.Transparent, false
				},
			}).Grow(1)
		})
	})
	r, ok := tt.Find("tinted-0")
	if !ok {
		t.Fatalf("tinted row missing; texts %q", tt.Texts())
	}
	got := at(tt.Image(), r.X-4, r.Y+r.H/2)
	want := tokens.Light().StatusDanger.Mix(tokens.Light().BgSurface, 0.12)
	wantColor(t, "tinted row", got, want, 6)
}
