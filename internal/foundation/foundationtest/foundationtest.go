// Package foundationtest provides reusable fixtures for foundation-layer tests.
//
// It is a normal package (so tests can import it from any module) but is NOT
// part of the runtime: no non-test code may import it. Later milestones extend
// it with seed helpers for projects, notes and papers.
package foundationtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/config"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/projectpapers"
	"researchos/internal/foundation/projects"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/trash"
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
	// Confirm overrides the confirmation service; by default an in-memory
	// service bound to the fixture clock is created.
	Confirm confirm.Service
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
	// Confirm is the injected confirmation service.
	Confirm confirm.Service
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
	conf := opts.Confirm
	if conf == nil {
		conf = confirm.New(confirm.Options{Clock: fixed})
	}
	root := filepath.Join(dir, "workspace")
	ws, err := workspace.Init(context.Background(), root, workspace.Options{
		Clock:        fixed,
		SkipBackup:   !opts.Backups,
		BackupRoot:   backupRoot,
		MaxOpenConns: opts.MaxOpenConns,
		Logger:       opts.Logger,
		Config:       cfg,
		Confirm:      conf,
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
		Confirm:    conf,
		BackupRoot: backupRoot,
	}, nil
}

// Global returns the global database handle.
func (f *Fixture) Global() *workspace.GlobalHandle { return f.Workspace.Global() }

// Close closes the workspace.
func (f *Fixture) Close() error { return f.Workspace.Close() }

// NewProject registers a project in global.db, creates its data directory and
// database, and returns the cached project handle.
func (f *Fixture) NewProject(ctx context.Context, name, summary, goals string) (string, *workspace.ProjectHandle, error) {
	store := projects.New(f.Workspace.Global().DB(), f.Clock)
	var id string
	if err := f.Workspace.Global().InTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = store.Create(ctx, tx, name, summary, goals)
		return err
	}); err != nil {
		return "", nil, err
	}
	if err := f.Workspace.CreateProjectData(ctx, id); err != nil {
		return "", nil, err
	}
	handle, err := f.Workspace.Project(ctx, id)
	if err != nil {
		return "", nil, err
	}
	return id, handle, nil
}

// SeedActivities inserts n synthetic entity.created rows in one transaction
// without going through the events writer. Tests use it to build realistic
// activity volumes (for example the AC-15 latency baseline).
func (f *Fixture) SeedActivities(ctx context.Context, handle *workspace.ProjectHandle, n int) error {
	if n < 0 {
		return fmt.Errorf("foundationtest: negative activity count %d", n)
	}
	if n == 0 {
		return nil
	}
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO activities(id, event_type, entity_type, entity_id, payload, actor, merge_key, created_at)
			 VALUES (?, 'entity.created', 'task', ?, ?, 'user', NULL, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		at := clock.Format(f.Clock.Now())
		for range n {
			eventID := ids.MustNew(f.Clock)
			entityID := ids.MustNew(f.Clock)
			body, err := json.Marshal(events.Payload{
				EntityType: schema.EntityTask,
				EntityID:   entityID,
				Summary:    "seed",
				Actor:      schema.ActorUser,
			})
			if err != nil {
				return err
			}
			if _, err := stmt.ExecContext(ctx, eventID, entityID, string(body), at); err != nil {
				return err
			}
		}
		return nil
	})
}

// ProjectPapers returns a project_papers store bound to handle.
func (f *Fixture) ProjectPapers(handle *workspace.ProjectHandle) *projectpapers.Store {
	return projectpapers.New(handle.ID, handle.DB(), handle.Events(), f.Clock)
}

// Relations returns the relation service of handle, resolving global entities
// through the fixture's global database.
func (f *Fixture) Relations(handle *workspace.ProjectHandle) *relations.Service {
	return relations.New(handle.DB(), f.Workspace.Global().DB(), handle.Events(), f.Clock)
}

// Trash returns the trash service of handle.
func (f *Fixture) Trash(handle *workspace.ProjectHandle) *trash.Service {
	return trash.New(handle.ID, handle.DB(), f.Workspace.Paths(), handle.Events(), f.Confirm, f.Clock)
}
