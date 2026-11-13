package kanban

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"sort"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/projects"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
	kanbanmigrations "researchos/internal/kanban/migrations"
)

// testEnv is one project database plus a bound Module on the shared fixed
// clock. Everything is real: SPEC-01 migrations, events writer, trash and
// relations services — the suite below reads activities and v_trash back
// through the foundation, exactly as SPEC-09/11 would.
type testEnv struct {
	t         *testing.T
	f         *foundationtest.Fixture
	handle    *workspace.ProjectHandle
	projectID string
	m         *Module
	ctx       context.Context
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	f, err := foundationtest.New(t.TempDir(), foundationtest.Options{
		Migrations: kanbanmigrations.MustAll(),
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	ctx := context.Background()
	projectID, handle, err := f.NewProject(ctx, "动力学研究", "", "")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	return &testEnv{
		t:         t,
		f:         f,
		handle:    handle,
		projectID: projectID,
		m:         New(handle, DepsFromWorkspace(f.Workspace, handle)),
		ctx:       ctx,
	}
}

// reopen simulates an application restart: the workspace is closed and
// re-opened from disk; a fresh Module must observe identical stored state.
func (e *testEnv) reopen() *Module {
	e.t.Helper()
	root := e.f.Root
	ws := e.f.Workspace
	if err := ws.Close(); err != nil {
		e.t.Fatalf("close workspace: %v", err)
	}
	reopened, err := workspace.Open(e.ctx, root, workspace.Options{
		Clock:      e.f.Clock,
		SkipBackup: true,
		BackupRoot: e.f.BackupRoot,
		Config:     e.f.Config,
		Confirm:    e.f.Confirm,
		Migrations: kanbanmigrations.MustAll(),
	})
	if err != nil {
		e.t.Fatalf("reopen workspace: %v", err)
	}
	e.f.Workspace = reopened
	handle, err := reopened.Project(e.ctx, e.projectID)
	if err != nil {
		e.t.Fatalf("reopen project: %v", err)
	}
	e.handle = handle
	e.m = New(handle, DepsFromWorkspace(reopened, handle))
	return e.m
}

func (e *testEnv) advance(d time.Duration) { e.f.Clock.Advance(d) }

// create makes one live task through the service, failing the test on
// error.
func (e *testEnv) create(title, column string) Task {
	e.t.Helper()
	task, err := e.m.Tasks().Create(e.ctx, schema.ActorUser, CreateInput{Title: title, Column: column})
	if err != nil {
		e.t.Fatalf("create %q: %v", title, err)
	}
	return task
}

// query runs one raw SQL against the project database.
func (e *testEnv) queryRow(query string, args ...any) *sql.Row {
	return e.handle.DB().QueryRowContext(e.ctx, query, args...)
}

func (e *testEnv) exec(query string, args ...any) {
	if _, err := e.handle.DB().ExecContext(e.ctx, query, args...); err != nil {
		e.t.Fatalf("exec %q: %v", query, err)
	}
}

func (e *testEnv) scanString(query string, args ...any) string {
	var s string
	if err := e.queryRow(query, args...).Scan(&s); err != nil {
		e.t.Fatalf("scan %q: %v", query, err)
	}
	return s
}

func (e *testEnv) scanInt(query string, args ...any) int {
	var n int
	if err := e.queryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("scan %q: %v", query, err)
	}
	return n
}

// acts returns the project's activities in chronological order.
func (e *testEnv) acts(entityID string, types ...schema.EventType) []events.Activity {
	q := events.Query{Limit: 500}
	if entityID != "" {
		q.EntityID = entityID
	}
	if len(types) > 0 {
		q.EventTypes = types
	}
	list, err := e.handle.Reader().List(e.ctx, q)
	if err != nil {
		e.t.Fatalf("activities: %v", err)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.Before(list[j].CreatedAt)
		}
		return list[i].ID < list[j].ID
	})
	return list
}

func (e *testEnv) actCount() int {
	n, err := e.handle.Reader().Count(e.ctx, events.Query{})
	if err != nil {
		e.t.Fatalf("activity count: %v", err)
	}
	return n
}

