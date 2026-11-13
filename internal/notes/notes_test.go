package notes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/indexhook"
	"researchos/internal/foundation/notefile"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
	"researchos/internal/notes/migrations"
)

// coreIndexer records the post-commit index hook calls and can fail on demand.
type coreIndexer struct {
	mu    sync.Mutex
	seen  []events.Activity
	fails bool
}

func (i *coreIndexer) OnEntityChanged(_ context.Context, ev events.Activity) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.seen = append(i.seen, ev)
	if i.fails {
		return fmt.Errorf("coreIndexer: boom")
	}
	return nil
}

func (i *coreIndexer) Rebuild(context.Context, indexhook.Scope) error { return nil }

func (i *coreIndexer) calls() []events.Activity {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]events.Activity(nil), i.seen...)
}

// coreFixture wires the notes services over a migrated fixture workspace.
type coreFixture struct {
	t       *testing.T
	ctx     context.Context
	fx      *foundationtest.Fixture
	handle  *workspace.ProjectHandle
	notes   *noteService
	rels    *relationService
	recon   *reconcileService
	indexer *coreIndexer
	clock   *clock.Fixed
}

func coreNewFixture(t *testing.T) *coreFixture {
	t.Helper()
	fx, err := foundationtest.NewWithProject(t.TempDir(), foundationtest.Options{
		Migrations: migrations.MustAll(),
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() { _ = fx.Close() })
	idx := &coreIndexer{}
	return &coreFixture{
		t:       t,
		ctx:     context.Background(),
		fx:      fx,
		handle:  fx.Project,
		notes:   NewNoteService(fx.Workspace, fx.ProjectID, ServiceOptions{Clock: fx.Clock, Indexer: idx}),
		rels:    NewRelationService(fx.Workspace, fx.ProjectID, ServiceOptions{Clock: fx.Clock}),
		recon:   NewReconcileService(fx.Workspace, fx.ProjectID, ServiceOptions{Clock: fx.Clock}),
		indexer: idx,
		clock:   fx.Clock,
	}
}

// create makes a scratchpad note and fails the test on error.
func (f *coreFixture) create(title, body string) *Note {
	f.t.Helper()
	return f.createKind(KindScratchpad, title, body)
}

func (f *coreFixture) createKind(kind NoteKind, title, body string) *Note {
	f.t.Helper()
	note, err := f.notes.Create(f.ctx, CreateNoteInput{Kind: kind, Title: title, Body: body})
	if err != nil {
		f.t.Fatalf("create %q: %v", title, err)
	}
	return note
}

func (f *coreFixture) abs(rel string) string {
	f.t.Helper()
	abs, err := f.fx.Workspace.Paths().Abs(rel)
	if err != nil {
		f.t.Fatalf("abs %s: %v", rel, err)
	}
	return abs
}

func (f *coreFixture) read(rel string) string {
	f.t.Helper()
	body, err := os.ReadFile(f.abs(rel))
	if err != nil {
		f.t.Fatalf("read %s: %v", rel, err)
	}
	return string(body)
}

func (f *coreFixture) write(rel, content string) {
	f.t.Helper()
	abs := f.abs(rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		f.t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		f.t.Fatalf("write %s: %v", rel, err)
	}
}

func (f *coreFixture) loadBody(noteID string) *Body {
	f.t.Helper()
	body, err := f.notes.LoadBody(f.ctx, noteID)
	if err != nil {
		f.t.Fatalf("load body: %v", err)
	}
	return body
}

func (f *coreFixture) save(noteID, markdown, base string) *SaveResult {
	f.t.Helper()
	res, err := f.notes.SaveBody(f.ctx, SaveBodyInput{NoteID: noteID, Markdown: markdown, BaseHash: base})
	if err != nil {
		f.t.Fatalf("save %s: %v", noteID, err)
	}
	return res
}

func (f *coreFixture) activities(q events.Query) []events.Activity {
	f.t.Helper()
	list, err := f.handle.Reader().List(f.ctx, q)
	if err != nil {
		f.t.Fatalf("list activities: %v", err)
	}
	return list
}

func (f *coreFixture) countActivities() int {
	f.t.Helper()
	n, err := f.handle.Reader().Count(f.ctx, events.Query{})
	if err != nil {
		f.t.Fatalf("count activities: %v", err)
	}
	return n
}

func (f *coreFixture) eventsOfType(noteID string, typ schema.EventType) []events.Activity {
	f.t.Helper()
	return f.activities(events.Query{EntityID: noteID, EventTypes: []events.Type{typ}})
}

// seedDatabase inserts a research database plus its title field directly. The
// DatabaseService worker owns the production path; these tests only need the
// rows PromoteToDatabase depends on.
func (f *coreFixture) seedDatabase(name string) (dbID, titleFieldID string) {
	f.t.Helper()
	now := clock.Format(f.clock.Now())
	dbID = ids.MustNew(f.clock)
	titleFieldID = ids.MustNew(f.clock)
	if _, err := f.handle.DB().Exec(
		`INSERT INTO research_databases(id, name, description, created_at, updated_at)
		 VALUES (?, ?, '', ?, ?)`, dbID, name, now, now); err != nil {
		f.t.Fatalf("seed database: %v", err)
	}
	if _, err := f.handle.DB().Exec(
		`INSERT INTO db_fields(id, database_id, name, type, config, role, sort_order, created_at, updated_at)
		 VALUES (?, ?, 'Name', 'text', '{}', 'title', 0, ?, ?)`, titleFieldID, dbID, now, now); err != nil {
		f.t.Fatalf("seed title field: %v", err)
	}
	return dbID, titleFieldID
}

// seedIdea inserts an idea row directly.
func (f *coreFixture) seedIdea(title string) string {
	f.t.Helper()
	id := ids.MustNew(f.clock)
	now := clock.Format(f.clock.Now())
	if _, err := f.handle.DB().Exec(
		`INSERT INTO ideas(id, title, description, status, rejection_reason, created_at, updated_at)
		 VALUES (?, ?, '', 'New', '', ?, ?)`, id, title, now, now); err != nil {
		f.t.Fatalf("seed idea: %v", err)
	}
	return id
}

// seedPaper inserts a global paper and sets its Zotero metadata title.
func (f *coreFixture) seedPaper(title string) string {
	f.t.Helper()
	id, err := f.fx.SeedPaper(f.ctx, "zotero-key-"+ids.MustNew(f.clock))
	if err != nil {
		f.t.Fatalf("seed paper: %v", err)
	}
	if title != "" {
		meta, err := json.Marshal(map[string]string{"title": title})
		if err != nil {
			f.t.Fatalf("seed paper metadata: %v", err)
		}
		if _, err := f.fx.Workspace.Global().DB().Exec(
			`UPDATE papers SET metadata = ? WHERE id = ?`, string(meta), id); err != nil {
			f.t.Fatalf("seed paper metadata: %v", err)
		}
	}
	return id
}

func coreMustScanString(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var v string
	if err := db.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatalf("scan %q: %v", query, err)
	}
	return v
}

