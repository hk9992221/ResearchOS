package kanban

import (
	"errors"
	"testing"
	"time"

	"researchos/internal/foundation/schema"
)

// ---------------------------------------------------------------------------
// AC-01 quick create: card at column end, survives restart, UUIDv7, event
// ---------------------------------------------------------------------------

func TestAC01_CreateAppendsPersistsAndEmits(t *testing.T) {
	e := newTestEnv(t)
	a := e.create("完成动力学推导", "Todo")
	b := e.create("整理推导笔记", "Todo")
	e.create("看文献", "Backlog")

	if got := e.columnNamesOf("Todo"); len(got) != 2 || got[0] != a.ID || got[1] != b.ID {
		t.Fatalf("Todo order = %v, want [%s %s]", got, a.ID, b.ID)
	}
	if !idsAreUUIDv7(a.ID) {
		t.Fatalf("id %s is not UUIDv7", a.ID)
	}

	acts := e.acts(a.ID, schema.EventEntityCreated)
	if len(acts) != 1 {
		t.Fatalf("entity.created count = %d, want 1", len(acts))
	}
	if acts[0].Actor != schema.ActorUser || acts[0].Payload.Actor != schema.ActorUser {
		t.Fatalf("actor = %q", acts[0].Actor)
	}
	if v := objectPayload(t, acts[0], false, "title"); v != "完成动力学推导" {
		t.Fatalf("payload new.title = %v", v)
	}

	before := a.SortOrder
	e.reopen()
	again := e.columnNamesOf("Todo")
	if len(again) != 2 || again[0] != a.ID || again[1] != b.ID {
		t.Fatalf("after restart order = %v", again)
	}
	task, err := e.m.Tasks().Get(e.ctx, a.ID)
	if err != nil || task.SortOrder != before {
		t.Fatalf("after restart sort_order = %d (was %d) err=%v", task.SortOrder, before, err)
	}
}

func idsAreUUIDv7(s string) bool {
	return len(s) == 36 && s[14] == '7' && (s[19] == '8' || s[19] == '9' || s[19] == 'a' || s[19] == 'b')
}

// ---------------------------------------------------------------------------
// AC-02 title validation
// ---------------------------------------------------------------------------

