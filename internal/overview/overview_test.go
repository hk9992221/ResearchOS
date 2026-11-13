package overview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	foundationworkspace "researchos/internal/foundation/workspace"
)

// testTime is Wednesday 2026-03-04 in UTC: the middle of the ISO week that
// starts Monday 2026-03-02, so weekly-boundary tests have days on both sides.
var testTime = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// env is a fixture with one registered project plus the service under test.
type env struct {
	t      *testing.T
	fx     *foundationtest.Fixture
	clock  *clock.Fixed
	pid    string
	handle *foundationworkspace.ProjectHandle
	svc    *Service
}

// newEnv builds a workspace, registers one project and wires a Service. mutate
// may adjust the Options before New runs.
func newEnv(t *testing.T, mutate func(*Options)) *env {
	t.Helper()
	fixed := &clock.Fixed{T: testTime}
	fx, err := foundationtest.New(t.TempDir(), foundationtest.Options{Clock: fixed})
	if err != nil {
		t.Fatalf("foundationtest.New: %v", err)
	}
	ctx := context.Background()
	pid, handle, err := fx.NewProject(ctx, "跨工况异常检测", "两句话简介", "识别并复现跨工况误报")
	if err != nil {
		t.Fatalf("NewProject: %v", err)
	}
	opts := Options{Workspace: fx.Workspace, Clock: fixed}
	if mutate != nil {
		mutate(&opts)
	}
	t.Cleanup(func() { _ = fx.Close() })
	return &env{t: t, fx: fx, clock: fixed, pid: pid, handle: handle, svc: New(opts)}
}

// pdb is the project database.
func (e *env) pdb() *sql.DB { return e.handle.DB() }

// gdb is the global database.
func (e *env) gdb() *sql.DB { return e.fx.Workspace.Global().DB() }

// exec runs a statement on the project database.
func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.pdb().Exec(query, args...); err != nil {
		e.t.Fatalf("exec %q: %v", query, err)
	}
}

// execGlobal runs a statement on global.db.
func (e *env) execGlobal(query string, args ...any) {
	e.t.Helper()
	if _, err := e.gdb().Exec(query, args...); err != nil {
		e.t.Fatalf("exec global %q: %v", query, err)
	}
}

// scalar runs a single-value query on the project database. Tests use it to
// cross-check a widget result against the SQL source of truth.
func (e *env) scalar(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pdb().QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("scalar %q: %v", query, err)
	}
	return n
}

// at is the fixture timestamp d days after testTime, in the storage layout.
func (e *env) at(d time.Duration) string { return clock.Format(e.clock.Now().Add(d)) }

// load is Load with a fatal error.
func (e *env) load() *Snapshot {
	e.t.Helper()
	snap, err := e.svc.Load(context.Background(), e.pid)
	if err != nil {
		e.t.Fatalf("Load: %v", err)
	}
	return snap
}

// idea inserts one idea row.
func (e *env) idea(title, status string, updatedAt time.Duration, deleted bool) string {
	e.t.Helper()
	id := ids.MustNew(e.clock)
	var del any
	if deleted {
		del = e.at(updatedAt)
	}
	e.exec(`INSERT INTO ideas(id, title, description, status, created_at, updated_at, deleted_at)
	        VALUES (?, ?, '', ?, ?, ?, ?)`,
		id, title, status, e.at(updatedAt), e.at(updatedAt), del)
	return id
}

// experiment inserts one experiment row.
func (e *env) experiment(title string, updatedAt time.Duration, deleted bool) string {
	e.t.Helper()
	id := ids.MustNew(e.clock)
	var del any
	if deleted {
		del = e.at(updatedAt)
	}
	e.exec(`INSERT INTO experiments(id, title, purpose, dir_ref, created_at, updated_at, deleted_at)
	        VALUES (?, ?, '', NULL, ?, ?, ?)`,
		id, title, e.at(updatedAt), e.at(updatedAt), del)
	return id
}

// run inserts one run row.
func (e *env) run(experimentID, status string, updatedAt time.Duration) string {
	e.t.Helper()
	id := ids.MustNew(e.clock)
	e.exec(`INSERT INTO runs(id, experiment_id, dir_ref, status, created_at, updated_at, deleted_at)
	        VALUES (?, ?, NULL, ?, ?, ?, NULL)`,
		id, experimentID, status, e.at(updatedAt), e.at(updatedAt))
	return id
}

