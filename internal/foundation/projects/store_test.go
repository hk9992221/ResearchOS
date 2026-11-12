package projects

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/storage"
)

var fixed = &clock.Fixed{T: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}

func openStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	path := t.TempDir() + "/global.db"
	db, err := storage.OpenDatabase(context.Background(), path, storage.KindGlobal, storage.Options{Clock: fixed, SkipBackup: true})
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db, fixed), db
}

func inTx(t *testing.T, db *sql.DB, fn func(tx *sql.Tx) error) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		t.Fatalf("transaction body: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestCreateAndGet(t *testing.T) {
	store, db := openStore(t)
	ctx := context.Background()
	var id string
	inTx(t, db, func(tx *sql.Tx) error {
		var err error
		id, err = store.Create(ctx, tx, "Alpha", "summary", "goals")
		return err
	})
	if !ids.Valid(id) {
		t.Fatalf("Create returned invalid id %q", id)
	}
	p, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Name != "Alpha" || p.Summary != "summary" || p.Goals != "goals" {
		t.Fatalf("Get = %+v", p)
	}
	if p.Status != schema.ProjectActive {
		t.Fatalf("Status = %q, want active", p.Status)
	}
	if p.ArchivedAt != nil {
		t.Fatalf("ArchivedAt = %v, want nil", p.ArchivedAt)
	}
	if !p.CreatedAt.Equal(fixed.Now()) || !p.UpdatedAt.Equal(fixed.Now()) {
		t.Fatalf("timestamps = %v / %v, want %v", p.CreatedAt, p.UpdatedAt, fixed.Now())
	}
	if p.FocusMD != "" {
		t.Fatalf("FocusMD = %q, want empty", p.FocusMD)
	}
}

func TestListAndStatusFilter(t *testing.T) {
	store, db := openStore(t)
	ctx := context.Background()
	idsOut := make([]string, 2)
	inTx(t, db, func(tx *sql.Tx) error {
		var err error
		if idsOut[0], err = store.Create(ctx, tx, "One", "", ""); err != nil {
			return err
		}
		idsOut[1], err = store.Create(ctx, tx, "Two", "", "")
		return err
	})

	all, err := store.List(ctx, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("List returned %d projects, want 2", len(all))
	}
	if all[0].ID > all[1].ID {
		t.Fatalf("List is not ordered by created_at, id: %v", []string{all[0].ID, all[1].ID})
	}

	archived := schema.ProjectArchived
	inTx(t, db, func(tx *sql.Tx) error {
		return store.SetStatus(ctx, tx, idsOut[0], archived, schema.ActorUser)
	})
	got, err := store.List(ctx, &archived)
	if err != nil {
		t.Fatalf("List(archived): %v", err)
	}
	if len(got) != 1 || got[0].ID != idsOut[0] {
		t.Fatalf("List(archived) = %+v", got)
	}
	active := schema.ProjectActive
	got, err = store.List(ctx, &active)
	if err != nil {
		t.Fatalf("List(active): %v", err)
	}
	if len(got) != 1 || got[0].ID != idsOut[1] {
		t.Fatalf("List(active) = %+v", got)
	}
}

func TestUpdate(t *testing.T) {
	store, db := openStore(t)
	ctx := context.Background()
	var id string
	inTx(t, db, func(tx *sql.Tx) error {
		var err error
		id, err = store.Create(ctx, tx, "Old", "keep-summary", "keep-goals")
		return err
	})
	fixed.Advance(time.Minute)
	name := "New"
	focus := "focus text"
	inTx(t, db, func(tx *sql.Tx) error {
		return store.Update(ctx, tx, id, ProjectPatch{Name: &name, FocusMD: &focus})
	})
	p, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Name != "New" || p.FocusMD != "focus text" {
		t.Fatalf("patch not applied: %+v", p)
	}
	if p.Summary != "keep-summary" || p.Goals != "keep-goals" {
		t.Fatalf("nil patch fields were overwritten: %+v", p)
	}
	if !p.UpdatedAt.Equal(fixed.Now()) {
		t.Fatalf("UpdatedAt = %v, want %v", p.UpdatedAt, fixed.Now())
	}
	// An empty patch is a no-op, including for a missing row.
	inTx(t, db, func(tx *sql.Tx) error {
		return store.Update(ctx, tx, "missing", ProjectPatch{})
	})
}

func TestSetStatusArchivedAt(t *testing.T) {
	store, db := openStore(t)
	ctx := context.Background()
	var id string
	inTx(t, db, func(tx *sql.Tx) error {
		var err error
		id, err = store.Create(ctx, tx, "P", "", "")
		return err
	})
	inTx(t, db, func(tx *sql.Tx) error {
		return store.SetStatus(ctx, tx, id, schema.ProjectArchived, schema.ActorUser)
	})
	p, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Status != schema.ProjectArchived || p.ArchivedAt == nil {
		t.Fatalf("archived state = %+v", p)
	}
	if !p.ArchivedAt.Equal(fixed.Now()) {
		t.Fatalf("ArchivedAt = %v, want %v", p.ArchivedAt, fixed.Now())
	}

	inTx(t, db, func(tx *sql.Tx) error {
		return store.SetStatus(ctx, tx, id, schema.ProjectActive, schema.ActorUser)
	})
	p, err = store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Status != schema.ProjectActive || p.ArchivedAt != nil {
		t.Fatalf("reactivated state = %+v", p)
	}

	inTx(t, db, func(tx *sql.Tx) error {
		if err := store.SetStatus(ctx, tx, id, schema.ProjectStatus("paused"), schema.ActorUser); err == nil {
			t.Error("SetStatus accepted an invalid status")
		}
		return nil
	})
}

func TestDeleteAndMissingRows(t *testing.T) {
	store, db := openStore(t)
	ctx := context.Background()
	var id string
	inTx(t, db, func(tx *sql.Tx) error {
		var err error
		id, err = store.Create(ctx, tx, "P", "", "")
		return err
	})
	inTx(t, db, func(tx *sql.Tx) error {
		return store.Delete(ctx, tx, id)
	})
	if _, err := store.Get(ctx, id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Get deleted = %v, want sql.ErrNoRows", err)
	}
	inTx(t, db, func(tx *sql.Tx) error {
		if err := store.Delete(ctx, tx, id); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("Delete missing = %v, want sql.ErrNoRows", err)
		}
		name := "x"
		if err := store.Update(ctx, tx, id, ProjectPatch{Name: &name}); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("Update missing = %v, want sql.ErrNoRows", err)
		}
		return nil
	})
}
