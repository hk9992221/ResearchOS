package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/schema"
)

var testClock = &clock.Fixed{T: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}

func openTestDB(t *testing.T, kind Kind) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), string(kind)+".db")
	db, err := OpenDatabase(context.Background(), path, kind, Options{Clock: testClock, SkipBackup: true})
	if err != nil {
		t.Fatalf("OpenDatabase(%s): %v", kind, err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

type recordingLogger struct {
	warns []string
}

func (l *recordingLogger) Warn(msg string, kv ...any) { l.warns = append(l.warns, msg) }

func TestPragmasAndPool(t *testing.T) {
	db, _ := openTestDB(t, KindGlobal)
	var journal string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(journal, "wal") {
		t.Errorf("journal_mode = %q, want wal", journal)
	}
	var synchronous, foreignKeys, busyTimeout int
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 1 {
		t.Errorf("synchronous = %d, want 1 (NORMAL)", synchronous)
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busyTimeout)
	}
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}
}

func TestOpenDatabasePathWithSpace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "with space")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "global.db")
	db, err := OpenDatabase(context.Background(), path, KindGlobal, Options{SkipBackup: true})
	if err != nil {
		t.Fatalf("OpenDatabase with spaces: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM projects`).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
}

func TestAC02Baseline(t *testing.T) {
	for _, kind := range []Kind{KindGlobal, KindProject} {
		t.Run(string(kind), func(t *testing.T) {
			db, _ := openTestDB(t, kind)
			var count, version int
			var name string
			if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("schema_migrations rows = %d, want 1", count)
			}
			if err := db.QueryRow(`SELECT version, name FROM schema_migrations`).Scan(&version, &name); err != nil {
				t.Fatal(err)
			}
			if version != 1 || name != "core" {
				t.Fatalf("baseline row = (%d, %q), want (1, \"core\")", version, name)
			}
			// user_version is deliberately unused: schema state lives only in
			// schema_migrations.
			var userVersion int
			if err := db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
				t.Fatal(err)
			}
			if userVersion != 0 {
				t.Fatalf("PRAGMA user_version = %d, want 0 (unused)", userVersion)
			}

			// No AUTOINCREMENT anywhere.
			rows, err := db.Query(`SELECT name, sql FROM sqlite_master WHERE type = 'table'`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			type table struct{ name, sql string }
			var tables []table
			for rows.Next() {
				var tb table
				var sqlText sql.NullString
				if err := rows.Scan(&tb.name, &sqlText); err != nil {
					t.Fatal(err)
				}
				tb.sql = sqlText.String
				tables = append(tables, tb)
				if strings.Contains(strings.ToUpper(tb.sql), "AUTOINCREMENT") {
					t.Errorf("table %s uses AUTOINCREMENT", tb.name)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}

			for _, tb := range tables {
				if strings.HasPrefix(tb.name, "sqlite_") {
					continue
				}
				info, err := db.Query(`PRAGMA table_info("` + tb.name + `")`)
				if err != nil {
					t.Fatal(err)
				}
				for info.Next() {
					var (
						cid     int
						name    string
						ctype   string
						notNull int
						dflt    sql.NullString
						pk      int
					)
					if err := info.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
						t.Fatal(err)
					}
					if pk == 0 {
						continue
					}
					if tb.name == "schema_migrations" {
						// The migration bookkeeping table is the documented exception:
						// its version is an INTEGER primary key.
						if ctype != "INTEGER" {
							t.Errorf("schema_migrations PK type = %q, want INTEGER", ctype)
						}
						continue
					}
					if ctype != "TEXT" {
						t.Errorf("%s.%s PK type = %q, want TEXT", tb.name, name, ctype)
					}
				}
				if err := info.Err(); err != nil {
					t.Fatal(err)
				}
				info.Close()
			}
		})
	}
}

