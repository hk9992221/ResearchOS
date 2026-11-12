package relations

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/storage"
)

var fixed = &clock.Fixed{T: time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC)}

func openTestDB(t *testing.T, kind storage.Kind) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), string(kind)+".db")
	db, err := storage.OpenDatabase(context.Background(), path, kind, storage.Options{Clock: fixed, SkipBackup: true})
	if err != nil {
		t.Fatalf("OpenDatabase(%s): %v", kind, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
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

func insertNote(t *testing.T, db *sql.DB, id string, deleted bool) {
	t.Helper()
	now := clock.Format(fixed.Now())
	var deletedAt any
	if deleted {
		deletedAt = now
	}
	if _, err := db.Exec(
		`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at, deleted_at)
		 VALUES (?, 'n', ?, 'scratchpad', ?, ?, ?)`,
		id, "projects/p/notes/"+id+".md", now, now, deletedAt); err != nil {
		t.Fatalf("insert note %s: %v", id, err)
	}
}

func insertPaper(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	now := clock.Format(fixed.Now())
	if _, err := db.Exec(
		`INSERT INTO papers(id, zotero_key, metadata, imported_at, updated_at)
		 VALUES (?, ?, '{}', ?, ?)`, id, "K-"+id, now, now); err != nil {
		t.Fatalf("insert paper %s: %v", id, err)
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

func TestAC20LinkUnlinkAndRules(t *testing.T) {
	ctx := context.Background()
	project := openTestDB(t, storage.KindProject)
	global := openTestDB(t, storage.KindGlobal)
	writer := events.New(project, events.Options{Clock: fixed})
	svc := New(project, global, writer, fixed)

	noteA, noteB, noteC, noteD := ids.MustNew(fixed), ids.MustNew(fixed), ids.MustNew(fixed), ids.MustNew(fixed)
	insertNote(t, project, noteA, false)
	insertNote(t, project, noteB, false)
	insertNote(t, project, noteC, true)
	insertNote(t, project, noteD, true)

	link := Link{SrcType: schema.EntityNote, SrcID: noteA, Relation: schema.RelationReferences, DstType: schema.EntityNote, DstID: noteB}
	var firstID string
	inTx(t, project, func(tx *sql.Tx) error {
		var err error
		firstID, err = svc.Link(ctx, tx, link, schema.ActorUser)
		return err
	})
	if !ids.Valid(firstID) {
		t.Fatalf("Link returned invalid id %q", firstID)
	}
	inTx(t, project, func(tx *sql.Tx) error {
		again, err := svc.Link(ctx, tx, link, schema.ActorUser)
		if err != nil {
			return err
		}
		if again != firstID {
			t.Errorf("idempotent Link id = %q, want %q", again, firstID)
		}
		return nil
	})
	if got := count(t, project, `SELECT count(*) FROM entity_relations`); got != 1 {
		t.Fatalf("entity_relations rows = %d, want 1", got)
	}
	added, err := writer.List(ctx, events.Query{EventTypes: []events.Type{schema.EventRelationAdded}})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 {
		t.Fatalf("relation.added events = %d, want 1", len(added))
	}
	if added[0].Payload.New == nil || len(*added[0].Payload.New) != 5 {
		t.Fatalf("relation.added payload = %+v", added[0].Payload.New)
	}

	// Wrong direction.
	ideaID := ids.MustNew(fixed)
	inTx(t, project, func(tx *sql.Tx) error {
		_, err := svc.Link(ctx, tx, Link{
			SrcType: schema.EntityIdea, SrcID: ideaID, Relation: schema.RelationValidates,
			DstType: schema.EntityNote, DstID: noteA,
		}, schema.ActorUser)
		if !errors.Is(err, errs.ErrRelationNotAllowed) {
			t.Errorf("idea -> note validates = %v, want ErrRelationNotAllowed", err)
		}
		return nil
	})

	// Self relation.
	inTx(t, project, func(tx *sql.Tx) error {
		_, err := svc.Link(ctx, tx, Link{
			SrcType: schema.EntityNote, SrcID: noteA, Relation: schema.RelationReferences,
			DstType: schema.EntityNote, DstID: noteA,
		}, schema.ActorUser)
		if !errors.Is(err, errs.ErrSelfRelation) {
			t.Errorf("self link = %v, want ErrSelfRelation", err)
		}
		return nil
	})

	// Soft-deleted endpoints cannot be linked.
	inTx(t, project, func(tx *sql.Tx) error {
		_, err := svc.Link(ctx, tx, Link{
			SrcType: schema.EntityNote, SrcID: noteA, Relation: schema.RelationReferences,
			DstType: schema.EntityNote, DstID: noteC,
		}, schema.ActorUser)
		if !errors.Is(err, errs.ErrEntityDeleted) {
			t.Errorf("link to soft-deleted target = %v, want ErrEntityDeleted", err)
		}
		_, err = svc.Link(ctx, tx, Link{
			SrcType: schema.EntityNote, SrcID: noteD, Relation: schema.RelationReferences,
			DstType: schema.EntityNote, DstID: noteA,
		}, schema.ActorUser)
		if !errors.Is(err, errs.ErrEntityDeleted) {
			t.Errorf("link from soft-deleted source = %v, want ErrEntityDeleted", err)
		}
		return nil
	})

	// Missing endpoints are errors.
	inTx(t, project, func(tx *sql.Tx) error {
		_, err := svc.Link(ctx, tx, Link{
			SrcType: schema.EntityNote, SrcID: noteA, Relation: schema.RelationReferences,
			DstType: schema.EntityNote, DstID: ids.MustNew(fixed),
		}, schema.ActorUser)
		if err == nil {
			t.Error("link to missing note succeeded")
		}
		return nil
	})

	// Cross-database: only references -> paper.
	paperID := ids.MustNew(fixed)
	insertPaper(t, global, paperID)
	inTx(t, project, func(tx *sql.Tx) error {
		_, err := svc.Link(ctx, tx, Link{
			SrcType: schema.EntityNote, SrcID: noteA, Relation: schema.RelationReferences,
			DstType: schema.EntityPaper, DstID: paperID,
		}, schema.ActorUser)
		if err != nil {
			t.Errorf("note -> paper references = %v, want nil", err)
		}
		_, err = svc.Link(ctx, tx, Link{
			SrcType: schema.EntityNote, SrcID: noteA, Relation: schema.RelationRelatedTo,
			DstType: schema.EntityPaper, DstID: paperID,
		}, schema.ActorUser)
		if !errors.Is(err, errs.ErrRelationNotAllowed) {
			t.Errorf("note -> paper related_to = %v, want ErrRelationNotAllowed", err)
		}
		_, err = svc.Link(ctx, tx, Link{
			SrcType: schema.EntityPaper, SrcID: paperID, Relation: schema.RelationReferences,
			DstType: schema.EntityNote, DstID: noteA,
		}, schema.ActorUser)
		if !errors.Is(err, errs.ErrRelationNotAllowed) {
			t.Errorf("paper -> note = %v, want ErrRelationNotAllowed", err)
		}
		return nil
	})

	// Unlink is idempotent and emits exactly one relation.removed.
	inTx(t, project, func(tx *sql.Tx) error {
		if err := svc.Unlink(ctx, tx, link, schema.ActorUser); err != nil {
			return err
		}
		return svc.Unlink(ctx, tx, link, schema.ActorUser)
	})
	if got := count(t, project, `SELECT count(*) FROM entity_relations`); got != 1 {
		t.Fatalf("entity_relations rows = %d, want 1 (paper link)", got)
	}
	removed, err := writer.List(ctx, events.Query{EventTypes: []events.Type{schema.EventRelationRemoved}})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Fatalf("relation.removed events = %d, want 1", len(removed))
	}
	if removed[0].Payload.Old == nil || (*removed[0].Payload.Old)["dst_id"] != noteB {
		t.Fatalf("relation.removed payload = %+v", removed[0].Payload.Old)
	}

	// From/To.
	from, err := svc.From(ctx, schema.EntityNote, noteA, schema.RelationReferences)
	if err != nil || len(from) != 1 || from[0].DstID != paperID {
		t.Fatalf("From = %+v, %v", from, err)
	}
	all, err := svc.From(ctx, schema.EntityNote, noteA, "")
	if err != nil || len(all) != 1 {
		t.Fatalf("From(all) = %+v, %v", all, err)
	}
	to, err := svc.To(ctx, schema.EntityPaper, paperID, schema.RelationReferences)
	if err != nil || len(to) != 1 || to[0].SrcID != noteA {
		t.Fatalf("To = %+v, %v", to, err)
	}
	none, err := svc.To(ctx, schema.EntityNote, noteB, schema.RelationReferences)
	if err != nil || len(none) != 0 {
		t.Fatalf("To(noteB) = %+v, %v", none, err)
	}

	// Resolve states.
	if state, err := svc.Resolve(ctx, schema.EntityNote, noteA); err != nil || state != TargetAlive {
		t.Fatalf("Resolve(alive note) = %v, %v", state, err)
	}
	if state, err := svc.Resolve(ctx, schema.EntityNote, noteC); err != nil || state != TargetSoftDeleted {
		t.Fatalf("Resolve(soft-deleted note) = %v, %v", state, err)
	}
	if state, err := svc.Resolve(ctx, schema.EntityNote, ids.MustNew(fixed)); err != nil || state != TargetMissing {
		t.Fatalf("Resolve(missing note) = %v, %v", state, err)
	}
	if state, err := svc.Resolve(ctx, schema.EntityPaper, paperID); err != nil || state != TargetAlive {
		t.Fatalf("Resolve(alive paper) = %v, %v", state, err)
	}
	if state, err := svc.Resolve(ctx, schema.EntityPaper, ids.MustNew(fixed)); err != nil || state != TargetMissing {
		t.Fatalf("Resolve(missing paper) = %v, %v", state, err)
	}
	if _, err := svc.Resolve(ctx, schema.EntityType("bogus"), noteA); !errors.Is(err, errs.ErrBadEntityType) {
		t.Fatalf("Resolve(bogus) = %v, want ErrBadEntityType", err)
	}
}
