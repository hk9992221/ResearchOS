package weeklyreview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	aimig "researchos/internal/ai/migrations"
	aioutput "researchos/internal/ai/output"
	"researchos/internal/ai/provider"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/settings"
	"researchos/internal/foundation/storage"
	"researchos/internal/foundation/trash"
	"researchos/internal/foundation/workspace"
	ideasmig "researchos/internal/ideas/migrations"
	kanbanmig "researchos/internal/kanban/migrations"
	notesmig "researchos/internal/notes/migrations"
)

// Deterministic UUIDv7 identifiers keep goldens reproducible.
const (
	testProjectID    = "0192f2a0-0000-7000-8000-000000000001"
	testNotePaper    = "0192f2a0-0000-7000-8000-0000000000a1"
	testNoteScratch  = "0192f2a0-0000-7000-8000-0000000000a2"
	testNoteDeleted  = "0192f2a0-0000-7000-8000-0000000000a3"
	testTaskDone     = "0192f2a0-0000-7000-8000-0000000000b1"
	testTaskBlocked  = "0192f2a0-0000-7000-8000-0000000000b2"
	testTaskArchived = "0192f2a0-0000-7000-8000-0000000000b3"
	testTaskPlanA    = "0192f2a0-0000-7000-8000-0000000000b4"
	testTaskPlanB    = "0192f2a0-0000-7000-8000-0000000000b5"
	testTaskPlanC    = "0192f2a0-0000-7000-8000-0000000000b6"
	testIdea         = "0192f2a0-0000-7000-8000-0000000000c1"
	testEvidence     = "0192f2a0-0000-7000-8000-0000000000c2"
	testExperiment   = "0192f2a0-0000-7000-8000-0000000000d1"
	testRun          = "0192f2a0-0000-7000-8000-0000000000d2"
	testSnapshot     = "0192f2a0-0000-7000-8000-0000000000d3"
	testReportID     = "0192f2a0-0000-7000-8000-0000000000e1"
	testPaper        = "0192f2a0-0000-7000-8000-0000000000f1"
)

// mutableClock is the injectable clock used by every fixture.
type mutableClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *mutableClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *mutableClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type envConfig struct {
	now         time.Time
	projectName string
	archived    bool
	settings    map[string]string
	ai          aioutput.GenerationService
	aiFactory   func(aioutput.AIOutputService, clock.Clock) aioutput.GenerationService
	wrapOutputs func(aioutput.AIOutputService) aioutput.AIOutputService
}

type testEnv struct {
	t         *testing.T
	ctx       context.Context
	ws        *workspace.Workspace
	handle    *workspace.ProjectHandle
	svc       Service
	clock     *mutableClock
	outputs   aioutput.AIOutputService
	projectID string
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return loc
}

func at(t *testing.T, loc *time.Location, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02 15:04", value, loc)
	if err != nil {
		t.Fatalf("parse time %s: %v", value, err)
	}
	return parsed
}

func dateAt(t *testing.T, loc *time.Location, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation(dateLayout, value, loc)
	if err != nil {
		t.Fatalf("parse date %s: %v", value, err)
	}
	return parsed
}

func periodOf(t *testing.T, loc *time.Location, start, endInclusive string) Period {
	t.Helper()
	from := dateAt(t, loc, start)
	to := dateAt(t, loc, endInclusive)
	return Period{Start: from, EndExclusive: to.AddDate(0, 0, 1)}
}

func mergeMigrations(t *testing.T, sets ...map[storage.Kind][]storage.Migration) map[storage.Kind][]storage.Migration {
	t.Helper()
	out := map[storage.Kind][]storage.Migration{}
	for _, set := range sets {
		for kind, list := range set {
			out[kind] = append(out[kind], list...)
		}
	}
	return out
}

