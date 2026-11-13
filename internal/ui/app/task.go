package app

import (
	"context"
	"errors"
	"sync"
	"time"
)

// TaskState is where a long-running operation stands.
type TaskState int

const (
	// TaskQueued is a task that has not started running yet.
	TaskQueued TaskState = iota
	// TaskRunning is a task the UI is showing progress for.
	TaskRunning
	// TaskDone is a task that finished successfully.
	TaskDone
	// TaskFailed is a task that ended with an error.
	TaskFailed
	// TaskCanceled is a task stopped at a safe boundary on request.
	TaskCanceled
)

// TaskSnapshot is the state of one long-running operation, as the UI shows it:
// its name, stage, progress, message, failure and cancel state.
type TaskSnapshot struct {
	ID        int64
	Name      string
	State     TaskState
	Stage     string
	Done      int
	Total     int
	Message   string
	Err       error
	Canceled  bool
	Canceling bool
	Finished  bool
	Started   time.Time
}

// Task is the UI state of a long-running operation. The operation updates it
// from its goroutine; the shell reads a snapshot while rendering. Cancel asks
// the operation to stop at its next safe boundary; it never interrupts a
// database transaction (SPEC-01 §17).
type Task struct {
	id   int64
	name string

	mu        sync.Mutex
	state     TaskState
	stage     string
	done      int
	total     int
	message   string
	err       error
	canceled  bool
	canceling bool
	finished  bool
	started   time.Time
	cancel    func()
}

// NewTask returns a queued task.
func NewTask(name string) *Task {
	return &Task{name: name, state: TaskQueued, stage: "Queued", started: time.Now()}
}

// ID returns the task's identity in its App.
func (t *Task) ID() int64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.id
}

// Name returns the operation's name.
func (t *Task) Name() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.name
}

// SetCancel installs the operation's safe cancel function. Canceling also
// cancels the task's context, so operations that honor context stop at their
// next safe boundary.
func (t *Task) SetCancel(fn func()) {
	t.mu.Lock()
	t.cancel = fn
	t.mu.Unlock()
}

// start marks the task running.
func (t *Task) start() {
	t.mu.Lock()
	t.state = TaskRunning
	t.stage = "Starting"
	t.started = time.Now()
	t.mu.Unlock()
}

// Stage records the current stage, as the migration phases.
func (t *Task) Stage(stage string) {
	t.mu.Lock()
	t.stage = stage
	t.mu.Unlock()
}

// Progress records how much is done out of how much in total. A total of 0
// means the length is unknown.
func (t *Task) Progress(done, total int) {
	t.mu.Lock()
	t.done, t.total = done, total
	t.mu.Unlock()
}

// SetMessage records a readable status line.
func (t *Task) SetMessage(message string) {
	t.mu.Lock()
	t.message = message
	t.mu.Unlock()
}

// Finish ends the task, with err when it failed.
func (t *Task) Finish(err error) {
	t.mu.Lock()
	t.err = err
	t.finished = true
	if err != nil {
		t.state = TaskFailed
	} else {
		t.state = TaskDone
	}
	t.mu.Unlock()
}

// Cancel asks the operation to stop at its next safe boundary. It is
// idempotent; the operation still reports its terminal state through Finish.
func (t *Task) Cancel() {
	t.mu.Lock()
	if t.canceling || t.finished {
		t.mu.Unlock()
		return
	}
	t.canceling = true
	fn := t.cancel
	t.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// MarkCanceled reports that the operation stopped because it was canceled.
func (t *Task) MarkCanceled() {
	t.mu.Lock()
	t.canceled = true
	t.finished = true
	t.state = TaskCanceled
	t.mu.Unlock()
}

// Snapshot returns the state to render.
func (t *Task) Snapshot() TaskSnapshot {
	if t == nil {
		return TaskSnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return TaskSnapshot{
		ID:        t.id,
		Name:      t.name,
		State:     t.state,
		Stage:     t.stage,
		Done:      t.done,
		Total:     t.total,
		Message:   t.message,
		Err:       t.err,
		Canceled:  t.canceled,
		Canceling: t.canceling,
		Finished:  t.finished,
		Started:   t.started,
	}
}

// RunTask starts name in a goroutine and keeps its state until the app closes.
// fn receives a context canceled by Task.Cancel and must return at its next
// safe boundary. The returned task is safe to read from any goroutine.
func (a *App) RunTask(name string, fn func(ctx context.Context, t *Task) error) *Task {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.taskSeq++
	t := NewTask(name)
	t.mu.Lock()
	t.id = a.taskSeq
	t.mu.Unlock()
	t.SetCancel(cancel)
	a.tasks = append(a.tasks, t)
	if len(a.tasks) > maxTasks {
		a.tasks = pruneTasks(a.tasks)
	}
	a.mu.Unlock()
	a.changed()

	go func() {
		t.start()
		a.changed()
		err := fn(ctx, t)
		switch {
		case ctx.Err() != nil && errors.Is(err, context.Canceled):
			t.MarkCanceled()
		case ctx.Err() != nil && err == nil:
			t.MarkCanceled()
		default:
			t.Finish(err)
		}
		cancel()
		a.changed()
	}()
	return t
}

// Tasks returns the task states, newest last. Terminal tasks stay until the
// next long run pushes them past the cap, so a failure never disappears
// silently.
func (a *App) Tasks() []TaskSnapshot {
	a.mu.Lock()
	tasks := append([]*Task(nil), a.tasks...)
	a.mu.Unlock()
	out := make([]TaskSnapshot, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.Snapshot())
	}
	return out
}

// CancelTask asks one task to stop at its next safe boundary.
func (a *App) CancelTask(id int64) {
	a.mu.Lock()
	var task *Task
	for _, t := range a.tasks {
		if t.ID() == id {
			task = t
			break
		}
	}
	a.mu.Unlock()
	if task != nil {
		task.Cancel()
		a.changed()
	}
}

// ClearTasks drops the terminal tasks from the list.
func (a *App) ClearTasks() {
	a.mu.Lock()
	live := a.tasks[:0]
	for _, t := range a.tasks {
		if !t.Snapshot().Finished {
			live = append(live, t)
		}
	}
	a.tasks = live
	a.mu.Unlock()
	a.changed()
}

// pruneTasks caps finished tasks, keeping the running ones and the newest
// finished ones.
func pruneTasks(tasks []*Task) []*Task {
	out := make([]*Task, 0, len(tasks))
	finished := 0
	for _, t := range tasks {
		if t.Snapshot().Finished {
			finished++
		}
	}
	drop := finished - maxFinishedTasks
	for _, t := range tasks {
		if drop > 0 && t.Snapshot().Finished {
			drop--
			continue
		}
		out = append(out, t)
	}
	return out
}

const (
	maxTasks         = 12
	maxFinishedTasks = 8
)
