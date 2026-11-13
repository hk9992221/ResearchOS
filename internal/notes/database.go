package notes

import (
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
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/trash"
)

// Field-name and database-name limits (SPEC-05 §5.2: names are non-empty and
// at most 64 characters for fields; databases follow the same spirit).
const (
	maxFieldNameRunes    = 64
	maxDatabaseNameRunes = 200
)

// dbTX is the query surface shared by *sql.DB and *sql.Tx.
type dbTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// dbRowScanner is the scan surface of *sql.Row and *sql.Rows.
type dbRowScanner interface {
	Scan(dest ...any) error
}

// dbInTx runs fn in one immediate transaction and commits it.
func dbInTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	if db == nil {
		return errors.New("notes: nil database handle")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("notes: begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("notes: commit transaction: %w", err)
	}
	return nil
}

// dbParseTime reads one stored timestamp column.
func dbParseTime(column, value string) (time.Time, error) {
	at, err := clock.Parse(value)
	if err != nil {
		return time.Time{}, fmt.Errorf("notes: parse %s %q: %w", column, value, err)
	}
	return at, nil
}

// dbParseNullableTime reads a nullable timestamp column.
func dbParseNullableTime(column string, value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	at, err := dbParseTime(column, value.String)
	if err != nil {
		return nil, err
	}
	return &at, nil
}

// selectOption is one entry of a select/multi_select field config.
type selectOption struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// fieldConfig is the decoded db_fields.config JSON. Only select/multi_select
// (options) and relation (targets) carry configuration in v1.
type fieldConfig struct {
	Options []selectOption `json:"options,omitempty"`
	Targets []string       `json:"targets,omitempty"`
}

// parseFieldConfig decodes a stored or supplied config. A malformed config is
// an error; an absent one is the zero value.
func parseFieldConfig(fieldType FieldType, raw json.RawMessage) (fieldConfig, error) {
	var cfg fieldConfig
	if len(strings.TrimSpace(string(raw))) == 0 || isJSONNull(raw) {
		return cfg, nil
	}
	switch fieldType {
	case FieldTypeSelect, FieldTypeMultiSelect:
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fieldConfig{}, fmt.Errorf("config must be a JSON object with options: %w", err)
		}
		for _, opt := range cfg.Options {
			if strings.TrimSpace(opt.Name) == "" {
				return fieldConfig{}, errors.New("every option needs a non-empty name")
			}
		}
	case FieldTypeRelation:
		var relationCfg struct {
			Targets []string `json:"targets"`
		}
		if err := json.Unmarshal(raw, &relationCfg); err != nil {
			return fieldConfig{}, fmt.Errorf("config must be a JSON object with targets: %w", err)
		}
		cfg.Targets = relationCfg.Targets
	}
	return cfg, nil
}

// normalizeFieldConfig validates a client-supplied config and returns its
// canonical form. Select/multi_select option ids are always server-generated;
// an option may carry an id only when that id already belongs to the field
// being updated (option ids stay stable, SPEC-05 §5.2).
func normalizeFieldConfig(c clock.Clock, fieldType FieldType, raw json.RawMessage, existing []selectOption) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		raw = json.RawMessage("{}")
	}
	// Everything except relation only reads known keys; reject unknown shapes
	// for the config-less types so typos do not disappear silently.
	switch fieldType {
	case FieldTypeSelect, FieldTypeMultiSelect:
		var input struct {
			Options []struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Color string `json:"color"`
			} `json:"options"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, dbValidationf("config must be a JSON object with an options array: %v", err)
		}
		known := make(map[string]bool, len(existing))
		for _, opt := range existing {
			known[opt.ID] = true
		}
		seen := make(map[string]bool, len(input.Options))
		options := make([]selectOption, 0, len(input.Options))
		for _, opt := range input.Options {
			name := strings.TrimSpace(opt.Name)
			if name == "" {
				return nil, dbValidationf("every option needs a non-empty name")
			}
			id := opt.ID
			if id == "" || !known[id] || seen[id] {
				generated, err := ids.New(c)
				if err != nil {
					return nil, fmt.Errorf("notes: generate option id: %w", err)
				}
				id = generated
			}
			seen[id] = true
			options = append(options, selectOption{ID: id, Name: name, Color: strings.TrimSpace(opt.Color)})
		}
		return json.Marshal(fieldConfig{Options: options})
	case FieldTypeRelation:
		var input struct {
			Targets []string `json:"targets"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, dbValidationf("config must be a JSON object with a targets array: %v", err)
		}
		if len(input.Targets) == 0 {
			return nil, dbValidationf("relation fields need a non-empty config.targets")
		}
		seen := make(map[string]bool, len(input.Targets))
		targets := make([]string, 0, len(input.Targets))
		for _, target := range input.Targets {
			entityType := schema.EntityType(strings.TrimSpace(target))
			if !relationTargetAllowed(entityType) {
				return nil, dbValidationf("relation target %q is not one of paper/note/idea/experiment/db_record", target)
			}
			if seen[string(entityType)] {
				continue
			}
			seen[string(entityType)] = true
			targets = append(targets, string(entityType))
		}
		return json.Marshal(fieldConfig{Targets: targets})
	default:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, dbValidationf("config must be a JSON object: %v", err)
		}
		if len(object) != 0 {
			return nil, dbValidationf("field type %q takes no config", fieldType)
		}
		return json.RawMessage("{}"), nil
	}
}

