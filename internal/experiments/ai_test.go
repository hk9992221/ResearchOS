package experiments

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"researchos/internal/ai/gateway"
	"researchos/internal/ai/migrations"
	"researchos/internal/ai/output"
	"researchos/internal/ai/provider"
	"researchos/internal/ai/role"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
)

// fakeOutputs is an in-memory SPEC-02 AIOutputService.
type fakeOutputs struct {
	mu   sync.Mutex
	rows map[string]output.AIOutput
}

func newFakeOutputs() *fakeOutputs { return &fakeOutputs{rows: map[string]output.AIOutput{}} }

func (f *fakeOutputs) CreateDraft(_ context.Context, in output.CreateDraftInput) (output.AIOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := output.AIOutput{
		ID:         ids.MustNew(nil),
		ProjectID:  in.ProjectID,
		TaskKind:   in.TaskKind,
		Entity:     in.Entity,
		ContentMD:  in.ContentMD,
		Status:     output.OutputDraft,
		Provenance: in.Provenance,
		CreatedAt:  time.Now().UTC(),
	}
	if row.Provenance.GeneratedAt.IsZero() {
		row.Provenance.GeneratedAt = row.CreatedAt
	}
	f.rows[row.ID] = row
	return row, nil
}

func (f *fakeOutputs) Get(_ context.Context, id string) (output.AIOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return output.AIOutput{}, output.ErrNotFound
	}
	return row, nil
}

