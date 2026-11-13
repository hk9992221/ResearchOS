package kanban

import (
	"context"
	"database/sql"

	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
)

// Event construction follows SPEC-08 §7.1/§7.2: this module emits only
// entity.created and status.changed, always through the project's
// events.Writer inside the caller transaction. entity.deleted/restored belong
// to foundation/trash, relation.added/removed to foundation/relations.
// Plain field edits emit nothing (the registry has no entity.updated).

// quoteTitle wraps a task title for the Chinese summary templates.
func quoteTitle(title string) string { return "「" + title + "」" }

// emitCreated writes entity.created with the New object {title, column,
// parent_task_id?} (TASK-01, §7.2 row 1).
func (m *Module) emitCreated(ctx context.Context, tx *sql.Tx, actor events.Actor, t Task) error {
	obj := events.Object{"title": t.Title, "column": t.Column}
	if t.ParentTaskID != nil {
		obj["parent_task_id"] = *t.ParentTaskID
	}
	return m.writer().Emit(ctx, tx, events.Event{
		Type:       schema.EventEntityCreated,
		EntityType: schema.EntityTask,
		EntityID:   t.ID,
		New:        &obj,
		Summary:    "创建任务" + quoteTitle(t.Title),
		Actor:      actor,
	})
}

// emitStatusChanged writes one status.changed with the given old/new.
func (m *Module) emitStatusChanged(ctx context.Context, tx *sql.Tx, actor events.Actor, taskID string, old, new events.Object, summary string) error {
	return m.writer().Emit(ctx, tx, events.Event{
		Type:       schema.EventStatusChanged,
		EntityType: schema.EntityTask,
		EntityID:   taskID,
		Old:        &old,
		New:        &new,
		Summary:    summary,
		Actor:      actor,
	})
}

// columnState renders the §7.2 N1 object {column, column_name, is_done} for
// one side of a column transition. column doubles as the task_columns key
// and the display name, so both keys carry Name; the snapshot rides in the
// payload and survives later renames.
func columnState(col Column) events.Object {
	return events.Object{
		"column":      col.Name,
		"column_name": col.Name,
		"is_done":     col.IsDone,
	}
}

// emitColumnMoved emits the single status.changed for a column transition:
// entering the first terminal column adds new.completed_at and reads 完成
// 任务; leaving every terminal column sets new.completed_at null and reads
// 重新打开任务; otherwise the 源列 → 目标列 form (§7.2 rows 2–4).
func (m *Module) emitColumnMoved(ctx context.Context, tx *sql.Tx, actor events.Actor, t Task, from, to Column, newCompletedAt *string) error {
	old := columnState(from)
	new := columnState(to)
	summary := quoteTitle(t.Title) + "：" + from.Name + " → " + to.Name
	switch {
	case !from.IsDone && to.IsDone:
		new["completed_at"] = *newCompletedAt
		summary = "完成任务" + quoteTitle(t.Title)
	case from.IsDone && !to.IsDone:
		new["completed_at"] = nil // JSON null per §7.2 "completed_at:null"
		summary = "重新打开任务" + quoteTitle(t.Title)
	}
	return m.emitStatusChanged(ctx, tx, actor, t.ID, old, new, summary)
}

// emitBlockedChanged emits the {blocked} pair (TASK-02, §7.2 row 5).
func (m *Module) emitBlockedChanged(ctx context.Context, tx *sql.Tx, actor events.Actor, t Task, to bool) error {
	summary := "任务" + quoteTitle(t.Title) + "标记为受阻"
	if !to {
		summary = "任务" + quoteTitle(t.Title) + "解除受阻"
	}
	return m.emitStatusChanged(ctx, tx, actor, t.ID,
		events.Object{"blocked": !to}, events.Object{"blocked": to}, summary)
}

// emitArchived emits the {archived} pair for Archive/Unarchive (§7.2 row 6,
// classification pending SPEC-00 clarification, O-3).
func (m *Module) emitArchived(ctx context.Context, tx *sql.Tx, actor events.Actor, t Task, to bool) error {
	summary := "归档任务" + quoteTitle(t.Title)
	if !to {
		summary = "取消归档任务" + quoteTitle(t.Title)
	}
	return m.emitStatusChanged(ctx, tx, actor, t.ID,
		events.Object{"archived": !to}, events.Object{"archived": to}, summary)
}
