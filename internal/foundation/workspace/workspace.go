// Package workspace owns the workspace lifecycle: initialization, opening,
// validation, project handles, scanning and re-pointing (SPEC-01 §5.4).
package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/config"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/paths"
	"researchos/internal/foundation/storage"
)

// Logger is the foundation logging surface (SPEC-01 §5.4).
type Logger interface {
	Debug(msg string, kv ...any)
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Error(msg string, kv ...any)
}

// Options configures workspace operations.
type Options struct {
	// Clock supplies timestamps. Nil means clock.System.
	Clock clock.Clock
	// SkipBackup disables migration backups. Tests only.
	SkipBackup bool
	// BackupRoot overrides the migration backup root. Empty means the OS cache
	// directory. Tests inject a temp directory.
	BackupRoot string
	// MaxOpenConns limits each database pool. Zero means 1.
	MaxOpenConns int
	// MaxIdleConns caps idle connections. Zero means 4.
	MaxIdleConns int
	// Logger receives warnings. Nil discards them.
	Logger Logger
	// Config is the application config store used by Repoint. Nil means
	// config.NewStore() is resolved on demand.
	Config config.Store
	// Confirm issues and consumes destructive-operation tokens. Nil makes
	// confirm-gated operations fail with ErrUserConfirmationRequired.
	Confirm confirm.Service
	// Migrations registers extra migrations per database kind (module packages
	// and tests).
	Migrations map[storage.Kind][]storage.Migration
}

func (o Options) withDefaults() Options {
	if o.Clock == nil {
		o.Clock = clock.System{}
	}
	if o.MaxOpenConns <= 0 {
		o.MaxOpenConns = 1
	}
	if o.MaxIdleConns <= 0 {
		o.MaxIdleConns = 4
	}
	return o
}

func (o Options) storageOptions() storage.Options {
	var logger storage.Logger
	if o.Logger != nil {
		logger = o.Logger
	}
	return storage.Options{
		Clock:        o.Clock,
		SkipBackup:   o.SkipBackup,
		BackupRoot:   o.BackupRoot,
		Logger:       logger,
		MaxOpenConns: o.MaxOpenConns,
		MaxIdleConns: o.MaxIdleConns,
		Extra:        o.Migrations,
	}
}

// Workspace is an open workspace. All access to its mutable fields goes through
// mu; the handles it returns are usable concurrently.
type Workspace struct {
	mu       sync.Mutex
	root     string
	p        paths.Paths
	opts     Options
	global   *GlobalHandle
	projects map[string]*ProjectHandle
}

// Init creates the SPEC-00 §1 layout and global.db under root and migrates it.
// root may be missing or empty (the two OS droppings .DS_Store and desktop.ini
// are ignored). Repeating Init on a healthy workspace is a no-op that preserves
// existing data.
func Init(ctx context.Context, root string, opts Options) (*Workspace, error) {
	opts = opts.withDefaults()
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("workspace: init: absolutize root: %w", err)
	}
	if err := os.MkdirAll(abs, paths.DirMode); err != nil {
		return nil, fmt.Errorf("workspace: init: create root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("workspace: init: stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace: init: %s is not a directory", abs)
	}

	_, state, err := readMarker(abs)
	if err != nil {
		return nil, err
	}
	globalPath := filepath.Join(abs, "global.db")
	globalExists := fileExists(globalPath)

	switch {
	case state == MarkerInvalid:
		return nil, fmt.Errorf("%w: %s has an invalid marker file", errs.ErrWorkspaceCorrupt, abs)
	case state == MarkerPresent && !globalExists:
		return nil, fmt.Errorf("%w: marker exists but global.db is missing in %s", errs.ErrWorkspaceCorrupt, abs)
	case state == MarkerMissing && globalExists:
		return nil, fmt.Errorf("%w: global.db exists but the marker is missing in %s", errs.ErrWorkspaceCorrupt, abs)
	case state == MarkerMissing:
		empty, err := dirEmpty(abs)
		if err != nil {
			return nil, fmt.Errorf("workspace: init: read root: %w", err)
		}
		if !empty {
			return nil, fmt.Errorf("%w: %s already contains foreign files", errs.ErrDirNotEmpty, abs)
		}
	}

	for _, dir := range []string{"library/papers", "projects", "exports"} {
		if err := os.MkdirAll(filepath.Join(abs, filepath.FromSlash(dir)), paths.DirMode); err != nil {
			return nil, fmt.Errorf("workspace: init: create %s: %w", dir, err)
		}
	}
	if state == MarkerMissing {
		if err := writeMarker(abs, opts.Clock); err != nil {
			return nil, err
		}
	}

	p, err := paths.New(abs)
	if err != nil {
		return nil, err
	}
	db, err := storage.OpenDatabase(ctx, globalPath, storage.KindGlobal, opts.storageOptions())
	if err != nil {
		return nil, err
	}
	return &Workspace{
		root:     p.Root(),
		p:        p,
		opts:     opts,
		global:   newGlobalHandle(db),
		projects: make(map[string]*ProjectHandle),
	}, nil
}

