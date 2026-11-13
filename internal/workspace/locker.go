package workspace

import (
	"context"
	"fmt"
	"sync"

	"researchos/internal/ports"
)

// memoryLocker is the in-process fallback implementation of ports.Locker used
// when the application injects none. It mirrors the project service contract
// (SPEC-03 §5.2): the workspace lock is exclusive and fails fast with ErrBusy,
// the per-project write lock blocks until free and refuses to start while a
// workspace lock is held. It never crosses the process boundary.
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
		return fmt.Errorf("%w: a migration holds the workspace lock", ErrBusy)
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
		return fmt.Errorf("%w: a migration holds the workspace lock", ErrBusy)
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
