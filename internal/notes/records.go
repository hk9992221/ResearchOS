package notes

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/notefile"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/trash"
)

// Record pagination limits (SPEC-05 §5.3).
const (
	defaultRecordLimit = 100
	maxRecordLimit     = 500
	recordColumns      = `id, database_id, note_id, title, data, created_at, updated_at, deleted_at`
)

// recordService implements RecordService (SPEC-05 §5.3).
type recordService struct {
	db        *sql.DB
	projectID string
	events    events.Writer
	trash     *trash.Service
	notes     *notefile.Store
	rels      *relations.Service
	clock     clock.Clock
}

var _ RecordService = (*recordService)(nil)

// NewRecordService binds the record service to one project database. noteStore
// creates the backing db_record notes and relSvc owns entity_relations.
func NewRecordService(db *sql.DB, projectID string, writer events.Writer, trashSvc *trash.Service, noteStore *notefile.Store, relSvc *relations.Service, opt ServiceOptions) RecordService {
	c := opt.Clock
	if c == nil {
		c = clock.System{}
	}
	return &recordService{
		db:        db,
		projectID: projectID,
		events:    writer,
		trash:     trashSvc,
		notes:     noteStore,
		rels:      relSvc,
		clock:     c,
	}
}

// ListRecords validates the filter/sort against the live schema and returns
// one page plus the unpaged total (SPEC-05 §5.3).
func (s *recordService) ListRecords(ctx context.Context, q RecordQuery) (RecordPage, error) {
	if q.DatabaseID == "" {
		return RecordPage{}, dbValidationf("record query needs a database id")
	}
	database, err := loadDatabase(ctx, s.db, q.DatabaseID)
	if err != nil {
		return RecordPage{}, err
	}
	if database.DeletedAt != nil {
		return RecordPage{}, fmt.Errorf("notes: list records: %w", errs.ErrEntityDeleted)
	}
	fields, err := dbLoadFields(ctx, s.db, q.DatabaseID, false)
	if err != nil {
		return RecordPage{}, err
	}
	fieldMap := fieldsByID(fields)
	predicate, err := compileFilter(q.Filter, fieldMap)
	if err != nil {
		return RecordPage{}, err
	}
	specs := compileSorts(q.Sorts, fieldMap)

	query := `SELECT ` + recordColumns + ` FROM db_records WHERE database_id = ?`
	if !q.IncludeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	query += ` ORDER BY sort_order, id`
	rows, err := s.db.QueryContext(ctx, query, q.DatabaseID)
	if err != nil {
		return RecordPage{}, fmt.Errorf("notes: list records: %w", err)
	}
	defer rows.Close()

	rowsByID := make(map[string]*Record)
	var candidates []*sortRow
	for rows.Next() {
		record, title, err := scanRecordFields(rows)
		if err != nil {
			return RecordPage{}, fmt.Errorf("notes: list records: %w", err)
		}
		values, err := decodeRecordValues(record.Values)
		if err != nil {
			return RecordPage{}, fmt.Errorf("notes: record %s data: %w", record.ID, err)
		}
		if predicate != nil && !predicate.match(values) {
			continue
		}
		rowsByID[record.ID] = record
		candidates = append(candidates, &sortRow{
			id:        record.ID,
			title:     title,
			createdAt: record.CreatedAt,
			updatedAt: record.UpdatedAt,
			values:    values,
		})
	}
	if err := rows.Err(); err != nil {
		return RecordPage{}, fmt.Errorf("notes: list records: %w", err)
	}
	// Without explicit sort keys the natural table order (sort_order, id) is
	// kept; explicit keys are evaluated in Go with nulls last and an id
	// tiebreak (SPEC-05 §4.5).
	if len(specs) > 0 {
		sortRows(candidates, specs)
	}

	total := len(candidates)
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return RecordPage{Total: total}, nil
	}
	candidates = candidates[offset:]
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = defaultRecordLimit
	case limit > maxRecordLimit:
		limit = maxRecordLimit
	}
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	page := RecordPage{Records: make([]*Record, 0, len(candidates)), Total: total}
	for _, candidate := range candidates {
		page.Records = append(page.Records, rowsByID[candidate.id])
	}
	return page, nil
}

