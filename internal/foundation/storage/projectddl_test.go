package storage

import (
	"database/sql"
	"testing"

	"researchos/internal/foundation/clock"
)

func now(t *testing.T) string {
	t.Helper()
	return clock.Format(testClock.Now())
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func mustFail(t *testing.T, db *sql.DB, label, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err == nil {
		t.Fatalf("%s: statement succeeded, want a constraint error", label)
	}
}

func insertIdea(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO ideas(id, title, created_at, updated_at) VALUES (?, 'idea', ?, ?)`, id, now(t), now(t))
}

func insertExperiment(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO experiments(id, title, created_at, updated_at) VALUES (?, 'experiment', ?, ?)`, id, now(t), now(t))
}

func insertNote(t *testing.T, db *sql.DB, id, filePath string) {
	t.Helper()
	mustExec(t, db,
		`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at) VALUES (?, 'note', ?, 'scratchpad', ?, ?)`,
		id, filePath, now(t), now(t))
}

func TestProjectCheckEnums(t *testing.T) {
	db, _ := openTestDB(t, KindProject)
	mustFail(t, db, "notes.kind",
		`INSERT INTO notes(id, file_path, kind, created_at, updated_at) VALUES ('n1', 'projects/p/notes/n1.md', 'bogus', ?, ?)`, now(t), now(t))
	mustFail(t, db, "notes.file_state",
		`INSERT INTO notes(id, file_path, kind, file_state, created_at, updated_at) VALUES ('n2', 'projects/p/notes/n2.md', 'scratchpad', 'broken', ?, ?)`, now(t), now(t))
	mustFail(t, db, "ideas.status",
		`INSERT INTO ideas(id, title, status, created_at, updated_at) VALUES ('i1', 'x', 'new', ?, ?)`, now(t), now(t))
	insertIdea(t, db, "idea-ok")
	mustFail(t, db, "evidence.direction",
		`INSERT INTO evidence(id, idea_id, direction, source_type, source_id, created_at, updated_at)
		 VALUES ('e1', 'idea-ok', 'neutral', 'paper', 'p1', ?, ?)`, now(t), now(t))
	mustFail(t, db, "snapshots.target_type",
		`INSERT INTO snapshots(id, target_type, target_id, created_at) VALUES ('s1', 'task', 't1', ?)`, now(t))
	mustFail(t, db, "weekly_reports.status",
		`INSERT INTO weekly_reports(id, period_start, period_end, status, created_at, updated_at)
		 VALUES ('w1', '2026-01-01', '2026-01-07', 'sent', ?, ?)`, now(t), now(t))
	mustFail(t, db, "activities.event_type",
		`INSERT INTO activities(id, event_type, entity_type, entity_id, payload, actor, created_at)
		 VALUES ('a1', 'entity.viewed', 'note', 'n1', '{}', 'user', ?)`, now(t))
	mustFail(t, db, "activities.actor",
		`INSERT INTO activities(id, event_type, entity_type, entity_id, payload, actor, created_at)
		 VALUES ('a2', 'entity.created', 'note', 'n1', '{}', 'system', ?)`, now(t))
	mustFail(t, db, "entity_relations.relation",
		`INSERT INTO entity_relations(id, src_type, src_id, relation, dst_type, dst_id, created_at)
		 VALUES ('r1', 'note', 'n1', 'includes', 'note', 'n2', ?)`, now(t))
	mustFail(t, db, "external_refs.ref_kind",
		`INSERT INTO external_refs(id, owner_type, owner_id, field, ref_kind, path, created_at, updated_at)
		 VALUES ('x1', 'experiment', 'e1', 'dir_ref', 'symlink', '/tmp/x', ?, ?)`, now(t), now(t))
	mustFail(t, db, "project_papers.linked_by",
		`INSERT INTO project_papers(project_id, paper_id, linked_at, linked_by)
		 VALUES ('p1', 'paper1', ?, 'ai')`, now(t))
	mustFail(t, db, "tasks.blocked",
		`INSERT INTO tasks(id, title, "column", blocked, created_at, updated_at)
		 VALUES ('t1', 'task', 'todo', 2, ?, ?)`, now(t), now(t))
	mustFail(t, db, "evidence.ai_generated",
		`INSERT INTO evidence(id, idea_id, direction, source_type, source_id, ai_generated, created_at, updated_at)
		 VALUES ('e2', 'idea-ok', 'support', 'paper', 'p1', 3, ?, ?)`, now(t), now(t))
}

