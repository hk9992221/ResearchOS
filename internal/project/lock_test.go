package project

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLockerBusy(t *testing.T) {
	ctx := context.Background()
	l := newMemoryLocker()

	// A migration (workspace lock) blocks project writes with ErrBusy.
	err := l.WithWorkspaceLock(ctx, func() error {
		if err := l.WithProjectWriteLock(ctx, "p1", func() error { return nil }); !errors.Is(err, ErrBusy) {
			t.Fatalf("project lock under workspace lock = %v, want ErrBusy", err)
		}
		if err := l.WithWorkspaceLock(ctx, func() error { return nil }); !errors.Is(err, ErrBusy) {
			t.Fatalf("nested workspace lock = %v, want ErrBusy", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithWorkspaceLock: %v", err)
	}

	// After release the project lock is available again.
	if err := l.WithProjectWriteLock(ctx, "p1", func() error { return nil }); err != nil {
		t.Fatalf("project lock after release: %v", err)
	}

	// The lock is released even when fn fails.
	if err := l.WithProjectWriteLock(ctx, "p1", func() error { return errors.New("boom") }); err == nil {
		t.Fatal("expected fn error")
	}
	if err := l.WithProjectWriteLock(ctx, "p1", func() error { return nil }); err != nil {
		t.Fatalf("lock leaked after fn error: %v", err)
	}

	// Concurrent writers of one project serialize; a waiting writer honours ctx.
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = l.WithProjectWriteLock(ctx, "p2", func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := l.WithProjectWriteLock(waitCtx, "p2", func() error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting lock = %v, want context.DeadlineExceeded", err)
	}
	// A different project is not blocked by p2's writer.
	otherCtx, cancelOther := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancelOther()
	if err := l.WithProjectWriteLock(otherCtx, "p3", func() error { return nil }); err != nil {
		t.Fatalf("independent project lock: %v", err)
	}
	close(release)
	<-done
	if err := l.WithProjectWriteLock(ctx, "p2", func() error { return nil }); err != nil {
		t.Fatalf("p2 lock after release: %v", err)
	}

	// Cancelled contexts never enter fn.
	cancelled, cancel2 := context.WithCancel(ctx)
	cancel2()
	called := false
	if err := l.WithProjectWriteLock(cancelled, "p4", func() error { called = true; return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lock = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("fn ran with a cancelled context")
	}
}
