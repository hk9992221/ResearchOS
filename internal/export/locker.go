package export

import (
	"context"
	"sync"

	"researchos/internal/ports"
)

// memoryLocker is the fallback implementation of ports.Locker used when the
// caller injects none. SPEC-01 provides no lock, so it lives in process memory:
// it never crosses a process boundary and never touches disk.
//
// The workspace lock is exclusive and fails fast; the per-project write lock
// blocks until free or ctx is done and refuses to start while a workspace
// (migration) lock is held, re-checking after it waited behind another writer.
type memoryLocker struct {
	mu        sync.Mutex
	workspace bool
	gates     map[string]chan struct{}
}

var _ ports.Locker = (*memoryLocker)(nil)

func newMemoryLocker() *memoryLocker {
	return &memoryLocker{gates: make(map[string]chan struct{})}
}

// WithWorkspaceLock implements ports.Locker.
func (l *memoryLocker) WithWorkspaceLock(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	if l.workspace {
		l.mu.Unlock()
		return ErrBusy
	}
	l.workspace = true
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.workspace = false
		l.mu.Unlock()
	}()
	return fn()
}

// WithProjectWriteLock implements ports.Locker.
func (l *memoryLocker) WithProjectWriteLock(ctx context.Context, projectID string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := l.workspaceBusy(); err != nil {
		return err
	}
	gate := l.gate(projectID)
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-gate }()
	if err := l.workspaceBusy(); err != nil {
		return err
	}
	return fn()
}

func (l *memoryLocker) workspaceBusy() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.workspace {
		return ErrBusy
	}
	return nil
}

func (l *memoryLocker) gate(projectID string) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	g, ok := l.gates[projectID]
	if !ok {
		g = make(chan struct{}, 1)
		l.gates[projectID] = g
	}
	return g
}