func TestAC12MigrationLifecycle(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	wsRoot := filepath.Join(base, "workspace")
	if err := os.MkdirAll(wsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(wsRoot, "global.db")
	backupRoot := filepath.Join(base, "cache", "backups")
	opt := Options{Clock: testClock, BackupRoot: backupRoot}

	db, err := OpenDatabase(ctx, dbPath, KindGlobal, opt)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	defer db.Close()

	now := clock.Format(testClock.Now())
	if _, err := db.Exec(`INSERT INTO settings(key, value, updated_at) VALUES ('app.last_project_id', 'keep-me', ?)`, now); err != nil {
		t.Fatal(err)
	}

	v2 := Migration{Version: 2, Name: "test_v2", SQL: `CREATE TABLE test_v2 (id TEXT NOT NULL PRIMARY KEY);`}
	opt.Extra = map[Kind][]Migration{KindGlobal: {v2}}
	res, err := Migrate(ctx, db, KindGlobal, dbPath, opt)
	if err != nil {
		t.Fatalf("Migrate v2: %v", err)
	}
	if res.From != 1 || res.To != 2 {
		t.Fatalf("result versions = %d -> %d, want 1 -> 2", res.From, res.To)
	}
	if len(res.Applied) != 1 || res.Applied[0] != 2 {
		t.Fatalf("applied = %v, want [2]", res.Applied)
	}
	if res.BackupPath == "" {
		t.Fatal("no backup was taken before migrating")
	}
	if strings.HasPrefix(filepath.Clean(res.BackupPath), filepath.Clean(wsRoot)) {
		t.Fatalf("backup %q is inside the workspace", res.BackupPath)
	}
	if !strings.HasPrefix(filepath.Clean(res.BackupPath), filepath.Clean(backupRoot)) {
		t.Fatalf("backup %q is not under the injected backup root", res.BackupPath)
	}
	var migrated, kept int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&migrated); err != nil {
		t.Fatal(err)
	}
	if migrated != 2 {
		t.Fatalf("schema_migrations rows = %d, want 2", migrated)
	}
	if err := db.QueryRow(`SELECT count(*) FROM settings WHERE value = 'keep-me'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatal("existing data was not preserved")
	}

	// Idempotent re-run: nothing applied, no new backup.
	backupsBefore := backupFiles(t, backupRoot)
	res2, err := Migrate(ctx, db, KindGlobal, dbPath, opt)
	if err != nil {
		t.Fatalf("Migrate idempotent: %v", err)
	}
	if len(res2.Applied) != 0 || res2.To != 2 {
		t.Fatalf("idempotent result = %+v, want no applied versions at 2", res2)
	}
	if got := backupFiles(t, backupRoot); len(got) != len(backupsBefore) {
		t.Fatalf("idempotent migration created a backup: %v -> %v", backupsBefore, got)
	}

	// A failing migration in the same transaction leaves no partial DDL and no
	// version bump, but does leave an integrity-checked pre-migration backup.
	v3 := Migration{Version: 3, Name: "test_fail", SQL: `CREATE TABLE test_fail (id TEXT NOT NULL PRIMARY KEY);
INSERT INTO missing_table(id) VALUES ('x');`}
	opt.Extra = map[Kind][]Migration{KindGlobal: {v2, v3}}
	if _, err := Migrate(ctx, db, KindGlobal, dbPath, opt); err == nil {
		t.Fatal("failing migration reported success")
	}
	var maxVersion int
	if err := db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&maxVersion); err != nil {
		t.Fatal(err)
	}
	if maxVersion != 2 {
		t.Fatalf("max version after failure = %d, want 2", maxVersion)
	}
	var failedTables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'test_fail'`).Scan(&failedTables); err != nil {
		t.Fatal(err)
	}
	if failedTables != 0 {
		t.Fatal("partial DDL from the failing migration survived")
	}
	files := backupFiles(t, backupRoot)
	if len(files) != 2 {
		t.Fatalf("backups = %v, want 2 (one per attempted migration step)", files)
	}
	for _, f := range files {
		assertBackupIntegrity(t, f)
	}

	// A database newer than the build is refused and left untouched.
	if _, err := db.Exec(`INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (99, 'future', 'x', ?)`, now); err != nil {
		t.Fatal(err)
	}
	before := schemaNames(t, db)
	_, err = Migrate(ctx, db, KindGlobal, dbPath, opt)
	if !errors.Is(err, errs.ErrSchemaTooNew) {
		t.Fatalf("Migrate(too new) = %v, want ErrSchemaTooNew", err)
	}
	after := schemaNames(t, db)
	if len(before) != len(after) {
		t.Fatalf("schema changed on ErrSchemaTooNew: %v -> %v", before, after)
	}
}

func TestMigrationChecksumTamper(t *testing.T) {
	ctx := context.Background()
	db, path := openTestDB(t, KindGlobal)
	if _, err := db.Exec(`UPDATE schema_migrations SET checksum = 'deadbeef' WHERE version = 1`); err != nil {
		t.Fatal(err)
	}
	_, err := Migrate(ctx, db, KindGlobal, path, Options{Clock: testClock, SkipBackup: true})
	if !errors.Is(err, errs.ErrMigrationTampered) {
		t.Fatalf("Migrate(tampered) = %v, want ErrMigrationTampered", err)
	}
}

