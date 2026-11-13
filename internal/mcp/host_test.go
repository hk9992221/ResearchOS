package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func principal() Principal {
	return Principal{ClientID: "c1", ClientName: "client", Transport: TransportStdio, ConnectionID: "conn-1"}
}

func call(t *testing.T, host *Host, name string, args map[string]any) CallResult {
	t.Helper()
	body, _ := json.Marshal(args)
	res, err := host.Call(context.Background(), principal(), CallRequest{Name: name, Arguments: body})
	if err != nil {
		t.Fatalf("%s: unexpected protocol error: %v", name, err)
	}
	return res
}

func TestToolsDeterministicAndSorted(t *testing.T) {
	p1 := newFakeProvider(ModuleNotes, "b", CategoryWrite, false)
	p2 := newFakeProvider(ModuleCore, "a", CategoryRead, true)
	host := newTestHost(t, []ToolProvider{p2, p1}, nil)

	first, err := host.Tools(context.Background(), principal())
	if err != nil {
		t.Fatal(err)
	}
	second, _ := host.Tools(context.Background(), principal())
	one, _ := json.Marshal(first)
	two, _ := json.Marshal(second)
	if string(one) != string(two) {
		t.Fatal("tools/list output is not deterministic")
	}
	if len(first) != 2 || first[0].Name != "a_tool" {
		t.Fatalf("tools order = %v", first)
	}

	ro := principal()
	ro.AccessMode = AccessReadOnly
	readOnly, _ := host.Tools(context.Background(), ro)
	if len(readOnly) != 1 || readOnly[0].Name != "a_tool" {
		t.Fatalf("read_only tools = %v", readOnly)
	}
	body, _ := json.Marshal(map[string]any{"value": "x", "idempotency_key": "k"})
	res, err := host.Call(context.Background(), ro, CallRequest{Name: "b_tool", Arguments: body})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("write tool ran in read_only mode")
	}
	if got := structuredCode(t, res); got != CodeAccessMode {
		t.Fatalf("code = %s", got)
	}
}

