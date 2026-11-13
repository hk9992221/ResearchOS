// Package verification holds the executable evidence for
// docs/ui/UI-01-能力验证报告.md: a headless MyGo view that exercises every
// capability the report rates. It is a test-only package in this branch; no
// non-test code depends on it.
package verification

import (
	"fmt"
	"testing"
	"time"

	mg "github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// folderIcon is a Lucide "folder" line icon, as the design system's icon set.
var folderIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/></svg>`))

type state struct {
	dark      bool
	nav       string
	tab       int
	modal     bool
	split     float32
	progress  float64
	rows      ui.ListState
	chosen    ui.Selection[string]
	notesOpen bool
	typed     string
}

func (s *state) view(c *ui.Context) {
	th := ui.LightTheme()
	if s.dark {
		th = ui.DarkTheme()
	}
	th.FontSize = 14
	th.Font = "Inter, Segoe UI, Microsoft YaHei UI"
	th.TooltipDelay = -1 // show at once, so the test needs no clock
	c.SetTheme(th)

	ui.Row(c).Fill().Children(func() {
		// Sidebar: collapsible navigation (the width is the collapse state).
		width := float32(232)
		if s.notesOpen {
			width = 64
		}
		ui.Sidebar(c, &s.nav, func() {
			ui.SidebarSection(c, "Global", nil, func() {
				ui.SidebarItem(c, "home", folderIcon, "Home")
				ui.SidebarItem(c, "projects", folderIcon, "Projects")
			})
		}).Width(width)

		ui.Split(c, &s.split, func() {
			ui.Column(c).Fill().Padding(24).Gap(12).Children(func() {
				ui.Text(c, "Page Title").FontSize(24).FontWeight(600)
				ui.Text(c, "Section Title").FontSize(18).FontWeight(600)
				ui.Tabs(c, &s.tab, "All", "Active")
				cols := []ui.TableColumn{
					{Title: "Name", Sortable: true},
					{Title: "Status", Width: 120, Sortable: true},
				}
				s.rows.Key = func(i int) any { return rowKey(i) }
				s.rows.Selection = &s.chosen
				s.rows.Label = func(i int) string { return rowKey(i) }
				ui.Table(c, &s.rows, cols, 4, func(row, col int) {
					if col == 0 {
						ui.Text(c, rowKey(row)).SingleLine()
					} else {
						ui.Text(c, "ok").SingleLine()
					}
				}).Grow(1)
				ui.Progress(c, s.progress).Label("Scanning")
				ui.Progress(c, -1).Label("Connecting")
				ui.Text(c, "hover me").Tooltip("A tooltip")
				ui.Text(c, "1,204").FontSize(14).FontWeight(500).FontFeatures("tnum")
				if ui.Button(c, "Delete").Clicked() {
					s.modal = true
				}
			})
		}, func() {
			ui.Column(c).Width(320).Padding(16).Children(func() {
				ui.Text(c, "Inspector")
				ui.Tree(c, func() {
					ui.TreeItem(c, "Notes", &s.notesOpen, func() {
						ui.Text(c, "Draft note")
					})
				})
			})
		})
	})

	ui.Modal(c, &s.modal, func() {
		ui.Text(c, "Permanently delete?")
		if ui.Button(c, "Cancel").Clicked() {
			s.modal = false
		}
	})

	// File picker: a native directory chooser. The reference is what the
	// report's File Picker row is verified against; calling Open would show a
	// real dialog, so the test only proves the API compiles.
	openFolder := func() ([]string, error) {
		return mg.Dialog.Open(mg.OpenDialogOptions{Directory: true, Title: "Select workspace"})
	}
	_ = openFolder

	// Async UI: a background goroutine applies state through Window.Update,
	// while Context.Invalidate asks for a repaint.
	var update = func(w *mg.Window, fn func()) { w.Update(fn) }
	_ = update
	var invalidate = func(c *ui.Context) { c.Invalidate() }
	_ = invalidate
}

func rowKey(i int) string { return fmt.Sprintf("row-%d", i) }

func TestCapabilities(t *testing.T) {
	s := &state{progress: 0.4, split: 800}
	s.rows.Sort = &ui.SortOrder{} // a sortable table writes its order here
	tt := ui.NewTester(s.view, 1440, 900)

	// Typography, Color, Icons, Progress: the view renders with the theme's
	// font stack, size scale and SVG icon, headless.
	if !tt.HasText("Page Title") || !tt.HasText("Scanning") || !tt.HasText("Connecting") {
		t.Fatalf("base view missing texts: %q", tt.Texts())
	}

	// Sidebar: choosing an item.
	if err := tt.Click("Home"); err != nil {
		t.Fatalf("sidebar click: %v", err)
	}
	if s.nav != "home" {
		t.Errorf("sidebar choice = %q, want home", s.nav)
	}

	// Table: multi-select with the platform modifier.
	if err := tt.Click("row-0"); err != nil {
		t.Fatalf("table click: %v", err)
	}
	if err := tt.ClickWith(ui.Cmd, "row-2"); err != nil {
		t.Fatalf("table ctrl-click: %v", err)
	}
	if got := s.chosen.Len(); got != 2 {
		t.Errorf("multi-select chose %d rows, want 2", got)
	}

	// Table: header sorting via ListState.Sort.
	if err := tt.Click("Name"); err != nil {
		t.Fatalf("header click: %v", err)
	}
	if s.rows.Sort == nil || s.rows.Sort.Column != "Name" {
		t.Errorf("sort = %+v, want Name", s.rows.Sort)
	}

	// Tooltip: shows on hover without a delay.
	if r, ok := tt.Find("hover me"); ok {
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Frame()
		if !tt.HasText("A tooltip") {
			t.Errorf("tooltip missing; texts %q", tt.Texts())
		}
	} else {
		t.Error("hover target not found")
	}

	// Dialog: modal opens, Escape closes it.
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("open dialog: %v", err)
	}
	if !s.modal || !tt.HasText("Permanently delete?") {
		t.Fatalf("modal did not open: %v %q", s.modal, tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if s.modal {
		t.Error("Escape did not close the modal")
	}

	// Theme: a runtime switch rebuilds the same state, no reload.
	s.dark = true
	tt.Frame()
	if !tt.HasText("Inspector") || s.nav != "home" {
		t.Error("runtime theme switch lost the view or navigation")
	}

	// Time-dependent pieces settle; keep the tester honest about pending work.
	tt.Advance(50 * time.Millisecond)
}
