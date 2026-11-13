package overview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/ids"
	"researchos/internal/ports"
)

func TestStatusWritableRule(t *testing.T) {
	// §5.8: only "archived" blocks content writes. The global schema CHECK
	// constrains the column, so the rule is asserted directly.
	if !statusWritable("active") {
		t.Fatal("active must be writable")
	}
	if statusWritable("archived") {
		t.Fatal("archived must be read-only")
	}
	if !statusWritable("paused") {
		t.Fatal("an unknown status stays writable for reconcile to report")
	}
}

func TestFocusFallsBackToGoals(t *testing.T) {
	e := newEnv(t, nil)
	goals := strings.Repeat("目标", 150) // 300 runes
	e.setFocus(goals, "")
	e.idea("进行中的想法", "Researching", -time.Hour, false)

	snap := e.load()
	focus := snap.CurrentFocus
	if focus.FocusMD != "" {
		t.Fatalf("FocusMD = %q, want empty", focus.FocusMD)
	}
	if !strings.HasSuffix(focus.GoalsExcerpt, "…") {
		t.Fatalf("GoalsExcerpt = %q, want a truncation marker", focus.GoalsExcerpt)
	}
	if got := len([]rune(focus.GoalsExcerpt)); got != goalsExcerptRunes {
		t.Fatalf("GoalsExcerpt is %d runes, want %d", got, goalsExcerptRunes)
	}
	if !equalRunes([]rune(goals)[:goalsExcerptRunes-1], []rune(focus.GoalsExcerpt)[:goalsExcerptRunes-1]) {
		t.Fatalf("GoalsExcerpt is not a prefix of goals: %q", focus.GoalsExcerpt)
	}
	if len(focus.TopIdeas) != 1 {
		t.Fatalf("TopIdeas = %d, want 1", len(focus.TopIdeas))
	}
	st := e.state(snap, WidgetCurrentFocus)
	if st.Degraded || st.PendingConfirmation || !st.Available {
		t.Fatalf("an empty focus_md is a fallback, not a failure: %+v", st)
	}
	if snap.Project.Goals != goals {
		t.Fatal("Project.Goals must stay verbatim")
	}
}

func TestFocusPrefersFocusMD(t *testing.T) {
	e := newEnv(t, nil)
	e.setFocus("短目标", "## 本周焦点\n\n聚焦跨工况误报。")
	snap := e.load()
	if snap.CurrentFocus.FocusMD != "## 本周焦点\n\n聚焦跨工况误报。" {
		t.Fatalf("FocusMD = %q", snap.CurrentFocus.FocusMD)
	}
	if snap.CurrentFocus.GoalsExcerpt != "短目标" {
		t.Fatalf("GoalsExcerpt = %q", snap.CurrentFocus.GoalsExcerpt)
	}
}

func TestRecentExperimentsPendingWithoutRuns(t *testing.T) {
	e := newEnv(t, nil)
	e.experiment("只有实验没有运行", -time.Hour, false)

	snap := e.load()
	if len(snap.RecentExperiments) != 1 {
		t.Fatalf("RecentExperiments = %+v", snap.RecentExperiments)
	}
	card := snap.RecentExperiments[0]
	if card.LastRunStatus != "—" || card.RunCount != 0 {
		t.Fatalf("ExperimentCard = %+v, want the em-dash placeholder", card)
	}
	st := e.state(snap, WidgetRecentExperiments)
	if !st.PendingConfirmation || st.Reason != "pending_confirmation" || st.Degraded {
		t.Fatalf("state = %+v, want pending without any run", st)
	}
}

// fakePaperIndex is a stub SPEC-04 port.
type fakePaperIndex struct {
	briefs []ports.PaperBrief
	err    error
	limit  int
}

func (f *fakePaperIndex) ListProjectPaperIDs(context.Context, string) ([]string, error) {
	return nil, nil
}

func (f *fakePaperIndex) CountPaperRefs(context.Context, string, string) (int, error) {
	return 0, nil
}

func (f *fakePaperIndex) ListProjectPapers(_ context.Context, projectID string, limit int) ([]ports.PaperBrief, error) {
	f.limit = limit
	if f.err != nil {
		return nil, f.err
	}
	if projectID == "" {
		return nil, fmt.Errorf("fake: empty project id")
	}
	return f.briefs, nil
}

