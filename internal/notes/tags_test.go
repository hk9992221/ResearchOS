package notes

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
	"researchos/internal/notes/migrations"
)

// tagEnv is the shared fixture for the tag tests: a SPEC-05-migrated project
// database plus the TagService over it.
type tagEnv struct {
	t      *testing.T
	fix    *foundationtest.Fixture
	handle *workspace.ProjectHandle
	db     *sql.DB
	svc    TagService
}

func tagNewEnv(t *testing.T) *tagEnv {
	t.Helper()
	fix, err := foundationtest.NewWithProject(t.TempDir(),
		foundationtest.Options{Migrations: migrations.MustAll()})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() { _ = fix.Close() })
	return &tagEnv{
		t:      t,
		fix:    fix,
		handle: fix.Project,
		db:     fix.Project.DB(),
		svc:    NewTagService(fix.Project.DB(), fix.Clock),
	}
}

// tagNewNote inserts a bare notes row (the tag service only reads metadata)
// and returns its id.
func (e *tagEnv) tagNewNote(deleted bool) string {
	e.t.Helper()
	id := ids.MustNew(e.fix.Clock)
	path := "projects/" + e.fix.ProjectID + "/notes/tag-" + id + ".md"
	var del any
	if deleted {
		del = tagNow(e)
	}
	_, err := e.db.Exec(
		`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at, deleted_at)
		 VALUES (?, '', ?, 'scratchpad', ?, ?, ?)`,
		id, path, tagNow(e), tagNow(e), del)
	if err != nil {
		e.t.Fatalf("seed note: %v", err)
	}
	return id
}

// tagNewRecord inserts a research database and one record; when deleted is
// true the record row carries deleted_at. It returns the record id.
func (e *tagEnv) tagNewRecord(deleted bool) string {
	e.t.Helper()
	dbID := ids.MustNew(e.fix.Clock)
	recID := ids.MustNew(e.fix.Clock)
	if _, err := e.db.Exec(
		`INSERT INTO research_databases(id, name, created_at, updated_at) VALUES (?, 'db', ?, ?)`,
		dbID, tagNow(e), tagNow(e)); err != nil {
		e.t.Fatalf("seed database: %v", err)
	}
	var del any
	if deleted {
		del = tagNow(e)
	}
	if _, err := e.db.Exec(
		`INSERT INTO db_records(id, database_id, title, created_at, updated_at, deleted_at)
		 VALUES (?, ?, '', ?, ?, ?)`,
		recID, dbID, tagNow(e), tagNow(e), del); err != nil {
		e.t.Fatalf("seed record: %v", err)
	}
	return recID
}

// tagNow formats the fixture clock's instant for direct SQL seeds.
func tagNow(e *tagEnv) string {
	e.t.Helper()
	return clock.Format(e.fix.Clock.Now())
}

