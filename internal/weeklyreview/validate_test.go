package weeklyreview

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
)

func unitPeriod(t *testing.T) Period {
	t.Helper()
	loc := mustLocation(t, "Asia/Shanghai")
	return periodOf(t, loc, "2026-11-02", "2026-11-08")
}

func testReport(t *testing.T, bodies map[SectionKey]string) string {
	t.Helper()
	p := unitPeriod(t)
	var b strings.Builder
	fmt.Fprintf(&b, "# Weekly Research Report — Alpha（%s – %s）\n\n", p.StartDate(), p.EndDate())
	for _, sec := range defaultTemplate.Sections {
		fmt.Fprintf(&b, "## %d. %s\n", sec.Num, sec.Title)
		body, ok := bodies[sec.Key]
		if !ok || strings.TrimSpace(body) == "" {
			body = "- 本周期无记录"
		}
		b.WriteString(body)
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func unitDigest(t *testing.T, items map[SectionKey][]DigestItem, planned []PlannedTask) *ActivityDigest {
	t.Helper()
	digest := &ActivityDigest{
		Period:   unitPeriod(t),
		Timezone: "Asia/Shanghai",
		Planned:  planned,
		Stats:    DigestStats{CountsByType: map[string]int{}, CountsBySection: map[SectionKey]int{}},
	}
	for _, sec := range defaultTemplate.Sections {
		digest.Sections = append(digest.Sections, DigestSection{
			Key: sec.Key, Title: sec.Title, Items: append([]DigestItem(nil), items[sec.Key]...),
		})
	}
	return digest
}

func violationRules(violations []Violation) []string {
	var out []string
	for _, v := range violations {
		out = append(out, v.Rule)
	}
	return out
}

func TestValidateV1PlannedAsDone(t *testing.T) {
	digest := unitDigest(t, nil, []PlannedTask{{
		Ref: EntityRef{Type: "task", ID: testTaskPlanA}, Title: "补写方法章节", Origin: "week_plan",
	}})
	content := testReport(t, map[SectionKey]string{
		SectionWork: "- 补写方法章节 [[ref:task:" + testTaskPlanA + "]]",
	})
	violations := Validate(content, defaultTemplate, digest)
	found := false
	for _, v := range violations {
		if v.Rule == "V1" && v.Severity == SeverityError && v.Section == SectionWork {
			found = true
			if !strings.HasPrefix(v.Fingerprint, "sha256:") {
				t.Fatalf("fingerprint %q lacks the sha256: prefix", v.Fingerprint)
			}
		}
	}
	if !found {
		t.Fatalf("V1 not reported: %+v", violations)
	}

	// Moving the same title to section 7 clears V1.
	content = testReport(t, map[SectionKey]string{
		SectionNextWeek: "- 补写方法章节 [[ref:task:" + testTaskPlanA + "]]",
	})
	for _, v := range Validate(content, defaultTemplate, digest) {
		if v.Rule == "V1" {
			t.Fatalf("V1 must not fire for the planned section: %+v", v)
		}
	}
}

func TestValidateV2UnsourcedNumber(t *testing.T) {
	digest := unitDigest(t, map[SectionKey][]DigestItem{
		SectionWork: {{Ref: EntityRef{Type: "note", ID: testNotePaper}, Summary: "实验记录", State: "done"}},
	}, nil)
	content := testReport(t, map[SectionKey]string{
		SectionWork: "- 模型准确率提升 12.7%，记录见笔记 [[ref:note:" + testNotePaper + "]]",
	})
	violations := Validate(content, defaultTemplate, digest)
	if !containsRule(violations, "V2") {
		t.Fatalf("V2 not reported: %+v", violations)
	}

	content = testReport(t, map[SectionKey]string{
		SectionWork: "- 模型准确率有所提升，记录见笔记 [[ref:note:" + testNotePaper + "]]",
	})
	if containsRule(Validate(content, defaultTemplate, digest), "V2") {
		t.Fatal("V2 must disappear once the unsourced number is removed")
	}

	// Numbers present in the digest are allowed.
	digest = unitDigest(t, map[SectionKey][]DigestItem{
		SectionWork: {{Ref: EntityRef{Type: "note", ID: testNotePaper}, Summary: "准确率 12.7%", State: "done"}},
	}, nil)
	content = testReport(t, map[SectionKey]string{
		SectionWork: "- 准确率 12.7% [[ref:note:" + testNotePaper + "]]",
	})
	if containsRule(Validate(content, defaultTemplate, digest), "V2") {
		t.Fatal("V2 must accept numbers that appear in the digest")
	}
}

func TestValidateV3UnconfirmedSource(t *testing.T) {
	digest := unitDigest(t, map[SectionKey][]DigestItem{
		SectionExperiment: {{
			Ref: EntityRef{Type: "snapshot", ID: testSnapshot}, Summary: "AI 分析完成",
			UnconfirmedSource: true, State: "done",
		}},
	}, nil)
	content := testReport(t, map[SectionKey]string{
		SectionExperiment: "- 结果表明方案有效 [[ref:snapshot:" + testSnapshot + "]]",
	})
	if !containsRule(Validate(content, defaultTemplate, digest), "V3") {
		t.Fatal("V3 must flag an unmarked unconfirmed AI conclusion")
	}
	content = testReport(t, map[SectionKey]string{
		SectionExperiment: "- 结果表明方案有效（待确认）[[ref:snapshot:" + testSnapshot + "]]",
	})
	if containsRule(Validate(content, defaultTemplate, digest), "V3") {
		t.Fatal("V3 must accept the （待确认） marker")
	}

	// A claim with no reference at all is also V3.
	digest = unitDigest(t, nil, nil)
	content = testReport(t, map[SectionKey]string{
		SectionTheory: "- 我们发现该方法有效",
	})
	if !containsRule(Validate(content, defaultTemplate, digest), "V3") {
		t.Fatal("V3 must flag unsourced claims")
	}
}

func TestValidateV4UnknownRef(t *testing.T) {
	digest := unitDigest(t, nil, nil)
	content := testReport(t, map[SectionKey]string{
		SectionWork: "- 引用 [[ref:note:" + testNotePaper + "]]",
	})
	violations := Validate(content, defaultTemplate, digest)
	found := false
	for _, v := range violations {
		if v.Rule == "V4" && v.Severity == SeverityWarning {
			found = true
		}
	}
	if !found {
		t.Fatalf("V4 not reported: %+v", violations)
	}
}

func TestValidateV5UngroundedPlan(t *testing.T) {
	digest := unitDigest(t, nil, []PlannedTask{{
		Ref: EntityRef{Type: "task", ID: testTaskPlanA}, Title: "补写方法章节",
	}})
	content := testReport(t, map[SectionKey]string{
		SectionNextWeek: "- 补写方法章节 [[ref:task:" + testTaskPlanB + "]]",
	})
	if !containsRule(Validate(content, defaultTemplate, digest), "V5") {
		t.Fatal("V5 must flag a task ref outside the planned candidates")
	}

	// A bullet without any candidate reference is a new plan entry.
	content = testReport(t, map[SectionKey]string{
		SectionNextWeek: "- 完成论文投稿",
	})
	if !containsRule(Validate(content, defaultTemplate, digest), "V5") {
		t.Fatal("V5 must flag a plan entry without a candidate ref")
	}

	// A candidate title written without a ref is still grounded.
	content = testReport(t, map[SectionKey]string{
		SectionNextWeek: "- 补写方法章节（已排入下周）",
	})
	if containsRule(Validate(content, defaultTemplate, digest), "V5") {
		t.Fatal("V5 must accept a planned candidate named by title")
	}

	content = testReport(t, map[SectionKey]string{
		SectionNextWeek: "- 补写方法章节 [[ref:task:" + testTaskPlanA + "]]",
	})
	if containsRule(Validate(content, defaultTemplate, digest), "V5") {
		t.Fatal("V5 must accept the listed candidate")
	}
}

func TestValidateV6Structure(t *testing.T) {
	digest := unitDigest(t, nil, nil)
	content := testReport(t, nil)
	if got := Validate(content, defaultTemplate, digest); containsRule(got, "V6") {
		t.Fatalf("well-formed report must not warn: %+v", got)
	}

	broken := strings.Replace(content, "## 1. 本周工作内容", "## 1. 本周工作总结", 1)
	violations := Validate(broken, defaultTemplate, digest)
	if !containsRule(violations, "V6") {
		t.Fatalf("renamed heading must warn: %+v", violations)
	}

	missing := strings.Replace(content, "## 3. 理论研究进展\n- 本周期无记录\n\n", "", 1)
	if !containsRule(Validate(missing, defaultTemplate, digest), "V6") {
		t.Fatal("missing section must warn")
	}

	swapped := strings.Replace(content, "## 1. 本周工作内容", "## TEMP", 1)
	swapped = strings.Replace(swapped, "## 2. 文献调研进展", "## 1. 本周工作内容", 1)
	swapped = strings.Replace(swapped, "## TEMP", "## 2. 文献调研进展", 1)
	if !containsRule(Validate(swapped, defaultTemplate, digest), "V6") {
		t.Fatal("swapped section order must warn")
	}
}

func containsRule(violations []Violation, rule string) bool {
	for _, v := range violations {
		if v.Rule == rule {
			return true
		}
	}
	return false
}

func TestAC09AckGate(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	env.seedColumn("Doing", "todo", false, 1)
	env.seedTask(taskSeed{ID: testTaskPlanA, Title: "补写方法章节", Column: "Doing", WeekPlan: "2026-W46"})

	report, err := env.svc.CreateReport(env.ctx, CreateReportInput{Period: period, Actor: ActorUser})
	if err != nil {
		t.Fatalf("CreateReport: %v", err)
	}
	content := testReport(t, map[SectionKey]string{
		SectionWork: "- 补写方法章节 [[ref:task:" + testTaskPlanA + "]]",
	})
	updated, err := env.svc.UpdateContent(env.ctx, UpdateContentInput{
		ReportID: report.ID, ContentMD: content, ExpectedHash: report.ContentHash, Actor: ActorUser,
	})
	if err != nil {
		t.Fatalf("UpdateContent: %v", err)
	}
	var v1 *Violation
	for i := range updated.Meta.Checks.Violations {
		if updated.Meta.Checks.Violations[i].Rule == "V1" {
			v1 = &updated.Meta.Checks.Violations[i]
		}
	}
	if v1 == nil {
		t.Fatalf("V1 missing after edit: %+v", updated.Meta.Checks.Violations)
	}

	_, err = env.svc.Confirm(env.ctx, ConfirmInput{ReportID: report.ID, Actor: ActorUser})
	if !IsCode(err, ErrUnresolvedViolations) {
		t.Fatalf("Confirm = %v, want ErrUnresolvedViolations", err)
	}
	var se *ServiceError
	if !errors.As(err, &se) {
		t.Fatalf("unresolved error = %T, want *ServiceError", err)
	}
	list, ok := se.Details["violations"].([]Violation)
	if !ok || len(list) == 0 {
		t.Fatalf("unresolved details = %#v", se.Details)
	}

	confirmed, err := env.svc.Confirm(env.ctx, ConfirmInput{
		ReportID:        report.ID,
		Actor:           ActorUser,
		AckFingerprints: []string{v1.Fingerprint},
		AckNotes:        map[string]string{v1.Fingerprint: "已完成，计划表未更新"},
	})
	if err != nil {
		t.Fatalf("Confirm after ack: %v", err)
	}
	if confirmed.Status != StatusConfirmed {
		t.Fatalf("status = %s, want confirmed", confirmed.Status)
	}
	acked := false
	for _, v := range confirmed.Meta.Checks.Violations {
		if v.Rule == "V1" && v.Ack != nil {
			acked = true
			if v.Ack.Actor != "user" || v.Ack.Note != "已完成，计划表未更新" || v.Ack.At.IsZero() {
				t.Fatalf("ack = %+v", v.Ack)
			}
		}
	}
	if !acked {
		t.Fatal("ack not persisted in meta.checks")
	}
}

func TestAC10AndAC11ServicePath(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	env.seedNote(testNotePaper, "paper_note", "论文笔记", false)
	env.emit(events.Event{Type: schema.EventEntityCreated, EntityType: schema.EntityNote,
		EntityID: testNotePaper, Summary: "新增论文笔记", Actor: schema.ActorUser,
		At: at(t, loc, "2026-11-03 10:00")})

	report, err := env.svc.CreateReport(env.ctx, CreateReportInput{Period: period, Skeleton: true, Actor: ActorUser})
	if err != nil {
		t.Fatalf("CreateReport: %v", err)
	}
	// AC-10: an unsourced number produces V2.
	content := strings.Replace(report.ContentMD,
		"## 1. 本周工作内容\n- 本周期无记录",
		"## 1. 本周工作内容\n- 准确率提升 12.7% [[ref:note:"+testNotePaper+"]]", 1)
	updated, err := env.svc.UpdateContent(env.ctx, UpdateContentInput{
		ReportID: report.ID, ContentMD: content, ExpectedHash: report.ContentHash, Actor: ActorUser,
	})
	if err != nil {
		t.Fatalf("UpdateContent: %v", err)
	}
	if !containsRule(updated.Meta.Checks.Violations, "V2") {
		t.Fatalf("V2 not persisted: %+v", updated.Meta.Checks.Violations)
	}
	// Removing the number clears it.
	content = strings.Replace(content, "12.7%", "约一成", 1)
	updated, err = env.svc.UpdateContent(env.ctx, UpdateContentInput{
		ReportID: report.ID, ContentMD: content, ExpectedHash: updated.ContentHash, Actor: ActorUser,
	})
	if err != nil {
		t.Fatalf("UpdateContent second: %v", err)
	}
	if containsRule(updated.Meta.Checks.Violations, "V2") {
		t.Fatalf("V2 survived the edit: %+v", updated.Meta.Checks.Violations)
	}
}

func TestCheckStateTruncation(t *testing.T) {
	violations := make([]Violation, maxViolations+5)
	for i := range violations {
		violations[i] = Violation{Rule: "V4", Severity: SeverityWarning, Fingerprint: fmt.Sprintf("sha256:%d", i)}
	}
	state := buildCheckState(violations, time.Now().UTC())
	if !state.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if len(state.Violations) != maxViolations {
		t.Fatalf("violations = %d, want %d", len(state.Violations), maxViolations)
	}
	small := buildCheckState(violations[:1], time.Now().UTC())
	if small.Truncated {
		t.Fatal("small violation set must not be marked truncated")
	}
}