func TestGlobalCheckEnums(t *testing.T) {
	db, _ := openTestDB(t, KindGlobal)
	mustFail(t, db, "projects.status",
		`INSERT INTO projects(id, name, status, created_at, updated_at) VALUES ('p1', 'x', 'paused', ?, ?)`, now(t), now(t))
	mustFail(t, db, "projects.name",
		`INSERT INTO projects(id, name, created_at, updated_at) VALUES ('p2', '', ?, ?)`, now(t), now(t))
	mustFail(t, db, "papers.file_state",
		`INSERT INTO papers(id, zotero_key, file_state, imported_at, updated_at) VALUES ('p3', 'k', 'broken', ?, ?)`, now(t), now(t))
	mustFail(t, db, "papers.source_state",
		`INSERT INTO papers(id, zotero_key, source_state, imported_at, updated_at) VALUES ('p4', 'k2', 'broken', ?, ?)`, now(t), now(t))
	mustFail(t, db, "providers.protocol",
		`INSERT INTO providers(id, name, protocol, base_url, api_key_ref, created_at, updated_at)
		 VALUES ('pr1', 'x', 'anthropic_messages', 'https://x', 'ref', ?, ?)`, now(t), now(t))
}

func TestProjectUniqueIndexes(t *testing.T) {
	db, _ := openTestDB(t, KindProject)

	insertNote(t, db, "note-a", "projects/p/notes/a.md")
	mustFail(t, db, "ux_notes_file_path",
		`INSERT INTO notes(id, file_path, kind, created_at, updated_at) VALUES ('note-b', 'projects/p/notes/a.md', 'scratchpad', ?, ?)`, now(t), now(t))

	mustExec(t, db, `INSERT INTO research_databases(id, name, created_at, updated_at) VALUES ('db1', 'db', ?, ?)`, now(t), now(t))
	mustExec(t, db, `INSERT INTO db_records(id, database_id, note_id, created_at, updated_at) VALUES ('rec1', 'db1', 'note-a', ?, ?)`, now(t), now(t))
	mustFail(t, db, "ux_db_records_note",
		`INSERT INTO db_records(id, database_id, note_id, created_at, updated_at) VALUES ('rec2', 'db1', 'note-a', ?, ?)`, now(t), now(t))
	// The partial index ignores NULL note_id, so many records without a page coexist.
	mustExec(t, db, `INSERT INTO db_records(id, database_id, note_id, created_at, updated_at) VALUES ('rec3', 'db1', NULL, ?, ?)`, now(t), now(t))
	mustExec(t, db, `INSERT INTO db_records(id, database_id, note_id, created_at, updated_at) VALUES ('rec4', 'db1', NULL, ?, ?)`, now(t), now(t))

	mustExec(t, db,
		`INSERT INTO entity_relations(id, src_type, src_id, relation, dst_type, dst_id, created_at)
		 VALUES ('rel1', 'note', 'note-a', 'related_to', 'note', 'note-a', ?)`, now(t))
	mustFail(t, db, "ux_entity_relations_tuple",
		`INSERT INTO entity_relations(id, src_type, src_id, relation, dst_type, dst_id, created_at)
		 VALUES ('rel2', 'note', 'note-a', 'related_to', 'note', 'note-a', ?)`, now(t))

	mustExec(t, db,
		`INSERT INTO activities(id, event_type, entity_type, entity_id, payload, actor, merge_key, created_at)
		 VALUES ('act1', 'note.updated', 'note', 'note-a', '{}', 'user', 'note.updated:note-a:2026-01-02', ?)`, now(t))
	mustFail(t, db, "ux_activities_merge_key",
		`INSERT INTO activities(id, event_type, entity_type, entity_id, payload, actor, merge_key, created_at)
		 VALUES ('act2', 'note.updated', 'note', 'note-a', '{}', 'user', 'note.updated:note-a:2026-01-02', ?)`, now(t))
	// Other events carry no merge_key and are not constrained.
	for _, id := range []string{"act3", "act4"} {
		mustExec(t, db,
			`INSERT INTO activities(id, event_type, entity_type, entity_id, payload, actor, created_at)
			 VALUES (?, 'entity.created', 'note', 'note-a', '{}', 'user', ?)`, id, now(t))
	}

	mustExec(t, db,
		`INSERT INTO external_refs(id, owner_type, owner_id, field, ref_kind, path, created_at, updated_at)
		 VALUES ('ref1', 'experiment', 'exp1', 'dir_ref', 'directory', '/tmp/exp', ?, ?)`, now(t), now(t))
	mustFail(t, db, "ux_external_refs_owner",
		`INSERT INTO external_refs(id, owner_type, owner_id, field, ref_kind, path, created_at, updated_at)
		 VALUES ('ref2', 'experiment', 'exp1', 'dir_ref', 'directory', '/tmp/other', ?, ?)`, now(t), now(t))
}