// task inserts one task row. deadline and priority are raw column values.
func (e *env) task(title, column, priority string, deadline *time.Time, blocked bool, updatedAt time.Duration) string {
	e.t.Helper()
	id := ids.MustNew(e.clock)
	b := 0
	if blocked {
		b = 1
	}
	var dl any
	if deadline != nil {
		dl = clock.Format(*deadline)
	}
	e.exec(`INSERT INTO tasks(id, title, "column", priority, deadline, blocked, sort_order, created_at, updated_at, deleted_at)
	        VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, NULL)`,
		id, title, column, priority, dl, b, e.at(updatedAt), e.at(updatedAt))
	return id
}

// note inserts one note row.
func (e *env) note(title, kind string, updatedAt time.Duration, deleted bool) string {
	e.t.Helper()
	id := ids.MustNew(e.clock)
	var del any
	if deleted {
		del = e.at(updatedAt)
	}
	e.exec(`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at, deleted_at, file_state)
	        VALUES (?, ?, ?, ?, ?, ?, ?, 'ok')`,
		id, title, fmt.Sprintf("projects/%s/notes/%s.md", e.pid, id), kind,
		e.at(updatedAt), e.at(updatedAt), del)
	return id
}

// activity inserts one activity row with an explicit created_at instant.
func (e *env) activity(eventType, entityType, entityID, stamped string) {
	e.t.Helper()
	e.exec(`INSERT INTO activities(id, event_type, entity_type, entity_id, payload, actor, created_at)
	        VALUES (?, ?, ?, ?, '{}', 'user', ?)`,
		ids.MustNew(e.clock), eventType, entityType, entityID, stamped)
}

// weeklyReport inserts one weekly_reports row spanning [start, end] as dates.
func (e *env) weeklyReport(start, end, status, content string, updatedAt time.Duration) string {
	e.t.Helper()
	id := ids.MustNew(e.clock)
	var confirmed any
	if status == "confirmed" {
		confirmed = e.at(updatedAt)
	}
	e.exec(`INSERT INTO weekly_reports(id, period_start, period_end, content_md, status, confirmed_at, created_at, updated_at, deleted_at)
	        VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		id, start, end, content, status, confirmed, e.at(updatedAt), e.at(updatedAt))
	return id
}

// paper inserts a global paper row and returns its id.
func (e *env) paper(metadata, filePath, fileState string) string {
	e.t.Helper()
	id := ids.MustNew(e.clock)
	var path any
	if filePath != "" {
		path = filePath
	}
	e.execGlobal(`INSERT INTO papers(id, zotero_key, metadata, file_path, file_state, imported_at, updated_at)
	              VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, "key-"+id, metadata, path, fileState, e.at(0), e.at(0))
	return id
}

// link associates a paper with the fixture project.
func (e *env) link(paperID string, linkedAt time.Duration) {
	e.t.Helper()
	e.exec(`INSERT INTO project_papers(project_id, paper_id, linked_at, linked_by)
	        VALUES (?, ?, ?, 'user')`, e.pid, paperID, e.at(linkedAt))
}

// setProjectStatus rewrites the registration row status (archive/focus edits are
// owned by internal/project, which this package must not import).
func (e *env) setProjectStatus(status string, archived bool) {
	e.t.Helper()
	var archivedAt any
	if archived {
		archivedAt = e.at(0)
	}
	e.execGlobal(`UPDATE projects SET status = ?, archived_at = ? WHERE id = ?`, status, archivedAt, e.pid)
}

// setFocus rewrites focus_md and goals on the registration row.
func (e *env) setFocus(goals, focusMD string) {
	e.t.Helper()
	e.execGlobal(`UPDATE projects SET goals = ?, focus_md = ? WHERE id = ?`, goals, focusMD, e.pid)
}

func (e *env) state(snap *Snapshot, w WidgetID) WidgetState {
	e.t.Helper()
	want := WidgetState{ID: w}
	for _, st := range snap.Widgets {
		if st.ID == w {
			return st
		}
	}
	e.t.Fatalf("snapshot has no state for widget %q", w)
	return want
}

