package output_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"researchos/internal/ai/aitest"
	"researchos/internal/ai/gateway"
	"researchos/internal/ai/output"
	"researchos/internal/ai/protocol"
	"researchos/internal/ai/provider"
	"researchos/internal/ai/role"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
)

type fakeRunner struct {
	contents []string
	calls    []gateway.ChatRequest
	summary  gateway.ProviderSummary
	err      error
}

func (fr *fakeRunner) Chat(_ context.Context, _ role.Role, req gateway.ChatRequest) (gateway.ChatResponse, gateway.ProviderSummary, error) {
	fr.calls = append(fr.calls, req)
	if fr.err != nil {
		return gateway.ChatResponse{}, gateway.ProviderSummary{}, fr.err
	}
	content := `{"answer":42}`
	if len(fr.contents) > 0 {
		content = fr.contents[0]
		fr.contents = fr.contents[1:]
	}
	return gateway.ChatResponse{
		Content: content, Model: "chat-m",
		Usage:    gateway.Usage{InputTokens: 10, OutputTokens: 5},
		Attempts: 1,
	}, fr.summary, nil
}

func baseSummary() gateway.ProviderSummary {
	return gateway.ProviderSummary{
		ProviderID: ids.MustNew(nil), ProviderName: "fake-oa",
		Protocol: provider.ProtocolOpenAIChat, ModelID: "chat-m",
	}
}

func proj(t *testing.T) (*foundationtest.Fixture, *sql.DB, string) {
	t.Helper()
	f := aitest.FixtureWithProject(t)
	return f, f.Project.DB(), f.ProjectID
}

