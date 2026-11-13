package notes

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
)

func dbRawValues(t *testing.T, value any) json.RawMessage {
	t.Helper()
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	return encoded
}

func dbValues(t *testing.T, record *Record) map[string]json.RawMessage {
	t.Helper()
	values, err := decodeRecordValues(record.Values)
	if err != nil {
		t.Fatalf("decode record values: %v", err)
	}
	return values
}

func TestRecordCreateAutoNote(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Rows")
	titleID := f.titleField(database.ID).ID

	record := f.createRecord(database.ID, map[string]any{titleID: "First row"})
	if record.NoteID == "" {
		t.Fatalf("record has no backing note")
	}
	if !ids.Valid(record.NoteID) {
		t.Fatalf("backing note id %q is not a UUIDv7", record.NoteID)
	}
	kind, title, filePath, deletedAt := f.noteRow(record.NoteID)
	if kind != string(schema.NoteDBRecord) {
		t.Fatalf("backing note kind = %q", kind)
	}
	if title != "First row" {
		t.Fatalf("backing note title = %q", title)
	}
	if deletedAt.Valid {
		t.Fatalf("fresh backing note is deleted")
	}
	abs, err := f.fx.Workspace.Paths().Abs(filePath)
	if err != nil {
		t.Fatalf("resolve note path: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("note body file %s: %v", abs, err)
	}
	if values := dbValues(t, record); titleValue(values, titleID) != "First row" {
		t.Fatalf("stored values = %s", record.Values)
	}
	if got := f.countRows(`SELECT count(*) FROM db_records WHERE id = ? AND title = ?`, record.ID, "First row"); got != 1 {
		t.Fatalf("db_records.title projection missing")
	}
	if got := f.countActivities(schema.EventEntityCreated, schema.EntityDBRecord); got != 1 {
		t.Fatalf("entity.created db_record = %d, want 1", got)
	}
	if got := f.countActivities(schema.EventEntityCreated, schema.EntityNote); got != 1 {
		t.Fatalf("entity.created note = %d, want 1", got)
	}
}

func TestRecordCreateWithExistingNote(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Promote")
	titleID := f.titleField(database.ID).ID
	noteID := f.createNote(schema.NoteScratchpad, "From scratch")

	record, err := f.createRecordForNote(database.ID, noteID, nil)
	if err != nil {
		t.Fatalf("create record for note: %v", err)
	}
	if record.NoteID != noteID {
		t.Fatalf("record note id = %q, want %q", record.NoteID, noteID)
	}
	if values := dbValues(t, record); titleValue(values, titleID) != "From scratch" {
		t.Fatalf("title value after promote = %s", record.Values)
	}
	kind, title, _, _ := f.noteRow(noteID)
	if kind != string(schema.NoteScratchpad) || title != "From scratch" {
		t.Fatalf("promoted note = (%s, %q), want scratchpad (D-05-04)", kind, title)
	}
	if _, err := f.createRecordForNote(database.ID, noteID, nil); !errors.Is(err, errDBValidation) {
		t.Fatalf("second record for the same note error = %v, want validation error", err)
	}
	if got := f.countRows(`SELECT count(*) FROM db_records WHERE note_id = ?`, noteID); got != 1 {
		t.Fatalf("records for note = %d, want 1", got)
	}

	if _, err := f.createRecordForNote(database.ID, ids.MustNew(f.fx.Clock), nil); !errors.Is(err, errDBValidation) {
		t.Fatalf("record for a missing note error = %v, want validation error", err)
	}
}

func TestRecordUpdateValuesValidation(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Validate")
	titleID := f.titleField(database.ID).ID
	textField := f.createField(database.ID, "Text", FieldTypeText, "")
	numberField := f.createField(database.ID, "Score", FieldTypeNumber, "")
	dateField := f.createField(database.ID, "When", FieldTypeDate, "")
	selectField := f.createField(database.ID, "Kind", FieldTypeSelect, `{"options":[{"name":"A"},{"name":"B"}]}`)
	selectCfg, err := parseFieldConfig(selectField.Type, selectField.Config)
	if err != nil {
		t.Fatalf("select config: %v", err)
	}
	optionA, optionB := selectCfg.Options[0].ID, selectCfg.Options[1].ID
	multiField := f.createField(database.ID, "Multi", FieldTypeMultiSelect, `{"options":[{"name":"A"},{"name":"B"}]}`)
	multiCfg, _ := parseFieldConfig(multiField.Type, multiField.Config)
	multiA := multiCfg.Options[0].ID
	checkboxField := f.createField(database.ID, "Done", FieldTypeCheckbox, "")
	urlField := f.createField(database.ID, "Link", FieldTypeURL, "")
	relationField := f.createField(database.ID, "Ref", FieldTypeRelation, `{"targets":["note"]}`)
	tagsField := f.createField(database.ID, "Tags", FieldTypeTags, "")

	targetNote := f.createNote(schema.NoteScratchpad, "Target")
	tagID := f.createTag("valid")

	record := f.createRecord(database.ID, map[string]any{
		titleID:          "Row",
		textField.ID:     "hello",
		numberField.ID:   5,
		dateField.ID:     "2026-01-10",
		selectField.ID:   optionA,
		multiField.ID:    []string{multiA},
		checkboxField.ID: true,
		urlField.ID:      "https://example.test",
		relationField.ID: []map[string]string{{"type": "note", "id": targetNote}},
		tagsField.ID:     []string{tagID},
	})
	before := string(record.Values)

	deletedTag := f.createTag("gone")
	if _, err := f.handle.DB().ExecContext(f.ctx,
		`UPDATE tags SET deleted_at = ? WHERE id = ?`, clock.Format(f.fx.Clock.Now()), deletedTag); err != nil {
		t.Fatalf("soft delete tag: %v", err)
	}

	cases := []struct {
		name    string
		fieldID string
		value   any
	}{
		{"number with string", numberField.ID, "not-a-number"},
		{"number with bool", numberField.ID, true},
		{"text with number", textField.ID, 7},
		{"date not strict", dateField.ID, "2026-1-1"},
		{"date impossible", dateField.ID, "2026-02-30"},
		{"select unknown option", selectField.ID, "11111111-1111-7111-8111-111111111111"},
		{"multi duplicate", multiField.ID, []string{multiA, multiA}},
		{"multi unknown option", multiField.ID, []string{"11111111-1111-7111-8111-111111111111"}},
		{"checkbox not bool", checkboxField.ID, "yes"},
		{"relation wrong target type", relationField.ID, []map[string]string{{"type": "paper", "id": ids.MustNew(f.fx.Clock)}}},
		{"relation missing target", relationField.ID, []map[string]string{{"type": "note", "id": ids.MustNew(f.fx.Clock)}}},
		{"relation self", relationField.ID, []map[string]string{{"type": "db_record", "id": record.ID}}},
		{"tags deleted tag", tagsField.ID, []string{deletedTag}},
		{"unknown field", ids.MustNew(f.fx.Clock), "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.records.UpdateValues(f.ctx, UpdateValuesInput{
				RecordID: record.ID,
				Values:   dbRawValues(t, map[string]any{tc.fieldID: tc.value}),
				Actor:    schema.ActorUser,
			})
			if !errors.Is(err, errDBValidation) {
				t.Fatalf("error = %v, want validation error", err)
			}
			loaded, err := f.records.GetRecord(f.ctx, record.ID)
			if err != nil {
				t.Fatalf("GetRecord: %v", err)
			}
			if string(loaded.Values) != before {
				t.Fatalf("values changed after rejected update:\n got %s\nwant %s", loaded.Values, before)
			}
		})
	}

	// Clearing with null and a valid update both work, and unknown stored ids
	// stay untouched.
	if _, err := f.records.UpdateValues(f.ctx, UpdateValuesInput{
		RecordID: record.ID,
		Values:   dbRawValues(t, map[string]any{numberField.ID: nil, selectField.ID: optionB}),
		Actor:    schema.ActorUser,
	}); err != nil {
		t.Fatalf("valid update: %v", err)
	}
	loaded, err := f.records.GetRecord(f.ctx, record.ID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	values := dbValues(t, loaded)
	if _, ok := values[numberField.ID]; ok {
		t.Fatalf("null did not clear the number value: %s", loaded.Values)
	}
	if got := titleValue(values, selectField.ID); got != optionB {
		t.Fatalf("select value = %q, want %q", got, optionB)
	}
}

