package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

// mapSettings is an in-memory SettingStore.
type mapSettings struct {
	mu     sync.Mutex
	values map[string]string
}

func (m *mapSettings) Get(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[key]
	return v, ok, nil
}

func (m *mapSettings) set(key, value string) {
	m.mu.Lock()
	m.values[key] = value
	m.mu.Unlock()
}

// memBroker is an in-memory ConfirmationBroker.
type memBroker struct {
	mu   sync.Mutex
	rows map[string]Confirmation
	seq  int
}

func newMemBroker() *memBroker { return &memBroker{rows: map[string]Confirmation{}} }

func (b *memBroker) Enqueue(_ context.Context, p Principal, req ConfirmationRequest, args json.RawMessage, hash string) (Confirmation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	id := fmt.Sprintf("conf-%d", b.seq)
	ttl := req.TTL
	if ttl <= 0 {
		ttl = time.Minute
	}
	now := time.Now().UTC()
	conf := Confirmation{ID: id, Tool: req.Tool, Args: args, ArgsHash: hash, Title: req.Title,
		Summary: req.Summary, ProjectID: req.ProjectID, ClientID: p.ClientID,
		State: ConfirmationRequested, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	b.rows[id] = conf
	return conf, nil
}

func (b *memBroker) Get(_ context.Context, p Principal, id string) (Confirmation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	conf, ok := b.rows[id]
	if !ok || (p.ClientID != "" && conf.ClientID != p.ClientID) {
		return Confirmation{}, Errorf(CodeNotFound, "not found")
	}
	return conf, nil
}

func (b *memBroker) List(_ context.Context, _ Principal, _ ConfirmationFilter) ([]Confirmation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Confirmation, 0, len(b.rows))
	for _, row := range b.rows {
		out = append(out, row)
	}
	return out, nil
}

func (b *memBroker) CancelByClient(_ context.Context, p Principal, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	conf, ok := b.rows[id]
	if !ok || conf.ClientID != p.ClientID {
		return Errorf(CodeNotFound, "not found")
	}
	if conf.State != ConfirmationRequested {
		return Errorf(CodeConflict, "not cancellable")
	}
	conf.State = ConfirmationCancelled
	b.rows[id] = conf
	return nil
}

// memJobs is an in-memory JobRunner.
type memJobs struct {
	mu   sync.Mutex
	rows map[string]Job
	seq  int
}

func newMemJobs() *memJobs { return &memJobs{rows: map[string]Job{}} }

func (j *memJobs) Submit(_ context.Context, p Principal, req JobRequest) (Job, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	job := Job{ID: fmt.Sprintf("job-%d", j.seq), Tool: req.Tool, ProjectID: req.ProjectID,
		ClientID: p.ClientID, State: JobQueued, ReadOnly: req.ReadOnly, CreatedAt: time.Now().UTC()}
	j.rows[job.ID] = job
	return job, nil
}

func (j *memJobs) Get(_ context.Context, _ Principal, id string) (Job, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.rows[id]
	if !ok {
		return Job{}, Errorf(CodeNotFound, "job not found")
	}
	return job, nil
}

func (j *memJobs) List(_ context.Context, _ Principal, _ JobFilter) ([]Job, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Job, 0, len(j.rows))
	for _, row := range j.rows {
		out = append(out, row)
	}
	return out, nil
}

func (j *memJobs) Cancel(_ context.Context, _ Principal, id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.rows[id]
	if !ok {
		return Errorf(CodeNotFound, "job not found")
	}
	if job.State != JobQueued && job.State != JobRunning {
		return Errorf(CodeConflict, "job finished")
	}
	return nil
}

func (j *memJobs) RecoverAtStartup(context.Context) error        { return nil }
func (j *memJobs) Purge(context.Context, time.Time) (int, error) { return 0, nil }

// memIdempotency is an in-memory IdempotencyStore.
type memIdempotency struct {
	mu   sync.Mutex
	rows map[string]memIdemRow
}

type memIdemRow struct {
	tool, hash string
	done       bool
	result     json.RawMessage
}

func newMemIdempotency() *memIdempotency { return &memIdempotency{rows: map[string]memIdemRow{}} }

