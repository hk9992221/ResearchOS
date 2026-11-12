package confirm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
)

func fixedClock() *clock.Fixed {
	return &clock.Fixed{T: time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC)}
}

func TestIssueConsume(t *testing.T) {
	svc := New(Options{Clock: fixedClock()})
	ctx := context.Background()
	sc := Scope{
		Kind:        KindPurgeEntity,
		ProjectID:   "p1",
		EntityTypes: []string{"note", "idea"},
		EntityIDs:   []string{"n1", "i1"},
	}
	token, err := svc.Issue(ctx, sc)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if token == "" {
		t.Fatal("Issue returned an empty token")
	}
	// Order-insensitive and duplicate-tolerant scope matching.
	consumeScope := Scope{
		Kind:        KindPurgeEntity,
		ProjectID:   "p1",
		EntityTypes: []string{"idea", "note", "note"},
		EntityIDs:   []string{"i1", "n1"},
	}
	if err := svc.Consume(ctx, token, consumeScope); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if err := svc.Consume(ctx, token, consumeScope); !errors.Is(err, errs.ErrUserConfirmationRequired) {
		t.Fatalf("second Consume = %v, want ErrUserConfirmationRequired", err)
	}
}

func TestUnknownToken(t *testing.T) {
	svc := New(Options{Clock: fixedClock()})
	if err := svc.Consume(context.Background(), "deadbeef", Scope{Kind: KindEmptyTrash}); !errors.Is(err, errs.ErrUserConfirmationRequired) {
		t.Fatalf("Consume(unknown) = %v", err)
	}
}

func TestExpiry(t *testing.T) {
	c := fixedClock()
	svc := New(Options{Clock: c})
	ctx := context.Background()
	sc := Scope{Kind: KindDeleteProject, ProjectID: "p1"}
	token, err := svc.Issue(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	c.Advance(4 * time.Minute)
	if err := svc.Consume(ctx, token, sc); err != nil {
		t.Fatalf("Consume before TTL: %v", err)
	}

	expired, err := svc.Issue(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	c.Advance(6 * time.Minute)
	if err := svc.Consume(ctx, expired, sc); !errors.Is(err, errs.ErrUserConfirmationRequired) {
		t.Fatalf("Consume after TTL = %v, want ErrUserConfirmationRequired", err)
	}
	// The expired token is gone even if the clock moves backward.
	c.Advance(-10 * time.Minute)
	if err := svc.Consume(ctx, expired, sc); !errors.Is(err, errs.ErrUserConfirmationRequired) {
		t.Fatalf("Consume expired token again = %v", err)
	}
}

func TestScopeMismatchKeepsTokenUsable(t *testing.T) {
	svc := New(Options{Clock: fixedClock()})
	ctx := context.Background()
	sc := Scope{Kind: KindPurgeEntity, ProjectID: "p1", EntityTypes: []string{"note"}, EntityIDs: []string{"n1"}}
	token, err := svc.Issue(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	mismatches := []Scope{
		{Kind: KindEmptyTrash, ProjectID: "p1", EntityTypes: []string{"note"}, EntityIDs: []string{"n1"}},
		{Kind: KindPurgeEntity, ProjectID: "p2", EntityTypes: []string{"note"}, EntityIDs: []string{"n1"}},
		{Kind: KindPurgeEntity, ProjectID: "p1", EntityTypes: []string{"idea"}, EntityIDs: []string{"n1"}},
		{Kind: KindPurgeEntity, ProjectID: "p1", EntityTypes: []string{"note"}, EntityIDs: []string{"n2"}},
		{Kind: KindPurgeEntity, ProjectID: "p1", EntityTypes: []string{"note"}, EntityIDs: []string{"n1", "n2"}},
	}
	for _, m := range mismatches {
		if err := svc.Consume(ctx, token, m); !errors.Is(err, errs.ErrConfirmScopeMismatch) {
			t.Fatalf("Consume(%+v) = %v, want ErrConfirmScopeMismatch", m, err)
		}
	}
	if err := svc.Consume(ctx, token, sc); err != nil {
		t.Fatalf("Consume with the right scope after mismatches: %v", err)
	}
}

func TestUnknownKindRejected(t *testing.T) {
	svc := New(Options{Clock: fixedClock()})
	if _, err := svc.Issue(context.Background(), Scope{Kind: Kind("drop_database")}); err == nil {
		t.Fatal("Issue accepted an unknown kind")
	}
}

func TestConcurrentIssueConsume(t *testing.T) {
	svc := New(Options{Clock: fixedClock()})
	ctx := context.Background()
	const workers = 50
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sc := Scope{Kind: KindPurgeEntity, ProjectID: "p1", EntityIDs: []string{string(rune('a' + i%26))}}
			token, err := svc.Issue(ctx, sc)
			if err != nil {
				errCh <- err
				return
			}
			if err := svc.Consume(ctx, token, sc); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent token flow: %v", err)
	}
}

func TestCustomTTL(t *testing.T) {
	c := fixedClock()
	svc := New(Options{Clock: c, TTL: time.Second})
	sc := Scope{Kind: KindEmptyTrash, ProjectID: "p1"}
	token, err := svc.Issue(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	c.Advance(2 * time.Second)
	if err := svc.Consume(context.Background(), token, sc); !errors.Is(err, errs.ErrUserConfirmationRequired) {
		t.Fatalf("Consume with custom TTL = %v", err)
	}
}