func (f *fakeOutputs) ListByEntity(_ context.Context, ref output.EntityRef) ([]output.AIOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []output.AIOutput{}
	for _, row := range f.rows {
		if row.Entity != nil && row.Entity.Type == ref.Type && row.Entity.ID == ref.ID {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeOutputs) UpdateContent(_ context.Context, id, contentMD string, actor output.Actor) (output.AIOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return output.AIOutput{}, output.ErrNotFound
	}
	if row.Status == output.OutputConfirmed && actor != output.ActorUser {
		return output.AIOutput{}, output.ErrConfirmedImmutableForMachine
	}
	row.ContentMD = contentMD
	row.HumanEdited = true
	f.rows[id] = row
	return row, nil
}

func (f *fakeOutputs) Confirm(_ context.Context, id string, actor output.Actor) (output.AIOutput, error) {
	if actor != output.ActorUser {
		return output.AIOutput{}, output.ErrConfirmationRequiresUser
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return output.AIOutput{}, output.ErrNotFound
	}
	at := time.Now().UTC()
	row.Status = output.OutputConfirmed
	row.ConfirmedAt = &at
	f.rows[id] = row
	return row, nil
}

func (f *fakeOutputs) ConfirmTx(ctx context.Context, _ *sql.Tx, id string, actor output.Actor) (output.AIOutput, error) {
	return f.Confirm(ctx, id, actor)
}

// fakeGenerator is a deterministic SPEC-02 GenerationService.
type fakeGenerator struct {
	outputs  output.AIOutputService
	reply    func() string
	err      error
	during   func()
	mu       sync.Mutex
	calls    int
	requests []output.StructuredRequest
}

func (f *fakeGenerator) Generate(context.Context, output.GenerateRequest) (output.GenerateResult, error) {
	return output.GenerateResult{}, errors.New("not used")
}

func (f *fakeGenerator) CompleteStructured(ctx context.Context, req output.StructuredRequest) (output.StructuredResponse, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if f.err != nil {
		return output.StructuredResponse{}, f.err
	}
	if f.during != nil && call == 1 {
		f.during()
	}
	content := f.reply()
	if req.Validate != nil {
		if err := req.Validate(json.RawMessage(content)); err != nil {
			return output.StructuredResponse{}, err
		}
	}
	row, err := f.outputs.CreateDraft(ctx, output.CreateDraftInput{
		ProjectID: req.ProjectID,
		TaskKind:  req.TaskKind,
		Entity:    req.Entity,
		ContentMD: content,
		Provenance: output.Provenance{
			ProviderID:    "provider-1",
			ProviderName:  "Fake Provider",
			ModelID:       "fake-model",
			PromptVersion: req.PromptVersion,
			SourceRefs:    req.SourceRefs,
			Parameters:    map[string]any{"temperature": 0.2},
		},
	})
	if err != nil {
		return output.StructuredResponse{}, err
	}
	return output.StructuredResponse{Output: row, Content: content, JSON: json.RawMessage(content)}, nil
}

// aiFixture builds a service wired to the fake SPEC-02 stack.
func aiFixture(t *testing.T) (*Service, *foundationtest.Fixture, *fakeOutputs, *fakeGenerator) {
	t.Helper()
	f, err := foundationtest.NewWithProject(t.TempDir(), foundationtest.Options{})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	outputs := newFakeOutputs()
	generator := &fakeGenerator{outputs: outputs}
	svc := New(f.Workspace, f.ProjectID, Options{Clock: f.Clock, AI: &AIConfig{Generator: generator, Outputs: outputs}})
	return svc, f, outputs, generator
}

// outlineReply builds a seven-section reply; sources is section → source path.
func outlineReply(sources map[string]string) string {
	keys := sectionKeys(AnalysisOutline)
	sections := make([]string, 0, len(keys))
	for _, key := range keys {
		source := sources[string(key)]
		body := "该小节内容来自来源文件。"
		unconfirmed := false
		unconfirmedJSON := "false"
		if source == "" {
			body = "无法确认：材料中缺少直接证据。"
			unconfirmed = true
			unconfirmedJSON = "true"
		}
		sourceJSON := "[]"
		if source != "" {
			sourceJSON = fmt.Sprintf(`[{"path":%q}]`, source)
		}
		sections = append(sections, fmt.Sprintf(`{"key":%q,"title":"t","text_md":%q,"sources":%s,"unconfirmed":%s,"unconfirmed_reason":null}`,
			key, body, sourceJSON, unconfirmedJSON))
		_ = unconfirmed
	}
	return `{"sections":[` + strings.Join(sections, ",") + `]}`
}

func TestGenerateOutlinePipeline(t *testing.T) {
	svc, _, outputs, generator := aiFixture(t)
	ctx := context.Background()
	root := sampleTree(t)
	rules := DefaultScanRules()
	rules.Exclude = append(rules.Exclude, "outputs/")
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "outline", DirPath: root, Rules: &rules})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	generator.reply = func() string {
		return outlineReply(map[string]string{
			"purpose": "README.md",
			"design":  "train.py",
		})
	}
	// Change one file during generation to prove drift detection.
	generator.during = func() {
		write(t, filepath.Join(root, "train.py"), "print('train changed')\n")
	}
	snap, err := svc.Analysis().GenerateOutline(ctx, exp.ID, AnalysisOptions{})
	if err != nil {
		t.Fatalf("GenerateOutline: %v", err)
	}
	if snap.AIAnalysis == nil || snap.AIAnalysis.Kind != AnalysisOutline {
		t.Fatalf("analysis = %+v", snap.AIAnalysis)
	}
	if len(snap.AIAnalysis.Sections) != len(sectionKeys(AnalysisOutline)) {
		t.Errorf("sections = %d", len(snap.AIAnalysis.Sections))
	}
	if snap.AIAnalysis.AIOutputID == "" || snap.AIAnalysis.PromptVersion != PromptVersionOutline {
		t.Errorf("analysis ids = %+v", snap.AIAnalysis)
	}
	if snap.AIAnalysis.ContentSHA256 != hashString(snap.AIAnalysis.ContentMD) {
		t.Error("content hash mismatch")
	}
	byKey := map[SectionKey]AISection{}
	for _, section := range snap.AIAnalysis.Sections {
		byKey[section.Key] = section
	}
	if len(byKey[SectionPurpose].Sources) != 1 || byKey[SectionPurpose].Sources[0].Path != "README.md" {
		t.Errorf("purpose sources = %+v", byKey[SectionPurpose].Sources)
	}
	if byKey[SectionPurpose].Sources[0].SHA256 == "" {
		t.Error("source hash was not filled from the manifest")
	}
	if !byKey[SectionData].Unconfirmed || !strings.Contains(byKey[SectionData].TextMD, "无法确认") {
		t.Errorf("data section = %+v", byKey[SectionData])
	}
	if len(snap.FileManifest) == 0 || snap.ModelInfo.ProviderID != "provider-1" || snap.ModelInfo.ModelID != "fake-model" {
		t.Errorf("snapshot evidence = %+v", snap)
	}
	if snap.ModelInfo.PromptVersion != PromptVersionOutline {
		t.Errorf("model prompt version = %q", snap.ModelInfo.PromptVersion)
	}
	drift := false
	for _, warning := range snap.AIAnalysis.Warnings {
		if strings.Contains(warning, "files_changed_during_analysis:train.py") {
			drift = true
		}
	}
	if !drift {
		t.Errorf("drift warnings = %v", snap.AIAnalysis.Warnings)
	}
	if snap.AIAnalysis.Config.ScanRules.MaxFiles == 0 {
		t.Error("scan rules missing from analysis config")
	}

	// The draft exists and history is queryable.
	if _, err := outputs.Get(ctx, snap.AIAnalysis.AIOutputID); err != nil {
		t.Fatalf("draft missing: %v", err)
	}
	history, err := svc.Snapshots().List(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, SnapshotFilter{})
	if err != nil || len(history) != 1 || history[0].ID != snap.ID {
		t.Fatalf("history = %+v, %v", history, err)
	}
	if len(generator.requests) != 1 {
		t.Fatalf("generator calls = %d", len(generator.requests))
	}
	// A second analysis appends a new snapshot and leaves the first untouched.
	first := snap
	frozenContent := first.AIAnalysis.ContentMD
	generator.during = nil
	generator.reply = func() string { return outlineReply(map[string]string{"purpose": "README.md"}) }
	second, err := svc.Analysis().GenerateOutline(ctx, exp.ID, AnalysisOptions{})
	if err != nil {
		t.Fatalf("second GenerateOutline: %v", err)
	}
	if second.ID == first.ID || second.AIAnalysis.AIOutputID == first.AIAnalysis.AIOutputID {
		t.Errorf("regeneration reused ids: %s / %s", second.ID, first.ID)
	}
	reloaded, err := svc.Snapshots().Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get first: %v", err)
	}
	if reloaded.AIAnalysis.ContentMD != frozenContent {
		t.Error("first snapshot content changed after the second analysis")
	}
	if rows, _ := outputs.ListByEntity(ctx, output.EntityRef{Type: string(schema.EntityExperiment), ID: exp.ID}); len(rows) != 2 {
		t.Errorf("ai outputs = %d, want 2", len(rows))
	}
	request := generator.requests[0]
	if request.TaskKind != TaskKindOutline || request.PromptVersion != PromptVersionOutline {
		t.Errorf("request = %+v", request)
	}
	if request.Entity == nil || request.Entity.Type != string(schema.EntityExperiment) || request.Entity.ID != exp.ID {
		t.Errorf("request entity = %+v", request.Entity)
	}
	if len(request.SourceRefs) == 0 {
		t.Error("request has no provenance sources")
	}
}

