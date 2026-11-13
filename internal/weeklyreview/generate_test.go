package weeklyreview

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	aioutput "researchos/internal/ai/output"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
)

// newAIEnv wires a scripted fake GenerationService into a fresh project.
func newAIEnv(t *testing.T, responses ...fakeResponse) (*testEnv, *fakeAI) {
	t.Helper()
	var (
		env  *testEnv
		fake *fakeAI
	)
	env = newTestEnv(t, envConfig{
		now: time.Date(2026, 11, 11, 12, 0, 0, 0, mustLocation(t, "Asia/Shanghai")),
		settings: map[string]string{
			"weekly.timezone": `"Asia/Shanghai"`,
		},
		aiFactory: func(outputs aioutput.AIOutputService, clk clock.Clock) aioutput.GenerationService {
			fake = newFakeAI(outputs, clk, responses...)
			fake.emit = func(out aioutput.AIOutput) {
				if out.Entity == nil {
					return
				}
				// Stand-in for SPEC-02's automatic ai.analysis_completed.
				if err := env.handle.Events().EmitStandalone(env.ctx, events.Event{
					Type:       schema.EventAIAnalysisCompleted,
					EntityType: schema.EntityWeeklyReport,
					EntityID:   out.Entity.ID,
					Summary:    "AI 分析完成",
					Actor:      schema.ActorAI,
					At:         env.clock.Now(),
				}); err != nil {
					t.Errorf("fake ai emit: %v", err)
				}
			}
			return fake
		},
	})
	return env, fake
}

func TestAC08GenerateValidSections(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env, fake := newAIEnv(t, fakeResponse{content: readGolden(t, "ai_valid.md")})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	report := env.newReport(period, false)

	outcome, err := env.svc.GenerateDraft(env.ctx, GenerateInput{ReportID: report.ID, Actor: ActorUser})
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if outcome.Report.Status != StatusDraft {
		t.Fatalf("status = %s, want draft", outcome.Report.Status)
	}
	if outcome.Report.Meta.Generation == nil || outcome.Report.Meta.Generation.AIOutputID == "" {
		t.Fatalf("provenance missing: %+v", outcome.Report.Meta.Generation)
	}
	if outcome.Report.Meta.Generation.PromptVersion != promptVersion {
		t.Fatalf("prompt version = %q", outcome.Report.Meta.Generation.PromptVersion)
	}
	if !strings.HasPrefix(outcome.Report.ContentMD, "# ") {
		t.Fatalf("missing H1:\n%s", outcome.Report.ContentMD)
	}
	if got := strings.Count(outcome.Report.ContentMD, "\n# "); got != 0 {
		t.Fatalf("unexpected extra H1: %d", got)
	}
	if got := strings.Count(outcome.Report.ContentMD, "\n## "); got != 7 {
		t.Fatalf("H2 count = %d, want 7:\n%s", got, outcome.Report.ContentMD)
	}
	parsed := parseReport(outcome.Report.ContentMD)
	if len(parsed.order) != 7 {
		t.Fatalf("parsed section order = %v", parsed.order)
	}
	for i, sec := range defaultTemplate.Sections {
		if parsed.order[i] != sec.Key {
			t.Fatalf("section %d = %s, want %s", i, parsed.order[i], sec.Key)
		}
	}
	if len(outcome.Violations) != 0 {
		t.Fatalf("unexpected violations: %+v", outcome.Violations)
	}

	generated := env.eventsOfType(schema.EventReportGenerated)
	if len(generated) != 1 || generated[0].Actor != schema.ActorAI {
		t.Fatalf("report.generated = %+v", generated)
	}
	analysis := env.eventsOfType(schema.EventAIAnalysisCompleted)
	if len(analysis) != 1 {
		t.Fatalf("ai.analysis_completed count = %d, want exactly 1", len(analysis))
	}
	if got := env.aiOutputStatus(outcome.Report.Meta.Generation.AIOutputID); got != "draft" {
		t.Fatalf("ai_output status = %s, want draft", got)
	}

	req, ok := fake.lastRequest()
	if !ok {
		t.Fatal("fake AI was not called")
	}
	if req.TaskKind != taskKind || req.PromptVersion != promptVersion {
		t.Fatalf("request = %+v", req)
	}
	if req.Entity == nil || req.Entity.Type != string(schema.EntityWeeklyReport) || req.Entity.ID != report.ID {
		t.Fatalf("request entity = %+v", req.Entity)
	}
	if len(req.SourceRefs) > 200 {
		t.Fatalf("source refs = %d, want <= 200", len(req.SourceRefs))
	}
}