func TestAC02_TitleValidation(t *testing.T) {
	e := newTestEnv(t)
	for _, bad := range []string{"", "   ", "\t\n"} {
		if _, err := e.m.Tasks().Create(e.ctx, schema.ActorUser, CreateInput{Title: bad}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("title %q: err = %v, want ErrInvalidInput", bad, err)
		}
	}
	long := make([]rune, 201)
	for i := range long {
		long[i] = '字'
	}
	if _, err := e.m.Tasks().Create(e.ctx, schema.ActorUser, CreateInput{Title: string(long)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("201-rune title: err = %v", err)
	}
	if _, err := e.m.Tasks().Create(e.ctx, schema.ActorUser, CreateInput{Title: string(long[:200])}); err != nil {
		t.Fatalf("200-rune title: %v", err)
	}
	// Trim survives: stored title is the trimmed value.
	task := e.create("  带空白  ", "Todo")
	if task.Title != "带空白" {
		t.Fatalf("stored title = %q", task.Title)
	}
}

// ---------------------------------------------------------------------------
// AC-03 field edits: sync everywhere, no Activity, updated_at advances;
// blocked toggle emits exactly one status.changed
// ---------------------------------------------------------------------------

func TestAC03_FieldEditsAndBlockedToggle(t *testing.T) {
	e := newTestEnv(t)
	task := e.create("推导", "Todo")
	base := e.actCount()

	e.advance(time.Second)
	dlp := new("2026-11-20")
	prio := PriorityHigh
	updated, err := e.m.Tasks().Update(e.ctx, schema.ActorUser, UpdateInput{
		ID: task.ID, Priority: &prio, Deadline: &dlp,
		Title: new("推导（修订）"), DescriptionMD: new("## 正文"),
		Labels: new([]string{"动力学"}),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if e.actCount() != base {
		t.Fatalf("plain edits wrote activities: %d vs %d", e.actCount(), base)
	}
	if !updated.UpdatedAt.After(task.UpdatedAt) {
		t.Fatalf("updated_at did not advance: %v vs %v", updated.UpdatedAt, task.UpdatedAt)
	}

	detail, err := e.m.Tasks().Get(e.ctx, task.ID)
	if err != nil || detail.Title != "推导（修订）" || detail.Priority != PriorityHigh ||
		detail.Deadline == nil || *detail.Deadline != *dlp || len(detail.Labels) != 1 {
		t.Fatalf("Get after edit = %+v err=%v", detail, err)
	}
	page, err := e.m.Tasks().List(e.ctx, Query{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Title != "推导（修订）" {
		t.Fatalf("List after edit = %+v err=%v", page.Items, err)
	}
	board, _ := e.m.Views().Board(e.ctx, BoardQuery{})
	if board.Columns[1].Tasks[0].Title != "推导（修订）" {
		t.Fatal("board card stale")
	}

	// blocked toggle: exactly one status.changed with new.blocked.
	e.advance(time.Second)
	blockedOn, err := e.m.Tasks().Update(e.ctx, schema.ActorUser,
		UpdateInput{ID: task.ID, Blocked: new(true)})
	if err != nil || !blockedOn.Blocked {
		t.Fatalf("blocked on: %+v err=%v", blockedOn, err)
	}
	acts := e.acts(task.ID, schema.EventStatusChanged)
	if len(acts) != 1 {
		t.Fatalf("status.changed count = %d, want 1", len(acts))
	}
	if v := objectPayload(t, acts[0], false, "blocked"); v != true {
		t.Fatalf("new.blocked = %v", v)
	}
	if v := objectPayload(t, acts[0], true, "blocked"); v != false {
		t.Fatalf("old.blocked = %v", v)
	}

	e.advance(time.Second)
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser,
		UpdateInput{ID: task.ID, Blocked: new(false)}); err != nil {
		t.Fatalf("unblock: %v", err)
	}
	acts = e.acts(task.ID, schema.EventStatusChanged)
	if len(acts) != 2 || objectPayload(t, acts[1], false, "blocked") != false {
		t.Fatalf("second toggle acts = %d", len(acts))
	}
}

// ---------------------------------------------------------------------------
// AC-04 optimistic lock via expected_updated_at
// ---------------------------------------------------------------------------

func TestAC04_StaleTokenRejected(t *testing.T) {
	e := newTestEnv(t)
	task := e.create("推导", "Todo")
	tokenA := task.Version()

	e.advance(time.Second)
	first, err := e.m.Tasks().Update(e.ctx, schema.ActorUser,
		UpdateInput{ID: task.ID, Title: new("窗口一改名"), ExpectedUpdatedAt: &tokenA})
	if err != nil {
		t.Fatalf("first update: %v", err)
	}
	e.advance(time.Second)
	// Second window still holds the token read before its own edit; that
	// token equals the current row only if nobody else wrote in between —
	// here it is stale against first.Version().
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser,
		UpdateInput{ID: task.ID, Title: new("窗口二覆盖"), ExpectedUpdatedAt: &tokenA}); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale token: err = %v, want ErrStaleRevision", err)
	}
	got, _ := e.m.Tasks().Get(e.ctx, task.ID)
	if got.Title != "窗口一改名" {
		t.Fatalf("stored title = %q (first edit must win)", got.Title)
	}
	// Fresh token succeeds.
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser,
		UpdateInput{ID: task.ID, Title: new("窗口二重试"), ExpectedUpdatedAt: new(first.Version())}); err != nil {
		t.Fatalf("retry with fresh token: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AC-05 priority none ↔ NULL and ordering
// ---------------------------------------------------------------------------

func TestAC05_PriorityNoneIsNULL(t *testing.T) {
	e := newTestEnv(t)
	h := e.create("高", "Todo")
	m := e.create("中", "Todo")
	l := e.create("低", "Todo")
	n := e.create("无", "Todo")
	none := PriorityNone
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser,
		UpdateInput{ID: n.ID, Priority: &none}); err != nil {
		t.Fatalf("set none: %v", err)
	}
	for _, id := range []string{h.ID, m.ID, l.ID} {
		_ = id
	}
	if isNull := e.scanInt(`SELECT priority IS NULL FROM tasks WHERE id = ?`, n.ID); isNull != 1 {
		t.Fatalf("none stored non-NULL")
	}
	got, _ := e.m.Tasks().Get(e.ctx, n.ID)
	if got.Priority != PriorityNone {
		t.Fatalf("read priority = %q, want none", got.Priority)
	}
	page, err := e.m.Tasks().List(e.ctx, Query{Sort: SortPriorityDesc, Column: new("Todo")})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 4 || page.Items[3].ID != n.ID {
		t.Fatalf("priority order = %v", idsOf(page.Items))
	}
	if page.Items[0].ID != h.ID || page.Items[1].ID != m.ID || page.Items[2].ID != l.ID {
		t.Fatalf("priority order = %v", idsOf(page.Items))
	}
}

func idsOf(items []TaskSummary) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = s.ID
	}
	return out
}

// ---------------------------------------------------------------------------
// AC-06 default columns
// ---------------------------------------------------------------------------

func TestAC06_DefaultColumns(t *testing.T) {
	e := newTestEnv(t)
	cols, err := e.m.Columns().List(e.ctx)
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	want := []struct {
		name, kind string
		done       bool
	}{
		{"Backlog", "backlog", false},
		{"Todo", "todo", false},
		{"In Progress", "in_progress", false},
		{"Review", "review", false},
		{"Done", "done", true},
	}
	if len(cols) != 5 {
		t.Fatalf("columns = %d", len(cols))
	}
	for i, c := range cols {
		if c.Name != want[i].name || c.Kind != want[i].kind || c.IsDone != want[i].done {
			t.Fatalf("column %d = %+v", i, c)
		}
	}
	// Seeding is idempotent and never overwrites user changes.
	if _, err := e.m.Columns().Rename(e.ctx, schema.ActorUser, "Backlog", "收件箱"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := e.m.Columns().Delete(e.ctx, schema.ActorUser, "Review", "Todo"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	cols, _ = e.m.Columns().List(e.ctx)
	if len(cols) != 4 {
		t.Fatalf("after user edits, columns = %d, want 4 (seed must not resurrect)", len(cols))
	}
	if cols[0].Name != "收件箱" {
		t.Fatalf("rename lost: %v", cols[0].Name)
	}
	// A database that lost its task_columns rows entirely gets a full
	// re-seed (empty-table backfill, §4.3).
	e.exec(`DELETE FROM task_columns`)
	cols, err = e.m.Columns().List(e.ctx)
	if err != nil || len(cols) != 5 || cols[0].Name != "Backlog" {
		t.Fatalf("empty-table backfill = %+v err=%v", cols, err)
	}
}

// ---------------------------------------------------------------------------
// AC-07 column rename cascades
// ---------------------------------------------------------------------------

func TestAC07_RenameCascade(t *testing.T) {
	e := newTestEnv(t)
	a := e.create("任务一", "In Progress")
	b := e.create("任务二", "In Progress")
	done := e.create("已完成", "Done")

	beforeA, _ := e.m.Tasks().Get(e.ctx, a.ID)
	if _, err := e.m.Columns().Rename(e.ctx, schema.ActorUser, "In Progress", "进行中"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if e.taskStringColumn(a.ID) != "进行中" || e.taskStringColumn(b.ID) != "进行中" {
		t.Fatal("cascade missed a task")
	}
	afterA, _ := e.m.Tasks().Get(e.ctx, a.ID)
	if afterA.SortOrder != beforeA.SortOrder {
		t.Fatal("rename changed sort_order")
	}
	// completed_at survives the rename unchanged (AC-07). The Done task
	// already carries completed_at; the cascade must neither clear nor
	// restamp it.
	if before, err := e.m.Tasks().Get(e.ctx, done.ID); err != nil {
		t.Fatal(err)
	} else if before.CompletedAt == nil {
		t.Fatal("fixture: done task lacks completed_at")
	} else if _, err := e.m.Columns().Rename(e.ctx, schema.ActorUser, "Done", "搞定"); err != nil {
		t.Fatalf("rename done: %v", err)
	} else if afterDone, err := e.m.Tasks().Get(e.ctx, done.ID); err != nil {
		t.Fatal(err)
	} else if afterDone.CompletedAt == nil || !afterDone.CompletedAt.Equal(*before.CompletedAt) {
		t.Fatalf("rename changed completed_at: %v vs %v", afterDone.CompletedAt, before.CompletedAt)
	}
	// No orphans: every live task's column resolves.
	if n := e.scanInt(`SELECT count(*) FROM tasks t
	    WHERE t.deleted_at IS NULL AND NOT EXISTS (SELECT 1 FROM task_columns c WHERE c.name = t."column")`); n != 0 {
		t.Fatalf("orphaned tasks = %d", n)
	}
	if _, err := e.m.Columns().Rename(e.ctx, schema.ActorUser, "搞定", "进行中"); !errors.Is(err, ErrDuplicateColumnName) {
		t.Fatalf("collision: err = %v", err)
	}
	if _, err := e.m.Columns().Rename(e.ctx, schema.ActorUser, "Nope", "X"); !errors.Is(err, ErrColumnNotFound) {
		t.Fatalf("missing source: err = %v", err)
	}
	// The board renders the new name.
	board, _ := e.m.Views().Board(e.ctx, BoardQuery{})
	if board.Columns[2].Column.Name != "进行中" || len(board.Columns[2].Tasks) != 2 {
		t.Fatalf("board after rename = %+v", board.Columns[2])
	}
}

// ---------------------------------------------------------------------------
// AC-08 create column placement + reorder persistence
// ---------------------------------------------------------------------------

func TestAC08_ColumnCreateAndMove(t *testing.T) {
	e := newTestEnv(t)
	c, err := e.m.Columns().Create(e.ctx, schema.ActorUser, CreateColumnInput{Name: "验证"})
	if err != nil {
		t.Fatalf("create column: %v", err)
	}
	if c.Kind != "custom" || c.Position <= 0 {
		t.Fatalf("column = %+v", c)
	}
	cols, _ := e.m.Columns().List(e.ctx)
	if cols[len(cols)-1].Name != "验证" {
		t.Fatalf("new column not far right: %v", namesOf(cols))
	}
	if _, err := e.m.Columns().Create(e.ctx, schema.ActorUser,
		CreateColumnInput{Name: "草稿", After: new("Backlog")}); err != nil {
		t.Fatalf("after: %v", err)
	}
	cols, _ = e.m.Columns().List(e.ctx)
	if cols[1].Name != "草稿" {
		t.Fatalf("After placement = %v", namesOf(cols))
	}
	// Move 验证 to the far left and prove it survives a restart.
	if _, err := e.m.Columns().Move(e.ctx, schema.ActorUser, "验证", 0); err != nil {
		t.Fatalf("move column: %v", err)
	}
	e.reopen()
	cols, _ = e.m.Columns().List(e.ctx)
	if cols[0].Name != "验证" {
		t.Fatalf("column order lost after restart: %v", namesOf(cols))
	}
	if _, err := e.m.Columns().Create(e.ctx, schema.ActorUser, CreateColumnInput{Name: "验证"}); !errors.Is(err, ErrDuplicateColumnName) {
		t.Fatalf("duplicate: err = %v", err)
	}
}

// namesOf lives in column_service.go (production); the tests use it directly.

// ---------------------------------------------------------------------------
// AC-09 delete column with migration
// ---------------------------------------------------------------------------

func TestAC09_DeleteColumnMigrates(t *testing.T) {
	e := newTestEnv(t)
	a := e.create("一", "Todo")
	e.advance(time.Minute)
	b := e.create("二", "Todo")
	c := e.create("三", "Todo")
	ipBefore := len(e.columnNamesOf("In Progress"))

	if err := e.m.Columns().Delete(e.ctx, schema.ActorUser, "Todo", ""); !errors.Is(err, ErrColumnNotEmpty) {
		t.Fatalf("no target: err = %v, want ErrColumnNotEmpty", err)
	}
	// Live sub task count is preserved by the migration.
	totalBefore := e.scanInt(`SELECT count(*) FROM tasks WHERE deleted_at IS NULL`)
	if err := e.m.Columns().Delete(e.ctx, schema.ActorUser, "Todo", "In Progress"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if e.scanInt(`SELECT count(*) FROM tasks`) != totalBefore {
		t.Fatal("task count not conserved")
	}
	order := e.columnNamesOf("In Progress")
	if len(order) != ipBefore+3 {
		t.Fatalf("target length = %d", len(order))
	}
	// Original order preserved, appended at the end.
	for i, want := range []string{a.ID, b.ID, c.ID} {
		if order[len(order)-3+i] != want {
			t.Fatalf("migrated order = %v", order)
		}
	}
	// Every column value points at an existing definition.
	if n := e.scanInt(`SELECT count(*) FROM tasks t WHERE NOT EXISTS
	    (SELECT 1 FROM task_columns c WHERE c.name = t."column")`); n != 0 {
		t.Fatalf("dangling columns = %d", n)
	}
	if _, err := e.m.Columns().List(e.ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Columns().Delete(e.ctx, schema.ActorUser, "Nope", "Todo"); !errors.Is(err, ErrColumnNotFound) {
		t.Fatalf("unknown column: err = %v", err)
	}
	// Migration into a terminal column maintains completed_at (§5.4).
	if _, err := e.m.Columns().Create(e.ctx, schema.ActorUser,
		CreateColumnInput{Name: "验证中", IsDone: true}); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE tasks SET "column" = '验证中', completed_at = NULL WHERE id = ?`, a.ID)
	if err := e.m.Columns().Delete(e.ctx, schema.ActorUser, "验证中", "Review"); err != nil {
		t.Fatalf("done→non-done delete: %v", err)
	}
	if n := e.scanInt(`SELECT completed_at IS NULL FROM tasks WHERE id = ?`, a.ID); n != 1 {
		t.Fatal("leaving a terminal column via migration must clear completed_at")
	}
}

// ---------------------------------------------------------------------------
// AC-10 done-column and last-column guards
// ---------------------------------------------------------------------------

func TestAC10_DoneAndLastColumnGuards(t *testing.T) {
	e := newTestEnv(t)
	if _, err := e.m.Columns().SetDone(e.ctx, schema.ActorUser, "Done", false); !errors.Is(err, ErrLastDoneColumn) {
		t.Fatalf("unmark last done: err = %v", err)
	}
	abandoned, err := e.m.Columns().Create(e.ctx, schema.ActorUser,
		CreateColumnInput{Name: "Abandoned", IsDone: true})
	if err != nil || !abandoned.IsDone {
		t.Fatalf("second done column: %+v err=%v", abandoned, err)
	}
	// Two done columns: unmarking one is legal.
	if _, err := e.m.Columns().SetDone(e.ctx, schema.ActorUser, "Abandoned", false); err != nil {
		t.Fatalf("unmark one of two: %v", err)
	}
	if _, err := e.m.Columns().SetDone(e.ctx, schema.ActorUser, "Abandoned", true); err != nil {
		t.Fatalf("re-mark: %v", err)
	}
	// Both Done and Abandoned are terminal now: deleting Done (with a
	// target) is legal because Abandoned still satisfies the ≥1 invariant.
	if err := e.m.Columns().Delete(e.ctx, schema.ActorUser, "Done", "Todo"); err != nil {
		t.Fatalf("delete one of two done columns: %v", err)
	}
	// Abandoned is the only terminal column left; deleting it, even with
	// a migration target, is rejected.
	if err := e.m.Columns().Delete(e.ctx, schema.ActorUser, "Abandoned", "Todo"); !errors.Is(err, ErrLastDoneColumn) {
		t.Fatalf("delete last done column: err = %v", err)
	}
	// Last column overall.
	for _, name := range []string{"Backlog", "Todo", "In Progress", "Review"} {
		if err := e.m.Columns().Delete(e.ctx, schema.ActorUser, name, "Abandoned"); err != nil {
			t.Fatalf("delete %s: %v", name, err)
		}
	}
	if err := e.m.Columns().Delete(e.ctx, schema.ActorUser, "Abandoned", ""); !errors.Is(err, ErrLastColumn) {
		t.Fatalf("delete last column: err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// AC-11 completed_at across terminal columns
// ---------------------------------------------------------------------------

func TestAC11_CompletedAtLifecycle(t *testing.T) {
	e := newTestEnv(t)
	task := e.create("推导", "Todo")
	if _, err := e.m.Columns().Create(e.ctx, schema.ActorUser,
		CreateColumnInput{Name: "Abandoned", IsDone: true}); err != nil {
		t.Fatal(err)
	}
	if got := e.scanInt(`SELECT completed_at IS NOT NULL FROM tasks WHERE id = ?`, task.ID); got != 0 {
		t.Fatal("must be incomplete in Todo")
	}
	e.advance(time.Second)
	done, err := e.m.Tasks().Move(e.ctx, schema.ActorUser, MoveInput{ID: task.ID, Column: "Done"})
	if err != nil {
		t.Fatalf("into done: %v", err)
	}
	if done.CompletedAt == nil {
		t.Fatal("entering Done must stamp completed_at")
	}
	first := *done.CompletedAt

	// Terminal → terminal keeps the original instant.
	e.advance(time.Hour)
	ab, err := e.m.Tasks().Move(e.ctx, schema.ActorUser, MoveInput{ID: task.ID, Column: "Abandoned"})
	if err != nil {
		t.Fatalf("done→abandoned: %v", err)
	}
	if ab.CompletedAt == nil || !ab.CompletedAt.Equal(first) {
		t.Fatalf("terminal-to-terminal moved completed_at: %v", ab.CompletedAt)
	}

	// Leaving every terminal column clears it.
	if back, err := e.m.Tasks().Move(e.ctx, schema.ActorUser, MoveInput{ID: task.ID, Column: "In Progress"}); err != nil {
		t.Fatalf("reopen: %v", err)
	} else if back.CompletedAt != nil {
		t.Fatalf("leaving terminals must clear completed_at, got %v", back.CompletedAt)
	}

	// Re-entering stamps again.
	e.advance(time.Second)
	reentered, err := e.m.Tasks().Move(e.ctx, schema.ActorUser, MoveInput{ID: task.ID, Column: "Done"})
	if err != nil || reentered.CompletedAt == nil {
		t.Fatalf("re-entry: err=%v completed=%v", err, reentered.CompletedAt)
	}
	if !reentered.CompletedAt.After(first) {
		t.Fatalf("re-entry stamp %v not after first %v", reentered.CompletedAt, first)
	}
}

// ---------------------------------------------------------------------------
// AC-12 same-column drag: order only, no Activity, survives 100 inserts
// ---------------------------------------------------------------------------

func TestAC12_SameColumnDragNoActivityRebalance(t *testing.T) {
	e := newTestEnv(t)
	ids := make([]string, 0, 20)
	for i := range 20 {
		ids = append(ids, e.create(string(rune('A'+i)), "Todo").ID)
	}
	base := e.actCount()
	anchor := ids[0]
	for i := 1; i < 16; i++ {
		target := ids[i]
		e.advance(time.Millisecond)
		if _, err := e.m.Tasks().Move(e.ctx, schema.ActorUser,
			MoveInput{ID: target, Column: "Todo", BeforeID: &anchor}); err != nil {
			t.Fatalf("drag %d before anchor: %v", i, err)
		}
		order := e.columnNamesOf("Todo")
		// target must land immediately before anchor.
		pos := indexOf(order, target)
		if pos < 0 || indexOf(order, anchor) != pos+1 {
			t.Fatalf("drag %d order = %v", i, order)
		}
	}
	if e.actCount() != base {
		t.Fatalf("same-column drags wrote %d activities", e.actCount()-base)
	}
	// sort_order strictly monotonic along the stored order.
	rows, err := e.handle.DB().QueryContext(e.ctx,
		`SELECT sort_order FROM tasks WHERE "column" = 'Todo' AND deleted_at IS NULL ORDER BY sort_order, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	prev := -1
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		if int(v) <= prev {
			t.Fatalf("sort_order not monotonic: %d after %d", v, prev)
		}
		prev = int(v)
	}

	// 100 rapid inserts at the same gap must never break monotonicity
	// (rebalance kicks in at gap exhaustion).
	for i := range 100 {
		t2 := e.create(string([]rune{'x', rune('a' + i%26)})+string(rune(i)), "Todo")
		if _, err := e.m.Tasks().Move(e.ctx, schema.ActorUser,
			MoveInput{ID: t2.ID, Column: "Todo", AfterID: new(anchor)}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	order := e.columnNamesOf("Todo")
	sorted := make([]int, 0, len(order))
	for _, id := range order {
		sorted = append(sorted, e.scanInt(`SELECT sort_order FROM tasks WHERE id = ?`, id))
	}
	for i := 1; i < len(sorted); i++ {
		if sorted[i] <= sorted[i-1] {
			t.Fatalf("order broken at %d: %v", i, sorted)
		}
	}
}

func indexOf(hay []string, needle string) int {
	for i, s := range hay {
		if s == needle {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// AC-13 cross-column drop onto anchors, single event, old/new shape
// ---------------------------------------------------------------------------

func TestAC13_CrossColumnDrop(t *testing.T) {
	e := newTestEnv(t)
	t1 := e.create("T1", "Todo")
	t2 := e.create("T2", "Todo")
	b1 := e.create("B1", "Backlog")
	b2 := e.create("B2", "Backlog")

	base := e.actCount()
	moved, err := e.m.Tasks().Move(e.ctx, schema.ActorUser,
		MoveInput{ID: b2.ID, Column: "Todo", BeforeID: &t2.ID})
	if err != nil {
		t.Fatalf("drop: %v", err)
	}
	order := e.columnNamesOf("Todo")
	if len(order) != 3 || order[0] != t1.ID || order[1] != b2.ID || order[2] != t2.ID {
		t.Fatalf("target order = %v", order)
	}
	if src := e.columnNamesOf("Backlog"); len(src) != 1 || src[0] != b1.ID {
		t.Fatalf("source column = %v", src)
	}
	_ = moved
	if e.actCount() != base+1 {
		t.Fatalf("events = %d, want exactly 1", e.actCount()-base)
	}
	act := e.lastAct(schema.EventStatusChanged)
	if objectPayload(t, act, true, "column") != "Backlog" ||
		objectPayload(t, act, true, "column_name") != "Backlog" ||
		objectPayload(t, act, true, "is_done") != false {
		t.Fatalf("old side = %v", *act.Payload.Old)
	}
	if objectPayload(t, act, false, "column") != "Todo" ||
		objectPayload(t, act, false, "column_name") != "Todo" ||
		objectPayload(t, act, false, "is_done") != false {
		t.Fatalf("new side = %v", *act.Payload.New)
	}

	// AfterID anchor: drop b1 after t1.
	if _, err := e.m.Tasks().Move(e.ctx, schema.ActorUser,
		MoveInput{ID: b1.ID, Column: "Todo", AfterID: &t1.ID}); err != nil {
		t.Fatalf("after drop: %v", err)
	}
	order = e.columnNamesOf("Todo")
	if order[0] != t1.ID || order[1] != b1.ID {
		t.Fatalf("after anchor = %v", order)
	}
	// Both anchors at once → rejected.
	_, err = e.m.Tasks().Move(e.ctx, schema.ActorUser,
		MoveInput{ID: b1.ID, Column: "Done", BeforeID: &t1.ID, AfterID: &t2.ID})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("exclusive anchors: err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// AC-14 invalid drops are refused before any write
// ---------------------------------------------------------------------------

func TestAC14_InvalidDrop(t *testing.T) {
	e := newTestEnv(t)
	t1 := e.create("T1", "Todo")
	b1 := e.create("B1", "Backlog")

	if _, err := e.m.Tasks().Move(e.ctx, schema.ActorUser,
		MoveInput{ID: t1.ID, Column: "Nope"}); !errors.Is(err, ErrColumnNotFound) {
		t.Fatalf("missing column: err = %v", err)
	}
	// Anchor from a different column than the target.
	if _, err := e.m.Tasks().Move(e.ctx, schema.ActorUser,
		MoveInput{ID: t1.ID, Column: "Review", BeforeID: &b1.ID}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("foreign anchor: err = %v", err)
	}
	if _, err := e.m.Tasks().Move(e.ctx, schema.ActorUser,
		MoveInput{ID: t1.ID, Column: "Todo", BeforeID: new("00000000-0000-4000-8000-000000000000")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing anchor: err = %v", err)
	}
	// Nothing moved: still in Todo, events unchanged besides creates.
	if e.taskStringColumn(t1.ID) != "Todo" {
		t.Fatal("failed move must not write")
	}
}

// ---------------------------------------------------------------------------
// week_plan (KBN-P02 data layer): writable, filterable, never emits
// ---------------------------------------------------------------------------

func TestWeekPlanDataLayer(t *testing.T) {
	e := newTestEnv(t)
	task := e.create("推导", "Todo")
	base := e.actCount()

	got, err := e.m.Tasks().SetWeekPlan(e.ctx, schema.ActorUser, task.ID, new("2026-W46"), nil)
	if err != nil || got.WeekPlan == nil || *got.WeekPlan != "2026-W46" {
		t.Fatalf("set: %+v err=%v", got, err)
	}
	if e.actCount() != base {
		t.Fatal("SetWeekPlan emitted activity")
	}
	page, err := e.m.Tasks().List(e.ctx, Query{WeekPlan: new("2026-W46")})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("filter = %+v err=%v", page.Items, err)
	}
	if _, err := e.m.Tasks().SetWeekPlan(e.ctx, schema.ActorUser, task.ID, new("2026-W54"), nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("impossible week: err = %v", err)
	}
	cleared, err := e.m.Tasks().SetWeekPlan(e.ctx, schema.ActorUser, task.ID, nil, nil)
	if err != nil || cleared.WeekPlan != nil {
		t.Fatalf("clear: %+v err=%v", cleared, err)
	}
}

// ---------------------------------------------------------------------------
// cursor pagination + sort table (AC-05 ordering already covers priority)
// ---------------------------------------------------------------------------

func TestCursorPagination(t *testing.T) {
	e := newTestEnv(t)
	want := make([]string, 0, 5)
	for i := range 5 {
		want = append(want, e.create(string(rune('A'+i)), "Todo").ID)
	}
	var seen []string
	cursor := ""
	pages := 0
	for {
		page, err := e.m.Tasks().List(e.ctx, Query{Column: new("Todo"), Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		seen = append(seen, idsOf(page.Items)...)
		pages++
		if page.NextCursor == "" {
			break
		}
		if pages > 10 {
			t.Fatal("cursor never terminates")
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("paginated %d rows: %v", len(seen), seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("page order = %v want %v", seen, want)
		}
	}
	// A corrupt cursor falls back to the first page instead of erroring.
	page, err := e.m.Tasks().List(e.ctx, Query{Column: new("Todo"), Limit: 2, Cursor: "!!not-a-cursor!!"})
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != want[0] {
		t.Fatalf("stale cursor behaviour: %v err=%v", page.Items, err)
	}

	// deadline_asc: dated first (ascending), undated last.
	e.advance(time.Second)
	d2 := e.create("D2", "Review")
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser, UpdateInput{ID: d2.ID, Deadline: new(new("2026-12-01"))}); err != nil {
		t.Fatal(err)
	}
	dNone := e.create("D0", "Review")
	e.advance(time.Second)
	d1 := e.create("D1", "Review")
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser, UpdateInput{ID: d1.ID, Deadline: new(new("2026-11-01"))}); err != nil {
		t.Fatal(err)
	}
	page, err = e.m.Tasks().List(e.ctx, Query{Column: new("Review"), Sort: SortDeadlineAsc})
	if err != nil {
		t.Fatal(err)
	}
	order := idsOf(page.Items)
	if len(order) != 3 || order[0] != d1.ID || order[1] != d2.ID || order[2] != dNone.ID {
		t.Fatalf("deadline order = %v", order)
	}

	// updated_desc: newest edit first.
	page, _ = e.m.Tasks().List(e.ctx, Query{Column: new("Review"), Sort: SortUpdatedDesc})
	if page.Items[0].ID != d1.ID {
		t.Fatalf("updated_desc first = %s, want %s", page.Items[0].ID, d1.ID)
	}
}

func TestTaskLimitAndSubtaskCap(t *testing.T) {
	e := newTestEnv(t)
	// Cap is 100; cheap to prove at 3 with a direct SQL bump of the
	// counter is dishonest — instead fill to the cap through the service.
	parent := e.create("父", "Todo")
	for i := range maxSubtasksPerParent {
		if _, err := e.m.Tasks().Create(e.ctx, schema.ActorUser, CreateInput{
			Title:        "子" + string(rune('0'+i/100)) + string(rune('0'+(i/10)%10)) + string(rune('0'+i%10)),
			ParentTaskID: &parent.ID,
		}); err != nil {
			t.Fatalf("subtask %d: %v", i, err)
		}
	}
	if _, err := e.m.Tasks().Create(e.ctx, schema.ActorUser,
		CreateInput{Title: "溢出", ParentTaskID: &parent.ID}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("101st subtask: err = %v", err)
	}
}
