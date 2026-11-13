// Package theme holds the UI theme state of ResearchOS: the Light, Dark and
// System modes of ui_plan.md §11, the tokens they resolve to, and their
// persistence through the foundation settings service.
//
// A Provider is plain Go: it owns no toolkit objects and touches no database
// beyond the settings keys. The MyGo adapter (internal/ui/mygo) maps
// Provider.Theme onto the toolkit's theme once per frame, which is what makes
// a theme switch take effect without reloading the app or rebuilding the
// navigation state.
package theme

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"researchos/internal/ui/tokens"
)

// Mode is the appearance the user chose.
type Mode string

const (
	// Light is the default appearance.
	Light Mode = "light"
	// Dark is the dark appearance.
	Dark Mode = "dark"
	// System follows the desktop's appearance.
	System Mode = "system"
)

// Settings keys of ui_plan.md §11. They live in the existing workspace
// settings table; the UI never adds a table of its own.
const (
	KeyTheme            = "ui.theme"
	KeySidebarCollapsed = "ui.sidebar_collapsed"
)

// ErrInvalidMode is returned for a mode other than Light, Dark or System.
var ErrInvalidMode = errors.New("theme: invalid mode")

// Store is the part of foundation/settings.Store the UI needs. It is an
// interface so the theme package stays independent of SQLite and so tests can
// use a map: the UI never opens a database.
type Store interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Put(ctx context.Context, key, value string) error
}

// ParseMode returns the mode named by s, defaulting to Light for an empty or
// unknown value, as a corrupt setting must not keep the app from starting.
func ParseMode(s string) Mode {
	switch Mode(s) {
	case Dark:
		return Dark
	case System:
		return System
	default:
		return Light
	}
}

// Valid reports whether m is a known mode.
func (m Mode) Valid() bool {
	return m == Light || m == Dark || m == System
}

// Provider resolves the active appearance and notifies its observers when it
// changes. It is safe for concurrent use: the UI frame reads Theme while a
// background task or a click may switch modes.
type Provider struct {
	mu       sync.RWMutex
	mode     Mode
	dark     bool // the desktop's appearance, for System
	motion   tokens.Motion
	theme    tokens.Theme
	store    Store
	sidebar  bool
	nextID   int
	watchers map[int]func(tokens.Theme)
}

// New returns a provider in Light mode. Call Load to restore the stored mode
// and sidebar state.
func New() *Provider {
	p := &Provider{mode: Light, watchers: make(map[int]func(tokens.Theme))}
	p.theme = p.resolve()
	return p
}

// Mode returns the chosen mode.
func (p *Provider) Mode() Mode {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode
}

// Theme returns the resolved appearance. The returned value is a copy; a
// change of mode never mutates it.
func (p *Provider) Theme() tokens.Theme {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.theme
}

// Dark reports whether the resolved appearance is dark.
func (p *Provider) Dark() bool { return p.Theme().Dark }

// Load restores ui.theme and ui.sidebar_collapsed from store and keeps the
// store for later writes. A missing key keeps the current value; an unknown
// mode falls back to Light. The store may be nil, in which case preferences
// live in memory only.
func (p *Provider) Load(ctx context.Context, store Store) error {
	p.mu.Lock()
	p.store = store
	p.mu.Unlock()
	if store == nil {
		return nil
	}
	raw, ok, err := store.Get(ctx, KeyTheme)
	if err != nil {
		return fmt.Errorf("theme: load %s: %w", KeyTheme, err)
	}
	collapsed, cok, err := store.Get(ctx, KeySidebarCollapsed)
	if err != nil {
		return fmt.Errorf("theme: load %s: %w", KeySidebarCollapsed, err)
	}

	p.mu.Lock()
	if ok {
		p.mode = ParseMode(raw)
	}
	if cok {
		p.sidebar = collapsed == "true"
	}
	p.theme = p.resolve()
	watchers := p.snapshotWatchers()
	th := p.theme
	p.mu.Unlock()

	notify(watchers, th)
	return nil
}

// SetStore changes where preferences are written, without reading it.
func (p *Provider) SetStore(store Store) {
	p.mu.Lock()
	p.store = store
	p.mu.Unlock()
}