func TestAC12ParseFailureFallsBackToSkeleton(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	valid := readGolden(t, "ai_valid.md")
	missing := strings.Replace(valid, "## 3. 理论研究进展\n- 本周期无记录\n\n", "", 1)
	env, fake := newAIEnv(t,
		fakeResponse{content: missing},
		fakeResponse{content: missing},
	)
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	report := env.newReport(period, false)

	outcome, err := env.svc.GenerateDraft(env.ctx, GenerateInput{ReportID: report.ID, Actor: ActorUser})
	if !IsCode(err, ErrAIOutputInvalid) {
		t.Fatalf("GenerateDraft = %v, want ErrAIOutputInvalid", err)
	}
	if outcome == nil || outcome.Report == nil {
		t.Fatal("outcome report missing")
	}
	if fake.callCount() != 2 {
		t.Fatalf("attempts = %d, want retry once (2)", fake.callCount())
	}
	want := BuildSkeleton("Alpha", period, outcome.Digest)
	if outcome.Report.ContentMD != want {
		t.Fatalf("fallback content is not the deterministic skeleton:\n%s", outcome.Report.ContentMD)
	}
	if outcome.Report.Status != StatusDraft {
		t.Fatalf("status = %s, want draft", outcome.Report.Status)
	}
	gen := outcome.Report.Meta.Generation
	if gen == nil || gen.AIStatus != "failed" || gen.Error == "" {
		t.Fatalf("generation meta = %+v", gen)
	}
	if got := len(env.eventsOfType(schema.EventReportGenerated)); got != 0 {
		t.Fatalf("report.generated count = %d, want 0", got)
	}

	// Manual editing still works after the AI failure.
	if _, err := env.svc.UpdateContent(env.ctx, UpdateContentInput{
		ReportID: report.ID, ContentMD: outcome.Report.ContentMD + "\n",
		ExpectedHash: outcome.Report.ContentHash, Actor: ActorUser,
	}); err != nil {
		t.Fatalf("UpdateContent after failure: %v", err)
	}
}

func TestAC13AIFailureKeepsContent(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env, _ := newAIEnv(t, fakeResponse{err: errors.New("provider timeout")})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	report := env.newReport(period, true)
	baseline := report.ContentMD
	before := env.activityCount()

	env.clock.Advance(10 * time.Minute)
	outcome, err := env.svc.GenerateDraft(env.ctx, GenerateInput{
		ReportID: report.ID, Replace: true, Actor: ActorUser,
	})
	if !IsCode(err, ErrAIUnavailable) {
		t.Fatalf("GenerateDraft = %v, want ErrAIUnavailable", err)
	}
	if outcome.Report.ContentMD != baseline {
		t.Fatal("failed AI generation cleared the existing content")
	}
	gen := outcome.Report.Meta.Generation
	if gen == nil || gen.AIStatus != "failed" || !strings.Contains(gen.Error, "timeout") {
		t.Fatalf("generation meta = %+v", gen)
	}
	if !outcome.Report.UpdatedAt.After(report.UpdatedAt) {
		t.Fatalf("updated_at = %s, want after %s", outcome.Report.UpdatedAt, report.UpdatedAt)
	}
	if got := env.activityCount(); got != before {
		t.Fatalf("failed generation wrote activities: %d -> %d", before, got)
	}
	if got := len(env.eventsOfType(schema.EventReportGenerated)); got != 0 {
		t.Fatalf("report.generated count = %d, want 0", got)
	}
	// The stored body is untouched.
	_, stored, _ := env.reportRow(report.ID)
	if stored != baseline {
		t.Fatal("stored content changed on AI failure")
	}
}

func TestAC22EmptyPeriodGeneration(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	outcome, err := env.svc.GenerateForPeriod(env.ctx, GeneratePeriodInput{
		Period: period, IncludeAI: false, Actor: ActorUser,
	})
	if err != nil {
		t.Fatalf("GenerateForPeriod: %v", err)
	}
	content := outcome.Report.ContentMD
	for _, sec := range defaultTemplate.Sections {
		if !strings.Contains(content, "## "+strconv.Itoa(sec.Num)+". "+sec.Title) {
			t.Fatalf("missing section %s:\n%s", sec.Title, content)
		}
	}
	if strings.Count(content, "本周期无记录") != len(defaultTemplate.Sections) {
		t.Fatalf("empty period must not invent entries:\n%s", content)
	}
	if outcome.Report.Meta.Coverage == nil || outcome.Report.Meta.Coverage.EventCount != 0 {
		t.Fatalf("coverage = %+v", outcome.Report.Meta.Coverage)
	}
	if outcome.Report.Meta.Generation == nil ||
		outcome.Report.Meta.Generation.Mode != "manual" ||
		outcome.Report.Meta.Generation.AIStatus != "skipped" {
		t.Fatalf("generation meta = %+v", outcome.Report.Meta.Generation)
	}
	if got := len(env.eventsOfType(schema.EventReportGenerated)); got != 0 {
		t.Fatalf("report.generated count = %d, want 0", got)
	}

	// Re-running reuses the single draft for the period.
	again, err := env.svc.GenerateForPeriod(env.ctx, GeneratePeriodInput{
		Period: period, IncludeAI: false, Actor: ActorUser,
	})
	if err != nil {
		t.Fatalf("GenerateForPeriod second: %v", err)
	}
	if again.Report.ID != outcome.Report.ID {
		t.Fatalf("reuse failed: %s vs %s", again.Report.ID, outcome.Report.ID)
	}
	if _, err := env.svc.Confirm(env.ctx, ConfirmInput{
		ReportID: outcome.Report.ID, Actor: ActorUser,
	}); err != nil {
		t.Fatalf("Confirm skeleton: %v", err)
	}
	if _, err := env.svc.GenerateForPeriod(env.ctx, GeneratePeriodInput{
		Period: period, IncludeAI: false, Actor: ActorUser,
	}); !IsCode(err, ErrConfirmedReadOnly) {
		t.Fatalf("GenerateForPeriod confirmed = %v, want ErrConfirmedReadOnly", err)
	}
}

