package project

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/ports"
)

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func countOn(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// seedStats fills the project database with rows the way SPEC-05/07/08 would.
func seedStats(t *testing.T, fx *foundationtest.Fixture, projectID string, fixed *clock.Fixed) time.Time {
	t.Helper()
	h, err := fx.Workspace.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("workspace.Project: %v", err)
	}
	db := h.DB()
	now := clock.Format(fixed.Now())
	deleted := now

	// notes: 2 alive + 1 in the trash.
	for i := range 3 {
		noteID := ids.MustNew(fixed)
		var del any
		if i == 0 {
			del = deleted
		}
		mustExec(t, db,
			`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at, deleted_at)
			 VALUES (?, ?, ?, 'scratchpad', ?, ?, ?)`,
			noteID, fmt.Sprintf("note %d", i),
			fmt.Sprintf("projects/%s/notes/%s.md", projectID, noteID), now, now, del)
	}

	// ideas: Researching + Validating are active; one Rejected is not; one
	// Researching is soft-deleted.
	for _, tc := range []struct {
		status  string
		deleted bool
	}{
		{"Researching", false},
		{"Validating", false},
		{"Rejected", false},
		{"Researching", true},
	} {
		var del any
		if tc.deleted {
			del = deleted
		}
		mustExec(t, db,
			`INSERT INTO ideas(id, title, status, created_at, updated_at, deleted_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			ids.MustNew(fixed), tc.status, tc.status, now, now, del)
	}

	// experiments: 2 alive + 1 in the trash.
	for i := range 3 {
		var del any
		if i == 2 {
			del = deleted
		}
		mustExec(t, db,
			`INSERT INTO experiments(id, title, created_at, updated_at, deleted_at)
			 VALUES (?, ?, ?, ?, ?)`,
			ids.MustNew(fixed), fmt.Sprintf("exp %d", i), now, now, del)
	}

	// tasks: 3 open (one blocked) + 1 trashed + 1 trashed and blocked.
	for _, tc := range []struct {
		blocked int
		deleted bool
	}{
		{0, false},
		{1, false},
		{0, false},
		{0, true},
		{1, true},
	} {
		var del any
		if tc.deleted {
			del = deleted
		}
		mustExec(t, db,
			`INSERT INTO tasks(id, title, "column", blocked, created_at, updated_at, deleted_at)
			 VALUES (?, 'task', 'todo', ?, ?, ?, ?)`,
			ids.MustNew(fixed), tc.blocked, now, now, del)
	}

	// One linked paper.
	mustExec(t, db,
		`INSERT INTO project_papers(project_id, paper_id, linked_at, linked_by) VALUES (?, ?, ?, 'user')`,
		projectID, ids.MustNew(fixed), now)

	// A later activity, so LastActivityAt is unambiguous.
	later := clock.Format(fixed.Now().Add(time.Hour))
	taskID := ids.MustNew(fixed)
	payload := fmt.Sprintf(`{"entity_type":"task","entity_id":%q,"summary":"seed","actor":"user"}`, taskID)
	mustExec(t, db,
		`INSERT INTO activities(id, event_type, entity_type, entity_id, payload, actor, merge_key, created_at)
		 VALUES (?, 'entity.created', 'task', ?, ?, 'user', NULL, ?)`,
		ids.MustNew(fixed), taskID, payload, later)
	return fixed.Now().Add(time.Hour)
}

func TestStatsMatchesSQL(t *testing.T) {
	s, fx, fixed := newFixture(t, nil)
	ctx := context.Background()
	p := mustCreate(t, s, CreateInput{Name: "Stats"})
	later := seedStats(t, fx, p.ID, fixed)

	h, err := fx.Workspace.Project(ctx, p.ID)
	if err != nil {
		t.Fatalf("workspace.Project: %v", err)
	}
	db := h.DB()
	want := Stats{
		NoteCount:       countOn(t, db, `SELECT count(*) FROM notes WHERE deleted_at IS NULL`),
		IdeaActive:      countOn(t, db, `SELECT count(*) FROM ideas WHERE status IN ('Researching','Validating') AND deleted_at IS NULL`),
		ExperimentCount: countOn(t, db, `SELECT count(*) FROM experiments WHERE deleted_at IS NULL`),
		TaskOpen:        countOn(t, db, `SELECT count(*) FROM tasks WHERE deleted_at IS NULL`),
		TaskBlocked:     countOn(t, db, `SELECT count(*) FROM tasks WHERE deleted_at IS NULL AND blocked = 1`),
		PaperCount:      countOn(t, db, `SELECT count(*) FROM project_papers`),
		TrashedCount:    countOn(t, db, `SELECT count(*) FROM v_trash`),
	}
	if want.NoteCount != 2 || want.IdeaActive != 2 || want.ExperimentCount != 2 ||
		want.TaskOpen != 3 || want.TaskBlocked != 1 || want.PaperCount != 1 || want.TrashedCount != 5 {
		t.Fatalf("seed is wrong: %+v", want)
	}

	got, err := s.Stats(ctx, p.ID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if got.NoteCount != want.NoteCount || got.IdeaActive != want.IdeaActive ||
		got.ExperimentCount != want.ExperimentCount || got.TaskOpen != want.TaskOpen ||
		got.TaskBlocked != want.TaskBlocked || got.PaperCount != want.PaperCount ||
		got.TrashedCount != want.TrashedCount {
		t.Fatalf("Stats = %+v, want %+v", got, want)
	}
	if got.DirBytes <= 0 {
		t.Fatalf("DirBytes = %d, want the project files", got.DirBytes)
	}
	if got.LastActivityAt == nil || !got.LastActivityAt.Equal(later) {
		t.Fatalf("LastActivityAt = %v, want %v", got.LastActivityAt, later)
	}

	// Archived projects stay readable.
	if err := s.Archive(ctx, p.ID, ports.ActorUser); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	archived, err := s.Stats(ctx, p.ID)
	if err != nil {
		t.Fatalf("Stats(archived): %v", err)
	}
	if archived.NoteCount != got.NoteCount || archived.TrashedCount != got.TrashedCount {
		t.Fatalf("Stats(archived) = %+v, want %+v", archived, got)
	}
	if _, err := s.Stats(ctx, ids.MustNew(fixed)); err == nil {
		t.Fatal("Stats(unknown) succeeded")
	}
}