// seedAll writes a deterministic corpus into every table the widgets read.
func (e *env) seedAll() {
	e.t.Helper()
	// Ideas: three active in a row plus noise that must not surface.
	e.idea("研究中的想法 1", "Researching", -1*time.Hour, false)
	e.idea("验证中的想法 2", "Validating", -2*time.Hour, false)
	e.idea("研究中的想法 3", "Researching", -3*time.Hour, false)
	e.idea("验证中的想法 4", "Validating", -4*time.Hour, false)
	e.idea("研究中的想法 5", "Researching", -5*time.Hour, false)
	e.idea("研究中的想法 6", "Researching", -6*time.Hour, false)
	e.idea("已完成的想法", "Supported", -30*time.Minute, false)
	e.idea("已删除的想法", "Researching", -10*time.Minute, true)

	// Experiments with runs of increasing recency.
	exp1 := e.experiment("基线实验", -3*time.Hour, false)
	e.run(exp1, "running", -90*time.Minute)
	e.run(exp1, "succeeded", -80*time.Minute)
	exp2 := e.experiment("消融实验", -2*time.Hour, false)
	e.run(exp2, "failed", -20*time.Minute)
	e.experiment("无运行实验", -1*time.Hour, false)
	e.experiment("已删除实验", -10*time.Minute, true)

	// Notes.
	for i := range 7 {
		e.note(fmt.Sprintf("笔记 %d", i), "scratchpad", -time.Duration(i)*time.Hour, false)
	}
	e.note("回收站笔记", "paper_note", -10*time.Minute, true)

	// Tasks: four with deadlines, four without; mixed priorities.
	soon := testTime.Add(24 * time.Hour)
	later := testTime.Add(72 * time.Hour)
	e.task("最近的截止", "In Progress", "low", &soon, false, -1*time.Hour)
	e.task("较远的截止", "Backlog", "high", &later, false, -2*time.Hour)
	e.task("无截止高优先", "In Progress", "high", nil, false, -3*time.Hour)
	e.task("无截止中优先", "Backlog", "medium", nil, false, -4*time.Hour)
	e.task("无截止无优先级", "Backlog", "", nil, false, -5*time.Hour)
	e.task("受阻任务 1", "In Progress", "high", &soon, true, -6*time.Hour)
	e.task("受阻任务 2", "Backlog", "low", nil, true, -7*time.Hour)
	e.task("多余的开放任务", "Done", "medium", nil, false, -8*time.Hour)
	e.task("已删除的开放任务", "Backlog", "high", nil, false, -9*time.Hour)
	e.exec(`UPDATE tasks SET deleted_at = updated_at WHERE title = '已删除的开放任务'`)

	// Activities inside the current week.
	e.activity("entity.created", "task", ids.MustNew(e.clock), e.at(-30*time.Minute))
	e.activity("status.changed", "idea", ids.MustNew(e.clock), e.at(-20*time.Minute))
	e.activity("entity.created", "task", ids.MustNew(e.clock), e.at(-10*time.Minute))
}