func TestProjectForeignKeys(t *testing.T) {
	db, _ := openTestDB(t, KindProject)

	// ideas -> evidence CASCADE.
	insertIdea(t, db, "idea-1")
	mustExec(t, db,
		`INSERT INTO evidence(id, idea_id, direction, source_type, source_id, created_at, updated_at)
		 VALUES ('ev1', 'idea-1', 'support', 'paper', 'p1', ?, ?)`, now(t), now(t))
	mustExec(t, db, `DELETE FROM ideas WHERE id = 'idea-1'`)
	if got := countRows(t, db, `SELECT count(*) FROM evidence WHERE id = 'ev1'`); got != 0 {
		t.Fatalf("evidence survived the idea cascade: %d", got)
	}

	// experiments -> runs CASCADE.
	insertExperiment(t, db, "exp-1")
	mustExec(t, db, `INSERT INTO runs(id, experiment_id, created_at, updated_at) VALUES ('run-1', 'exp-1', ?, ?)`, now(t), now(t))
	mustExec(t, db, `DELETE FROM experiments WHERE id = 'exp-1'`)
	if got := countRows(t, db, `SELECT count(*) FROM runs WHERE id = 'run-1'`); got != 0 {
		t.Fatalf("run survived the experiment cascade: %d", got)
	}

	// research_databases -> db_fields / db_records CASCADE.
	mustExec(t, db, `INSERT INTO research_databases(id, name, created_at, updated_at) VALUES ('rdb', 'db', ?, ?)`, now(t), now(t))
	mustExec(t, db,
		`INSERT INTO db_fields(id, database_id, name, type, created_at, updated_at)
		 VALUES ('fld', 'rdb', 'field', 'text', ?, ?)`, now(t), now(t))
	mustExec(t, db, `INSERT INTO db_records(id, database_id, created_at, updated_at) VALUES ('rec', 'rdb', ?, ?)`, now(t), now(t))
	mustExec(t, db, `DELETE FROM research_databases WHERE id = 'rdb'`)
	if got := countRows(t, db, `SELECT count(*) FROM db_fields WHERE id = 'fld'`); got != 0 {
		t.Fatalf("db_field survived the database cascade: %d", got)
	}
	if got := countRows(t, db, `SELECT count(*) FROM db_records WHERE id = 'rec'`); got != 0 {
		t.Fatalf("db_record survived the database cascade: %d", got)
	}

	// db_records.note_id SET NULL when the note row is removed.
	mustExec(t, db, `INSERT INTO research_databases(id, name, created_at, updated_at) VALUES ('rdb2', 'db2', ?, ?)`, now(t), now(t))
	insertNote(t, db, "note-keep", "projects/p/notes/keep.md")
	mustExec(t, db,
		`INSERT INTO db_records(id, database_id, note_id, created_at, updated_at)
		 VALUES ('rec-link', 'rdb2', 'note-keep', ?, ?)`, now(t), now(t))
	mustExec(t, db, `DELETE FROM notes WHERE id = 'note-keep'`)
	var noteID sql.NullString
	if err := db.QueryRow(`SELECT note_id FROM db_records WHERE id = 'rec-link'`).Scan(&noteID); err != nil {
		t.Fatal(err)
	}
	if noteID.Valid {
		t.Fatalf("db_records.note_id = %q, want NULL after note deletion", noteID.String)
	}
	if got := countRows(t, db, `SELECT count(*) FROM db_records WHERE id = 'rec-link'`); got != 1 {
		t.Fatal("the record row itself was deleted")
	}
}