// tagCount joins and tag rows directly, bypassing the service.
func (e *tagEnv) tagCount(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (e *tagEnv) tagCreate(name, color string) *Tag {
	e.t.Helper()
	tag, err := e.svc.CreateTag(context.Background(), name, color, "user")
	if err != nil {
		e.t.Fatalf("CreateTag(%q): %v", name, err)
	}
	return tag
}

func TestTagCreateAndList(t *testing.T) {
	e := tagNewEnv(t)
	ctx := context.Background()

	// Names are trimmed; color is optional.
	created := e.tagCreate("  Go  ", "")
	if created.Name != "Go" {
		t.Fatalf("name not trimmed: %q", created.Name)
	}
	if !ids.Valid(created.ID) {
		t.Fatalf("id is not UUIDv7: %q", created.ID)
	}
	if created.DeletedAt != nil {
		t.Fatalf("fresh tag deleted: %+v", created)
	}

	// Acceptance 9: same name modulo case returns the existing tag without
	// creating a duplicate row and without an error.
	dup, err := e.svc.CreateTag(ctx, "go", "#ff0000", "user")
	if err != nil {
		t.Fatalf("duplicate create: %v", err)
	}
	if dup.ID != created.ID || dup.Color != "" {
		t.Fatalf("duplicate create mutated the tag: %+v vs %+v", dup, created)
	}
	if n := e.tagCount(`SELECT count(*) FROM tags`); n != 1 {
		t.Fatalf("duplicate created %d rows", n)
	}

	e.tagCreate("banana", "yellow")
	e.tagCreate("Apple", "#0f0")

	// Ordering: case-insensitive by name, deleted excluded by default.
	tags, err := e.svc.ListTags(ctx, false)
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	var names []string
	for _, tg := range tags {
		names = append(names, tg.Name)
	}
	want := []string{"Apple", "banana", "Go"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", names, want)
	}

	// Soft-deleted tags appear only with includeDeleted.
	if err := e.svc.SoftDeleteTag(ctx, created.ID, "user"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	withDeleted, err := e.svc.ListTags(ctx, true)
	if err != nil {
		t.Fatalf("ListTags(all): %v", err)
	}
	if len(withDeleted) != 3 {
		t.Fatalf("includeDeleted list = %d tags, want 3", len(withDeleted))
	}

	// Validation: empty / oversized / control-character names, bad colors.
	for _, bad := range []string{"", "   ", strings.Repeat("x", 65), "a\nb"} {
		if _, err := e.svc.CreateTag(ctx, bad, "", "user"); !errors.Is(err, ErrTagValidation) {
			t.Fatalf("CreateTag(%q) err = %v, want ErrTagValidation", bad, err)
		}
	}
	for _, badColor := range []string{"#12", "#12345g", "red blue", "#0f0f0f0"} {
		if _, err := e.svc.CreateTag(ctx, "colored", badColor, "user"); !errors.Is(err, ErrTagValidation) {
			t.Fatalf("CreateTag color %q err = %v, want ErrTagValidation", badColor, err)
		}
	}
	if _, err := e.svc.CreateTag(ctx, "colored", "", "user"); err != nil {
		t.Fatalf("empty color must be accepted: %v", err)
	}
}

func TestTagRename(t *testing.T) {
	e := tagNewEnv(t)
	ctx := context.Background()

	gone := e.tagCreate("Go", "")
	other := e.tagCreate("Other", "")

	renamed, err := e.svc.RenameTag(ctx, gone.ID, "  Golang  ", "#00add8", "user")
	if err != nil {
		t.Fatalf("RenameTag: %v", err)
	}
	if renamed.Name != "Golang" || renamed.Color != "#00add8" {
		t.Fatalf("rename result %+v", renamed)
	}

	// Duplicate name (case-insensitive) is a validation error.
	if _, err := e.svc.RenameTag(ctx, other.ID, "gOLANG", "", "user"); !errors.Is(err, ErrTagValidation) {
		t.Fatalf("duplicate rename err = %v, want ErrTagValidation", err)
	}
	// Renaming to its own name (any case) is fine and an empty color keeps
	// the current one.
	again, err := e.svc.RenameTag(ctx, gone.ID, "golang", "", "user")
	if err != nil {
		t.Fatalf("self rename: %v", err)
	}
	if again.Color != "#00add8" {
		t.Fatalf("empty color dropped existing color: %q", again.Color)
	}

	// Deleted and unknown tags cannot be renamed.
	if err := e.svc.SoftDeleteTag(ctx, other.ID, "user"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := e.svc.RenameTag(ctx, other.ID, "back", "", "user"); !errors.Is(err, ErrTagValidation) {
		t.Fatalf("rename deleted err = %v, want ErrTagValidation", err)
	}
	unknown := ids.MustNew(e.fix.Clock)
	if _, err := e.svc.RenameTag(ctx, unknown, "x", "", "user"); !errors.Is(err, ErrTagValidation) {
		t.Fatalf("rename unknown err = %v, want ErrTagValidation", err)
	}
	if _, err := e.svc.RenameTag(ctx, "not-a-uuid", "x", "", "user"); !errors.Is(err, errs.ErrBadEntityID) {
		t.Fatalf("rename bad id err = %v, want ErrBadEntityID", err)
	}
}

func TestTagSoftDeleteRestore(t *testing.T) {
	e := tagNewEnv(t)
	ctx := context.Background()

	tag := e.tagCreate("Draft", "")
	note := e.tagNewNote(false)
	if err := e.svc.Assign(ctx, []EntityRef{{Type: schema.EntityNote, ID: note}}, tag.ID, "user"); err != nil {
		t.Fatalf("assign: %v", err)
	}

	if err := e.svc.SoftDeleteTag(ctx, tag.ID, "user"); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	// D-05-07: join rows survive the soft delete untouched.
	if n := e.tagCount(`SELECT count(*) FROM entity_tags WHERE tag_id = ?`, tag.ID); n != 1 {
		t.Fatalf("entity_tags rows after delete = %d, want 1", n)
	}
	live, _ := e.svc.ListTags(ctx, false)
	if len(live) != 0 {
		t.Fatalf("deleted tag still in live list: %+v", live)
	}
	// Idempotent re-delete and no events for tags.
	if err := e.svc.SoftDeleteTag(ctx, tag.ID, "user"); err != nil {
		t.Fatalf("re-delete: %v", err)
	}
	if n := e.tagCount(`SELECT count(*) FROM activities`); n != 0 {
		t.Fatalf("tag operations wrote %d activities, want 0", n)
	}
	// Deleted tags reject Assign/Unassign/Rename but EntitiesByTag still
	// answers.
	if err := e.svc.Assign(ctx, []EntityRef{{Type: schema.EntityNote, ID: note}}, tag.ID, "user"); !errors.Is(err, ErrTagValidation) {
		t.Fatalf("assign to deleted tag err = %v", err)
	}
	refs, err := e.svc.EntitiesByTag(ctx, tag.ID)
	if err != nil || len(refs) != 1 || refs[0].ID != note {
		t.Fatalf("EntitiesByTag deleted tag = %v, %v", refs, err)
	}

	if err := e.svc.RestoreTag(ctx, tag.ID, "user"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	restored, _ := e.svc.ListTags(ctx, false)
	if len(restored) != 1 || restored[0].ID != tag.ID || restored[0].DeletedAt != nil {
		t.Fatalf("restore did not bring the tag back: %+v", restored)
	}
	if err := e.svc.RestoreTag(ctx, tag.ID, "user"); err != nil {
		t.Fatalf("restore live tag: %v", err)
	}
	// Assignments are immediately visible again (D-05-07).
	refs, err = e.svc.EntitiesByTag(ctx, tag.ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("entities after restore = %v, %v", refs, err)
	}

	// Recreating the name while deleted is allowed (fresh tag) but blocks the
	// restore of the old one.
	if err := e.svc.SoftDeleteTag(ctx, tag.ID, "user"); err != nil {
		t.Fatalf("re-delete: %v", err)
	}
	fresh := e.tagCreate("draft", "")
	if fresh.ID == tag.ID {
		t.Fatal("recreate reused the deleted tag id")
	}
	if err := e.svc.RestoreTag(ctx, tag.ID, "user"); !errors.Is(err, ErrTagValidation) {
		t.Fatalf("restore into taken name err = %v, want ErrTagValidation", err)
	}
	if err := e.svc.RestoreTag(ctx, ids.MustNew(e.fix.Clock), "user"); !errors.Is(err, ErrTagValidation) {
		t.Fatal("restore of unknown tag must fail")
	}
}

func TestTagAssignUnassign(t *testing.T) {
	e := tagNewEnv(t)
	ctx := context.Background()

	tag := e.tagCreate("Shared", "")
	note := e.tagNewNote(false)
	deadNote := e.tagNewNote(true)
	record := e.tagNewRecord(false)
	deadRecord := e.tagNewRecord(true)
	paper := ids.MustNew(e.fix.Clock)

	refs := []EntityRef{{Type: schema.EntityNote, ID: note}, {Type: schema.EntityDBRecord, ID: record}}
	if err := e.svc.Assign(ctx, refs, tag.ID, "user"); err != nil {
		t.Fatalf("assign: %v", err)
	}
	// Idempotent: the primary key absorbs repeats.
	if err := e.svc.Assign(ctx, refs, tag.ID, "user"); err != nil {
		t.Fatalf("re-assign: %v", err)
	}
	if n := e.tagCount(`SELECT count(*) FROM entity_tags WHERE tag_id = ?`, tag.ID); n != 2 {
		t.Fatalf("join rows = %d, want 2", n)
	}

	got, err := e.svc.EntitiesByTag(ctx, tag.ID)
	if err != nil {
		t.Fatalf("EntitiesByTag: %v", err)
	}
	if len(got) != 2 || got[0].Type != schema.EntityDBRecord || got[0].ID != record ||
		got[1].Type != schema.EntityNote || got[1].ID != note {
		t.Fatalf("entities = %+v, want ordered (db_record, note)", got)
	}

	// Rejections: deleted entities, unknown entities, unsupported types,
	// malformed ids.
	bad := []struct {
		name    string
		ref     EntityRef
		wantErr error
	}{
		{"deleted note", EntityRef{Type: schema.EntityNote, ID: deadNote}, errs.ErrEntityDeleted},
		{"deleted record", EntityRef{Type: schema.EntityDBRecord, ID: deadRecord}, errs.ErrEntityDeleted},
		{"unknown note", EntityRef{Type: schema.EntityNote, ID: paper}, ErrTagValidation},
		{"paper type", EntityRef{Type: schema.EntityPaper, ID: paper}, ErrTagValidation},
		{"bad id", EntityRef{Type: schema.EntityNote, ID: "nope"}, errs.ErrBadEntityID},
	}
	for _, tc := range bad {
		if err := e.svc.Assign(ctx, []EntityRef{tc.ref}, tag.ID, "user"); !errors.Is(err, tc.wantErr) {
			t.Fatalf("assign %s err = %v, want %v", tc.name, err, tc.wantErr)
		}
	}
	// A failing ref in a mixed batch leaves nothing behind (checks run first).
	mixed := []EntityRef{{Type: schema.EntityNote, ID: deadNote}, refs[0]}
	if err := e.svc.Assign(ctx, mixed, tag.ID, "user"); !errors.Is(err, errs.ErrEntityDeleted) {
		t.Fatalf("mixed assign err = %v", err)
	}
	if n := e.tagCount(`SELECT count(*) FROM entity_tags WHERE tag_id = ?`, tag.ID); n != 2 {
		t.Fatalf("failed mixed assign changed rows to %d", n)
	}

	// Unknown tag and empty batch.
	if err := e.svc.Assign(ctx, refs, ids.MustNew(e.fix.Clock), "user"); !errors.Is(err, ErrTagValidation) {
		t.Fatal("assign to unknown tag must fail")
	}
	if err := e.svc.Assign(ctx, nil, tag.ID, "user"); err != nil {
		t.Fatalf("empty assign: %v", err)
	}

	// Unassign removes exact rows; repeats are no-ops. Tags never touch
	// entity_relations.
	if err := e.svc.Unassign(ctx, []EntityRef{refs[1]}, tag.ID, "user"); err != nil {
		t.Fatalf("unassign: %v", err)
	}
	if err := e.svc.Unassign(ctx, []EntityRef{refs[1]}, tag.ID, "user"); err != nil {
		t.Fatalf("re-unassign: %v", err)
	}
	if n := e.tagCount(`SELECT count(*) FROM entity_tags WHERE tag_id = ?`, tag.ID); n != 1 {
		t.Fatalf("rows after unassign = %d, want 1", n)
	}
	if n := e.tagCount(`SELECT count(*) FROM entity_relations`); n != 0 {
		t.Fatalf("tag changes wrote %d relation rows, want 0", n)
	}
	if err := e.svc.Unassign(ctx, []EntityRef{{Type: schema.EntityNote, ID: note}},
		ids.MustNew(e.fix.Clock), "user"); !errors.Is(err, ErrTagValidation) {
		t.Fatal("unassign from unknown tag must fail")
	}

	// EntitiesByTag includes refs whose entity is now soft-deleted.
	becomesDead := e.tagNewNote(false)
	if err := e.svc.Assign(ctx, []EntityRef{{Type: schema.EntityNote, ID: becomesDead}}, tag.ID, "user"); err != nil {
		t.Fatalf("assign live note: %v", err)
	}
	e.mustSoftDeleteNote(ctx, t, becomesDead)
	refs2, err := e.svc.EntitiesByTag(ctx, tag.ID)
	if err != nil {
		t.Fatalf("EntitiesByTag: %v", err)
	}
	if len(refs2) != 2 {
		t.Fatalf("entities = %+v, want the soft-deleted note kept too", refs2)
	}
	if _, err := e.svc.EntitiesByTag(ctx, ids.MustNew(e.fix.Clock)); !errors.Is(err, ErrTagValidation) {
		t.Fatal("EntitiesByTag of unknown tag must fail")
	}
}

// mustSoftDeleteNote flips notes.deleted_at directly, as the trash service
// would.
func (e *tagEnv) mustSoftDeleteNote(ctx context.Context, t *testing.T, noteID string) {
	t.Helper()
	if _, err := e.db.ExecContext(ctx, `UPDATE notes SET deleted_at = ? WHERE id = ?`,
		tagNow(e), noteID); err != nil {
		t.Fatalf("soft delete note: %v", err)
	}
}
