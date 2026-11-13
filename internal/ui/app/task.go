package app

import (
	"sync"
	"time"
)

// TaskSnapshot is the state of one long-running operation, as the UI shows it:
// the stage, the progress, the message, the failure and whether a cancel was
// asked for.
type TaskSnapshot struct {
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
	mu        sync.Mutex
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

// NewTask returns a task that started now.
func NewTask() *Task {
	return &Task{started: time.Now(), stage: "Starting"}
}

// SetCancel installs the operation's safe cancel function.
func (t *Task) SetCancel(fn func()) {
	t.mu.Lock()
	t.cancel = fn
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
	t.mu.Unlock()
}

// Cancel asks the operation to stop at its next safe boundary. It is
// idempotent; the operation still reports its terminal state through Finish.
func (t *Task) Cancel() {
	t.mu.Lock()
	fn := t.cancel
	if t.canceling {
		t.mu.Unlock()
		return
	}
	t.canceling = true
	t.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Snapshot returns the state to render.
func (t *Task) Snapshot() TaskSnapshot {
	if t == nil {
		return TaskSnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return TaskSnapshot{
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

// MarkCanceled reports that the operation stopped because it was canceled.
func (t *Task) MarkCanceled() {
	t.mu.Lock()
	t.canceled = true
	t.finished = true
	t.mu.Unlock()
}