// SetMode switches the appearance and persists ui.theme. The new theme is in
// effect before the call returns, whether or not persisting succeeds: a
// failed write must not make the UI lie about what it shows. The error
// reports the write.
func (p *Provider) SetMode(ctx context.Context, m Mode) error {
	if !m.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidMode, m)
	}
	p.mu.Lock()
	if p.mode == m {
		p.mu.Unlock()
		return nil
	}
	p.mode = m
	p.theme = p.resolve()
	store := p.store
	watchers := p.snapshotWatchers()
	th := p.theme
	p.mu.Unlock()

	notify(watchers, th)
	if store == nil {
		return nil
	}
	if err := store.Put(ctx, KeyTheme, string(m)); err != nil {
		return fmt.Errorf("theme: save %s: %w", KeyTheme, err)
	}
	return nil
}

// SetSystemDark records the desktop's appearance, which System mode follows.
func (p *Provider) SetSystemDark(dark bool) {
	p.mu.Lock()
	if p.dark == dark {
		p.mu.Unlock()
		return
	}
	p.dark = dark
	if p.mode != System {
		p.mu.Unlock()
		return
	}
	p.theme = p.resolve()
	watchers := p.snapshotWatchers()
	th := p.theme
	p.mu.Unlock()
	notify(watchers, th)
}

// SetReduceMotion records the desktop's reduced-motion setting. Durations of
// transitions then resolve to zero.
func (p *Provider) SetReduceMotion(reduced bool) {
	p.mu.Lock()
	if p.motion.Reduced == reduced {
		p.mu.Unlock()
		return
	}
	p.motion.Reduced = reduced
	p.theme.Motion = p.motion
	watchers := p.snapshotWatchers()
	th := p.theme
	p.mu.Unlock()
	notify(watchers, th)
}

// SidebarCollapsed reports the stored state of the application sidebar.
func (p *Provider) SidebarCollapsed() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sidebar
}

// SetSidebarCollapsed stores the sidebar state as ui.sidebar_collapsed and
// returns the write error, if any. The in-memory state changes either way.
func (p *Provider) SetSidebarCollapsed(ctx context.Context, collapsed bool) error {
	p.mu.Lock()
	if p.sidebar == collapsed {
		p.mu.Unlock()
		return nil
	}
	p.sidebar = collapsed
	store := p.store
	p.mu.Unlock()
	if store == nil {
		return nil
	}
	value := "false"
	if collapsed {
		value = "true"
	}
	if err := store.Put(ctx, KeySidebarCollapsed, value); err != nil {
		return fmt.Errorf("theme: save %s: %w", KeySidebarCollapsed, err)
	}
	return nil
}

// ToggleSidebar flips the sidebar state and stores it.
func (p *Provider) ToggleSidebar(ctx context.Context) error {
	return p.SetSidebarCollapsed(ctx, !p.SidebarCollapsed())
}

// Subscribe calls fn with the resolved theme whenever it changes, and returns
// a function that stops it. fn runs without the provider's lock, so it may
// read the provider and request a repaint.
func (p *Provider) Subscribe(fn func(tokens.Theme)) (unsubscribe func()) {
	p.mu.Lock()
	id := p.nextID
	p.nextID++
	p.watchers[id] = fn
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		delete(p.watchers, id)
		p.mu.Unlock()
	}
}

// resolve computes the theme from the mode, the desktop appearance and the
// motion policy. The caller holds the write lock or is constructing p.
func (p *Provider) resolve() tokens.Theme {
	dark := p.mode == Dark || (p.mode == System && p.dark)
	var th tokens.Theme
	if dark {
		th = tokens.DarkTheme()
	} else {
		th = tokens.LightTheme()
	}
	th.Motion = p.motion
	return th
}

// snapshotWatchers returns the watchers to call once the lock is released.
func (p *Provider) snapshotWatchers() []func(tokens.Theme) {
	out := make([]func(tokens.Theme), 0, len(p.watchers))
	for _, fn := range p.watchers {
		out = append(out, fn)
	}
	return out
}

func notify(watchers []func(tokens.Theme), th tokens.Theme) {
	for _, fn := range watchers {
		fn(th)
	}
}
