package events_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/storage"
)

var fixed = &clock.Fixed{T: time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)}

func newTestService(t *testing.T, loc *time.Location) (*events.Service, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "project.db")
	db, err := storage.OpenDatabase(context.Background(), path, storage.KindProject,
		storage.Options{Clock: fixed, SkipBackup: true})
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return events.New(db, events.Options{Clock: fixed, Location: loc}), db
}

func emit(t *testing.T, svc *events.Service, db *sql.DB, ev events.Event) error {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := svc.Emit(ctx, tx, ev); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return nil
}

func emitNote(t *testing.T, svc *events.Service, db *sql.DB, ev events.NoteUpdatedEvent) error {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := svc.EmitNoteUpdated(ctx, tx, ev); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return nil
}

func countActivities(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM activities`).Scan(&n); err != nil {
		t.Fatalf("count activities: %v", err)
	}
	return n
}

func latestRow(t *testing.T, db *sql.DB) (payload, entityType, entityID, actor string) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT payload, entity_type, entity_id, actor FROM activities ORDER BY rowid DESC LIMIT 1`,
	).Scan(&payload, &entityType, &entityID, &actor); err != nil {
		t.Fatalf("latest activity: %v", err)
	}
	return payload, entityType, entityID, actor
}

func relationObject(entityID, dstID string) events.Object {
	return events.Object{
		"src_type": "note",
		"src_id":   entityID,
		"relation": "references",
		"dst_type": "note",
		"dst_id":   dstID,
	}
}

func TestAC05Validation(t *testing.T) {
	svc, db := newTestService(t, time.UTC)
	ctx := context.Background()
	entityID := ids.MustNew(fixed)
	base := events.Event{
		Type:       schema.EventEntityCreated,
		EntityType: schema.EntityNote,
		EntityID:   entityID,
		Summary:    "created",
		Actor:      schema.ActorUser,
	}

	cases := []struct {
		name string
		mut  func(*events.Event)
		want error
	}{
		{"event type", func(ev *events.Event) { ev.Type = schema.EventType("entity.viewed") }, errs.ErrBadEventType},
		{"actor", func(ev *events.Event) { ev.Actor = schema.Actor("system") }, errs.ErrBadActor},
		{"entity type", func(ev *events.Event) { ev.EntityType = schema.EntityType("bogus") }, errs.ErrBadEntityType},
		{"entity id", func(ev *events.Event) { ev.EntityID = "not-a-uuid" }, errs.ErrBadEntityID},
		{"note.updated via Emit", func(ev *events.Event) { ev.Type = schema.EventNoteUpdated }, errs.ErrWrongEmitPath},
	}
	for _, c := range cases {
		ev := base
		c.mut(&ev)
		err := emit(t, svc, db, ev)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: Emit = %v, want %v", c.name, err, c.want)
		}
	}
	if got := countActivities(t, db); got != 0 {
		t.Fatalf("invalid events were written: %d rows", got)
	}

	// relation.added must carry exactly the 5-key tuple.
	for _, missing := range []string{"src_type", "src_id", "relation", "dst_type", "dst_id"} {
		obj := relationObject(entityID, ids.MustNew(fixed))
		delete(obj, missing)
		ev := base
		ev.Type = schema.EventRelationAdded
		ev.New = &obj
		if err := emit(t, svc, db, ev); !errors.Is(err, errs.ErrBadPayloadShape) {
			t.Errorf("relation.added missing %q = %v, want ErrBadPayloadShape", missing, err)
		}
	}
	extra := relationObject(entityID, ids.MustNew(fixed))
	extra["extra"] = "x"
	ev := base
	ev.Type = schema.EventRelationAdded
	ev.New = &extra
	if err := emit(t, svc, db, ev); !errors.Is(err, errs.ErrBadPayloadShape) {
		t.Errorf("relation.added with extra key = %v, want ErrBadPayloadShape", err)
	}

	// A payload value that cannot be serialized is a shape error.
	bad := events.Object{"bad": make(chan int)}
	ev = base
	ev.New = &bad
	if err := emit(t, svc, db, ev); !errors.Is(err, errs.ErrBadPayloadShape) {
		t.Errorf("unserializable payload = %v, want ErrBadPayloadShape", err)
	}
	if got := countActivities(t, db); got != 0 {
		t.Fatalf("shape errors wrote rows: %d", got)
	}

	// ParseObject rejects every non-object top level.
	for _, raw := range []string{`[]`, `[1,2]`, `"draft"`, `null`, `123`, `true`, ``, `{"a":1} trailing`} {
		if _, err := events.ParseObject([]byte(raw)); !errors.Is(err, errs.ErrBadPayloadShape) {
			t.Errorf("ParseObject(%q) = %v, want ErrBadPayloadShape", raw, err)
		}
	}
	obj, err := events.ParseObject([]byte(`{"status":"draft"}`))
	if err != nil || obj == nil || (*obj)["status"] != "draft" {
		t.Fatalf("ParseObject(object) = %v, %v", obj, err)
	}

	// A successful write projects the payload into the columns.
	src := ids.MustNew(fixed)
	dst := ids.MustNew(fixed)
	rel := relationObject(src, dst)
	ev = base
	ev.Type = schema.EventRelationAdded
	ev.EntityType = schema.EntityNote
	ev.EntityID = src
	ev.New = &rel
	ev.Summary = strings.Repeat("长", 250) // also exercises summary truncation
	if err := emit(t, svc, db, ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	payloadJSON, entityType, entityID, actor := latestRow(t, db)
	if entityType != string(schema.EntityNote) || entityID != src || actor != string(schema.ActorUser) {
		t.Fatalf("columns = (%s, %s, %s)", entityType, entityID, actor)
	}
	var payload events.Payload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("payload JSON: %v", err)
	}
	if string(payload.EntityType) != entityType || payload.EntityID != entityID || string(payload.Actor) != actor {
		t.Fatalf("payload projection mismatch: %+v vs columns (%s, %s, %s)", payload, entityType, entityID, actor)
	}
	if n := utf8.RuneCountInString(payload.Summary); n != 200 {
		t.Fatalf("summary rune count = %d, want 200", n)
	}
	if payload.New == nil || len(*payload.New) != 5 {
		t.Fatalf("payload.new = %v, want the 5-key tuple", payload.New)
	}
	var mergeKey sql.NullString
	if err := db.QueryRow(`SELECT merge_key FROM activities ORDER BY rowid DESC LIMIT 1`).Scan(&mergeKey); err != nil {
		t.Fatal(err)
	}
	if mergeKey.Valid {
		t.Fatalf("non-note event carries merge_key %q", mergeKey.String)
	}

	// Emit requires a caller transaction.
	if err := svc.Emit(ctx, nil, base); err == nil {
		t.Fatal("Emit(nil tx) succeeded")
	}
	// EmitStandalone uses the same validation path.
	if err := svc.EmitStandalone(ctx, events.Event{Type: schema.EventNoteUpdated}); !errors.Is(err, errs.ErrWrongEmitPath) {
		t.Fatalf("EmitStandalone(note.updated) = %v, want ErrWrongEmitPath", err)
	}
}

