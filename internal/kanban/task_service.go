package kanban

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/trash"
)

// taskService implements TaskService (SPEC-08 §5.2).
type taskService struct{ m *Module }

var _ TaskService = (*taskService)(nil)

// sameParent compares optional parent ids.
func sameParent(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// isTerminalColumn reports whether name currently marks is_done.
func (m *Module) isTerminalColumn(ctx context.Context, ex execer, name string) (bool, error) {
	col, ok, err := m.columnByName(ctx, ex, name)
	if err != nil || !ok {
		return false, err
	}
	return col.IsDone, nil
}

// loadGateTask loads a task for a write that requires it to be alive;
// archived parents additionally block subtask writes (SPEC-08 §5.2 rule 8,
// TASK-10, AC-21).
func (m *Module) loadGateTask(ctx context.Context, id string) (Task, error) {
	if err := validateTaskID(id); err != nil {
		return Task{}, err
	}
	t, ok, err := m.taskByID(ctx, m.db(), id)
	if err != nil {
		return Task{}, err
	}
	if !ok || t.DeletedAt != nil {
		return Task{}, fmt.Errorf("%w: task %s", ErrTaskNotFound, id)
	}
	if t.ArchivedAt != nil {
		return Task{}, fmt.Errorf("%w: task %s", ErrTaskArchived, id)
	}
	if t.ParentTaskID != nil {
		p, pok, perr := m.taskByID(ctx, m.db(), *t.ParentTaskID)
		if perr != nil {
			return Task{}, perr
		}
		if pok && p.ArchivedAt != nil {
			return Task{}, fmt.Errorf("%w: parent task %s is archived", ErrTaskArchived, *t.ParentTaskID)
		}
	}
	return t, nil
}

// withSeededTx runs fn in a project transaction that has first ensured the
// default column seed (§4.3).
func (m *Module) withSeededTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return m.handle.InTx(ctx, func(tx *sql.Tx) error {
		if err := m.ensureDefaultColumns(ctx, tx); err != nil {
			return err
		}
		return fn(tx)
	})
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func (s *taskService) Create(ctx context.Context, actor events.Actor, in CreateInput) (Task, error) {
	title, err := validateTitle(in.Title)
	if err != nil {
		return Task{}, err
	}
	if err := validateDescription(in.DescriptionMD); err != nil {
		return Task{}, err
	}
	if err := validPriorityInput(in.Priority); err != nil {
		return Task{}, err
	}
	if err := validateDeadline(in.Deadline); err != nil {
		return Task{}, err
	}
	if err := validateWeekPlan(in.WeekPlan); err != nil {
		return Task{}, err
	}
	labels, err := sanitizeLabels(in.Labels)
	if err != nil {
		return Task{}, err
	}
	if in.ParentTaskID != nil {
		if err := validateTaskID(*in.ParentTaskID); err != nil {
			return Task{}, err
		}
	}
	if err := s.m.assertWritable(ctx); err != nil {
		return Task{}, err
	}

	var out Task
	a := kanbanActor(actor)
	err = s.m.withSeededTx(ctx, func(tx *sql.Tx) error {
		// Column resolution: explicit names must exist; empty selects the
		// leftmost non-terminal column (TASK-01).
		colName := in.Column
		if colName != "" {
			if _, ok, err := s.m.columnByName(ctx, tx, colName); err != nil {
				return err
			} else if !ok {
				return fmt.Errorf("%w: column %q", ErrColumnNotFound, colName)
			}
		} else if colName, err = s.m.defaultColumnName(ctx, tx); err != nil {
			return err
		}

		if in.ParentTaskID != nil {
			p, ok, perr := s.m.taskByID(ctx, tx, *in.ParentTaskID)
			if perr != nil {
				return perr
			}
			if !ok || p.DeletedAt != nil || p.ArchivedAt != nil {
				return fmt.Errorf("%w: parent %s", ErrInvalidParent, *in.ParentTaskID)
			}
			if p.ParentTaskID != nil {
				return fmt.Errorf("%w: parent %s is already a subtask", ErrSubtaskOnSubtask, *in.ParentTaskID)
			}
			kids, cerr := s.m.childTaskIDs(ctx, tx, p.ID, false)
			if cerr != nil {
				return cerr
			}
			if len(kids) >= maxSubtasksPerParent {
				return fmt.Errorf("%w: parent %s already has %d subtasks", ErrInvalidInput, p.ID, maxSubtasksPerParent)
			}
		}

		n, cerr := s.m.countActiveTasks(ctx, tx)
		if cerr != nil {
			return cerr
		}
		if n >= maxActiveTasks {
			return fmt.Errorf("%w: %d active tasks", ErrTaskLimitExceeded, n)
		}

		sort, serr := s.m.nextTaskSort(ctx, tx, colName, in.ParentTaskID)
		if serr != nil {
			return serr
		}

		now := s.m.clock.Now()
		task := Task{
			ID:            ids.MustNew(s.m.clock),
			Title:         title,
			Column:        colName,
			ParentTaskID:  in.ParentTaskID,
			DescriptionMD: in.DescriptionMD,
			Priority:      priorityOrDefault(in.Priority),
			Deadline:      in.Deadline,
			Labels:        labels,
			WeekPlan:      in.WeekPlan,
			SortOrder:     sort,
			CreatedAt:     now.UTC(),
			UpdatedAt:     now.UTC(),
		}
		if done, derr := s.m.isTerminalColumn(ctx, tx, colName); derr != nil {
			return derr
		} else if done {
			// Creating inside a terminal column counts as first entry
			// (§4.3 completion semantics).
			task.CompletedAt = &now
		}
		if ierr := s.m.insertTask(ctx, tx, task); ierr != nil {
			return ierr
		}
		if eerr := s.m.emitCreated(ctx, tx, a, task); eerr != nil {
			return eerr
		}
		out = task
		return nil
	})
	if err != nil {
		return Task{}, err
	}
	return out, nil
}

// priorityOrDefault normalizes the empty input to none.
func priorityOrDefault(p Priority) Priority {
	if p == "" {
		return PriorityNone
	}
	return p
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func (s *taskService) Get(ctx context.Context, id string) (TaskDetail, error) {
	if err := validateTaskID(id); err != nil {
		return TaskDetail{}, err
	}
	t, ok, err := s.m.taskByID(ctx, s.m.db(), id)
	if err != nil {
		return TaskDetail{}, err
	}
	if !ok {
		return TaskDetail{}, fmt.Errorf("%w: task %s", ErrTaskNotFound, id)
	}
	checklist, err := s.m.listChecklist(ctx, s.m.db(), id)
	if err != nil {
		return TaskDetail{}, err
	}
	subWhere := []string{"1 = 1", "t.parent_task_id = ?", "t.deleted_at IS NULL", "t.archived_at IS NULL"}
	subtasks, _, err := s.m.querySummaries(ctx, s.m.db(), subWhere, []any{id},
		sortSpecFor(SortPositionAsc), maxSubtasksPerParent, "")
	if err != nil {
		return TaskDetail{}, err
	}
	links, err := s.m.links.List(ctx, id)
	if err != nil {
		return TaskDetail{}, err
	}
	col := Column{Name: t.Column}
	if found, ok, cerr := s.m.columnByName(ctx, s.m.db(), t.Column); cerr != nil {
		return TaskDetail{}, cerr
	} else if ok {
		col = found
	}
	return TaskDetail{
		Task:      t,
		Checklist: checklist,
		Subtasks:  subtasks,
		Links:     links,
		Column:    col,
	}, nil
}

// ---------------------------------------------------------------------------
// List / ListArchived
// ---------------------------------------------------------------------------

// normalizeQuery validates the query and applies the defaults (sort
// position_asc, limit 50/200 handled by querySummaries).
func normalizeQuery(q Query) (TaskSort, error) {
	sort := q.Sort
	if sort == "" {
		sort = SortPositionAsc
	}
	if !sort.valid() {
		return "", fmt.Errorf("%w: sort %q", ErrInvalidInput, string(sort))
	}
	if q.ParentID != nil {
		if err := validateTaskID(*q.ParentID); err != nil {
			return "", err
		}
	}
	if q.Column != nil && *q.Column == "" {
		return "", fmt.Errorf("%w: empty column filter", ErrInvalidInput)
	}
	return sort, nil
}

// clampLimit applies the API page rule: default 50, hard cap 200 (O-11).
func clampLimit(n int) int {
	switch {
	case n <= 0:
		return listDefaultLimit
	case n > listMaxLimit:
		return listMaxLimit
	default:
		return n
	}
}

func (s *taskService) List(ctx context.Context, q Query) (Page[TaskSummary], error) {
	sort, err := normalizeQuery(q)
	if err != nil {
		return Page[TaskSummary]{}, err
	}
	where, args, err := buildQueryWhere(q)
	if err != nil {
		return Page[TaskSummary]{}, err
	}
	items, next, err := s.m.querySummaries(ctx, s.m.db(), where, args, sortSpecFor(sort), clampLimit(q.Limit), q.Cursor)
	if err != nil {
		return Page[TaskSummary]{}, err
	}
	return Page[TaskSummary]{Items: items, NextCursor: next}, nil
}

// ListArchived returns only archived, still-alive rows; q.IncludeDeleted is
// ignored and IncludeArchived is forced (SPEC-08 §5.2 rule 12).
func (s *taskService) ListArchived(ctx context.Context, q Query) (Page[TaskSummary], error) {
	sort, err := normalizeQuery(q)
	if err != nil {
		return Page[TaskSummary]{}, err
	}
	q.IncludeArchived = true
	q.IncludeDeleted = false
	where, args, err := buildQueryWhere(q)
	if err != nil {
		return Page[TaskSummary]{}, err
	}
	where = append(where, "t.archived_at IS NOT NULL")
	items, next, err := s.m.querySummaries(ctx, s.m.db(), where, args, sortSpecFor(sort), clampLimit(q.Limit), q.Cursor)
	if err != nil {
		return Page[TaskSummary]{}, err
	}
	return Page[TaskSummary]{Items: items, NextCursor: next}, nil
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (s *taskService) Update(ctx context.Context, actor events.Actor, in UpdateInput) (Task, error) {
	if err := s.m.assertWritable(ctx); err != nil {
		return Task{}, err
	}
	t, err := s.m.loadGateTask(ctx, in.ID)
	if err != nil {
		return Task{}, err
	}
	if in.ExpectedUpdatedAt != nil && *in.ExpectedUpdatedAt != t.Version() {
		return Task{}, ErrStaleRevision
	}

	if in.Title != nil {
		if _, err := validateTitle(*in.Title); err != nil {
			return Task{}, err
		}
	}
	if in.DescriptionMD != nil {
		if err := validateDescription(*in.DescriptionMD); err != nil {
			return Task{}, err
		}
	}
	if in.Priority != nil {
		if err := validPriorityInput(*in.Priority); err != nil {
			return Task{}, err
		}
	}
	if in.Deadline != nil {
		if err := validateDeadline(*in.Deadline); err != nil {
			return Task{}, err
		}
	}
	if in.Labels != nil {
		if _, err := sanitizeLabels(*in.Labels); err != nil {
			return Task{}, err
		}
	}
	newColumn := ""
	if in.Column != nil {
		c := *in.Column
		if c == "" {
			return Task{}, fmt.Errorf("%w: empty column", ErrInvalidInput)
		}
		if c != t.Column {
			newColumn = c
		}
	}
	blockedTo := -1
	if in.Blocked != nil && *in.Blocked != t.Blocked {
		blockedTo = boolInt(*in.Blocked)
	}
	noChange := in.Title == nil && in.DescriptionMD == nil && in.Priority == nil &&
		in.Deadline == nil && in.Labels == nil && in.Blocked == nil && newColumn == ""
	if noChange {
		return t, nil
	}

	a := kanbanActor(actor)
	var out Task
	err = s.m.withSeededTx(ctx, func(tx *sql.Tx) error {
		// Column change delegates to the Move migration semantics: append
		// to the end of the target column domain (§5.2 rule 2/3/4).
		var (
			fromCol, toCol Column
			newSort        int
			newCompleted   *time.Time
			clearCompleted bool
			columnChanged  = newColumn != ""
		)
		if columnChanged {
			var ok bool
			var cerr error
			fromCol, ok, cerr = s.m.columnByName(ctx, tx, t.Column)
			if cerr != nil {
				return cerr
			}
			if !ok {
				fromCol = Column{Name: t.Column}
			}
			if toCol, ok, cerr = s.m.columnByName(ctx, tx, newColumn); cerr != nil {
				return cerr
			} else if !ok {
				return fmt.Errorf("%w: column %q", ErrColumnNotFound, newColumn)
			}
			if newSort, cerr = s.m.nextTaskSort(ctx, tx, newColumn, t.ParentTaskID); cerr != nil {
				return cerr
			}
			if !fromCol.IsDone && toCol.IsDone && t.CompletedAt == nil {
				now := s.m.clock.Now().UTC()
				newCompleted = &now
			}
			if fromCol.IsDone && !toCol.IsDone {
				clearCompleted = true
			}
		}

		var set []string
		var args []any
		if in.Title != nil {
			title, _ := validateTitle(*in.Title)
			set = append(set, `title = ?`)
			args = append(args, title)
		}
		if in.DescriptionMD != nil {
			set = append(set, `description_md = ?`)
			args = append(args, *in.DescriptionMD)
		}
		if in.Priority != nil {
			set = append(set, `priority = ?`)
			args = append(args, (*in.Priority).stored())
		}
		if in.Deadline != nil {
			set = append(set, `deadline = ?`)
			args = append(args, nullString(*in.Deadline))
		}
		if in.Labels != nil {
			labels, _ := sanitizeLabels(*in.Labels)
			encoded, lerr := encodeLabels(labels)
			if lerr != nil {
				return lerr
			}
			set = append(set, `labels = ?`)
			args = append(args, encoded)
		}
		if blockedTo >= 0 {
			set = append(set, `blocked = ?`)
			args = append(args, blockedTo)
		}
		if columnChanged {
			set = append(set, `"column" = ?`, `sort_order = ?`)
			args = append(args, newColumn, newSort)
			switch {
			case newCompleted != nil:
				set = append(set, `completed_at = ?`)
				args = append(args, optTimeText(newCompleted))
			case clearCompleted:
				set = append(set, `completed_at = NULL`)
			}
		}
		set = append(set, `updated_at = ?`)
		args = append(args, s.m.now())

		// Placeholder order: every SET value, then the id, then the
		// optional concurrency token (§5.2 rule 2).
		where := `WHERE id = ? AND deleted_at IS NULL`
		if t.DeletedAt != nil {
			return fmt.Errorf("%w: task %s", ErrTaskNotFound, t.ID)
		}
		execArgs := append(append([]any{}, args...), t.ID)
		if in.ExpectedUpdatedAt != nil {
			where += ` AND updated_at = ?`
			execArgs = append(execArgs, *in.ExpectedUpdatedAt)
		}
		res, uerr := tx.ExecContext(ctx,
			`UPDATE tasks SET `+joinSet(set)+` `+where, execArgs...)
		if uerr != nil {
			return fmt.Errorf("kanban: update task: %w", uerr)
		}
		affected, aerr := res.RowsAffected()
		if aerr != nil {
			return aerr
		}
		if affected == 0 {
			// With a token this is the optimistic-lock veto; without one the
			// row can only have vanished or been deleted concurrently
			// (§5.2 rule 2).
			if in.ExpectedUpdatedAt != nil {
				return fmt.Errorf("%w: task %s", ErrStaleRevision, t.ID)
			}
			return fmt.Errorf("%w: task %s", ErrTaskNotFound, t.ID)
		}
		if columnChanged {
			completedPayload := (*string)(nil)
			if newCompleted != nil {
				s := optTimeText(newCompleted)
				if str, okk := s.(string); okk {
					completedPayload = &str
				}
			}
			if merr := s.m.emitColumnMoved(ctx, tx, a, t, fromCol, toCol, completedPayload); merr != nil {
				return merr
			}
		}
		if blockedTo >= 0 {
			if merr := s.m.emitBlockedChanged(ctx, tx, a, t, blockedTo == 1); merr != nil {
				return merr
			}
		}
		refreshed, _, rerr := s.m.taskByID(ctx, tx, t.ID)
		if rerr != nil {
			return rerr
		}
		out = refreshed
		return nil
	})
	if err != nil {
		return Task{}, err
	}
	return out, nil
}

// joinSet renders the SQL SET clause.
func joinSet(set []string) string {
	out := ""
	for i, c := range set {
		if i > 0 {
			out += ", "
		}
		out += c
	}
	return out
}

// ---------------------------------------------------------------------------
// SetWeekPlan
// ---------------------------------------------------------------------------

func (s *taskService) SetWeekPlan(ctx context.Context, actor events.Actor, id string, week *string, expected *Version) (Task, error) {
	if err := s.m.assertWritable(ctx); err != nil {
		return Task{}, err
	}
	t, err := s.m.loadGateTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if err := validateWeekPlan(week); err != nil {
		return Task{}, err
	}
	if expected != nil && *expected != t.Version() {
		return Task{}, ErrStaleRevision
	}
	var out Task
	err = s.m.handle.InTx(ctx, func(tx *sql.Tx) error {
		args := []any{nullString(week), s.m.now(), t.ID}
		where := `WHERE id = ? AND deleted_at IS NULL`
		if expected != nil {
			where += ` AND updated_at = ?`
		}
		var res sql.Result
		var uerr error
		if expected != nil {
			res, uerr = tx.ExecContext(ctx,
				`UPDATE tasks SET week_plan = ?, updated_at = ? `+where,
				args[0], args[1], t.ID, *expected)
		} else {
			res, uerr = tx.ExecContext(ctx,
				`UPDATE tasks SET week_plan = ?, updated_at = ? `+where, args...)
		}
		if uerr != nil {
			return fmt.Errorf("kanban: set week plan: %w", uerr)
		}
		affected, aerr := res.RowsAffected()
		if aerr != nil {
			return aerr
		}
		if affected == 0 {
			if expected != nil {
				return fmt.Errorf("%w: task %s", ErrStaleRevision, t.ID)
			}
			return fmt.Errorf("%w: task %s", ErrTaskNotFound, t.ID)
		}
		refreshed, _, rerr := s.m.taskByID(ctx, tx, t.ID)
		if rerr != nil {
			return rerr
		}
		out = refreshed
		return nil
	})
	if err != nil {
		return Task{}, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Archive / Unarchive
// ---------------------------------------------------------------------------

func (s *taskService) Archive(ctx context.Context, actor events.Actor, id string) (Task, error) {
	if err := validateTaskID(id); err != nil {
		return Task{}, err
	}
	if err := s.m.assertWritable(ctx); err != nil {
		return Task{}, err
	}
	t, ok, err := s.m.taskByID(ctx, s.m.db(), id)
	if err != nil {
		return Task{}, err
	}
	if !ok || t.DeletedAt != nil {
		return Task{}, fmt.Errorf("%w: task %s", ErrTaskNotFound, id)
	}
	if t.ArchivedAt != nil {
		return t, nil // idempotent, no event
	}
	// Archiving a subtask of an archived parent is a write on that subtree
	// and follows the same gate (§5.2 rule 8).
	if t.ParentTaskID != nil {
		p, pok, perr := s.m.taskByID(ctx, s.m.db(), *t.ParentTaskID)
		if perr != nil {
			return Task{}, perr
		}
		if pok && p.ArchivedAt != nil {
			return Task{}, fmt.Errorf("%w: parent task %s is archived", ErrTaskArchived, *t.ParentTaskID)
		}
	}
	return s.m.setArchived(ctx, actor, t, true)
}

func (s *taskService) Unarchive(ctx context.Context, actor events.Actor, id string) (Task, error) {
	if err := validateTaskID(id); err != nil {
		return Task{}, err
	}
	if err := s.m.assertWritable(ctx); err != nil {
		return Task{}, err
	}
	t, ok, err := s.m.taskByID(ctx, s.m.db(), id)
	if err != nil {
		return Task{}, err
	}
	if !ok || t.DeletedAt != nil {
		return Task{}, fmt.Errorf("%w: task %s", ErrTaskNotFound, id)
	}
	if t.ArchivedAt == nil {
		return t, nil // idempotent, no event
	}
	// Unarchive is one of the three writes an archived task permits
	// (§5.2 Archive); no archived-parent gate applies here.
	return s.m.setArchived(ctx, actor, t, false)
}

// setArchived writes archived_at without touching column, sort_order or
// completed_at (TASK-10: archiving is not completion, AC-20).
func (m *Module) setArchived(ctx context.Context, actor events.Actor, t Task, to bool) (Task, error) {
	a := kanbanActor(actor)
	var out Task
	err := m.handle.InTx(ctx, func(tx *sql.Tx) error {
		var res sql.Result
		var err error
		if to {
			res, err = tx.ExecContext(ctx,
				`UPDATE tasks SET archived_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
				m.now(), m.now(), t.ID)
		} else {
			res, err = tx.ExecContext(ctx,
				`UPDATE tasks SET archived_at = NULL, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
				m.now(), t.ID)
		}
		if err != nil {
			return fmt.Errorf("kanban: archive: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: task %s", ErrTaskNotFound, t.ID)
		}
		if eerr := m.emitArchived(ctx, tx, a, t, to); eerr != nil {
			return eerr
		}
		refreshed, _, rerr := m.taskByID(ctx, tx, t.ID)
		if rerr != nil {
			return rerr
		}
		out = refreshed
		return nil
	})
	if err != nil {
		return Task{}, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// SoftDelete / Restore (via foundation/trash; events belong to trash)
// ---------------------------------------------------------------------------

func (s *taskService) SoftDelete(ctx context.Context, actor events.Actor, id string) error {
	if err := validateTaskID(id); err != nil {
		return err
	}
	if err := s.m.assertWritable(ctx); err != nil {
		return err
	}
	t, ok, err := s.m.taskByID(ctx, s.m.db(), id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: task %s", ErrTaskNotFound, id)
	}
	if t.DeletedAt != nil {
		return nil // already in the trash: trash.SoftDelete would be a no-op
	}
	a := kanbanActor(actor)
	if s.m.opts.Trash == nil {
		return errors.New("kanban: trash service not configured")
	}
	return s.m.handle.InTx(ctx, func(tx *sql.Tx) error {
		// A parent cascades to every live subtask, each getting its own
		// entity.deleted through trash (§5.2 rule 6, AC-19).
		if t.ParentTaskID == nil {
			kids, kerr := s.m.childTaskIDs(ctx, tx, t.ID, false)
			if kerr != nil {
				return kerr
			}
			for _, kid := range kids {
				if derr := s.m.opts.Trash.SoftDelete(ctx, tx, trash.Ref{Type: schema.EntityTask, ID: kid}, a); derr != nil {
					return derr
				}
			}
		}
		return s.m.opts.Trash.SoftDelete(ctx, tx, trash.Ref{Type: schema.EntityTask, ID: t.ID}, a)
	})
}

func (s *taskService) Restore(ctx context.Context, actor events.Actor, id string) (Task, error) {
	if err := validateTaskID(id); err != nil {
		return Task{}, err
	}
	if err := s.m.assertWritable(ctx); err != nil {
		return Task{}, err
	}
	t, ok, err := s.m.taskByID(ctx, s.m.db(), id)
	if err != nil {
		return Task{}, err
	}
	if !ok {
		return Task{}, fmt.Errorf("%w: task %s", ErrTaskNotFound, id)
	}
	if t.DeletedAt == nil {
		return t, nil // already alive, trash.Restore emits nothing
	}
	if t.ParentTaskID != nil {
		p, pok, perr := s.m.taskByID(ctx, s.m.db(), *t.ParentTaskID)
		if perr != nil {
			return Task{}, perr
		}
		// Restoring a subtask whose parent is still in the trash (or gone)
		// is rejected (§5.2 rule 7).
		if !pok || p.DeletedAt != nil {
			return Task{}, fmt.Errorf("%w: parent %s is still deleted", ErrInvalidParent, *t.ParentTaskID)
		}
	}
	if s.m.opts.Trash == nil {
		return Task{}, errors.New("kanban: trash service not configured")
	}
	a := kanbanActor(actor)
	ref := trash.Ref{Type: schema.EntityTask, ID: t.ID}
	err = s.m.handle.InTx(ctx, func(tx *sql.Tx) error {
		if rerr := s.m.opts.Trash.Restore(ctx, tx, ref, a); rerr != nil {
			return rerr
		}
		if t.ParentTaskID == nil {
			kids, kerr := s.m.childTaskIDs(ctx, tx, t.ID, true)
			if kerr != nil {
				return kerr
			}
			for _, kid := range kids {
				if rerr := s.m.opts.Trash.Restore(ctx, tx, trash.Ref{Type: schema.EntityTask, ID: kid}, a); rerr != nil {
					return rerr
				}
			}
		}
		return nil
	})
	if err != nil {
		return Task{}, err
	}
	restored, _, err := s.m.taskByID(ctx, s.m.db(), id)
	if err != nil {
		return Task{}, err
	}
	return restored, nil
}