func TestLoadWidgetDataMatchesSQL(t *testing.T) {
	e := newEnv(t, nil)
	e.seedAll()
	snap := e.load()

	if snap.Project.ID != e.pid || snap.Project.Name != "跨工况异常检测" {
		t.Fatalf("Project = %+v", snap.Project)
	}
	if snap.Project.Goals != "识别并复现跨工况误报" || snap.Project.Status != "active" {
		t.Fatalf("Project fields = %+v", snap.Project)
	}
	if snap.Project.Archived || !snap.Writable {
		t.Fatalf("active project must be writable: %+v writable=%v", snap.Project, snap.Writable)
	}
	if !snap.GeneratedAt.Equal(testTime) {
		t.Fatalf("GeneratedAt = %v, want %v", snap.GeneratedAt, testTime)
	}
	if len(snap.Widgets) != len(widgetOrder) {
		t.Fatalf("Widgets = %d entries, want %d", len(snap.Widgets), len(widgetOrder))
	}
	for i, st := range snap.Widgets {
		if st.ID != widgetOrder[i] {
			t.Fatalf("Widgets[%d].ID = %q, want %q", i, st.ID, widgetOrder[i])
		}
		if st.Degraded {
			t.Fatalf("Widgets[%d] (%s) is degraded on a healthy load: %+v", i, st.ID, st)
		}
		if !st.Available {
			t.Fatalf("Widgets[%d] (%s) is unavailable on a healthy load", i, st.ID)
		}
	}

	// Current Focus: excerpt plus the three newest active ideas.
	wantFocus := []string{"研究中的想法 1", "验证中的想法 2", "研究中的想法 3"}
	if got := titlesOfIdeas(snap.CurrentFocus.TopIdeas); !eqStrings(got, wantFocus) {
		t.Fatalf("TopIdeas = %v, want %v", got, wantFocus)
	}
	if snap.CurrentFocus.GoalsExcerpt != "识别并复现跨工况误报" {
		t.Fatalf("GoalsExcerpt = %q", snap.CurrentFocus.GoalsExcerpt)
	}
	if snap.CurrentFocus.FocusMD != "" || !snap.CurrentFocus.FocusEditable {
		t.Fatalf("FocusWidget = %+v", snap.CurrentFocus)
	}
	if st := e.state(snap, WidgetCurrentFocus); st.PendingConfirmation {
		t.Fatalf("Current Focus must not be pending after focus_md was adopted: %+v", st)
	}

	// Active Ideas: five, newest first, no Supported and no trashed row.
	wantIdeas := []string{
		"研究中的想法 1", "验证中的想法 2", "研究中的想法 3", "验证中的想法 4", "研究中的想法 5",
	}
	if got := titlesOfIdeas(snap.ActiveIdeas); !eqStrings(got, wantIdeas) {
		t.Fatalf("ActiveIdeas = %v, want %v", got, wantIdeas)
	}
	for _, card := range snap.ActiveIdeas {
		if card.Status != "Researching" && card.Status != "Validating" {
			t.Fatalf("ActiveIdeas status = %q for %q", card.Status, card.Title)
		}
		if card.Deleted {
			t.Fatalf("ActiveIdeas carries a deleted row: %+v", card)
		}
	}

	// Recent Experiments: three live rows, latest run status and count.
	if len(snap.RecentExperiments) != 3 {
		t.Fatalf("RecentExperiments = %d, want 3: %+v", len(snap.RecentExperiments), snap.RecentExperiments)
	}
	wantExp := []struct {
		title, last string
		runs        int
	}{
		{"无运行实验", "—", 0},
		{"消融实验", "failed", 1},
		{"基线实验", "succeeded", 2},
	}
	for i, w := range wantExp {
		got := snap.RecentExperiments[i]
		if got.Title != w.title || got.LastRunStatus != w.last || got.RunCount != w.runs {
			t.Fatalf("RecentExperiments[%d] = %+v, want %+v", i, got, w)
		}
	}
	if st := e.state(snap, WidgetRecentExperiments); st.PendingConfirmation {
		t.Fatalf("experiments with runs must not be pending: %+v", st)
	}

	// My Tasks: the eight live open rows (the schema has no completed_at yet, so
	// a Done-column task still counts until T3-08 adds the column). Deadline
	// ascending with NULLs last, then the priority rank descending.
	wantTasks := []struct {
		title    string
		priority int
		hasDue   bool
	}{
		{"受阻任务 1", 3, true},
		{"最近的截止", 1, true},
		{"较远的截止", 3, true},
		{"无截止高优先", 3, false},
		{"无截止中优先", 2, false},
		{"多余的开放任务", 2, false},
		{"受阻任务 2", 1, false},
		{"无截止无优先级", 0, false},
	}
	if len(snap.MyTasks.Items) != len(wantTasks) {
		t.Fatalf("MyTasks items = %d, want %d: %+v", len(snap.MyTasks.Items), len(wantTasks), snap.MyTasks.Items)
	}
	for i, w := range wantTasks {
		got := snap.MyTasks.Items[i]
		if got.Title != w.title || got.Priority != w.priority {
			t.Fatalf("MyTasks[%d] = %+v, want title %q priority %d", i, got, w.title, w.priority)
		}
		if (got.Deadline != nil) != w.hasDue {
			t.Fatalf("MyTasks[%d] deadline = %v, want present=%v", i, got.Deadline, w.hasDue)
		}
		if got.BlockedReason != "" {
			t.Fatalf("MyTasks[%d].BlockedReason = %q, want empty (§16.2)", i, got.BlockedReason)
		}
	}
	if snap.MyTasks.WeekPlanned {
		t.Fatal("WeekPlanned must stay false while week_plan is unconfirmed")
	}
	if st := e.state(snap, WidgetMyTasks); !st.PendingConfirmation || st.Reason != "pending_confirmation" {
		t.Fatalf("MyTasks state = %+v", st)
	}

	// Recent Notes: five live rows, newest first.
	wantNotes := []string{"笔记 0", "笔记 1", "笔记 2", "笔记 3", "笔记 4"}
	if got := titlesOfNotes(snap.RecentNotes); !eqStrings(got, wantNotes) {
		t.Fatalf("RecentNotes = %v, want %v", got, wantNotes)
	}

	// Blocked Tasks: only the two blocked live rows, most recent first.
	wantBlocked := []string{"受阻任务 1", "受阻任务 2"}
	var gotBlocked []string
	for _, b := range snap.BlockedTasks.Items {
		if !b.Blocked {
			t.Fatalf("BlockedTasks carries an unblocked task: %+v", b)
		}
		gotBlocked = append(gotBlocked, b.Title)
	}
	if !eqStrings(gotBlocked, wantBlocked) {
		t.Fatalf("BlockedTasks = %v, want %v", gotBlocked, wantBlocked)
	}
	if st := e.state(snap, WidgetBlockedTasks); !st.PendingConfirmation {
		t.Fatalf("BlockedTasks state = %+v", st)
	}

	// Weekly Progress: no confirmed report → activity counters plus the badge.
	wp := snap.WeeklyProgress
	if wp.Confirmed || wp.ReportID != "" {
		t.Fatalf("WeeklyProgress = %+v, want the counter fallback", wp)
	}
	if wp.ActivityCounts["task"] != 2 || wp.ActivityCounts["idea"] != 1 {
		t.Fatalf("ActivityCounts = %v", wp.ActivityCounts)
	}
	if st := e.state(snap, WidgetWeeklyProgress); !st.PendingConfirmation || st.Reason != "pending_confirmation" {
		t.Fatalf("WeeklyProgress state = %+v", st)
	}
}