func TestAC06NoteUpdatedMerge(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	svc, db := newTestService(t, loc)
	ctx := context.Background()
	noteID := ids.MustNew(fixed)

	first := time.Date(2026, 5, 6, 15, 8, 9, 0, loc) // 07:08:09Z
	if err := emitNote(t, svc, db, events.NoteUpdatedEvent{NoteID: noteID, Summary: "save 1", Actor: schema.ActorUser, At: first}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := emitNote(t, svc, db, events.NoteUpdatedEvent{NoteID: noteID, Summary: "save 2", Actor: schema.ActorAI, At: first.Add(10 * time.Minute)}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	third := first.Add(50 * time.Minute)
	if err := emitNote(t, svc, db, events.NoteUpdatedEvent{NoteID: noteID, Summary: "save 3", Actor: schema.ActorMCP, At: third}); err != nil {
		t.Fatalf("third save: %v", err)
	}

	var payloadJSON, actorColumn, createdAt string
	if err := db.QueryRow(
		`SELECT payload, actor, created_at FROM activities WHERE event_type = 'note.updated'`,
	).Scan(&payloadJSON, &actorColumn, &createdAt); err != nil {
		t.Fatalf("merged row: %v", err)
	}
	if got := countActivities(t, db); got != 1 {
		t.Fatalf("activities rows = %d, want 1", got)
	}
	if createdAt != clock.Format(first) {
		t.Fatalf("created_at = %q, want first save %q", createdAt, clock.Format(first))
	}
	var mergeKey string
	if err := db.QueryRow(`SELECT merge_key FROM activities WHERE event_type = 'note.updated'`).Scan(&mergeKey); err != nil {
		t.Fatal(err)
	}
	if want := "note.updated:" + noteID + ":2026-05-06"; mergeKey != want {
		t.Fatalf("merge_key = %q, want %q", mergeKey, want)
	}
	if actorColumn != string(schema.ActorUser) {
		t.Fatalf("actor column = %q, want the first save's actor", actorColumn)
	}
	var payload events.Payload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.SaveCount != 3 {
		t.Fatalf("save_count = %d, want 3", payload.SaveCount)
	}
	if payload.LastSavedAt != clock.Format(third) {
		t.Fatalf("last_saved_at = %q, want %q", payload.LastSavedAt, clock.Format(third))
	}
	if payload.Summary != "save 3" || payload.Actor != schema.ActorMCP {
		t.Fatalf("payload summary/actor = %q/%q", payload.Summary, payload.Actor)
	}

	// Reading a merged row must tolerate payload.actor (last save) differing
	// from the actor column (first save).
	list, err := svc.List(ctx, events.Query{EventTypes: []events.Type{schema.EventNoteUpdated}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Payload.SaveCount != 3 || list[0].Actor != schema.ActorUser || list[0].Payload.Actor != schema.ActorMCP {
		t.Fatalf("List merge = %+v", list)
	}

	// The next local day starts a new row.
	nextDay := time.Date(2026, 5, 7, 0, 10, 0, 0, loc)
	if err := emitNote(t, svc, db, events.NoteUpdatedEvent{NoteID: noteID, Summary: "save 4", Actor: schema.ActorUser, At: nextDay}); err != nil {
		t.Fatalf("next-day save: %v", err)
	}
	if got := countActivities(t, db); got != 2 {
		t.Fatalf("activities rows after day change = %d, want 2", got)
	}
	// A different note is always a separate row.
	otherNote := ids.MustNew(fixed)
	if err := emitNote(t, svc, db, events.NoteUpdatedEvent{NoteID: otherNote, Summary: "other", Actor: schema.ActorUser, At: first}); err != nil {
		t.Fatalf("other note: %v", err)
	}
	if got := countActivities(t, db); got != 3 {
		t.Fatalf("activities rows after other note = %d, want 3", got)
	}
	// Invalid actors/ids are rejected through this path too.
	if err := emitNote(t, svc, db, events.NoteUpdatedEvent{NoteID: noteID, Actor: schema.Actor("robot")}); !errors.Is(err, errs.ErrBadActor) {
		t.Fatalf("bad actor = %v, want ErrBadActor", err)
	}
	if err := emitNote(t, svc, db, events.NoteUpdatedEvent{NoteID: "nope", Actor: schema.ActorUser}); !errors.Is(err, errs.ErrBadEntityID) {
		t.Fatalf("bad note id = %v, want ErrBadEntityID", err)
	}
}

func TestAC24NewEventTypes(t *testing.T) {
	svc, db := newTestService(t, time.UTC)
	ctx := context.Background()
	entityID := ids.MustNew(fixed)

	oldObj := events.Object{"name": "A"}
	newObj := events.Object{"name": "B"}
	if err := emit(t, svc, db, events.Event{
		Type:       schema.EventEntityUpdated,
		EntityType: schema.EntityProject,
		EntityID:   entityID,
		Old:        &oldObj,
		New:        &newObj,
		Summary:    "rename",
		Actor:      schema.ActorUser,
	}); err != nil {
		t.Fatalf("entity.updated: %v", err)
	}
	list, err := svc.List(ctx, events.Query{
		EventTypes: []events.Type{schema.EventEntityUpdated},
		EntityID:   entityID,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("entity.updated rows = %d, want 1", len(list))
	}
	act := list[0]
	if act.Payload.Old == nil || (*act.Payload.Old)["name"] != "A" {
		t.Fatalf("payload.old = %v", act.Payload.Old)
	}
	if act.Payload.New == nil || (*act.Payload.New)["name"] != "B" {
		t.Fatalf("payload.new = %v", act.Payload.New)
	}

	// entity.updated without old and new is invalid.
	if err := emit(t, svc, db, events.Event{
		Type: schema.EventEntityUpdated, EntityType: schema.EntityProject, EntityID: entityID, Actor: schema.ActorUser,
	}); !errors.Is(err, errs.ErrBadPayloadShape) {
		t.Fatalf("entity.updated without old/new = %v", err)
	}

	// entity.purged must not carry old/new.
	purgedOld := events.Object{"name": "A"}
	if err := emit(t, svc, db, events.Event{
		Type: schema.EventEntityPurged, EntityType: schema.EntityNote, EntityID: entityID,
		Old: &purgedOld, Summary: "purged", Actor: schema.ActorUser,
	}); !errors.Is(err, errs.ErrBadPayloadShape) {
		t.Fatalf("entity.purged with old = %v, want ErrBadPayloadShape", err)
	}
	if err := emit(t, svc, db, events.Event{
		Type: schema.EventEntityPurged, EntityType: schema.EntityNote, EntityID: entityID,
		Summary: "purged", Actor: schema.ActorUser,
	}); err != nil {
		t.Fatalf("entity.purged: %v", err)
	}

	// entity.relocated requires path on both sides.
	relocOld := events.Object{"path": "/old"}
	if err := emit(t, svc, db, events.Event{
		Type: schema.EventEntityRelocated, EntityType: schema.EntityExperiment, EntityID: entityID,
		Old: &relocOld, Summary: "relocate", Actor: schema.ActorUser,
	}); !errors.Is(err, errs.ErrBadPayloadShape) {
		t.Fatalf("entity.relocated missing new = %v", err)
	}
	relocNoPath := events.Object{"other": "x"}
	relocNew := events.Object{"path": "/new"}
	if err := emit(t, svc, db, events.Event{
		Type: schema.EventEntityRelocated, EntityType: schema.EntityExperiment, EntityID: entityID,
		Old: &relocNoPath, New: &relocNew, Summary: "relocate", Actor: schema.ActorUser,
	}); !errors.Is(err, errs.ErrBadPayloadShape) {
		t.Fatalf("entity.relocated without path = %v", err)
	}
	if err := emit(t, svc, db, events.Event{
		Type: schema.EventEntityRelocated, EntityType: schema.EntityExperiment, EntityID: entityID,
		Old: &relocOld, New: &relocNew, Summary: "relocate", Actor: schema.ActorUser,
	}); err != nil {
		t.Fatalf("entity.relocated: %v", err)
	}

	// status.changed accepts an object with status (old only is enough).
	statusOld := events.Object{"status": "draft"}
	if err := emit(t, svc, db, events.Event{
		Type: schema.EventStatusChanged, EntityType: schema.EntityWeeklyReport, EntityID: entityID,
		Old: &statusOld, Summary: "status", Actor: schema.ActorUser,
	}); err != nil {
		t.Fatalf("status.changed: %v", err)
	}
	noStatus := events.Object{"column": "todo"}
	if err := emit(t, svc, db, events.Event{
		Type: schema.EventStatusChanged, EntityType: schema.EntityTask, EntityID: entityID,
		Old: &noStatus, Summary: "status", Actor: schema.ActorUser,
	}); !errors.Is(err, errs.ErrBadPayloadShape) {
		t.Fatalf("status.changed without status = %v", err)
	}
	if _, err := events.ParseObject([]byte(`"draft"`)); !errors.Is(err, errs.ErrBadPayloadShape) {
		t.Fatalf("status.changed raw string = %v, want ErrBadPayloadShape", err)
	}
}

func TestAC14ConcurrentEmitStandalone(t *testing.T) {
	svc, db := newTestService(t, time.UTC)
	const goroutines = 2
	const perGoroutine = 100
	errCh := make(chan error, goroutines*perGoroutine)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			ctx := context.WithValue(context.Background(), goroutineKey{}, g)
			for range perGoroutine {
				ev := events.Event{
					Type:       schema.EventEntityCreated,
					EntityType: schema.EntityTask,
					EntityID:   ids.MustNew(fixed),
					Summary:    fmt.Sprintf("g%d", g),
					Actor:      schema.ActorUser,
					At:         fixed.Now(),
				}
				if err := svc.EmitStandalone(ctx, ev); err != nil {
					errCh <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("EmitStandalone: %v", err)
	}
	if got := countActivities(t, db); got != goroutines*perGoroutine {
		t.Fatalf("activities rows = %d, want %d", got, goroutines*perGoroutine)
	}
}

type goroutineKey struct{}

func TestAC15bMergeLatency(t *testing.T) {
	ctx := context.Background()
	fx, err := foundationtest.New(t.TempDir(), foundationtest.Options{Clock: fixed})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	defer fx.Close()
	_, handle, err := fx.NewProject(ctx, "perf", "", "")
	if err != nil {
		t.Fatalf("NewProject: %v", err)
	}
	const seeded = 50_000
	if err := fx.SeedActivities(ctx, handle, seeded); err != nil {
		t.Fatalf("SeedActivities: %v", err)
	}
	svc := events.New(handle.DB(), events.Options{Clock: fixed})
	noteID := ids.MustNew(fixed)
	merge := func() time.Duration {
		start := time.Now()
		if err := handle.InTx(ctx, func(tx *sql.Tx) error {
			return svc.EmitNoteUpdated(ctx, tx, events.NoteUpdatedEvent{
				NoteID: noteID, Summary: "merge", Actor: schema.ActorUser, At: fixed.Now(),
			})
		}); err != nil {
			t.Fatalf("merge: %v", err)
		}
		return time.Since(start)
	}
	first := merge()
	warm := merge()
	t.Logf("AC-15(b): note.updated merge with %d seeded activities: first %s, warm %s", seeded, first, warm)
}

func TestReaderFiltersAndPagination(t *testing.T) {
	svc, db := newTestService(t, time.UTC)
	ctx := context.Background()
	entityA := ids.MustNew(fixed)
	entityB := ids.MustNew(fixed)
	t0 := fixed.Now()

	mustEmit := func(ev events.Event) {
		t.Helper()
		if err := emit(t, svc, db, ev); err != nil {
			t.Fatalf("Emit %s: %v", ev.Type, err)
		}
	}
	mustEmit(events.Event{Type: schema.EventEntityCreated, EntityType: schema.EntityNote, EntityID: entityA, Summary: "a", Actor: schema.ActorUser, At: t0})
	statusOld := events.Object{"status": "New"}
	statusNew := events.Object{"status": "Researching"}
	mustEmit(events.Event{Type: schema.EventStatusChanged, EntityType: schema.EntityIdea, EntityID: entityB,
		Old: &statusOld, New: &statusNew, Summary: "b", Actor: schema.ActorAI, At: t0.Add(time.Minute)})
	mustEmit(events.Event{Type: schema.EventEntityCreated, EntityType: schema.EntityNote, EntityID: entityA, Summary: "c", Actor: schema.ActorMCP, At: t0.Add(2 * time.Minute)})

	cases := []struct {
		name string
		q    events.Query
		want int
	}{
		{"all", events.Query{}, 3},
		{"event type", events.Query{EventTypes: []events.Type{schema.EventEntityCreated}}, 2},
		{"entity id", events.Query{EntityID: entityA}, 2},
		{"entity types", events.Query{EntityTypes: []events.EntityType{schema.EntityIdea}}, 1},
		{"actors", events.Query{Actors: []events.Actor{schema.ActorUser, schema.ActorMCP}}, 2},
		{"since inclusive", events.Query{Since: new(t0.Add(time.Minute))}, 2},
		{"until inclusive", events.Query{Until: new(t0.Add(time.Minute))}, 2},
		{"range", events.Query{Since: new(t0.Add(time.Minute)), Until: new(t0.Add(time.Minute))}, 1},
		{"combined", events.Query{EntityTypes: []events.EntityType{schema.EntityNote}, Actors: []events.Actor{schema.ActorMCP}}, 1},
	}
	for _, c := range cases {
		got, err := svc.List(ctx, c.q)
		if err != nil {
			t.Fatalf("%s: List: %v", c.name, err)
		}
		if len(got) != c.want {
			t.Errorf("%s: List returned %d rows, want %d", c.name, len(got), c.want)
		}
		count, err := svc.Count(ctx, c.q)
		if err != nil {
			t.Fatalf("%s: Count: %v", c.name, err)
		}
		if count != c.want {
			t.Errorf("%s: Count = %d, want %d", c.name, count, c.want)
		}
	}

	// Newest first with a stable id tiebreak; pagination walks the same order.
	all, err := svc.List(ctx, events.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if all[0].Payload.Summary != "c" || all[2].Payload.Summary != "a" {
		t.Fatalf("order = %q ... %q", all[0].Payload.Summary, all[2].Payload.Summary)
	}
	page, err := svc.List(ctx, events.Query{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != all[1].ID {
		t.Fatalf("offset page = %+v, want %s", page, all[1].ID)
	}

	// Aggregations cover the whole filtered set.
	day, err := svc.Aggregate(ctx, events.AggregateQuery{Bucket: events.BucketDay})
	if err != nil {
		t.Fatalf("Aggregate day: %v", err)
	}
	if len(day) != 1 || day[0].Key != "2026-05-06" || day[0].Count != 3 {
		t.Fatalf("day buckets = %+v", day)
	}
	byEntity, err := svc.Aggregate(ctx, events.AggregateQuery{Bucket: events.BucketEntityType})
	if err != nil {
		t.Fatalf("Aggregate entity: %v", err)
	}
	if len(byEntity) != 2 || byEntity[0].Key != string(schema.EntityNote) || byEntity[0].Count != 2 {
		t.Fatalf("entity buckets = %+v", byEntity)
	}
	byType, err := svc.Aggregate(ctx, events.AggregateQuery{Bucket: events.BucketEventType})
	if err != nil {
		t.Fatalf("Aggregate type: %v", err)
	}
	if len(byType) != 2 || byType[0].Key != string(schema.EventEntityCreated) || byType[0].Count != 2 {
		t.Fatalf("type buckets = %+v", byType)
	}
	byActor, err := svc.Aggregate(ctx, events.AggregateQuery{Bucket: events.BucketActor})
	if err != nil {
		t.Fatalf("Aggregate actor: %v", err)
	}
	if len(byActor) != 3 {
		t.Fatalf("actor buckets = %+v", byActor)
	}
	for _, b := range byActor {
		if b.Count != 1 {
			t.Fatalf("actor bucket %+v", b)
		}
	}
	filtered, err := svc.Aggregate(ctx, events.AggregateQuery{
		Query:  events.Query{EventTypes: []events.Type{schema.EventEntityCreated}},
		Bucket: events.BucketActor,
	})
	if err != nil || len(filtered) != 2 {
		t.Fatalf("filtered aggregate = %+v, %v", filtered, err)
	}
	if _, err := svc.Aggregate(ctx, events.AggregateQuery{Bucket: events.BucketKind("hour")}); err == nil {
		t.Fatal("Aggregate accepted an unknown bucket")
	}
}

func TestAggregateDayUsesConfiguredLocation(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	svc, db := newTestService(t, loc)
	ctx := context.Background()
	entity := ids.MustNew(fixed)
	for _, at := range []time.Time{
		time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC),  // 2026-05-06 18:00 local
		time.Date(2026, 5, 6, 23, 30, 0, 0, time.UTC), // 2026-05-07 07:30 local
	} {
		if err := emit(t, svc, db, events.Event{
			Type: schema.EventEntityCreated, EntityType: schema.EntityNote, EntityID: entity,
			Summary: "x", Actor: schema.ActorUser, At: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	buckets, err := svc.Aggregate(ctx, events.AggregateQuery{Bucket: events.BucketDay})
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 || buckets[0].Key != "2026-05-06" || buckets[1].Key != "2026-05-07" {
		t.Fatalf("local day buckets = %+v", buckets)
	}
}

func TestListLimitDefaultAndCap(t *testing.T) {
	svc, db := newTestService(t, time.UTC)
	ctx := context.Background()
	// Seed 510 rows directly to keep the test fast.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO activities(id, event_type, entity_type, entity_id, payload, actor, created_at)
		 VALUES (?, 'entity.created', 'task', ?, ?, 'user', ?)`)
	if err != nil {
		t.Fatal(err)
	}
	at := clock.Format(fixed.Now())
	for range 510 {
		entityID := ids.MustNew(fixed)
		payload := fmt.Sprintf(`{"entity_type":"task","entity_id":"%s","summary":"seed","actor":"user"}`, entityID)
		if _, err := stmt.ExecContext(ctx, ids.MustNew(fixed), entityID, payload, at); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got, err := svc.List(ctx, events.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 50 {
		t.Fatalf("default limit returned %d rows, want 50", len(got))
	}
	got, err = svc.List(ctx, events.Query{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 500 {
		t.Fatalf("capped limit returned %d rows, want 500", len(got))
	}
}

func TestPayloadColumnConsistency(t *testing.T) {
	svc, db := newTestService(t, time.UTC)
	ctx := context.Background()
	entity := ids.MustNew(fixed)
	if err := emit(t, svc, db, events.Event{
		Type: schema.EventEntityCreated, EntityType: schema.EntityTask, EntityID: entity,
		Summary: "x", Actor: schema.ActorUser,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE activities SET payload = json_set(payload, '$.entity_id', 'mismatch')`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.List(ctx, events.Query{}); err == nil {
		t.Fatal("List accepted a payload/column entity mismatch")
	}
	if _, err := db.Exec(`UPDATE activities SET payload = json_set(payload, '$.entity_id', ?)`, entity); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE activities SET actor = 'mcp'`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.List(ctx, events.Query{}); err == nil {
		t.Fatal("List accepted a payload/column actor mismatch")
	}
	// note.updated merged rows are the one documented exception (see AC-06).
}

func ptr[T any](v T) *T { return &v }
