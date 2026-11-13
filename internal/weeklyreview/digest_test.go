package weeklyreview

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	aioutput "researchos/internal/ai/output"
	"researchos/internal/ai/provider"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
)

func TestAC01DefaultPeriod(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now: time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{
			"weekly.week_start_day": "0",
			"weekly.timezone":       `"Asia/Shanghai"`,
			"weekly.default_period": `"current"`,
		},
	})
	period, err := env.svc.DefaultPeriod(env.ctx)
	if err != nil {
		t.Fatalf("DefaultPeriod: %v", err)
	}
	if got, want := period.Start.Format(time.RFC3339), "2026-11-08T00:00:00+08:00"; got != want {
		t.Fatalf("start = %s, want %s", got, want)
	}
	if got, want := period.EndExclusive.Format(time.RFC3339), "2026-11-15T00:00:00+08:00"; got != want {
		t.Fatalf("end = %s, want %s", got, want)
	}

	env.execGlobal(`UPDATE settings SET value = '"previous"' WHERE key = 'weekly.default_period'`)
	period, err = env.svc.DefaultPeriod(env.ctx)
	if err != nil {
		t.Fatalf("DefaultPeriod previous: %v", err)
	}
	if got, want := period.Start.Format(time.RFC3339), "2026-11-01T00:00:00+08:00"; got != want {
		t.Fatalf("previous start = %s, want %s", got, want)
	}
	if got, want := period.EndExclusive.Format(time.RFC3339), "2026-11-08T00:00:00+08:00"; got != want {
		t.Fatalf("previous end = %s, want %s", got, want)
	}
}

func TestAC01DefaultPeriodDST(t *testing.T) {
	loc := mustLocation(t, "America/New_York")
	// 2026-03-08 is the US spring-forward Sunday; the week advances by
	// calendar days, not 7×24h.
	env := newTestEnv(t, envConfig{
		now: time.Date(2026, 3, 10, 12, 0, 0, 0, loc),
		settings: map[string]string{
			"weekly.week_start_day": "0",
			"weekly.timezone":       `"America/New_York"`,
		},
	})
	period, err := env.svc.DefaultPeriod(env.ctx)
	if err != nil {
		t.Fatalf("DefaultPeriod: %v", err)
	}
	if got, want := period.Start.Format(time.RFC3339), "2026-03-08T00:00:00-05:00"; got != want {
		t.Fatalf("start = %s, want %s", got, want)
	}
	if got, want := period.EndExclusive.Format(time.RFC3339), "2026-03-15T00:00:00-04:00"; got != want {
		t.Fatalf("end = %s, want %s", got, want)
	}
	if days := period.Days(); days != 7 {
		t.Fatalf("days = %d, want 7", days)
	}
}

func TestAC02WindowBoundary(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-08", "2026-11-14")

	env.emit(events.Event{
		Type: schema.EventEntityCreated, EntityType: schema.EntityNote, EntityID: testNotePaper,
		Summary: "边界前", Actor: schema.ActorUser, At: at(t, loc, "2026-11-07 23:59"),
	})
	env.emit(events.Event{
		Type: schema.EventEntityCreated, EntityType: schema.EntityNote, EntityID: testNotePaper,
		Summary: "边界内", Actor: schema.ActorUser, At: at(t, loc, "2026-11-08 00:00"),
	})
	// Last stored instant before EndExclusive is inside; the EndExclusive
	// instant itself is outside (events.Query.Until is inclusive).
	env.emit(events.Event{
		Type: schema.EventEntityCreated, EntityType: schema.EntityNote, EntityID: testNotePaper,
		Summary: "窗口末尾", Actor: schema.ActorUser, At: period.EndExclusive.Add(-time.Millisecond),
	})
	env.emit(events.Event{
		Type: schema.EventEntityCreated, EntityType: schema.EntityNote, EntityID: testNotePaper,
		Summary: "窗口之外", Actor: schema.ActorUser, At: period.EndExclusive,
	})

	digest, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: period})
	if err != nil {
		t.Fatalf("CollectActivities: %v", err)
	}
	var summaries []string
	for _, sec := range digest.Sections {
		for _, item := range sec.Items {
			summaries = append(summaries, item.Summary)
		}
	}
	joined := strings.Join(summaries, "|")
	if strings.Contains(joined, "边界前") || strings.Contains(joined, "窗口之外") {
		t.Fatalf("out-of-window events leaked into digest: %s", joined)
	}
	if !strings.Contains(joined, "边界内") || !strings.Contains(joined, "窗口末尾") {
		t.Fatalf("in-window events missing from digest: %s", joined)
	}
}