// validateFieldName trims and bounds a field name.
func validateFieldName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", dbValidationf("field name must not be empty")
	}
	if len([]rune(trimmed)) > maxFieldNameRunes {
		return "", dbValidationf("field name must be at most %d characters", maxFieldNameRunes)
	}
	return trimmed, nil
}

// databaseService implements DatabaseService (SPEC-05 §5.2).
type databaseService struct {
	db     *sql.DB
	events events.Writer
	trash  *trash.Service
	clock  clock.Clock
}

var _ DatabaseService = (*databaseService)(nil)

// NewDatabaseService binds the database service to one project database.
func NewDatabaseService(db *sql.DB, writer events.Writer, trashSvc *trash.Service, opt ServiceOptions) DatabaseService {
	c := opt.Clock
	if c == nil {
		c = clock.System{}
	}
	return &databaseService{db: db, events: writer, trash: trashSvc, clock: c}
}

// CreateDatabase inserts a database and its mandatory title field in one
// transaction (SPEC-05 §5.2).
func (s *databaseService) CreateDatabase(ctx context.Context, in CreateDatabaseInput) (*ResearchDatabase, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, dbValidationf("database name must not be empty")
	}
	if len([]rune(name)) > maxDatabaseNameRunes {
		return nil, dbValidationf("database name must be at most %d characters", maxDatabaseNameRunes)
	}
	description := strings.TrimSpace(in.Description)
	now := s.clock.Now()
	databaseID, err := ids.New(s.clock)
	if err != nil {
		return nil, fmt.Errorf("notes: generate database id: %w", err)
	}
	titleFieldID, err := ids.New(s.clock)
	if err != nil {
		return nil, fmt.Errorf("notes: generate field id: %w", err)
	}
	err = dbInTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO research_databases(id, name, description, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?)`,
			databaseID, name, description, clock.Format(now), clock.Format(now)); err != nil {
			return fmt.Errorf("notes: insert database: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO db_fields(id, database_id, name, type, config, sort_order, role, created_at, updated_at)
			 VALUES (?, ?, ?, ?, '{}', 0, 'title', ?, ?)`,
			titleFieldID, databaseID, "Name", string(FieldTypeText), clock.Format(now), clock.Format(now)); err != nil {
			return fmt.Errorf("notes: insert title field: %w", err)
		}
		return s.events.Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityCreated,
			EntityType: schema.EntityResearchDatabase,
			EntityID:   databaseID,
			Summary:    "创建数据库：" + name,
			Actor:      in.Actor,
			At:         now,
		})
	})
	if err != nil {
		return nil, err
	}
	return &ResearchDatabase{
		ID:          databaseID,
		Name:        name,
		Description: description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// GetDatabase returns a database row; soft-deleted rows are returned with
// DeletedAt set so callers can decide (the trash UI needs them).
func (s *databaseService) GetDatabase(ctx context.Context, dbID string) (*ResearchDatabase, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, created_at, updated_at, deleted_at
		   FROM research_databases WHERE id = ?`, dbID)
	database, err := scanDatabase(row)
	if err != nil {
		return nil, fmt.Errorf("notes: get database %s: %w", dbID, err)
	}
	return database, nil
}

// ListDatabases returns databases ordered by name; includeDeleted also returns
// soft-deleted rows.
func (s *databaseService) ListDatabases(ctx context.Context, includeDeleted bool) ([]*ResearchDatabase, error) {
	query := `SELECT id, name, description, created_at, updated_at, deleted_at
	            FROM research_databases`
	if !includeDeleted {
		query += ` WHERE deleted_at IS NULL`
	}
	query += ` ORDER BY lower(name), id`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("notes: list databases: %w", err)
	}
	defer rows.Close()
	var out []*ResearchDatabase
	for rows.Next() {
		database, err := scanDatabase(rows)
		if err != nil {
			return nil, fmt.Errorf("notes: list databases: %w", err)
		}
		out = append(out, database)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notes: list databases: %w", err)
	}
	return out, nil
}

// RenameDatabase renames a live database and records entity.updated (SPEC-01
// v1.1 registered the event; SPEC-05's "未采纳前不发" condition is met).
func (s *databaseService) RenameDatabase(ctx context.Context, dbID, name string, actor events.Actor) (*ResearchDatabase, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, dbValidationf("database name must not be empty")
	}
	if len([]rune(trimmed)) > maxDatabaseNameRunes {
		return nil, dbValidationf("database name must be at most %d characters", maxDatabaseNameRunes)
	}
	now := s.clock.Now()
	err := dbInTx(ctx, s.db, func(tx *sql.Tx) error {
		current, err := loadDatabase(ctx, tx, dbID)
		if err != nil {
			return err
		}
		if current.DeletedAt != nil {
			return fmt.Errorf("notes: rename database %s: %w", dbID, errs.ErrEntityDeleted)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE research_databases SET name = ?, updated_at = ? WHERE id = ?`,
			trimmed, clock.Format(now), dbID); err != nil {
			return fmt.Errorf("notes: rename database: %w", err)
		}
		return s.events.Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityUpdated,
			EntityType: schema.EntityResearchDatabase,
			EntityID:   dbID,
			Old:        &events.Object{"name": current.Name},
			New:        &events.Object{"name": trimmed},
			Summary:    "重命名数据库",
			Actor:      actor,
			At:         now,
		})
	})
	if err != nil {
		return nil, err
	}
	return s.GetDatabase(ctx, dbID)
}

