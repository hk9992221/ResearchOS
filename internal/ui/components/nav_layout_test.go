package components_test

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/tokens"
)

func TestTabsChooseAndShowPage(t *testing.T) {
	tab := 0
	tt, _ := tester(t, 700, 400, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PagePadding).Children(func() {
			u.Tabs(&tab, "All", "Active", "Archived")
			switch tab {
			case 0:
				ui.Text(u.C, "every row")
			case 1:
				ui.Text(u.C, "active rows")
			default:
				ui.Text(u.C, "archived rows")
			}
		})
	})
	if !tt.HasText("every row") {
		t.Fatalf("first tab page missing; texts %q", tt.Texts())
	}
	if err := tt.Click("Active"); err != nil {
		t.Fatalf("click tab: %v", err)
	}
	if tab != 1 || !tt.HasText("active rows") {
		t.Fatalf("tab = %d, texts %q", tab, tt.Texts())
	}
}

func TestBreadcrumbChoosesAncestor(t *testing.T) {
	chosen := 2
	tt, _ := tester(t, 700, 300, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PagePadding).Children(func() {
			u.Breadcrumb([]string{"Workspace", "Projects", "Alpha"}, &chosen)
		})
	})
	if err := tt.Click("Workspace"); err != nil {
		t.Fatalf("click breadcrumb: %v", err)
	}
	if chosen != 0 {
		t.Errorf("chosen = %d, want 0", chosen)
	}
}

func TestSplitPanelShowsOptionalPane(t *testing.T) {
	size := float32(280)
	closed, _ := tester(t, 900, 500, func(u *components.UI) {
		u.SplitPanel(components.SplitPanelOptions{
			Size:       &size,
			ShowSecond: false,
			First:      func() { ui.Text(u.C, "Main content") },
			Second:     func() { ui.Text(u.C, "Inspector") },
		})
	})
	if !closed.HasText("Main content") {
		t.Fatalf("main pane missing; texts %q", closed.Texts())
	}
	if closed.HasText("Inspector") {
		t.Error("closed inspector still rendered")
	}

	opened, _ := tester(t, 900, 500, func(u *components.UI) {
		u.SplitPanel(components.SplitPanelOptions{
			Size:       &size,
			ShowSecond: true,
			First:      func() { ui.Text(u.C, "Main content") },
			Second:     func() { ui.Text(u.C, "Inspector") },
		})
	})
	if !opened.HasText("Main content") || !opened.HasText("Inspector") {
		t.Errorf("split panes missing; texts %q", opened.Texts())
	}
}

func TestPageHeaderOrderAndContent(t *testing.T) {
	chosen := 1
	tt, _ := tester(t, 900, 360, func(u *components.UI) {
		u.PageHeader(components.PageHeaderOptions{
			Breadcrumbs:      []string{"Workspace", "Projects"},
			BreadcrumbChosen: &chosen,
			Title:            "Alpha",
			Description:      "A project about protein folding.",
			Actions: func() {
				u.Button("New note", components.ButtonOptions{Variant: components.VariantPrimary})
			},
		})
	})
	for _, want := range []string{"Workspace", "Projects", "Alpha", "A project about protein folding.", "New note"} {
		if !tt.HasText(want) {
			t.Errorf("page header lacks %q; texts %q", want, tt.Texts())
		}
	}
	title, _ := tt.Find("Alpha")
	crumb, _ := tt.Find("Workspace")
	if title.Y <= crumb.Y {
		t.Errorf("title (y=%.0f) is not above the breadcrumb (y=%.0f)", title.Y, crumb.Y)
	}
}

func TestTooltipShowsOnHover(t *testing.T) {
	tt, _ := tester(t, 500, 240, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PageBlockGap).Children(func() {
			u.Tooltip(u.IconButton(icons.Settings, "Settings", components.IconButtonOptions{}), "Open workspace settings")
		})
	})
	info, ok := tt.AccessByRole(ui.RoleButton, "Settings")
	if !ok {
		t.Fatalf("icon button missing; texts %q", tt.Texts())
	}
	tt.Move(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
	tt.Frame()
	tt.Advance(time.Second)
	if !tt.HasText("Open workspace settings") {
		t.Errorf("tooltip missing; texts %q", tt.Texts())
	}
}

func TestToolbarAndSpacer(t *testing.T) {
	tt, _ := tester(t, 760, 200, func(u *components.UI) {
		u.Toolbar("Project actions", func() {
			u.Button("Filter", components.ButtonOptions{Variant: components.VariantGhost})
			u.Spacer()
			u.Button("Refresh", components.ButtonOptions{Variant: components.VariantSecondary, Icon: icons.Refresh})
		})
	})
	if !tt.HasText("Filter") || !tt.HasText("Refresh") {
		t.Fatalf("toolbar controls missing; texts %q", tt.Texts())
	}
	info, ok := tt.Access("Project actions")
	if !ok || info.Role != ui.RoleToolbar {
		t.Errorf("toolbar not announced: %+v", info)
	}
}
