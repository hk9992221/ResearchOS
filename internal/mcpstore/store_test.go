package mcpstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/workspace"
	"researchos/internal/mcp"
	"researchos/internal/mcpstore/migrations"
)

func newStore(t *testing.T) (*Store, *clock.Fixed, *workspace.Workspace) {
	t.Helper()
	fixed := &clock.Fixed{T: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	ws, err := workspace.Init(context.Background(), t.TempDir(), workspace.Options{
		Clock:      fixed,
		SkipBackup: true,
		Migrations: migrations.MustAll(),
	})
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	store := New(ws.Global(), Options{Clock: fixed, ConfirmationTTL: func(context.Context) time.Duration {
		return 15 * time.Minute
	}})
	return store, fixed, ws
}

func testPrincipal() mcp.Principal {
	return mcp.Principal{ClientID: "client-1", ClientName: "c1", Transport: mcp.TransportHTTP}
}

func TestConfirmationLifecycle(t *testing.T) {
	store, _, _ := newStore(t)
	confirms := NewConfirmations(store)
	ctx := context.Background()
	p := testPrincipal()
	conf, err := confirms.Enqueue(ctx, p, mcp.ConfirmationRequest{
		Tool: "project_delete", Title: "删除", Summary: "s", ProjectID: "p1",
		Impact: map[string]any{"notes": 2},
	}, json.RawMessage(`{"project_id":"p1"}`), "hash-1")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if conf.State != mcp.ConfirmationRequested || conf.ID == "" {
		t.Fatalf("confirmation = %+v", conf)
	}
	if _, err := confirms.Get(ctx, mcp.Principal{ClientID: "other"}, conf.ID); err == nil {
		t.Fatal("another client read the confirmation")
	}
	got, err := confirms.Get(ctx, p, conf.ID)
	if err != nil || got.Title != "删除" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := confirms.Resolve(ctx, conf.ID, true, "user"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := confirms.Resolve(ctx, conf.ID, true, "user"); err == nil {
		t.Fatal("double approval succeeded")
	}
	done, err := confirms.Finish(ctx, conf.ID, map[string]any{"ok": true}, "")
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if done.State != mcp.ConfirmationExecuted || len(done.Result) == 0 {
		t.Fatalf("finished = %+v", done)
	}
	if _, err := confirms.Finish(ctx, conf.ID, nil, "CONFLICT"); err == nil {
		t.Fatal("finish after executed succeeded")
	}

	second, _ := confirms.Enqueue(ctx, p, mcp.ConfirmationRequest{Tool: "trash_empty"}, nil, "hash-2")
	if err := confirms.CancelByClient(ctx, mcp.Principal{ClientID: "other"}, second.ID); err == nil {
		t.Fatal("another client cancelled the confirmation")
	}
	if err := confirms.CancelByClient(ctx, p, second.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	view, _ := confirms.Get(ctx, p, second.ID)
	if view.State != mcp.ConfirmationCancelled {
		t.Fatalf("state after cancel = %s", view.State)
	}
	if err := confirms.CancelByClient(ctx, p, second.ID); err == nil {
		t.Fatal("cancelled request cancelled twice")
	}

	third, _ := confirms.Enqueue(ctx, p, mcp.ConfirmationRequest{Tool: "report_confirm"}, nil, "hash-3")
	denied, err := confirms.Resolve(ctx, third.ID, false, "user")
	if err != nil || denied.State != mcp.ConfirmationDenied {
		t.Fatalf("deny = %+v, %v", denied, err)
	}
}

func TestConfirmationTTL(t *testing.T) {
	store, fixed, _ := newStore(t)
	confirms := NewConfirmations(store)
	ctx := context.Background()
	conf, _ := confirms.Enqueue(ctx, testPrincipal(), mcp.ConfirmationRequest{Tool: "project_delete"}, nil, "h")
	fixed.Advance(16 * time.Minute)
	got, err := confirms.Get(ctx, testPrincipal(), conf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != mcp.ConfirmationExpired {
		t.Fatalf("state = %s", got.State)
	}
	if _, err := confirms.Resolve(ctx, conf.ID, true, "user"); err == nil {
		t.Fatal("expired confirmation approved")
	}
}

func TestRecoveryRevertsApprovedAndFailsJobs(t *testing.T) {
	store, _, ws := newStore(t)
	confirms := NewConfirmations(store)
	jobs := NewJobs(store)
	ctx := context.Background()
	conf, _ := confirms.Enqueue(ctx, testPrincipal(), mcp.ConfirmationRequest{Tool: "project_delete"}, nil, "h")
	if _, err := confirms.Resolve(ctx, conf.ID, true, "user"); err != nil {
		t.Fatal(err)
	}
	job, err := jobs.Submit(ctx, testPrincipal(), mcp.JobRequest{
		Tool: "project_export", ProjectID: "p1", ReadOnly: false,
		Run: func(ctx context.Context, progress mcp.ProgressFunc) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a running job from a dead process.
	if _, err := ws.Global().DB().ExecContext(ctx,
		`UPDATE mcp_job SET state='running', started_at=? WHERE id=?`, clock.Format(time.Now().UTC()), job.ID); err != nil {
		t.Fatal(err)
	}
	report, err := store.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Reverted != 1 {
		t.Fatalf("reverted = %d", report.Reverted)
	}
	reverted, _ := confirms.Get(ctx, testPrincipal(), conf.ID)
	if reverted.State != mcp.ConfirmationRequested {
		t.Fatalf("approved not reverted: %s", reverted.State)
	}
	failed, _ := jobs.Get(ctx, testPrincipal(), job.ID)
	if failed.State != mcp.JobFailed || len(failed.Error) == 0 {
		t.Fatalf("job after recovery = %+v", failed)
	}
	_ = jobs.Cancel(ctx, testPrincipal(), job.ID)
}

func TestJobProgressAndConcurrency(t *testing.T) {
	store, _, _ := newStore(t)
	jobs := NewJobs(store)
	ctx := context.Background()
	done := make(chan mcp.Job, 1)
	job, err := jobs.Submit(ctx, testPrincipal(), mcp.JobRequest{
		Tool: "project_export", ProjectID: "p1",
		Run: func(ctx context.Context, progress mcp.ProgressFunc) (any, error) {
			progress(10, "开始")
			progress(50, "写入 1/2")
			progress(30, "倒退被忽略")
			return map[string]any{"ok": true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			got, err := jobs.Get(ctx, testPrincipal(), job.ID)
			if err == nil && (got.State == mcp.JobSucceeded || got.State == mcp.JobFailed || got.State == mcp.JobCancelled) {
				done <- got
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	select {
	case finished := <-done:
		if finished.State != mcp.JobSucceeded || finished.Progress != 100 {
			t.Fatalf("finished = %+v", finished)
		}
		var result map[string]any
		_ = json.Unmarshal(finished.Result, &result)
		if result["ok"] != true {
			t.Fatalf("result = %v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("job did not finish")
	}

	// Two write jobs per project are allowed; the third is rate limited.
	block := make(chan struct{})
	submit := func() error {
		_, err := jobs.Submit(ctx, testPrincipal(), mcp.JobRequest{
			Tool: "project_export", ProjectID: "busy",
			Run: func(ctx context.Context, progress mcp.ProgressFunc) (any, error) {
				select {
				case <-block:
				case <-ctx.Done():
				}
				return nil, nil
			},
		})
		return err
	}
	if err := submit(); err != nil {
		t.Fatal(err)
	}
	if err := submit(); err != nil {
		t.Fatal(err)
	}
	if err := submit(); err == nil || mcp.CodeOf(err) != mcp.CodeRateLimited {
		t.Fatalf("third write job = %v", err)
	}
	close(block)
}

func TestIdempotencyStore(t *testing.T) {
	store, fixed, _ := newStore(t)
	ctx := context.Background()
	claim, err := store.Claim(ctx, "c", "k", "tool", "hash", fixed.T)
	if err != nil || claim.Replayed {
		t.Fatalf("first claim = %+v, %v", claim, err)
	}
	if _, err := store.Claim(ctx, "c", "k", "tool", "hash", fixed.T); err != mcp.IdempotencyInFlight {
		t.Fatalf("in-flight claim = %v", err)
	}
	if _, err := store.Claim(ctx, "c", "k", "tool", "other", fixed.T); err != mcp.IdempotencyConflict {
		t.Fatalf("conflict claim = %v", err)
	}
	if err := store.Complete(ctx, "c", "k", json.RawMessage(`{"ok":1}`)); err != nil {
		t.Fatal(err)
	}
	replay, err := store.Claim(ctx, "c", "k", "tool", "hash", fixed.T)
	if err != nil || !replay.Replayed || string(replay.Result) != `{"ok":1}` {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	fixed.Advance(25 * time.Hour)
	after, err := store.Claim(ctx, "c", "k", "tool", "hash", fixed.T)
	if err != nil || after.Replayed {
		t.Fatalf("claim after expiry = %+v, %v", after, err)
	}
	if err := store.Release(ctx, "c", "k"); err != nil {
		t.Fatal(err)
	}
}

func TestClientTokens(t *testing.T) {
	store, _, _ := newStore(t)
	clients := NewClientService(store)
	ctx := context.Background()
	client, token, err := clients.Create(ctx, "Claude Desktop", mcp.TransportHTTP)
	if err != nil || token == "" {
		t.Fatalf("create = %+v, %v", client, err)
	}
	p, err := clients.Authenticate(ctx, token)
	if err != nil || p.ClientID != client.ID {
		t.Fatalf("authenticate = %+v, %v", p, err)
	}
	if _, err := clients.Authenticate(ctx, "bogus"); err == nil {
		t.Fatal("bogus token accepted")
	}
	newToken, err := clients.Rotate(ctx, client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Authenticate(ctx, token); err == nil {
		t.Fatal("old token still works after rotation")
	}
	if _, err := clients.Authenticate(ctx, newToken); err != nil {
		t.Fatalf("new token: %v", err)
	}
	if err := clients.SetEnabled(ctx, client.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Authenticate(ctx, newToken); err == nil {
		t.Fatal("disabled client authenticated")
	}
	list, _ := clients.List(ctx)
	if len(list) != 1 {
		t.Fatalf("list = %v", list)
	}
	if err := clients.Revoke(ctx, client.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Authenticate(ctx, newToken); err == nil {
		t.Fatal("revoked token accepted")
	}
}

func TestPurge(t *testing.T) {
	store, fixed, ws := newStore(t)
	ctx := context.Background()
	confirms := NewConfirmations(store)
	conf, _ := confirms.Enqueue(ctx, testPrincipal(), mcp.ConfirmationRequest{Tool: "x"}, nil, "h")
	if _, err := confirms.Resolve(ctx, conf.ID, false, "user"); err != nil {
		t.Fatal(err)
	}
	fixed.Advance(31 * 24 * time.Hour)
	n, err := store.Purge(ctx, fixed.T.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("purge removed nothing")
	}
	var count int
	if err := ws.Global().DB().QueryRowContext(ctx, `SELECT count(*) FROM mcp_confirmation WHERE id = ?`, conf.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("settled confirmation survived retention")
	}
}

func TestJobCancel(t *testing.T) {
	store, _, _ := newStore(t)
	jobs := NewJobs(store)
	ctx := context.Background()
	job, err := jobs.Submit(ctx, testPrincipal(), mcp.JobRequest{
		Tool: "experiment_scan", ProjectID: "p1", ReadOnly: true,
		Run: func(ctx context.Context, progress mcp.ProgressFunc) (any, error) {
			progress(10, "扫描中")
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, err := jobs.Get(ctx, testPrincipal(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == mcp.JobRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never started: %+v", current)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := jobs.Cancel(ctx, testPrincipal(), job.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	for {
		current, err := jobs.Get(ctx, testPrincipal(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == mcp.JobCancelled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job not cancelled: %+v", current)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := jobs.Cancel(ctx, testPrincipal(), job.ID); err == nil {
		t.Fatal("cancelled job cancelled twice")
	}
}

func TestConfirmationExecutionClaim(t *testing.T) {
	store, _, _ := newStore(t)
	confirms := NewConfirmations(store)
	ctx := context.Background()
	p := testPrincipal()
	conf, _ := confirms.Enqueue(ctx, p, mcp.ConfirmationRequest{Tool: "project_delete"}, nil, "h")
	if _, err := confirms.Resolve(ctx, conf.ID, true, "user"); err != nil {
		t.Fatal(err)
	}
	claimed, err := confirms.ClaimExecution(ctx, conf.ID)
	if err != nil || claimed.State != mcp.ConfirmationExecuting {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if _, err := confirms.ClaimExecution(ctx, conf.ID); err == nil {
		t.Fatal("confirmation executed twice")
	}
	if _, err := confirms.Finish(ctx, conf.ID, nil, ""); err != nil {
		t.Fatalf("finish executing: %v", err)
	}
	if _, err := confirms.ClaimExecution(ctx, conf.ID); err == nil {
		t.Fatal("executed confirmation claimed again")
	}
}
