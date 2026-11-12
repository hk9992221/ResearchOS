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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/config"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/papers"
	"researchos/internal/foundation/projectpapers"
	"researchos/internal/foundation/projects"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/settings"
	"researchos/internal/foundation/storage"
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
	// ProjectID and Project are the default project created by NewWithProject.
	ProjectID string
	Project   *workspace.ProjectHandle
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

// NewWithProject builds a zero-config fixture with one default project, ready
// for wave-2 module tests. The project's handle is cached on the fixture.
func NewWithProject(dir string, opts Options) (*Fixture, error) {
	f, err := New(dir, opts)
	if err != nil {
		return nil, err
	}
	id, handle, err := f.NewProject(context.Background(), "Fixture", "", "")
	if err != nil {
		f.Close()
		return nil, err
	}
	f.ProjectID = id
	f.Project = handle
	return f, nil
}

// Papers returns a store over the fixture's global papers table.
func (f *Fixture) Papers() *papers.Store {
	return papers.New(f.Workspace.Global().DB(), f.Clock)
}

// Settings returns the workspace-level settings store.
func (f *Fixture) Settings() *settings.Store {
	return settings.NewWorkspace(f.Workspace.Global().DB()).WithClock(f.Clock)
}

// ProjectSettings returns the settings store of one project.
func (f *Fixture) ProjectSettings(handle *workspace.ProjectHandle) *settings.Store {
	return settings.NewProject(handle.DB()).WithClock(f.Clock)
}

// SeedPaper inserts a paper with a local copy path and creates its directory.
func (f *Fixture) SeedPaper(ctx context.Context, key string) (string, error) {
	store := f.Papers()
	var id string
	if err := f.Workspace.Global().InTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, _, err = store.Upsert(ctx, tx, papers.Paper{ZoteroKey: key})
		if err != nil {
			return err
		}
		return store.UpdateFile(ctx, tx, id, f.Workspace.Paths().PaperPDF(id), "hash-"+key, 1, schema.PaperFileOK)
	}); err != nil {
		return "", err
	}
	dir, err := f.Workspace.Paths().Abs(f.Workspace.Paths().PaperDir(id))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return id, nil
}

// LinkProjectPaper associates a paper with the fixture's default project.
func (f *Fixture) LinkProjectPaper(ctx context.Context, paperID string) error {
	if f.Project == nil {
		return errors.New("foundationtest: fixture has no default project")
	}
	store := f.ProjectPapers(f.Project)
	return f.Project.InTx(ctx, func(tx *sql.Tx) error {
		_, err := store.Link(ctx, tx, projectpapers.Link{
			ProjectID: f.ProjectID, PaperID: paperID, LinkedBy: schema.ActorUser,
		})
		return err
	})
}

// SeedNotes inserts n synthetic notes directly (test-only fast path); the first
// softDeleted rows are placed in the trash. Each note gets a unique file path.
func (f *Fixture) SeedNotes(ctx context.Context, handle *workspace.ProjectHandle, n, softDeleted int) error {
	if n < 0 || softDeleted < 0 || softDeleted > n {
		return fmt.Errorf("foundationtest: invalid note counts n=%d softDeleted=%d", n, softDeleted)
	}
	if n == 0 {
		return nil
	}
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at, file_hash, file_state, deleted_at)
			 VALUES (?, 'seed', ?, 'scratchpad', ?, ?, '', 'ok', ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		at := clock.Format(f.Clock.Now())
		for i := range n {
			noteID := ids.MustNew(f.Clock)
			var deleted any
			if i < softDeleted {
				deleted = at
			}
			rel := fmt.Sprintf("projects/%s/notes/%s.md", handle.ID, noteID)
			if _, err := stmt.ExecContext(ctx, noteID, rel, at, at, deleted); err != nil {
				return err
			}
		}
		return nil
	})
}

// CompareSchema checks a live database against a golden schema file produced by
// storage.DumpSchema.
func (f *Fixture) CompareSchema(ctx context.Context, db *sql.DB, goldenPath string) error {
	got, err := storage.DumpSchema(ctx, db)
	if err != nil {
		return err
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		return err
	}
	if string(want) != got {
		return fmt.Errorf("foundationtest: schema differs from %s", goldenPath)
	}
	return nil
}

// RunConcurrentInTx runs fn in goroutines × rounds, each call with its own
// context (the pattern InTx requires for concurrent operations). It returns a
// joined error or nil.
func RunConcurrentInTx(ctx context.Context, global *workspace.GlobalHandle, goroutines, rounds int, fn func(ctx context.Context, tx *sql.Tx, g, r int) error) error {
	type key struct{}
	errCh := make(chan error, goroutines*rounds)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			gctx := context.WithValue(ctx, key{}, g)
			for r := range rounds {
				if err := global.InTx(gctx, func(tx *sql.Tx) error {
					return fn(gctx, tx, g, r)
				}); err != nil {
					errCh <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
