package shell_test

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/clock"
	"researchos/internal/ports"
	"researchos/internal/project"
	"researchos/internal/ui/app"
	"researchos/internal/ui/shell"
	"researchos/internal/ui/theme"
)

type fixture struct {
	t         *testing.T
	tt        *ui.Tester
	app       *app.App
	provider  *theme.Provider
	persisted []bool
}

func newFixture(t *testing.T, w, h int) *fixture {
	t.Helper()
	f := &fixture{t: t, provider: theme.New()}
	f.app = app.New(app.Options{
		WorkspaceName: "ResearchOS",
		PersistSidebar: func(_ context.Context, collapsed bool) error {
			f.persisted = append(f.persisted, collapsed)
			return nil
		},
	})
	s := shell.New(shell.Config{
		App:   f.app,
		Theme: f.provider,
		Pages: shell.DefaultPages(context.Background(), f.app),
	})
	f.tt = ui.NewTester(s.View, w, h)
	return f
}

func (f *fixture) button(name string) ui.AccessInfo {
	f.t.Helper()
	info, ok := f.tt.AccessByRole(ui.RoleButton, name)
	if !ok {
		f.t.Fatalf("no button %q; texts %q", name, f.tt.Texts())
	}
	return info
}

func (f *fixture) buttonCount(name string) int {
	n := 0
	for _, info := range f.tt.AccessAll() {
		if info.Role == ui.RoleButton && info.Name == name {
			n++
		}
	}
	return n
}

func TestSidebarCollapsesToTheRail(t *testing.T) {
	f := newFixture(t, 1440, 900)
	full := f.button("Home").Bounds.W
	if full < 150 {
		t.Fatalf("expanded nav width = %.0f, want the 232px sidebar", full)
	}

	f.tt.Click("Toggle sidebar")
	f.tt.Frame()
	f.tt.Advance(2 * tokensPanel)
	f.tt.Frame()
	rail := f.button("Home").Bounds.W
	if rail < 24 || rail > 80 {
		t.Errorf("collapsed nav width = %.0f, want the 64px rail", rail)
	}
	if !f.app.SidebarCollapsed() {
		t.Error("app did not record the collapsed sidebar")
	}
	if len(f.persisted) == 0 || !f.persisted[len(f.persisted)-1] {
		t.Errorf("sidebar state not persisted: %v", f.persisted)
	}

	f.tt.Click("Toggle sidebar")
	f.tt.Frame()
	f.tt.Advance(2 * tokensPanel)
	f.tt.Frame()
	if got := f.button("Home").Bounds.W; got < 150 {
		t.Errorf("expanded nav width = %.0f after toggling back", got)
	}
}

func TestProjectNavigationOnlyWithAnOpenProject(t *testing.T) {
	f := newFixture(t, 1440, 900)
	if _, ok := f.tt.AccessByRole(ui.RoleButton, "Literature"); ok {
		t.Fatal("project navigation is visible without an open project")
	}

	f.app.SetProject(&project.Project{ID: "p1", Name: "Alpha"})
	f.tt.Frame()
	for _, name := range []string{"Overview", "Literature", "Research Notes", "Ideas", "Experiments", "Kanban", "Weekly Review"} {
		if _, ok := f.tt.AccessByRole(ui.RoleButton, name); !ok {
			t.Errorf("project navigation lacks %q", name)
		}
	}
	if !f.tt.HasText("Alpha") {
		t.Errorf("project name missing from the header; texts %q", f.tt.Texts())
	}

	_ = f.app.CloseProject(context.Background())
	f.tt.Frame()
	if _, ok := f.tt.AccessByRole(ui.RoleButton, "Literature"); ok {
		t.Error("project navigation survived closing the project")
	}
}

func TestInspectorIsClosedByDefaultAndDoesNotOverlay(t *testing.T) {
	f := newFixture(t, 1440, 900)
	if f.tt.HasText("Nothing selected") {
		t.Fatal("inspector is open by default")
	}

	f.app.SetInspectorOpen(true)
	f.tt.Frame()
	if !f.tt.HasText("Nothing selected") {
		t.Fatalf("inspector did not open; texts %q", f.tt.Texts())
	}
	main, ok := f.tt.Find("Your research workspace at a glance.")
	if !ok {
		t.Fatalf("main content missing; texts %q", f.tt.Texts())
	}
	insp, ok := f.tt.Find("Nothing selected")
	if !ok {
		t.Fatal("inspector content missing")
	}
	if insp.X <= main.X+100 {
		t.Errorf("inspector at x=%.0f overlaps the main area at x=%.0f", insp.X, main.X)
	}

	f.tt.Click("Close inspector")
	f.tt.Frame()
	if f.tt.HasText("Nothing selected") {
		t.Error("inspector did not close")
	}
}

func TestNarrowWindowCollapsesSidebarAndClosesInspector(t *testing.T) {
	f := newFixture(t, 1440, 900)
	f.app.SetInspectorOpen(true)
	f.tt.Frame()
	if !f.tt.HasText("Nothing selected") {
		t.Fatal("inspector did not open at 1440")
	}

	f.tt.SetSize(1024, 700)
	f.tt.Frame()
	f.tt.Advance(2 * tokensPanel)
	f.tt.Frame()
	if f.tt.HasText("Nothing selected") {
		t.Error("inspector stayed open at 1024")
	}
	if w := f.button("Home").Bounds.W; w > 80 {
		t.Errorf("sidebar width at 1024 = %.0f, want the rail", w)
	}
	if f.app.SidebarCollapsed() {
		t.Error("narrowing the window overwrote the persisted sidebar preference")
	}

	f.tt.SetSize(1440, 900)
	f.tt.Frame()
	f.tt.Advance(2 * tokensPanel)
	f.tt.Frame()
	if w := f.button("Home").Bounds.W; w < 150 {
		t.Errorf("sidebar width back at 1440 = %.0f", w)
	}
}

