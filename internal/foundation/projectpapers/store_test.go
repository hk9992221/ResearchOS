package projectpapers

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

var fixed = &clock.Fixed{T: time.Date(2026, 8, 9, 10, 11, 12, 0, time.UTC)}

func openProject(t *testing.T) (*Store, *sql.DB, *events.Service, string) {
	t.Helper()
	projectID := ids.MustNew(fixed)
	path := filepath.Join(t.TempDir(), "project.db")
	db, err := storage.OpenDatabase(context.Background(), path, storage.KindProject, storage.Options{Clock: fixed, SkipBackup: true})
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	svc := events.New(db, events.Options{Clock: fixed})
	return New(projectID, db, svc, fixed), db, svc, projectID
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

func TestAC22LinkUnlink(t *testing.T) {
	ctx := context.Background()
	store, db, svc, projectID := openProject(t)
	paperID := ids.MustNew(fixed)

	var created bool
	inTx(t, db, func(tx *sql.Tx) error {
		var err error
		created, err = store.Link(ctx, tx, Link{ProjectID: projectID, PaperID: paperID, LinkedBy: schema.ActorUser})
		return err
	})
	if !created {
		t.Fatal("first Link reported created=false")
	}
	inTx(t, db, func(tx *sql.Tx) error {
		again, err := store.Link(ctx, tx, Link{ProjectID: projectID, PaperID: paperID, LinkedBy: schema.ActorMCP})
		if err != nil {
			return err
		}
		if again {
			t.Error("second Link reported created=true")
		}
		return nil
	})
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM project_papers`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("project_papers rows = %d, want 1", rows)
	}
	added, err := svc.List(ctx, events.Query{EventTypes: []events.Type{schema.EventRelationAdded}})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 {
		t.Fatalf("relation.added events = %d, want 1", len(added))
	}
	payload := added[0].Payload
	if payload.EntityType != schema.EntityPaper || payload.EntityID != paperID || payload.New == nil {
		t.Fatalf("relation.added payload = %+v", payload)
	}
	newObj := *payload.New
	want := map[string]string{
		"src_type": "project",
		"src_id":   projectID,
		"relation": "related_to",
		"dst_type": "paper",
		"dst_id":   paperID,
	}
	if len(newObj) != len(want) {
		t.Fatalf("payload.new = %+v, want 5 keys", newObj)
	}
	for key, value := range want {
		if got, ok := newObj[key].(string); !ok || got != value {
			t.Errorf("payload.new[%s] = %v, want %q", key, newObj[key], value)
		}
	}

	links, err := store.ByProject(ctx, projectID)
	if err != nil || len(links) != 1 || links[0].PaperID != paperID || links[0].LinkedBy != schema.ActorUser {
		t.Fatalf("ByProject = %+v, %v", links, err)
	}
	byPaper, err := store.ByPaper(ctx, paperID)
	if err != nil || len(byPaper) != 1 {
		t.Fatalf("ByPaper = %+v, %v", byPaper, err)
	}
	paperIDs, err := store.PaperIDs(ctx)
	if err != nil || len(paperIDs) != 1 || paperIDs[0] != paperID {
		t.Fatalf("PaperIDs = %+v, %v", paperIDs, err)
	}
	if n, healthy, err := store.CountRefs(ctx, paperID); err != nil || !healthy || n != 1 {
		t.Fatalf("CountRefs = %d, %v, %v", n, healthy, err)
	}
	if n, healthy, err := store.CountRefs(ctx, ids.MustNew(fixed)); err != nil || !healthy || n != 0 {
		t.Fatalf("CountRefs(missing) = %d, %v, %v", n, healthy, err)
	}

	// Unlink.
	var removed bool
	inTx(t, db, func(tx *sql.Tx) error {
		var err error
		removed, err = store.Unlink(ctx, tx, projectID, paperID, schema.ActorUser)
		return err
	})
	if !removed {
		t.Fatal("Unlink reported removed=false")
	}
	if err := db.QueryRow(`SELECT count(*) FROM project_papers`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("project_papers rows = %d, want 0", rows)
	}
	removedEvents, err := svc.List(ctx, events.Query{EventTypes: []events.Type{schema.EventRelationRemoved}})
	if err != nil {
		t.Fatal(err)
	}
	if len(removedEvents) != 1 {
		t.Fatalf("relation.removed events = %d, want 1", len(removedEvents))
	}
	oldObj := removedEvents[0].Payload.Old
	if oldObj == nil || len(*oldObj) != 5 || (*oldObj)["relation"] != "related_to" || (*oldObj)["src_type"] != "project" {
		t.Fatalf("relation.removed payload = %+v", oldObj)
	}

	// Idempotent unlink.
	inTx(t, db, func(tx *sql.Tx) error {
		again, err := store.Unlink(ctx, tx, projectID, paperID, schema.ActorUser)
		if err != nil {
			return err
		}
		if again {
			t.Error("second Unlink reported removed=true")
		}
		return nil
	})
	if n, err := svc.Count(ctx, events.Query{EventTypes: []events.Type{schema.EventRelationRemoved}}); err != nil || n != 1 {
		t.Fatalf("relation.removed count after idempotent unlink = %d, %v", n, err)
	}

	// project_papers never mirrors into entity_relations.
	var relations int
	if err := db.QueryRow(`SELECT count(*) FROM entity_relations`).Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if relations != 0 {
		t.Fatalf("entity_relations rows = %d, want 0", relations)
	}
}

func TestLinkValidation(t *testing.T) {
	ctx := context.Background()
	store, db, _, projectID := openProject(t)
	paperID := ids.MustNew(fixed)

	inTx(t, db, func(tx *sql.Tx) error {
		if _, err := store.Link(ctx, tx, Link{ProjectID: projectID, PaperID: paperID, LinkedBy: schema.ActorAI}); !errors.Is(err, errs.ErrBadActor) {
			t.Errorf("LinkedBy=ai = %v, want ErrBadActor", err)
		}
		if _, err := store.Link(ctx, tx, Link{ProjectID: ids.MustNew(fixed), PaperID: paperID, LinkedBy: schema.ActorUser}); err == nil {
			t.Error("Link accepted a foreign project id")
		}
		if _, err := store.Link(ctx, tx, Link{ProjectID: projectID, PaperID: "not-a-uuid", LinkedBy: schema.ActorUser}); !errors.Is(err, errs.ErrBadEntityID) {
			t.Errorf("invalid paper id = %v, want ErrBadEntityID", err)
		}
		if _, err := store.Unlink(ctx, tx, ids.MustNew(fixed), paperID, schema.ActorUser); err == nil {
			t.Error("Unlink accepted a foreign project id")
		}
		return nil
	})
}