func titlesOfIdeas(cards []IdeaCard) []string {
	out := make([]string, 0, len(cards))
	for _, c := range cards {
		out = append(out, c.Title)
	}
	return out
}

func titlesOfNotes(cards []NoteCard) []string {
	out := make([]string, 0, len(cards))
	for _, c := range cards {
		out = append(out, c.Title)
	}
	return out
}

func eqStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestUnknownProjectIsErrNotFound(t *testing.T) {
	e := newEnv(t, nil)
	missing := ids.MustNew(e.clock)
	if _, err := e.svc.Load(context.Background(), missing); !isNotFound(err) {
		t.Fatalf("Load(missing) = %v, want ErrNotFound", err)
	}
	if _, err := e.svc.Load(context.Background(), ""); !isNotFound(err) {
		t.Fatalf("Load(empty) = %v, want ErrNotFound", err)
	}
	if err := e.svc.CompleteTask(context.Background(), missing, "task-1"); !isNotFound(err) {
		t.Fatalf("CompleteTask(missing) = %v, want ErrNotFound", err)
	}
	if _, err := e.svc.QuickAddNote(context.Background(), missing, QuickNoteInput{}); !isNotFound(err) {
		t.Fatalf("QuickAddNote(missing) = %v, want ErrNotFound", err)
	}
}

func TestServiceWithoutWorkspaceFails(t *testing.T) {
	s := New(Options{})
	if _, err := s.Load(context.Background(), "whatever"); err == nil {
		t.Fatal("Load without a workspace = nil, want error")
	}
	if err := s.CompleteTask(context.Background(), "p", "t"); err == nil {
		t.Fatal("CompleteTask without a workspace = nil, want error")
	}
}

// isNotFound is errors.Is against this package's sentinel.
func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

func TestNewAppliesSpecDefaults(t *testing.T) {
	// §5.6: 15s result cache, 500ms per widget.
	zero := New(Options{})
	if zero.cacheTTL != 15*time.Second {
		t.Fatalf("cacheTTL = %v, want 15s", zero.cacheTTL)
	}
	if zero.widgetTimeout != 500*time.Millisecond {
		t.Fatalf("widgetTimeout = %v, want 500ms", zero.widgetTimeout)
	}
	negative := New(Options{CacheTTL: -time.Second, WidgetTimeout: -time.Second})
	if negative.cacheTTL != 15*time.Second || negative.widgetTimeout != 500*time.Millisecond {
		t.Fatalf("negative options = %v / %v, want the defaults", negative.cacheTTL, negative.widgetTimeout)
	}
	if negative.clock == nil {
		t.Fatal("a clock is always available, even without a workspace")
	}
}