func newTestEnv(t *testing.T, cfg envConfig) *testEnv {
	t.Helper()
	if cfg.now.IsZero() {
		cfg.now = time.Date(2026, 11, 11, 12, 0, 0, 0, mustLocation(t, "Asia/Shanghai"))
	}
	if cfg.projectName == "" {
		cfg.projectName = "Alpha"
	}
	clk := &mutableClock{t: cfg.now}
	root := t.TempDir()
	migrations := mergeMigrations(t,
		aimig.MustAll(), kanbanmig.MustAll(), notesmig.MustAll(), ideasmig.MustAll(),
	)
	ws, err := workspace.Init(context.Background(), root, workspace.Options{
		Clock:      clk,
		SkipBackup: true,
		BackupRoot: t.TempDir(),
		Migrations: migrations,
	})
	if err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })

	now := clock.Format(clk.Now())
	status := string(schema.ProjectActive)
	if cfg.archived {
		status = string(schema.ProjectArchived)
	}
	if _, err := ws.Global().DB().Exec(
		`INSERT INTO projects(id, name, summary, goals, status, created_at, updated_at)
		 VALUES (?, ?, 'summary', 'goals', ?, ?, ?)`,
		testProjectID, cfg.projectName, status, now, now); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := ws.CreateProjectData(context.Background(), testProjectID); err != nil {
		t.Fatalf("create project data: %v", err)
	}
	handle, err := ws.Project(context.Background(), testProjectID)
	if err != nil {
		t.Fatalf("open project: %v", err)
	}

	settingsStore := settings.NewWorkspace(ws.Global().DB()).WithClock(clk)
	for key, value := range cfg.settings {
		if err := ws.Global().InTx(context.Background(), func(tx *sql.Tx) error {
			return settingsStore.Set(context.Background(), tx, key, value)
		}); err != nil {
			t.Fatalf("seed setting %s: %v", key, err)
		}
	}

	outputs, err := aioutput.NewOutputService(handle.DB(), aioutput.OutputOptions{
		Clock:     clk,
		ProjectID: testProjectID,
	})
	if err != nil {
		t.Fatalf("output service: %v", err)
	}
	if cfg.wrapOutputs != nil {
		outputs = cfg.wrapOutputs(outputs)
	}
	if cfg.aiFactory != nil {
		cfg.ai = cfg.aiFactory(outputs, clk)
	}
	svc := New(ws, testProjectID, Options{
		Clock:    clk,
		AI:       cfg.ai,
		Outputs:  outputs,
		Trash:    trash.New(testProjectID, handle.DB(), ws.Paths(), handle.Events(), nil, clk),
		Settings: settingsStore,
	})
	return &testEnv{
		t:         t,
		ctx:       context.Background(),
		ws:        ws,
		handle:    handle,
		svc:       svc,
		clock:     clk,
		outputs:   outputs,
		projectID: testProjectID,
	}
}

func (e *testEnv) emit(ev events.Event) {
	e.t.Helper()
	if err := e.handle.InTx(e.ctx, func(tx *sql.Tx) error {
		return e.handle.Events().Emit(e.ctx, tx, ev)
	}); err != nil {
		e.t.Fatalf("emit %s: %v", ev.Type, err)
	}
}

func (e *testEnv) emitNoteUpdated(ev events.NoteUpdatedEvent) {
	e.t.Helper()
	if err := e.handle.InTx(e.ctx, func(tx *sql.Tx) error {
		return e.handle.Events().EmitNoteUpdated(e.ctx, tx, ev)
	}); err != nil {
		e.t.Fatalf("emit note.updated: %v", err)
	}
}

func (e *testEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.handle.DB().Exec(query, args...); err != nil {
		e.t.Fatalf("exec fixture: %v", err)
	}
}

// execGlobal writes the workspace global database (settings, projects).
func (e *testEnv) execGlobal(query string, args ...any) {
	e.t.Helper()
	if _, err := e.ws.Global().DB().Exec(query, args...); err != nil {
		e.t.Fatalf("exec global fixture: %v", err)
	}
}

func (e *testEnv) seedNote(id, kind, title string, deleted bool) {
	e.t.Helper()
	var deletedAt any
	if deleted {
		deletedAt = clock.Format(e.clock.Now())
	}
	e.exec(`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at, deleted_at)
	        VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, title, "notes/"+id+".md", kind, clock.Format(e.clock.Now()),
		clock.Format(e.clock.Now()), deletedAt)
}

func (e *testEnv) seedColumn(name, kind string, isDone bool, position int) {
	e.t.Helper()
	done := 0
	if isDone {
		done = 1
	}
	now := clock.Format(e.clock.Now())
	e.exec(`INSERT INTO task_columns(name, kind, is_done, position, created_at, updated_at)
	        VALUES (?, ?, ?, ?, ?, ?)`, name, kind, done, position, now, now)
}

type taskSeed struct {
	ID        string
	Title     string
	Column    string
	Blocked   bool
	WeekPlan  string
	Deadline  string
	Archived  bool
	Completed bool
	Deleted   bool
}

func (e *testEnv) seedTask(seed taskSeed) {
	e.t.Helper()
	blocked := 0
	if seed.Blocked {
		blocked = 1
	}
	var weekPlan, deadline, archivedAt, completedAt, deletedAt any
	if seed.WeekPlan != "" {
		weekPlan = seed.WeekPlan
	}
	if seed.Deadline != "" {
		deadline = seed.Deadline
	}
	now := clock.Format(e.clock.Now())
	if seed.Archived {
		archivedAt = now
	}
	if seed.Completed {
		completedAt = now
	}
	if seed.Deleted {
		deletedAt = now
	}
	e.exec(`INSERT INTO tasks(id, title, "column", blocked, week_plan, deadline,
	         created_at, updated_at, archived_at, completed_at, deleted_at)
	        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		seed.ID, seed.Title, seed.Column, blocked, weekPlan, deadline, now, now,
		archivedAt, completedAt, deletedAt)
}