// Open opens an existing workspace: it validates the marker and layout and
// migrates global.db. A directory that is not a workspace yields
// ErrNotWorkspace; an inconsistent marker/database pair yields
// ErrWorkspaceCorrupt.
func Open(ctx context.Context, root string, opts Options) (*Workspace, error) {
	opts = opts.withDefaults()
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("workspace: open: absolutize root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s does not exist", errs.ErrNotWorkspace, abs)
		}
		return nil, fmt.Errorf("workspace: open: stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a directory", errs.ErrNotWorkspace, abs)
	}

	_, state, err := readMarker(abs)
	if err != nil {
		return nil, err
	}
	globalPath := filepath.Join(abs, "global.db")
	globalExists := fileExists(globalPath)

	switch state {
	case MarkerInvalid:
		return nil, fmt.Errorf("%w: %s has an invalid marker file", errs.ErrWorkspaceCorrupt, abs)
	case MarkerMissing:
		if globalExists {
			return nil, fmt.Errorf("%w: global.db exists but the marker is missing in %s", errs.ErrWorkspaceCorrupt, abs)
		}
		return nil, fmt.Errorf("%w: %s has no marker and no global.db", errs.ErrNotWorkspace, abs)
	case MarkerPresent:
		if !globalExists {
			return nil, fmt.Errorf("%w: marker exists but global.db is missing in %s", errs.ErrWorkspaceCorrupt, abs)
		}
	}

	p, err := paths.New(abs)
	if err != nil {
		return nil, err
	}
	db, err := storage.OpenDatabase(ctx, globalPath, storage.KindGlobal, opts.storageOptions())
	if err != nil {
		return nil, err
	}
	return &Workspace{
		root:     p.Root(),
		p:        p,
		opts:     opts,
		global:   newGlobalHandle(db),
		projects: make(map[string]*ProjectHandle),
	}, nil
}

// Root returns the absolute workspace root.
func (w *Workspace) Root() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.root
}

// Clock returns the workspace clock. Services that need timestamps (reconcile,
// diagnostics) use it so tests keep deterministic time.
func (w *Workspace) Clock() clock.Clock {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.opts.Clock
}

// Config returns the injected application config store (nil when the workspace
// was created without one).
func (w *Workspace) Config() config.Store {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.opts.Config
}

// Paths returns the workspace path resolver.
func (w *Workspace) Paths() paths.Paths {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.p
}

// Global returns the global database handle. It is nil after Close or Repoint.
func (w *Workspace) Global() *GlobalHandle {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.global
}

// Project opens (and caches) the project database, migrating it on first open.
func (w *Workspace) Project(ctx context.Context, projectID string) (*ProjectHandle, error) {
	if !ids.Valid(projectID) {
		return nil, fmt.Errorf("workspace: project: invalid project id %q", projectID)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if h, ok := w.projects[projectID]; ok {
		return h, nil
	}
	dir, err := w.p.Abs(w.p.ProjectDir(projectID))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("workspace: project %s has no data directory %s", projectID, dir)
	}
	db, err := storage.OpenDatabase(ctx, filepath.Join(dir, "project.db"), storage.KindProject, w.opts.storageOptions())
	if err != nil {
		return nil, err
	}
	h := newProjectHandle(projectID, db, w.opts.Clock)
	w.projects[projectID] = h
	return h, nil
}

