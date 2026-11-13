package overview

import (
	"context"
	"errors"
	"testing"
	"time"

	"researchos/internal/ports"
)

// isArchived is errors.Is against the read-only sentinel.
func isArchived(err error) bool { return errors.Is(err, ErrArchived) }

// fakeTasks records the SPEC-08 calls.
type fakeTasks struct {
	err   error
	calls []taskCall
}

type taskCall struct {
	projectID string
	taskID    string
	actor     ports.Actor
}

func (f *fakeTasks) CompleteTask(ctx context.Context, projectID, taskID string, actor ports.Actor) error {
	f.calls = append(f.calls, taskCall{projectID: projectID, taskID: taskID, actor: actor})
	return f.err
}

// fakeNotes records the SPEC-05 calls.
type fakeNotes struct {
	err    error
	calls  []noteCall
	result ports.EntityRef
}

type noteCall struct {
	projectID string
	title     string
	bodyMD    string
	actor     ports.Actor
}

func (f *fakeNotes) CreateScratchpad(ctx context.Context, projectID, title, bodyMD string, actor ports.Actor) (ports.EntityRef, error) {
	f.calls = append(f.calls, noteCall{projectID: projectID, title: title, bodyMD: bodyMD, actor: actor})
	if f.err != nil {
		return ports.EntityRef{}, f.err
	}
	if f.result != (ports.EntityRef{}) {
		return f.result, nil
	}
	return ports.EntityRef{Type: "note", ID: "note-1"}, nil
}

func TestQuickAddNoteUsesNoteCreator(t *testing.T) {
	notes := &fakeNotes{result: ports.EntityRef{Type: "note", ID: "n-9"}}
	tasks := &fakeTasks{}
	e := newEnv(t, func(o *Options) { o.Notes = notes; o.Tasks = tasks })
	e.note("已有笔记", "scratchpad", -time.Hour, false)
	e.load() // warm the cache so invalidation is observable

	ref, err := e.svc.QuickAddNote(context.Background(), e.pid, QuickNoteInput{
		Title:  "会议记录",
		BodyMD: "## 决定\n\n- 先跑基线",
	})
	if err != nil {
		t.Fatalf("QuickAddNote: %v", err)
	}
	if ref.ID != "n-9" || ref.Type != "note" {
		t.Fatalf("ref = %+v", ref)
	}
	if len(notes.calls) != 1 {
		t.Fatalf("calls = %+v", notes.calls)
	}
	call := notes.calls[0]
	if call.title != "会议记录" || call.bodyMD != "## 决定\n\n- 先跑基线" {
		t.Fatalf("call = %+v", call)
	}
	if call.actor != ports.ActorUser {
		t.Fatalf("actor = %q, want user (§5.6: quick actions run as the user)", call.actor)
	}
	if e.svc.previous(e.pid) != nil {
		t.Fatal("the snapshot must be invalidated after a quick write")
	}
	// The new note shows up on the next Load.
	if got := e.load(); len(got.RecentNotes) != 1 {
		t.Fatalf("RecentNotes = %+v", got.RecentNotes)
	}
}

func TestQuickAddNoteGeneratesLocalTitle(t *testing.T) {
	notes := &fakeNotes{}
	e := newEnv(t, func(o *Options) { o.Notes = notes })
	if _, err := e.svc.QuickAddNote(context.Background(), e.pid, QuickNoteInput{}); err != nil {
		t.Fatalf("QuickAddNote: %v", err)
	}
	// testTime is 2026-03-04 05:06:07 UTC.
	if got, want := notes.calls[0].title, "Quick note 2026-03-04 05:06"; got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
	if notes.calls[0].bodyMD != "" {
		t.Fatalf("body = %q, want the empty body to pass through", notes.calls[0].bodyMD)
	}

	// A blank-but-not-empty title is treated as empty.
	if _, err := e.svc.QuickAddNote(context.Background(), e.pid, QuickNoteInput{Title: "   "}); err != nil {
		t.Fatalf("QuickAddNote: %v", err)
	}
	if got, want := notes.calls[1].title, "Quick note 2026-03-04 05:06"; got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
}

func TestCompleteTaskUsesKanbanPort(t *testing.T) {
	tasks := &fakeTasks{}
	e := newEnv(t, func(o *Options) { o.Tasks = tasks })
	e.task("待完成", "In Progress", "high", nil, false, -time.Hour)
	e.load()

	if err := e.svc.CompleteTask(context.Background(), e.pid, "task-7"); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if len(tasks.calls) != 1 || tasks.calls[0].taskID != "task-7" || tasks.calls[0].projectID != e.pid {
		t.Fatalf("calls = %+v", tasks.calls)
	}
	if tasks.calls[0].actor != ports.ActorUser {
		t.Fatalf("actor = %q, want user", tasks.calls[0].actor)
	}
	if e.svc.previous(e.pid) != nil {
		t.Fatal("the snapshot must be invalidated after completing a task")
	}
}