func TestAC03DigestCountsAndDeterminism(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	env.seedNote(testNotePaper, "paper_note", "论文笔记", false)

	inside := at(t, loc, "2026-11-04 10:00")
	outside := at(t, loc, "2026-10-30 10:00")
	for i := range 32 {
		env.emit(events.Event{
			Type: schema.EventEntityCreated, EntityType: schema.EntityNote, EntityID: testNotePaper,
			Summary: fmt.Sprintf("笔记事件 %d", i), Actor: schema.ActorUser,
			At: inside.Add(time.Duration(i) * time.Minute),
		})
	}
	for i := range 8 {
		env.emit(events.Event{
			Type: schema.EventEntityCreated, EntityType: schema.EntityNote, EntityID: testNotePaper,
			Summary: fmt.Sprintf("窗口外 %d", i), Actor: schema.ActorUser,
			At: outside.Add(time.Duration(i) * time.Minute),
		})
	}
	first, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: period})
	if err != nil {
		t.Fatalf("CollectActivities: %v", err)
	}
	total := 0
	for _, sec := range first.Sections {
		total += len(sec.Items)
	}
	if total != 32 {
		t.Fatalf("digest items = %d, want 32", total)
	}
	if got := first.Stats.CountsByType["entity.created"]; got != 32 {
		t.Fatalf("counts_by_type[entity.created] = %d, want 32", got)
	}
	if got := first.Stats.CountsByActor["user"]; got != 32 {
		t.Fatalf("counts_by_actor[user] = %d, want 32", got)
	}
	second, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: period})
	if err != nil {
		t.Fatalf("CollectActivities second: %v", err)
	}
	if first.DigestHash == "" || first.DigestHash != second.DigestHash {
		t.Fatalf("digest hash not deterministic: %q vs %q", first.DigestHash, second.DigestHash)
	}
}

func TestAC04NoteKindSections(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	env.seedNote(testNotePaper, "paper_note", "论文笔记", false)
	env.seedNote(testNoteScratch, "scratchpad", "灵感笔记", false)
	env.emit(events.Event{Type: schema.EventEntityCreated, EntityType: schema.EntityNote,
		EntityID: testNotePaper, Summary: "新增论文笔记", Actor: schema.ActorUser,
		At: at(t, loc, "2026-11-03 10:00")})
	env.emit(events.Event{Type: schema.EventEntityCreated, EntityType: schema.EntityNote,
		EntityID: testNoteScratch, Summary: "新增笔记", Actor: schema.ActorUser,
		At: at(t, loc, "2026-11-04 10:00")})

	digest, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: period})
	if err != nil {
		t.Fatalf("CollectActivities: %v", err)
	}
	literature := sectionItems(digest, SectionLiterature)
	theory := sectionItems(digest, SectionTheory)
	if len(literature) != 1 || literature[0].Ref.ID != testNotePaper {
		t.Fatalf("literature section = %+v", literature)
	}
	if len(theory) != 1 || theory[0].Ref.ID != testNoteScratch {
		t.Fatalf("theory section = %+v", theory)
	}
}

func TestAC05NoteUpdatedMergedRow(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	env.seedNote(testNoteScratch, "scratchpad", "灵感笔记", false)
	first := time.Date(2026, 11, 4, 2, 0, 0, 0, time.UTC)
	second := first.Add(2 * time.Hour)
	third := second.Add(2 * time.Hour)
	for _, saveAt := range []time.Time{first, second, third} {
		env.emitNoteUpdated(events.NoteUpdatedEvent{
			NoteID: testNoteScratch, Summary: "更新笔记", Actor: schema.ActorUser, At: saveAt,
		})
	}
	digest, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: period})
	if err != nil {
		t.Fatalf("CollectActivities: %v", err)
	}
	items := sectionItems(digest, SectionTheory)
	if len(items) != 1 {
		t.Fatalf("note.updated must stay one merged row, got %d", len(items))
	}
	if items[0].Detail != "3 次保存" {
		t.Fatalf("detail = %q, want 3 次保存", items[0].Detail)
	}
	if !items[0].FirstAt.Equal(first) {
		t.Fatalf("first_at = %s, want %s", items[0].FirstAt, first)
	}
	if !items[0].LastAt.Equal(third) {
		t.Fatalf("last_at = %s, want %s", items[0].LastAt, third)
	}
}

