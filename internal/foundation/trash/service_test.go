package trash

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/paths"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/storage"
)

func setupTrash(t *testing.T, c *clock.Fixed) (*Service, *sql.DB, confirm.Service, paths.Paths, *events.Service, string) {
	t.Helper()
	projectID := ids.MustNew(c)
	root := filepath.Join(t.TempDir(), "ws")
	projectDir := filepath.Join(root, "projects", projectID)
	for _, sub := range []string{"notes", "attachments"} {
		if err := os.MkdirAll(filepath.Join(projectDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p, err := paths.New(root)
	if err != nil {
		t.Fatalf("paths.New: %v", err)
	}
	db, err := storage.OpenDatabase(context.Background(), filepath.Join(projectDir, "project.db"),
		storage.KindProject, storage.Options{Clock: c, SkipBackup: true})
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	writer := events.New(db, events.Options{Clock: c})
	conf := confirm.New(confirm.Options{Clock: c})
	return New(projectID, db, p, writer, conf, c), db, conf, p, writer, projectID
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

func nowOf(c *clock.Fixed) string { return clock.Format(c.Now()) }

func insertNote(t *testing.T, db *sql.DB, c *clock.Fixed, id string) {
	t.Helper()
	now := nowOf(c)
	if _, err := db.Exec(
		`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at, file_hash)
		 VALUES (?, 'note title', ?, 'scratchpad', ?, ?, NULL)`,
		id, "projects/p/notes/"+id+".md", now, now); err != nil {
		t.Fatalf("insert note: %v", err)
	}
}

func insertIdea(t *testing.T, db *sql.DB, c *clock.Fixed, id string) {
	t.Helper()
	now := nowOf(c)
	if _, err := db.Exec(`INSERT INTO ideas(id, title, created_at, updated_at) VALUES (?, 'idea', ?, ?)`, id, now, now); err != nil {
		t.Fatalf("insert idea: %v", err)
	}
}

func insertEvidence(t *testing.T, db *sql.DB, c *clock.Fixed, id, ideaID string) {
	t.Helper()
	now := nowOf(c)
	if _, err := db.Exec(
		`INSERT INTO evidence(id, idea_id, direction, source_type, source_id, created_at, updated_at)
		 VALUES (?, ?, 'support', 'paper', 'p1', ?, ?)`, id, ideaID, now, now); err != nil {
		t.Fatalf("insert evidence: %v", err)
	}
}

func insertExperiment(t *testing.T, db *sql.DB, c *clock.Fixed, id string) {
	t.Helper()
	now := nowOf(c)
	if _, err := db.Exec(`INSERT INTO experiments(id, title, created_at, updated_at) VALUES (?, 'exp', ?, ?)`, id, now, now); err != nil {
		t.Fatalf("insert experiment: %v", err)
	}
}

func insertRun(t *testing.T, db *sql.DB, c *clock.Fixed, id, experimentID string) {
	t.Helper()
	now := nowOf(c)
	if _, err := db.Exec(
		`INSERT INTO runs(id, experiment_id, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		id, experimentID, now, now); err != nil {
		t.Fatalf("insert run: %v", err)
	}
}

func writeNoteFile(t *testing.T, p paths.Paths, projectID, noteID string) {
	t.Helper()
	abs, err := p.Abs(p.NoteFile(projectID, noteID))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeAttachments(t *testing.T, p paths.Paths, projectID, noteID string) {
	t.Helper()
	abs, err := p.Abs(p.AttachmentsDir(projectID, noteID))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(abs, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abs, "img.png"), []byte("img"), 0o644); err != nil {
		t.Fatal(err)
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

func TestAC07SoftDeleteRestore(t *testing.T) {
	ctx := context.Background()
	c := &clock.Fixed{T: time.Date(2026, 9, 10, 11, 12, 13, 0, time.UTC)}
	svc, db, _, p, writer, projectID := setupTrash(t, c)
	noteID := ids.MustNew(c)
	insertNote(t, db, c, noteID)
	writeNoteFile(t, p, projectID, noteID)
	writeAttachments(t, p, projectID, noteID)

	inTx(t, db, func(tx *sql.Tx) error {
		return svc.SoftDelete(ctx, tx, Ref{Type: schema.EntityNote, ID: noteID}, schema.ActorUser)
	})
	var deletedAt sql.NullString
	if err := db.QueryRow(`SELECT deleted_at FROM notes WHERE id = ?`, noteID).Scan(&deletedAt); err != nil {
		t.Fatal(err)
	}
	if !deletedAt.Valid {
		t.Fatal("deleted_at was not set")
	}
	absNote, _ := p.Abs(p.NoteFile(projectID, noteID))
	if _, err := os.Stat(absNote); err != nil {
		t.Fatalf("note file was deleted by SoftDelete: %v", err)
	}
	absAtt, _ := p.Abs(p.AttachmentsDir(projectID, noteID))
	if _, err := os.Stat(absAtt); err != nil {
		t.Fatalf("attachments were deleted by SoftDelete: %v", err)
	}

	items, err := svc.List(ctx, ListQuery{})
	if err != nil || len(items) != 1 || items[0].ID != noteID || items[0].EntityType != schema.EntityNote {
		t.Fatalf("List = %+v, %v", items, err)
	}
	if n, err := svc.Count(ctx); err != nil || n != 1 {
		t.Fatalf("Count = %d, %v", n, err)
	}
	relSvc := relations.New(db, nil, writer, c)
	if state, err := relSvc.Resolve(ctx, schema.EntityNote, noteID); err != nil || state != relations.TargetSoftDeleted {
		t.Fatalf("Resolve(soft-deleted note) = %v, %v", state, err)
	}
	if n, err := writer.Count(ctx, events.Query{EventTypes: []events.Type{schema.EventEntityDeleted}}); err != nil || n != 1 {
		t.Fatalf("entity.deleted events = %d, %v", n, err)
	}

	// Idempotent second delete.
	inTx(t, db, func(tx *sql.Tx) error {
		return svc.SoftDelete(ctx, tx, Ref{Type: schema.EntityNote, ID: noteID}, schema.ActorUser)
	})
	if n, _ := writer.Count(ctx, events.Query{EventTypes: []events.Type{schema.EventEntityDeleted}}); n != 1 {
		t.Fatalf("second SoftDelete emitted an event (%d)", n)
	}

	// Restore and idempotent second restore.
	inTx(t, db, func(tx *sql.Tx) error {
		return svc.Restore(ctx, tx, Ref{Type: schema.EntityNote, ID: noteID}, schema.ActorUser)
	})
	if err := db.QueryRow(`SELECT deleted_at FROM notes WHERE id = ?`, noteID).Scan(&deletedAt); err != nil {
		t.Fatal(err)
	}
	if deletedAt.Valid {
		t.Fatal("deleted_at was not cleared")
	}
	if n, _ := writer.Count(ctx, events.Query{EventTypes: []events.Type{schema.EventEntityRestored}}); n != 1 {
		t.Fatalf("entity.restored events = %d, want 1", n)
	}
	inTx(t, db, func(tx *sql.Tx) error {
		return svc.Restore(ctx, tx, Ref{Type: schema.EntityNote, ID: noteID}, schema.ActorUser)
	})
	if n, _ := writer.Count(ctx, events.Query{EventTypes: []events.Type{schema.EventEntityRestored}}); n != 1 {
		t.Fatalf("second Restore emitted an event (%d)", n)
	}
	if items, err := svc.List(ctx, ListQuery{}); err != nil || len(items) != 0 {
		t.Fatalf("List after restore = %+v, %v", items, err)
	}

	// db_fields are soft-deletable but intentionally absent from v_trash.
	now := nowOf(c)
	if _, err := db.Exec(`INSERT INTO research_databases(id, name, created_at, updated_at) VALUES ('rdb', 'db', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	fieldID := ids.MustNew(c)
	if _, err := db.Exec(`INSERT INTO db_fields(id, database_id, name, type, created_at, updated_at) VALUES (?, 'rdb', 'f', 'text', ?, ?)`, fieldID, now, now); err != nil {
		t.Fatal(err)
	}
	inTx(t, db, func(tx *sql.Tx) error {
		return svc.SoftDelete(ctx, tx, Ref{Type: schema.EntityDBField, ID: fieldID}, schema.ActorUser)
	})
	if items, err := svc.List(ctx, ListQuery{}); err != nil || len(items) != 0 {
		t.Fatalf("v_trash leaked db_fields: %+v, %v", items, err)
	}
}

func TestAC08Purge(t *testing.T) {
	ctx := context.Background()
	c := &clock.Fixed{T: time.Date(2026, 10, 11, 12, 13, 14, 0, time.UTC)}
	svc, db, conf, p, writer, projectID := setupTrash(t, c)

	noteID := ids.MustNew(c)
	otherNote := ids.MustNew(c)
	ideaID := ids.MustNew(c)
	evidenceID := ids.MustNew(c)
	experimentID := ids.MustNew(c)
	runID := ids.MustNew(c)
	insertNote(t, db, c, noteID)
	insertNote(t, db, c, otherNote)
	insertIdea(t, db, c, ideaID)
	insertEvidence(t, db, c, evidenceID, ideaID)
	insertExperiment(t, db, c, experimentID)
	insertRun(t, db, c, runID, experimentID)
	writeNoteFile(t, p, projectID, noteID)
	writeAttachments(t, p, projectID, noteID)

	relSvc := relations.New(db, nil, writer, c)
	inTx(t, db, func(tx *sql.Tx) error {
		if _, err := relSvc.Link(ctx, tx, relations.Link{
			SrcType: schema.EntityNote, SrcID: noteID, Relation: schema.RelationReferences,
			DstType: schema.EntityNote, DstID: otherNote,
		}, schema.ActorUser); err != nil {
			return err
		}
		_, err := relSvc.Link(ctx, tx, relations.Link{
			SrcType: schema.EntityNote, SrcID: otherNote, Relation: schema.RelationReferences,
			DstType: schema.EntityNote, DstID: noteID,
		}, schema.ActorUser)
		return err
	})
	if err := writer.EmitStandalone(ctx, events.Event{
		Type: schema.EventEntityCreated, EntityType: schema.EntityNote, EntityID: otherNote,
		Summary: "pre-existing", Actor: schema.ActorUser, At: c.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	refs := []Ref{
		{Type: schema.EntityIdea, ID: ideaID},
		{Type: schema.EntityNote, ID: noteID},
		{Type: schema.EntityExperiment, ID: experimentID},
	}
	rowsBefore := count(t, db, `SELECT count(*) FROM notes`) + count(t, db, `SELECT count(*) FROM ideas`) +
		count(t, db, `SELECT count(*) FROM entity_relations`)

	// No token: nothing changes.
	if _, err := svc.Purge(ctx, PurgeRequest{Refs: refs, Actor: schema.ActorUser}); !errors.Is(err, errs.ErrUserConfirmationRequired) {
		t.Fatalf("Purge without token = %v, want ErrUserConfirmationRequired", err)
	}
	if got := count(t, db, `SELECT count(*) FROM notes`) + count(t, db, `SELECT count(*) FROM ideas`) +
		count(t, db, `SELECT count(*) FROM entity_relations`); got != rowsBefore {
		t.Fatalf("rows changed without a token: %d -> %d", rowsBefore, got)
	}

	scope := scopeFor(projectID, confirm.KindPurgeEntity, refs)
	token, err := conf.Issue(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	c.Advance(6 * time.Minute)
	if _, err := svc.Purge(ctx, PurgeRequest{Refs: refs, Confirm: token, Actor: schema.ActorUser}); !errors.Is(err, errs.ErrUserConfirmationRequired) {
		t.Fatalf("Purge with expired token = %v, want ErrUserConfirmationRequired", err)
	}
	if got := count(t, db, `SELECT count(*) FROM notes`); got != 2 {
		t.Fatalf("rows changed with an expired token: notes=%d", got)
	}

	token, err = conf.Issue(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Purge(ctx, PurgeRequest{Refs: refs, Confirm: token, Actor: schema.ActorUser})
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if res.Purged != 3 || res.PurgedEvents != 3 {
		t.Fatalf("PurgeResult = %+v, want Purged=PurgedEvents=3", res)
	}
	if res.ClearedLinks != 2 {
		t.Fatalf("ClearedLinks = %d, want 2", res.ClearedLinks)
	}
	if res.RemovedFiles < 2 {
		t.Fatalf("RemovedFiles = %d, want at least 2", res.RemovedFiles)
	}
	if res.RemovedDirs < 1 {
		t.Fatalf("RemovedDirs = %d, want at least 1", res.RemovedDirs)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}

	if got := count(t, db, `SELECT count(*) FROM ideas`); got != 0 {
		t.Fatalf("ideas = %d, want 0", got)
	}
	if got := count(t, db, `SELECT count(*) FROM evidence`); got != 0 {
		t.Fatalf("evidence = %d, want 0 (idea cascade)", got)
	}
	if got := count(t, db, `SELECT count(*) FROM experiments`); got != 0 {
		t.Fatalf("experiments = %d, want 0", got)
	}
	if got := count(t, db, `SELECT count(*) FROM runs`); got != 0 {
		t.Fatalf("runs = %d, want 0 (experiment cascade)", got)
	}
	if got := count(t, db, `SELECT count(*) FROM notes`); got != 1 {
		t.Fatalf("notes = %d, want 1 (other note kept)", got)
	}
	if got := count(t, db, `SELECT count(*) FROM entity_relations`); got != 0 {
		t.Fatalf("entity_relations = %d, want 0", got)
	}
	absNote, _ := p.Abs(p.NoteFile(projectID, noteID))
	if _, err := os.Stat(absNote); !os.IsNotExist(err) {
		t.Fatalf("note file still exists: %v", err)
	}
	absAtt, _ := p.Abs(p.AttachmentsDir(projectID, noteID))
	if _, err := os.Stat(absAtt); !os.IsNotExist(err) {
		t.Fatalf("attachments still exist: %v", err)
	}
	if n, _ := writer.Count(ctx, events.Query{EventTypes: []events.Type{schema.EventEntityPurged}}); n != 3 {
		t.Fatalf("entity.purged events = %d, want 3", n)
	}
	if n, _ := writer.Count(ctx, events.Query{EventTypes: []events.Type{schema.EventRelationRemoved}}); n != 2 {
		t.Fatalf("relation.removed events = %d, want 2", n)
	}
	if n, _ := writer.Count(ctx, events.Query{EventTypes: []events.Type{schema.EventEntityCreated}}); n != 1 {
		t.Fatalf("pre-existing activity was lost (entity.created=%d)", n)
	}

	// The token is one-time.
	if _, err := svc.Purge(ctx, PurgeRequest{Refs: refs, Confirm: token, Actor: schema.ActorUser}); !errors.Is(err, errs.ErrUserConfirmationRequired) {
		t.Fatalf("reused token = %v, want ErrUserConfirmationRequired", err)
	}
}

func TestEmptyTrash(t *testing.T) {
	ctx := context.Background()
	c := &clock.Fixed{T: time.Date(2026, 11, 12, 13, 14, 15, 0, time.UTC)}
	svc, db, conf, p, _, projectID := setupTrash(t, c)
	first := ids.MustNew(c)
	second := ids.MustNew(c)
	for _, id := range []string{first, second} {
		insertNote(t, db, c, id)
		writeNoteFile(t, p, projectID, id)
		inTx(t, db, func(tx *sql.Tx) error {
			return svc.SoftDelete(ctx, tx, Ref{Type: schema.EntityNote, ID: id}, schema.ActorUser)
		})
	}
	items, err := svc.List(ctx, ListQuery{})
	if err != nil || len(items) != 2 {
		t.Fatalf("List = %+v, %v", items, err)
	}
	var refs []Ref
	for _, item := range items {
		refs = append(refs, Ref{Type: item.EntityType, ID: item.ID})
	}
	token, err := conf.Issue(ctx, scopeFor(projectID, confirm.KindEmptyTrash, refs))
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Empty(ctx, EmptyRequest{Confirm: token, Actor: schema.ActorUser})
	if err != nil {
		t.Fatalf("Empty: %v", err)
	}
	if res.Purged != 2 || res.PurgedEvents != 2 {
		t.Fatalf("Empty result = %+v", res)
	}
	if n, err := svc.Count(ctx); err != nil || n != 0 {
		t.Fatalf("Count after Empty = %d, %v", n, err)
	}
	for _, id := range []string{first, second} {
		abs, _ := p.Abs(p.NoteFile(projectID, id))
		if _, err := os.Stat(abs); !os.IsNotExist(err) {
			t.Fatalf("note %s file still exists", id)
		}
	}
	// A stale token cannot empty again.
	if _, err := svc.Empty(ctx, EmptyRequest{Confirm: token, Actor: schema.ActorUser}); !errors.Is(err, errs.ErrUserConfirmationRequired) {
		t.Fatalf("reused empty token = %v", err)
	}
}

func scopeFor(projectID string, kind confirm.Kind, refs []Ref) confirm.Scope {
	types := make([]string, 0, len(refs))
	idsOut := make([]string, 0, len(refs))
	for _, ref := range refs {
		types = append(types, string(ref.Type))
		idsOut = append(idsOut, ref.ID)
	}
	return confirm.Scope{Kind: kind, ProjectID: projectID, EntityTypes: types, EntityIDs: idsOut}
}

func TestListFiltersAndPaging(t *testing.T) {
	ctx := context.Background()
	c := &clock.Fixed{T: time.Date(2026, 12, 13, 14, 15, 16, 0, time.UTC)}
	svc, db, _, _, _, _ := setupTrash(t, c)
	noteA := ids.MustNew(c)
	noteB := ids.MustNew(c)
	idea := ids.MustNew(c)
	insertNote(t, db, c, noteA)
	insertNote(t, db, c, noteB)
	insertIdea(t, db, c, idea)

	delete := func(ref Ref) {
		t.Helper()
		inTx(t, db, func(tx *sql.Tx) error {
			return svc.SoftDelete(ctx, tx, ref, schema.ActorUser)
		})
		c.Advance(time.Second)
	}
	delete(Ref{Type: schema.EntityNote, ID: noteA})
	delete(Ref{Type: schema.EntityNote, ID: noteB})
	delete(Ref{Type: schema.EntityIdea, ID: idea})

	notes, err := svc.List(ctx, ListQuery{EntityTypes: []schema.EntityType{schema.EntityNote}})
	if err != nil || len(notes) != 2 {
		t.Fatalf("List(notes) = %+v, %v", notes, err)
	}
	ideas, err := svc.List(ctx, ListQuery{EntityTypes: []schema.EntityType{schema.EntityIdea}})
	if err != nil || len(ideas) != 1 || ideas[0].ID != idea {
		t.Fatalf("List(ideas) = %+v, %v", ideas, err)
	}
	all, err := svc.List(ctx, ListQuery{})
	if err != nil || len(all) != 3 {
		t.Fatalf("List(all) = %+v, %v", all, err)
	}
	if all[0].ID != idea || all[2].ID != noteA {
		t.Fatalf("trash order = %v, %v, %v; want idea first, noteA last", all[0].ID, all[1].ID, all[2].ID)
	}
	page, err := svc.List(ctx, ListQuery{Limit: 1, Offset: 1})
	if err != nil || len(page) != 1 || page[0].ID != noteB {
		t.Fatalf("page = %+v, %v", page, err)
	}
	if n, err := svc.Count(ctx); err != nil || n != 3 {
		t.Fatalf("Count = %d, %v", n, err)
	}
}