// GetRecord returns a record, soft-deleted ones included (the caller sees
// DeletedAt). A missing record wraps sql.ErrNoRows.
func (s *recordService) GetRecord(ctx context.Context, recordID string) (*Record, error) {
	record, err := loadRecordRow(ctx, s.db, recordID)
	if err != nil {
		return nil, err
	}
	return record, nil
}

// CreateRecord creates the row and, when no note is supplied, an empty
// db_record note through notefile.Store.Create (SPEC-05 §5.3, D-05-03).
func (s *recordService) CreateRecord(ctx context.Context, in CreateRecordInput) (*Record, error) {
	if in.DatabaseID == "" {
		return nil, dbValidationf("record needs a database id")
	}
	supplied, err := decodeRecordValues(in.Values)
	if err != nil {
		return nil, err
	}
	recordID, err := ids.New(s.clock)
	if err != nil {
		return nil, fmt.Errorf("notes: generate record id: %w", err)
	}
	now := s.clock.Now()
	var result *Record
	err = dbInTx(ctx, s.db, func(tx *sql.Tx) error {
		database, err := loadDatabase(ctx, tx, in.DatabaseID)
		if err != nil {
			return err
		}
		if database.DeletedAt != nil {
			return fmt.Errorf("notes: create record: %w", errs.ErrEntityDeleted)
		}
		fields, err := dbLoadFields(ctx, tx, in.DatabaseID, false)
		if err != nil {
			return err
		}
		fieldMap := fieldsByID(fields)
		titleField, err := loadTitleField(ctx, tx, in.DatabaseID)
		if err != nil {
			return err
		}
		data, err := s.normalizeSuppliedValues(ctx, tx, fieldMap, supplied)
		if err != nil {
			return err
		}

		noteID := in.NoteID
		if noteID == "" {
			title := titleValue(data, titleField.ID)
			created, err := s.notes.Create(ctx, tx, notefile.CreateRequest{
				ProjectID: s.projectID,
				Kind:      schema.NoteDBRecord,
				Title:     title,
				Actor:     in.Actor,
			})
			if err != nil {
				return err
			}
			noteID = created.NoteID
		} else {
			noteTitle, err := liveNoteTitle(ctx, tx, noteID)
			if err != nil {
				return err
			}
			if _, exists, err := recordByNoteID(ctx, tx, noteID); err != nil {
				return err
			} else if exists {
				return dbValidationf("note %s already has a database record", noteID)
			}
			if titleValue(data, titleField.ID) == "" && noteTitle != "" {
				encoded, _ := json.Marshal(noteTitle)
				data[titleField.ID] = encoded
			}
			if err := syncNoteTitle(ctx, tx, noteID, noteTitle, titleValue(data, titleField.ID), now); err != nil {
				return err
			}
		}
		if _, ok := data[titleField.ID]; !ok {
			empty, _ := json.Marshal("")
			data[titleField.ID] = empty
		}
		var position int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sort_order) + 1, 0) FROM db_records WHERE database_id = ?`,
			in.DatabaseID).Scan(&position); err != nil {
			return fmt.Errorf("notes: next record position: %w", err)
		}
		encoded, err := marshalRecordValues(data)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO db_records(id, database_id, note_id, title, data, sort_order, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			recordID, in.DatabaseID, noteID, titleValue(data, titleField.ID), string(encoded), position,
			clock.Format(now), clock.Format(now)); err != nil {
			if isUniqueViolation(err) {
				return dbValidationf("note %s already has a database record", noteID)
			}
			return fmt.Errorf("notes: insert record: %w", err)
		}
		// Relation side effects run after the row exists: relations.Link
		// requires a live source row.
		if err := s.applyValueSideEffects(ctx, tx, recordID, noteID, in.DatabaseID, nil, data, in.Actor, now); err != nil {
			return err
		}
		if err := s.events.Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityCreated,
			EntityType: schema.EntityDBRecord,
			EntityID:   recordID,
			Summary:    "创建记录",
			Actor:      in.Actor,
			At:         now,
		}); err != nil {
			return err
		}
		result = &Record{
			ID:         recordID,
			DatabaseID: in.DatabaseID,
			NoteID:     noteID,
			Values:     encoded,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// UpdateValues merges validated values into the record, applies the relation
// and tag side effects, projects the title and emits exactly one note.updated.
func (s *recordService) UpdateValues(ctx context.Context, in UpdateValuesInput) (*Record, error) {
	supplied, err := decodeRecordValues(in.Values)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	var result *Record
	err = dbInTx(ctx, s.db, func(tx *sql.Tx) error {
		record, err := loadRecordRow(ctx, tx, in.RecordID)
		if err != nil {
			return err
		}
		if record.DeletedAt != nil {
			return fmt.Errorf("notes: update values: %w", errs.ErrEntityDeleted)
		}
		fields, err := dbLoadFields(ctx, tx, record.DatabaseID, false)
		if err != nil {
			return err
		}
		fieldMap := fieldsByID(fields)
		titleField, err := loadTitleField(ctx, tx, record.DatabaseID)
		if err != nil {
			return err
		}
		normalized, err := s.normalizeSuppliedValues(ctx, tx, fieldMap, supplied)
		if err != nil {
			return err
		}
		oldData, err := decodeRecordValues(record.Values)
		if err != nil {
			return fmt.Errorf("notes: record %s data: %w", record.ID, err)
		}
		merged := make(map[string]json.RawMessage, len(oldData)+len(normalized))
		for id, raw := range oldData {
			merged[id] = raw
		}
		for id, raw := range normalized {
			if raw == nil {
				delete(merged, id)
				continue
			}
			merged[id] = raw
		}
		if _, ok := merged[titleField.ID]; !ok {
			empty, _ := json.Marshal("")
			merged[titleField.ID] = empty
		}
		title := titleValue(merged, titleField.ID)
		if err := s.applyValueSideEffects(ctx, tx, record.ID, record.NoteID, record.DatabaseID, oldData, normalized, in.Actor, now); err != nil {
			return err
		}
		encoded, err := marshalRecordValues(merged)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE db_records SET data = ?, title = ?, updated_at = ? WHERE id = ?`,
			string(encoded), title, clock.Format(now), record.ID); err != nil {
			return fmt.Errorf("notes: update record: %w", err)
		}
		if record.NoteID != "" {
			existingNoteTitle, _, err := noteTitleByID(ctx, tx, record.NoteID)
			if err != nil {
				return err
			}
			if err := syncNoteTitle(ctx, tx, record.NoteID, existingNoteTitle, title, now); err != nil {
				return err
			}
			if err := s.events.EmitNoteUpdated(ctx, tx, events.NoteUpdatedEvent{
				NoteID:  record.NoteID,
				Summary: "更新记录字段",
				Actor:   in.Actor,
				At:      now,
			}); err != nil {
				return err
			}
		}
		result = &Record{
			ID:         record.ID,
			DatabaseID: record.DatabaseID,
			NoteID:     record.NoteID,
			Values:     encoded,
			CreatedAt:  record.CreatedAt,
			UpdatedAt:  now,
			DeletedAt:  record.DeletedAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// SetTags replaces the record's tag set on entity_tags. Tags are not relations,
// so no relation or activity event is emitted (SPEC-05 §5.3).
func (s *recordService) SetTags(ctx context.Context, recordID string, tagIDs []string, actor events.Actor) error {
	now := s.clock.Now()
	return dbInTx(ctx, s.db, func(tx *sql.Tx) error {
		record, err := loadRecordRow(ctx, tx, recordID)
		if err != nil {
			return err
		}
		if record.DeletedAt != nil {
			return fmt.Errorf("notes: set tags: %w", errs.ErrEntityDeleted)
		}
		unique := make([]string, 0, len(tagIDs))
		seen := make(map[string]bool, len(tagIDs))
		for _, tagID := range tagIDs {
			if tagID == "" {
				return dbValidationf("tag ids must not be empty")
			}
			live, err := tagIsLive(ctx, tx, tagID)
			if err != nil {
				return err
			}
			if !live {
				return dbValidationf("tag %s does not exist or was deleted", tagID)
			}
			if seen[tagID] {
				continue
			}
			seen[tagID] = true
			unique = append(unique, tagID)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM entity_tags WHERE entity_type = ? AND entity_id = ?`,
			string(schema.EntityDBRecord), recordID); err != nil {
			return fmt.Errorf("notes: clear record tags: %w", err)
		}
		if err := insertEntityTags(ctx, tx, recordID, unique, now); err != nil {
			return err
		}
		_ = actor // tag changes emit nothing (SPEC-05 §5.3)
		return nil
	})
}

// SoftDeleteRecord moves the record into the trash; deleteNote also moves the
// backing note in the same transaction (D-05-05).
func (s *recordService) SoftDeleteRecord(ctx context.Context, recordID string, deleteNote bool, actor events.Actor) error {
	return dbInTx(ctx, s.db, func(tx *sql.Tx) error {
		record, err := loadRecordRow(ctx, tx, recordID)
		if err != nil {
			return err
		}
		if err := s.trash.SoftDelete(ctx, tx, trash.Ref{Type: schema.EntityDBRecord, ID: record.ID}, actor); err != nil {
			return err
		}
		if deleteNote && record.NoteID != "" {
			if err := s.trash.SoftDelete(ctx, tx, trash.Ref{Type: schema.EntityNote, ID: record.NoteID}, actor); err != nil {
				return err
			}
		}
		return nil
	})
}

// RestoreRecord restores the record and, when it is still trashed, its backing
// note (SPEC-05 §5.3).
func (s *recordService) RestoreRecord(ctx context.Context, recordID string, actor events.Actor) error {
	return dbInTx(ctx, s.db, func(tx *sql.Tx) error {
		record, err := loadRecordRow(ctx, tx, recordID)
		if err != nil {
			return err
		}
		if err := s.trash.Restore(ctx, tx, trash.Ref{Type: schema.EntityDBRecord, ID: record.ID}, actor); err != nil {
			return err
		}
		if record.NoteID != "" {
			var deleted sql.NullString
			err := tx.QueryRowContext(ctx, `SELECT deleted_at FROM notes WHERE id = ?`, record.NoteID).Scan(&deleted)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("notes: check note %s: %w", record.NoteID, err)
			}
			if err == nil && deleted.Valid {
				return s.trash.Restore(ctx, tx, trash.Ref{Type: schema.EntityNote, ID: record.NoteID}, actor)
			}
		}
		return nil
	})
}

// ReorderRecords rewrites db_records.sort_order for a full permutation of the
// database's live records. Reordering emits nothing.
func (s *recordService) ReorderRecords(ctx context.Context, dbID string, orderedRecordIDs []string, actor events.Actor) error {
	now := s.clock.Now()
	_ = actor
	return dbInTx(ctx, s.db, func(tx *sql.Tx) error {
		database, err := loadDatabase(ctx, tx, dbID)
		if err != nil {
			return err
		}
		if database.DeletedAt != nil {
			return fmt.Errorf("notes: reorder records: %w", errs.ErrEntityDeleted)
		}
		rows, err := tx.QueryContext(ctx,
			`SELECT id FROM db_records WHERE database_id = ? AND deleted_at IS NULL ORDER BY sort_order, id`, dbID)
		if err != nil {
			return fmt.Errorf("notes: list records for reorder: %w", err)
		}
		var liveIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("notes: list records for reorder: %w", err)
			}
			liveIDs = append(liveIDs, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("notes: list records for reorder: %w", err)
		}
		rows.Close()
		if err := checkPermutation("records", orderedRecordIDs, liveIDs); err != nil {
			return err
		}
		for position, recordID := range orderedRecordIDs {
			if _, err := tx.ExecContext(ctx,
				`UPDATE db_records SET sort_order = ?, updated_at = ? WHERE id = ? AND database_id = ?`,
				position, clock.Format(now), recordID, dbID); err != nil {
				return fmt.Errorf("notes: reorder records: %w", err)
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Value validation and side effects
// ---------------------------------------------------------------------------

// normalizeSuppliedValues validates every supplied field id/value pair and
// returns canonical JSON per field. JSON null means "clear the value" and is
// returned as a nil raw message; callers delete the key.
func (s *recordService) normalizeSuppliedValues(ctx context.Context, tx *sql.Tx, fields map[string]*Field, supplied map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	normalized := make(map[string]json.RawMessage, len(supplied))
	for fieldID, raw := range supplied {
		field, ok := fields[fieldID]
		if !ok {
			return nil, dbValidationf("field %s does not exist or was deleted in this database", fieldID)
		}
		if isJSONNull(raw) {
			normalized[fieldID] = nil
			continue
		}
		cfg, err := parseFieldConfig(field.Type, field.Config)
		if err != nil {
			return nil, fmt.Errorf("notes: stored config of field %s: %w", field.ID, err)
		}
		value, err := validateValue(ctx, tx, s.rels, field, cfg, raw)
		if err != nil {
			return nil, err
		}
		normalized[fieldID] = value
	}
	return normalized, nil
}

// validateValue checks one cell against its field type (SPEC-05 §4.4) and
// returns the canonical JSON to store.
func validateValue(ctx context.Context, tx *sql.Tx, relSvc *relations.Service, field *Field, cfg fieldConfig, raw json.RawMessage) (json.RawMessage, error) {
	switch field.Type {
	case FieldTypeText, FieldTypeURL:
		value, err := decodeJSONString(raw)
		if err != nil {
			return nil, dbValidationf("field %q needs a string: %v", field.Name, err)
		}
		return json.Marshal(value)
	case FieldTypeNumber:
		if _, err := decodeJSONNumber(raw); err != nil {
			return nil, dbValidationf("field %q needs a number: %v", field.Name, err)
		}
		return json.RawMessage(bytes.TrimSpace(raw)), nil
	case FieldTypeDate:
		value, err := decodeJSONDate(raw)
		if err != nil {
			return nil, dbValidationf("field %q needs a YYYY-MM-DD date: %v", field.Name, err)
		}
		return json.Marshal(value)
	case FieldTypeSelect:
		value, err := decodeJSONString(raw)
		if err != nil || value == "" {
			return nil, dbValidationf("field %q needs an option id", field.Name)
		}
		if !optionExists(cfg.Options, value) {
			return nil, dbValidationf("value %q is not an option of field %q", value, field.Name)
		}
		return json.Marshal(value)
	case FieldTypeMultiSelect:
		values, err := decodeStringArray(raw)
		if err != nil {
			return nil, dbValidationf("field %q needs an array of option ids: %v", field.Name, err)
		}
		seen := make(map[string]bool, len(values))
		for _, value := range values {
			if !optionExists(cfg.Options, value) {
				return nil, dbValidationf("value %q is not an option of field %q", value, field.Name)
			}
			if seen[value] {
				return nil, dbValidationf("field %q contains the duplicate option %q", field.Name, value)
			}
			seen[value] = true
		}
		return json.Marshal(values)
	case FieldTypeCheckbox:
		value, ok := rawBool(raw)
		if !ok {
			return nil, dbValidationf("field %q needs a boolean", field.Name)
		}
		return json.Marshal(value)
	case FieldTypeTags:
		values, err := decodeStringArray(raw)
		if err != nil {
			return nil, dbValidationf("field %q needs an array of tag ids: %v", field.Name, err)
		}
		seen := make(map[string]bool, len(values))
		for _, tagID := range values {
			live, err := tagIsLive(ctx, tx, tagID)
			if err != nil {
				return nil, err
			}
			if !live {
				return nil, dbValidationf("tag %s does not exist or was deleted", tagID)
			}
			if seen[tagID] {
				return nil, dbValidationf("field %q contains the duplicate tag %q", field.Name, tagID)
			}
			seen[tagID] = true
		}
		return json.Marshal(values)
	case FieldTypeRelation:
		refs, err := decodeRelationArray(raw)
		if err != nil {
			return nil, dbValidationf("field %q needs an array of {type,id} targets: %v", field.Name, err)
		}
		if relSvc == nil {
			return nil, errors.New("notes: relation service is not configured")
		}
		seen := make(map[string]bool, len(refs))
		for _, ref := range refs {
			if !targetAllowed(cfg.Targets, ref.Type) {
				return nil, dbValidationf("field %q does not allow targets of type %q", field.Name, ref.Type)
			}
			key := string(ref.Type) + "/" + ref.ID
			if seen[key] {
				return nil, dbValidationf("field %q contains the duplicate target %s", field.Name, key)
			}
			seen[key] = true
			alive, err := entityAliveInTx(ctx, tx, relSvc, ref)
			if err != nil {
				return nil, err
			}
			if !alive {
				return nil, dbValidationf("relation target %s is missing or deleted", key)
			}
		}
		return json.Marshal(refs)
	}
	return nil, dbValidationf("field %q has an unsupported type %q", field.Name, field.Type)
}

// projectEntityTable maps the relation-capable project entities to their table.
// Resolving them through relations.Service.Resolve would use the service's own
// *sql.DB while a transaction is open; with a single-connection pool that
// deadlocks, so project targets are checked on the caller's transaction.
var projectEntityTable = map[schema.EntityType]string{
	schema.EntityNote:       "notes",
	schema.EntityDBRecord:   "db_records",
	schema.EntityIdea:       "ideas",
	schema.EntityExperiment: "experiments",
}

// entityAliveInTx reports whether a relation target exists and is not
// soft-deleted. papers live in global.db, so only they go through the relation
// service (which owns the global handle).
func entityAliveInTx(ctx context.Context, tx *sql.Tx, relSvc *relations.Service, ref EntityRef) (bool, error) {
	if table, ok := projectEntityTable[ref.Type]; ok {
		var deleted sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT deleted_at FROM `+table+` WHERE id = ?`, ref.ID).Scan(&deleted)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("notes: resolve relation target %s/%s: %w", ref.Type, ref.ID, err)
		}
		return !deleted.Valid, nil
	}
	if ref.Type == schema.EntityPaper {
		if relSvc == nil {
			return false, errors.New("notes: relation service is not configured")
		}
		state, err := relSvc.Resolve(ctx, ref.Type, ref.ID)
		if err != nil {
			return false, fmt.Errorf("notes: resolve relation target %s/%s: %w", ref.Type, ref.ID, err)
		}
		return state == relations.TargetAlive, nil
	}
	return false, dbValidationf("relation target type %q is not allowed", ref.Type)
}

// applyValueSideEffects writes the entity_relations and entity_tags rows for
// relation and tags fields. oldData is nil on create; supplied restricts the
// work to the fields touched by this operation.
func (s *recordService) applyValueSideEffects(ctx context.Context, tx *sql.Tx, recordID, noteID, databaseID string, oldData, supplied map[string]json.RawMessage, actor events.Actor, now time.Time) error {
	if len(supplied) == 0 {
		return nil
	}
	for fieldID, newRaw := range supplied {
		field, exists, err := fieldByIDTx(ctx, tx, fieldID)
		if err != nil {
			return err
		}
		if !exists || field.DatabaseID != databaseID {
			// Unknown ids already in data are preserved untouched; they never
			// get side effects.
			continue
		}
		switch field.Type {
		case FieldTypeRelation:
			if noteID == "" {
				return dbValidationf("relation field %q needs a backing note, but record %s has none", field.Name, recordID)
			}
			if err := s.applyRelationDiff(ctx, tx, recordID, noteID, oldData, fieldID, newRaw, actor); err != nil {
				return err
			}
		case FieldTypeTags:
			if err := applyTagDiff(ctx, tx, recordID, oldData, fieldID, newRaw, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// relationForTarget maps a relation-field target to a registered relation type
// (SPEC-05 §5.5): paper/note targets use `references`, everything else
// (idea, experiment, db_record) uses `related_to`.
func relationForTarget(target schema.EntityType) schema.Relation {
	switch target {
	case schema.EntityPaper, schema.EntityNote:
		return schema.RelationReferences
	default:
		return schema.RelationRelatedTo
	}
}

// applyRelationDiff links/unlinks the edges of one relation field. The edge
// source is the record's backing note (db_records.note_id), not the record
// itself: SPEC-05 §5.5 requires `references` for Note/Record → Paper and
// Note/Record → Note, and SPEC-01's registry only permits `references` with
// src_type=note.
func (s *recordService) applyRelationDiff(ctx context.Context, tx *sql.Tx, recordID, noteID string, oldData map[string]json.RawMessage, fieldID string, newRaw json.RawMessage, actor events.Actor) error {
	if s.rels == nil {
		return errors.New("notes: relation service is not configured")
	}
	oldRefs := refsFromRaw(oldData[fieldID])
	newRefs := refsFromRaw(newRaw)
	for _, ref := range oldRefs {
		if containsRef(newRefs, ref) {
			continue
		}
		edge := relations.Link{
			SrcType:  schema.EntityNote,
			SrcID:    noteID,
			Relation: relationForTarget(ref.Type),
			DstType:  ref.Type,
			DstID:    ref.ID,
		}
		if err := s.rels.Unlink(ctx, tx, edge, actor); err != nil {
			return fmt.Errorf("notes: unlink %s/%s: %w", ref.Type, ref.ID, err)
		}
	}
	for _, ref := range newRefs {
		if containsRef(oldRefs, ref) {
			continue
		}
		if ref.Type == schema.EntityDBRecord && ref.ID == recordID {
			return dbValidationf("a record cannot relate to itself")
		}
		edge := relations.Link{
			SrcType:  schema.EntityNote,
			SrcID:    noteID,
			Relation: relationForTarget(ref.Type),
			DstType:  ref.Type,
			DstID:    ref.ID,
		}
		if _, err := s.rels.Link(ctx, tx, edge, actor); err != nil {
			return fmt.Errorf("notes: link %s/%s: %w", ref.Type, ref.ID, err)
		}
	}
	return nil
}

// applyTagDiff replaces the entity_tags rows of one tags field.
func applyTagDiff(ctx context.Context, tx *sql.Tx, recordID string, oldData map[string]json.RawMessage, fieldID string, newRaw json.RawMessage, now time.Time) error {
	oldIDs := idsFromRaw(oldData[fieldID])
	newIDs := idsFromRaw(newRaw)
	for _, tagID := range oldIDs {
		if containsString(newIDs, tagID) {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM entity_tags WHERE tag_id = ? AND entity_type = ? AND entity_id = ?`,
			tagID, string(schema.EntityDBRecord), recordID); err != nil {
			return fmt.Errorf("notes: unlink tag %s: %w", tagID, err)
		}
	}
	var added []string
	for _, tagID := range newIDs {
		if !containsString(oldIDs, tagID) {
			added = append(added, tagID)
		}
	}
	return insertEntityTags(ctx, tx, recordID, added, now)
}

// ---------------------------------------------------------------------------
// Row and JSON helpers
// ---------------------------------------------------------------------------

func scanRecordRow(row dbRowScanner) (*Record, error) {
	record, _, err := scanRecordFields(row)
	return record, err
}

// scanRecordFields reads one db_records row plus its title projection (the
// Record type deliberately has no Title: the title lives on the notes row).
func scanRecordFields(row dbRowScanner) (*Record, string, error) {
	var (
		record    Record
		noteID    sql.NullString
		title     string
		data      string
		createdAt string
		updatedAt string
		deletedAt sql.NullString
	)
	if err := row.Scan(&record.ID, &record.DatabaseID, &noteID, &title, &data,
		&createdAt, &updatedAt, &deletedAt); err != nil {
		return nil, "", err
	}
	if noteID.Valid {
		record.NoteID = noteID.String
	}
	record.Values = json.RawMessage(data)
	created, err := dbParseTime("db_records.created_at", createdAt)
	if err != nil {
		return nil, "", err
	}
	updated, err := dbParseTime("db_records.updated_at", updatedAt)
	if err != nil {
		return nil, "", err
	}
	deleted, err := dbParseNullableTime("db_records.deleted_at", deletedAt)
	if err != nil {
		return nil, "", err
	}
	record.CreatedAt = created
	record.UpdatedAt = updated
	record.DeletedAt = deleted
	return &record, title, nil
}

func loadRecordRow(ctx context.Context, q dbTX, recordID string) (*Record, error) {
	row := q.QueryRowContext(ctx, `SELECT `+recordColumns+` FROM db_records WHERE id = ?`, recordID)
	record, err := scanRecordRow(row)
	if err != nil {
		return nil, fmt.Errorf("notes: load record %s: %w", recordID, err)
	}
	return record, nil
}

func recordByNoteID(ctx context.Context, q dbTX, noteID string) (string, bool, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT id FROM db_records WHERE note_id = ?`, noteID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("notes: check record by note: %w", err)
	}
	return id, true, nil
}

// noteTitleByID reads the stored notes.title; found=false when the row is gone.
func noteTitleByID(ctx context.Context, q dbTX, noteID string) (string, bool, error) {
	var title string
	err := q.QueryRowContext(ctx, `SELECT title FROM notes WHERE id = ?`, noteID).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("notes: load note %s: %w", noteID, err)
	}
	return title, true, nil
}

func liveNoteTitle(ctx context.Context, q dbTX, noteID string) (string, error) {
	var (
		title     string
		deletedAt sql.NullString
	)
	err := q.QueryRowContext(ctx, `SELECT title, deleted_at FROM notes WHERE id = ?`, noteID).Scan(&title, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", dbValidationf("note %s does not exist", noteID)
	}
	if err != nil {
		return "", fmt.Errorf("notes: load note %s: %w", noteID, err)
	}
	if deletedAt.Valid {
		return "", fmt.Errorf("notes: note %s: %w", noteID, errs.ErrEntityDeleted)
	}
	return title, nil
}

// syncNoteTitle projects the title field onto notes.title. existingTitle is the
// note's previous title; the row is only touched when it changes.
func syncNoteTitle(ctx context.Context, tx *sql.Tx, noteID, existingTitle, title string, now time.Time) error {
	if noteID == "" || existingTitle == title {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE notes SET title = ?, updated_at = ? WHERE id = ?`,
		title, clock.Format(now), noteID); err != nil {
		return fmt.Errorf("notes: sync note title: %w", err)
	}
	return nil
}

func fieldsByID(fields []*Field) map[string]*Field {
	out := make(map[string]*Field, len(fields))
	for _, field := range fields {
		out[field.ID] = field
	}
	return out
}

func fieldByIDTx(ctx context.Context, q dbTX, fieldID string) (*Field, bool, error) {
	field, err := loadField(ctx, q, fieldID, false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return field, true, nil
}

func decodeRecordValues(raw json.RawMessage) (map[string]json.RawMessage, error) {
	values := make(map[string]json.RawMessage)
	if isJSONNull(raw) {
		return values, nil
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, dbValidationf("values must be a JSON object of field id to value: %v", err)
	}
	return values, nil
}

func marshalRecordValues(values map[string]json.RawMessage) (json.RawMessage, error) {
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("notes: encode record values: %w", err)
	}
	return encoded, nil
}

func titleValue(values map[string]json.RawMessage, titleFieldID string) string {
	raw, ok := values[titleFieldID]
	if !ok || isJSONNull(raw) {
		return ""
	}
	title, ok := rawString(raw)
	if !ok {
		return ""
	}
	return title
}

func optionExists(options []selectOption, id string) bool {
	for _, option := range options {
		if option.ID == id {
			return true
		}
	}
	return false
}

func targetAllowed(targets []string, entityType schema.EntityType) bool {
	for _, target := range targets {
		if target == string(entityType) {
			return true
		}
	}
	return false
}

func tagIsLive(ctx context.Context, q dbTX, tagID string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM tags WHERE id = ? AND deleted_at IS NULL`, tagID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("notes: check tag %s: %w", tagID, err)
	}
	return true, nil
}

func insertEntityTags(ctx context.Context, tx *sql.Tx, recordID string, tagIDs []string, now time.Time) error {
	if len(tagIDs) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO entity_tags(tag_id, entity_type, entity_id, created_at) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("notes: insert entity tags: %w", err)
	}
	defer stmt.Close()
	for _, tagID := range tagIDs {
		if _, err := stmt.ExecContext(ctx, tagID, string(schema.EntityDBRecord), recordID, clock.Format(now)); err != nil {
			return fmt.Errorf("notes: insert entity tag %s: %w", tagID, err)
		}
	}
	return nil
}

func decodeStringArray(raw json.RawMessage) ([]string, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.New("value must be a JSON array of strings")
	}
	for _, item := range list {
		if item == "" {
			return nil, errors.New("array items must be non-empty")
		}
	}
	return list, nil
}

func decodeRelationArray(raw json.RawMessage) ([]EntityRef, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	var items []struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errors.New("value must be a JSON array of {type,id} objects")
	}
	refs := make([]EntityRef, 0, len(items))
	for _, item := range items {
		entityType := schema.EntityType(item.Type)
		if !relationTargetAllowed(entityType) {
			return nil, fmt.Errorf("target type %q is not allowed", item.Type)
		}
		if !ids.Valid(item.ID) {
			return nil, fmt.Errorf("target id %q is not a UUIDv7", item.ID)
		}
		refs = append(refs, EntityRef{Type: entityType, ID: item.ID})
	}
	return refs, nil
}

func refsFromRaw(raw json.RawMessage) []EntityRef {
	if isJSONNull(raw) {
		return nil
	}
	refs, err := decodeRelationArray(raw)
	if err != nil {
		return nil
	}
	return refs
}

func idsFromRaw(raw json.RawMessage) []string {
	list, err := decodeStringArray(raw)
	if err != nil {
		return nil
	}
	return list
}

func containsRef(refs []EntityRef, want EntityRef) bool {
	for _, ref := range refs {
		if ref.Type == want.Type && ref.ID == want.ID {
			return true
		}
	}
	return false
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToUpper(err.Error()), "UNIQUE")
}
