package externalrefs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/storage"
)

var fixed = &clock.Fixed{T: time.Date(2027, 4, 5, 6, 7, 8, 0, time.UTC)}

func setup(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "project.db")
	db, err := storage.OpenDatabase(context.Background(), path, storage.KindProject,
		storage.Options{Clock: fixed, SkipBackup: true})
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db, events.New(db, events.Options{Clock: fixed}), fixed), db
}

func inTx(t *testing.T, db *sql.DB, fn func(tx *sql.Tx) error) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("transaction body: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func insertExperiment(t *testing.T, db *sql.DB, id, dirRef string) {
	t.Helper()
	now := clock.Format(fixed.Now())
	if _, err := db.Exec(
		`INSERT INTO experiments(id, title, dir_ref, created_at, updated_at) VALUES (?, 'exp', ?, ?, ?)`,
		id, dirRef, now, now); err != nil {
		t.Fatalf("insert experiment: %v", err)
	}
}

func insertRun(t *testing.T, db *sql.DB, id, experimentID string) {
	t.Helper()
	now := clock.Format(fixed.Now())
	if _, err := db.Exec(
		`INSERT INTO runs(id, experiment_id, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		id, experimentID, now, now); err != nil {
		t.Fatalf("insert run: %v", err)
	}
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// snapshot records every entry's relative path, size and mtime under root.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("%d|%v", info.Size(), info.ModTime())
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

func TestAC11ExternalReferenceLifecycle(t *testing.T) {
	ctx := context.Background()
	svc, db := setup(t)
	expID := ids.MustNew(fixed)
	oldDir := filepath.Join(t.TempDir(), "experiment")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "run.log"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	insertExperiment(t, db, expID, oldDir)

	var refID string
	inTx(t, db, func(tx *sql.Tx) error {
		var err error
		refID, err = svc.Register(ctx, tx, Ref{
			OwnerType: schema.EntityExperiment, OwnerID: expID,
			Field: FieldDirRef, Kind: KindDirectory, Path: oldDir,
		})
		return err
	})

	beforeProbe := snapshot(t, oldDir)
	statuses, err := svc.Check(ctx, "p", []string{refID})
	if err != nil || len(statuses) != 1 || !statuses[0].Exists {
		t.Fatalf("Check = %+v, %v", statuses, err)
	}
	if after := snapshot(t, oldDir); !reflect.DeepEqual(beforeProbe, after) {
		t.Fatalf("Check modified the external directory:\nbefore %v\nafter  %v", beforeProbe, after)
	}
	if statuses[0].Status != StatusOK || statuses[0].LastCheckedAt == nil {
		t.Fatalf("Check ref = %+v", statuses[0].Ref)
	}

	// The directory is renamed behind our back: broken, but the entity column
	// must keep the old value.
	newDir := oldDir + "-moved"
	if err := os.Rename(oldDir, newDir); err != nil {
		t.Fatal(err)
	}
	statuses, err = svc.Check(ctx, "p", []string{refID})
	if err != nil || len(statuses) != 1 || statuses[0].Exists {
		t.Fatalf("Check(broken) = %+v, %v", statuses, err)
	}
	if statuses[0].Reason != "不存在" {
		t.Fatalf("Reason = %q", statuses[0].Reason)
	}
	var dirRef string
	if err := db.QueryRow(`SELECT dir_ref FROM experiments WHERE id = ?`, expID).Scan(&dirRef); err != nil {
		t.Fatal(err)
	}
	if dirRef != oldDir {
		t.Fatalf("experiments.dir_ref = %q, want the unchanged %q", dirRef, oldDir)
	}
	if _, err := svc.CheckAndRecord(ctx, "p", []string{refID}); err != nil {
		t.Fatalf("CheckAndRecord: %v", err)
	}
	var recorded string
	if err := db.QueryRow(`SELECT status FROM external_refs WHERE id = ?`, refID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != StatusBroken {
		t.Fatalf("recorded status = %q, want broken", recorded)
	}

	beforeRelocate := snapshot(t, newDir)
	res, err := svc.Relocate(ctx, RelocateRequest{ProjectID: "p", RefID: refID, NewPath: newDir}, schema.ActorUser)
	if err != nil {
		t.Fatalf("Relocate: %v", err)
	}
	if !res.Status.Exists || res.Status.Path != newDir || res.Status.Status != StatusOK {
		t.Fatalf("Relocate result = %+v", res.Status)
	}
	if got := count(t, db, `SELECT count(*) FROM external_refs`); got != 1 {
		t.Fatalf("external_refs rows = %d, want 1", got)
	}
	if err := db.QueryRow(`SELECT dir_ref FROM experiments WHERE id = ?`, expID).Scan(&dirRef); err != nil {
		t.Fatal(err)
	}
	if dirRef != newDir {
		t.Fatalf("experiments.dir_ref = %q, want %q", dirRef, newDir)
	}
	writer := events.New(db, events.Options{Clock: fixed})
	relocations, err := writer.List(ctx, events.Query{EventTypes: []events.Type{schema.EventEntityRelocated}})
	if err != nil || len(relocations) != 1 {
		t.Fatalf("entity.relocated = %+v, %v", relocations, err)
	}
	if relocations[0].Payload.Old == nil || (*relocations[0].Payload.Old)["path"] != oldDir {
		t.Fatalf("old path = %+v", relocations[0].Payload.Old)
	}
	if relocations[0].Payload.New == nil || (*relocations[0].Payload.New)["path"] != newDir {
		t.Fatalf("new path = %+v", relocations[0].Payload.New)
	}

	// Relocating is read-only for the external directory.
	if after := snapshot(t, newDir); !reflect.DeepEqual(beforeRelocate, after) {
		t.Fatalf("external directory changed:\nbefore %v\nafter  %v", beforeRelocate, after)
	}
}

func TestGitRepositoryProbeAndRelocate(t *testing.T) {
	ctx := context.Background()
	svc, db := setup(t)
	expID := ids.MustNew(fixed)
	repoOld := filepath.Join(t.TempDir(), "repo-old")
	repoNew := filepath.Join(t.TempDir(), "repo-new")
	for _, dir := range []string{repoOld, repoNew} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// A worktree-style .git *file* is enough; nothing inside is read.
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	insertExperiment(t, db, expID, "")
	var refID string
	inTx(t, db, func(tx *sql.Tx) error {
		var err error
		refID, err = svc.Register(ctx, tx, Ref{
			OwnerType: schema.EntityExperiment, OwnerID: expID,
			Field: FieldGitRepo, Kind: KindGitRepository, Path: repoOld,
		})
		return err
	})
	statuses, err := svc.Check(ctx, "p", []string{refID})
	if err != nil || len(statuses) != 1 || !statuses[0].Exists {
		t.Fatalf("Check(git) = %+v, %v", statuses, err)
	}
	// git_repo has no owning column: relocation updates only the reference row.
	res, err := svc.Relocate(ctx, RelocateRequest{ProjectID: "p", RefID: refID, NewPath: repoNew}, schema.ActorUser)
	if err != nil || res.Status.Path != repoNew {
		t.Fatalf("Relocate(git) = %+v, %v", res.Status, err)
	}

	// Plain directories are not repositories.
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	inTx(t, db, func(tx *sql.Tx) error {
		_, err := svc.Register(ctx, tx, Ref{
			OwnerType: schema.EntityExperiment, OwnerID: ids.MustNew(fixed),
			Field: FieldGitRepo, Kind: KindGitRepository, Path: plain,
		})
		if !errors.Is(err, errs.ErrRefNotFound) {
			t.Errorf("Register(plain dir as git) = %v, want ErrRefNotFound", err)
		}
		return nil
	})
}

func TestRunOwnerAndUpsert(t *testing.T) {
	ctx := context.Background()
	svc, db := setup(t)
	expID := ids.MustNew(fixed)
	runID := ids.MustNew(fixed)
	insertExperiment(t, db, expID, "")
	insertRun(t, db, runID, expID)

	dir := filepath.Join(t.TempDir(), "run-data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var refID string
	inTx(t, db, func(tx *sql.Tx) error {
		var err error
		refID, err = svc.Register(ctx, tx, Ref{
			OwnerType: schema.EntityRun, OwnerID: runID,
			Field: FieldDirRef, Kind: KindDirectory, Path: dir,
		})
		return err
	})
	if _, err := svc.Relocate(ctx, RelocateRequest{ProjectID: "p", RefID: refID, NewPath: dir}, schema.ActorUser); err != nil {
		t.Fatalf("Relocate(run): %v", err)
	}
	var runDir string
	if err := db.QueryRow(`SELECT dir_ref FROM runs WHERE id = ?`, runID).Scan(&runDir); err != nil {
		t.Fatal(err)
	}
	if runDir != dir {
		t.Fatalf("runs.dir_ref = %q", runDir)
	}

	// Registering the same (owner, field) again updates the row in place.
	inTx(t, db, func(tx *sql.Tx) error {
		again, err := svc.Register(ctx, tx, Ref{
			OwnerType: schema.EntityRun, OwnerID: runID,
			Field: FieldDirRef, Kind: KindDirectory, Path: dir,
		})
		if err != nil {
			return err
		}
		if again != refID {
			t.Errorf("re-register id = %q, want %q", again, refID)
		}
		return nil
	})
	if got := count(t, db, `SELECT count(*) FROM external_refs`); got != 1 {
		t.Fatalf("external_refs rows = %d, want 1", got)
	}
	owner, err := svc.CheckOwner(ctx, "p", schema.EntityRun, runID)
	if err != nil || len(owner) != 1 || !owner[0].Exists {
		t.Fatalf("CheckOwner = %+v, %v", owner, err)
	}
	all, err := svc.Check(ctx, "p", nil)
	if err != nil || len(all) != 1 {
		t.Fatalf("Check(all) = %+v, %v", all, err)
	}
}

func TestRegisterValidation(t *testing.T) {
	ctx := context.Background()
	svc, db := setup(t)
	dir := t.TempDir()
	inTx(t, db, func(tx *sql.Tx) error {
		if _, err := svc.Register(ctx, tx, Ref{
			OwnerType: schema.EntityRun, OwnerID: "bad", Field: FieldDirRef, Kind: KindDirectory, Path: dir,
		}); !errors.Is(err, errs.ErrBadEntityID) {
			t.Errorf("invalid owner id = %v, want ErrBadEntityID", err)
		}
		if _, err := svc.Register(ctx, tx, Ref{
			OwnerType: schema.EntityNote, OwnerID: ids.MustNew(fixed), Field: FieldDirRef, Kind: KindDirectory, Path: dir,
		}); !errors.Is(err, errs.ErrBadEntityType) {
			t.Errorf("invalid owner type = %v, want ErrBadEntityType", err)
		}
		if _, err := svc.Register(ctx, tx, Ref{
			OwnerType: schema.EntityRun, OwnerID: ids.MustNew(fixed), Field: FieldDirRef, Kind: KindDirectory, Path: filepath.Join(dir, "missing"),
		}); !errors.Is(err, errs.ErrRefNotFound) {
			t.Errorf("missing path = %v, want ErrRefNotFound", err)
		}
		if _, err := svc.Register(ctx, tx, Ref{
			OwnerType: schema.EntityRun, OwnerID: ids.MustNew(fixed), Field: FieldDirRef, Kind: KindDirectory, Path: "relative/path",
		}); err == nil {
			t.Error("relative path was accepted")
		}
		return nil
	})
}