func TestReadinessTrimmingAndNotification(t *testing.T) {
	provider := newFakeProvider(ModuleIdeas, "idea", CategoryRead, true)
	provider.setReady(nil)
	notifier := NewNotifier()
	settings := &mapSettings{values: map[string]string{}}
	host, err := NewHost(HostOptions{
		Settings: settings, Broker: newMemBroker(), Jobs: newMemJobs(), Idempotency: newMemIdempotency(),
		Notifier: notifier, Limiter: NewLimiter(1000, 1000, nil), Providers: []ToolProvider{provider},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, _ := host.Tools(context.Background(), principal())
	if len(tools) != 1 {
		t.Fatal("tool missing while ready")
	}
	provider.setReady(Errorf(CodeInternal, "down"))
	tools, _ = host.Tools(context.Background(), principal())
	if len(tools) != 0 {
		t.Fatal("tool listed while module is not ready")
	}
	res := call(t, host, "idea_tool", map[string]any{"value": "x"})
	if !res.IsError || structuredCode(t, res) != CodeNotFound {
		t.Fatalf("module-not-ready result = %v", res.Structured)
	}
	if !strings.Contains(string(mustMarshal(t, res.Structured)), "ideas") {
		t.Fatal("NOT_FOUND does not expose details.module")
	}

	ch, cancel := notifier.Subscribe()
	defer cancel()
	provider.setReady(nil)
	if !host.Refresh(context.Background()) {
		t.Fatal("refresh did not detect a readiness change")
	}
	select {
	case <-ch:
	default:
		t.Fatal("tools/list_changed notification was not broadcast")
	}
}

func TestValidationAndIdempotency(t *testing.T) {
	provider := newFakeProvider(ModuleNotes, "note", CategoryWrite, false)
	host := newTestHost(t, []ToolProvider{provider}, nil)

	if _, err := host.Call(context.Background(), principal(), CallRequest{Name: "nope"}); err == nil || CodeOf(err) != CodeInvalidArgument {
		t.Fatal("unknown tool not rejected with an INVALID_ARGUMENT protocol error")
	}
	if res := call(t, host, "note_tool", map[string]any{"value": "x", "extra": 1}); structuredCode(t, res) != CodeInvalidArgument {
		t.Fatal("additionalProperties not enforced")
	}
	if res := call(t, host, "note_tool", map[string]any{}); structuredCode(t, res) != CodeInvalidArgument {
		t.Fatal("required property not enforced")
	}
	if res := call(t, host, "note_tool", map[string]any{"value": "x"}); structuredCode(t, res) != CodeInvalidArgument {
		t.Fatal("missing idempotency_key accepted")
	}
	if res := call(t, host, "note_tool", map[string]any{"value": "panic", "idempotency_key": "k1"}); !res.IsError {
		t.Fatal("panic escaped the host")
	}
	first := call(t, host, "note_tool", map[string]any{"value": "x", "idempotency_key": "k2"})
	second := call(t, host, "note_tool", map[string]any{"value": "x", "idempotency_key": "k2"})
	if first.IsError || second.IsError {
		t.Fatalf("idempotent call failed: %v / %v", first.Structured, second.Structured)
	}
	meta, _ := second.Meta["replayed"].(bool)
	if !meta {
		t.Fatal("replay was not marked")
	}
	conflict := call(t, host, "note_tool", map[string]any{"value": "y", "idempotency_key": "k2"})
	if structuredCode(t, conflict) != CodeConflict {
		t.Fatalf("same key different args = %v", conflict.Structured)
	}
}

func TestPendingAndJobOutcomes(t *testing.T) {
	provider := newFakeProvider(ModuleNotes, "note", CategoryConfirm, false)
	jobProvider := newFakeProvider(ModuleKanban, "task", CategoryJob, false)
	host := newTestHost(t, []ToolProvider{provider, jobProvider}, nil)

	pending := call(t, host, "note_tool", map[string]any{"value": "pending", "project_id": "p1", "idempotency_key": "c1"})
	body := structuredMap(t, pending.Structured)
	if body["status"] != StatusPending || body["confirmation_id"] == nil {
		t.Fatalf("pending result = %v", body)
	}
	replay := call(t, host, "note_tool", map[string]any{"value": "pending", "project_id": "p1", "idempotency_key": "c1"})
	if structuredMap(t, replay.Structured)["confirmation_id"] != body["confirmation_id"] {
		t.Fatal("C replay produced a different confirmation id")
	}
	job := call(t, host, "task_tool", map[string]any{"value": "job", "idempotency_key": "j1"})
	if structuredMap(t, job.Structured)["status"] != StatusJob {
		t.Fatalf("job result = %v", job.Structured)
	}
}

func TestArchivedGateAndPayloadLimits(t *testing.T) {
	provider := newFakeProvider(ModuleNotes, "note", CategoryWrite, false)
	gate := GateFunc(func(ctx context.Context, projectID string) (bool, error) {
		if projectID == "archived" {
			return true, nil
		}
		if projectID == "missing" {
			return false, Errorf(CodeNotFound, "no project")
		}
		return false, nil
	})
	host := newTestHost(t, []ToolProvider{provider}, gate)
	res := call(t, host, "note_tool", map[string]any{"value": "x", "project_id": "archived", "idempotency_key": "a1"})
	if structuredCode(t, res) != CodeArchivedReadOnly {
		t.Fatalf("archived gate = %v", res.Structured)
	}
	res = call(t, host, "note_tool", map[string]any{"value": "x", "project_id": "missing", "idempotency_key": "a2"})
	if structuredCode(t, res) != CodeNotFound {
		t.Fatalf("missing project gate = %v", res.Structured)
	}
	huge := strings.Repeat("x", MaxBodyBytes+MaxBodyToolOverhead+1)
	res = call(t, host, "note_tool", map[string]any{"value": "x", "body_md": huge, "idempotency_key": "a3"})
	if structuredCode(t, res) != CodeLimitExceeded {
		t.Fatalf("huge body = %v", res.Structured)
	}
}

func TestRateLimit(t *testing.T) {
	provider := newFakeProvider(ModuleNotes, "note", CategoryRead, true)
	settings := &mapSettings{values: map[string]string{}}
	host, err := NewHost(HostOptions{
		Settings: settings, Broker: newMemBroker(), Jobs: newMemJobs(), Idempotency: newMemIdempotency(),
		Limiter: NewLimiter(1, 1, nil), Providers: []ToolProvider{provider},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res := call(t, host, "note_tool", map[string]any{"value": "x"}); res.IsError {
		t.Fatal("first call failed")
	}
	res := call(t, host, "note_tool", map[string]any{"value": "x"})
	if structuredCode(t, res) != CodeRateLimited {
		t.Fatalf("second call = %v", res.Structured)
	}
	if !strings.Contains(string(mustMarshal(t, res.Structured)), "retry_after_ms") {
		t.Fatal("RATE_LIMITED lacks retry_after_ms")
	}
}

func structuredMap(t *testing.T, v any) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(mustMarshal(t, v), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func structuredCode(t *testing.T, res CallResult) Code {
	t.Helper()
	body := structuredMap(t, res.Structured)
	inner, _ := body["error"].(map[string]any)
	code, _ := inner["code"].(string)
	return Code(code)
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
