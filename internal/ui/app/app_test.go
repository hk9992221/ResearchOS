package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/ports"
	"researchos/internal/project"
	"researchos/internal/ui/components"
)

func TestDefaults(t *testing.T) {
	a := New(Options{WorkspaceName: "ResearchOS"})
	if got := a.Route().Kind; got != RouteHome {
		t.Errorf("route = %q, want home", got)
	}
	if a.SidebarCollapsed() || a.PaletteOpen() {
		t.Error("sidebar or palette is open by default")
	}
	if insp := a.Inspector(); insp.Open || insp.Width != 320 {
		t.Errorf("inspector default = %+v", insp)
	}
	if a.WorkspaceName() != "ResearchOS" {
		t.Error("workspace name lost")
	}
}

func TestNavigateCopiesParamsAndFillsProject(t *testing.T) {
	a := New(Options{})
	a.SetProject(&project.Project{ID: "p1", Name: "Alpha"})

	params := map[string]string{"view": "blocked"}
	a.Navigate(ports.Route{Kind: ports.RouteTask, Params: params})
	params["view"] = "mutated"

	got := a.Route()
	if got.ProjectID != "p1" {
		t.Errorf("project id = %q, want p1", got.ProjectID)
	}
	if got.Params["view"] != "blocked" {
		t.Errorf("params were not copied: %v", got.Params)
	}
}

func TestSidebarPersistence(t *testing.T) {
	var saved []bool
	writeErr := errors.New("database is locked")
	a := New(Options{PersistSidebar: func(_ context.Context, v bool) error {
		saved = append(saved, v)
		if v {
			return writeErr
		}
		return nil
	}})

	if err := a.SetSidebarCollapsed(context.Background(), true); !errors.Is(err, writeErr) {
		t.Fatalf("err = %v, want the write error", err)
	}
	if !a.SidebarCollapsed() {
		t.Error("a failed write rolled the sidebar state back")
	}
	if err := a.ToggleSidebar(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.SidebarCollapsed() || len(saved) != 2 || saved[1] {
		t.Errorf("saved = %v, collapsed = %v", saved, a.SidebarCollapsed())
	}
}

func TestToastsExpireAndAreCapped(t *testing.T) {
	fixed := &clock.Fixed{T: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	a := New(Options{Clock: fixed})
	for range 6 {
		a.Toast(LevelInfo, "message", "body")
	}
	if got := len(a.Toasts()); got != maxToasts {
		t.Fatalf("toasts = %d, want capped at %d", got, maxToasts)
	}
	last := a.Toasts()[len(a.Toasts())-1]
	fixed.Advance(7 * time.Second)
	if got := len(a.Toasts()); got != 0 {
		t.Errorf("toasts after expiry = %d", got)
	}
	a.DismissToast(last.ID)
	if len(a.Toasts()) != 0 {
		t.Error("dismiss left a toast")
	}
}

func TestTaskLifecycle(t *testing.T) {
	task := NewTask("copy")
	cancels := 0
	task.SetCancel(func() { cancels++ })
	task.Stage("copy")
	task.Progress(3, 10)
	task.SetMessage("Copying notes")

	if snap := task.Snapshot(); snap.Stage != "copy" || snap.Done != 3 || snap.Total != 10 {
		t.Errorf("snapshot = %+v", snap)
	}
	task.Cancel()
	task.Cancel()
	if cancels != 1 {
		t.Errorf("cancel called %d times, want once", cancels)
	}
	if snap := task.Snapshot(); !snap.Canceling {
		t.Error("task does not report the pending cancel")
	}
	task.Finish(errors.New("boom"))
	if snap := task.Snapshot(); !snap.Finished || snap.Err == nil {
		t.Errorf("finished snapshot = %+v", snap)
	}
}

func TestProjectCacheWithoutService(t *testing.T) {
	a := New(Options{})
	if _, ok, _ := a.ProjectList(); ok {
		t.Fatal("cache reports loaded before a load")
	}
	if err := a.RefreshProjects(context.Background()); err != nil {
		t.Fatal(err)
	}
	list, ok, err := a.ProjectList()
	if !ok || err != nil || len(list) != 0 {
		t.Errorf("cache = %v, %v, %v", list, ok, err)
	}
	if err := a.OpenProject(context.Background(), "p1"); err == nil {
		t.Error("open project without a service did not fail")
	}
}

func TestInspectorState(t *testing.T) {
	a := New(Options{})
	a.ShowInspector("Note", func(v *components.UI) {})
	if insp := a.Inspector(); !insp.Open || insp.Title != "Note" || insp.Body == nil {
		t.Fatalf("inspector = %+v", insp)
	}
	a.SetInspectorWidth(360)
	if got := a.Inspector().Width; got != 360 {
		t.Errorf("width = %v", got)
	}
	a.HideInspector()
	if a.Inspector().Open {
		t.Error("inspector did not close")
	}
}

func TestRunTaskIsAsyncAndKeepsTerminalState(t *testing.T) {
	a := New(Options{})
	release := make(chan struct{})
	started := make(chan struct{})
	a.RunTask("migrate", func(ctx context.Context, snapshot *Task) error {
		snapshot.Stage("copying")
		close(started)
		<-release
		return nil
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("task did not start")
	}
	// The call returned while the task was still running.
	live := a.Tasks()
	if len(live) != 1 || live[0].Finished {
		t.Fatalf("tasks = %+v", live)
	}
	if live[0].Stage != "copying" {
		t.Errorf("stage = %q", live[0].Stage)
	}
	close(release)
	waitTasks(t, a, func(list []TaskSnapshot) bool { return len(list) == 1 && list[0].Finished })
	if got := a.Tasks()[0].State; got != TaskDone {
		t.Errorf("state = %v, want done", got)
	}

	a.RunTask("broken", func(context.Context, *Task) error { return errors.New("boom") })
	waitTasks(t, a, func(list []TaskSnapshot) bool {
		for _, s := range list {
			if s.Name == "broken" && s.Finished {
				return true
			}
		}
		return false
	})
	if got := a.Tasks(); len(got) != 2 {
		t.Errorf("terminal task was dropped: %+v", got)
	}
}

func TestTaskCancelReachesTheOperation(t *testing.T) {
	a := New(Options{})
	sawCancel := make(chan struct{})
	task := a.RunTask("scan", func(ctx context.Context, snapshot *Task) error {
		<-ctx.Done()
		close(sawCancel)
		return ctx.Err()
	})
	a.CancelTask(task.ID())
	select {
	case <-sawCancel:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not reach the operation")
	}
	waitTasks(t, a, func(list []TaskSnapshot) bool { return list[0].Finished })
	if got := a.Tasks()[0].State; got != TaskCanceled {
		t.Errorf("state = %v, want canceled", got)
	}
	if !a.Tasks()[0].Canceled {
		t.Error("snapshot does not report the cancel")
	}
}

func waitTasks(t *testing.T, a *App, ok func([]TaskSnapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok(a.Tasks()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("tasks did not settle: %+v", a.Tasks())
}