func TestLiteratureUsesPaperIndexPort(t *testing.T) {
	index := &fakePaperIndex{briefs: []ports.PaperBrief{
		{ID: "paper-2", Title: "第二篇", Authors: "李四", Year: 2025, Missing: true},
		{ID: "paper-1", Title: "第一篇", Authors: "张三", Year: 2024},
	}}
	e := newEnv(t, func(o *Options) { o.PaperIndex = index })
	// The fallback table is populated too: the registered port must win.
	e.link(e.paper(`{"title":"回退条目"}`, "library/papers/x/source.pdf", "ok"), -time.Hour)

	snap := e.load()
	if index.limit != widgetLimit {
		t.Fatalf("port limit = %d, want %d", index.limit, widgetLimit)
	}
	if len(snap.Literature) != 2 || snap.Literature[0].ID != "paper-2" {
		t.Fatalf("Literature = %+v", snap.Literature)
	}
	if !snap.Literature[0].Missing || snap.Literature[1].Missing {
		t.Fatalf("Missing flags = %+v", snap.Literature)
	}
	if st := e.state(snap, WidgetLiterature); !st.Available || st.Degraded {
		t.Fatalf("state = %+v", st)
	}
}

func TestLiteraturePortFailureDegradesTheCard(t *testing.T) {
	index := &fakePaperIndex{err: fmt.Errorf("zotero offline")}
	e := newEnv(t, func(o *Options) { o.PaperIndex = index })
	e.link(e.paper(`{"title":"存在的关联"}`, "library/papers/x/source.pdf", "ok"), -time.Hour)

	snap := e.load()
	st := e.state(snap, WidgetLiterature)
	if !st.Degraded || !st.Available {
		t.Fatalf("state = %+v, want a degraded but visible card", st)
	}
	if len(snap.Literature) != 0 {
		t.Fatalf("Literature = %+v, want no invented data", snap.Literature)
	}
}