// SoftDeleteDatabase moves a database into the trash without cascading to its
// fields or records (SPEC-05 §5.2); trash emits entity.deleted.
func (s *databaseService) SoftDeleteDatabase(ctx context.Context, dbID string, actor events.Actor) error {
	return dbInTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := requireDatabaseRow(ctx, tx, dbID); err != nil {
			return err
		}
		return s.trash.SoftDelete(ctx, tx, trash.Ref{Type: schema.EntityResearchDatabase, ID: dbID}, actor)
	})
}

// RestoreDatabase restores a soft-deleted database; trash emits entity.restored.
func (s *databaseService) RestoreDatabase(ctx context.Context, dbID string, actor events.Actor) error {
	return dbInTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := requireDatabaseRow(ctx, tx, dbID); err != nil {
			return err
		}
		return s.trash.Restore(ctx, tx, trash.Ref{Type: schema.EntityResearchDatabase, ID: dbID}, actor)
	})
}

// CreateField adds a plain field and emits entity.created db_field.
func (s *databaseService) CreateField(ctx context.Context, in CreateFieldInput) (*Field, error) {
	name, err := validateFieldName(in.Name)
	if err != nil {
		return nil, err
	}
	if !FieldTypeValid(in.Type) {
		return nil, dbValidationf("unknown field type %q", in.Type)
	}
	config, err := normalizeFieldConfig(s.clock, in.Type, in.Config, nil)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	fieldID, err := ids.New(s.clock)
	if err != nil {
		return nil, fmt.Errorf("notes: generate field id: %w", err)
	}
	var position int
	err = dbInTx(ctx, s.db, func(tx *sql.Tx) error {
		database, err := loadDatabase(ctx, tx, in.DatabaseID)
		if err != nil {
			return err
		}
		if database.DeletedAt != nil {
			return fmt.Errorf("notes: create field: %w", errs.ErrEntityDeleted)
		}
		if err := checkFieldNameFree(ctx, tx, in.DatabaseID, name, ""); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sort_order) + 1, 0) FROM db_fields WHERE database_id = ?`,
			in.DatabaseID).Scan(&position); err != nil {
			return fmt.Errorf("notes: next field position: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO db_fields(id, database_id, name, type, config, sort_order, role, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, 'plain', ?, ?)`,
			fieldID, in.DatabaseID, name, string(in.Type), string(config), position,
			clock.Format(now), clock.Format(now)); err != nil {
			return fmt.Errorf("notes: insert field: %w", err)
		}
		return s.events.Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityCreated,
			EntityType: schema.EntityDBField,
			EntityID:   fieldID,
			Summary:    "新增字段：" + name,
			Actor:      in.Actor,
			At:         now,
		})
	})
	if err != nil {
		return nil, err
	}
	return &Field{
		ID:         fieldID,
		DatabaseID: in.DatabaseID,
		Name:       name,
		Type:       in.Type,
		Config:     config,
		Role:       RolePlain,
		Position:   position,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// UpdateField renames or reconfigures a field; the type never changes. Option
// ids stay stable and the title role is preserved (SPEC-05 §5.2).
func (s *databaseService) UpdateField(ctx context.Context, in UpdateFieldInput) (*Field, error) {
	name, err := validateFieldName(in.Name)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	err = dbInTx(ctx, s.db, func(tx *sql.Tx) error {
		current, err := loadField(ctx, tx, in.FieldID, true)
		if err != nil {
			return err
		}
		if current.DeletedAt != nil {
			return fmt.Errorf("notes: update field: %w", errs.ErrEntityDeleted)
		}
		existingCfg, err := parseFieldConfig(current.Type, current.Config)
		if err != nil {
			return fmt.Errorf("notes: stored config of field %s: %w", current.ID, err)
		}
		rawConfig := in.Config
		if len(strings.TrimSpace(string(rawConfig))) == 0 {
			rawConfig = current.Config // config omitted: keep the stored one
		}
		config, err := normalizeFieldConfig(s.clock, current.Type, rawConfig, existingCfg.Options)
		if err != nil {
			return err
		}
		if err := checkFieldNameFree(ctx, tx, current.DatabaseID, name, current.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE db_fields SET name = ?, config = ?, updated_at = ? WHERE id = ?`,
			name, string(config), clock.Format(now), current.ID); err != nil {
			return fmt.Errorf("notes: update field: %w", err)
		}
		oldObject := events.Object{}
		newObject := events.Object{}
		if current.Name != name {
			oldObject["name"] = current.Name
			newObject["name"] = name
		}
		if string(current.Config) != string(config) {
			oldObject["config"] = json.RawMessage(current.Config)
			newObject["config"] = json.RawMessage(config)
		}
		if len(oldObject) == 0 {
			return nil // nothing changed: no event, matching SPEC-05 §7
		}
		return s.events.Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityUpdated,
			EntityType: schema.EntityDBField,
			EntityID:   current.ID,
			Old:        &oldObject,
			New:        &newObject,
			Summary:    "修改字段：" + name,
			Actor:      in.Actor,
			At:         now,
		})
	})
	if err != nil {
		return nil, err
	}
	return s.fieldByID(ctx, in.FieldID)
}

// ReorderFields rewrites db_fields.sort_order for a full permutation of the
// database's live fields. Reordering emits nothing (SPEC-05 §5.2).
func (s *databaseService) ReorderFields(ctx context.Context, dbID string, orderedFieldIDs []string, actor events.Actor) error {
	now := s.clock.Now()
	return dbInTx(ctx, s.db, func(tx *sql.Tx) error {
		database, err := loadDatabase(ctx, tx, dbID)
		if err != nil {
			return err
		}
		if database.DeletedAt != nil {
			return fmt.Errorf("notes: reorder fields: %w", errs.ErrEntityDeleted)
		}
		live, err := dbLoadFields(ctx, tx, dbID, false)
		if err != nil {
			return err
		}
		liveIDs := make([]string, len(live))
		for i, field := range live {
			liveIDs[i] = field.ID
		}
		if err := checkPermutation("fields", orderedFieldIDs, liveIDs); err != nil {
			return err
		}
		for position, fieldID := range orderedFieldIDs {
			if _, err := tx.ExecContext(ctx,
				`UPDATE db_fields SET sort_order = ?, updated_at = ? WHERE id = ? AND database_id = ?`,
				position, clock.Format(now), fieldID, dbID); err != nil {
				return fmt.Errorf("notes: reorder fields: %w", err)
			}
		}
		return nil
	})
}

// DeleteField soft-deletes a field; values in db_records.data stay untouched.
// The title field cannot be deleted (SPEC-05 §5.2).
func (s *databaseService) DeleteField(ctx context.Context, fieldID string, actor events.Actor) error {
	return dbInTx(ctx, s.db, func(tx *sql.Tx) error {
		field, err := loadField(ctx, tx, fieldID, true)
		if err != nil {
			return err
		}
		if field.Role == RoleTitle {
			return dbValidationf("the title field cannot be deleted; delete the database instead")
		}
		return s.trash.SoftDelete(ctx, tx, trash.Ref{Type: schema.EntityDBField, ID: fieldID}, actor)
	})
}

// RestoreField clears db_fields.deleted_at; trash emits entity.restored.
func (s *databaseService) RestoreField(ctx context.Context, fieldID string, actor events.Actor) error {
	return dbInTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := loadField(ctx, tx, fieldID, true); err != nil {
			return err
		}
		return s.trash.Restore(ctx, tx, trash.Ref{Type: schema.EntityDBField, ID: fieldID}, actor)
	})
}

// ListFields returns the live fields of a database ordered by position, id.
func (s *databaseService) ListFields(ctx context.Context, dbID string) ([]*Field, error) {
	return dbLoadFields(ctx, s.db, dbID, false)
}

// fieldByID loads one field including soft-deleted ones.
func (s *databaseService) fieldByID(ctx context.Context, fieldID string) (*Field, error) {
	return loadField(ctx, s.db, fieldID, true)
}

// ---------------------------------------------------------------------------
// Row helpers
// ---------------------------------------------------------------------------

func scanDatabase(row dbRowScanner) (*ResearchDatabase, error) {
	var (
		database    ResearchDatabase
		createdAt   string
		updatedAt   string
		deletedAt   sql.NullString
		description string
	)
	if err := row.Scan(&database.ID, &database.Name, &description, &createdAt, &updatedAt, &deletedAt); err != nil {
		return nil, err
	}
	database.Description = description
	created, err := dbParseTime("research_databases.created_at", createdAt)
	if err != nil {
		return nil, err
	}
	updated, err := dbParseTime("research_databases.updated_at", updatedAt)
	if err != nil {
		return nil, err
	}
	deleted, err := dbParseNullableTime("research_databases.deleted_at", deletedAt)
	if err != nil {
		return nil, err
	}
	database.CreatedAt = created
	database.UpdatedAt = updated
	database.DeletedAt = deleted
	return &database, nil
}

func loadDatabase(ctx context.Context, q dbTX, dbID string) (*ResearchDatabase, error) {
	row := q.QueryRowContext(ctx,
		`SELECT id, name, description, created_at, updated_at, deleted_at
		   FROM research_databases WHERE id = ?`, dbID)
	database, err := scanDatabase(row)
	if err != nil {
		return nil, fmt.Errorf("notes: load database %s: %w", dbID, err)
	}
	return database, nil
}

// requireDatabaseRow returns sql.ErrNoRows when the database does not exist,
// even when it is soft-deleted (trash handles the idempotent no-op).
func requireDatabaseRow(ctx context.Context, q dbTX, dbID string) error {
	var exists int
	if err := q.QueryRowContext(ctx,
		`SELECT 1 FROM research_databases WHERE id = ?`, dbID).Scan(&exists); err != nil {
		return fmt.Errorf("notes: database %s: %w", dbID, err)
	}
	return nil
}

func scanField(row dbRowScanner) (*Field, error) {
	var (
		field     Field
		fieldType string
		role      string
		config    string
		createdAt string
		updatedAt string
		deletedAt sql.NullString
	)
	if err := row.Scan(&field.ID, &field.DatabaseID, &field.Name, &fieldType, &config,
		&field.Position, &role, &createdAt, &updatedAt, &deletedAt); err != nil {
		return nil, err
	}
	field.Type = FieldType(fieldType)
	field.Role = FieldRole(role)
	field.Config = json.RawMessage(config)
	created, err := dbParseTime("db_fields.created_at", createdAt)
	if err != nil {
		return nil, err
	}
	updated, err := dbParseTime("db_fields.updated_at", updatedAt)
	if err != nil {
		return nil, err
	}
	deleted, err := dbParseNullableTime("db_fields.deleted_at", deletedAt)
	if err != nil {
		return nil, err
	}
	field.CreatedAt = created
	field.UpdatedAt = updated
	field.DeletedAt = deleted
	return &field, nil
}

const fieldColumns = `id, database_id, name, type, config, sort_order, role, created_at, updated_at, deleted_at`

// dbLoadFields returns a database's fields; includeDeleted keeps trashed ones.
func dbLoadFields(ctx context.Context, q dbTX, databaseID string, includeDeleted bool) ([]*Field, error) {
	query := `SELECT ` + fieldColumns + ` FROM db_fields WHERE database_id = ?`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	query += ` ORDER BY sort_order, id`
	rows, err := q.QueryContext(ctx, query, databaseID)
	if err != nil {
		return nil, fmt.Errorf("notes: list fields: %w", err)
	}
	defer rows.Close()
	var out []*Field
	for rows.Next() {
		field, err := scanField(rows)
		if err != nil {
			return nil, fmt.Errorf("notes: list fields: %w", err)
		}
		out = append(out, field)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notes: list fields: %w", err)
	}
	return out, nil
}

func loadField(ctx context.Context, q dbTX, fieldID string, includeDeleted bool) (*Field, error) {
	query := `SELECT ` + fieldColumns + ` FROM db_fields WHERE id = ?`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	field, err := scanField(q.QueryRowContext(ctx, query, fieldID))
	if err != nil {
		return nil, fmt.Errorf("notes: load field %s: %w", fieldID, err)
	}
	return field, nil
}

// loadTitleField returns the database's live role=title field.
func loadTitleField(ctx context.Context, q dbTX, databaseID string) (*Field, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+fieldColumns+` FROM db_fields
		  WHERE database_id = ? AND role = 'title' AND deleted_at IS NULL`, databaseID)
	field, err := scanField(row)
	if err != nil {
		return nil, fmt.Errorf("notes: title field of database %s: %w", databaseID, err)
	}
	return field, nil
}

// checkFieldNameFree enforces the case-insensitive in-database uniqueness that
// migration 0250 also enforces with a partial unique index.
func checkFieldNameFree(ctx context.Context, q dbTX, databaseID, name, excludeFieldID string) error {
	rows, err := q.QueryContext(ctx,
		`SELECT id, name FROM db_fields WHERE database_id = ? AND deleted_at IS NULL`, databaseID)
	if err != nil {
		return fmt.Errorf("notes: check field name: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id       string
			existing string
		)
		if err := rows.Scan(&id, &existing); err != nil {
			return fmt.Errorf("notes: check field name: %w", err)
		}
		if id == excludeFieldID {
			continue
		}
		if strings.EqualFold(existing, name) {
			return dbValidationf("a field named %q already exists in this database", name)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("notes: check field name: %w", err)
	}
	return nil
}

// checkPermutation verifies that orderedIDs is a full permutation of the live
// ids passed in.
func checkPermutation(kind string, orderedIDs, liveIDs []string) error {
	if len(orderedIDs) != len(liveIDs) {
		return dbValidationf("ordered %s must contain all %d live ids, got %d", kind, len(liveIDs), len(orderedIDs))
	}
	known := make(map[string]bool, len(liveIDs))
	for _, id := range liveIDs {
		known[id] = true
	}
	seen := make(map[string]bool, len(orderedIDs))
	for _, id := range orderedIDs {
		if !known[id] {
			return dbValidationf("%s id %q is not a live row of this database", kind, id)
		}
		if seen[id] {
			return dbValidationf("%s id %q is repeated", kind, id)
		}
		seen[id] = true
	}
	return nil
}
