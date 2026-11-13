package migrations

import (
	"database/sql"
	"strings"
	"testing"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
)

// TestProjectMigration0250 applies the SPEC-05 band to a fresh project database
// and checks the columns, tables and partial unique indexes of §4.2.
func TestProjectMigration0250(t *testing.T) {
	fx, err := foundationtest.NewWithProject(t.TempDir(), foundationtest.Options{
		Migrations: MustAll(),
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() { _ = fx.Close() })
	db := fx.Project.DB()
	c := fx.Clock

	// schema_migrations records the band.
	var version int
	if err := db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("max migration: %v", err)
	}
	if version != 250 {
		t.Fatalf("project schema version = %d, want 250", version)
	}

	for _, column := range []struct{ table, name string }{
		{"research_databases", "description"},
		{"db_fields", "role"},
	} {
		if !coreHasColumn(t, db, column.table, column.name) {
			t.Fatalf("%s.%s is missing", column.table, column.name)
		}
	}
	for _, table := range []string{"tags", "entity_tags"} {
		if !coreHasTable(t, db, table) {
			t.Fatalf("table %s is missing", table)
		}
	}

	now := clock.Format(c.Now())
	dbID := ids.MustNew(c)
	if _, err := db.Exec(
		`INSERT INTO research_databases(id, name, description, created_at, updated_at)
		 VALUES (?, 'DB', '', ?, ?)`, dbID, now, now); err != nil {
		t.Fatalf("insert database: %v", err)
	}
	insertField := func(name, role string) error {
		_, err := db.Exec(
			`INSERT INTO db_fields(id, database_id, name, type, config, role, sort_order, created_at, updated_at)
			 VALUES (?, ?, ?, 'text', '{}', ?, 0, ?, ?)`,
			ids.MustNew(c), dbID, name, role, now, now)
		return err
	}
	if err := insertField("Name", "title"); err != nil {
		t.Fatalf("first title field: %v", err)
	}
	if err := insertField("Other", "title"); err == nil {
		t.Fatal("a second title field was accepted")
	}
	if err := insertField("name", "plain"); err == nil {
		t.Fatal("a case-insensitive duplicate field name was accepted")
	}
	// Soft-deleted rows free the name for reuse.
	if _, err := db.Exec(
		`UPDATE db_fields SET deleted_at = ? WHERE database_id = ? AND lower(name) = 'name'`, now, dbID); err != nil {
		t.Fatalf("soft delete field: %v", err)
	}
	if err := insertField("Name", "plain"); err != nil {
		t.Fatalf("reusing a soft-deleted field name: %v", err)
	}

	// Tag names are unique case-insensitively, soft-deleted rows excluded.
	tagID := ids.MustNew(c)
	if _, err := db.Exec(
		`INSERT INTO tags(id, name, color, created_at, updated_at) VALUES (?, 'Reading', '', ?, ?)`,
		tagID, now, now); err != nil {
		t.Fatalf("insert tag: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO tags(id, name, color, created_at, updated_at) VALUES (?, 'reading', '', ?, ?)`,
		ids.MustNew(c), now, now); err == nil {
		t.Fatal("case-insensitive duplicate tag name was accepted")
	}

	// entity_tags: composite primary key plus the tag foreign key.
	entityID := ids.MustNew(c)
	if _, err := db.Exec(
		`INSERT INTO entity_tags(tag_id, entity_type, entity_id, created_at) VALUES (?, 'note', ?, ?)`,
		tagID, entityID, now); err != nil {
		t.Fatalf("insert entity tag: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO entity_tags(tag_id, entity_type, entity_id, created_at) VALUES (?, 'note', ?, ?)`,
		tagID, entityID, now); err == nil {
		t.Fatal("duplicate entity tag was accepted")
	}
	if _, err := db.Exec(
		`INSERT INTO entity_tags(tag_id, entity_type, entity_id, created_at) VALUES (?, 'note', ?, ?)`,
		ids.MustNew(c), entityID, now); err == nil {
		t.Fatal("entity tag with an unknown tag id was accepted")
	}

	// Foreign keys are per connection, so check on the same pool.
	for _, index := range []string{
		"ux_db_fields_title_role", "ux_db_fields_name", "ux_tags_name", "idx_entity_tags_entity",
	} {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&name)
		if err != nil {
			t.Fatalf("index %s missing: %v", index, err)
		}
	}
}

func coreHasTable(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	return err == nil && name == table
}

func coreHasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info('` + table + `')`)
	if err != nil {
		t.Fatalf("pragma table_info %s: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("pragma scan: %v", err)
		}
		if strings.EqualFold(name, column) {
			return true
		}
	}
	return false
}