// lastAct returns the newest activity of the given types (any entity).
func (e *testEnv) lastAct(types ...schema.EventType) events.Activity {
	list := e.acts("", types...)
	if len(list) == 0 {
		e.t.Fatalf("no activity of %v", types)
	}
	return list[len(list)-1]
}

// objectPayload fetches one key of the payload old/new of an activity.
func objectPayload(t *testing.T, a events.Activity, wantOld bool, key string) any {
	t.Helper()
	obj := a.Payload.New
	if wantOld {
		obj = a.Payload.Old
	}
	if obj == nil {
		t.Fatalf("activity %s has no %v object", a.Type, map[bool]string{true: "old", false: "new"}[wantOld])
	}
	v, ok := (*obj)[key]
	if !ok {
		t.Fatalf("activity %s object lacks key %q (has %v)", a.Type, key, maps.Keys(*obj))
	}
	return v
}

// ---------------------------------------------------------------------------
// raw research-entity fixtures (SPEC-04/06/07 tables, schema v1 rows only)
// ---------------------------------------------------------------------------

func (e *testEnv) insertNote(id, title string) {
	at := clock.Format(e.f.Clock.Now())
	path := fmt.Sprintf("projects/%s/notes/%s.md", e.projectID, id)
	e.exec(`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at)
	        VALUES (?, ?, ?, 'scratchpad', ?, ?)`, id, title, path, at, at)
}

func (e *testEnv) insertIdea(id, title, status string) string {
	if status == "" {
		status = "New"
	}
	at := clock.Format(e.f.Clock.Now())
	e.exec(`INSERT INTO ideas(id, title, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		id, title, status, at, at)
	return at
}

func (e *testEnv) insertExperiment(id, title string) string {
	at := clock.Format(e.f.Clock.Now())
	e.exec(`INSERT INTO experiments(id, title, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		id, title, at, at)
	return at
}

func (e *testEnv) insertRun(id, experimentID, status string) string {
	at := clock.Format(e.f.Clock.Now())
	e.exec(`INSERT INTO runs(id, experiment_id, status, created_at, updated_at)
	        VALUES (?, ?, ?, ?, ?)`, id, experimentID, status, at, at)
	return at
}

func (e *testEnv) insertWeeklyReport(id, start, end string) {
	at := clock.Format(e.f.Clock.Now())
	e.exec(`INSERT INTO weekly_reports(id, period_start, period_end, status, created_at, updated_at)
	        VALUES (?, ?, ?, 'draft', ?, ?)`, id, start, end, at, at)
}

// archiveProject flips the project to read-only through the foundation
// store (the same row SPEC-03 writes).
func (e *testEnv) archiveProject() {
	store := projects.New(e.f.Workspace.Global().DB(), e.f.Clock)
	err := e.f.Workspace.Global().InTx(e.ctx, func(tx *sql.Tx) error {
		return store.SetStatus(e.ctx, tx, e.projectID, schema.ProjectArchived, schema.ActorUser)
	})
	if err != nil {
		e.t.Fatalf("archive project: %v", err)
	}
}

// columnNamesOf reads the board order of one column.
func (e *testEnv) columnNamesOf(column string) []string {
	rows, err := e.handle.DB().QueryContext(e.ctx,
		`SELECT id FROM tasks WHERE "column" = ? AND deleted_at IS NULL
		 AND archived_at IS NULL AND parent_task_id IS NULL ORDER BY sort_order, id`, column)
	if err != nil {
		e.t.Fatalf("order query: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			e.t.Fatalf("order scan: %v", err)
		}
		out = append(out, id)
	}
	return out
}

// uuidFixture mints a fresh valid UUIDv7 for raw seed rows.
func (e *testEnv) uuidFixture() string { return ids.MustNew(e.f.Clock) }

// taskStringColumn reads tasks."column" raw.
func (e *testEnv) taskStringColumn(id string) string {
	return e.scanString(`SELECT "column" FROM tasks WHERE id = ?`, id)
}
