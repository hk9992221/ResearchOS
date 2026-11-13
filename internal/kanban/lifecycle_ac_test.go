package kanban

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/trash"
)

// ---------------------------------------------------------------------------
// AC-15 lifecycle isolation: task completion never touches research entities
// ---------------------------------------------------------------------------

func TestAC15_LifecycleIsolation(t *testing.T) {
	e := newTestEnv(t)
	ideaID := e.uuidFixture()
	ideaStamp := e.insertIdea(ideaID, "新研究方向", "Researching")
	expID := e.uuidFixture()
	expStamp := e.insertExperiment(expID, "阻尼实验")
	runID := e.uuidFixture()
	runStamp := e.insertRun(runID, expID, "running")

	task := e.create("推导", "Todo")
	ctx := e.ctx
	if _, err := e.m.Links().Add(ctx, task.ID, schema.RelationRelatedTo, schema.EntityIdea, ideaID, schema.ActorUser); err != nil {
		t.Fatalf("link idea: %v", err)
	}
	if _, err := e.m.Links().Add(ctx, task.ID, schema.RelationDeliverableOf, schema.EntityRun, runID, schema.ActorUser); err != nil {
		t.Fatalf("link run: %v", err)
	}

	before := e.actCount()
	if _, err := e.m.Tasks().Complete(ctx, schema.ActorUser, task.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Idea/Experiment/Run rows byte-identical.
	if got := e.scanString(`SELECT status FROM ideas WHERE id = ?`, ideaID); got != "Researching" {
		t.Fatalf("idea status mutated: %s", got)
	}
	if got := e.scanString(`SELECT updated_at FROM ideas WHERE id = ?`, ideaID); got != ideaStamp {
		t.Fatalf("idea updated_at mutated: %s vs %s", got, ideaStamp)
	}
	if got := e.scanString(`SELECT updated_at FROM experiments WHERE id = ?`, expID); got != expStamp {
		t.Fatal("experiment updated_at mutated")
	}
	if got := e.scanString(`SELECT updated_at FROM runs WHERE id = ?`, runID); got != runStamp {
		t.Fatal("run updated_at mutated")
	}
	if got := e.scanString(`SELECT status FROM runs WHERE id = ?`, runID); got != "running" {
		t.Fatalf("run status mutated: %s", got)
	}
	// No evidence rows, no AI events, and the entities accrue no activity
	// from the task's lifecycle (AC-15: no implicit cross-module writes).
	if n := e.scanInt(`SELECT count(*) FROM evidence`); n != 0 {
		t.Fatalf("evidence rows = %d", n)
	}
	if n := len(e.acts("", schema.EventAIAnalysisCompleted)); n != 0 {
		t.Fatalf("ai events = %d", n)
	}
	if acts := e.acts(ideaID); len(acts) != 0 {
		t.Fatalf("idea activities = %d", len(acts))
	}
	// The only new activity is the task's single status.changed.
	if got := e.actCount(); got != before+1 {
		t.Fatalf("events after complete = %d, want +1", got-before)
	}

	// Reverse direction: the idea becoming Supported does not move the task.
	e.exec(`UPDATE ideas SET status = 'Supported' WHERE id = ?`, ideaID)
	if e.taskStringColumn(task.ID) != "Done" {
		t.Fatal("task followed the idea")
	}
}

// ---------------------------------------------------------------------------
// AC-16 subtasks: two layers only, no auto-complete cascade
// ---------------------------------------------------------------------------

func TestAC16_SubtaskRules(t *testing.T) {
	e := newTestEnv(t)
	parent := e.create("父任务", "Todo")
	childA, err := e.m.Tasks().Create(e.ctx, schema.ActorUser, CreateInput{Title: "子A", ParentTaskID: &parent.ID})
	if err != nil {
		t.Fatalf("child: %v", err)
	}
	childB, err := e.m.Tasks().Create(e.ctx, schema.ActorUser,
		CreateInput{Title: "子B", ParentTaskID: &parent.ID, Column: "In Progress"})
	if err != nil {
		t.Fatalf("childB: %v", err)
	}
	if _, err := e.m.Tasks().Create(e.ctx, schema.ActorUser,
		CreateInput{Title: "孙", ParentTaskID: &childA.ID}); !errors.Is(err, ErrSubtaskOnSubtask) {
		t.Fatalf("grandchild: err = %v, want ErrSubtaskOnSubtask", err)
	}
	// A deleted parent cannot adopt children.
	other := e.create("回收父", "Todo")
	if err := e.m.Tasks().SoftDelete(e.ctx, schema.ActorUser, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Tasks().Create(e.ctx, schema.ActorUser,
		CreateInput{Title: "投奔", ParentTaskID: &other.ID}); !errors.Is(err, ErrInvalidParent) {
		t.Fatalf("deleted parent: err = %v", err)
	}

	// Children complete; the parent does not follow.
	for _, id := range []string{childA.ID, childB.ID} {
		if _, err := e.m.Tasks().Move(e.ctx, schema.ActorUser, MoveInput{ID: id, Column: "Done"}); err != nil {
			t.Fatalf("child done: %v", err)
		}
	}
	got, _ := e.m.Tasks().Get(e.ctx, parent.ID)
	if got.Task.Column != "Todo" {
		t.Fatalf("parent auto-completed: %s", got.Task.Column)
	}
	// Board shows 2/2 on the card; subtasks never surface as top-level.
	board, _ := e.m.Views().Board(e.ctx, BoardQuery{})
	card := findCard(board, "Todo", parent.ID)
	if card == nil || card.SubtaskDone != 2 || card.SubtaskTotal != 2 {
		t.Fatalf("parent card = %+v", card)
	}
	if findCard(board, "Done", childA.ID) != nil {
		t.Fatal("subtask leaked onto the board")
	}
	// Children keep independent fields and are listed via ParentID.
	prio := PriorityHigh
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser,
		UpdateInput{ID: childA.ID, Priority: &prio, Deadline: new(new("2026-11-30"))}); err != nil {
		t.Fatalf("child edit: %v", err)
	}
	page, _ := e.m.Tasks().List(e.ctx, Query{ParentID: &parent.ID})
	if len(page.Items) != 2 {
		t.Fatalf("children list = %d", len(page.Items))
	}
}

func findCard(board BoardView, column, id string) *TaskSummary {
	for _, c := range board.Columns {
		if c.Column.Name != column {
			continue
		}
		for i := range c.Tasks {
			if c.Tasks[i].ID == id {
				return &c.Tasks[i]
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// AC-17 checklist: CRUD, reorder, no Activity, no parent updated_at move
// ---------------------------------------------------------------------------

func TestAC17_Checklist(t *testing.T) {
	e := newTestEnv(t)
	task := e.create("读论文", "Todo")
	base := e.actCount()
	parentUpdated := task.UpdatedAt

	itemA, err := e.m.Checklist().Add(e.ctx, schema.ActorUser, task.ID, "摘要", nil)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	e.advance(time.Second)
	itemB, err := e.m.Checklist().Add(e.ctx, schema.ActorUser, task.ID, "复现", new(itemA.ID))
	if err != nil {
		t.Fatalf("add before: %v", err)
	}

	detail, _ := e.m.Tasks().Get(e.ctx, task.ID)
	if len(detail.Checklist) != 2 || detail.Checklist[0].ID != itemB.ID || detail.Checklist[1].ID != itemA.ID {
		t.Fatalf("checklist order = %+v", detail.Checklist)
	}

	e.advance(time.Second)
	if _, err := e.m.Checklist().SetDone(e.ctx, schema.ActorUser, itemA.ID, true); err != nil {
		t.Fatalf("set done: %v", err)
	}
	if _, err := e.m.Checklist().Update(e.ctx, schema.ActorUser, itemB.ID, "方法精读"); err != nil {
		t.Fatalf("update text: %v", err)
	}
	if _, err := e.m.Checklist().Add(e.ctx, schema.ActorUser, task.ID, "", nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty text: %v", err)
	}
	if _, err := e.m.Checklist().Add(e.ctx, schema.ActorUser, task.ID, string(longText(501)), nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("501-char text: %v", err)
	}

	// Reorder: [itemB, itemA] must be accepted as-is; a foreign id is not.
	if err := e.m.Checklist().Reorder(e.ctx, schema.ActorUser, task.ID, []string{itemB.ID, itemA.ID}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if err := e.m.Checklist().Reorder(e.ctx, schema.ActorUser, task.ID, []string{itemB.ID}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("partial reorder: %v", err)
	}

	// None of this wrote activity or moved the parent's token (AC-17).
	if e.actCount() != base {
		t.Fatalf("checklist wrote %d activities", e.actCount()-base)
	}
	reloaded, _ := e.m.Tasks().Get(e.ctx, task.ID)
	if !reloaded.UpdatedAt.Equal(parentUpdated) {
		t.Fatalf("parent updated_at moved: %v vs %v", reloaded.UpdatedAt, parentUpdated)
	}

	// State survives a restart.
	e.reopen()
	reloaded, _ = e.m.Tasks().Get(e.ctx, task.ID)
	if len(reloaded.Checklist) != 2 || !reloaded.Checklist[1].Done ||
		reloaded.Checklist[0].Text != "方法精读" {
		t.Fatalf("after restart = %+v", reloaded.Checklist)
	}
	// Card counters aggregate without extra loads.
	board, _ := e.m.Views().Board(e.ctx, BoardQuery{})
	card := findCard(board, "Todo", task.ID)
	if card == nil || card.ChecklistDone != 1 || card.ChecklistTotal != 2 {
		t.Fatalf("card counters = %+v", card)
	}

	// Remove is physical.
	if err := e.m.Checklist().Remove(e.ctx, schema.ActorUser, itemA.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if n := e.scanInt(`SELECT count(*) FROM task_checklist_items WHERE id = ?`, itemA.ID); n != 0 {
		t.Fatal("item survived Remove")
	}
}

func longText(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// AC-18 labels: dedup, caps, AND filtering, aggregation
// ---------------------------------------------------------------------------

func TestAC18_Labels(t *testing.T) {
	e := newTestEnv(t)
	a := e.create("A", "Todo")
	b := e.create("B", "Todo")
	c := e.create("C", "Todo")

	labels := []string{"动力学", " 动力学 ", "推导", "急"}
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser,
		UpdateInput{ID: a.ID, Labels: &labels}); err != nil {
		t.Fatalf("labels: %v", err)
	}
	got, _ := e.m.Tasks().Get(e.ctx, a.ID)
	if len(got.Labels) != 3 || got.Labels[0] != "动力学" {
		t.Fatalf("dedup/trim = %v", got.Labels)
	}
	hi := []string{"动力学"}
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser,
		UpdateInput{ID: b.ID, Labels: &hi}); err != nil {
		t.Fatal(err)
	}
	lo := []string{"推导"}
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser,
		UpdateInput{ID: c.ID, Labels: &lo}); err != nil {
		t.Fatal(err)
	}

	// AND semantics: only A carries both.
	page, err := e.m.Tasks().List(e.ctx, Query{Labels: []string{"动力学", "推导"}})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != a.ID {
		t.Fatalf("AND filter = %v err=%v", idsOf(page.Items), err)
	}
	// Exact match: prefix must not match.
	page, _ = e.m.Tasks().List(e.ctx, Query{Labels: []string{"动"}})
	if len(page.Items) != 0 {
		t.Fatalf("prefix matched: %v", idsOf(page.Items))
	}

	// 33-char label rejected; 21st label rejected.
	tooLong := []string{longText(33)}
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser,
		UpdateInput{ID: a.ID, Labels: &tooLong}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("33-char label: %v", err)
	}
	var twentyOne []string
	for i := range 21 {
		twentyOne = append(twentyOne, string(rune('a'+i)))
	}
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser,
		UpdateInput{ID: a.ID, Labels: &twentyOne}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("21st label: %v", err)
	}

	// LabelService.Add/Remove convenience + idempotence.
	if _, err := e.m.Labels().Add(e.ctx, schema.ActorUser, b.ID, "急", nil); err != nil {
		t.Fatalf("label add: %v", err)
	}
	if _, err := e.m.Labels().Add(e.ctx, schema.ActorUser, b.ID, "急", nil); err != nil {
		t.Fatalf("label add idempotent: %v", err)
	}
	taskB, _ := e.m.Tasks().Get(e.ctx, b.ID)
	if len(taskB.Labels) != 2 {
		t.Fatalf("b labels = %v", taskB.Labels)
	}
	if _, err := e.m.Labels().Remove(e.ctx, schema.ActorUser, b.ID, "动力学", nil); err != nil {
		t.Fatalf("label remove: %v", err)
	}
	taskB, _ = e.m.Tasks().Get(e.ctx, b.ID)
	if len(taskB.Labels) != 1 || taskB.Labels[0] != "急" {
		t.Fatalf("b labels after remove = %v", taskB.Labels)
	}

	// Aggregation over live tasks.
	counts, err := e.m.Labels().List(e.ctx)
	if err != nil {
		t.Fatalf("labels list: %v", err)
	}
	// b started [动力学] then gained 急 and lost 动力学, so a+c hold 动力学.
	expect := map[string]int{"动力学": 1, "推导": 2, "急": 2}
	for _, c := range counts {
		if expect[c.Label] != c.Count {
			t.Fatalf("count %s = %d, want %d", c.Label, c.Count, expect[c.Label])
		}
		delete(expect, c.Label)
	}
	if len(expect) != 0 {
		t.Fatalf("missing labels %+v", expect)
	}
}

// ---------------------------------------------------------------------------
// AC-19 cascade delete / restore through trash
// ---------------------------------------------------------------------------

func TestAC19_CascadeTrash(t *testing.T) {
	e := newTestEnv(t)
	parent := e.create("父", "Todo")
	c1, _ := e.m.Tasks().Create(e.ctx, schema.ActorUser, CreateInput{Title: "子一", ParentTaskID: &parent.ID})
	c2, _ := e.m.Tasks().Create(e.ctx, schema.ActorUser, CreateInput{Title: "子二", ParentTaskID: &parent.ID})

	before := e.actCount()
	if err := e.m.Tasks().SoftDelete(e.ctx, schema.ActorUser, parent.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	// Parent and each child got exactly one entity.deleted (AC-19).
	if e.actCount() != before+3 {
		t.Fatalf("events = %d, want +3", e.actCount()-before)
	}
	for _, a := range e.acts("", schema.EventEntityDeleted) {
		if a.EntityID != parent.ID && a.EntityID != c1.ID && a.EntityID != c2.ID {
			t.Fatalf("unexpected entity.deleted %s", a.EntityID)
		}
	}
	for _, id := range []string{parent.ID, c1.ID, c2.ID} {
		if e.scanInt(`SELECT deleted_at IS NOT NULL FROM tasks WHERE id = ?`, id) != 1 {
			t.Fatalf("%s not in trash", id)
		}
	}
	// The task module never wrote the events itself — they carry the
	// foundation's trash summary.
	if got := e.acts(parent.ID, schema.EventEntityDeleted)[0].Payload.Summary; got != "移入回收站" {
		t.Fatalf("delete summary = %q (must be the trash-layer text)", got)
	}

	// Restoring the parent brings the children back.
	if _, err := e.m.Tasks().Restore(e.ctx, schema.ActorUser, parent.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, id := range []string{parent.ID, c1.ID, c2.ID} {
		if e.scanInt(`SELECT deleted_at IS NULL FROM tasks WHERE id = ?`, id) != 1 {
			t.Fatalf("%s not restored", id)
		}
	}

	// Deleting only a child: parent survives, counter drops.
	if err := e.m.Tasks().SoftDelete(e.ctx, schema.ActorUser, c2.ID); err != nil {
		t.Fatal(err)
	}
	board, _ := e.m.Views().Board(e.ctx, BoardQuery{})
	card := findCard(board, "Todo", parent.ID)
	if card == nil || card.SubtaskTotal != 1 {
		t.Fatalf("parent card after child delete = %+v", card)
	}

	// Restoring a child alone while its parent is gone → ErrInvalidParent.
	if err := e.m.Tasks().SoftDelete(e.ctx, schema.ActorUser, parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Tasks().Restore(e.ctx, schema.ActorUser, c1.ID); !errors.Is(err, ErrInvalidParent) {
		t.Fatalf("orphan restore: err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// AC-20/21 archiving
// ---------------------------------------------------------------------------

func TestAC20_ArchiveVisibility(t *testing.T) {
	e := newTestEnv(t)
	plain := e.create("普通", "Todo")
	finished, _ := e.m.Tasks().Move(e.ctx, schema.ActorUser, MoveInput{ID: e.create("完结", "Review").ID, Column: "Done"})
	archivedDone, err := e.m.Tasks().Archive(e.ctx, schema.ActorUser, finished.ID)
	if err != nil || archivedDone.ArchivedAt == nil {
		t.Fatalf("archive done task: %+v err=%v", archivedDone, err)
	}
	if archivedDone.CompletedAt == nil {
		t.Fatal("archive must not clear completed_at")
	}
	completedAt := *archivedDone.CompletedAt
	sortBefore := plain.SortOrder

	archived, err := e.m.Tasks().Archive(e.ctx, schema.ActorUser, plain.ID)
	if err != nil || archived.ArchivedAt == nil {
		t.Fatalf("archive: %+v err=%v", archived, err)
	}

	// Gone from Board / List / Blocked, present in the archive list.
	board, _ := e.m.Views().Board(e.ctx, BoardQuery{})
	if findCard(board, "Todo", plain.ID) != nil {
		t.Fatal("archived card still on the board")
	}
	page, _ := e.m.Tasks().List(e.ctx, Query{})
	for _, s := range page.Items {
		if s.ID == plain.ID {
			t.Fatal("archived in default List")
		}
	}
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser,
		UpdateInput{ID: plain.ID, Blocked: new(true)}); !errors.Is(err, ErrTaskArchived) {
		t.Fatalf("blocked toggle on archived: %v", err)
	}
	blocked, _ := e.m.Views().Blocked(e.ctx)
	for _, s := range blocked.Items {
		if s.ID == plain.ID {
			t.Fatal("archived in Blocked")
		}
	}
	archivedPage, err := e.m.Tasks().ListArchived(e.ctx, Query{})
	if err != nil || len(archivedPage.Items) != 2 {
		t.Fatalf("ListArchived = %v err=%v", idsOf(archivedPage.Items), err)
	}
	// Not in the trash: archiving is not deleting.
	items, err := trashService(e).List(e.ctx, trash.ListQuery{EntityTypes: []schema.EntityType{schema.EntityTask}})
	if err != nil || len(items) != 0 {
		t.Fatalf("archive entered the trash: %v err=%v", items, err)
	}

	// Unarchive returns to the exact column and position.
	e.advance(time.Second)
	restored, err := e.m.Tasks().Unarchive(e.ctx, schema.ActorUser, plain.ID)
	if err != nil || restored.ArchivedAt != nil {
		t.Fatalf("unarchive: %+v err=%v", restored, err)
	}
	if restored.Column != "Todo" || restored.SortOrder != sortBefore {
		t.Fatalf("restored position: %+v", restored)
	}
	again, _ := e.m.Tasks().Get(e.ctx, finished.ID)
	if again.CompletedAt == nil || !again.CompletedAt.Equal(completedAt) {
		t.Fatal("completed_at changed across archiving")
	}

	// One status.changed per archive transition, carrying {archived}.
	acts := e.acts(plain.ID, schema.EventStatusChanged)
	if len(acts) != 2 ||
		objectPayload(t, acts[0], false, "archived") != true ||
		objectPayload(t, acts[1], false, "archived") != false ||
		objectPayload(t, acts[1], true, "archived") != true {
		t.Fatalf("archive events = %+v", acts)
	}
}

func TestAC21_ArchiveWriteGate(t *testing.T) {
	e := newTestEnv(t)
	archived := e.create("归档", "Todo")
	if _, err := e.m.Tasks().Archive(e.ctx, schema.ActorUser, archived.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Tasks().Move(e.ctx, schema.ActorUser,
		MoveInput{ID: archived.ID, Column: "Review"}); !errors.Is(err, ErrTaskArchived) {
		t.Fatalf("move archived: %v", err)
	}
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser,
		UpdateInput{ID: archived.ID, Title: new("改")}); !errors.Is(err, ErrTaskArchived) {
		t.Fatalf("edit archived: %v", err)
	}
	if _, err := e.m.Tasks().Complete(e.ctx, schema.ActorUser, archived.ID); !errors.Is(err, ErrTaskArchived) {
		t.Fatalf("complete archived: %v", err)
	}
	if _, err := e.m.Tasks().SetWeekPlan(e.ctx, schema.ActorUser, archived.ID, new("2026-W46"), nil); !errors.Is(err, ErrTaskArchived) {
		t.Fatalf("weekplan archived: %v", err)
	}
	if _, err := e.m.Checklist().Add(e.ctx, schema.ActorUser, archived.ID, "勾", nil); !errors.Is(err, ErrTaskArchived) {
		t.Fatalf("checklist on archived: %v", err)
	}
	// The three permitted writes still work.
	if err := e.m.Tasks().SoftDelete(e.ctx, schema.ActorUser, archived.ID); err != nil {
		t.Fatalf("delete archived: %v", err)
	}
	if _, err := e.m.Tasks().Restore(e.ctx, schema.ActorUser, archived.ID); err != nil {
		t.Fatalf("restore archived: %v", err)
	}
	if _, err := e.m.Tasks().Unarchive(e.ctx, schema.ActorUser, archived.ID); err != nil {
		t.Fatalf("unarchive: %v", err)
	}

	// While the parent is archived its subtask details are read-only
	// (TASK-10 / AC-21). Re-archive it first: the gate checks above
	// exercised the unarchive/restore path and left it active.
	if _, err := e.m.Tasks().Archive(e.ctx, schema.ActorUser, archived.ID); err != nil {
		t.Fatal(err)
	}
	sub := e.create("子任务", "Todo")
	e.exec(`UPDATE tasks SET parent_task_id = ? WHERE id = ?`, archived.ID, sub.ID)
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser,
		UpdateInput{ID: sub.ID, Title: new("改子")}); !errors.Is(err, ErrTaskArchived) {
		t.Fatalf("edit subtask of archived parent: %v", err)
	}
	if _, err := e.m.Tasks().Move(e.ctx, schema.ActorUser,
		MoveInput{ID: sub.ID, Column: "Review"}); !errors.Is(err, ErrTaskArchived) {
		t.Fatalf("move subtask of archived parent: %v", err)
	}
}

// trashService reaches the configured foundation trash for assertions.
func trashService(e *testEnv) *trash.Service { return e.m.opts.Trash }

// ---------------------------------------------------------------------------
// AC-22 trash restore keeps position; archived project blocks all writes
// ---------------------------------------------------------------------------

func TestAC22_TrashRestoreAndProjectGate(t *testing.T) {
	e := newTestEnv(t)
	other := e.create("锚", "In Progress")
	task := e.create("删我", "In Progress")
	sortBefore := task.SortOrder

	if err := e.m.Tasks().SoftDelete(e.ctx, schema.ActorUser, task.ID); err != nil {
		t.Fatal(err)
	}
	restored, err := e.m.Tasks().Restore(e.ctx, schema.ActorUser, task.ID)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Column != "In Progress" || restored.SortOrder != sortBefore {
		t.Fatalf("restore lost position: %+v", restored)
	}
	order := e.columnNamesOf("In Progress")
	if len(order) != 2 || order[0] != other.ID || order[1] != task.ID {
		t.Fatalf("order after restore = %v", order)
	}

	// Archived project: every write path returns ErrProjectArchived while
	// reads keep working (AC-22, SPEC-08 §4.1).
	e.archiveProject()
	gates := map[string]func() error{
		"Create": func() error {
			_, err := e.m.Tasks().Create(e.ctx, schema.ActorUser, CreateInput{Title: "x"})
			return err
		},
		"Update": func() error {
			_, err := e.m.Tasks().Update(e.ctx, schema.ActorUser, UpdateInput{ID: task.ID, Title: new("y")})
			return err
		},
		"Move": func() error {
			_, err := e.m.Tasks().Move(e.ctx, schema.ActorUser, MoveInput{ID: task.ID, Column: "Done"})
			return err
		},
		"Complete":   func() error { _, err := e.m.Tasks().Complete(e.ctx, schema.ActorUser, task.ID); return err },
		"Archive":    func() error { _, err := e.m.Tasks().Archive(e.ctx, schema.ActorUser, task.ID); return err },
		"SoftDelete": func() error { return e.m.Tasks().SoftDelete(e.ctx, schema.ActorUser, task.ID) },
		"Checklist":  func() error { _, err := e.m.Checklist().Add(e.ctx, schema.ActorUser, task.ID, "x", nil); return err },
		"LabelAdd":   func() error { _, err := e.m.Labels().Add(e.ctx, schema.ActorUser, task.ID, "x", nil); return err },
		"ColCreate": func() error {
			_, err := e.m.Columns().Create(e.ctx, schema.ActorUser, CreateColumnInput{Name: "新列"})
			return err
		},
		"ColRename":  func() error { _, err := e.m.Columns().Rename(e.ctx, schema.ActorUser, "Review", "评审"); return err },
		"ColSetDone": func() error { _, err := e.m.Columns().SetDone(e.ctx, schema.ActorUser, "Review", true); return err },
		"ColDelete":  func() error { return e.m.Columns().Delete(e.ctx, schema.ActorUser, "Review", "Done") },
		"LinkAdd": func() error {
			_, err := e.m.Links().Add(e.ctx, task.ID, schema.RelationRelatedTo, schema.EntityNote, e.uuidFixture(), schema.ActorUser)
			return err
		},
	}
	for name, call := range gates {
		if err := call(); !errors.Is(err, ErrProjectArchived) {
			t.Fatalf("%s under archived project: err = %v", name, err)
		}
	}
	if _, err := e.m.Views().Board(e.ctx, BoardQuery{}); err != nil {
		t.Fatalf("reads must stay open: %v", err)
	}
	detail, err := e.m.Tasks().Get(e.ctx, task.ID)
	if err != nil || detail.Title != "删我" {
		t.Fatalf("Get after gate: %+v err=%v", detail, err)
	}
}

// ---------------------------------------------------------------------------
// AC-23..26 relations
// ---------------------------------------------------------------------------

func TestAC23_RelationsVisibleBothWays(t *testing.T) {
	e := newTestEnv(t)
	noteID := e.uuidFixture()
	e.insertNote(noteID, "拉格朗日笔记")
	task := e.create("推导", "Todo")

	before := e.actCount()
	relID, err := e.m.Links().Add(e.ctx, task.ID, schema.RelationRelatedTo, schema.EntityNote, noteID, schema.ActorUser)
	if err != nil || !ids.Valid(relID) {
		t.Fatalf("add: id=%s err=%v", relID, err)
	}
	// Exactly one relation.added written by the foundation.
	if e.actCount() != before+1 {
		t.Fatalf("link events = %d", e.actCount()-before)
	}
	act := e.lastAct(schema.EventRelationAdded)
	if objectPayload(t, act, false, "dst_type") != "note" || objectPayload(t, act, false, "dst_id") != noteID {
		t.Fatalf("relation payload = %v", *act.Payload.New)
	}

	links, err := e.m.Links().List(e.ctx, task.ID)
	if err != nil || len(links) != 1 || links[0].DstTitle != "拉格朗日笔记" ||
		links[0].TargetState != relations.TargetAlive {
		t.Fatalf("links = %+v err=%v", links, err)
	}
	detail, _ := e.m.Tasks().Get(e.ctx, task.ID)
	if len(detail.Links) != 1 || detail.Links[0].RelationID != relID {
		t.Fatalf("detail links = %+v", detail.Links)
	}
	back, err := e.m.Links().ListTasksByTarget(e.ctx, schema.EntityNote, noteID)
	if err != nil || len(back) != 1 || back[0].ID != task.ID {
		t.Fatalf("back-link = %v err=%v", back, err)
	}

	// deliverable_of to weekly_report is in the v1 direction table.
	reportID := e.uuidFixture()
	e.insertWeeklyReport(reportID, "2026-11-02", "2026-11-08")
	if _, err := e.m.Links().Add(e.ctx, task.ID, schema.RelationDeliverableOf, schema.EntityWeeklyReport, reportID, schema.ActorUser); err != nil {
		t.Fatalf("deliverable_of weekly_report: %v", err)
	}
}

func TestAC24_TargetDeletedMarker(t *testing.T) {
	e := newTestEnv(t)
	noteID := e.uuidFixture()
	e.insertNote(noteID, "被删笔记")
	task := e.create("推导", "Todo")
	if _, err := e.m.Links().Add(e.ctx, task.ID, schema.RelationRelatedTo, schema.EntityNote, noteID, schema.ActorUser); err != nil {
		t.Fatal(err)
	}
	if err := e.inTx(func(tx *sql.Tx) error {
		return e.m.opts.Trash.SoftDelete(e.ctx, tx, trash.Ref{Type: schema.EntityNote, ID: noteID}, schema.ActorUser)
	}); err != nil {
		t.Fatalf("delete note: %v", err)
	}
	links, _ := e.m.Links().List(e.ctx, task.ID)
	if len(links) != 1 || links[0].TargetState != relations.TargetSoftDeleted {
		t.Fatalf("links = %+v", links)
	}
	// The relation row survives.
	if n := e.scanInt(`SELECT count(*) FROM entity_relations WHERE dst_id = ?`, noteID); n != 1 {
		t.Fatal("relation row must survive the target's soft deletion")
	}
	// New links to a deleted target are refused.
	task2 := e.create("另一任务", "Todo")
	if _, err := e.m.Links().Add(e.ctx, task2.ID, schema.RelationRelatedTo, schema.EntityNote, noteID, schema.ActorUser); !errors.Is(err, errs.ErrEntityDeleted) {
		t.Fatalf("link to deleted target: %v", err)
	}
	// Restoring the target clears the marker.
	if err := e.inTx(func(tx *sql.Tx) error {
		return e.m.opts.Trash.Restore(e.ctx, tx, trash.Ref{Type: schema.EntityNote, ID: noteID}, schema.ActorUser)
	}); err != nil {
		t.Fatal(err)
	}
	links, _ = e.m.Links().List(e.ctx, task.ID)
	if links[0].TargetState != relations.TargetAlive {
		t.Fatalf("after restore = %+v", links[0])
	}
}

func (e *testEnv) inTx(fn func(tx *sql.Tx) error) error {
	return e.handle.InTx(e.ctx, fn)
}

func TestAC25_RelationIdempotent(t *testing.T) {
	e := newTestEnv(t)
	noteID := e.uuidFixture()
	e.insertNote(noteID, "笔记")
	task := e.create("推导", "Todo")
	before := e.actCount()
	id1, err := e.m.Links().Add(e.ctx, task.ID, schema.RelationRelatedTo, schema.EntityNote, noteID, schema.ActorUser)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := e.m.Links().Add(e.ctx, task.ID, schema.RelationRelatedTo, schema.EntityNote, noteID, schema.ActorUser)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("idempotence returned %s vs %s", id1, id2)
	}
	if n := e.scanInt(`SELECT count(*) FROM entity_relations WHERE src_id = ?`, task.ID); n != 1 {
		t.Fatalf("relation rows = %d", n)
	}
	if e.actCount() != before+1 {
		t.Fatalf("events = %d, want one relation.added", e.actCount()-before)
	}
}

func TestAC26_ScopeRejections(t *testing.T) {
	e := newTestEnv(t)
	task := e.create("推导", "Todo")

	// task → paper is outside the v1 direction table (O-13).
	if _, err := e.m.Links().Add(e.ctx, task.ID, schema.RelationDeliverableOf,
		schema.EntityPaper, e.uuidFixture(), schema.ActorUser); !errors.Is(err, errs.ErrRelationNotAllowed) {
		t.Fatalf("task→paper deliverable: %v", err)
	}
	if _, err := e.m.Links().Add(e.ctx, task.ID, schema.RelationRelatedTo,
		schema.EntityPaper, e.uuidFixture(), schema.ActorUser); !errors.Is(err, errs.ErrRelationNotAllowed) {
		t.Fatalf("task→paper related: %v", err)
	}
	// Cross-project target: a well-formed UUID that lives in another
	// project's database.
	other := newTestEnv(t)
	foreignNote := other.uuidFixture()
	other.insertNote(foreignNote, "别的项目")
	if _, err := e.m.Links().Add(e.ctx, task.ID, schema.RelationRelatedTo,
		schema.EntityNote, foreignNote, schema.ActorUser); !errors.Is(err, errs.ErrRelationNotAllowed) {
		t.Fatalf("cross-project: %v", err)
	}
	// Self relation stays rejected by the foundation.
	if _, err := e.m.Links().Add(e.ctx, task.ID, schema.RelationRelatedTo,
		schema.EntityTask, task.ID, schema.ActorUser); !errors.Is(err, errs.ErrSelfRelation) {
		t.Fatalf("self: %v", err)
	}
	// references/validates/derived_from are note/experiment semantics.
	if _, err := e.m.Links().Add(e.ctx, task.ID, schema.RelationReferences,
		schema.EntityNote, foreignNote, schema.ActorUser); !errors.Is(err, errs.ErrRelationNotAllowed) {
		t.Fatalf("references: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AC-27/28 port, actors, MCP-path parity
// ---------------------------------------------------------------------------

func TestAC27_CompleterPort(t *testing.T) {
	e := newTestEnv(t)
	task := e.create("周报口径", "Review")
	dlp := new("2026-11-14")
	prio := PriorityHigh
	labels := []string{"综述"}
	if _, err := e.m.Tasks().Update(e.ctx, schema.ActorUser, UpdateInput{
		ID: task.ID, Deadline: &dlp, Priority: &prio, Labels: &labels,
	}); err != nil {
		t.Fatal(err)
	}

	// The Overview quick action completes through the port with its actor.
	completer := NewCompleter(e.f.Workspace)
	if err := completer.CompleteTask(e.ctx, e.projectID, task.ID, schema.ActorMCP); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	card := findCard(e.mustBoard(), "Done", task.ID)
	if card == nil {
		t.Fatal("port did not move the card to Done")
	}
	if card.CompletedAt == nil || card.Deadline == nil || *card.Deadline != *dlp ||
		card.Priority != PriorityHigh || len(card.Labels) != 1 {
		t.Fatalf("summary fields = %+v", card)
	}
	act := e.lastAct(schema.EventStatusChanged)
	if act.Actor != schema.ActorMCP {
		t.Fatalf("port event actor = %s", act.Actor)
	}
	// Idempotent second call adds no event.
	before := e.actCount()
	if err := completer.CompleteTask(e.ctx, e.projectID, task.ID, schema.ActorMCP); err != nil {
		t.Fatal(err)
	}
	if e.actCount() != before {
		t.Fatal("second CompleteTask emitted")
	}
}

func TestAC28_ActorsAndSameColumnSilence(t *testing.T) {
	e := newTestEnv(t)
	mcp, err := e.m.Tasks().Create(e.ctx, schema.ActorMCP, CreateInput{Title: "Agent建的任务"})
	if err != nil {
		t.Fatalf("mcp create: %v", err)
	}
	act := e.acts(mcp.ID, schema.EventEntityCreated)[0]
	if act.Actor != schema.ActorMCP || act.Payload.Actor != schema.ActorMCP {
		t.Fatalf("mcp actor = %s / payload %s", act.Actor, act.Payload.Actor)
	}
	ui := e.create("UI建的任务", "")
	if ui.Column != "Backlog" {
		t.Fatalf("default column = %s", ui.Column)
	}
	act = e.acts(ui.ID, schema.EventEntityCreated)[0]
	if act.Actor != schema.ActorUser {
		t.Fatalf("ui actor = %s", act.Actor)
	}

	before := e.actCount()
	if _, err := e.m.Tasks().Move(e.ctx, schema.ActorMCP,
		MoveInput{ID: mcp.ID, Column: "Backlog", AfterID: new(ui.ID)}); err != nil {
		t.Fatalf("same-column move: %v", err)
	}
	if e.actCount() != before {
		t.Fatalf("task_move same column emitted %d events", e.actCount()-before)
	}
}

func (e *testEnv) mustBoard() BoardView {
	view, err := e.m.Views().Board(e.ctx, BoardQuery{})
	if err != nil {
		e.t.Fatalf("board: %v", err)
	}
	return view
}