func TestRecordTitleProjectionEmitsOneNoteUpdated(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Titles")
	titleID := f.titleField(database.ID).ID
	record := f.createRecord(database.ID, map[string]any{titleID: "Old"})

	if got := f.countRows(`SELECT count(*) FROM activities WHERE event_type = 'note.updated'`); got != 0 {
		t.Fatalf("note.updated after create = %d, want 0", got)
	}
	if _, err := f.records.UpdateValues(f.ctx, UpdateValuesInput{
		RecordID: record.ID,
		Values:   dbRawValues(t, map[string]any{titleID: "New"}),
		Actor:    schema.ActorMCP,
	}); err != nil {
		t.Fatalf("UpdateValues: %v", err)
	}
	_, noteTitle, _, _ := f.noteRow(record.NoteID)
	if noteTitle != "New" {
		t.Fatalf("notes.title = %q, want New", noteTitle)
	}
	if got := f.countRows(`SELECT count(*) FROM db_records WHERE id = ? AND title = ?`, record.ID, "New"); got != 1 {
		t.Fatalf("db_records.title not projected")
	}
	if got := f.countRows(
		`SELECT count(*) FROM activities WHERE event_type = 'note.updated' AND entity_id = ?`, record.NoteID); got != 1 {
		t.Fatalf("note.updated rows = %d, want 1", got)
	}

	// A second edit the same day merges into the same activity row.
	if _, err := f.records.UpdateValues(f.ctx, UpdateValuesInput{
		RecordID: record.ID,
		Values:   dbRawValues(t, map[string]any{titleID: "Newer"}),
		Actor:    schema.ActorUser,
	}); err != nil {
		t.Fatalf("second UpdateValues: %v", err)
	}
	if got := f.countRows(
		`SELECT count(*) FROM activities WHERE event_type = 'note.updated' AND entity_id = ?`, record.NoteID); got != 1 {
		t.Fatalf("merged note.updated rows = %d, want 1", got)
	}
	var rawPayload string
	if err := f.handle.DB().QueryRowContext(f.ctx,
		`SELECT payload FROM activities WHERE event_type = 'note.updated' AND entity_id = ?`, record.NoteID).
		Scan(&rawPayload); err != nil {
		t.Fatalf("load payload: %v", err)
	}
	var payload struct {
		SaveCount int `json:"save_count"`
	}
	if err := json.Unmarshal([]byte(rawPayload), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.SaveCount != 2 {
		t.Fatalf("save_count = %d, want 2", payload.SaveCount)
	}
}

func TestRecordRelationSideEffects(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Relations")
	relationField := f.createField(database.ID, "Refs", FieldTypeRelation, `{"targets":["note","idea"]}`)
	noteOne := f.createNote(schema.NoteScratchpad, "One")
	ideaID := ids.MustNew(f.fx.Clock)
	at := clock.Format(f.fx.Clock.Now())
	if _, err := f.handle.DB().ExecContext(f.ctx,
		`INSERT INTO ideas(id, title, created_at, updated_at) VALUES (?, 'Idea', ?, ?)`,
		ideaID, at, at); err != nil {
		t.Fatalf("seed idea: %v", err)
	}

	record := f.createRecord(database.ID, map[string]any{
		relationField.ID: []map[string]string{{"type": "note", "id": noteOne}},
	})
	// Edges are written with the record's backing note as source, and a note
	// target maps to the registered `references` relation (SPEC-05 §5.5).
	if got := f.countRows(
		`SELECT count(*) FROM entity_relations
		  WHERE src_type = 'note' AND src_id = ? AND relation = 'references' AND dst_type = 'note' AND dst_id = ?`,
		record.NoteID, noteOne); got != 1 {
		t.Fatalf("relation rows after create = %d, want 1 note->note references edge", got)
	}

	if _, err := f.records.UpdateValues(f.ctx, UpdateValuesInput{
		RecordID: record.ID,
		Values:   dbRawValues(t, map[string]any{relationField.ID: []map[string]string{{"type": "idea", "id": ideaID}}}),
		Actor:    schema.ActorUser,
	}); err != nil {
		t.Fatalf("UpdateValues relation swap: %v", err)
	}
	if got := f.countRows(`SELECT count(*) FROM entity_relations WHERE src_type = 'note' AND src_id = ?`, record.NoteID); got != 1 {
		t.Fatalf("relation rows after swap = %d, want 1", got)
	}
	if got := f.countRows(
		`SELECT count(*) FROM entity_relations
		  WHERE src_type = 'note' AND src_id = ? AND relation = 'related_to' AND dst_type = 'idea' AND dst_id = ?`,
		record.NoteID, ideaID); got != 1 {
		t.Fatalf("idea target must use related_to")
	}
	if got := f.countRows(`SELECT count(*) FROM entity_relations WHERE src_type = 'db_record'`); got != 0 {
		t.Fatalf("records must not be relation sources, found %d", got)
	}

	if _, err := f.records.UpdateValues(f.ctx, UpdateValuesInput{
		RecordID: record.ID,
		Values:   dbRawValues(t, map[string]any{relationField.ID: []map[string]string{}}),
		Actor:    schema.ActorUser,
	}); err != nil {
		t.Fatalf("UpdateValues relation clear: %v", err)
	}
	if got := f.countRows(`SELECT count(*) FROM entity_relations WHERE src_id = ?`, record.NoteID); got != 0 {
		t.Fatalf("relation rows after clear = %d, want 0", got)
	}
	if got := f.countActivities(schema.EventRelationAdded, schema.EntityNote); got != 2 {
		t.Fatalf("relation.added events = %d, want 2", got)
	}
	if got := f.countActivities(schema.EventRelationRemoved, schema.EntityNote); got != 2 {
		t.Fatalf("relation.removed events = %d, want 2", got)
	}
}

func TestRecordTagsSideEffectsAndSetTags(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Tagging")
	tagsField := f.createField(database.ID, "Tags", FieldTypeTags, "")
	tagOne := f.createTag("one")
	tagTwo := f.createTag("two")

	record := f.createRecord(database.ID, map[string]any{tagsField.ID: []string{tagOne}})
	if got := f.countRows(
		`SELECT count(*) FROM entity_tags WHERE entity_type = 'db_record' AND entity_id = ?`, record.ID); got != 1 {
		t.Fatalf("entity_tags after create = %d, want 1", got)
	}

	if _, err := f.records.UpdateValues(f.ctx, UpdateValuesInput{
		RecordID: record.ID,
		Values:   dbRawValues(t, map[string]any{tagsField.ID: []string{tagOne, tagTwo}}),
		Actor:    schema.ActorUser,
	}); err != nil {
		t.Fatalf("UpdateValues tags: %v", err)
	}
	if got := f.countRows(`SELECT count(*) FROM entity_tags WHERE entity_id = ?`, record.ID); got != 2 {
		t.Fatalf("entity_tags after add = %d, want 2", got)
	}

	if err := f.records.SetTags(f.ctx, record.ID, []string{tagTwo}, schema.ActorUser); err != nil {
		t.Fatalf("SetTags: %v", err)
	}
	if got := f.countRows(
		`SELECT count(*) FROM entity_tags WHERE entity_id = ? AND tag_id = ?`, record.ID, tagTwo); got != 1 {
		t.Fatalf("entity_tags after SetTags = %d, want 1", got)
	}
	if got := f.countRows(`SELECT count(*) FROM entity_tags WHERE entity_id = ?`, record.ID); got != 1 {
		t.Fatalf("entity_tags total after SetTags = %d, want 1", got)
	}
	if err := f.records.SetTags(f.ctx, record.ID, []string{tagTwo}, schema.ActorMCP); err != nil {
		t.Fatalf("SetTags idempotent: %v", err)
	}
	if got := f.countRows(`SELECT count(*) FROM entity_tags WHERE entity_id = ?`, record.ID); got != 1 {
		t.Fatalf("SetTags is not idempotent: %d rows", got)
	}
	if got := f.countActivities(schema.EventEntityCreated, schema.EntityDBRecord); got != 1 {
		t.Fatalf("tag updates emitted extra record events: %d", got)
	}

	// A soft-deleted tag is rejected by both write paths.
	if _, err := f.handle.DB().ExecContext(f.ctx,
		`UPDATE tags SET deleted_at = ? WHERE id = ?`, clock.Format(f.fx.Clock.Now()), tagTwo); err != nil {
		t.Fatalf("soft delete tag: %v", err)
	}
	if err := f.records.SetTags(f.ctx, record.ID, []string{tagTwo}, schema.ActorUser); !errors.Is(err, errDBValidation) {
		t.Fatalf("SetTags with deleted tag error = %v, want validation error", err)
	}
	if _, err := f.records.UpdateValues(f.ctx, UpdateValuesInput{
		RecordID: record.ID,
		Values:   dbRawValues(t, map[string]any{tagsField.ID: []string{tagTwo}}),
		Actor:    schema.ActorUser,
	}); !errors.Is(err, errDBValidation) {
		t.Fatalf("UpdateValues with deleted tag error = %v, want validation error", err)
	}
	if got := f.countRows(`SELECT count(*) FROM entity_tags WHERE entity_id = ?`, record.ID); got != 1 {
		t.Fatalf("existing tag rows must survive tag deletion, got %d", got)
	}
}

func TestRecordDeleteRestoreNoteSemantics(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Detach")
	titleID := f.titleField(database.ID).ID
	record := f.createRecord(database.ID, map[string]any{titleID: "Keep me"})

	if err := f.records.SoftDeleteRecord(f.ctx, record.ID, false, schema.ActorUser); err != nil {
		t.Fatalf("SoftDeleteRecord(false): %v", err)
	}
	deleted, err := f.records.GetRecord(f.ctx, record.ID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if deleted.DeletedAt == nil {
		t.Fatalf("record was not soft-deleted")
	}
	if _, _, _, noteDeleted := f.noteRow(record.NoteID); noteDeleted.Valid {
		t.Fatalf("detach must keep the backing note")
	}
	page, err := f.records.ListRecords(f.ctx, RecordQuery{DatabaseID: database.ID})
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("live records = %d, want 0", page.Total)
	}
	page, err = f.records.ListRecords(f.ctx, RecordQuery{DatabaseID: database.ID, IncludeDeleted: true})
	if err != nil {
		t.Fatalf("ListRecords(includeDeleted): %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("records including deleted = %d, want 1", page.Total)
	}

	if err := f.records.RestoreRecord(f.ctx, record.ID, schema.ActorUser); err != nil {
		t.Fatalf("RestoreRecord: %v", err)
	}
	if _, _, _, noteDeleted := f.noteRow(record.NoteID); noteDeleted.Valid {
		t.Fatalf("backing note is deleted after restore")
	}

	if err := f.records.SoftDeleteRecord(f.ctx, record.ID, true, schema.ActorUser); err != nil {
		t.Fatalf("SoftDeleteRecord(true): %v", err)
	}
	_, _, _, noteDeleted := f.noteRow(record.NoteID)
	if !noteDeleted.Valid {
		t.Fatalf("deleteNote did not delete the backing note")
	}
	if err := f.records.RestoreRecord(f.ctx, record.ID, schema.ActorUser); err != nil {
		t.Fatalf("RestoreRecord with deleted note: %v", err)
	}
	restored, err := f.records.GetRecord(f.ctx, record.ID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if restored.DeletedAt != nil {
		t.Fatalf("record is still deleted")
	}
	if _, _, _, noteDeleted := f.noteRow(record.NoteID); noteDeleted.Valid {
		t.Fatalf("restore did not cascade to the still-deleted note")
	}
	if got := f.countActivities(schema.EventEntityDeleted, schema.EntityNote); got != 1 {
		t.Fatalf("entity.deleted note = %d, want 1", got)
	}
	if got := f.countActivities(schema.EventEntityDeleted, schema.EntityDBRecord); got != 2 {
		t.Fatalf("entity.deleted db_record = %d, want 2", got)
	}
}

func TestRecordPaginationTotalAndIsolation(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Pages")
	titleID := f.titleField(database.ID).ID
	other := f.createDatabase("Other")
	otherField := f.createField(other.ID, "OnlyOther", FieldTypeText, "")

	created := make([]*Record, 0, 5)
	for i := range 5 {
		created = append(created, f.createRecord(database.ID, map[string]any{titleID: string(rune('a' + i))}))
	}

	page, err := f.records.ListRecords(f.ctx, RecordQuery{DatabaseID: database.ID, Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("ListRecords page: %v", err)
	}
	if page.Total != 5 || len(page.Records) != 2 {
		t.Fatalf("page = total %d rows %d, want 5/2", page.Total, len(page.Records))
	}
	if page.Records[0].ID != created[1].ID || page.Records[1].ID != created[2].ID {
		t.Fatalf("natural row order not preserved: %s, %s", page.Records[0].ID, page.Records[1].ID)
	}
	page, err = f.records.ListRecords(f.ctx, RecordQuery{DatabaseID: database.ID})
	if err != nil {
		t.Fatalf("ListRecords default: %v", err)
	}
	if page.Total != 5 || len(page.Records) != 5 {
		t.Fatalf("default page = %d/%d, want 5/5 (default limit 100)", page.Total, len(page.Records))
	}
	page, err = f.records.ListRecords(f.ctx, RecordQuery{DatabaseID: database.ID, Limit: 1000})
	if err != nil {
		t.Fatalf("ListRecords cap: %v", err)
	}
	if len(page.Records) != 5 {
		t.Fatalf("capped page rows = %d, want 5", len(page.Records))
	}
	page, err = f.records.ListRecords(f.ctx, RecordQuery{DatabaseID: database.ID, Offset: 10})
	if err != nil {
		t.Fatalf("ListRecords offset: %v", err)
	}
	if page.Total != 5 || len(page.Records) != 0 {
		t.Fatalf("offset page = %d/%d, want 5/0", page.Total, len(page.Records))
	}

	if _, err := f.records.ListRecords(f.ctx, RecordQuery{DatabaseID: other.ID}); err != nil {
		t.Fatalf("ListRecords other: %v", err)
	}
	page, err = f.records.ListRecords(f.ctx, RecordQuery{DatabaseID: other.ID})
	if err != nil {
		t.Fatalf("ListRecords other: %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("other database leaked %d records", page.Total)
	}
	if _, err := f.records.UpdateValues(f.ctx, UpdateValuesInput{
		RecordID: created[0].ID,
		Values:   dbRawValues(t, map[string]any{otherField.ID: "x"}),
		Actor:    schema.ActorUser,
	}); !errors.Is(err, errDBValidation) {
		t.Fatalf("foreign field update error = %v, want validation error", err)
	}
	if _, err := f.records.GetRecord(f.ctx, ids.MustNew(f.fx.Clock)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing record error = %v, want sql.ErrNoRows", err)
	}
}

func TestRecordSortMultiKeyNullsLast(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Sorts")
	titleID := f.titleField(database.ID).ID
	scoreField := f.createField(database.ID, "Score", FieldTypeNumber, "")

	high := f.createRecord(database.ID, map[string]any{titleID: "b", scoreField.ID: 2})
	lowC := f.createRecord(database.ID, map[string]any{titleID: "c", scoreField.ID: 1})
	lowA := f.createRecord(database.ID, map[string]any{titleID: "a", scoreField.ID: 1})
	empty := f.createRecord(database.ID, map[string]any{titleID: "z"})

	idsOf := func(page RecordPage) []string {
		out := make([]string, 0, len(page.Records))
		for _, record := range page.Records {
			out = append(out, record.ID)
		}
		return out
	}
	want := func(got []string, expected ...*Record) {
		t.Helper()
		if len(got) != len(expected) {
			t.Fatalf("order = %v, want %d rows", got, len(expected))
		}
		for i, record := range expected {
			if got[i] != record.ID {
				t.Fatalf("order[%d] = %s, want %s (full %v)", i, got[i], record.ID, got)
			}
		}
	}

	page, err := f.records.ListRecords(f.ctx, RecordQuery{
		DatabaseID: database.ID,
		Sorts:      []SortKey{{FieldID: scoreField.ID}, {FieldID: SortTitle}},
	})
	if err != nil {
		t.Fatalf("ListRecords sort asc: %v", err)
	}
	want(idsOf(page), lowA, lowC, high, empty)

	page, err = f.records.ListRecords(f.ctx, RecordQuery{
		DatabaseID: database.ID,
		Sorts:      []SortKey{{FieldID: scoreField.ID, Desc: true}, {FieldID: SortTitle}},
	})
	if err != nil {
		t.Fatalf("ListRecords sort desc: %v", err)
	}
	want(idsOf(page), high, lowA, lowC, empty)

	page, err = f.records.ListRecords(f.ctx, RecordQuery{
		DatabaseID: database.ID,
		Sorts:      []SortKey{{FieldID: SortTitle, Desc: true}},
	})
	if err != nil {
		t.Fatalf("ListRecords title desc: %v", err)
	}
	want(idsOf(page), empty, lowC, high, lowA)
}

func TestRecordReorder(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Order")
	titleID := f.titleField(database.ID).ID
	first := f.createRecord(database.ID, map[string]any{titleID: "one"})
	second := f.createRecord(database.ID, map[string]any{titleID: "two"})
	third := f.createRecord(database.ID, map[string]any{titleID: "three"})

	page, err := f.records.ListRecords(f.ctx, RecordQuery{DatabaseID: database.ID})
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(page.Records) != 3 || page.Records[0].ID != first.ID {
		t.Fatalf("unexpected natural order")
	}
	if err := f.records.ReorderRecords(f.ctx, database.ID, []string{third.ID, second.ID, first.ID}, schema.ActorUser); err != nil {
		t.Fatalf("ReorderRecords: %v", err)
	}
	page, err = f.records.ListRecords(f.ctx, RecordQuery{DatabaseID: database.ID})
	if err != nil {
		t.Fatalf("ListRecords after reorder: %v", err)
	}
	if page.Records[0].ID != third.ID || page.Records[1].ID != second.ID || page.Records[2].ID != first.ID {
		t.Fatalf("reordered rows = %s, %s, %s", page.Records[0].ID, page.Records[1].ID, page.Records[2].ID)
	}
	if err := f.records.ReorderRecords(f.ctx, database.ID, []string{third.ID, second.ID}, schema.ActorUser); !errors.Is(err, errDBValidation) {
		t.Fatalf("partial record permutation error = %v, want validation error", err)
	}
	if err := f.records.ReorderRecords(f.ctx, database.ID, []string{third.ID, third.ID, first.ID}, schema.ActorUser); !errors.Is(err, errDBValidation) {
		t.Fatalf("duplicate record permutation error = %v, want validation error", err)
	}
	if got := f.countActivities(schema.EventEntityUpdated, schema.EntityDBRecord); got != 0 {
		t.Fatalf("reorder emitted %d db_record update events, want 0", got)
	}
}

func TestRecordInjectionStringIsDataOnly(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Injection")
	titleID := f.titleField(database.ID).ID
	payload := `Robert'); DROP TABLE db_records;--`
	record := f.createRecord(database.ID, map[string]any{titleID: payload})

	page, err := f.records.ListRecords(f.ctx, RecordQuery{
		DatabaseID: database.ID,
		Filter:     &FilterNode{FieldID: titleID, Operator: "eq", Value: dbRawValues(t, payload)},
	})
	if err != nil {
		t.Fatalf("ListRecords with injection payload: %v", err)
	}
	if page.Total != 1 || page.Records[0].ID != record.ID {
		t.Fatalf("injection filter result = total %d", page.Total)
	}
	if got := f.countRows(`SELECT count(*) FROM db_records`); got != 1 {
		t.Fatalf("db_records rows = %d, want 1 (table must survive)", got)
	}
	if values := dbValues(t, record); titleValue(values, titleID) != payload {
		t.Fatalf("payload was altered: %s", record.Values)
	}
	if _, err := f.records.ListRecords(f.ctx, RecordQuery{
		DatabaseID: database.ID,
		Filter:     &FilterNode{FieldID: titleID, Operator: "contains", Value: dbRawValues(t, "DROP TABLE")},
	}); err != nil {
		t.Fatalf("contains injection payload: %v", err)
	}
}

func TestRecordUpdateOnDeletedRecordRejected(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Deleted")
	titleID := f.titleField(database.ID).ID
	record := f.createRecord(database.ID, map[string]any{titleID: "x"})
	if err := f.records.SoftDeleteRecord(f.ctx, record.ID, false, schema.ActorUser); err != nil {
		t.Fatalf("SoftDeleteRecord: %v", err)
	}
	if _, err := f.records.UpdateValues(f.ctx, UpdateValuesInput{
		RecordID: record.ID,
		Values:   dbRawValues(t, map[string]any{titleID: "y"}),
		Actor:    schema.ActorUser,
	}); !errors.Is(err, errs.ErrEntityDeleted) {
		t.Fatalf("update deleted record error = %v, want ErrEntityDeleted", err)
	}
	if err := f.records.SetTags(f.ctx, record.ID, nil, schema.ActorUser); !errors.Is(err, errs.ErrEntityDeleted) {
		t.Fatalf("SetTags deleted record error = %v, want ErrEntityDeleted", err)
	}
}
