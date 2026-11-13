package kanban

import (
	"context"
	"database/sql"
)

// viewService implements ViewService (SPEC-08 §5.7). Read-only projections:
// Board and Blocked always exclude archived and deleted tasks; card
// counters arrive with the rows from one aggregate statement (no N+1).

type viewService struct{ m *Module }

var _ ViewService = (*viewService)(nil)

// boardTaskLimit is the internal ceiling of one Board read: the project
// already caps live top-level tasks at maxActiveTasks (§4.3).
const boardTaskLimit = maxActiveTasks

func (v *viewService) Board(ctx context.Context, q BoardQuery) (BoardView, error) {
	var (
		cols  []Column
		items []TaskSummary
	)
	err := v.m.withSeededTx(ctx, func(tx *sql.Tx) error {
		var err error
		cols, err = v.m.listColumns(ctx, tx)
		return err
	})
	if err != nil {
		return BoardView{}, err
	}
	qq := Query{
		OnlyTopLevel: true,
		Priority:     q.Priority,
		Labels:       q.Labels,
		Blocked:      q.Blocked,
		Text:         q.Text,
	}
	where, args, err := buildQueryWhere(qq)
	if err != nil {
		return BoardView{}, err
	}
	items, _, err = v.m.querySummaries(ctx, v.m.db(), where, args,
		sortSpecFor(SortPositionAsc), boardTaskLimit, "")
	if err != nil {
		return BoardView{}, err
	}

	byColumn := make(map[string][]TaskSummary, len(cols))
	for _, s := range items {
		byColumn[s.Column] = append(byColumn[s.Column], s)
	}
	view := BoardView{Columns: make([]BoardColumn, 0, len(cols))}
	for _, c := range cols {
		view.Columns = append(view.Columns, BoardColumn{Column: c, Tasks: byColumn[c.Name]})
		delete(byColumn, c.Name)
	}
	if unknown := len(byColumn); unknown > 0 && v.m.opts.Logger != nil {
		// Tasks on a column name that no longer exists (manual DB edit or
		// a migration gap): surfaced for repair, invisible on the board.
		v.m.opts.Logger.Warn("kanban: board found tasks on unknown columns", "count", unknown)
	}
	return view, nil
}

// Blocked lists blocked, unfinished live tasks by deadline ASC then
// priority DESC (§5.7). "Unfinished" reads task_columns.is_done, never the
// kind string (§4.3: kind is a hint, completion is a column property).
func (v *viewService) Blocked(ctx context.Context) (Page[TaskSummary], error) {
	where := []string{
		"1 = 1",
		"t.deleted_at IS NULL",
		"t.archived_at IS NULL",
		"t.blocked = 1",
		`NOT EXISTS (SELECT 1 FROM task_columns c WHERE c.name = t."column" AND c.is_done = 1)`,
	}
	items, next, err := v.m.querySummaries(ctx, v.m.db(), where, nil,
		sortSpecFor(SortDeadlineAsc), boardTaskLimit, "")
	if err != nil {
		return Page[TaskSummary]{}, err
	}
	return Page[TaskSummary]{Items: items, NextCursor: next}, nil
}
