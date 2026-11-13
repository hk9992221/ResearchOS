package project

import (
	"context"
	"fmt"
	"sync"

	"researchos/internal/ports"
)

// memoryLocker is the in-process implementation of ports.Locker (SPEC-03 §5.2
// "以下能力 SPEC-01 未提供，由本 SPEC 在进程内实现"). It is intentionally small:
//
//   - the workspace lock is exclusive and fails fast with ErrBusy, because it
//     models a migration that must not wait for new writers;
//   - the project write lock is exclusive per project and blocks until the lock
//     is free or ctx is done;
//   - a project write lock refuses to start (ErrBusy) while the workspace lock
//     is held, and re-checks after waiting so a writer that queued behind
//     another writer cannot slip past a migration that started meanwhile.
//
// Every acquisition is released by defer, so an error or panic in fn cannot
// leak the lock.
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
	// A migration may have acquired the workspace lock while we waited behind
	// another project writer; the caller expects ErrBusy instead of running
	// alongside a migration.
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

// gate returns the buffered channel that serializes writers of one project.
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
