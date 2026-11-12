// Package foundationtest provides reusable fixtures for foundation-layer tests.
//
// It is a normal package (so tests can import it from any module) but is NOT
// part of the runtime: no non-test code may import it. Later milestones extend
// it with seed helpers for projects, notes and papers.
package foundationtest

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/config"
	"researchos/internal/foundation/workspace"
)

// DefaultTime is the fixed instant used by fixtures when no clock is supplied.
var DefaultTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// Options configures New.
type Options struct {
	// Clock is the fixed clock shared by the workspace and the fixture. Nil
	// means a fixed clock at DefaultTime.
	Clock *clock.Fixed
	// Backups enables migration backups (disabled by default in tests).
	Backups bool
	// MaxOpenConns overrides the connection pool size.
	MaxOpenConns int
	// Logger receives workspace warnings.
	Logger workspace.Logger
	// Config overrides the config store; by default one is created under
	// <dir>/config/config.json.
	Config config.Store
	// BackupRoot overrides the backup directory; by default
	// <dir>/cache/backups.
	BackupRoot string
}

// Fixture is an initialized workspace plus its isolated config and cache
// directories.
type Fixture struct {
	// Dir is the fixture base directory (config/cache/workspace live below it).
	Dir string
	// Root is the workspace root directory.
	Root string
	// Workspace is the open workspace.
	Workspace *workspace.Workspace
	// Clock is the shared fixed clock.
	Clock *clock.Fixed
	// Config is the injected application config store.
	Config config.Store
	// BackupRoot is the migration backup root.
	BackupRoot string
}

// New builds a workspace under <dir>/workspace together with an isolated config
// file and backup directory. The caller owns dir (usually t.TempDir()).
func New(dir string, opts Options) (*Fixture, error) {
	if dir == "" {
		return nil, fmt.Errorf("foundationtest: empty base directory")
	}
	fixed := opts.Clock
	if fixed == nil {
		fixed = &clock.Fixed{T: DefaultTime}
	}
	cfg := opts.Config
	if cfg == nil {
		var err error
		cfg, err = config.NewStoreAt(filepath.Join(dir, "config", "config.json"))
		if err != nil {
			return nil, err
		}
	}
	backupRoot := opts.BackupRoot
	if backupRoot == "" {
		backupRoot = filepath.Join(dir, "cache", "backups")
	}
	root := filepath.Join(dir, "workspace")
	ws, err := workspace.Init(context.Background(), root, workspace.Options{
		Clock:        fixed,
		SkipBackup:   !opts.Backups,
		BackupRoot:   backupRoot,
		MaxOpenConns: opts.MaxOpenConns,
		Logger:       opts.Logger,
		Config:       cfg,
	})
	if err != nil {
		return nil, err
	}
	return &Fixture{
		Dir:        dir,
		Root:       root,
		Workspace:  ws,
		Clock:      fixed,
		Config:     cfg,
		BackupRoot: backupRoot,
	}, nil
}

// Global returns the global database handle.
func (f *Fixture) Global() *workspace.GlobalHandle { return f.Workspace.Global() }

// Close closes the workspace.
func (f *Fixture) Close() error { return f.Workspace.Close() }