func TestLiteratureFallsBackToProjectPapers(t *testing.T) {
	e := newEnv(t, nil) // no PaperIndex
	meta := func(title, creators, date string) string {
		b, err := json.Marshal(map[string]any{
			"title":    title,
			"creators": json.RawMessage(creators),
			"date":     date,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(b)
	}
	p1 := e.paper(meta("旧论文", `[{"lastName":"Wu","firstName":"Jian"}]`, "2023-05-01"),
		"library/papers/one/source.pdf", "ok")
	p2 := e.paper(meta("机构作者论文", `[{"name":"DeepMind"}]`, "2026"),
		"library/papers/two/source.pdf", "ok")
	p3 := e.paper(`{"title":"坏 JSON 也不得让组件失败"`, "", "missing")
	p4 := e.paper(meta("逃逸路径", `[]`, "unknown"), "../outside.pdf", "ok")
	dangling := ids.MustNew(e.clock)
	e.link(p1, -72*time.Hour)
	e.link(p2, -48*time.Hour)
	e.link(p3, -24*time.Hour)
	e.link(p4, -1*time.Hour)
	e.link(dangling, -12*time.Hour)
	// A sixth link is cut by the limit of five.
	e.link(e.paper(`{"title":"第六篇"}`, "library/papers/six/source.pdf", "ok"), -200*time.Hour)

	snap := e.load()
	if len(snap.Literature) != widgetLimit {
		t.Fatalf("Literature = %d entries, want %d: %+v", len(snap.Literature), widgetLimit, snap.Literature)
	}
	wantOrder := []string{p4, dangling, p3, p2, p1}
	for i, want := range wantOrder {
		if got := snap.Literature[i].ID; got != want {
			t.Fatalf("Literature[%d].ID = %s, want %s (linked_at DESC)", i, got, want)
		}
	}
	byID := map[string]PaperCard{}
	for _, c := range snap.Literature {
		byID[c.ID] = c
	}
	if c := byID[p4]; !c.Missing || c.Title != "逃逸路径" || c.Year != 0 || c.Authors != "" {
		t.Errorf("path outside the workspace must report Missing: %+v", c)
	}
	if c := byID[p1]; c.Title != "旧论文" || c.Authors != "Jian Wu" || c.Year != 2023 || c.Missing {
		t.Errorf("named creator = %+v", c)
	}
	if c := byID[p2]; c.Authors != "DeepMind" || c.Year != 2026 || c.Missing {
		t.Errorf("institutional creator = %+v", c)
	}
	if c := byID[p3]; !c.Missing || c.Title != "" || c.Year != 0 {
		t.Errorf("malformed metadata must yield blanks, not fail: %+v", c)
	}
	if c := byID[dangling]; c.ID == "" || !c.Missing {
		t.Errorf("dangling link = %+v, want a missing entry", c)
	}
	if st := e.state(snap, WidgetLiterature); !st.Available || st.Degraded {
		t.Fatalf("state = %+v", st)
	}
}

func TestLiteratureIssuedYearAndCSLAuthors(t *testing.T) {
	e := newEnv(t, nil)
	id := e.paper(`{"title":"CSL style","author":[{"literal":"A. Team"},{"lastName":"Lee","firstName":"Bo"}],"issued":{"date-parts":[[2021,3]]}}`,
		"library/papers/x/source.pdf", "ok")
	e.link(id, -time.Hour)

	snap := e.load()
	if len(snap.Literature) != 1 {
		t.Fatalf("Literature = %+v", snap.Literature)
	}
	card := snap.Literature[0]
	if card.Year != 2021 {
		t.Errorf("Year = %d, want 2021", card.Year)
	}
	if card.Authors != "A. Team, Bo Lee" {
		t.Errorf("Authors = %q", card.Authors)
	}
	if card.Missing {
		t.Errorf("Missing = true for an in-workspace copy")
	}
}

func TestLiteratureHidesWithoutAnySource(t *testing.T) {
	// No PaperIndex and no project.db: neither source answers, so the card is
	// hidden without failing Load (§5.6).
	e := newEnv(t, nil)
	pid := registerWithoutData(t, e)
	snap, err := e.svc.Load(context.Background(), pid)
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	st := e.state(snap, WidgetLiterature)
	if st.Available {
		t.Fatalf("state = %+v, want Available=false", st)
	}
	if len(snap.Literature) != 0 {
		t.Fatalf("Literature = %+v, want nothing shown", snap.Literature)
	}
}

func TestWeeklyProgressPrefersConfirmedReport(t *testing.T) {
	e := newEnv(t, nil)
	e.activity("entity.created", "task", "t-1", e.at(-time.Hour))
	body := strings.Repeat("本周完成了基线复现。", 100)
	current := e.weeklyReport("2026-03-02", "2026-03-08", "confirmed", body, -30*time.Minute)
	// Last week's confirmed draft and this week's draft must not win.
	e.weeklyReport("2026-02-23", "2026-03-01", "confirmed", "上周的稿子", -3*time.Hour)
	e.weeklyReport("2026-03-02", "2026-03-08", "draft", "草稿", -10*time.Minute)

	snap := e.load()
	wp := snap.WeeklyProgress
	if wp.ReportID != current || !wp.Confirmed {
		t.Fatalf("WeeklyProgress = %+v, want the report of the current week", wp)
	}
	if len([]rune(wp.Excerpt)) != reportExcerptRunes || !strings.HasSuffix(wp.Excerpt, "…") {
		t.Fatalf("Excerpt length = %d, want %d plus a marker", len([]rune(wp.Excerpt)), reportExcerptRunes)
	}
	if wp.ActivityCounts != nil {
		t.Fatalf("ActivityCounts = %v, want nil when a confirmed draft exists", wp.ActivityCounts)
	}
	if st := e.state(snap, WidgetWeeklyProgress); st.PendingConfirmation {
		t.Fatalf("a confirmed weekly report is confirmed data: %+v", st)
	}
}

func TestWeeklyProgressIgnoresNonOverlappingPeriods(t *testing.T) {
	e := newEnv(t, nil)
	// period_end is the day before the week starts → no overlap.
	e.weeklyReport("2026-02-16", "2026-03-01", "confirmed", "上周", -time.Hour)
	e.activity("entity.created", "note", "n-1", e.at(-time.Hour))

	snap := e.load()
	if snap.WeeklyProgress.Confirmed {
		t.Fatalf("WeeklyProgress = %+v, must not show last week's report", snap.WeeklyProgress)
	}
	if snap.WeeklyProgress.ActivityCounts["note"] != 1 {
		t.Fatalf("ActivityCounts = %v", snap.WeeklyProgress.ActivityCounts)
	}
	if st := e.state(snap, WidgetWeeklyProgress); !st.PendingConfirmation || st.Reason != "pending_confirmation" {
		t.Fatalf("state = %+v, want the counter fallback to be pending", st)
	}
}

func TestWeeklyProgressPicksLatestOverlappingDraft(t *testing.T) {
	e := newEnv(t, nil)
	// Two confirmed drafts overlapping the week: the later period_start wins.
	e.weeklyReport("2026-02-26", "2026-03-03", "confirmed", "跨周的旧稿", -5*time.Hour)
	latest := e.weeklyReport("2026-03-02", "2026-03-08", "confirmed", "本周稿", -4*time.Hour)

	snap := e.load()
	if snap.WeeklyProgress.ReportID != latest {
		t.Fatalf("ReportID = %s, want %s", snap.WeeklyProgress.ReportID, latest)
	}
}

func TestWeeklyWindowUsesClockZone(t *testing.T) {
	// Wednesday 01:00 in UTC+8 is Tuesday 17:00 UTC, so the ISO week starts at
	// Monday 2026-03-02 00:00+08:00 = Sunday 2026-03-01 16:00Z.
	e := newEnv(t, func(o *Options) {
		o.Clock = &clock.Fixed{T: time.Date(2026, 3, 4, 1, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))}
	})
	e.activity("entity.created", "idea", "i-in", clock.Format(time.Date(2026, 3, 1, 16, 30, 0, 0, time.UTC)))
	e.activity("entity.created", "idea", "i-out", clock.Format(time.Date(2026, 3, 1, 15, 30, 0, 0, time.UTC)))
	e.activity("entity.created", "task", "t-next", clock.Format(time.Date(2026, 3, 8, 16, 30, 0, 0, time.UTC)))

	snap := e.load()
	if got := snap.WeeklyProgress.ActivityCounts; got["idea"] != 1 || got["task"] != 0 {
		t.Fatalf("ActivityCounts = %v, want only the event inside the local week", got)
	}
}

func TestWeeklyActivityCountsAreCappedAndScoped(t *testing.T) {
	e := newEnv(t, nil)
	kinds := []string{"note", "idea", "task", "paper", "experiment", "run", "weekly_report", "evidence"}
	for i, k := range kinds {
		// Descending volume so the limit of six has to drop the smallest rows.
		for range len(kinds) - i {
			e.activity("entity.created", k, fmt.Sprintf("%s-id", k), e.at(-time.Minute))
		}
	}
	e.exec(`UPDATE activities SET created_at = ? WHERE entity_type = 'evidence'`,
		clock.Format(testTime.AddDate(0, 0, -20))) // last month: out of window

	snap := e.load()
	counts := snap.WeeklyProgress.ActivityCounts
	if len(counts) != 6 {
		t.Fatalf("ActivityCounts = %v, want the six busiest entity types", counts)
	}
	if _, ok := counts["evidence"]; ok {
		t.Fatal("events outside the week must not be counted")
	}
	if _, ok := counts["weekly_report"]; ok {
		t.Fatal("the seventh-ranked entity type must be cut")
	}
	if counts["note"] != 8 || counts["run"] != 3 {
		t.Fatalf("counts = %v", counts)
	}
}

func TestTaskDeadlineAndPriorityFallbacks(t *testing.T) {
	e := newEnv(t, nil)
	e.exec(`INSERT INTO tasks(id, title, "column", priority, deadline, blocked, created_at, updated_at, deleted_at)
	        VALUES ('t1', '未知优先级', 'Backlog', 'urgent', '2026-03-10', 0, ?, ?, NULL)`,
		e.at(-time.Hour), e.at(-time.Hour))
	e.exec(`INSERT INTO tasks(id, title, "column", priority, deadline, blocked, created_at, updated_at, deleted_at)
	        VALUES ('t2', '空值', 'Backlog', NULL, NULL, 0, ?, ?, NULL)`,
		e.at(-time.Hour), e.at(-time.Hour))
	e.exec(`INSERT INTO tasks(id, title, "column", priority, deadline, blocked, created_at, updated_at, deleted_at)
	        VALUES ('t3', '大写优先级', 'In Progress', 'HIGH', '2026-03-05T00:00:00.000Z', 0, ?, ?, NULL)`,
		e.at(-time.Hour), e.at(-time.Hour))

	snap := e.load()
	byID := map[string]ports.TaskBrief{}
	for _, b := range snap.MyTasks.Items {
		byID[b.ID] = b
	}
	if len(byID) != 3 {
		t.Fatalf("items = %+v", snap.MyTasks.Items)
	}
	if byID["t1"].Priority != 0 {
		t.Errorf("unknown priority rank = %d, want 0", byID["t1"].Priority)
	}
	if byID["t1"].Deadline == nil || byID["t1"].Deadline.Day() != 10 {
		t.Errorf("bare-date deadline = %v, want the 10th", byID["t1"].Deadline)
	}
	if byID["t2"].Priority != 0 || byID["t2"].Deadline != nil {
		t.Errorf("null row = %+v", byID["t2"])
	}
	if byID["t3"].Priority != 3 {
		t.Errorf("uppercase HIGH rank = %d, want 3", byID["t3"].Priority)
	}
	// t3 (deadline 2026-03-05) precedes t1 (deadline 2026-03-10) and both come
	// before the row without a deadline.
	if got := []string{snap.MyTasks.Items[0].ID, snap.MyTasks.Items[1].ID, snap.MyTasks.Items[2].ID}; !eqStrings(got, []string{"t3", "t1", "t2"}) {
		t.Fatalf("order = %v, want [t3 t1 t2]", got)
	}
}

func TestWidgetsDegradeWhenTheProjectDatabaseIsMissing(t *testing.T) {
	e := newEnv(t, nil)
	pid := registerWithoutData(t, e)
	snap, err := e.svc.Load(context.Background(), pid)
	if err != nil {
		t.Fatalf("Load = %v, want a degraded snapshot instead of an error", err)
	}
	if snap.Project.Name != "残缺项目" || !snap.Writable {
		t.Fatalf("header = %+v writable=%v", snap.Project, snap.Writable)
	}
	for _, w := range widgetOrder {
		if w == WidgetLiterature {
			continue // covered by TestLiteratureHidesWithoutAnySource
		}
		st := e.state(snap, w)
		if !st.Degraded || st.Reason != "query_error" || !st.Available {
			t.Fatalf("%s state = %+v, want visible degradation", w, st)
		}
	}
	// The pending badges of the unconfirmed widgets survive their degradation.
	if !e.state(snap, WidgetMyTasks).PendingConfirmation {
		t.Fatal("MyTasks lost its badge while degraded")
	}
	if !e.state(snap, WidgetBlockedTasks).PendingConfirmation {
		t.Fatal("BlockedTasks lost its badge while degraded")
	}
	// No data was invented for a widget that never answered.
	if len(snap.ActiveIdeas) != 0 || len(snap.RecentNotes) != 0 || snap.MyTasks.Items != nil {
		t.Fatalf("degraded snapshot carries data: %+v", snap)
	}
}

// registerWithoutData inserts a registration row without creating the project
// directory, which is exactly the state reconcile reports later.
func registerWithoutData(t *testing.T, e *env) string {
	t.Helper()
	pid := ids.MustNew(e.clock)
	stamped := clock.Format(e.clock.Now())
	if _, err := e.gdb().Exec(
		`INSERT INTO projects(id, name, summary, goals, focus_md, status, archived_at, created_at, updated_at)
		 VALUES (?, '残缺项目', '', '', '', 'active', NULL, ?, ?)`, pid, stamped, stamped); err != nil {
		t.Fatalf("register project without data: %v", err)
	}
	return pid
}

func TestArchivedProjectIsReadOnly(t *testing.T) {
	e := newEnv(t, func(o *Options) {
		o.Tasks = &fakeTasks{}
		o.Notes = &fakeNotes{}
	})
	e.setProjectStatus("archived", true)
	e.idea("归档项目仍然可读", "Researching", -time.Hour, false)

	snap := e.load()
	if !snap.Project.Archived || snap.Writable {
		t.Fatalf("archived snapshot: %+v writable=%v", snap.Project, snap.Writable)
	}
	if snap.CurrentFocus.FocusEditable {
		t.Fatal("FocusEditable must be false for an archived project")
	}
	if len(snap.ActiveIdeas) != 1 {
		t.Fatalf("ActiveIdeas = %+v, want the read-only snapshot to keep its data", snap.ActiveIdeas)
	}
	for _, st := range snap.Widgets {
		if st.Degraded {
			t.Fatalf("%s degraded for an archived project: %+v", st.ID, st)
		}
	}
	if err := e.svc.CompleteTask(context.Background(), e.pid, "t-1"); !isArchived(err) {
		t.Fatalf("CompleteTask = %v, want ErrArchived", err)
	}
	if _, err := e.svc.QuickAddNote(context.Background(), e.pid, QuickNoteInput{Title: "x"}); !isArchived(err) {
		t.Fatalf("QuickAddNote = %v, want ErrArchived", err)
	}
}

func equalRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestWidgetLimitsTruncateAtTheSpecifiedCount checks the §5.6 caps against
// source tables that hold more rows than each card can show.
func TestWidgetLimitsTruncateAtTheSpecifiedCount(t *testing.T) {
	t.Run("my tasks", func(t *testing.T) {
		e := newEnv(t, nil)
		// Distinct deadlines a day apart: deadline ascending is also insertion
		// order, so the cut is predictable.
		for i := 1; i <= myTasksLimit+2; i++ {
			due := testTime.AddDate(0, 0, i)
			e.task(fmt.Sprintf("任务 %02d", i), "Backlog", "medium", &due, false,
				-time.Duration(i)*time.Hour)
		}
		if got := e.scalar(`SELECT count(*) FROM tasks WHERE deleted_at IS NULL`); got != myTasksLimit+2 {
			t.Fatalf("source rows = %d, want %d", got, myTasksLimit+2)
		}
		snap := e.load()
		if got := len(snap.MyTasks.Items); got != myTasksLimit {
			t.Fatalf("MyTasks = %d items, want %d", got, myTasksLimit)
		}
		for i, item := range snap.MyTasks.Items {
			if want := fmt.Sprintf("任务 %02d", i+1); item.Title != want {
				t.Fatalf("MyTasks[%d] = %q, want %q", i, item.Title, want)
			}
		}
	})

	t.Run("blocked tasks", func(t *testing.T) {
		e := newEnv(t, nil)
		for i := 1; i <= widgetLimit+2; i++ {
			e.task(fmt.Sprintf("受阻 %02d", i), "In Progress", "high", nil, true,
				-time.Duration(i)*time.Hour)
		}
		if got := e.scalar(`SELECT count(*) FROM tasks WHERE blocked = 1 AND deleted_at IS NULL`); got != widgetLimit+2 {
			t.Fatalf("source rows = %d, want %d", got, widgetLimit+2)
		}
		snap := e.load()
		if got := len(snap.BlockedTasks.Items); got != widgetLimit {
			t.Fatalf("BlockedTasks = %d items, want %d", got, widgetLimit)
		}
		for i, item := range snap.BlockedTasks.Items {
			if want := fmt.Sprintf("受阻 %02d", i+1); item.Title != want {
				t.Fatalf("BlockedTasks[%d] = %q, want %q (updated_at DESC)", i, item.Title, want)
			}
		}
	})

	t.Run("experiments", func(t *testing.T) {
		e := newEnv(t, nil)
		for i := 1; i <= widgetLimit+2; i++ {
			e.experiment(fmt.Sprintf("实验 %02d", i), -time.Duration(i)*time.Hour, false)
		}
		if got := e.scalar(`SELECT count(*) FROM experiments WHERE deleted_at IS NULL`); got != widgetLimit+2 {
			t.Fatalf("source rows = %d, want %d", got, widgetLimit+2)
		}
		snap := e.load()
		if got := len(snap.RecentExperiments); got != widgetLimit {
			t.Fatalf("RecentExperiments = %d items, want %d", got, widgetLimit)
		}
		for i, card := range snap.RecentExperiments {
			if want := fmt.Sprintf("实验 %02d", i+1); card.Title != want {
				t.Fatalf("RecentExperiments[%d] = %q, want %q", i, card.Title, want)
			}
		}
	})

	t.Run("notes", func(t *testing.T) {
		e := newEnv(t, nil)
		for i := 1; i <= widgetLimit+2; i++ {
			e.note(fmt.Sprintf("笔记 %02d", i), "scratchpad", -time.Duration(i)*time.Hour, false)
		}
		if got := e.scalar(`SELECT count(*) FROM notes WHERE deleted_at IS NULL`); got != widgetLimit+2 {
			t.Fatalf("source rows = %d, want %d", got, widgetLimit+2)
		}
		snap := e.load()
		if got := len(snap.RecentNotes); got != widgetLimit {
			t.Fatalf("RecentNotes = %d items, want %d", got, widgetLimit)
		}
		for i, card := range snap.RecentNotes {
			if want := fmt.Sprintf("笔记 %02d", i+1); card.Title != want {
				t.Fatalf("RecentNotes[%d] = %q, want %q", i, card.Title, want)
			}
		}
	})

	t.Run("active ideas", func(t *testing.T) {
		e := newEnv(t, nil)
		for i := 1; i <= widgetLimit+2; i++ {
			e.idea(fmt.Sprintf("想法 %02d", i), "Researching", -time.Duration(i)*time.Hour, false)
		}
		if got := e.scalar(`SELECT count(*) FROM ideas WHERE deleted_at IS NULL`); got != widgetLimit+2 {
			t.Fatalf("source rows = %d, want %d", got, widgetLimit+2)
		}
		snap := e.load()
		if got := len(snap.ActiveIdeas); got != widgetLimit {
			t.Fatalf("ActiveIdeas = %d items, want %d", got, widgetLimit)
		}
		for i, card := range snap.ActiveIdeas {
			if want := fmt.Sprintf("想法 %02d", i+1); card.Title != want {
				t.Fatalf("ActiveIdeas[%d] = %q, want %q", i, card.Title, want)
			}
		}
		if got := len(snap.CurrentFocus.TopIdeas); got != focusTopIdeas {
			t.Fatalf("TopIdeas = %d items, want %d", got, focusTopIdeas)
		}
	})
}

// TestTaskTerminalColumnsAreDetected checks the PRAGMA probe: once the SPEC-08
// migration band adds completed_at/archived_at, the widgets must exclude those
// rows without any code change.
func TestTaskTerminalColumnsAreDetected(t *testing.T) {
	e := newEnv(t, nil)
	e.exec(`ALTER TABLE tasks ADD COLUMN completed_at TEXT`)
	e.exec(`ALTER TABLE tasks ADD COLUMN archived_at TEXT`)

	open := e.task("仍然开放", "In Progress", "high", nil, false, -time.Hour)
	completed := e.task("已完成", "Done", "high", nil, false, -2*time.Hour)
	archived := e.task("已归档", "Backlog", "high", nil, false, -3*time.Hour)
	blocked := e.task("受阻但开放", "In Progress", "high", nil, true, -4*time.Hour)
	e.exec(`UPDATE tasks SET completed_at = ? WHERE id = ?`, e.at(-2*time.Hour), completed)
	e.exec(`UPDATE tasks SET archived_at = ? WHERE id = ?`, e.at(-3*time.Hour), archived)

	snap := e.load()
	var ids []string
	for _, item := range snap.MyTasks.Items {
		ids = append(ids, item.ID)
	}
	if !eqStrings(ids, []string{open, blocked}) {
		t.Fatalf("MyTasks ids = %v, want only the two open rows [%s %s]", ids, open, blocked)
	}
	// A blocked task is still open, so both cards show it once.
	if len(snap.BlockedTasks.Items) != 1 || snap.BlockedTasks.Items[0].ID != blocked {
		t.Fatalf("BlockedTasks = %+v, want only the open blocked row", snap.BlockedTasks.Items)
	}
}