// CreateProjectData creates projects/<id>/{notes,attachments} and a migrated
// project.db. The projects.Store registration row is the caller's job (§5.4).
func (w *Workspace) CreateProjectData(ctx context.Context, projectID string) error {
	if !ids.Valid(projectID) {
		return fmt.Errorf("workspace: create project data: invalid project id %q", projectID)
	}
	w.mu.Lock()
	p := w.p
	w.mu.Unlock()

	base, err := p.Abs(p.ProjectDir(projectID))
	if err != nil {
		return err
	}
	for _, sub := range []string{"notes", "attachments"} {
		if err := os.MkdirAll(filepath.Join(base, sub), paths.DirMode); err != nil {
			return fmt.Errorf("workspace: create project data: %w", err)
		}
	}
	_, err = w.Project(ctx, projectID)
	return err
}

// Close closes every open database handle. It is safe to call twice.
func (w *Workspace) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var errList []error
	for id, h := range w.projects {
		if err := h.Close(); err != nil {
			errList = append(errList, fmt.Errorf("project %s: %w", id, err))
		}
		delete(w.projects, id)
	}
	if w.global != nil {
		if err := w.global.Close(); err != nil {
			errList = append(errList, fmt.Errorf("global: %w", err))
		}
		w.global = nil
	}
	return errors.Join(errList...)
}

// Repoint switches the workspace root after the user moved the whole directory
// tree: it validates the new layout, updates the application config and closes
// and invalidates the old handles. The caller opens the new root afterwards.
// No files are moved.
func (w *Workspace) Repoint(ctx context.Context, newRoot string) error {
	abs, err := filepath.Abs(newRoot)
	if err != nil {
		return fmt.Errorf("workspace: repoint: absolutize root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: %s is not a workspace directory", errs.ErrNotWorkspace, abs)
	}
	_, state, err := readMarker(abs)
	if err != nil {
		return err
	}
	switch state {
	case MarkerMissing:
		return fmt.Errorf("%w: %s has no marker", errs.ErrNotWorkspace, abs)
	case MarkerInvalid:
		return fmt.Errorf("%w: %s has an invalid marker", errs.ErrWorkspaceCorrupt, abs)
	}
	if !fileExists(filepath.Join(abs, "global.db")) {
		return fmt.Errorf("%w: marker exists but global.db is missing in %s", errs.ErrWorkspaceCorrupt, abs)
	}
	p, err := paths.New(abs)
	if err != nil {
		return err
	}

	cfg := w.opts.Config
	if cfg == nil {
		cfg, err = config.NewStore()
		if err != nil {
			return fmt.Errorf("workspace: repoint: %w", err)
		}
	}
	cfgDir := filepath.Dir(cfg.Path())
	if sameOrNested(p.Root(), cfgDir) {
		return fmt.Errorf("workspace: repoint: config directory %s and workspace %s must not nest", cfgDir, p.Root())
	}

	cur, err := cfg.Load(ctx)
	if err != nil && !errors.Is(err, errs.ErrConfigCorrupt) {
		return fmt.Errorf("workspace: repoint: %w", err)
	}
	cur.WorkspaceRoot = p.Root()
	if cur.SchemaVersion == 0 {
		cur.SchemaVersion = 1
	}
	if err := cfg.Save(ctx, cur); err != nil {
		return fmt.Errorf("workspace: repoint: %w", err)
	}

	w.mu.Lock()
	for id, h := range w.projects {
		_ = h.Close()
		delete(w.projects, id)
	}
	if w.global != nil {
		_ = w.global.Close()
		w.global = nil
	}
	w.root = p.Root()
	w.p = p
	w.mu.Unlock()
	return nil
}

// sameOrNested reports whether a and b are the same directory or one contains
// the other.
func sameOrNested(a, b string) bool {
	return a == b || nested(a, b) || nested(b, a)
}

func nested(outer, inner string) bool {
	rel, err := filepath.Rel(outer, inner)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
