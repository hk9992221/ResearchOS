package notes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/notefile"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
	"researchos/internal/notes/migrations"
)

// dbFixture bundles the fixture workspace with the two services under test and
// their collaborators. Every helper of the database/record tests is prefixed
// "db" so it cannot collide with the other workers' test helpers.
type dbFixture struct {
	t         *testing.T
	ctx       context.Context
	fx        *foundationtest.Fixture
	handle    *workspace.ProjectHandle
	databases DatabaseService
	records   RecordService
	noteStore *notefile.Store
	relations *relations.Service
}

func dbNewFixture(t *testing.T) *dbFixture {
	t.Helper()
	fx, err := foundationtest.NewWithProject(t.TempDir(), foundationtest.Options{
		Migrations: migrations.MustAll(),
	})
	if err != nil {
		t.Fatalf("dbNewFixture: %v", err)
	}
	t.Cleanup(func() { _ = fx.Close() })
	handle := fx.Project
	noteStore := notefile.New(handle.ID, handle.DB(), fx.Workspace.Paths(), handle.Events(), fx.Clock)
	relSvc := relations.New(handle.DB(), fx.Workspace.Global().DB(), handle.Events(), fx.Clock)
	opts := ServiceOptions{Clock: fx.Clock}
	return &dbFixture{
		t:         t,
		ctx:       context.Background(),
		fx:        fx,
		handle:    handle,
		databases: NewDatabaseService(handle.DB(), handle.Events(), fx.Trash(handle), opts),
		records:   NewRecordService(handle.DB(), handle.ID, handle.Events(), fx.Trash(handle), noteStore, relSvc, opts),
		noteStore: noteStore,
		relations: relSvc,
	}
}

func (f *dbFixture) createDatabase(name string) *ResearchDatabase {
	f.t.Helper()
	database, err := f.databases.CreateDatabase(f.ctx, CreateDatabaseInput{
		Name:        name,
		Description: "desc " + name,
		Actor:       schema.ActorUser,
	})
	if err != nil {
		f.t.Fatalf("create database %q: %v", name, err)
	}
	return database
}

func (f *dbFixture) createField(databaseID, name string, fieldType FieldType, config string) *Field {
	f.t.Helper()
	in := CreateFieldInput{DatabaseID: databaseID, Name: name, Type: fieldType, Actor: schema.ActorUser}
	if config != "" {
		in.Config = json.RawMessage(config)
	}
	field, err := f.databases.CreateField(f.ctx, in)
	if err != nil {
		f.t.Fatalf("create field %q: %v", name, err)
	}
	return field
}

func (f *dbFixture) titleField(databaseID string) *Field {
	f.t.Helper()
	fields, err := f.databases.ListFields(f.ctx, databaseID)
	if err != nil {
		f.t.Fatalf("list fields: %v", err)
	}
	for _, field := range fields {
		if field.Role == RoleTitle {
			return field
		}
	}
	f.t.Fatalf("database %s has no title field", databaseID)
	return nil
}

func (f *dbFixture) createRecord(databaseID string, values map[string]any) *Record {
	f.t.Helper()
	var raw json.RawMessage
	if values != nil {
		encoded, err := json.Marshal(values)
		if err != nil {
			f.t.Fatalf("marshal record values: %v", err)
		}
		raw = encoded
	}
	record, err := f.records.CreateRecord(f.ctx, CreateRecordInput{
		DatabaseID: databaseID,
		Values:     raw,
		Actor:      schema.ActorUser,
	})
	if err != nil {
		f.t.Fatalf("create record: %v", err)
	}
	return record
}

func (f *dbFixture) createRecordForNote(databaseID, noteID string, values map[string]any) (*Record, error) {
	var raw json.RawMessage
	if values != nil {
		encoded, err := json.Marshal(values)
		if err != nil {
			return nil, err
		}
		raw = encoded
	}
	return f.records.CreateRecord(f.ctx, CreateRecordInput{
		DatabaseID: databaseID,
		NoteID:     noteID,
		Values:     raw,
		Actor:      schema.ActorMCP,
	})
}

func (f *dbFixture) createNote(kind schema.NoteKind, title string) string {
	f.t.Helper()
	var noteID string
	err := f.handle.InTx(f.ctx, func(tx *sql.Tx) error {
		res, err := f.noteStore.Create(f.ctx, tx, notefile.CreateRequest{
			ProjectID: f.handle.ID,
			Kind:      kind,
			Title:     title,
			Actor:     schema.ActorUser,
		})
		if err != nil {
			return err
		}
		noteID = res.NoteID
		return nil
	})
	if err != nil {
		f.t.Fatalf("create note: %v", err)
	}
	return noteID
}