// insertRawActivity bypasses the events writer so fixtures can carry an
// unregistered entity_type (AC-24) that the registry would otherwise reject.
func (e *testEnv) insertRawActivity(id, eventType, entityType, entityID, summary string, at time.Time) {
	e.t.Helper()
	payload := fmt.Sprintf(`{"entity_type":%q,"entity_id":%q,"summary":%q,"actor":"user"}`,
		entityType, entityID, summary)
	e.exec(`INSERT INTO activities(id, event_type, entity_type, entity_id, payload, actor, created_at)
	        VALUES (?, ?, ?, ?, ?, 'user', ?)`,
		id, eventType, entityType, entityID, payload, clock.Format(at))
}

func (e *testEnv) activityCount() int {
	e.t.Helper()
	n, err := e.handle.Reader().Count(e.ctx, events.Query{})
	if err != nil {
		e.t.Fatalf("count activities: %v", err)
	}
	return n
}

func (e *testEnv) eventsOfType(eventType schema.EventType) []events.Activity {
	e.t.Helper()
	list, err := e.handle.Reader().List(e.ctx, events.Query{EventTypes: []events.Type{eventType}, Limit: 500})
	if err != nil {
		e.t.Fatalf("list %s: %v", eventType, err)
	}
	return list
}

func (e *testEnv) reportRow(id string) (status string, content string, meta string) {
	e.t.Helper()
	if err := e.handle.DB().QueryRow(
		`SELECT status, content_md, meta FROM weekly_reports WHERE id = ?`, id).
		Scan(&status, &content, &meta); err != nil {
		e.t.Fatalf("read report row: %v", err)
	}
	return status, content, meta
}

func (e *testEnv) aiOutputStatus(id string) string {
	e.t.Helper()
	var status string
	if err := e.handle.DB().QueryRow(`SELECT status FROM ai_outputs WHERE id = ?`, id).Scan(&status); err != nil {
		e.t.Fatalf("read ai_output status: %v", err)
	}
	return status
}

// fakeResponse is one scripted GenerationService answer.
type fakeResponse struct {
	content string
	err     error
}

// fakeAI implements output.GenerationService with real ai_outputs drafts.
type fakeAI struct {
	mu        sync.Mutex
	outputs   aioutput.AIOutputService
	projectID string
	clock     clock.Clock
	responses []fakeResponse
	calls     int
	requests  []aioutput.GenerateRequest
	// emit stands in for SPEC-02's ai.analysis_completed write.
	emit func(aioutput.AIOutput)
}

func newFakeAI(outputs aioutput.AIOutputService, clk clock.Clock, responses ...fakeResponse) *fakeAI {
	return &fakeAI{outputs: outputs, projectID: testProjectID, clock: clk, responses: responses}
}

func (f *fakeAI) Generate(ctx context.Context, req aioutput.GenerateRequest) (aioutput.GenerateResult, error) {
	f.mu.Lock()
	if f.calls >= len(f.responses) {
		f.mu.Unlock()
		return aioutput.GenerateResult{}, errors.New("fake ai: no response scripted")
	}
	resp := f.responses[f.calls]
	f.calls++
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if resp.err != nil {
		return aioutput.GenerateResult{}, resp.err
	}
	out, err := f.outputs.CreateDraft(ctx, aioutput.CreateDraftInput{
		ProjectID: f.projectID,
		TaskKind:  req.TaskKind,
		Entity:    req.Entity,
		ContentMD: resp.content,
		Provenance: aioutput.Provenance{
			ProviderID:    "provider-1",
			ProviderName:  "Fake Provider",
			Protocol:      provider.ProtocolOpenAIChat,
			ModelID:       "fake-model",
			PromptVersion: req.PromptVersion,
			GeneratedAt:   f.clock.Now(),
		},
	})
	if err != nil {
		return aioutput.GenerateResult{}, err
	}
	if f.emit != nil {
		f.emit(out)
	}
	return aioutput.GenerateResult{Output: out, Attempts: 1}, nil
}

func (f *fakeAI) lastRequest() (aioutput.GenerateRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return aioutput.GenerateRequest{}, false
	}
	return f.requests[len(f.requests)-1], true
}

func (f *fakeAI) CompleteStructured(context.Context, aioutput.StructuredRequest) (aioutput.StructuredResponse, error) {
	return aioutput.StructuredResponse{}, errors.New("fake ai: structured generation is not used")
}

func (f *fakeAI) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// failingOutputs confirms through the real path and then fails, so a caller
// transaction rolls both the report and the output back (AC-28).
type failingOutputs struct {
	aioutput.AIOutputService
}

func (f failingOutputs) ConfirmTx(ctx context.Context, tx *sql.Tx, id string, actor aioutput.Actor) (aioutput.AIOutput, error) {
	if _, err := f.AIOutputService.ConfirmTx(ctx, tx, id, actor); err != nil {
		return aioutput.AIOutput{}, err
	}
	return aioutput.AIOutput{}, errors.New("injected confirmation failure")
}

func readGolden(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return string(raw)
}

func writeGolden(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatalf("mkdir testdata: %v", err)
	}
	if err := os.WriteFile(filepath.Join("testdata", name), []byte(content), 0o644); err != nil {
		t.Fatalf("write golden %s: %v", name, err)
	}
}
