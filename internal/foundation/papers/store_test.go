package papers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/storage"
)

var fixed = &clock.Fixed{T: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)}

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

func TestAC21ZoteroIdentity(t *testing.T) {
	store, db := openStore(t)
	ctx := context.Background()

	var personalID, groupID, againID string
	var created, againCreated bool
	inTx(t, db, func(tx *sql.Tx) error {
		var err error
		personalID, created, err = store.Upsert(ctx, tx, Paper{
			ZoteroLibraryID: "0", ZoteroKey: "K1", AttachmentKey: "ATT1",
			Metadata: json.RawMessage(`{"title":"Paper One"}`),
			FilePath: "library/papers/personal/source.pdf", FileHash: "hash-1", FileSize: 111,
		})
		if err != nil {
			return err
		}
		groupID, _, err = store.Upsert(ctx, tx, Paper{
			ZoteroLibraryID: "G1", ZoteroKey: "K1",
			Metadata: json.RawMessage(`{"title":"Group Paper"}`),
		})
		if err != nil {
			return err
		}
		againID, againCreated, err = store.Upsert(ctx, tx, Paper{
			ZoteroLibraryID: "0", ZoteroKey: "K1",
			Metadata: json.RawMessage(`{"title":"Changed"}`),
			FilePath: "library/papers/other/source.pdf", FileHash: "hash-2", FileSize: 222,
		})
		return err
	})
	if !created {
		t.Fatal("first Upsert reported created=false")
	}
	if !ids.Valid(personalID) || !ids.Valid(groupID) {
		t.Fatalf("invalid ids: %q %q", personalID, groupID)
	}
	if personalID == groupID {
		t.Fatal("the same zotero key in two libraries collapsed into one row")
	}
	if !ids.Valid(againID) || againCreated {
		t.Fatalf("second Upsert = (%q, %v), want existing id and created=false", againID, againCreated)
	}
	if againID != personalID {
		t.Fatalf("second Upsert id = %q, want %q", againID, personalID)
	}

	p, err := store.Get(ctx, personalID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.FilePath != "library/papers/personal/source.pdf" || p.FileHash != "hash-1" || p.FileSize != 111 {
		t.Fatalf("Upsert overwrote the existing copy metadata: %+v", p)
	}
	if string(p.Metadata) != `{"title":"Paper One"}` {
		t.Fatalf("Upsert merged metadata implicitly: %s", p.Metadata)
	}

	if n := count(t, db, `SELECT count(*) FROM papers`); n != 2 {
		t.Fatalf("papers rows = %d, want 2", n)
	}
	// The unique index itself rejects a raw duplicate identity.
	now := clock.Format(fixed.Now())
	if _, err := db.Exec(`INSERT INTO papers(id, zotero_library_id, zotero_key, metadata, imported_at, updated_at)
		VALUES ('dup', '0', 'K1', '{}', ?, ?)`, now, now); err == nil {
		t.Fatal("raw duplicate identity insert succeeded")
	}
}

func TestByZoteroAndDefaults(t *testing.T) {
	store, db := openStore(t)
	ctx := context.Background()
	var id string
	inTx(t, db, func(tx *sql.Tx) error {
		var err error
		id, _, err = store.Upsert(ctx, tx, Paper{ZoteroKey: "K2"})
		return err
	})
	p, found, err := store.ByZotero(ctx, "0", "K2")
	if err != nil {
		t.Fatalf("ByZotero: %v", err)
	}
	if !found || p.ID != id {
		t.Fatalf("ByZotero = (%+v, %v)", p, found)
	}
	if string(p.Metadata) != "{}" {
		t.Fatalf("default metadata = %q, want {}", p.Metadata)
	}
	if p.FileState != schema.PaperFileOK || p.SourceState != schema.PaperSourceOK {
		t.Fatalf("default states = %q / %q", p.FileState, p.SourceState)
	}
	if p.FilePath != "" || p.FileHash != "" || p.FileSize != 0 || p.LastSyncedAt != nil {
		t.Fatalf("default copy fields = %+v", p)
	}
	if _, found, err := store.ByZotero(ctx, "0", "NOPE"); err != nil || found {
		t.Fatalf("ByZotero(missing) = %v, %v", found, err)
	}
	inTx(t, db, func(tx *sql.Tx) error {
		if _, _, err := store.Upsert(ctx, tx, Paper{ZoteroKey: ""}); err == nil {
			t.Error("Upsert accepted an empty zotero key")
		}
		if _, _, err := store.Upsert(ctx, tx, Paper{ZoteroKey: "K3", Metadata: json.RawMessage(`{bad`)}); err == nil {
			t.Error("Upsert accepted invalid metadata JSON")
		}
		return nil
	})
}

func TestList(t *testing.T) {
	store, db := openStore(t)
	ctx := context.Background()
	inTx(t, db, func(tx *sql.Tx) error {
		if _, _, err := store.Upsert(ctx, tx, Paper{ZoteroKey: "A", Metadata: json.RawMessage(`{"title":"Alpha Study"}`)}); err != nil {
			return err
		}
		if _, _, err := store.Upsert(ctx, tx, Paper{ZoteroKey: "B", Metadata: json.RawMessage(`{"title":"Beta Study"}`)}); err != nil {
			return err
		}
		return nil
	})
	got, err := store.List(ctx, PaperQuery{TitleLike: "alph"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ZoteroKey != "A" {
		t.Fatalf("List(TitleLike) = %+v", got)
	}
	got, err = store.List(ctx, PaperQuery{Limit: 1})
	if err != nil {
		t.Fatalf("List limit: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List(limit 1) returned %d rows", len(got))
	}
	got, err = store.List(ctx, PaperQuery{Offset: 1})
	if err != nil {
		t.Fatalf("List offset: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List(offset 1) returned %d rows", len(got))
	}
	missing := schema.PaperFileMissing
	got, err = store.List(ctx, PaperQuery{FileState: &missing})
	if err != nil {
		t.Fatalf("List(file missing): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List(file missing) = %+v", got)
	}
	since := fixed.Now().Add(-time.Hour)
	until := fixed.Now().Add(time.Hour)
	got, err = store.List(ctx, PaperQuery{ImportedFrom: &since, ImportedTo: &until})
	if err != nil {
		t.Fatalf("List(imported range): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List(imported range) = %d rows, want 2", len(got))
	}
}

func TestUpdateFileAndDelete(t *testing.T) {
	store, db := openStore(t)
	ctx := context.Background()
	var id string
	inTx(t, db, func(tx *sql.Tx) error {
		var err error
		id, _, err = store.Upsert(ctx, tx, Paper{ZoteroKey: "K"})
		return err
	})
	inTx(t, db, func(tx *sql.Tx) error {
		return store.UpdateFile(ctx, tx, id, "library/papers/x/source.pdf", "abc", 42, schema.PaperFileMissing)
	})
	p, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.FilePath != "library/papers/x/source.pdf" || p.FileHash != "abc" || p.FileSize != 42 || p.FileState != schema.PaperFileMissing {
		t.Fatalf("UpdateFile did not persist: %+v", p)
	}
	inTx(t, db, func(tx *sql.Tx) error {
		if err := store.UpdateFile(ctx, tx, id, "x", "h", 1, schema.PaperFileState("broken")); err == nil {
			t.Error("UpdateFile accepted an invalid state")
		}
		if err := store.UpdateFile(ctx, tx, "missing", "x", "h", 1, schema.PaperFileOK); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("UpdateFile(missing) = %v, want sql.ErrNoRows", err)
		}
		return nil
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
		return nil
	})
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}