func (f *dbFixture) createTag(name string) string {
	f.t.Helper()
	tagID := ids.MustNew(f.fx.Clock)
	at := clock.Format(f.fx.Clock.Now())
	if _, err := f.handle.DB().ExecContext(f.ctx,
		`INSERT INTO tags(id, name, color, created_at, updated_at) VALUES (?, ?, '', ?, ?)`,
		tagID, name, at, at); err != nil {
		f.t.Fatalf("create tag %q: %v", name, err)
	}
	return tagID
}

func (f *dbFixture) countRows(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.handle.DB().QueryRowContext(f.ctx, query, args...).Scan(&n); err != nil {
		f.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (f *dbFixture) countActivities(eventType schema.EventType, entityType schema.EntityType) int {
	f.t.Helper()
	return f.countRows(
		`SELECT count(*) FROM activities WHERE event_type = ? AND entity_type = ?`,
		string(eventType), string(entityType))
}

func (f *dbFixture) noteRow(noteID string) (kind, title, filePath string, deletedAt sql.NullString) {
	f.t.Helper()
	err := f.handle.DB().QueryRowContext(f.ctx,
		`SELECT kind, title, file_path, deleted_at FROM notes WHERE id = ?`, noteID).
		Scan(&kind, &title, &filePath, &deletedAt)
	if err != nil {
		f.t.Fatalf("load note %s: %v", noteID, err)
	}
	return kind, title, filePath, deletedAt
}

func dbFieldNames(fields []*Field) []string {
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		names = append(names, field.Name)
	}
	return names
}

func TestDBCreateDatabaseCreatesOneTitleField(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Papers")

	if database.ID == "" || !ids.Valid(database.ID) {
		t.Fatalf("database id %q is not a UUIDv7", database.ID)
	}
	if database.Name != "Papers" || database.Description != "desc Papers" {
		t.Fatalf("unexpected database row: %+v", database)
	}
	fields, err := f.databases.ListFields(f.ctx, database.ID)
	if err != nil {
		t.Fatalf("ListFields: %v", err)
	}
	if len(fields) != 1 {
		t.Fatalf("fresh database has %d fields, want 1", len(fields))
	}
	title := fields[0]
	if title.Role != RoleTitle || title.Type != FieldTypeText || title.Name != "Name" {
		t.Fatalf("unexpected title field: %+v", title)
	}
	if string(title.Config) != "{}" {
		t.Fatalf("title config = %s, want {}", title.Config)
	}
	if title.Position != 0 {
		t.Fatalf("title position = %d, want 0", title.Position)
	}

	if got := f.countActivities(schema.EventEntityCreated, schema.EntityResearchDatabase); got != 1 {
		t.Fatalf("entity.created research_database = %d, want 1", got)
	}
	// The implicit title field is part of the database creation; SPEC-05 §5.2
	// only requires the research_database event here.
	if got := f.countActivities(schema.EventEntityCreated, schema.EntityDBField); got != 0 {
		t.Fatalf("entity.created db_field = %d, want 0", got)
	}

	second := f.createDatabase("Ideas")
	if _, err := f.databases.ListFields(f.ctx, second.ID); err != nil {
		t.Fatalf("ListFields second: %v", err)
	}
	if got := len(dbFieldNames(mustFields(t, f, second.ID))); got != 1 {
		t.Fatalf("second database has %d fields, want 1", got)
	}
}

func mustFields(t *testing.T, f *dbFixture, databaseID string) []*Field {
	t.Helper()
	fields, err := f.databases.ListFields(f.ctx, databaseID)
	if err != nil {
		t.Fatalf("ListFields: %v", err)
	}
	return fields
}

func TestDBFieldLifecycle(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Repo")

	notes := f.createField(database.ID, "Notes", FieldTypeText, "")
	score := f.createField(database.ID, "Score", FieldTypeNumber, "")

	if _, err := f.databases.CreateField(f.ctx, CreateFieldInput{
		DatabaseID: database.ID, Name: "notes", Type: FieldTypeText, Actor: schema.ActorUser,
	}); !errors.Is(err, errDBValidation) {
		t.Fatalf("duplicate field name error = %v, want validation error", err)
	}

	fields := mustFields(t, f, database.ID)
	if got := dbFieldNames(fields); len(got) != 3 || got[0] != "Name" || got[1] != "Notes" || got[2] != "Score" {
		t.Fatalf("field order = %v", got)
	}

	renamed, err := f.databases.UpdateField(f.ctx, UpdateFieldInput{
		FieldID: notes.ID, Name: "Content", Actor: schema.ActorUser,
	})
	if err != nil {
		t.Fatalf("UpdateField rename: %v", err)
	}
	if renamed.Name != "Content" || string(renamed.Config) != "{}" {
		t.Fatalf("renamed field = %+v", renamed)
	}
	if got := f.countActivities(schema.EventEntityUpdated, schema.EntityDBField); got != 1 {
		t.Fatalf("entity.updated db_field = %d, want 1", got)
	}

	ordered := []string{score.ID, notes.ID, f.titleField(database.ID).ID}
	if err := f.databases.ReorderFields(f.ctx, database.ID, ordered, schema.ActorUser); err != nil {
		t.Fatalf("ReorderFields: %v", err)
	}
	fields = mustFields(t, f, database.ID)
	if got := dbFieldNames(fields); got[0] != "Score" || got[1] != "Content" || got[2] != "Name" {
		t.Fatalf("reordered names = %v", got)
	}
	for i, field := range fields {
		if field.Position != i {
			t.Fatalf("field %s position = %d, want %d", field.Name, field.Position, i)
		}
	}

	if err := f.databases.ReorderFields(f.ctx, database.ID, ordered[:2], schema.ActorUser); !errors.Is(err, errDBValidation) {
		t.Fatalf("partial permutation error = %v, want validation error", err)
	}
	duplicate := []string{score.ID, score.ID, notes.ID}
	if err := f.databases.ReorderFields(f.ctx, database.ID, duplicate, schema.ActorUser); !errors.Is(err, errDBValidation) {
		t.Fatalf("duplicate permutation error = %v, want validation error", err)
	}

	titleID := f.titleField(database.ID).ID
	if err := f.databases.DeleteField(f.ctx, titleID, schema.ActorUser); !errors.Is(err, errDBValidation) {
		t.Fatalf("delete title error = %v, want validation error", err)
	}
	if got := len(mustFields(t, f, database.ID)); got != 3 {
		t.Fatalf("fields after rejected title delete = %d, want 3", got)
	}

	if err := f.databases.DeleteField(f.ctx, notes.ID, schema.ActorUser); err != nil {
		t.Fatalf("DeleteField: %v", err)
	}
	fields = mustFields(t, f, database.ID)
	if got := dbFieldNames(fields); len(got) != 2 {
		t.Fatalf("fields after delete = %v", got)
	}
	if got := f.countActivities(schema.EventEntityDeleted, schema.EntityDBField); got != 1 {
		t.Fatalf("entity.deleted db_field = %d, want 1", got)
	}

	// Restore must clear deleted_at and bring the field back in place.
	if err := f.databases.RestoreField(f.ctx, notes.ID, schema.ActorUser); err != nil {
		t.Fatalf("RestoreField: %v", err)
	}
	fields = mustFields(t, f, database.ID)
	if got := dbFieldNames(fields); len(got) != 3 {
		t.Fatalf("fields after restore = %v", got)
	}
	if got := f.countActivities(schema.EventEntityRestored, schema.EntityDBField); got != 1 {
		t.Fatalf("entity.restored db_field = %d, want 1", got)
	}
}

func TestDBFieldConfigValidation(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Config")

	selectField := f.createField(database.ID, "Status", FieldTypeSelect,
		`{"options":[{"name":"Draft","color":"gray"},{"name":"Done","color":"green"}]}`)
	cfg, err := parseFieldConfig(selectField.Type, selectField.Config)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if len(cfg.Options) != 2 {
		t.Fatalf("options = %d, want 2", len(cfg.Options))
	}
	for _, option := range cfg.Options {
		if !ids.Valid(option.ID) {
			t.Fatalf("option id %q is not a server-generated UUIDv7", option.ID)
		}
	}

	// Client-supplied ids are never accepted on create.
	forged := f.createField(database.ID, "Forged", FieldTypeSelect,
		`{"options":[{"id":"11111111-1111-7111-8111-111111111111","name":"X"}]}`)
	forgedCfg, err := parseFieldConfig(forged.Type, forged.Config)
	if err != nil {
		t.Fatalf("parse forged config: %v", err)
	}
	if forgedCfg.Options[0].ID == "11111111-1111-7111-8111-111111111111" {
		t.Fatalf("client option id was accepted")
	}

	// Option ids stay stable across updates; new options get fresh ids.
	firstID := cfg.Options[0].ID
	updated, err := f.databases.UpdateField(f.ctx, UpdateFieldInput{
		FieldID: selectField.ID,
		Name:    "Status",
		Config:  json.RawMessage(`{"options":[{"id":"` + firstID + `","name":"Draft v2","color":"gray"},{"name":"Archived","color":"red"}]}`),
		Actor:   schema.ActorUser,
	})
	if err != nil {
		t.Fatalf("UpdateField config: %v", err)
	}
	updatedCfg, err := parseFieldConfig(updated.Type, updated.Config)
	if err != nil {
		t.Fatalf("parse updated config: %v", err)
	}
	if len(updatedCfg.Options) != 2 || updatedCfg.Options[0].ID != firstID || updatedCfg.Options[0].Name != "Draft v2" {
		t.Fatalf("options after update = %+v", updatedCfg.Options)
	}
	if updatedCfg.Options[1].ID == firstID || !ids.Valid(updatedCfg.Options[1].ID) {
		t.Fatalf("new option id = %q", updatedCfg.Options[1].ID)
	}

	if _, err := f.databases.CreateField(f.ctx, CreateFieldInput{
		DatabaseID: database.ID, Name: "BadText", Type: FieldTypeText,
		Config: json.RawMessage(`{"options":[]}`), Actor: schema.ActorUser,
	}); !errors.Is(err, errDBValidation) {
		t.Fatalf("text-with-options error = %v, want validation error", err)
	}
	if _, err := f.databases.CreateField(f.ctx, CreateFieldInput{
		DatabaseID: database.ID, Name: "BadRelation", Type: FieldTypeRelation,
		Config: json.RawMessage(`{"targets":[]}`), Actor: schema.ActorUser,
	}); !errors.Is(err, errDBValidation) {
		t.Fatalf("empty relation targets error = %v, want validation error", err)
	}
	if _, err := f.databases.CreateField(f.ctx, CreateFieldInput{
		DatabaseID: database.ID, Name: "BadRelation2", Type: FieldTypeRelation,
		Config: json.RawMessage(`{"targets":["bogus"]}`), Actor: schema.ActorUser,
	}); !errors.Is(err, errDBValidation) {
		t.Fatalf("bogus relation target error = %v, want validation error", err)
	}
	relationField := f.createField(database.ID, "Refs", FieldTypeRelation,
		`{"targets":["paper","note"]}`)
	relationCfg, err := parseFieldConfig(relationField.Type, relationField.Config)
	if err != nil {
		t.Fatalf("parse relation config: %v", err)
	}
	if len(relationCfg.Targets) != 2 {
		t.Fatalf("relation targets = %v", relationCfg.Targets)
	}

	if _, err := f.databases.CreateField(f.ctx, CreateFieldInput{
		DatabaseID: database.ID, Name: "", Type: FieldTypeText, Actor: schema.ActorUser,
	}); !errors.Is(err, errDBValidation) {
		t.Fatalf("empty field name error = %v, want validation error", err)
	}
	longName := ""
	for range maxFieldNameRunes + 1 {
		longName += "x"
	}
	if _, err := f.databases.CreateField(f.ctx, CreateFieldInput{
		DatabaseID: database.ID, Name: longName, Type: FieldTypeText, Actor: schema.ActorUser,
	}); !errors.Is(err, errDBValidation) {
		t.Fatalf("long field name error = %v, want validation error", err)
	}
}

func TestDBDatabaseSoftDeleteDoesNotCascade(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Trash")
	f.createField(database.ID, "Notes", FieldTypeText, "")
	f.createRecord(database.ID, map[string]any{f.titleField(database.ID).ID: "row"})

	if err := f.databases.SoftDeleteDatabase(f.ctx, database.ID, schema.ActorUser); err != nil {
		t.Fatalf("SoftDeleteDatabase: %v", err)
	}
	loaded, err := f.databases.GetDatabase(f.ctx, database.ID)
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	if loaded.DeletedAt == nil {
		t.Fatalf("database was not soft-deleted")
	}
	live, err := f.databases.ListDatabases(f.ctx, false)
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("live databases = %d, want 0", len(live))
	}
	all, err := f.databases.ListDatabases(f.ctx, true)
	if err != nil {
		t.Fatalf("ListDatabases(true): %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("all databases = %d, want 1", len(all))
	}
	if got := len(mustFields(t, f, database.ID)); got != 2 {
		t.Fatalf("fields after database delete = %d, want 2 (not cascaded)", got)
	}
	if got := f.countRows(`SELECT count(*) FROM db_records WHERE database_id = ?`, database.ID); got != 1 {
		t.Fatalf("records after database delete = %d, want 1 (not cascaded)", got)
	}
	if _, err := f.records.ListRecords(f.ctx, RecordQuery{DatabaseID: database.ID}); !errors.Is(err, errs.ErrEntityDeleted) {
		t.Fatalf("ListRecords on deleted database error = %v, want ErrEntityDeleted", err)
	}
	if got := f.countActivities(schema.EventEntityDeleted, schema.EntityResearchDatabase); got != 1 {
		t.Fatalf("entity.deleted research_database = %d, want 1", got)
	}

	if err := f.databases.RestoreDatabase(f.ctx, database.ID, schema.ActorUser); err != nil {
		t.Fatalf("RestoreDatabase: %v", err)
	}
	live, err = f.databases.ListDatabases(f.ctx, false)
	if err != nil {
		t.Fatalf("ListDatabases after restore: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("live databases after restore = %d, want 1", len(live))
	}
	page, err := f.records.ListRecords(f.ctx, RecordQuery{DatabaseID: database.ID})
	if err != nil {
		t.Fatalf("ListRecords after restore: %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("records after restore = %d, want 1", page.Total)
	}
	if got := f.countActivities(schema.EventEntityRestored, schema.EntityResearchDatabase); got != 1 {
		t.Fatalf("entity.restored research_database = %d, want 1", got)
	}
}

func TestDBRenameDatabase(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Old")

	renamed, err := f.databases.RenameDatabase(f.ctx, database.ID, "  New Name  ", schema.ActorMCP)
	if err != nil {
		t.Fatalf("RenameDatabase: %v", err)
	}
	if renamed.Name != "New Name" {
		t.Fatalf("renamed name = %q", renamed.Name)
	}
	if _, err := f.databases.RenameDatabase(f.ctx, database.ID, "   ", schema.ActorUser); !errors.Is(err, errDBValidation) {
		t.Fatalf("empty rename error = %v, want validation error", err)
	}
	if got := f.countActivities(schema.EventEntityUpdated, schema.EntityResearchDatabase); got != 1 {
		t.Fatalf("entity.updated research_database = %d, want 1", got)
	}
	var rawPayload string
	if err := f.handle.DB().QueryRowContext(f.ctx,
		`SELECT payload FROM activities WHERE event_type = 'entity.updated' AND entity_type = 'research_database'`).
		Scan(&rawPayload); err != nil {
		t.Fatalf("load rename payload: %v", err)
	}
	var payload events.Payload
	if err := json.Unmarshal([]byte(rawPayload), &payload); err != nil {
		t.Fatalf("decode payload %q: %v", rawPayload, err)
	}
	if payload.Summary == "" || payload.Old == nil || payload.New == nil {
		t.Fatalf("rename payload = %+v", payload)
	}
	if name, ok := (*payload.New)["name"]; !ok || name != "New Name" {
		t.Fatalf("rename payload new = %+v", *payload.New)
	}
}

func TestDBRestoreFieldRejectsMissingField(t *testing.T) {
	f := dbNewFixture(t)
	if err := f.databases.RestoreField(f.ctx, ids.MustNew(f.fx.Clock), schema.ActorUser); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("restore missing field error = %v, want sql.ErrNoRows", err)
	}
}

func TestDBFieldListNeverLeaksOtherDatabases(t *testing.T) {
	f := dbNewFixture(t)
	first := f.createDatabase("First")
	second := f.createDatabase("Second")
	f.createField(second.ID, "OnlySecond", FieldTypeText, "")

	fields := mustFields(t, f, first.ID)
	for _, field := range fields {
		if field.DatabaseID != first.ID {
			t.Fatalf("ListFields(%s) returned field of %s", first.ID, field.DatabaseID)
		}
		if field.Name == "OnlySecond" {
			t.Fatalf("ListFields(%s) leaked a field of another database", first.ID)
		}
	}
}