func TestMigrationDisappearedFromBuild(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "global.db")
	v2 := Migration{Version: 2, Name: "test_v2", SQL: `CREATE TABLE test_v2 (id TEXT NOT NULL PRIMARY KEY);`}
	db, err := OpenDatabase(ctx, path, KindGlobal, Options{
		Clock: testClock, SkipBackup: true,
		Extra: map[Kind][]Migration{KindGlobal: {v2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// The new build has v3 but no longer ships the applied v2.
	v3 := Migration{Version: 3, Name: "test_v3", SQL: `CREATE TABLE test_v3 (id TEXT NOT NULL PRIMARY KEY);`}
	_, err = Migrate(ctx, db, KindGlobal, path, Options{
		Clock: testClock, SkipBackup: true,
		Extra: map[Kind][]Migration{KindGlobal: {v3}},
	})
	if !errors.Is(err, errs.ErrMigrationTampered) {
		t.Fatalf("Migrate(without v2) = %v, want ErrMigrationTampered", err)
	}
}

func TestDuplicateMigrationVersion(t *testing.T) {
	_, err := migrationsFor(KindGlobal, []Migration{{Version: 1, Name: "dup", SQL: "SELECT 1;"}})
	if err == nil {
		t.Fatal("duplicate version accepted")
	}
}

func TestLoadFSRejectsBadNames(t *testing.T) {
	for name, body := range map[string]string{
		"bad.sql":           "SELECT 1;",
		"0100_bad-name.sql": "SELECT 1;",
		"0000_zero.sql":     "SELECT 1;",
		"1_short.sql":       "SELECT 1;",
	} {
		fsys := fstest.MapFS{name: &fstest.MapFile{Data: []byte(body)}}
		if _, err := LoadFS(fsys, "."); err == nil {
			t.Errorf("LoadFS accepted %q", name)
		}
	}
}

func TestBackupRetention(t *testing.T) {
	dir := t.TempDir()
	for day := 1; day <= 7; day++ {
		name := fmt.Sprintf("2026010%d-000000-v1.db", day)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".tmp-hidden"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pruneBackups(dir, backupKeep); err != nil {
		t.Fatalf("pruneBackups: %v", err)
	}
	left := backupFiles(t, dir)
	if len(left) != backupKeep {
		t.Fatalf("retained %d backups, want %d (%v)", len(left), backupKeep, left)
	}
	sort.Strings(left)
	if filepath.Base(left[0]) != "20260103-000000-v1.db" {
		t.Fatalf("oldest retained = %s, want 20260103", filepath.Base(left[0]))
	}
}

func TestBackupDirectoryUnavailableWarnsAndContinues(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	logger := &recordingLogger{}
	path := filepath.Join(base, "global.db")
	opt := Options{Clock: testClock, SkipBackup: true}
	db, err := OpenDatabase(ctx, path, KindGlobal, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	opt = Options{
		Clock:      testClock,
		BackupRoot: filepath.Join(blocker, "backups"),
		Logger:     logger,
		Extra: map[Kind][]Migration{KindGlobal: {
			{Version: 2, Name: "test_v2", SQL: `CREATE TABLE test_v2 (id TEXT NOT NULL PRIMARY KEY);`},
		}},
	}
	res, err := Migrate(ctx, db, KindGlobal, path, opt)
	if err != nil {
		t.Fatalf("Migrate with unavailable backup dir: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("BackupPath = %q, want empty", res.BackupPath)
	}
	if len(logger.warns) == 0 {
		t.Fatal("no warning was logged for the unavailable backup directory")
	}
}

func TestAC16NoSearchOrAITables(t *testing.T) {
	forbiddenPrefixes := []string{"fts", "index_"}
	forbiddenExact := []string{
		"ai_outputs", "paper_chunks", "paper_chunk_embeddings", "chunk_embeddings",
		"index_sources", "index_chunks", "index_jobs", "provider_models",
	}
	for _, kind := range []Kind{KindGlobal, KindProject} {
		db, _ := openTestDB(t, kind)
		rows, err := db.Query(`SELECT name FROM sqlite_master`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			lower := strings.ToLower(name)
			for _, p := range forbiddenPrefixes {
				if strings.HasPrefix(lower, p) {
					t.Errorf("%s: table %q violates the reserved index band", kind, name)
				}
			}
			for _, e := range forbiddenExact {
				if lower == e {
					t.Errorf("%s: reserved table %q was created", kind, name)
				}
			}
		}
		rows.Close()
	}

	ctx := context.Background()
	db, path := openTestDB(t, KindGlobal)
	bad := Migration{Version: 100, Name: "search_index", SQL: `CREATE TABLE index_probe (id TEXT NOT NULL PRIMARY KEY);
SELECT * FROM does_not_exist;`}
	_, err := Migrate(ctx, db, KindGlobal, path, Options{
		Clock:      testClock,
		SkipBackup: true,
		Extra:      map[Kind][]Migration{KindGlobal: {bad}},
	})
	if err == nil {
		t.Fatal("failing 0100 migration reported success")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("schema_migrations rows = %d, want 1", count)
	}
	var probe int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'index_probe'`).Scan(&probe); err != nil {
		t.Fatal(err)
	}
	if probe != 0 {
		t.Fatal("partial 0100 DDL survived")
	}
	reopened, err := OpenDatabase(ctx, path, KindGlobal, Options{Clock: testClock, SkipBackup: true})
	if err != nil {
		t.Fatalf("reopen at v1 after a failed 0100: %v", err)
	}
	reopened.Close()
}

func TestGoldenSchema(t *testing.T) {
	cases := []struct {
		kind Kind
		file string
	}{
		{KindGlobal, "global_v1.golden.sql"},
		{KindProject, "project_v1.golden.sql"},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			db, _ := openTestDB(t, tc.kind)
			got := dumpSchema(t, db)
			golden := filepath.Join("testdata", "schema", tc.file)
			if os.Getenv("RESEARCHOS_UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("updated %s", golden)
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (regenerate with RESEARCHOS_UPDATE_GOLDEN=1): %v", err)
			}
			if string(want) != got {
				t.Fatalf("schema differs from %s\n--- want ---\n%s\n--- got ---\n%s", golden, want, got)
			}
		})
	}
}

func TestEnumsMatchDDLChecks(t *testing.T) {
	globalSQL := mustMigrationText(t, KindGlobal, 1)
	projectSQL := mustMigrationText(t, KindProject, 1)
	cases := []struct {
		label    string
		sql      string
		table    string
		column   string
		registry []string
	}{
		{"projects.status", globalSQL, "projects", "status", stringsOf(schema.AllProjectStatuses())},
		{"papers.file_state", globalSQL, "papers", "file_state", stringsOf(schema.AllPaperFileStates())},
		{"papers.source_state", globalSQL, "papers", "source_state", stringsOf(schema.AllPaperSourceStates())},
		{"notes.kind", projectSQL, "notes", "kind", stringsOf(schema.AllNoteKinds())},
		{"notes.file_state", projectSQL, "notes", "file_state", stringsOf(schema.AllPaperFileStates())},
		{"ideas.status", projectSQL, "ideas", "status", stringsOf(schema.AllIdeaStatuses())},
		{"evidence.direction", projectSQL, "evidence", "direction", stringsOf(schema.AllDirections())},
		{"weekly_reports.status", projectSQL, "weekly_reports", "status", stringsOf(schema.AllReportStatuses())},
		{"activities.event_type", projectSQL, "activities", "event_type", stringsOf(schema.AllEventTypes())},
		{"activities.actor", projectSQL, "activities", "actor", stringsOf(schema.AllActors())},
		{"entity_relations.relation", projectSQL, "entity_relations", "relation", stringsOf(schema.AllRelations())},
	}
	for _, c := range cases {
		got := checkLiterals(t, c.sql, c.table, c.column)
		want := append([]string(nil), c.registry...)
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: DDL literals %v != registry %v", c.label, got, want)
		}
	}
}

// --- helpers ---

func backupFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".db") && !strings.HasPrefix(d.Name(), ".") {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func assertBackupIntegrity(t *testing.T, path string) {
	t.Helper()
	db, err := openReadOnly(context.Background(), path)
	if err != nil {
		t.Fatalf("open backup %s: %v", path, err)
	}
	defer db.Close()
	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		t.Fatalf("integrity_check %s: %v", path, err)
	}
	if result != "ok" {
		t.Fatalf("backup %s integrity_check = %q", path, result)
	}
}

func schemaNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT type || ':' || name FROM sqlite_master ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func dumpSchema(t *testing.T, db *sql.DB) string {
	t.Helper()
	got, err := DumpSchema(context.Background(), db)
	if err != nil {
		t.Fatalf("DumpSchema: %v", err)
	}
	return got
}

func mustMigrationText(t *testing.T, kind Kind, version int) string {
	t.Helper()
	ms, err := migrationsFor(kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Version == version {
			return m.SQL
		}
	}
	t.Fatalf("migration %d for %s not found", version, kind)
	return ""
}

func stringsOf[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

var quotedRe = regexp.MustCompile(`'([^']*)'`)

func checkLiterals(t *testing.T, sqlText, table, column string) []string {
	t.Helper()
	blockRe := regexp.MustCompile(`(?s)CREATE TABLE\s+` + table + `\s*\((.*?)\n\);`)
	block := blockRe.FindStringSubmatch(sqlText)
	if block == nil {
		t.Fatalf("table %s not found in migration", table)
	}
	checkRe := regexp.MustCompile(`(?s)CHECK\s*\(\s*` + column + `\s+IN\s*\((.*?)\)\s*\)`)
	check := checkRe.FindStringSubmatch(block[1])
	if check == nil {
		t.Fatalf("CHECK (%s IN ...) not found in table %s", column, table)
	}
	var out []string
	for _, m := range quotedRe.FindAllStringSubmatch(check[1], -1) {
		out = append(out, m[1])
	}
	return out
}