func TestPromptContract(t *testing.T) {
	info := projectInfo{ID: testProjectID, Name: "Alpha", Summary: "summary", Goals: "goals"}
	digest := unitDigest(t, map[SectionKey][]DigestItem{
		SectionWork: {{Ref: EntityRef{Type: "note", ID: testNotePaper}, Summary: "记录", State: "done"}},
	}, []PlannedTask{{Ref: EntityRef{Type: "task", ID: testTaskPlanA}, Title: "补写方法章节", Origin: "week_plan"}})
	messages := buildPromptMessages(info, digest)
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(messages))
	}
	system := messages[0].Content
	for rule := 1; rule <= 10; rule++ {
		if !strings.Contains(system, "\n"+strconv.Itoa(rule)+". ") && !strings.HasPrefix(system, strconv.Itoa(rule)+". ") {
			t.Fatalf("system prompt is missing constraint %d:\n%s", rule, system)
		}
	}
	for _, phrase := range []string{
		"只能使用输入", "不得写成已完成", "第 7 节不得写入已完成任务", "待确认",
		"不得编造数值", "不得下结论", "Idea", "原因未记录", "本周期无记录", "12 条", "120 字",
	} {
		if !strings.Contains(system, phrase) {
			t.Fatalf("system prompt is missing %q", phrase)
		}
	}
	user := messages[1].Content
	if !strings.Contains(user, "Alpha") || !strings.Contains(user, "补写方法章节") ||
		!strings.Contains(user, "本周工作内容") {
		t.Fatalf("user prompt lacks digest/metadata:\n%s", user)
	}
}

func TestAC28ConfirmTxAtomicity(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	valid := readGolden(t, "ai_valid.md")

	// Happy path: report and ai_output flip together.
	env, _ := newAIEnv(t, fakeResponse{content: valid})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	report := env.newReport(period, false)
	outcome, err := env.svc.GenerateDraft(env.ctx, GenerateInput{ReportID: report.ID, Actor: ActorUser})
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	outputID := outcome.Report.Meta.Generation.AIOutputID
	if outputID == "" {
		t.Fatal("ai_output_id missing")
	}
	confirmed, err := env.svc.Confirm(env.ctx, ConfirmInput{ReportID: report.ID, Actor: ActorUser})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if confirmed.Status != StatusConfirmed {
		t.Fatalf("status = %s", confirmed.Status)
	}
	if got := env.aiOutputStatus(outputID); got != "confirmed" {
		t.Fatalf("ai_output status = %s, want confirmed", got)
	}
	if confirmed.Meta.ConfirmedHash != confirmed.ContentHash {
		t.Fatalf("confirmed_hash = %q, want %q", confirmed.Meta.ConfirmedHash, confirmed.ContentHash)
	}

	// Rollback injection: ConfirmTx runs, then the transaction fails.
	var rollbackEnv *testEnv
	rollbackEnv = newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
		wrapOutputs: func(svc aioutput.AIOutputService) aioutput.AIOutputService {
			return failingOutputs{AIOutputService: svc}
		},
		aiFactory: func(outputs aioutput.AIOutputService, clk clock.Clock) aioutput.GenerationService {
			return newFakeAI(outputs, clk, fakeResponse{content: valid})
		},
	})
	rollbackReport := rollbackEnv.newReport(period, false)
	rollbackOutcome, err := rollbackEnv.svc.GenerateDraft(rollbackEnv.ctx, GenerateInput{
		ReportID: rollbackReport.ID, Actor: ActorUser,
	})
	if err != nil {
		t.Fatalf("rollback GenerateDraft: %v", err)
	}
	rollbackOutput := rollbackOutcome.Report.Meta.Generation.AIOutputID
	if _, err := rollbackEnv.svc.Confirm(rollbackEnv.ctx, ConfirmInput{
		ReportID: rollbackReport.ID, Actor: ActorUser,
	}); err == nil {
		t.Fatal("Confirm succeeded despite injected failure")
	}
	if status, _, _ := rollbackEnv.reportRow(rollbackReport.ID); status != string(StatusDraft) {
		t.Fatalf("report status = %s, want draft after rollback", status)
	}
	if got := rollbackEnv.aiOutputStatus(rollbackOutput); got != "draft" {
		t.Fatalf("ai_output status = %s, want draft after rollback", got)
	}
}