func newOutputs(t *testing.T, f *foundationtest.Fixture, projectID string, readOnly func(context.Context) error) output.AIOutputService {
	t.Helper()
	svc, err := output.NewOutputService(f.Project.DB(), output.OutputOptions{
		ProjectID: projectID, Clock: f.Clock, ReadOnly: readOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func msg(text string) protocol.ChatMessage {
	return protocol.ChatMessage{Role: protocol.RoleUser, Content: text}
}

func activityQuery(evType string) events.Query {
	return events.Query{EventTypes: []schema.EventType{schema.EventType(evType)}}
}

func draftInput(projectID string) output.CreateDraftInput {
	return output.CreateDraftInput{
		ProjectID: projectID, TaskKind: "weekly_report",
		ContentMD: "# draft",
		Provenance: output.Provenance{
			ProviderID: ids.MustNew(nil), ProviderName: "p", Protocol: provider.ProtocolOpenAIChat,
			ModelID: "m", PromptVersion: "v1",
			SourceRefs: []output.SourceRef{{EntityType: "note", EntityID: ids.MustNew(nil), Locator: "lines:10-20", Title: "N"}},
		},
	}
}

func TestDraftValidationAndProvenance(t *testing.T) {
	f, _, projectID := proj(t)
	ctx := context.Background()
	svc := newOutputs(t, f, projectID, nil)

	in := draftInput(projectID)
	in.Provenance.PromptVersion = ""
	if _, err := svc.CreateDraft(ctx, in); !errors.Is(err, output.ErrInvalidPromptVersion) {
		t.Fatalf("empty prompt version: %v", err)
	}
	in = draftInput(projectID)
	in.TaskKind = "mystery_task"
	if _, err := svc.CreateDraft(ctx, in); !errors.Is(err, output.ErrUnknownTaskKind) {
		t.Fatalf("unknown kind: %v", err)
	}
	in = draftInput(projectID)
	in.TaskKind = "custom_kind"
	output.RegisterTaskKinds("custom_kind")
	if _, err := svc.CreateDraft(ctx, in); err != nil {
		t.Fatalf("registered kind: %v", err)
	}
	in = draftInput(projectID)
	in.Entity = &output.EntityRef{Type: "note", ID: "not-a-uuid"}
	if _, err := svc.CreateDraft(ctx, in); !errors.Is(err, output.ErrInvalidEntity) {
		t.Fatalf("bad entity id: %v", err)
	}
	in = draftInput(projectID)
	in.Provenance.Protocol = "carrier-pigeon"
	if _, err := svc.CreateDraft(ctx, in); !errors.Is(err, provider.ErrInvalidProtocol) {
		t.Fatalf("bad protocol: %v", err)
	}
	in = draftInput(projectID)
	in.Entity = &output.EntityRef{Type: "weekly_report", ID: ids.MustNew(nil)}
	if _, err := svc.CreateDraft(ctx, in); err != nil {
		t.Fatal(err)
	}

	// Round-trip: status draft, refs decoded, project binding.
	list, err := svc.ListByEntity(ctx, *in.Entity)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v %v", list, err)
	}
	o := list[0]
	if o.Status != output.OutputDraft || o.ConfirmedAt != nil || o.HumanEdited {
		t.Fatalf("draft shape = %+v", o)
	}
	if len(o.Provenance.SourceRefs) != 1 || o.Provenance.SourceRefs[0].Locator != "lines:10-20" {
		t.Fatalf("refs = %+v", o.Provenance.SourceRefs)
	}
	if o.ProjectID != projectID {
		t.Fatalf("project binding = %q", o.ProjectID)
	}
	// Cross-project draft rejected.
	wrong := draftInput("other-project")
	if _, err := svc.CreateDraft(ctx, wrong); err == nil {
		t.Fatal("cross-project draft accepted")
	}
}

func TestStateMachineAndConfirmPermissions(t *testing.T) {
	f, _, projectID := proj(t)
	ctx := context.Background()
	svc := newOutputs(t, f, projectID, nil)
	o, err := svc.CreateDraft(ctx, draftInput(projectID))
	if err != nil {
		t.Fatal(err)
	}

	// AI/MCP cannot confirm (AC-06.1).
	if _, err := svc.Confirm(ctx, o.ID, output.ActorMCP); !errors.Is(err, output.ErrConfirmationRequiresUser) {
		t.Fatalf("mcp confirm: %v", err)
	}
	if _, err := svc.Confirm(ctx, o.ID, output.ActorAI); !errors.Is(err, output.ErrConfirmationRequiresUser) {
		t.Fatalf("ai confirm: %v", err)
	}
	// Draft edits by mcp/ai allowed; human_edited only for user (§5.9 table).
	edited, err := svc.UpdateContent(ctx, o.ID, "draft v2", output.ActorMCP)
	if err != nil || edited.HumanEdited {
		t.Fatalf("mcp draft edit: %+v %v", edited, err)
	}
	edited, err = svc.UpdateContent(ctx, o.ID, "draft v3", output.ActorUser)
	if err != nil || !edited.HumanEdited {
		t.Fatalf("user draft edit: %+v %v", edited, err)
	}
	if edited.Status != output.OutputDraft {
		t.Fatal("edit must not confirm")
	}

	// User confirm flips status + confirmed_at (AC-06.2).
	confirmed, err := svc.Confirm(ctx, o.ID, output.ActorUser)
	if err != nil || confirmed.Status != output.OutputConfirmed || confirmed.ConfirmedAt == nil {
		t.Fatalf("confirm: %+v %v", confirmed, err)
	}
	// Idempotent re-confirm.
	again, err := svc.Confirm(ctx, o.ID, output.ActorUser)
	if err != nil || again.Status != output.OutputConfirmed {
		t.Fatalf("reconfirm: %+v %v", again, err)
	}
	// AI cannot rewrite confirmed content (AC-06.3); user can, staying
	// confirmed with human_edited.
	if _, err := svc.UpdateContent(ctx, o.ID, "hijack", output.ActorAI); !errors.Is(err, output.ErrConfirmedImmutableForMachine) {
		t.Fatalf("ai edit confirmed: %v", err)
	}
	edited, err = svc.UpdateContent(ctx, o.ID, "user polish", output.ActorUser)
	if err != nil || edited.Status != output.OutputConfirmed || !edited.HumanEdited {
		t.Fatalf("user edit confirmed: %+v %v", edited, err)
	}
	// No path downgrades confirmed back to draft (AC-06.5).
	var status, confirmedAt string
	f.Project.DB().QueryRow(`SELECT status, confirmed_at FROM ai_outputs WHERE id = ?`, o.ID).Scan(&status, &confirmedAt)
	if status != "confirmed" || confirmedAt == "" {
		t.Fatalf("downgrade detected: %s %q", status, confirmedAt)
	}
}

func TestConfirmTxAtomicity(t *testing.T) {
	f, db, projectID := proj(t)
	ctx := context.Background()
	svc := newOutputs(t, f, projectID, nil)
	o, err := svc.CreateDraft(ctx, draftInput(projectID))
	if err != nil {
		t.Fatal(err)
	}

	// Failure inside the same transaction rolls the confirm back.
	err = f.Project.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := svc.ConfirmTx(ctx, tx, o.ID, output.ActorUser); err != nil {
			return err
		}
		return errors.New("module side failed")
	})
	if err == nil {
		t.Fatal("expected module failure")
	}
	var status string
	db.QueryRow(`SELECT status FROM ai_outputs WHERE id = ?`, o.ID).Scan(&status)
	if status != "draft" {
		t.Fatalf("confirm leaked after rollback: %s", status)
	}

	// Success path: output flips inside the caller tx.
	if err := f.Project.InTx(ctx, func(tx *sql.Tx) error {
		_, err := svc.ConfirmTx(ctx, tx, o.ID, output.ActorUser)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT status FROM ai_outputs WHERE id = ?`, o.ID).Scan(&status)
	if status != "confirmed" {
		t.Fatalf("confirm tx: %s", status)
	}
	if _, err := svc.ConfirmTx(ctx, nil, o.ID, output.ActorUser); err == nil {
		t.Fatal("ConfirmTx requires a transaction")
	}
}

func TestArchivedProjectReadOnly(t *testing.T) {
	f, _, projectID := proj(t)
	ctx := context.Background()
	readOnly := output.NewProjectReadOnly(f.Global().DB(), projectID)
	svc := newOutputs(t, f, projectID, readOnly)
	o, err := svc.CreateDraft(ctx, draftInput(projectID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Global().DB().Exec(`UPDATE projects SET status = 'archived' WHERE id = ?`, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateDraft(ctx, draftInput(projectID)); !errors.Is(err, output.ErrProjectReadOnly) {
		t.Fatalf("create on archived: %v", err)
	}
	if _, err := svc.Confirm(ctx, o.ID, output.ActorUser); !errors.Is(err, output.ErrProjectReadOnly) {
		t.Fatalf("confirm on archived: %v", err)
	}
	if _, err := svc.UpdateContent(ctx, o.ID, "x", output.ActorUser); !errors.Is(err, output.ErrProjectReadOnly) {
		t.Fatalf("edit on archived: %v", err)
	}
	// Reads stay allowed on archived projects (§5.9 rule 6).
	if _, err := svc.Get(ctx, o.ID); err != nil {
		t.Fatalf("get on archived: %v", err)
	}
}

func TestGenerateDraftEventsAndProvenance(t *testing.T) {
	f, _, projectID := proj(t)
	ctx := context.Background()
	out := newOutputs(t, f, projectID, nil)
	run := &fakeRunner{contents: []string{"evidence draft"}, summary: baseSummary()}
	eventsW := f.Project.Events()

	ent := &output.EntityRef{Type: "idea", ID: ids.MustNew(nil)}
	svc, err := output.NewGenerationService(output.GenerationOptions{
		Outputs: out, Runner: run, Events: eventsW,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Empty prompt version is rejected before any call (AC-05.3).
	bad := output.GenerateRequest{ProjectID: projectID, TaskKind: "weekly_report", Entity: ent}
	if _, err := svc.Generate(ctx, bad); !errors.Is(err, output.ErrInvalidPromptVersion) {
		t.Fatalf("empty prompt version: %v", err)
	}
	if len(run.calls) != 0 {
		t.Fatal("runner hit before validation")
	}

	req := output.GenerateRequest{
		ProjectID: projectID, TaskKind: "evidence_extraction", PromptVersion: "ev-v2",
		Messages:   []protocol.ChatMessage{msg("extract")},
		SourceRefs: []output.SourceRef{{EntityType: "note", EntityID: ids.MustNew(nil), Title: "src"}},
		Entity:     ent,
	}
	res, err := svc.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Output.Status != output.OutputDraft || res.Output.ContentMD != "evidence draft" {
		t.Fatalf("output = %+v", res.Output)
	}
	// Provenance comes from the actual call, never the caller (AC-05.2).
	if res.Output.Provenance.ProviderName != "fake-oa" || res.Output.Provenance.Protocol != provider.ProtocolOpenAIChat ||
		res.Output.Provenance.ModelID != "chat-m" || res.Output.Provenance.PromptVersion != "ev-v2" {
		t.Fatalf("provenance = %+v", res.Output.Provenance)
	}
	if res.Output.Usage == nil || res.Output.Usage.InputTokens != 10 {
		t.Fatalf("usage = %+v", res.Output.Usage)
	}

	// Bound entity → exactly one ai.analysis_completed with actor=ai (AC-05.4).
	acts, err := f.Project.Reader().List(ctx, activityQuery("ai.analysis_completed"))
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 {
		t.Fatalf("activities = %+v", acts)
	}
	if acts[0].Actor != schema.ActorAI || acts[0].EntityID != ent.ID {
		t.Fatalf("event = %+v", acts[0])
	}
	if !strings.Contains(acts[0].Payload.Summary, "fake-oa") {
		t.Fatalf("summary = %q", acts[0].Payload.Summary)
	}

	// Unbound generation writes no event.
	run.contents = []string{"scratch"}
	if _, err := svc.Generate(ctx, output.GenerateRequest{
		ProjectID: projectID, TaskKind: "other", PromptVersion: "v1",
		Messages: []protocol.ChatMessage{msg("x")},
	}); err != nil {
		t.Fatal(err)
	}
	acts, _ = f.Project.Reader().List(ctx, activityQuery("ai.analysis_completed"))
	if len(acts) != 1 {
		t.Fatalf("unbound emitted events: %+v", acts)
	}

	// Regeneration appends a new draft; history stays (AC-06.4).
	f.Clock.Advance(1000 * time.Millisecond) // newest-first needs distinct stamps
	run.contents = []string{"second attempt"}
	if _, err := svc.Generate(ctx, req); err != nil {
		t.Fatal(err)
	}
	list, _ := out.ListByEntity(ctx, *ent)
	if len(list) != 2 || list[0].ContentMD != "second attempt" {
		t.Fatalf("history = %+v", list)
	}
	// One more bound event for the second attempt.
	acts, _ = f.Project.Reader().List(ctx, activityQuery("ai.analysis_completed"))
	if len(acts) != 2 {
		t.Fatalf("regeneration events = %d", len(acts))
	}

	// Upstream failure propagates without a draft.
	run.err = errors.New("ai: auth: dead")
	if _, err := svc.Generate(ctx, req); err == nil {
		t.Fatal("expected gateway error")
	}
}

func TestOutputPersistErrorKeepsContent(t *testing.T) {
	_, _, projectID := proj(t)
	ctx := context.Background()
	run := &fakeRunner{contents: []string{"precious generated text"}, summary: baseSummary()}
	svc, err := output.NewGenerationService(output.GenerationOptions{Outputs: &failOutputs{}, Runner: run})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Generate(ctx, output.GenerateRequest{
		ProjectID: projectID, TaskKind: "weekly_report", PromptVersion: "wr-v1",
		Messages: []protocol.ChatMessage{msg("summarize")},
	})
	var pe *output.OutputPersistError
	if !errors.As(err, &pe) {
		t.Fatalf("persist error shape: %v", err)
	}
	if pe.Input.ContentMD != "precious generated text" || pe.Input.Provenance.PromptVersion != "wr-v1" {
		t.Fatalf("input lost: %+v", pe.Input)
	}
}

type failOutputs struct{}

func (f *failOutputs) CreateDraft(context.Context, output.CreateDraftInput) (output.AIOutput, error) {
	return output.AIOutput{}, errors.New("database is locked")
}
func (f *failOutputs) Get(context.Context, string) (output.AIOutput, error) {
	return output.AIOutput{}, nil
}
func (f *failOutputs) ListByEntity(context.Context, output.EntityRef) ([]output.AIOutput, error) {
	return nil, nil
}
func (f *failOutputs) UpdateContent(context.Context, string, string, output.Actor) (output.AIOutput, error) {
	return output.AIOutput{}, nil
}
func (f *failOutputs) Confirm(context.Context, string, output.Actor) (output.AIOutput, error) {
	return output.AIOutput{}, nil
}
func (f *failOutputs) ConfirmTx(context.Context, *sql.Tx, string, output.Actor) (output.AIOutput, error) {
	return output.AIOutput{}, nil
}

func TestCompleteStructuredValidationAndRepair(t *testing.T) {
	f, db, projectID := proj(t)
	ctx := context.Background()
	out := newOutputs(t, f, projectID, nil)
	schemaJSON := json.RawMessage(`{"type":"object","required":["answer"]}`)
	answerErr := func(_ json.RawMessage) error { return errors.New("answer must be 42") }

	// 1) Clean JSON with fences parses.
	run := &fakeRunner{contents: []string{"```json\n{\"answer\":42}\n```"}, summary: baseSummary()}
	svc, _ := output.NewGenerationService(output.GenerationOptions{Outputs: out, Runner: run})
	sres, err := svc.CompleteStructured(ctx, output.StructuredRequest{
		ProjectID: projectID, TaskKind: "evidence_extraction", PromptVersion: "ev-v1",
		Messages: []protocol.ChatMessage{msg("extract")}, JSONSchema: schemaJSON, SchemaName: "evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(sres.JSON) != `{"answer":42}` {
		t.Fatalf("json = %s", sres.JSON)
	}
	// Native structured output requested (OpenAI-style default probe).
	if run.calls[0].ResponseFormat == nil || run.calls[0].ResponseFormat.Type != "json_schema" {
		t.Fatalf("format = %+v", run.calls[0].ResponseFormat)
	}
	if sres.Output.Status != output.OutputDraft || sres.Output.ContentMD != `{"answer":42}` {
		t.Fatalf("draft = %+v", sres.Output)
	}

	// 2) Repair round: first reply invalid, corrected after one repair
	//    prompt; exactly two calls; one draft row.
	run2 := &fakeRunner{contents: []string{"Sure! The answer is 42.", `{"answer":42,"extra":true}`}, summary: baseSummary()}
	svc2, _ := output.NewGenerationService(output.GenerationOptions{Outputs: out, Runner: run2})
	res2, err := svc2.CompleteStructured(ctx, output.StructuredRequest{
		ProjectID: projectID, TaskKind: "evidence_extraction", PromptVersion: "ev-v1",
		Messages: []protocol.ChatMessage{msg("extract")}, JSONSchema: schemaJSON, SchemaName: "evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(run2.calls) != 2 {
		t.Fatalf("calls = %d", len(run2.calls))
	}
	hist := run2.calls[1].Messages
	if string(hist[len(hist)-2].Role) != "assistant" || !strings.Contains(hist[len(hist)-1].Content, "failed validation") {
		t.Fatalf("repair history = %+v", hist)
	}
	if res2.Attempts != 2 {
		t.Fatalf("attempts = %+v", res2)
	}

	// 3) Persistent garbage: no draft row, error visible, capped at
	//    1+RepairAttempts calls — never an unbounded loop.
	before := countRows(t, db)
	run3 := &fakeRunner{contents: []string{"nope", "still nope", "third nope"}, summary: baseSummary()}
	svc3, _ := output.NewGenerationService(output.GenerationOptions{Outputs: out, Runner: run3})
	_, err = svc3.CompleteStructured(ctx, output.StructuredRequest{
		ProjectID: projectID, TaskKind: "evidence_extraction", PromptVersion: "ev-v1",
		Messages: []protocol.ChatMessage{msg("extract")}, JSONSchema: schemaJSON, SchemaName: "evidence",
		Validate: answerErr, RepairAttempts: 1,
	})
	if !errors.Is(err, output.ErrOutputValidation) {
		t.Fatalf("validation failure: %v", err)
	}
	if len(run3.calls) != 2 {
		t.Fatalf("retry storm: %d calls", len(run3.calls))
	}
	if countRows(t, db) != before {
		t.Fatal("failed structured run wrote a draft")
	}

	// 4) Missing schema rejected before the network.
	run4 := &fakeRunner{summary: baseSummary()}
	svc4, _ := output.NewGenerationService(output.GenerationOptions{Outputs: out, Runner: run4})
	if _, err := svc4.CompleteStructured(ctx, output.StructuredRequest{
		ProjectID: projectID, TaskKind: "other", PromptVersion: "v1",
	}); !errors.Is(err, output.ErrSchemaRequired) {
		t.Fatalf("no schema: %v", err)
	}
	if len(run4.calls) != 0 {
		t.Fatal("network hit without schema")
	}
}

func TestCompleteStructuredAnthropicPromptFallback(t *testing.T) {
	f, _, projectID := proj(t)
	ctx := context.Background()
	out := newOutputs(t, f, projectID, nil)
	run := &fakeRunner{contents: []string{`{"answer":42}`}, summary: baseSummary()}
	svc, _ := output.NewGenerationService(output.GenerationOptions{
		Outputs: out, Runner: run,
		ProtocolProbe: func(context.Context) (provider.Protocol, error) { return provider.ProtocolAnthropic, nil },
	})
	if _, err := svc.CompleteStructured(ctx, output.StructuredRequest{
		ProjectID: projectID, TaskKind: "experiment_results", PromptVersion: "rs-v3",
		Messages: []protocol.ChatMessage{msg("summarize")}, JSONSchema: json.RawMessage(`{"type":"object"}`),
	}); err != nil {
		t.Fatal(err)
	}
	msgs := run.calls[0].Messages
	if string(msgs[0].Role) != "system" || !strings.Contains(msgs[0].Content, "JSON") {
		t.Fatalf("prompt constraint missing: %+v", msgs)
	}
	if run.calls[0].ResponseFormat != nil {
		t.Fatal("anthropic path must not request native json_schema")
	}
}

func countRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ai_outputs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