// TestCoreCreateNote covers acceptance item 1: the file and the row are
// created together with a UUIDv7, the hash baseline is set and exactly one
// entity.created is emitted.
func TestCoreCreateNote(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("First note", "hello world")

	if !ids.Valid(note.ID) {
		t.Fatalf("id %q is not a UUIDv7", note.ID)
	}
	wantPath := f.fx.Workspace.Paths().NoteFile(f.fx.ProjectID, note.ID)
	if note.FilePath != wantPath {
		t.Fatalf("file_path = %q, want %q", note.FilePath, wantPath)
	}
	if got := f.read(wantPath); got != "hello world" {
		t.Fatalf("file content = %q", got)
	}
	if note.Kind != KindScratchpad || note.Title != "First note" {
		t.Fatalf("note = %+v", note)
	}
	if note.BodyState != BodyOK {
		t.Fatalf("body state = %q", note.BodyState)
	}
	if note.ContentHash != notefile.HashBytes([]byte("hello world")) {
		t.Fatalf("content hash = %q", note.ContentHash)
	}
	created := f.eventsOfType(note.ID, schema.EventEntityCreated)
	if len(created) != 1 {
		t.Fatalf("entity.created count = %d, want 1", len(created))
	}
	if len(f.indexer.calls()) != 1 {
		t.Fatalf("index hook calls = %d, want 1", len(f.indexer.calls()))
	}

	// Reading the created note returns the same row.
	got, err := f.notes.Get(f.ctx, note.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != note.Title || !got.CreatedAt.Equal(note.CreatedAt) {
		t.Fatalf("get = %+v, want %+v", got, note)
	}
}

// TestCoreListAndSearch covers kind/title/tag filters, paging and the 500 cap.
func TestCoreListAndSearch(t *testing.T) {
	f := coreNewFixture(t)

	alpha := f.create("Alpha Idea", "a")
	f.clock.Advance(time.Second)
	beta := f.create("beta note", "b")
	f.clock.Advance(time.Second)
	dbNote := f.createKind(KindDBRecord, "Alpha Row", "r")
	f.clock.Advance(time.Second)

	page, err := f.notes.List(f.ctx, ListNotesQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 3 || len(page.Notes) != 3 {
		t.Fatalf("all notes total=%d len=%d", page.Total, len(page.Notes))
	}
	// Newest first.
	if page.Notes[0].ID != dbNote.ID || page.Notes[2].ID != alpha.ID {
		t.Fatalf("unexpected order: %s, %s, %s", page.Notes[0].Title, page.Notes[1].Title, page.Notes[2].Title)
	}

	page, err = f.notes.List(f.ctx, ListNotesQuery{TitleLike: "ALPHA"})
	if err != nil {
		t.Fatalf("list title: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("case-insensitive title search total = %d, want 2", page.Total)
	}

	page, err = f.notes.List(f.ctx, ListNotesQuery{Kind: KindScratchpad})
	if err != nil {
		t.Fatalf("list kind: %v", err)
	}
	if page.Total != 2 || page.Notes[0].ID != beta.ID {
		t.Fatalf("kind filter total=%d", page.Total)
	}

	// Paging keeps the unpaged total.
	page, err = f.notes.List(f.ctx, ListNotesQuery{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("list page: %v", err)
	}
	if page.Total != 3 || len(page.Notes) != 1 || page.Notes[0].ID != beta.ID {
		t.Fatalf("paged list total=%d len=%d", page.Total, len(page.Notes))
	}

	// Soft-deleted notes only show with IncludeDeleted.
	if err := f.notes.SoftDelete(f.ctx, beta.ID, ""); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	page, _ = f.notes.List(f.ctx, ListNotesQuery{})
	if page.Total != 2 {
		t.Fatalf("after delete total = %d, want 2", page.Total)
	}
	page, _ = f.notes.List(f.ctx, ListNotesQuery{IncludeDeleted: true})
	if page.Total != 3 {
		t.Fatalf("include deleted total = %d, want 3", page.Total)
	}

	// Tag filter reads entity_tags (assignments are written by TagService).
	now := clock.Format(f.clock.Now())
	tagID := ids.MustNew(f.clock)
	if _, err := f.handle.DB().Exec(
		`INSERT INTO tags(id, name, color, created_at, updated_at) VALUES (?, 'reading', '', ?, ?)`,
		tagID, now, now); err != nil {
		t.Fatalf("seed tag: %v", err)
	}
	if _, err := f.handle.DB().Exec(
		`INSERT INTO entity_tags(tag_id, entity_type, entity_id, created_at) VALUES (?, 'note', ?, ?)`,
		tagID, alpha.ID, now); err != nil {
		t.Fatalf("seed entity tag: %v", err)
	}
	page, err = f.notes.List(f.ctx, ListNotesQuery{TagIDs: []string{tagID}})
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	if page.Total != 1 || page.Notes[0].ID != alpha.ID {
		t.Fatalf("tag filter total=%d", page.Total)
	}

	// The 500-row cap: seeding files would be slow, rows are enough here.
	if err := f.fx.SeedNotes(f.ctx, f.handle, 600, 0); err != nil {
		t.Fatalf("seed notes: %v", err)
	}
	page, err = f.notes.List(f.ctx, ListNotesQuery{Limit: 5000})
	if err != nil {
		t.Fatalf("list cap: %v", err)
	}
	if len(page.Notes) != 500 {
		t.Fatalf("capped limit = %d, want 500", len(page.Notes))
	}
	if page.Total != 602 {
		t.Fatalf("total = %d, want 602", page.Total)
	}
}

// TestCoreLoadBodyMissing covers acceptance item 5's missing half: no error,
// file_state flips to missing once and no activity is written.
func TestCoreLoadBodyMissing(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("Lost", "content")
	before := f.countActivities()

	if err := os.Remove(f.abs(note.FilePath)); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	body := f.loadBody(note.ID)
	if body.State != BodyMissing || body.Markdown != "" || body.Hash != "" {
		t.Fatalf("body = %+v", body)
	}
	state := coreMustScanString(t, f.handle.DB(), `SELECT file_state FROM notes WHERE id = ?`, note.ID)
	if state != "missing" {
		t.Fatalf("file_state = %q, want missing", state)
	}
	// A second load stays silent and does not fail.
	_ = f.loadBody(note.ID)
	if got := f.countActivities(); got != before {
		t.Fatalf("activities changed by reads: %d -> %d", before, got)
	}
}

// TestCoreReadsWriteNoActivities covers acceptance item 15 at the service
// level: open/read/list/link resolution never write Activity rows.
func TestCoreReadsWriteNoActivities(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("Read only", "body")
	paperID := f.seedPaper("Some paper")
	if err := f.rels.Add(f.ctx, RelationEdge{
		Src: EntityRef{Type: schema.EntityNote, ID: note.ID},
		Dst: EntityRef{Type: schema.EntityPaper, ID: paperID},
	}); err != nil {
		t.Fatalf("add relation: %v", err)
	}
	before := f.countActivities()

	if _, err := f.notes.Get(f.ctx, note.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := f.notes.List(f.ctx, ListNotesQuery{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	f.loadBody(note.ID)
	if _, err := f.notes.OutboundLinks(f.ctx, note.ID); err != nil {
		t.Fatalf("outbound links: %v", err)
	}
	if _, err := f.notes.ResolveTarget(f.ctx, EntityRef{Type: schema.EntityNote, ID: note.ID}); err != nil {
		t.Fatalf("resolve target: %v", err)
	}
	if _, err := f.notes.Document(f.ctx, note.ID); err != nil {
		t.Fatalf("document: %v", err)
	}
	if _, err := f.rels.ListFrom(f.ctx, EntityRef{Type: schema.EntityNote, ID: note.ID}, ""); err != nil {
		t.Fatalf("list from: %v", err)
	}
	if _, err := f.recon.Scan(f.ctx, f.fx.ProjectID); err != nil {
		t.Fatalf("scan: %v", err)
	}

	after := f.countActivities()
	if after != before {
		t.Fatalf("read operations wrote %d activities", after-before)
	}
}

// TestCorePromoteAndDetach covers acceptance item 12.
func TestCorePromoteAndDetach(t *testing.T) {
	f := coreNewFixture(t)
	dbID, titleFieldID := f.seedDatabase("Reading list")
	note := f.create("Promoted", "body stays")
	noteFile := f.abs(note.FilePath)

	rec, err := f.notes.PromoteToDatabase(f.ctx, PromoteInput{NoteID: note.ID, DatabaseID: dbID})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if rec.NoteID != note.ID || rec.DatabaseID != dbID {
		t.Fatalf("record = %+v", rec)
	}
	var values map[string]string
	if err := json.Unmarshal(rec.Values, &values); err != nil {
		t.Fatalf("values: %v", err)
	}
	if values[titleFieldID] != "Promoted" {
		t.Fatalf("title field value = %+v", values)
	}
	// No body copy and no kind change (D-05-04).
	if _, err := os.Stat(noteFile); err != nil {
		t.Fatalf("note file: %v", err)
	}
	if got := coreMustScanString(t, f.handle.DB(), `SELECT kind FROM notes WHERE id = ?`, note.ID); got != "scratchpad" {
		t.Fatalf("note kind = %q", got)
	}
	if n := len(f.eventsOfType(rec.ID, schema.EventEntityCreated)); n != 1 {
		t.Fatalf("record entity.created = %d", n)
	}

	// A note can only become one record's page.
	if _, err := f.notes.PromoteToDatabase(f.ctx, PromoteInput{NoteID: note.ID, DatabaseID: dbID}); err == nil {
		t.Fatal("second promote succeeded, want an error")
	}

	// Detach without deleting the note: record trashed, note and file intact.
	if err := f.notes.DetachFromDatabase(f.ctx, rec.ID, false, ""); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if got := coreMustScanString(t, f.handle.DB(), `SELECT COALESCE(deleted_at, '') FROM db_records WHERE id = ?`, rec.ID); got == "" {
		t.Fatal("record is not soft-deleted")
	}
	if got := coreMustScanString(t, f.handle.DB(), `SELECT COALESCE(deleted_at, '') FROM notes WHERE id = ?`, note.ID); got != "" {
		t.Fatalf("note deleted at %q", got)
	}
	if _, err := os.Stat(noteFile); err != nil {
		t.Fatalf("note file removed on detach: %v", err)
	}
}

// TestCoreDetachDeletesNote checks the deleteNote=true branch.
func TestCoreDetachDeletesNote(t *testing.T) {
	f := coreNewFixture(t)
	dbID, _ := f.seedDatabase("Second")
	note := f.create("Both go", "body")
	rec, err := f.notes.PromoteToDatabase(f.ctx, PromoteInput{NoteID: note.ID, DatabaseID: dbID})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := f.notes.DetachFromDatabase(f.ctx, rec.ID, true, ""); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if got := coreMustScanString(t, f.handle.DB(), `SELECT COALESCE(deleted_at, '') FROM notes WHERE id = ?`, note.ID); got == "" {
		t.Fatal("note is not soft-deleted")
	}
	if _, err := os.Stat(f.abs(note.FilePath)); err != nil {
		t.Fatalf("note file must be preserved: %v", err)
	}
	if n := len(f.eventsOfType(note.ID, schema.EventEntityDeleted)); n != 1 {
		t.Fatalf("note entity.deleted = %d, want 1", n)
	}
}

// TestCoreRestoreReindexes checks restore clears deleted_at and re-runs the
// index hook without a second note file.
func TestCoreRestoreReindexes(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("Restorable", "body")
	if err := f.notes.SoftDelete(f.ctx, note.ID, ""); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if _, err := f.notes.SaveBody(f.ctx, SaveBodyInput{NoteID: note.ID, Markdown: "x", BaseHash: ""}); err == nil {
		t.Fatal("save on a deleted note succeeded")
	}
	if err := f.notes.Restore(f.ctx, note.ID, ""); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := f.notes.Get(f.ctx, note.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DeletedAt != nil {
		t.Fatal("note still deleted")
	}
	if got.BodyState != BodyOK {
		t.Fatalf("body state = %q", got.BodyState)
	}
	if n := len(f.eventsOfType(note.ID, schema.EventEntityRestored)); n != 1 {
		t.Fatalf("entity.restored = %d", n)
	}
	last := f.indexer.calls()[len(f.indexer.calls())-1]
	if last.Type != schema.EventEntityRestored {
		t.Fatalf("last index hook = %s", last.Type)
	}
}

// TestCoreUpdateTitleMergesEvent checks title-only updates write one merged
// note.updated and leave the body file alone.
func TestCoreUpdateTitleMergesEvent(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("Old", "body")
	f.clock.Advance(time.Second)
	got, err := f.notes.UpdateTitle(f.ctx, note.ID, "New", "")
	if err != nil {
		t.Fatalf("update title: %v", err)
	}
	if got.Title != "New" {
		t.Fatalf("title = %q", got.Title)
	}
	if f.read(note.FilePath) != "body" {
		t.Fatal("body changed by a title update")
	}
	acts := f.eventsOfType(note.ID, schema.EventNoteUpdated)
	if len(acts) != 1 || acts[0].Payload.SaveCount != 1 {
		t.Fatalf("note.updated = %+v", acts)
	}
	// An unchanged title writes nothing.
	if _, err := f.notes.UpdateTitle(f.ctx, note.ID, "New", ""); err != nil {
		t.Fatalf("update title again: %v", err)
	}
	acts = f.eventsOfType(note.ID, schema.EventNoteUpdated)
	if len(acts) != 1 || acts[0].Payload.SaveCount != 1 {
		t.Fatalf("note.updated after no-op = %+v", acts)
	}
}

// TestCoreIndexFailureIsNotFatal proves the post-commit hook never fails a
// committed business write.
func TestCoreIndexFailureIsNotFatal(t *testing.T) {
	f := coreNewFixture(t)
	f.indexer.fails = true
	note := f.create("Still works", "body")
	if note.ID == "" {
		t.Fatal("create failed because of the index hook")
	}
	if res := f.save(note.ID, "next", f.loadBody(note.ID).Hash); res.Conflict {
		t.Fatal("save reported a conflict")
	}
	if got := f.read(note.FilePath); got != "next" {
		t.Fatalf("disk = %q", got)
	}
}
