package theme

import (
	"context"
	"errors"
	"sync"
	"testing"

	"researchos/internal/foundation/settings"
	"researchos/internal/ui/tokens"
)

type fakeStore struct {
	mu sync.Mutex
	kv map[string]string
}

func newFakeStore() *fakeStore { return &fakeStore{kv: make(map[string]string)} }

func (s *fakeStore) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.kv[key]
	return v, ok, nil
}

func (s *fakeStore) Put(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kv[key] = value
	return nil
}

// failingStore reports every write, as a read-only or busy database does.
type failingStore struct{ err error }

func (f failingStore) Get(context.Context, string) (string, bool, error) { return "", false, nil }
func (f failingStore) Put(context.Context, string, string) error         { return f.err }

func TestDefaultIsLight(t *testing.T) {
	p := New()
	if p.Mode() != Light {
		t.Fatalf("mode = %q, want light", p.Mode())
	}
	th := p.Theme()
	if th.Dark {
		t.Error("default theme is dark")
	}
	if th.Palette.BgCanvas != tokens.Light().BgCanvas {
		t.Errorf("canvas = %s, want light canvas", th.Palette.BgCanvas)
	}
}

func TestModes(t *testing.T) {
	ctx := context.Background()
	p := New()

	if err := p.SetMode(ctx, Dark); err != nil {
		t.Fatal(err)
	}
	if !p.Theme().Dark || p.Theme().Palette.BgCanvas != tokens.Dark().BgCanvas {
		t.Error("dark mode did not resolve the dark palette")
	}

	if err := p.SetMode(ctx, System); err != nil {
		t.Fatal(err)
	}
	if p.Theme().Dark {
		t.Error("system mode with a light desktop is dark")
	}
	p.SetSystemDark(true)
	if !p.Theme().Dark {
		t.Error("system mode did not follow the desktop to dark")
	}
	p.SetSystemDark(false)
	if p.Theme().Dark {
		t.Error("system mode did not follow the desktop to light")
	}

	// The desktop appearance does not override an explicit mode.
	if err := p.SetMode(ctx, Light); err != nil {
		t.Fatal(err)
	}
	p.SetSystemDark(true)
	if p.Theme().Dark {
		t.Error("light mode followed the desktop")
	}
}

func TestInvalidMode(t *testing.T) {
	if err := New().SetMode(context.Background(), Mode("sepia")); !errors.Is(err, ErrInvalidMode) {
		t.Fatalf("err = %v, want ErrInvalidMode", err)
	}
	if Mode("light").Valid() != true || Mode("").Valid() {
		t.Error("Valid is wrong")
	}
}

func TestPersistence(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	p := New()
	if err := p.Load(ctx, store); err != nil {
		t.Fatal(err)
	}
	if err := p.SetMode(ctx, Dark); err != nil {
		t.Fatal(err)
	}
	if got := store.kv[KeyTheme]; got != "dark" {
		t.Errorf("%s = %q, want dark", KeyTheme, got)
	}
	if err := p.SetSidebarCollapsed(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := store.kv[KeySidebarCollapsed]; got != "true" {
		t.Errorf("%s = %q, want true", KeySidebarCollapsed, got)
	}
	if err := p.ToggleSidebar(ctx); err != nil {
		t.Fatal(err)
	}
	if p.SidebarCollapsed() || store.kv[KeySidebarCollapsed] != "false" {
		t.Error("toggle did not persist the collapsed state")
	}

	restored := New()
	if err := restored.Load(ctx, store); err != nil {
		t.Fatal(err)
	}
	if restored.Mode() != Dark || restored.Theme().Palette.BgCanvas != tokens.Dark().BgCanvas {
		t.Error("stored mode was not restored")
	}
	if restored.SidebarCollapsed() {
		t.Error("stored sidebar state was not restored")
	}
}

func TestLoadCorruptModeKeepsLight(t *testing.T) {
	store := newFakeStore()
	store.kv[KeyTheme] = "solarized"
	store.kv[KeySidebarCollapsed] = "yes-please"
	p := New()
	if err := p.Load(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if p.Mode() != Light {
		t.Errorf("mode = %q, want light", p.Mode())
	}
	if p.SidebarCollapsed() {
		t.Error("non-bool sidebar value was accepted")
	}
}

func TestPersistenceFailureDoesNotBlockMode(t *testing.T) {
	ctx := context.Background()
	p := New()
	p.SetStore(failingStore{err: errors.New("database is locked")})
	if err := p.SetMode(ctx, Dark); err == nil {
		t.Fatal("write error was swallowed")
	}
	if p.Mode() != Dark || !p.Theme().Dark {
		t.Error("mode change was rolled back by a write failure")
	}
}

func TestSubscribe(t *testing.T) {
	ctx := context.Background()
	p := New()
	var mu sync.Mutex
	var got []bool
	cancel := p.Subscribe(func(th tokens.Theme) {
		mu.Lock()
		got = append(got, th.Dark)
		mu.Unlock()
	})
	if err := p.SetMode(ctx, Dark); err != nil {
		t.Fatal(err)
	}
	p.SetSystemDark(true) // explicit mode: no change, no notification
	p.SetReduceMotion(true)
	cancel()
	if err := p.SetMode(ctx, Light); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || !got[0] || !got[1] {
		t.Errorf("notifications = %v, want [true true]", got)
	}
}

func TestReducedMotion(t *testing.T) {
	p := New()
	if p.Theme().Motion.Scale(tokens.PanelDuration) != tokens.PanelDuration {
		t.Error("normal motion shortened a duration")
	}
	p.SetReduceMotion(true)
	if p.Theme().Motion.Scale(tokens.PanelDuration) != 0 {
		t.Error("reduced motion did not drop the duration")
	}
	p.SetMode(context.Background(), Dark)
	if p.Theme().Motion.Scale(tokens.HoverDuration) != 0 {
		t.Error("switching modes forgot reduced motion")
	}
}

// TestSettingsStoreIsCompatible pins the wiring to the existing settings
// service: no new table, no direct SQLite access from the UI.
func TestSettingsStoreIsCompatible(t *testing.T) {
	var _ Store = settings.NewWorkspace(nil)
	var _ Store = settings.NewProject(nil)
}