func TestQuickActionPortErrorsLeaveTheCacheAlone(t *testing.T) {
	boom := errors.New("kanban: task is in a done column")
	e := newEnv(t, func(o *Options) { o.Tasks = &fakeTasks{err: boom}; o.Notes = &fakeNotes{err: boom} })
	snap := e.load()
	if err := e.svc.CompleteTask(context.Background(), e.pid, "t-1"); !errors.Is(err, boom) {
		t.Fatalf("CompleteTask = %v, want the port error", err)
	}
	if _, err := e.svc.QuickAddNote(context.Background(), e.pid, QuickNoteInput{}); !errors.Is(err, boom) {
		t.Fatalf("QuickAddNote = %v, want the port error", err)
	}
	// A failed write must not drop the snapshot: the data is still correct.
	if got := e.svc.previous(e.pid); got != snap {
		t.Fatal("the cache was invalidated by a write that never happened")
	}
}

func TestMissingPortsReportFeatureUnavailable(t *testing.T) {
	e := newEnv(t, nil) // no Tasks, no Notes, no PaperIndex
	if err := e.svc.CompleteTask(context.Background(), e.pid, "t-1"); !errors.Is(err, ErrFeatureUnavailable) {
		t.Fatalf("CompleteTask = %v, want ErrFeatureUnavailable", err)
	}
	if _, err := e.svc.QuickAddNote(context.Background(), e.pid, QuickNoteInput{}); !errors.Is(err, ErrFeatureUnavailable) {
		t.Fatalf("QuickAddNote = %v, want ErrFeatureUnavailable", err)
	}
	snap := e.load()
	if len(snap.Widgets) != len(widgetOrder) {
		t.Fatalf("Widgets = %d", len(snap.Widgets))
	}
	// Without the SPEC-04 port the project_papers fallback still answers, so the
	// card is an empty state, not a hidden module.
	if st := e.state(snap, WidgetLiterature); !st.Available || st.Degraded {
		t.Fatalf("Literature = %+v, want the visible empty fallback", st)
	}
	if len(snap.Literature) != 0 {
		t.Fatalf("Literature = %+v, want no invented rows", snap.Literature)
	}

	// With neither the port nor a project database there is no source left: the
	// card hides and Load still succeeds (§5.6, §8.1 PRJ-04).
	pid := registerWithoutData(t, e)
	broken, err := e.svc.Load(context.Background(), pid)
	if err != nil {
		t.Fatalf("Load = %v, want a snapshot whose Literature card is hidden", err)
	}
	st := e.state(broken, WidgetLiterature)
	if st.Available || st.Degraded || st.Reason != "module_not_registered" {
		t.Fatalf("Literature = %+v, want Available=false with module_not_registered", st)
	}
	if len(broken.Literature) != 0 {
		t.Fatalf("Literature = %+v, want nothing shown", broken.Literature)
	}
}

func TestCheckWritableOverridesArchiveLookup(t *testing.T) {
	guard := errors.New("project: p is archived (项目已归档，只读)")
	called := 0
	e := newEnv(t, func(o *Options) {
		o.Tasks = &fakeTasks{}
		o.Notes = &fakeNotes{}
		o.CheckWritable = func(ctx context.Context, projectID string) error {
			called++
			return guard
		}
	})
	if err := e.svc.CompleteTask(context.Background(), e.pid, "t-1"); !errors.Is(err, guard) {
		t.Fatalf("CompleteTask = %v, want the guard error unchanged", err)
	}
	if _, err := e.svc.QuickAddNote(context.Background(), e.pid, QuickNoteInput{}); !errors.Is(err, guard) {
		t.Fatalf("QuickAddNote = %v, want the guard error unchanged", err)
	}
	if called != 2 {
		t.Fatalf("guard calls = %d, want 2", called)
	}

	// An accepting guard lets the write through even though the row is archived:
	// the injected hook owns the decision.
	e.svc.checkWritable = func(context.Context, string) error { return nil }
	if err := e.svc.CompleteTask(context.Background(), e.pid, "t-1"); err != nil {
		t.Fatalf("CompleteTask with an accepting guard = %v", err)
	}
}
