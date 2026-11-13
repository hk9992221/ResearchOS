package kanban

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/schema"
)

// taskColumns is the full tasks projection used by Get/Create/Update scans.
const taskColumns = `id, title, "column", parent_task_id, description_md, priority,
deadline, blocked, labels, week_plan, sort_order, completed_at, archived_at,
deleted_at, created_at, updated_at`

// rowScanner is satisfied by *sql.Row and *sql.Rows scanning one task.
type rowScanner interface {
	Scan(dest ...any) error
}

func nullText(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

// parseOptTime decodes an optional clock.Format column.
func parseOptTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := clock.Parse(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// optTimeText renders an optional time for a SQL text parameter.
func optTimeText(t *time.Time) any {
	if t == nil {
		return nil
	}
	return clock.Format(*t)
}

// scanTask maps one tasks row (found=false when the row is absent).
func scanTask(rs rowScanner) (Task, bool, error) {
	var (
		t                            Task
		parent, prio, deadline, week sql.NullString
		completed, archived, deleted sql.NullString
		created, updated             string
		blocked                      int
		labelsRaw                    string
		sort                         int64
	)
	if err := rs.Scan(&t.ID, &t.Title, &t.Column, &parent, &t.DescriptionMD, &prio,
		&deadline, &blocked, &labelsRaw, &week, &sort, &completed, &archived,
		&deleted, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return Task{}, false, nil
		}
		return Task{}, false, fmt.Errorf("kanban: scan task: %w", err)
	}
	at, err := clock.Parse(updated)
	if err != nil {
		return Task{}, false, fmt.Errorf("kanban: task %s updated_at: %w", t.ID, err)
	}
	createdAt, err := clock.Parse(created)
	if err != nil {
		return Task{}, false, fmt.Errorf("kanban: task %s created_at: %w", t.ID, err)
	}
	t.ParentTaskID = nullText(parent)
	t.Priority = PriorityNone
	if prio.Valid && prio.String != "" {
		t.Priority = Priority(prio.String)
	}
	t.Deadline = nullText(deadline)
	t.Blocked = blocked != 0
	t.Labels = decodeLabels(labelsRaw)
	t.WeekPlan = nullText(week)
	t.SortOrder = int(sort)
	if t.CompletedAt, err = parseOptTime(completed); err != nil {
		return Task{}, false, fmt.Errorf("kanban: task %s completed_at: %w", t.ID, err)
	}
	if t.ArchivedAt, err = parseOptTime(archived); err != nil {
		return Task{}, false, fmt.Errorf("kanban: task %s archived_at: %w", t.ID, err)
	}
	if t.DeletedAt, err = parseOptTime(deleted); err != nil {
		return Task{}, false, fmt.Errorf("kanban: task %s deleted_at: %w", t.ID, err)
	}
	t.CreatedAt = createdAt
	t.UpdatedAt = at
	return t, true, nil
}

// taskByID loads one row of any lifecycle state.
func (m *Module) taskByID(ctx context.Context, ex execer, id string) (Task, bool, error) {
	row := ex.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
	return scanTask(row)
}

// insertTask writes a new row; the caller has completed all validation.
func (m *Module) insertTask(ctx context.Context, tx *sql.Tx, t Task) error {
	labels, err := encodeLabels(t.Labels)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO tasks(id, title, "column", parent_task_id, description_md, priority,
		 deadline, blocked, labels, week_plan, sort_order, completed_at, archived_at,
		 created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Title, t.Column, parentParam(t.ParentTaskID), t.DescriptionMD,
		t.Priority.stored(), nullString(t.Deadline), boolInt(t.Blocked), labels,
		nullString(t.WeekPlan), t.SortOrder, optTimeText(t.CompletedAt),
		optTimeText(t.ArchivedAt), clock.Format(t.CreatedAt), clock.Format(t.UpdatedAt))
	if err != nil {
		return fmt.Errorf("kanban: insert task: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// task_columns
// ---------------------------------------------------------------------------

func (m *Module) listColumns(ctx context.Context, ex execer) ([]Column, error) {
	rows, err := ex.QueryContext(ctx,
		`SELECT name, kind, is_done, position FROM task_columns ORDER BY position, name`)
	if err != nil {
		return nil, fmt.Errorf("kanban: list columns: %w", err)
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var c Column
		var isDone int
		if err := rows.Scan(&c.Name, &c.Kind, &isDone, &c.Position); err != nil {
			return nil, fmt.Errorf("kanban: list columns: %w", err)
		}
		c.IsDone = isDone != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *Module) columnByName(ctx context.Context, ex execer, name string) (Column, bool, error) {
	var c Column
	var isDone int
	err := ex.QueryRowContext(ctx,
		`SELECT name, kind, is_done, position FROM task_columns WHERE name = ?`, name).
		Scan(&c.Name, &c.Kind, &isDone, &c.Position)
	if err == sql.ErrNoRows {
		return Column{}, false, nil
	}
	if err != nil {
		return Column{}, false, fmt.Errorf("kanban: column %s: %w", name, err)
	}
	c.IsDone = isDone != 0
	return c, true, nil
}

// defaultColumnName is the Create fallback: the non-terminal column with the
// smallest position, or the leftmost column when every column is terminal
// (TASK-01).
func (m *Module) defaultColumnName(ctx context.Context, ex execer) (string, error) {
	var name string
	err := ex.QueryRowContext(ctx,
		`SELECT name FROM task_columns ORDER BY is_done ASC, position ASC LIMIT 1`).Scan(&name)
	if err == sql.ErrNoRows {
		return "", ErrColumnNotFound
	}
	if err != nil {
		return "", fmt.Errorf("kanban: default column: %w", err)
	}
	return name, nil
}

// leftmostDoneColumn is Complete's target (SPEC-08 §5.2 Complete).
func (m *Module) leftmostDoneColumn(ctx context.Context, ex execer) (string, bool, error) {
	var name string
	err := ex.QueryRowContext(ctx,
		`SELECT name FROM task_columns WHERE is_done = 1 ORDER BY position ASC LIMIT 1`).Scan(&name)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("kanban: done column: %w", err)
	}
	return name, true, nil
}

// ---------------------------------------------------------------------------
// sort_order placement (SPEC-08 §5.2 rules 1/3)
// ---------------------------------------------------------------------------

// domainSlot is one member of a sort domain (column × parent, live rows).
type domainSlot struct {
	id   string
	sort int
}

// nextTaskSort is the append position of the target domain.
func (m *Module) nextTaskSort(ctx context.Context, ex execer, column string, parent *string) (int, error) {
	var max sql.NullInt64
	err := ex.QueryRowContext(ctx,
		`SELECT MAX(sort_order) FROM tasks
		 WHERE "column" = ? AND parent_task_id IS ? AND deleted_at IS NULL`,
		column, parentParam(parent)).Scan(&max)
	if err != nil {
		return 0, fmt.Errorf("kanban: next sort: %w", err)
	}
	if !max.Valid {
		return sortStep, nil
	}
	return int(max.Int64) + sortStep, nil
}

// taskDomain lists the live members of a sort domain in order (mover
// excluded).
func (m *Module) taskDomain(ctx context.Context, ex execer, column string, parent *string, excludeID string) ([]domainSlot, error) {
	rows, err := ex.QueryContext(ctx,
		`SELECT id, sort_order FROM tasks
		 WHERE "column" = ? AND parent_task_id IS ? AND deleted_at IS NULL AND id <> ?
		 ORDER BY sort_order, id`,
		column, parentParam(parent), excludeID)
	if err != nil {
		return nil, fmt.Errorf("kanban: task domain: %w", err)
	}
	defer rows.Close()
	var out []domainSlot
	for rows.Next() {
		var s domainSlot
		var sort int64
		if err := rows.Scan(&s.id, &sort); err != nil {
			return nil, fmt.Errorf("kanban: task domain: %w", err)
		}
		s.sort = int(sort)
		out = append(out, s)
	}
	return out, rows.Err()
}

// placement is the outcome of planInsert.
type placement struct {
	// sort is the moving member's new sort_order.
	sort int
	// ordered holds the full post-move domain (including the mover at its
	// new index) when the neighbouring gap was exhausted; the caller
	// renumbers every listed member with the step. ordered is nil when a
	// simple midpoint (or edge shift) sufficed.
	ordered []domainSlot
}

// planInsert computes the insertion sort_order for an anchored move inside
// the domain (fetched without the mover). before=true treats the anchor as
// the right neighbour, before=false as the left one.
func planInsert(domain []domainSlot, moverID string, anchorID string, before bool) (placement, error) {
	if anchorID == "" {
		if len(domain) == 0 {
			return placement{sort: sortStep}, nil
		}
		return placement{sort: domain[len(domain)-1].sort + sortStep}, nil
	}
	idx := -1
	for i, s := range domain {
		if s.id == anchorID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return placement{}, fmt.Errorf("%w: anchor %s is not in the target domain", ErrInvalidInput, anchorID)
	}
	var left, right *domainSlot
	insertAt := idx
	if before {
		right = &domain[idx]
		if idx > 0 {
			left = &domain[idx-1]
		}
	} else {
		left = &domain[idx]
		if idx+1 < len(domain) {
			right = &domain[idx+1]
		}
		insertAt = idx + 1
	}
	switch {
	case left != nil && right != nil:
		if right.sort-left.sort > 1 {
			return placement{sort: left.sort + (right.sort-left.sort)/2}, nil
		}
		// Gap exhausted: renumber the whole domain with the mover inserted
		// (SPEC-08 §5.2 rule 3).
		ordered := make([]domainSlot, 0, len(domain)+1)
		ordered = append(ordered, domain[:insertAt]...)
		ordered = append(ordered, domainSlot{id: moverID})
		ordered = append(ordered, domain[insertAt:]...)
		for i := range ordered {
			ordered[i].sort = (i + 1) * sortStep
		}
		return placement{sort: ordered[insertAt].sort, ordered: ordered}, nil
	case left != nil:
		return placement{sort: left.sort + sortStep}, nil
	case right != nil:
		return placement{sort: right.sort - sortStep}, nil
	default:
		return placement{sort: sortStep}, nil
	}
}

// checklistDomain lists the items of one task in order (excluded id dropped).
func (m *Module) checklistDomain(ctx context.Context, ex execer, taskID, excludeID string) ([]domainSlot, error) {
	rows, err := ex.QueryContext(ctx,
		`SELECT id, sort_order FROM task_checklist_items
		 WHERE task_id = ? AND id <> ? ORDER BY sort_order, id`, taskID, excludeID)
	if err != nil {
		return nil, fmt.Errorf("kanban: checklist domain: %w", err)
	}
	defer rows.Close()
	var out []domainSlot
	for rows.Next() {
		var s domainSlot
		var sort int64
		if err := rows.Scan(&s.id, &sort); err != nil {
			return nil, fmt.Errorf("kanban: checklist domain: %w", err)
		}
		s.sort = int(sort)
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// summaries (Board / List / Blocked / archive cards)
// ---------------------------------------------------------------------------

// taskSummaryColumns is the card projection: the row plus subtask, checklist
// and link counters from one aggregate statement (SPEC-08 §5.7: counters are
// correlated subqueries of a single scan, never N+1).
const taskSummaryColumns = `t.id, t.title, t."column", t.parent_task_id, t.priority,
t.deadline, t.blocked, t.labels, t.sort_order, t.completed_at, t.archived_at,
t.updated_at,
(SELECT count(*) FROM tasks s WHERE s.parent_task_id = t.id AND s.deleted_at IS NULL),
(SELECT count(*) FROM tasks s WHERE s.parent_task_id = t.id AND s.deleted_at IS NULL
   AND s."column" IN (SELECT name FROM task_columns WHERE is_done = 1)),
(SELECT count(*) FROM task_checklist_items c WHERE c.task_id = t.id),
(SELECT count(*) FROM task_checklist_items c WHERE c.task_id = t.id AND c.done = 1),
(SELECT count(*) FROM entity_relations r WHERE r.src_type = ? AND r.src_id = t.id)`

const priorityRankSQL = `CASE t.priority WHEN 'high' THEN 0 WHEN 'medium' THEN 1
WHEN 'low' THEN 2 ELSE 3 END`

const deadlineNullSQL = `CASE WHEN t.deadline IS NULL THEN 1 ELSE 0 END`

// sortKey is one component of a cursor-encodable ordering. Keys default to
// ASC; only updated_desc sets desc.
type sortKey struct {
	expr    string
	numeric bool
	desc    bool
}

// sortSpec is the ORDER BY plus the key tuple the cursor encodes.
type sortSpec struct {
	orderBy string
	keys    []sortKey
}

func sortSpecFor(s TaskSort) sortSpec {
	switch s {
	case SortUpdatedDesc:
		return sortSpec{
			orderBy: "t.updated_at DESC, t.id ASC",
			keys:    []sortKey{{expr: "t.updated_at", desc: true}},
		}
	case SortPriorityDesc:
		return sortSpec{
			orderBy: priorityRankSQL + " ASC, " + deadlineNullSQL + " ASC, COALESCE(t.deadline, '') ASC, t.sort_order ASC, t.id ASC",
			keys: []sortKey{
				{expr: priorityRankSQL, numeric: true},
				{expr: deadlineNullSQL, numeric: true},
				{expr: "COALESCE(t.deadline, '')"},
				{expr: "t.sort_order", numeric: true},
			},
		}
	case SortDeadlineAsc:
		return sortSpec{
			orderBy: deadlineNullSQL + " ASC, COALESCE(t.deadline, '') ASC, " + priorityRankSQL + " ASC, t.sort_order ASC, t.id ASC",
			keys: []sortKey{
				{expr: deadlineNullSQL, numeric: true},
				{expr: "COALESCE(t.deadline, '')"},
				{expr: priorityRankSQL, numeric: true},
				{expr: "t.sort_order", numeric: true},
			},
		}
	default: // position_asc
		return sortSpec{
			orderBy: "t.sort_order ASC, t.id ASC",
			keys:    []sortKey{{expr: "t.sort_order", numeric: true}},
		}
	}
}

// scanSummary maps one row of taskSummaryColumns plus len(keys) trailing
// text-cast cursor values.
func scanSummary(rs rowScanner, keyCount int) (TaskSummary, []string, error) {
	var (
		s                               TaskSummary
		parent, prio, deadline, updated sql.NullString
		completed, archived             sql.NullString
		blocked                         int
		labelsRaw                       string
		sort                            int64
		counters                        [5]int64
		keyVals                         = make([]sql.NullString, keyCount)
	)
	dest := []any{&s.ID, &s.Title, &s.Column, &parent, &prio, &deadline, &blocked,
		&labelsRaw, &sort, &completed, &archived, &updated,
		&counters[0], &counters[1], &counters[2], &counters[3], &counters[4]}
	for i := range keyVals {
		dest = append(dest, &keyVals[i])
	}
	if err := rs.Scan(dest...); err != nil {
		return TaskSummary{}, nil, fmt.Errorf("kanban: scan summary: %w", err)
	}
	s.ParentTaskID = nullText(parent)
	s.Priority = PriorityNone
	if prio.Valid && prio.String != "" {
		s.Priority = Priority(prio.String)
	}
	s.Deadline = nullText(deadline)
	s.Blocked = blocked != 0
	s.Labels = decodeLabels(labelsRaw)
	s.SortOrder = int(sort)
	s.SubtaskTotal = int(counters[0])
	s.SubtaskDone = int(counters[1])
	s.ChecklistTotal = int(counters[2])
	s.ChecklistDone = int(counters[3])
	s.LinkCount = int(counters[4])
	var err error
	if s.CompletedAt, err = parseOptTime(completed); err != nil {
		return s, nil, fmt.Errorf("kanban: summary completed_at: %w", err)
	}
	if s.ArchivedAt, err = parseOptTime(archived); err != nil {
		return s, nil, fmt.Errorf("kanban: summary archived_at: %w", err)
	}
	if updated.Valid {
		v, perr := clock.Parse(updated.String)
		if perr != nil {
			return s, nil, fmt.Errorf("kanban: summary updated_at: %w", perr)
		}
		s.UpdatedAt = v
	}
	keys := make([]string, keyCount)
	for i := range keyVals {
		keys[i] = keyVals[i].String
	}
	return s, keys, nil
}

// querySummaries runs one card query: filter fragments, ordering, keyset
// cursor and the limit+1 page probe (SPEC-08 §5.2 rules 10/11). An
// unparsable or stale cursor simply yields the first page; a cursor whose
// anchor row vanished is exactly that case and never errors. limit is the
// final page ceiling; the 50/200 API rule is applied at the service
// boundary (clampLimit), internal reads pass their own bound.
func (m *Module) querySummaries(ctx context.Context, ex execer, where []string, args []any, spec sortSpec, limit int, cursor string) ([]TaskSummary, string, error) {
	if limit <= 0 {
		limit = listDefaultLimit
	}
	if c, ok := decodeCursor(cursor); ok && len(c.Keys) == len(spec.keys) {
		pred, cargs := cursorPredicate(spec.keys, c.Keys, c.ID)
		where = append(where[:len(where):len(where)], pred)
		args = append(args[:len(args):len(args)], cargs...)
	}
	keySelect := make([]string, len(spec.keys))
	for i, k := range spec.keys {
		keySelect[i] = "CAST(" + k.expr + " AS TEXT)"
	}
	statement := `SELECT ` + taskSummaryColumns + `, ` + strings.Join(keySelect, ", ") + `
FROM tasks t
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY ` + spec.orderBy + `
LIMIT ?`
	all := make([]any, 0, 1+len(args)+1)
	all = append(all, string(schema.EntityTask))
	all = append(all, args...)
	all = append(all, limit+1)
	rows, err := ex.QueryContext(ctx, statement, all...)
	if err != nil {
		return nil, "", fmt.Errorf("kanban: query tasks: %w", err)
	}
	defer rows.Close()
	items := make([]TaskSummary, 0, limit+1)
	allKeys := make([][]string, 0, limit+1)
	for rows.Next() {
		s, keys, err := scanSummary(rows, len(spec.keys))
		if err != nil {
			return nil, "", err
		}
		items = append(items, s)
		allKeys = append(allKeys, keys)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("kanban: query tasks: %w", err)
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = encodeCursor(allKeys[limit-1], items[limit-1].ID)
	}
	return items, next, nil
}

// cursorPredicate builds the keyset chain for the stored key values.
func cursorPredicate(keys []sortKey, values []string, id string) (string, []any) {
	chain := "t.id > ?"
	args := []any{id}
	for i := len(keys) - 1; i >= 0; i-- {
		k := keys[i]
		ph := "?"
		if k.numeric {
			ph = "CAST(? AS INTEGER)"
		}
		op := ">"
		if k.desc {
			op = "<"
		}
		chain = "(" + k.expr + " " + op + " " + ph + " OR (" + k.expr + " = " + ph + " AND " + chain + "))"
		args = append([]any{values[i], values[i]}, args...)
	}
	return chain, args
}

// buildQueryWhere compiles a Query into SQL fragments.
func buildQueryWhere(q Query) ([]string, []any, error) {
	where := []string{"1 = 1"}
	var args []any
	if !q.IncludeDeleted {
		where = append(where, "t.deleted_at IS NULL")
	}
	if !q.IncludeArchived {
		where = append(where, "t.archived_at IS NULL")
	}
	if q.Column != nil {
		where = append(where, `t."column" = ?`)
		args = append(args, *q.Column)
	}
	if q.ParentID != nil {
		where = append(where, "t.parent_task_id = ?")
		args = append(args, *q.ParentID)
	} else if q.OnlyTopLevel {
		where = append(where, "t.parent_task_id IS NULL")
	}
	if len(q.Priority) > 0 {
		var names []any
		wantNone := false
		for _, p := range q.Priority {
			if p == PriorityNone || p == "" {
				wantNone = true
				continue
			}
			if !p.valid() {
				return nil, nil, fmt.Errorf("%w: priority %q", ErrInvalidInput, string(p))
			}
			names = append(names, string(p))
		}
		switch {
		case len(names) > 0 && wantNone:
			where = append(where, "(t.priority IN ("+placeholders(len(names))+") OR t.priority IS NULL)")
			args = append(args, names...)
		case len(names) > 0:
			where = append(where, "t.priority IN ("+placeholders(len(names))+")")
			args = append(args, names...)
		default:
			where = append(where, "t.priority IS NULL")
		}
	}
	for _, l := range q.Labels {
		where = append(where, "EXISTS (SELECT 1 FROM json_each(t.labels) WHERE json_each.value = ?)")
		args = append(args, l)
	}
	if q.DeadlineFrom != nil {
		where = append(where, "t.deadline >= ?")
		args = append(args, *q.DeadlineFrom)
	}
	if q.DeadlineTo != nil {
		where = append(where, "t.deadline <= ?")
		args = append(args, *q.DeadlineTo)
	}
	if q.Blocked != nil {
		where = append(where, "t.blocked = ?")
		args = append(args, boolInt(*q.Blocked))
	}
	if q.WeekPlan != nil {
		where = append(where, "t.week_plan = ?")
		args = append(args, *q.WeekPlan)
	}
	if q.Text != "" {
		p := "%" + escapeLike(strings.TrimSpace(q.Text)) + "%"
		where = append(where, `(t.title LIKE ? ESCAPE '\' OR t.description_md LIKE ? ESCAPE '\')`)
		args = append(args, p, p)
	}
	return where, args, nil
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// summariesByIDs loads card summaries for an explicit id set (back-links).
func (m *Module) summariesByIDs(ctx context.Context, ids []string) ([]TaskSummary, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	where := []string{"1 = 1", "t.id IN (" + placeholders(len(ids)) + ")"}
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := m.db().QueryContext(ctx, `SELECT `+taskSummaryColumns+` FROM tasks t WHERE `+
		strings.Join(where, " AND ")+` ORDER BY t.sort_order, t.id`,
		append([]any{string(schema.EntityTask)}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("kanban: summaries by id: %w", err)
	}
	defer rows.Close()
	var out []TaskSummary
	for rows.Next() {
		s, _, err := scanSummary(rows, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// counts
// ---------------------------------------------------------------------------

// countActiveTasks feeds the project task cap (§4.3).
func (m *Module) countActiveTasks(ctx context.Context, ex execer) (int, error) {
	var n int64
	err := ex.QueryRowContext(ctx,
		`SELECT count(*) FROM tasks WHERE deleted_at IS NULL AND archived_at IS NULL`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("kanban: count active tasks: %w", err)
	}
	return int(n), nil
}

// childTaskIDs lists subtasks; deleted selects the trash side.
func (m *Module) childTaskIDs(ctx context.Context, ex execer, parentID string, deleted bool) ([]string, error) {
	pred := "deleted_at IS NULL"
	if deleted {
		pred = "deleted_at IS NOT NULL"
	}
	rows, err := ex.QueryContext(ctx,
		`SELECT id FROM tasks WHERE parent_task_id = ? AND `+pred+` ORDER BY sort_order, id`,
		parentID)
	if err != nil {
		return nil, fmt.Errorf("kanban: child ids: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("kanban: child ids: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// liveLabelRows reads the labels column of live tasks for aggregation.
func (m *Module) liveLabelRows(ctx context.Context) ([]string, error) {
	rows, err := m.db().QueryContext(ctx,
		`SELECT labels FROM tasks WHERE deleted_at IS NULL AND archived_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("kanban: labels scan: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("kanban: labels scan: %w", err)
		}
		out = append(out, raw)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// checklist rows
// ---------------------------------------------------------------------------

const checklistColumns = `id, task_id, text, done, sort_order`

func scanChecklistItem(rs rowScanner) (ChecklistItem, bool, error) {
	var (
		i    ChecklistItem
		done int
		sort int64
	)
	err := rs.Scan(&i.ID, &i.TaskID, &i.Text, &done, &sort)
	if err == sql.ErrNoRows {
		return ChecklistItem{}, false, nil
	}
	if err != nil {
		return ChecklistItem{}, false, fmt.Errorf("kanban: scan checklist: %w", err)
	}
	i.Done = done != 0
	i.SortOrder = int(sort)
	return i, true, nil
}

func (m *Module) checklistItem(ctx context.Context, ex execer, id string) (ChecklistItem, bool, error) {
	row := ex.QueryRowContext(ctx,
		`SELECT `+checklistColumns+` FROM task_checklist_items WHERE id = ?`, id)
	return scanChecklistItem(row)
}

func (m *Module) listChecklist(ctx context.Context, ex execer, taskID string) ([]ChecklistItem, error) {
	rows, err := ex.QueryContext(ctx,
		`SELECT `+checklistColumns+` FROM task_checklist_items WHERE task_id = ?
		 ORDER BY sort_order, id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("kanban: list checklist: %w", err)
	}
	defer rows.Close()
	var out []ChecklistItem
	for rows.Next() {
		i, ok, err := scanChecklistItem(rows)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, i)
		}
	}
	return out, rows.Err()
}