func TestNavigationKeepsTheShellAndItsState(t *testing.T) {
	f := newFixture(t, 1440, 900)
	f.tt.Click("Toggle sidebar")
	f.tt.Frame()
	f.tt.Advance(2 * tokensPanel)
	f.tt.Frame()
	before := f.buttonCount("Home")
	if before != 1 {
		t.Fatalf("Home buttons = %d, want one sidebar entry", before)
	}

	f.tt.Click("Projects")
	f.tt.Frame()
	if got := f.app.Route().Kind; got != app.RouteProjects {
		t.Fatalf("route = %q, want projects", got)
	}
	if got := f.buttonCount("Home"); got != before {
		t.Errorf("Home buttons = %d after navigation, want %d: the shell was rebuilt", got, before)
	}
	if !f.app.SidebarCollapsed() {
		t.Error("navigation lost the sidebar state")
	}
	if w := f.button("Home").Bounds.W; w > 80 {
		t.Errorf("sidebar expanded again on navigation: width %.0f", w)
	}
}

func TestKeyboardShortcuts(t *testing.T) {
	f := newFixture(t, 1440, 900)

	f.tt.Key(ui.Cmd, ui.KeyK)
	f.tt.Type("fold")
	if got := f.app.Search(); got != "fold" {
		t.Errorf("search = %q after Ctrl+K typing, want fold", got)
	}

	f.tt.Key(ui.Cmd|ui.Shift, ui.KeyP)
	f.tt.Frame()
	if !f.app.PaletteOpen() {
		t.Fatal("Ctrl+Shift+P did not open the palette")
	}
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()
	if f.app.PaletteOpen() {
		t.Error("Escape did not close the palette")
	}

	f.app.SetInspectorOpen(true)
	f.tt.Frame()
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()
	if f.app.Inspector().Open {
		t.Error("Escape did not close the inspector")
	}
}

func TestPaletteRunsANavigationCommand(t *testing.T) {
	f := newFixture(t, 1440, 900)
	f.tt.Key(ui.Cmd|ui.Shift, ui.KeyP)
	f.tt.Frame()
	f.tt.Type("Settings")
	f.tt.Key(0, ui.KeyEnter)
	f.tt.Frame()

	if got := f.app.Route().Kind; got != ports.RouteSettings {
		t.Fatalf("route = %q, want settings", got)
	}
	if f.app.PaletteOpen() {
		t.Error("palette stayed open after running a command")
	}
	if !f.tt.HasText("Settings is not available yet") {
		t.Errorf("settings placeholder missing; texts %q", f.tt.Texts())
	}
}

func TestThemeCommandKeepsTheRoute(t *testing.T) {
	f := newFixture(t, 1440, 900)
	f.tt.Click("Projects")
	f.tt.Frame()

	f.tt.Key(ui.Cmd|ui.Shift, ui.KeyP)
	f.tt.Frame()
	f.tt.Type("Switch theme")
	f.tt.Key(0, ui.KeyEnter)
	f.tt.Frame()

	if got := f.provider.Mode(); got != theme.Dark {
		t.Errorf("theme = %q, want dark", got)
	}
	if got := f.app.Route().Kind; got != app.RouteProjects {
		t.Errorf("route = %q after theme switch, want projects", got)
	}
	if !f.tt.HasText("Projects") {
		t.Error("page content lost after theme switch")
	}
}

func TestToastFeedback(t *testing.T) {
	fixed := &clock.Fixed{T: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	f := &fixture{t: t, provider: theme.New()}
	f.app = app.New(app.Options{WorkspaceName: "ResearchOS", Clock: fixed})
	s := shell.New(shell.Config{App: f.app, Theme: f.provider, Pages: shell.DefaultPages(context.Background(), f.app)})
	f.tt = ui.NewTester(s.View, 1200, 800)

	f.app.Toast(app.LevelSuccess, "Workspace saved", "The settings were written.")
	f.tt.Frame()
	if !f.tt.HasText("Workspace saved") || !f.tt.HasText("The settings were written.") {
		t.Fatalf("toast missing; texts %q", f.tt.Texts())
	}
	f.tt.Click("Dismiss")
	f.tt.Frame()
	if f.tt.HasText("Workspace saved") {
		t.Error("dismiss did not remove the toast")
	}

	f.app.Toast(app.LevelDanger, "Scan failed", "The folder is not readable.")
	f.tt.Frame()
	if !f.tt.HasText("Scan failed") {
		t.Fatal("second toast missing")
	}
	fixed.Advance(13 * time.Second)
	f.tt.Frame()
	if f.tt.HasText("Scan failed") {
		t.Error("expired toast still shows")
	}
}

// tokensPanel is the panel transition duration, for waiting out the sidebar
// animation.
const tokensPanel = 180 * time.Millisecond
