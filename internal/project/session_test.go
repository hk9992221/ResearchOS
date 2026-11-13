package project

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"researchos/internal/foundation/projects"
	"researchos/internal/ports"
)

// recordingListener records lifecycle callbacks in order.
type recordingListener struct {
	mu     sync.Mutex
	events []string
	failOn map[string]bool
}

func (r *recordingListener) record(s string) {
	r.mu.Lock()
	r.events = append(r.events, s)
	r.mu.Unlock()
}

func (r *recordingListener) OnProjectOpen(_ context.Context, s *Session) error {
	if r.failOn[s.Project.ID] {
		r.record("fail:" + s.Project.ID)
		return errors.New("listener veto")
	}
	r.record("open:" + s.Project.ID)
	return nil
}

func (r *recordingListener) OnProjectClose(_ context.Context, projectID string) error {
	r.record("close:" + projectID)
	return nil
}

func (r *recordingListener) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

func (r *recordingListener) reset() {
	r.mu.Lock()
	r.events = nil
	r.mu.Unlock()
}

func TestOpenSingleSessionAndLastProjectID(t *testing.T) {
	s, fx, fixed := newFixture(t, nil)
	ctx := context.Background()
	rec := &recordingListener{}
	s.RegisterListener(rec)

	a := mustCreate(t, s, CreateInput{Name: "Alpha"})
	b := mustCreate(t, s, CreateInput{Name: "Beta"})

	// Creating B closed A first; the single session is B.
	if got, want := rec.snapshot(), []string{"open:" + a.ID, "close:" + a.ID, "open:" + b.ID}; !slices.Equal(got, want) {
		t.Fatalf("listener order = %v, want %v", got, want)
	}
	if cur := s.Current(); cur == nil || cur.Project.ID != b.ID {
		t.Fatalf("Current = %+v, want %s", cur, b.ID)
	}

	// Switching to A closes B first.
	rec.reset()
	if _, err := s.Open(ctx, a.ID); err != nil {
		t.Fatalf("Open(A): %v", err)
	}
	if got, want := rec.snapshot(), []string{"close:" + b.ID, "open:" + a.ID}; !slices.Equal(got, want) {
		t.Fatalf("listener order = %v, want %v", got, want)
	}
	if got := globalString(t, fx, `SELECT value FROM settings WHERE key = 'app.last_project_id'`); got != a.ID {
		t.Fatalf("app.last_project_id = %q, want %q", got, a.ID)
	}
	if cur := s.Current(); cur == nil || cur.Project.ID != a.ID || !cur.Writable {
		t.Fatalf("Current = %+v", cur)
	}

	// Close is idempotent and empties the session.
	if err := s.Close(ctx, a.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(ctx, a.ID); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if cur := s.Current(); cur != nil {
		t.Fatalf("Current = %+v after Close, want nil", cur)
	}

	// A failing listener aborts the open, closes the successful listeners and
	// leaves no session behind.
	store := projects.New(fx.Workspace.Global().DB(), fixed)
	var cID string
	if err := fx.Workspace.Global().InTx(ctx, func(tx *sql.Tx) error {
		var err error
		cID, err = store.Create(ctx, tx, "Vetoed", "", "")
		return err
	}); err != nil {
		t.Fatalf("create vetoed project: %v", err)
	}
	if err := fx.Workspace.CreateProjectData(ctx, cID); err != nil {
		t.Fatalf("CreateProjectData: %v", err)
	}

	veto := &recordingListener{failOn: map[string]bool{cID: true}}
	s.RegisterListener(veto)
	if _, err := s.Open(ctx, a.ID); err != nil {
		t.Fatalf("Open(A): %v", err)
	}
	rec.reset()
	veto.reset()
	if _, err := s.Open(ctx, cID); err == nil {
		t.Fatal("Open with a vetoing listener succeeded")
	} else if !strings.Contains(err.Error(), "listener veto") {
		t.Fatalf("Open error = %v, want the listener error", err)
	}

	// Each listener records its own callbacks; the vetoing one sees the close
	// of the previous session, then fails, and the recorder sees the rollback
	// close of the aborted project.
	if got, want := rec.snapshot(), []string{"close:" + a.ID, "open:" + cID, "close:" + cID}; !slices.Equal(got, want) {
		t.Fatalf("recorder order after failed open = %v, want %v", got, want)
	}
	if got, want := veto.snapshot(), []string{"close:" + a.ID, "fail:" + cID}; !slices.Equal(got, want) {
		t.Fatalf("vetoing listener order = %v, want %v", got, want)
	}
	if cur := s.Current(); cur != nil {
		t.Fatalf("Current = %+v after failed open, want nil", cur)
	}
	if got := globalString(t, fx, `SELECT value FROM settings WHERE key = 'app.last_project_id'`); got != a.ID {
		t.Fatalf("app.last_project_id = %q, want unchanged %q", got, a.ID)
	}
}

func TestArchiveUnarchive(t *testing.T) {
	s, fx, _ := newFixture(t, nil)
	ctx := context.Background()
	p := mustCreate(t, s, CreateInput{Name: "Alpha"})

	if err := s.Archive(ctx, p.ID, ports.ActorUser); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if got := globalString(t, fx, `SELECT status FROM projects WHERE id = ?`, p.ID); got != "archived" {
		t.Fatalf("status = %q, want archived", got)
	}
	if got := globalString(t, fx, `SELECT archived_at FROM projects WHERE id = ?`, p.ID); got == "" {
		t.Fatal("archived_at is NULL after Archive")
	}
	changes := eventsOfType(projectEvents(t, fx, p.ID), "status.changed")
	if len(changes) != 1 {
		t.Fatalf("status.changed count = %d, want 1", len(changes))
	}
	ev := changes[0]
	if ev.Actor != "user" || ev.Payload.Actor != "user" {
		t.Fatalf("actor = %q/%q, want user", ev.Actor, ev.Payload.Actor)
	}
	if ev.Payload.Old["status"] != "active" || ev.Payload.New["status"] != "archived" {
		t.Fatalf("old/new = %v/%v", ev.Payload.Old, ev.Payload.New)
	}
	if cur := s.Current(); cur == nil || cur.Writable {
		t.Fatalf("Current = %+v, want read-only session", cur)
	}
	if cur := s.Current(); cur == nil || cur.Project.Status != StatusArchived {
		t.Fatalf("session project status = %+v", cur)
	}

	// Archived projects stay readable.
	if _, err := s.Get(ctx, p.ID); err != nil {
		t.Fatalf("Get archived: %v", err)
	}
	listed, err := s.List(ctx, ListQuery{IncludeArchived: true})
	if err != nil || len(listed) != 1 || listed[0].Status != StatusArchived {
		t.Fatalf("List(IncludeArchived) = %+v (err %v)", listed, err)
	}

	// Idempotent archive: no second event.
	if err := s.Archive(ctx, p.ID, ports.ActorMCP); err != nil {
		t.Fatalf("second Archive: %v", err)
	}
	if n := len(eventsOfType(projectEvents(t, fx, p.ID), "status.changed")); n != 1 {
		t.Fatalf("status.changed after no-op archive = %d, want 1", n)
	}

	// Only user and mcp may change the state.
	for _, actor := range []ports.Actor{ports.ActorAI, ports.Actor("robot"), ""} {
		if err := s.Archive(ctx, p.ID, actor); !errors.Is(err, ErrInvalidActor) {
			t.Fatalf("Archive(%q) error = %v, want ErrInvalidActor", actor, err)
		}
		if err := s.Unarchive(ctx, p.ID, actor); !errors.Is(err, ErrInvalidActor) {
			t.Fatalf("Unarchive(%q) error = %v, want ErrInvalidActor", actor, err)
		}
	}

	// Unarchive restores writability and clears archived_at.
	if err := s.Unarchive(ctx, p.ID, ports.ActorMCP); err != nil {
		t.Fatalf("Unarchive: %v", err)
	}
	if got := globalString(t, fx, `SELECT status FROM projects WHERE id = ?`, p.ID); got != "active" {
		t.Fatalf("status = %q, want active", got)
	}
	if got := globalString(t, fx, `SELECT archived_at FROM projects WHERE id = ?`, p.ID); got != "" {
		t.Fatalf("archived_at = %q, want NULL", got)
	}
	changes = eventsOfType(projectEvents(t, fx, p.ID), "status.changed")
	if len(changes) != 2 {
		t.Fatalf("status.changed count = %d, want 2", len(changes))
	}
	ev = changes[1]
	if ev.Actor != "mcp" {
		t.Fatalf("unarchive actor = %q, want mcp", ev.Actor)
	}
	if ev.Payload.Old["status"] != "archived" || ev.Payload.New["status"] != "active" {
		t.Fatalf("old/new = %v/%v", ev.Payload.Old, ev.Payload.New)
	}
	if cur := s.Current(); cur == nil || !cur.Writable {
		t.Fatalf("Current = %+v, want writable", cur)
	}

	// Edits work again after unarchive.
	name := "Alpha 2"
	if _, err := s.Update(ctx, UpdateInput{ID: p.ID, Name: &name}); err != nil {
		t.Fatalf("Update after unarchive: %v", err)
	}

	// Idempotent unarchive: still exactly two status events.
	if err := s.Unarchive(ctx, p.ID, ports.ActorUser); err != nil {
		t.Fatalf("second Unarchive: %v", err)
	}
	if n := len(eventsOfType(projectEvents(t, fx, p.ID), "status.changed")); n != 2 {
		t.Fatalf("status.changed after no-op unarchive = %d, want 2", n)
	}
}

func TestArchiveKeepsHandleForReadOnly(t *testing.T) {
	s, fx, _ := newFixture(t, nil)
	ctx := context.Background()
	p := mustCreate(t, s, CreateInput{Name: "Alpha"})
	if err := s.Archive(ctx, p.ID, ports.ActorUser); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	// The session keeps its project handle; Stats reads an archived project.
	st, err := s.Stats(ctx, p.ID)
	if err != nil {
		t.Fatalf("Stats on archived: %v", err)
	}
	if st.NoteCount != 0 || st.LastActivityAt == nil {
		t.Fatalf("Stats = %+v", st)
	}
	if _, err := fx.Workspace.Project(ctx, p.ID); err != nil {
		t.Fatalf("project handle was closed by Archive: %v", err)
	}
}
