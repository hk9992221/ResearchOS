package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/ports"
)

// testTime is the fixed instant every fixture starts at.
var testTime = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// newFixture builds a temp workspace and a Service bound to it.
func newFixture(t *testing.T, mutate func(*Options)) (*Service, *foundationtest.Fixture, *clock.Fixed) {
	t.Helper()
	fixed := &clock.Fixed{T: testTime}
	fx, err := foundationtest.New(t.TempDir(), foundationtest.Options{Clock: fixed})
	if err != nil {
		t.Fatalf("foundationtest.New: %v", err)
	}
	opts := Options{Workspace: fx.Workspace, Clock: fixed}
	if mutate != nil {
		mutate(&opts)
	}
	s := New(opts)
	t.Cleanup(func() {
		_ = s.Close(context.Background(), "")
		_ = fx.Close()
	})
	return s, fx, fixed
}

// mustCreate creates one project and fails the test on error.
func mustCreate(t *testing.T, s *Service, in CreateInput) *Project {
	t.Helper()
	p, err := s.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("Create(%+v): %v", in, err)
	}
	return p
}

// globalCount runs a counting query against global.db.
func globalCount(t *testing.T, fx *foundationtest.Fixture, query string, args ...any) int {
	t.Helper()
	var n int
	if err := fx.Workspace.Global().DB().QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// globalString runs a single-text query against global.db.
func globalString(t *testing.T, fx *foundationtest.Fixture, query string, args ...any) string {
	t.Helper()
	var v sql.NullString
	if err := fx.Workspace.Global().DB().QueryRowContext(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return v.String
}

// storedEvent is one activity row with its payload partly decoded.
type storedEvent struct {
	Type       string
	EntityType string
	EntityID   string
	Actor      string
	Payload    eventPayload
}

type eventPayload struct {
	EntityType string         `json:"entity_type"`
	EntityID   string         `json:"entity_id"`
	Old        map[string]any `json:"old"`
	New        map[string]any `json:"new"`
	Summary    string         `json:"summary"`
	Actor      string         `json:"actor"`
}

// projectEvents reads a project's activities oldest first.
func projectEvents(t *testing.T, fx *foundationtest.Fixture, projectID string) []storedEvent {
	t.Helper()
	h, err := fx.Workspace.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("workspace.Project(%s): %v", projectID, err)
	}
	rows, err := h.DB().QueryContext(context.Background(),
		`SELECT event_type, entity_type, entity_id, actor, payload FROM activities ORDER BY rowid`)
	if err != nil {
		t.Fatalf("query activities: %v", err)
	}
	defer rows.Close()
	var out []storedEvent
	for rows.Next() {
		var e storedEvent
		var payload string
		if err := rows.Scan(&e.Type, &e.EntityType, &e.EntityID, &e.Actor, &payload); err != nil {
			t.Fatalf("scan activity: %v", err)
		}
		if err := json.Unmarshal([]byte(payload), &e.Payload); err != nil {
			t.Fatalf("decode payload %q: %v", payload, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("activities rows: %v", err)
	}
	return out
}

// eventsOfType filters stored events by event type.
func eventsOfType(in []storedEvent, typ string) []storedEvent {
	var out []storedEvent
	for _, e := range in {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestCreatePipelineStructure(t *testing.T) {
	s, fx, _ := newFixture(t, nil)
	ctx := context.Background()

	p, err := s.Create(ctx, CreateInput{Name: "  Alpha  ", Summary: "sum", Goals: "goal"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.Name != "Alpha" {
		t.Fatalf("Name = %q, want trimmed %q", p.Name, "Alpha")
	}
	if p.Status != StatusActive || p.ArchivedAt != nil {
		t.Fatalf("Status/ArchivedAt = %v/%v", p.Status, p.ArchivedAt)
	}
	if !ids.Valid(p.ID) {
		t.Fatalf("ID %q is not UUIDv7", p.ID)
	}

	// Directory name is the id, never the name; layout is exactly §1.1. SQLite
	// sidecar files (-wal/-shm/-journal) belong to the open database, not to
	// the layout contract, so they are ignored.
	projectDir := filepath.Join(fx.Root, "projects", p.ID)
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		t.Fatalf("read project dir: %v", err)
	}
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		switch e.Name() {
		case "project.db-wal", "project.db-shm", "project.db-journal":
			continue
		}
		got = append(got, e.Name())
	}
	want := map[string]bool{"project.db": true, "notes": true, "attachments": true}
	if len(got) != len(want) {
		t.Fatalf("project dir entries = %v, want exactly %v", got, want)
	}
	for _, name := range got {
		if !want[name] {
			t.Fatalf("unexpected project dir entry %q", name)
		}
	}
	for _, name := range []string{"notes", "attachments"} {
		info, err := os.Stat(filepath.Join(projectDir, name))
		if err != nil || !info.IsDir() {
			t.Fatalf("%s: %v (isDir=%v)", name, err, err == nil && info.IsDir())
		}
	}
	all, err := os.ReadDir(filepath.Join(fx.Root, "projects"))
	if err != nil {
		t.Fatalf("read projects dir: %v", err)
	}
	if len(all) != 1 || all[0].Name() != p.ID {
		t.Fatalf("projects/ = %v, want only %s", all, p.ID)
	}

	// Global registration row.
	if n := globalCount(t, fx, `SELECT count(*) FROM projects WHERE id = ?`, p.ID); n != 1 {
		t.Fatalf("global rows = %d, want 1", n)
	}
	if got := globalString(t, fx, `SELECT name FROM projects WHERE id = ?`, p.ID); got != "Alpha" {
		t.Fatalf("stored name = %q", got)
	}
	if got := globalString(t, fx, `SELECT summary FROM projects WHERE id = ?`, p.ID); got != "sum" {
		t.Fatalf("stored summary = %q", got)
	}

	// Activity: one entity.created, actor=user, no old/new.
	evs := projectEvents(t, fx, p.ID)
	if len(evs) != 1 {
		t.Fatalf("activities = %+v, want 1", evs)
	}
	ev := evs[0]
	if ev.Type != "entity.created" || ev.EntityType != "project" || ev.EntityID != p.ID || ev.Actor != "user" {
		t.Fatalf("created event = %+v", ev)
	}
	if ev.Payload.Old != nil || ev.Payload.New != nil {
		t.Fatalf("created event old/new = %v/%v, want nil", ev.Payload.Old, ev.Payload.New)
	}
	if ev.Payload.Summary == "" || ev.Payload.Actor != "user" {
		t.Fatalf("created payload = %+v", ev.Payload)
	}

	// Session and browse state.
	cur := s.Current()
	if cur == nil || cur.Project.ID != p.ID || !cur.Writable {
		t.Fatalf("Current = %+v, want open writable session for %s", cur, p.ID)
	}
	if got := globalString(t, fx, `SELECT value FROM settings WHERE key = 'app.last_project_id'`); got != p.ID {
		t.Fatalf("app.last_project_id = %q, want %q", got, p.ID)
	}
}

func TestCreateRollback(t *testing.T) {
	ctx := context.Background()

	t.Run("project data failure", func(t *testing.T) {
		s, fx, _ := newFixture(t, func(o *Options) {
			o.CreateProjectData = func(context.Context, string) error {
				return errors.New("mkdir boom")
			}
		})
		_, err := s.Create(ctx, CreateInput{Name: "Alpha"})
		if err == nil || !strings.Contains(err.Error(), "mkdir boom") {
			t.Fatalf("Create error = %v, want mkdir boom", err)
		}
		if n := globalCount(t, fx, `SELECT count(*) FROM projects`); n != 0 {
			t.Fatalf("global rows = %d, want 0", n)
		}
		entries, rerr := os.ReadDir(filepath.Join(fx.Root, "projects"))
		if rerr != nil {
			t.Fatalf("read projects: %v", rerr)
		}
		if len(entries) != 0 {
			t.Fatalf("projects/ = %v, want empty", entries)
		}
	})

	t.Run("global insert failure", func(t *testing.T) {
		s, fx, _ := newFixture(t, func(o *Options) {
			o.InsertProject = func(context.Context, *sql.Tx, string, string, string, string) error {
				return errors.New("insert boom")
			}
		})
		_, err := s.Create(ctx, CreateInput{Name: "Alpha"})
		if err == nil || !strings.Contains(err.Error(), "insert boom") {
			t.Fatalf("Create error = %v, want insert boom", err)
		}
		if n := globalCount(t, fx, `SELECT count(*) FROM projects`); n != 0 {
			t.Fatalf("global rows = %d, want 0", n)
		}
		entries, rerr := os.ReadDir(filepath.Join(fx.Root, "projects"))
		if rerr != nil {
			t.Fatalf("read projects: %v", rerr)
		}
		if len(entries) != 0 {
			t.Fatalf("projects/ = %v, want empty (directory must be rolled back)", entries)
		}
	})

	t.Run("event failure keeps the project", func(t *testing.T) {
		s, fx, _ := newFixture(t, func(o *Options) {
			ws := o.Workspace
			o.CreateProjectData = func(ctx context.Context, id string) error {
				if err := ws.CreateProjectData(ctx, id); err != nil {
					return err
				}
				// Close the pool behind the workspace cache: the event write
				// then fails while the directory and row are already created.
				h, err := ws.Project(ctx, id)
				if err != nil {
					return err
				}
				return h.Close()
			}
		})
		p, err := s.Create(ctx, CreateInput{Name: "Alpha"})
		if !errors.Is(err, ErrEventMissing) {
			t.Fatalf("Create error = %v, want ErrEventMissing", err)
		}
		if p == nil || p.Name != "Alpha" {
			t.Fatalf("project = %+v, want the created project", p)
		}
		if n := globalCount(t, fx, `SELECT count(*) FROM projects WHERE id = ?`, p.ID); n != 1 {
			t.Fatalf("global rows = %d, want the project kept", n)
		}
		if _, serr := os.Stat(filepath.Join(fx.Root, "projects", p.ID, "project.db")); serr != nil {
			t.Fatalf("project data removed on event failure: %v", serr)
		}
	})
}

func TestUpdateEvents(t *testing.T) {
	s, fx, fixed := newFixture(t, nil)
	ctx := context.Background()
	p := mustCreate(t, s, CreateInput{Name: "Alpha", Summary: "first", Goals: "goals"})
	projectDir := filepath.Join(fx.Root, "projects", p.ID)

	// Rename: only "name" moves, the directory path is unchanged.
	newName := "Beta"
	up, err := s.Update(ctx, UpdateInput{ID: p.ID, Name: &newName})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if up.Name != "Beta" || up.Summary != "first" || up.Goals != "goals" {
		t.Fatalf("updated = %+v", up)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "project.db")); err != nil {
		t.Fatalf("project.db moved after rename: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(fx.Root, "projects"))
	if err != nil || len(entries) != 1 || entries[0].Name() != p.ID {
		t.Fatalf("projects/ = %v (err %v), rename must not affect the directory", entries, err)
	}

	updates := eventsOfType(projectEvents(t, fx, p.ID), "entity.updated")
	if len(updates) != 1 {
		t.Fatalf("entity.updated count = %d, want 1", len(updates))
	}
	ev := updates[0]
	if ev.Actor != "user" || ev.Payload.Actor != "user" {
		t.Fatalf("actor = %q/%q, want user", ev.Actor, ev.Payload.Actor)
	}
	if len(ev.Payload.Old) != 1 || len(ev.Payload.New) != 1 {
		t.Fatalf("old/new = %v/%v, want only name", ev.Payload.Old, ev.Payload.New)
	}
	if ev.Payload.Old["name"] != "Alpha" || ev.Payload.New["name"] != "Beta" {
		t.Fatalf("old/new = %v/%v", ev.Payload.Old["name"], ev.Payload.New["name"])
	}
	if got := globalString(t, fx, `SELECT name FROM projects WHERE id = ?`, p.ID); got != "Beta" {
		t.Fatalf("stored name = %q", got)
	}

	// No-op update: no write, no event.
	same := "Beta"
	if _, err := s.Update(ctx, UpdateInput{ID: p.ID, Name: &same}); err != nil {
		t.Fatalf("no-op Update: %v", err)
	}
	if n := len(eventsOfType(projectEvents(t, fx, p.ID), "entity.updated")); n != 1 {
		t.Fatalf("entity.updated after no-op = %d, want 1", n)
	}

	// Multiple changed fields with an MCP actor.
	newSummary, newFocus := "second", "focus body"
	up, err = s.Update(ctx, UpdateInput{
		ID: p.ID, Summary: &newSummary, Focus: &newFocus, Actor: ports.ActorMCP,
	})
	if err != nil {
		t.Fatalf("Update multi: %v", err)
	}
	if up.Summary != "second" || up.FocusMD != "focus body" {
		t.Fatalf("updated = %+v", up)
	}
	updates = eventsOfType(projectEvents(t, fx, p.ID), "entity.updated")
	if len(updates) != 2 {
		t.Fatalf("entity.updated count = %d, want 2", len(updates))
	}
	ev = updates[len(updates)-1]
	if ev.Actor != "mcp" {
		t.Fatalf("actor = %q, want mcp", ev.Actor)
	}
	if len(ev.Payload.Old) != 2 || len(ev.Payload.New) != 2 {
		t.Fatalf("old/new = %v/%v, want exactly summary+focus_md", ev.Payload.Old, ev.Payload.New)
	}
	if ev.Payload.Old["summary"] != "first" || ev.Payload.New["summary"] != "second" {
		t.Fatalf("summary old/new = %v/%v", ev.Payload.Old["summary"], ev.Payload.New["summary"])
	}
	if _, ok := ev.Payload.New["focus_md"]; !ok {
		t.Fatalf("new = %v, want focus_md", ev.Payload.New)
	}
	if _, ok := ev.Payload.New["name"]; ok {
		t.Fatalf("new = %v, must not contain unchanged name", ev.Payload.New)
	}

	// Field limits.
	blank, longName, control := "   ", strings.Repeat("a", 201), "bad\x01name"
	for _, tc := range []struct {
		name string
		in   UpdateInput
		want error
	}{
		{"blank name", UpdateInput{ID: p.ID, Name: &blank}, ErrNameInvalid},
		{"long name", UpdateInput{ID: p.ID, Name: &longName}, ErrNameInvalid},
		{"control char", UpdateInput{ID: p.ID, Name: &control}, ErrNameInvalid},
		{"summary limit", UpdateInput{ID: p.ID, Summary: new(strings.Repeat("s", maxSummaryRunes+1))}, ErrInvalidInput},
		{"goals limit", UpdateInput{ID: p.ID, Goals: new(strings.Repeat("g", maxGoalsRunes+1))}, ErrInvalidInput},
		{"focus limit", UpdateInput{ID: p.ID, Focus: new(strings.Repeat("f", maxFocusRunes+1))}, ErrInvalidInput},
	} {
		if _, err := s.Update(ctx, tc.in); !errors.Is(err, tc.want) {
			t.Fatalf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	// Boundary values are accepted.
	maxName := strings.Repeat("n", maxNameRunes)
	if _, err := s.Update(ctx, UpdateInput{ID: p.ID, Name: &maxName}); err != nil {
		t.Fatalf("200-rune name rejected: %v", err)
	}

	// Archived projects are read-only.
	if err := s.Archive(ctx, p.ID, ports.ActorUser); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	_, err = s.Update(ctx, UpdateInput{ID: p.ID, Name: &newName})
	if !errors.Is(err, ErrArchived) {
		t.Fatalf("archived Update error = %v, want ErrArchived", err)
	}
	var archived *ArchivedError
	if !errors.As(err, &archived) || archived.ProjectID != p.ID {
		t.Fatalf("error %v is not *ArchivedError{%s}", err, p.ID)
	}

	// The clock moved nowhere: ordering assertions above do not depend on it.
	if fixed.Now().Before(testTime) {
		t.Fatalf("clock went backwards")
	}
}

func TestGetNotFound(t *testing.T) {
	s, _, fixed := newFixture(t, nil)
	ctx := context.Background()
	for _, id := range []string{ids.MustNew(fixed), "", "definitely-not-a-uuid"} {
		if _, err := s.Get(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get(%q) error = %v, want ErrNotFound", id, err)
		}
	}
}

func TestListOrderingFiltering(t *testing.T) {
	s, _, fixed := newFixture(t, nil)
	ctx := context.Background()

	a := mustCreate(t, s, CreateInput{Name: "Alpha", Summary: "first project"})
	fixed.Advance(time.Minute)
	b := mustCreate(t, s, CreateInput{Name: "beta", Summary: "Anomaly detection"})
	fixed.Advance(time.Minute)
	c := mustCreate(t, s, CreateInput{Name: "Gamma", Summary: "third"})

	got, err := s.List(ctx, ListQuery{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{c.ID, b.ID, a.ID}
	if idsOf(got) != strings.Join(want, ",") {
		t.Fatalf("List order = %v, want %v", idsOf(got), want)
	}

	// Case-insensitive substring over name and summary.
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"ALPH", []string{a.ID}},
		{"anomal", []string{b.ID}},
		{"A", []string{c.ID, b.ID, a.ID}}, // "Gamma", "beta", "Alpha" all contain "a"
	} {
		got, err := s.List(ctx, ListQuery{Query: tc.q})
		if err != nil {
			t.Fatalf("List(%q): %v", tc.q, err)
		}
		if idsOf(got) != strings.Join(tc.want, ",") {
			t.Fatalf("List(%q) = %v, want %v", tc.q, idsOf(got), tc.want)
		}
	}

	// Archived projects are hidden by default and ordered last on request.
	if err := s.Archive(ctx, b.ID, ports.ActorUser); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	got, err = s.List(ctx, ListQuery{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if idsOf(got) != strings.Join([]string{c.ID, a.ID}, ",") {
		t.Fatalf("active List = %v", idsOf(got))
	}
	got, err = s.List(ctx, ListQuery{IncludeArchived: true})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if idsOf(got) != strings.Join([]string{c.ID, a.ID, b.ID}, ",") {
		t.Fatalf("List(IncludeArchived) = %v, want ordered active then archived", idsOf(got))
	}

	// Ties on created_at fall back to id DESC, deterministically.
	tieA := mustCreate(t, s, CreateInput{Name: "Tie one"})
	tieB := mustCreate(t, s, CreateInput{Name: "Tie two"})
	got, err = s.List(ctx, ListQuery{Query: "Tie"})
	if err != nil {
		t.Fatalf("List tie: %v", err)
	}
	higher, lower := tieA.ID, tieB.ID
	if higher < lower {
		higher, lower = lower, higher
	}
	if idsOf(got) != higher+","+lower {
		t.Fatalf("tie order = %v, want id DESC %s,%s", idsOf(got), higher, lower)
	}
}

func idsOf(in []Project) string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		out = append(out, p.ID)
	}
	return strings.Join(out, ",")
}