func TestAC06DeletedTargetStillVisible(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	env.seedNote(testNoteDeleted, "scratchpad", "已删笔记", true)
	env.emit(events.Event{Type: schema.EventEntityDeleted, EntityType: schema.EntityNote,
		EntityID: testNoteDeleted, Summary: "移入回收站", Actor: schema.ActorUser,
		At: at(t, loc, "2026-11-04 10:00")})

	digest, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: period})
	if err != nil {
		t.Fatalf("CollectActivities: %v", err)
	}
	items := sectionItems(digest, SectionTheory)
	if len(items) != 1 {
		t.Fatalf("deleted target item missing: %+v", items)
	}
	if !items[0].Broken || !strings.Contains(items[0].Summary, "目标已删除") {
		t.Fatalf("deleted target not marked broken: %+v", items[0])
	}
}

func TestAC07Truncation(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now: time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{
			"weekly.timezone":          `"Asia/Shanghai"`,
			"weekly.max_digest_events": "2000",
		},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	base := at(t, loc, "2026-11-03 00:00")
	err := env.handle.InTx(env.ctx, func(tx *sql.Tx) error {
		for i := range 100 {
			ev := events.Event{
				Type: schema.EventStatusChanged, EntityType: schema.EntityIdea, EntityID: testIdea,
				Old: &events.Object{"status": "open"}, New: &events.Object{"status": "confirmed"},
				Summary: fmt.Sprintf("状态变更 %d", i), Actor: schema.ActorUser,
				At: base.Add(-time.Hour).Add(time.Duration(i) * time.Second),
			}
			if err := env.handle.Events().Emit(env.ctx, tx, ev); err != nil {
				return err
			}
		}
		for i := range 5000 {
			ev := events.Event{
				Type: schema.EventEntityCreated, EntityType: schema.EntityNote, EntityID: testNotePaper,
				Summary: fmt.Sprintf("笔记事件 %d", i), Actor: schema.ActorUser,
				At: base.Add(time.Duration(i) * time.Second),
			}
			if err := env.handle.Events().Emit(env.ctx, tx, ev); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed events: %v", err)
	}
	digest, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: period})
	if err != nil {
		t.Fatalf("CollectActivities: %v", err)
	}
	if !digest.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	total := 0
	for _, sec := range digest.Sections {
		total += len(sec.Items)
		if len(sec.Items) > maxSectionItems {
			t.Fatalf("section %s has %d items, cap is %d", sec.Key, len(sec.Items), maxSectionItems)
		}
	}
	if total > 2000 {
		t.Fatalf("kept items = %d, max is 2000", total)
	}
	// status.changed outranks entity.created, so the oldest status events
	// survive truncation and collapse into three transitions plus a fold.
	idea := sectionItems(digest, SectionIdea)
	if len(idea) != 4 || !strings.Contains(idea[3].Summary, "状态变更折叠") {
		t.Fatalf("status.changed truncation/collapse = %+v", idea)
	}
	if got := len(sectionItems(digest, SectionTheory)); got != maxSectionItems {
		t.Fatalf("theory items = %d, want the per-section cap %d", got, maxSectionItems)
	}
	if got := digest.Stats.CountsByType["entity.created"]; got != 5000 {
		t.Fatalf("counts_by_type reflects whole window: %d", got)
	}
	found := false
	for _, warning := range digest.Warnings {
		if strings.Contains(warning, "截断") {
			found = true
		}
	}
	if !found {
		t.Fatalf("truncation warning missing: %v", digest.Warnings)
	}
	second, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: period})
	if err != nil {
		t.Fatalf("CollectActivities second: %v", err)
	}
	if digest.DigestHash != second.DigestHash {
		t.Fatalf("truncated digest hash not deterministic")
	}
}