func TestAC26AIReservedColumns(t *testing.T) {
	db, _ := openTestDB(t, KindProject)
	insertIdea(t, db, "idea-ai")
	mustExec(t, db,
		`INSERT INTO evidence(id, idea_id, direction, source_type, source_id, created_at, updated_at)
		 VALUES ('ev-ai', 'idea-ai', 'support', 'paper', 'p1', ?, ?)`, now(t), now(t))

	var provenance string
	var aiOutput sql.NullString
	if err := db.QueryRow(`SELECT provenance, ai_output_id FROM evidence WHERE id = 'ev-ai'`).Scan(&provenance, &aiOutput); err != nil {
		t.Fatal(err)
	}
	if provenance != "{}" {
		t.Fatalf("evidence.provenance default = %q, want {}", provenance)
	}
	if aiOutput.Valid {
		t.Fatalf("evidence.ai_output_id default = %q, want NULL", aiOutput.String)
	}
	// The ai_outputs table does not exist, yet the column is writable everywhere.
	if got := countRows(t, db, `SELECT count(*) FROM sqlite_master WHERE name = 'ai_outputs'`); got != 0 {
		t.Fatal("ai_outputs must not exist in the v1 project schema")
	}
	mustExec(t, db, `UPDATE evidence SET ai_output_id = 'ai-1' WHERE id = 'ev-ai'`)
	mustExec(t, db,
		`INSERT INTO snapshots(id, target_type, target_id, ai_output_id, created_at)
		 VALUES ('snap-ai', 'run', 'run-1', 'ai-1', ?)`, now(t))
	mustExec(t, db,
		`INSERT INTO weekly_reports(id, period_start, period_end, ai_output_id, created_at, updated_at)
		 VALUES ('wr-ai', '2026-01-01', '2026-01-07', 'ai-1', ?, ?)`, now(t), now(t))

	// meta survives a JSON round trip byte for byte.
	meta := `{"coverage":{"notes":3},"provenance":{"provider":"p"},"checks":[true,false]}`
	mustExec(t, db, `UPDATE weekly_reports SET meta = ? WHERE id = 'wr-ai'`, meta)
	var gotMeta string
	if err := db.QueryRow(`SELECT meta FROM weekly_reports WHERE id = 'wr-ai'`).Scan(&gotMeta); err != nil {
		t.Fatal(err)
	}
	if gotMeta != meta {
		t.Fatalf("weekly_reports.meta = %q, want %q", gotMeta, meta)
	}
	if got := countRows(t, db, `SELECT json_extract(meta, '$.coverage.notes') FROM weekly_reports WHERE id = 'wr-ai'`); got != 3 {
		t.Fatalf("json_extract coverage.notes = %d, want 3", got)
	}

	// confirmed_at is writable and clearable by the status flow.
	mustExec(t, db, `UPDATE weekly_reports SET status = 'confirmed', confirmed_at = ? WHERE id = 'wr-ai'`, now(t))
	var confirmedAt sql.NullString
	if err := db.QueryRow(`SELECT confirmed_at FROM weekly_reports WHERE id = 'wr-ai'`).Scan(&confirmedAt); err != nil {
		t.Fatal(err)
	}
	if !confirmedAt.Valid {
		t.Fatal("confirmed_at was not written")
	}
	mustExec(t, db, `UPDATE weekly_reports SET status = 'draft', confirmed_at = NULL WHERE id = 'wr-ai'`)
	if err := db.QueryRow(`SELECT confirmed_at FROM weekly_reports WHERE id = 'wr-ai'`).Scan(&confirmedAt); err != nil {
		t.Fatal(err)
	}
	if confirmedAt.Valid {
		t.Fatal("confirmed_at was not cleared")
	}
}

func TestAC27RunStatusAndCascade(t *testing.T) {
	db, _ := openTestDB(t, KindProject)
	insertExperiment(t, db, "exp-runs")

	// An experiment without runs is legal.
	if got := countRows(t, db, `SELECT count(*) FROM runs WHERE experiment_id = 'exp-runs'`); got != 0 {
		t.Fatalf("runs = %d, want 0", got)
	}

	mustExec(t, db, `INSERT INTO runs(id, experiment_id, created_at, updated_at) VALUES ('run-default', 'exp-runs', ?, ?)`, now(t), now(t))
	var status string
	if err := db.QueryRow(`SELECT status FROM runs WHERE id = 'run-default'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "unknown" {
		t.Fatalf("runs.status default = %q, want unknown", status)
	}

	for _, id := range []string{"run-running", "run-finished", "run-failed", "run-custom"} {
		mustExec(t, db, `INSERT INTO runs(id, experiment_id, created_at, updated_at) VALUES (?, 'exp-runs', ?, ?)`, id, now(t), now(t))
	}
	for id, want := range map[string]string{
		"run-running":  "running",
		"run-finished": "finished",
		"run-failed":   "failed",
		"run-custom":   "user-defined-state",
	} {
		mustExec(t, db, `UPDATE runs SET status = ? WHERE id = ?`, want, id)
	}
	if got := countRows(t, db, `SELECT count(*) FROM runs WHERE experiment_id = 'exp-runs'`); got != 5 {
		t.Fatalf("runs = %d, want 5", got)
	}

	mustExec(t, db, `DELETE FROM experiments WHERE id = 'exp-runs'`)
	if got := countRows(t, db, `SELECT count(*) FROM runs`); got != 0 {
		t.Fatalf("runs left after experiment deletion: %d", got)
	}
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}