func TestSourceValidationAndUnconfirmed(t *testing.T) {
	svc, _, _, generator := aiFixture(t)
	ctx := context.Background()
	root := externalDir(t)
	write(t, filepath.Join(root, "README.md"), "# doc\n")
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "sources", DirPath: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	generator.reply = func() string {
		return outlineReply(map[string]string{
			"purpose": "missing.md",
			"design":  "README.md",
		})
	}
	snap, err := svc.Analysis().GenerateOutline(ctx, exp.ID, AnalysisOptions{})
	if err != nil {
		t.Fatalf("GenerateOutline: %v", err)
	}
	for _, section := range snap.AIAnalysis.Sections {
		switch section.Key {
		case SectionPurpose:
			if len(section.Sources) != 0 || !section.Unconfirmed {
				t.Errorf("missing source was not dropped: %+v", section)
			}
		case SectionDesign:
			if len(section.Sources) != 1 || section.Sources[0].Path != "README.md" || section.Sources[0].SHA256 == "" {
				t.Errorf("valid source = %+v", section.Sources)
			}
		}
	}
	warned := false
	for _, warning := range snap.AIAnalysis.Warnings {
		if warning == "source_not_in_manifest:missing.md" {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %v", snap.AIAnalysis.Warnings)
	}
}

func resultsReply() string {
	return `{"sections":[
	 {"key":"metrics","title":"t","text_md":"AI claims 0.99","sources":[],"unconfirmed":false,"unconfirmed_reason":null},
	 {"key":"observations","title":"t","text_md":"指标为 0.912。","sources":[{"path":"metrics.json"}],"unconfirmed":false,"unconfirmed_reason":null},
	 {"key":"summary","title":"t","text_md":"准确率提升约 3%。","sources":[{"path":"metrics.json"}],"unconfirmed":false,"unconfirmed_reason":null},
	 {"key":"comparison","title":"t","text_md":"无法确认：只有一个 Run。","sources":[],"unconfirmed":true,"unconfirmed_reason":null},
	 {"key":"limitations","title":"t","text_md":"仅单次运行。","sources":[],"unconfirmed":false,"unconfirmed_reason":null},
	 {"key":"conclusion","title":"t","text_md":"该配置可用。","sources":[{"path":"metrics.json","metric_keys":["eval/accuracy","unknown.key"]}],"unconfirmed":false,"unconfirmed_reason":null}
	]}`
}

func TestGenerateResultsMetricsAndClaims(t *testing.T) {
	svc, _, _, generator := aiFixture(t)
	ctx := context.Background()
	root := externalDir(t)
	write(t, filepath.Join(root, "metrics.json"), `{"eval": {"accuracy": 0.912}}`)
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "results", DirPath: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	generator.reply = resultsReply
	snap, err := svc.Analysis().GenerateResults(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, AnalysisOptions{})
	if err != nil {
		t.Fatalf("GenerateResults: %v", err)
	}
	if len(snap.Metrics.Metrics) != 1 || snap.Metrics.Metrics[0].Key != "eval/accuracy" {
		t.Fatalf("metrics = %+v", snap.Metrics)
	}
	if snap.Metrics.Metrics[0].Value == nil || *snap.Metrics.Metrics[0].Value != 0.912 {
		t.Errorf("metric value = %v", snap.Metrics.Metrics[0].Value)
	}
	byKey := map[SectionKey]AISection{}
	for _, section := range snap.AIAnalysis.Sections {
		byKey[section.Key] = section
	}
	if !strings.Contains(byKey[SectionMetrics].TextMD, "0.912") || strings.Contains(byKey[SectionMetrics].TextMD, "0.99") {
		t.Errorf("metrics section was not deterministic: %q", byKey[SectionMetrics].TextMD)
	}
	conclusion := byKey[SectionConclusion]
	if len(conclusion.Sources) != 1 || len(conclusion.Sources[0].MetricKeys) != 1 || conclusion.Sources[0].MetricKeys[0] != "eval/accuracy" {
		t.Errorf("conclusion sources = %+v", conclusion.Sources)
	}
	if byKey[SectionLimitations].Unconfirmed != true {
		t.Error("sections without sources must be unconfirmed")
	}
	var numberClaim bool
	for _, claim := range snap.AIAnalysis.UnverifiedClaims {
		if claim.Section == string(SectionSummary) && strings.Contains(claim.Text, "3") {
			numberClaim = true
		}
		if claim.Section == string(SectionMetrics) {
			t.Error("the deterministic metrics section must not be number-checked")
		}
	}
	if !numberClaim {
		t.Errorf("unverified claims = %+v", snap.AIAnalysis.UnverifiedClaims)
	}
	warned := false
	for _, warning := range snap.AIAnalysis.Warnings {
		if warning == "unknown_metric_key:unknown.key" {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %v", snap.AIAnalysis.Warnings)
	}
}

func TestAIFailuresAreIsolated(t *testing.T) {
	svc, _, _, generator := aiFixture(t)
	ctx := context.Background()
	root := externalDir(t)
	write(t, filepath.Join(root, "README.md"), "# doc\n")
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "failure", DirPath: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	generator.err = errors.New("provider offline")
	if _, err := svc.Analysis().GenerateOutline(ctx, exp.ID, AnalysisOptions{}); err == nil {
		t.Fatal("generation error was swallowed")
	}
	if snaps, _ := svc.Snapshots().List(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, SnapshotFilter{}); len(snaps) != 0 {
		t.Errorf("failed generation wrote snapshots: %+v", snaps)
	}
	// Scanning, metrics and history stay usable without AI.
	if _, err := svc.Scanning().Plan(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, nil); err != nil {
		t.Errorf("scan after AI failure: %v", err)
	}
	// A later successful retry works normally and appends one snapshot.
	generator.err = nil
	generator.reply = func() string { return outlineReply(map[string]string{"purpose": "README.md"}) }
	if _, err := svc.Analysis().GenerateOutline(ctx, exp.ID, AnalysisOptions{}); err != nil {
		t.Errorf("retry after AI failure: %v", err)
	}
	if _, err := svc.Snapshots().List(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, SnapshotFilter{}); err != nil {
		t.Errorf("history after AI failure: %v", err)
	}

	// Without SPEC-02 wiring the AI methods are unavailable.
	plain := New(svc.ws, svc.projectID, Options{Clock: svc.clock})
	if _, err := plain.Analysis().GenerateOutline(ctx, exp.ID, AnalysisOptions{}); !errors.Is(err, ErrAIUnavailable) {
		t.Errorf("unwired AI error = %v", err)
	}
}

func TestSnapshotPersistRetryWithoutRegeneration(t *testing.T) {
	svc, f, _, generator := aiFixture(t)
	ctx := context.Background()
	root := externalDir(t)
	write(t, filepath.Join(root, "README.md"), "# doc\n")
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "retry", DirPath: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	generator.reply = func() string { return outlineReply(map[string]string{"purpose": "README.md"}) }
	// Break the snapshot table during generation, then restore it.
	generator.during = func() {
		if _, err := f.Project.DB().Exec(`ALTER TABLE snapshots RENAME TO snapshots_offline`); err != nil {
			t.Fatalf("disable snapshots: %v", err)
		}
	}
	_, err = svc.Analysis().GenerateOutline(ctx, exp.ID, AnalysisOptions{})
	var persistErr *SnapshotPersistError
	if !errors.As(err, &persistErr) || !errors.Is(err, ErrSnapshotPersist) {
		t.Fatalf("error = %v, want SnapshotPersistError", err)
	}
	if persistErr.AIOutputID == "" {
		t.Error("persist error carries no ai_output_id")
	}
	if _, err := f.Project.DB().Exec(`ALTER TABLE snapshots_offline RENAME TO snapshots`); err != nil {
		t.Fatalf("restore snapshots: %v", err)
	}
	snap, err := svc.Analysis().RetrySaveSnapshot(ctx, persistErr.AIOutputID)
	if err != nil {
		t.Fatalf("RetrySaveSnapshot: %v", err)
	}
	if snap.AIAnalysis == nil || snap.AIAnalysis.AIOutputID != persistErr.AIOutputID {
		t.Errorf("retried snapshot = %+v", snap)
	}
	if generator.calls != 1 {
		t.Errorf("generator calls = %d, want 1 (no regeneration)", generator.calls)
	}
	if _, err := svc.Analysis().RetrySaveSnapshot(ctx, persistErr.AIOutputID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second retry error = %v", err)
	}
}

func TestEditAndConfirmPermissions(t *testing.T) {
	svc, _, outputs, generator := aiFixture(t)
	ctx := context.Background()
	root := externalDir(t)
	write(t, filepath.Join(root, "README.md"), "# doc\n")
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "review", DirPath: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	generator.reply = func() string { return outlineReply(map[string]string{"purpose": "README.md"}) }
	snap, err := svc.Analysis().GenerateOutline(ctx, exp.ID, AnalysisOptions{})
	if err != nil {
		t.Fatalf("GenerateOutline: %v", err)
	}
	frozen := snap.AIAnalysis.ContentMD
	if err := svc.Analysis().UpdateContent(ctx, snap.ID, "edited by user", schema.ActorUser); err != nil {
		t.Fatalf("UpdateContent: %v", err)
	}
	row, err := outputs.Get(ctx, snap.AIAnalysis.AIOutputID)
	if err != nil {
		t.Fatalf("Get output: %v", err)
	}
	if row.ContentMD != "edited by user" || !row.HumanEdited || row.Status != output.OutputDraft {
		t.Errorf("edited output = %+v", row)
	}
	if snap.AIAnalysis.ContentMD != frozen {
		t.Error("snapshot frozen copy was rewritten")
	}
	if err := svc.Analysis().Confirm(ctx, snap.ID, schema.ActorMCP); !errors.Is(err, output.ErrConfirmationRequiresUser) {
		t.Errorf("MCP confirm error = %v", err)
	}
	if err := svc.Analysis().Confirm(ctx, snap.ID, schema.ActorUser); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	row, _ = outputs.Get(ctx, snap.AIAnalysis.AIOutputID)
	if row.Status != output.OutputConfirmed || row.ConfirmedAt == nil {
		t.Errorf("confirmed output = %+v", row)
	}
	if err := svc.Analysis().UpdateContent(ctx, snap.ID, "AI overwrite", schema.ActorAI); !errors.Is(err, output.ErrConfirmedImmutableForMachine) {
		t.Errorf("AI edit of confirmed error = %v", err)
	}
	// User edits remain allowed on confirmed content.
	if err := svc.Analysis().UpdateContent(ctx, snap.ID, "user edit", schema.ActorUser); err != nil {
		t.Errorf("user edit after confirm: %v", err)
	}
}

func TestArchivedProjectBlocksGeneration(t *testing.T) {
	svc, f, _, generator := aiFixture(t)
	ctx := context.Background()
	root := externalDir(t)
	write(t, filepath.Join(root, "README.md"), "# doc\n")
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "archived", DirPath: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	preview, err := svc.Analysis().PreviewContext(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, AnalysisOptions{})
	if err != nil || len(preview.Entries) == 0 {
		t.Fatalf("PreviewContext: %v", err)
	}
	if _, err := f.Workspace.Global().DB().Exec(`UPDATE projects SET status = 'archived' WHERE id = ?`, f.ProjectID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	generator.reply = func() string { return outlineReply(nil) }
	if _, err := svc.Analysis().GenerateOutline(ctx, exp.ID, AnalysisOptions{}); !errors.Is(err, ErrReadOnlyProject) {
		t.Errorf("generation on archived project = %v", err)
	}
	if generator.calls != 0 {
		t.Errorf("AI was called for an archived project (%d calls)", generator.calls)
	}
	// Preview remains read-only and available.
	if _, err := svc.Analysis().PreviewContext(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, AnalysisOptions{}); err != nil {
		t.Errorf("PreviewContext under archive: %v", err)
	}
}

func TestValidateReplyRejectsBrokenShapes(t *testing.T) {
	bad := []string{
		`{"sections":[]}`,
		`{"sections":[{"key":"purpose","text_md":"x","sources":[],"unconfirmed":true}]}`,
	}
	for _, reply := range bad {
		if err := validateReply(json.RawMessage(reply), AnalysisOutline); !errors.Is(err, ErrInvalidAIOutput) {
			t.Errorf("reply %q error = %v", reply, err)
		}
	}
	dup := `{"sections":[{"key":"purpose","text_md":"x","sources":[],"unconfirmed":false},{"key":"purpose","text_md":"x","sources":[],"unconfirmed":false}]}`
	if err := validateReply(json.RawMessage(dup), AnalysisOutline); !errors.Is(err, ErrInvalidAIOutput) {
		t.Errorf("duplicate error = %v", err)
	}
}

var _ output.GenerationService = (*fakeGenerator)(nil)
var _ output.AIOutputService = (*fakeOutputs)(nil)

// fakeRunner is a SPEC-02 ChatRunner returning one canned structured reply.
type fakeRunner struct{ reply string }

func (r *fakeRunner) Chat(context.Context, role.Role, gateway.ChatRequest) (gateway.ChatResponse, gateway.ProviderSummary, error) {
	return gateway.ChatResponse{
		Content:  r.reply,
		Model:    "integration-model",
		Usage:    gateway.Usage{InputTokens: 11, OutputTokens: 22},
		Attempts: 1,
	}, gateway.ProviderSummary{
		ProviderID:   "provider-integration",
		ProviderName: "Integration Provider",
		Protocol:     provider.ProtocolOpenAIChat,
		ModelID:      "integration-model",
	}, nil
}

// TestRealSPEC02Integration wires the actual SPEC-02 output service and
// generation service over the project database, covering the ai_outputs state
// shown in history (§3.7, AC-14).
func TestRealSPEC02Integration(t *testing.T) {
	ctx := context.Background()
	aiMigrations, err := migrations.All()
	if err != nil {
		t.Fatalf("ai migrations: %v", err)
	}
	f, err := foundationtest.NewWithProject(t.TempDir(), foundationtest.Options{Migrations: aiMigrations})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	outputs, err := output.NewOutputService(f.Project.DB(), output.OutputOptions{
		Clock:     f.Clock,
		ProjectID: f.ProjectID,
		ReadOnly:  output.NewProjectReadOnly(f.Workspace.Global().DB(), f.ProjectID),
	})
	if err != nil {
		t.Fatalf("output service: %v", err)
	}
	generator, err := output.NewGenerationService(output.GenerationOptions{
		Outputs: outputs,
		Runner: &fakeRunner{reply: outlineReply(map[string]string{
			"purpose": "README.md",
			"design":  "train.py",
		})},
	})
	if err != nil {
		t.Fatalf("generation service: %v", err)
	}
	svc := New(f.Workspace, f.ProjectID, Options{Clock: f.Clock, AI: &AIConfig{Generator: generator, Outputs: outputs}})
	root := sampleTree(t)
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "integration", DirPath: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	snap, err := svc.Analysis().GenerateOutline(ctx, exp.ID, AnalysisOptions{})
	if err != nil {
		t.Fatalf("GenerateOutline: %v", err)
	}
	if snap.ModelInfo.ProviderID != "provider-integration" || snap.ModelInfo.Protocol != string(provider.ProtocolOpenAIChat) {
		t.Errorf("model info = %+v", snap.ModelInfo)
	}
	history, err := svc.Snapshots().List(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, SnapshotFilter{})
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %+v, %v", history, err)
	}
	if history[0].AIStatus != string(output.OutputDraft) {
		t.Errorf("draft status = %q", history[0].AIStatus)
	}
	if err := svc.Analysis().UpdateContent(ctx, snap.ID, "manual edit", schema.ActorUser); err != nil {
		t.Fatalf("UpdateContent: %v", err)
	}
	history, _ = svc.Snapshots().List(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, SnapshotFilter{})
	if !history[0].HumanEdited {
		t.Error("human_edited not reflected in history")
	}
	if err := svc.Analysis().Confirm(ctx, snap.ID, schema.ActorUser); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	history, _ = svc.Snapshots().List(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, SnapshotFilter{})
	if history[0].AIStatus != string(output.OutputConfirmed) || history[0].ConfirmedAt == nil {
		t.Errorf("confirmed history = %+v", history[0])
	}
	// Snapshot frozen copy survives editing and confirmation.
	loaded, err := svc.Snapshots().Get(ctx, snap.ID)
	if err != nil {
		t.Fatalf("Get snapshot: %v", err)
	}
	if loaded.AIAnalysis.ContentMD == "manual edit" {
		t.Error("snapshot frozen copy was rewritten by an edit")
	}
}