func TestAC24UnknownEntityType(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	env.insertRawActivity("0192f2a0-0000-7000-8000-0000000000f9", "entity.created",
		"mystery_type", "0192f2a0-0000-7000-8000-0000000000f8",
		"未知实体事件", at(t, loc, "2026-11-04 10:00"))

	digest, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: period})
	if err != nil {
		t.Fatalf("CollectActivities: %v", err)
	}
	work := sectionItems(digest, SectionWork)
	if len(work) != 1 || work[0].Ref.Type != "mystery_type" {
		t.Fatalf("unknown entity not routed to work: %+v", work)
	}
	warned := false
	for _, warning := range digest.Warnings {
		if strings.Contains(warning, "mystery_type") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("missing unknown-type warning: %v", digest.Warnings)
	}
}

func TestAC25TaskStatusObjectNormalization(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	env.seedColumn("Doing", "todo", false, 1)
	env.seedColumn("Done", "done", true, 2)
	env.seedTask(taskSeed{ID: testTaskDone, Title: "完成动力学推导", Column: "Done", Completed: true})
	env.seedTask(taskSeed{ID: testTaskBlocked, Title: "核对边界条件", Column: "Doing", Blocked: true})
	env.seedTask(taskSeed{ID: testTaskArchived, Title: "旧版模拟脚本", Column: "Doing", Archived: true})

	env.emit(events.Event{
		Type: schema.EventStatusChanged, EntityType: schema.EntityTask, EntityID: testTaskDone,
		Old:   &events.Object{"column_name": "Doing", "is_done": false, "blocked": false},
		New:   &events.Object{"column_name": "Done", "is_done": true, "blocked": false},
		Actor: schema.ActorUser, At: at(t, loc, "2026-11-03 10:00"),
	})
	env.emit(events.Event{
		Type: schema.EventStatusChanged, EntityType: schema.EntityTask, EntityID: testTaskBlocked,
		Old:   &events.Object{"column_name": "Doing", "is_done": false, "blocked": false},
		New:   &events.Object{"column_name": "Doing", "is_done": false, "blocked": true},
		Actor: schema.ActorUser, At: at(t, loc, "2026-11-04 10:00"),
	})
	env.emit(events.Event{
		Type: schema.EventStatusChanged, EntityType: schema.EntityTask, EntityID: testTaskArchived,
		Old:   &events.Object{"column_name": "Doing", "is_done": false, "blocked": false, "archived": false},
		New:   &events.Object{"column_name": "Doing", "is_done": false, "blocked": false, "archived": true},
		Actor: schema.ActorUser, At: at(t, loc, "2026-11-05 10:00"),
	})

	digest, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: period})
	if err != nil {
		t.Fatalf("CollectActivities: %v", err)
	}
	work := sectionItems(digest, SectionWork)
	problem := sectionItems(digest, SectionProblem)
	if len(work) != 2 {
		t.Fatalf("work items = %d, want 2: %+v", len(work), work)
	}
	var completion *DigestItem
	for i := range work {
		if work[i].Ref.ID == testTaskDone {
			completion = &work[i]
		}
		if strings.Contains(work[i].Summary, "{") || strings.Contains(work[i].Old, "{") {
			t.Fatalf("raw JSON leaked into digest: %+v", work[i])
		}
	}
	if completion == nil || !strings.Contains(completion.Summary, "完成任务") ||
		!strings.Contains(completion.Summary, "Done") {
		t.Fatalf("completion summary = %+v", completion)
	}
	if len(problem) != 1 || !strings.Contains(problem[0].Summary, "标记受阻") {
		t.Fatalf("problem section = %+v", problem)
	}
	warned := false
	for _, warning := range digest.Warnings {
		if strings.Contains(warning, "归档") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("archive warning missing: %v", digest.Warnings)
	}
}