func (m *memIdempotency) Claim(_ context.Context, clientID, key, tool, argsHash string, _ time.Time) (IdempotencyClaim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := clientID + "\x00" + key
	row, ok := m.rows[id]
	if ok {
		if row.tool != tool || row.hash != argsHash {
			return IdempotencyClaim{}, IdempotencyConflict
		}
		if row.done {
			return IdempotencyClaim{Replayed: true, Result: row.result}, nil
		}
		return IdempotencyClaim{}, IdempotencyInFlight
	}
	m.rows[id] = memIdemRow{tool: tool, hash: argsHash}
	return IdempotencyClaim{}, nil
}

func (m *memIdempotency) Complete(_ context.Context, clientID, key string, result json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := clientID + "\x00" + key
	row := m.rows[id]
	row.done = true
	row.result = result
	m.rows[id] = row
	return nil
}

func (m *memIdempotency) Release(_ context.Context, clientID, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, clientID+"\x00"+key)
	return nil
}

func (m *memIdempotency) Purge(context.Context, time.Time) (int, error) { return 0, nil }

// fakeProvider is a controllable ToolProvider.
type fakeProvider struct {
	module ModuleID
	tools  []ToolBinding
	mu     sync.Mutex
	ready  error
	calls  int
}

func (p *fakeProvider) Module() ModuleID { return p.module }

func (p *fakeProvider) Ready(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ready
}

func (p *fakeProvider) Tools() []ToolBinding { return p.tools }

func (p *fakeProvider) setReady(err error) {
	p.mu.Lock()
	p.ready = err
	p.mu.Unlock()
}

func simpleSchema(required string) json.RawMessage {
	props := map[string]any{
		"project_id": map[string]any{"type": "string"},
		"value":      map[string]any{"type": "string"},
		"body_md":    map[string]any{"type": "string"},
	}
	if required == "" {
		body, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "additionalProperties": false})
		return body
	}
	body, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": []string{required}, "additionalProperties": false})
	return body
}

func newFakeProvider(module ModuleID, prefix string, category Category, readonly bool) *fakeProvider {
	schema := simpleSchema("value")
	handle := func(ctx context.Context, p Principal, in json.RawMessage) (ToolOutcome, error) {
		args, _ := DecodeArguments(in)
		v, _ := args.String("value")
		switch v {
		case "fail":
			return ToolOutcome{}, Errorf(CodeConflict, "fake conflict")
		case "panic":
			panic("boom")
		case "pending":
			return ToolOutcome{Status: StatusPending, Confirmation: &ConfirmationRequest{Title: "fake", Impact: map[string]any{"n": 1}, ProjectID: argsValue(args, "project_id")}}, nil
		case "job":
			return ToolOutcome{Status: StatusJob, JobID: "job-1"}, nil
		}
		return ToolOutcome{Status: StatusOK, Structured: map[string]any{"echo": v}}, nil
	}
	name := prefix + "_tool"
	return &fakeProvider{
		module: module,
		tools: []ToolBinding{{
			Spec: ToolSpec{
				Name:         name,
				Title:        name,
				Description:  "fake",
				InputSchema:  schema,
				OutputSchema: genericFakeOutput(),
				Annotations:  ToolAnnotations{ReadOnlyHint: readonly, IdempotentHint: true},
				Category:     category,
				Module:       module,
			},
			Handle: handle,
		}},
	}
}

func argsValue(a *Arguments, name string) string {
	v, _ := a.String(name)
	return v
}

func genericFakeOutput() json.RawMessage {
	body, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": true})
	return body
}

func newTestHost(t *testing.T, providers []ToolProvider, gate ProjectGate) *Host {
	t.Helper()
	settings := &mapSettings{values: map[string]string{}}
	host, err := NewHost(HostOptions{
		Settings:    settings,
		Broker:      newMemBroker(),
		Jobs:        newMemJobs(),
		Idempotency: newMemIdempotency(),
		Gate:        gate,
		Limiter:     NewLimiter(1000, 1000, nil),
		Notifier:    NewNotifier(),
		ServerName:  "test",
		Providers:   providers,
	})
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	return host
}