func TestAC26PlannedCandidates(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now: time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{
			"weekly.week_start_day": "1",
			"weekly.timezone":       `"Asia/Shanghai"`,
		},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	env.seedColumn("Doing", "todo", false, 1)
	env.seedColumn("Done", "done", true, 2)
	env.seedTask(taskSeed{ID: testTaskPlanA, Title: "任务 A", Column: "Doing", WeekPlan: "2026-W46"})
	env.seedTask(taskSeed{ID: testTaskPlanB, Title: "任务 B", Column: "Doing", Deadline: "2026-11-10"})
	env.seedTask(taskSeed{ID: testTaskPlanC, Title: "任务 C", Column: "Doing",
		WeekPlan: "2026-W46", Archived: true})
	env.seedTask(taskSeed{ID: testTaskDone, Title: "任务 D", Column: "Done",
		WeekPlan: "2026-W46", Completed: true})

	digest, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: period})
	if err != nil {
		t.Fatalf("CollectActivities: %v", err)
	}
	origins := map[string]string{}
	for _, task := range digest.Planned {
		origins[task.Ref.ID] = task.Origin
	}
	if origins[testTaskPlanA] != "week_plan" {
		t.Fatalf("A origin = %q, want week_plan", origins[testTaskPlanA])
	}
	if origins[testTaskPlanB] != "deadline" {
		t.Fatalf("B origin = %q, want deadline", origins[testTaskPlanB])
	}
	if _, ok := origins[testTaskPlanC]; ok {
		t.Fatal("archived task C must not be a planned candidate")
	}
	if _, ok := origins[testTaskDone]; ok {
		t.Fatal("completed task D must not be a planned candidate")
	}
}

func TestAC27CustomWeekISOAlignment(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now: time.Date(2026, 11, 15, 12, 0, 0, 0, loc),
		settings: map[string]string{
			"weekly.week_start_day": "0",
			"weekly.timezone":       `"Asia/Shanghai"`,
		},
	})
	period := periodOf(t, loc, "2026-11-08", "2026-11-14")
	env.seedColumn("Doing", "todo", false, 1)
	env.seedTask(taskSeed{ID: testTaskPlanA, Title: "下周候选", Column: "Doing", WeekPlan: "2026-W46"})

	digest, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: period})
	if err != nil {
		t.Fatalf("CollectActivities: %v", err)
	}
	if len(digest.Planned) != 1 || digest.Planned[0].Ref.ID != testTaskPlanA {
		t.Fatalf("planned = %+v, want the W46 candidate", digest.Planned)
	}
	found := false
	for _, warning := range digest.Warnings {
		if strings.Contains(warning, "2026-W46") && strings.Contains(warning, "不对齐") {
			found = true
		}
	}
	if !found {
		t.Fatalf("custom week alignment warning missing: %v", digest.Warnings)
	}
}

// seedGoldenDigest builds the deterministic fixture shared by the digest and
// skeleton goldens.
func seedGoldenDigest(t *testing.T) (*testEnv, Period) {
	t.Helper()
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:         time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		projectName: "Alpha",
		settings: map[string]string{
			"weekly.week_start_day": "1",
			"weekly.timezone":       `"Asia/Shanghai"`,
		},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	env.seedNote(testNotePaper, "paper_note", "论文笔记", false)
	env.seedNote(testNoteScratch, "scratchpad", "灵感笔记", false)
	env.seedColumn("Doing", "todo", false, 1)
	env.seedColumn("Done", "done", true, 2)
	env.seedTask(taskSeed{ID: testTaskDone, Title: "完成动力学推导", Column: "Done", Completed: true})
	env.seedTask(taskSeed{ID: testTaskBlocked, Title: "核对边界条件", Column: "Doing", Blocked: true})
	env.seedTask(taskSeed{ID: testTaskPlanA, Title: "补写方法章节", Column: "Doing", WeekPlan: "2026-W46"})

	out, err := env.outputs.CreateDraft(env.ctx, aioutput.CreateDraftInput{
		ProjectID: testProjectID,
		TaskKind:  "experiment_results",
		Entity:    &aioutput.EntityRef{Type: string(schema.EntityExperiment), ID: testExperiment},
		ContentMD: "结果分析草稿",
		Provenance: aioutput.Provenance{
			ProviderID: "provider-1", ProviderName: "Fake Provider",
			Protocol: provider.ProtocolOpenAIChat, ModelID: "fake-model",
			PromptVersion: "experiment_results/v1", GeneratedAt: env.clock.Now(),
		},
	})
	if err != nil {
		t.Fatalf("seed ai output: %v", err)
	}
	env.exec(`INSERT INTO snapshots(id, target_type, target_id, ai_output_id, created_at)
	          VALUES (?, 'experiment', ?, ?, ?)`,
		testSnapshot, testExperiment, out.ID, clock.Format(env.clock.Now()))

	env.emit(events.Event{Type: schema.EventEntityCreated, EntityType: schema.EntityNote,
		EntityID: testNotePaper, Summary: "新增论文笔记", Actor: schema.ActorUser,
		At: at(t, loc, "2026-11-02 10:00")})
	env.emitNoteUpdated(events.NoteUpdatedEvent{
		NoteID: testNoteScratch, Summary: "更新笔记", Actor: schema.ActorUser,
		At: at(t, loc, "2026-11-03 09:00"),
	})
	env.emit(events.Event{
		Type: schema.EventStatusChanged, EntityType: schema.EntityTask, EntityID: testTaskDone,
		Old:   &events.Object{"column_name": "Doing", "is_done": false, "blocked": false},
		New:   &events.Object{"column_name": "Done", "is_done": true, "blocked": false},
		Actor: schema.ActorUser, At: at(t, loc, "2026-11-04 15:00"),
	})
	env.emit(events.Event{
		Type: schema.EventStatusChanged, EntityType: schema.EntityTask, EntityID: testTaskBlocked,
		Old:   &events.Object{"column_name": "Doing", "is_done": false, "blocked": false},
		New:   &events.Object{"column_name": "Doing", "is_done": false, "blocked": true},
		Actor: schema.ActorUser, At: at(t, loc, "2026-11-05 10:00"),
	})
	env.emit(events.Event{Type: schema.EventPaperImported, EntityType: schema.EntityPaper,
		EntityID: testPaper, Summary: "导入文献：Some Paper", Actor: schema.ActorUser,
		At: at(t, loc, "2026-11-06 09:30")})
	env.emit(events.Event{Type: schema.EventAIAnalysisCompleted, EntityType: schema.EntitySnapshot,
		EntityID: testSnapshot, Summary: "AI 分析完成（Results）", Actor: schema.ActorAI,
		New: &events.Object{"kind": "results", "target_type": "experiment", "target_id": testExperiment},
		At:  at(t, loc, "2026-11-07 16:00")})
	return env, period
}

type goldenItem struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	State       string `json:"state"`
	Summary     string `json:"summary"`
	Detail      string `json:"detail"`
	Old         string `json:"old"`
	New         string `json:"new"`
	Actor       string `json:"actor"`
	FirstAt     string `json:"first_at"`
	LastAt      string `json:"last_at"`
	Broken      bool   `json:"broken"`
	Unconfirmed bool   `json:"unconfirmed_source"`
}

type goldenSection struct {
	Key   string       `json:"key"`
	Items []goldenItem `json:"items"`
}

type goldenBlocked struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Title  string `json:"title"`
	Column string `json:"column_name"`
	IsDone bool   `json:"is_done"`
	Reason string `json:"reason"`
}

type goldenPlanned struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Column   string `json:"column_name"`
	IsDone   bool   `json:"is_done"`
	Deadline string `json:"deadline,omitempty"`
	WeekPlan string `json:"week_plan,omitempty"`
	Origin   string `json:"origin"`
}

type goldenDigest struct {
	PeriodStart     string          `json:"period_start"`
	PeriodEnd       string          `json:"period_end"`
	Timezone        string          `json:"timezone"`
	CountsByType    map[string]int  `json:"counts_by_type"`
	CountsBySection map[string]int  `json:"counts_by_section"`
	Sections        []goldenSection `json:"sections"`
	Blocked         []goldenBlocked `json:"blocked"`
	Planned         []goldenPlanned `json:"planned"`
	Warnings        []string        `json:"warnings"`
	Truncated       bool            `json:"truncated"`
}

func digestGoldenJSON(t *testing.T, digest *ActivityDigest) string {
	t.Helper()
	g := goldenDigest{
		PeriodStart:     digest.Period.StartDate(),
		PeriodEnd:       digest.Period.EndDate(),
		Timezone:        digest.Timezone,
		CountsByType:    digest.Stats.CountsByType,
		CountsBySection: map[string]int{},
		Sections:        []goldenSection{},
		Blocked:         []goldenBlocked{},
		Planned:         []goldenPlanned{},
		Warnings:        digest.Warnings,
		Truncated:       digest.Truncated,
	}
	if g.Warnings == nil {
		g.Warnings = []string{}
	}
	for key, count := range digest.Stats.CountsBySection {
		g.CountsBySection[string(key)] = count
	}
	for _, sec := range digest.Sections {
		gs := goldenSection{Key: string(sec.Key), Items: []goldenItem{}}
		for _, item := range sec.Items {
			gs.Items = append(gs.Items, goldenItem{
				Type: item.Ref.Type, ID: item.Ref.ID, Kind: item.Kind, State: item.State,
				Summary: item.Summary, Detail: item.Detail, Old: item.Old, New: item.New,
				Actor: item.Actor, FirstAt: item.FirstAt.Format(time.RFC3339),
				LastAt: item.LastAt.Format(time.RFC3339), Broken: item.Broken,
				Unconfirmed: item.UnconfirmedSource,
			})
		}
		g.Sections = append(g.Sections, gs)
	}
	for _, task := range digest.Blocked {
		g.Blocked = append(g.Blocked, goldenBlocked{
			Type: task.Ref.Type, ID: task.Ref.ID, Title: task.Title,
			Column: task.ColumnName, IsDone: task.IsDone, Reason: task.Reason,
		})
	}
	for _, task := range digest.Planned {
		gp := goldenPlanned{
			Type: task.Ref.Type, ID: task.Ref.ID, Title: task.Title,
			Column: task.ColumnName, IsDone: task.IsDone, Origin: task.Origin,
		}
		if task.Deadline != nil {
			gp.Deadline = task.Deadline.Format(dateLayout)
		}
		if task.WeekPlan != nil {
			gp.WeekPlan = *task.WeekPlan
		}
		g.Planned = append(g.Planned, gp)
	}
	raw, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden digest: %v", err)
	}
	return string(raw) + "\n"
}

func TestGoldenDigest(t *testing.T) {
	env, period := seedGoldenDigest(t)
	digest, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: period})
	if err != nil {
		t.Fatalf("CollectActivities: %v", err)
	}
	got := digestGoldenJSON(t, digest)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		writeGolden(t, "digest_week.json", got)
		return
	}
	if want := readGolden(t, "digest_week.json"); got != want {
		t.Fatalf("digest golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestGoldenSkeleton(t *testing.T) {
	env, period := seedGoldenDigest(t)
	digest, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: period})
	if err != nil {
		t.Fatalf("CollectActivities: %v", err)
	}
	got := BuildSkeleton("Alpha", period, digest)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		writeGolden(t, "skeleton.md", got)
		return
	}
	if want := readGolden(t, "skeleton.md"); got != want {
		t.Fatalf("skeleton golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestSkeletonEmptyPeriod(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	empty := &ActivityDigest{Period: period}
	got := BuildSkeleton("Alpha", period, empty)
	for _, sec := range defaultTemplate.Sections {
		heading := fmt.Sprintf("## %d. %s", sec.Num, sec.Title)
		if !strings.Contains(got, heading) {
			t.Fatalf("skeleton missing %q", heading)
		}
	}
	if strings.Count(got, "本周期无记录") != len(defaultTemplate.Sections) {
		t.Fatalf("empty skeleton must say 本周期无记录 once per section:\n%s", got)
	}
}

func TestUTCTimezoneBoundary(t *testing.T) {
	loc := mustLocation(t, "UTC")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"UTC"`},
	})
	period, err := env.svc.DefaultPeriod(env.ctx)
	if err != nil {
		t.Fatalf("DefaultPeriod: %v", err)
	}
	if got, want := period.Start.Format(time.RFC3339), "2026-11-09T00:00:00Z"; got != want {
		t.Fatalf("UTC week start = %s, want %s", got, want)
	}
	window := periodOf(t, loc, "2026-11-09", "2026-11-15")
	env.emit(events.Event{Type: schema.EventEntityCreated, EntityType: schema.EntityNote,
		EntityID: testNotePaper, Summary: "UTC 边界前", Actor: schema.ActorUser,
		At: window.Start.Add(-time.Second)})
	env.emit(events.Event{Type: schema.EventEntityCreated, EntityType: schema.EntityNote,
		EntityID: testNotePaper, Summary: "UTC 边界内", Actor: schema.ActorUser,
		At: window.Start})
	digest, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: window})
	if err != nil {
		t.Fatalf("CollectActivities: %v", err)
	}
	items := sectionItems(digest, SectionTheory)
	if len(items) != 1 || items[0].Summary != "UTC 边界内" {
		t.Fatalf("UTC boundary items = %+v", items)
	}
}
