package kanban

import (
	"context"
	"database/sql"
	"fmt"

	"researchos/internal/foundation/events"
)

// moveService carries the shared drag/move engine (SPEC-08 §5.2 rules 3/4,
// TASK-09). Move, Update's column delegation and Complete all run through
// applyMove inside the caller transaction.

type moveTarget struct {
	column   string
	beforeID *string
	afterID  *string
}

// anchor resolves the single optional anchor. Both set is invalid input
// (SPEC-11 task_move parity, TASK-09).
func (mt moveTarget) anchor() (string, bool, error) {
	if mt.beforeID != nil && mt.afterID != nil {
		return "", false, fmt.Errorf("%w: before and after anchors are exclusive", ErrInvalidInput)
	}
	if mt.beforeID != nil {
		return *mt.beforeID, true, nil
	}
	if mt.afterID != nil {
		return *mt.afterID, false, nil
	}
	return "", false, nil
}

// validateAnchor checks the anchor belongs to the target column and shares
// the mover's parent (SPEC-08 §5.2 rule 3, AC-14).
func (m *Module) validateAnchor(ctx context.Context, tx *sql.Tx, mover Task, mt moveTarget, anchorID string) error {
	a, ok, err := m.taskByID(ctx, tx, anchorID)
	if err != nil {
		return err
	}
	if !ok || a.DeletedAt != nil || a.Column != mt.column || !sameParent(a.ParentTaskID, mover.ParentTaskID) {
		return fmt.Errorf("%w: anchor %s is not in column %q of the same parent domain", ErrInvalidInput, anchorID, mt.column)
	}
	return nil
}

// applyMove relocates a loaded, already-gated task. It computes placement,
// rewrites the domain when the gap is exhausted, maintains completed_at on
// column transitions and emits the single status.changed. Same-column
// reorder never emits (rule 4, AC-12/AC-13).
func (m *Module) applyMove(ctx context.Context, tx *sql.Tx, actor events.Actor, t Task, mt moveTarget) (Task, error) {
	toCol, ok, err := m.columnByName(ctx, tx, mt.column)
	if err != nil {
		return Task{}, err
	}
	if !ok {
		return Task{}, fmt.Errorf("%w: column %q", ErrColumnNotFound, mt.column)
	}
	anchorID, before, err := mt.anchor()
	if err != nil {
		return Task{}, err
	}
	if anchorID != "" {
		if anchorID == t.ID {
			return Task{}, fmt.Errorf("%w: anchor is the moved task itself", ErrInvalidInput)
		}
		if err := m.validateAnchor(ctx, tx, t, mt, anchorID); err != nil {
			return Task{}, err
		}
	}

	domain, err := m.taskDomain(ctx, tx, mt.column, t.ParentTaskID, t.ID)
	if err != nil {
		return Task{}, err
	}
	plan, err := planInsert(domain, t.ID, anchorID, before)
	if err != nil {
		return Task{}, err
	}
	if plan.ordered != nil {
		// Gap exhausted: rewrite every listed member with the step and keep
		// the cards monotonic (AC-12). Only sort_order changes, so no other
		// task's concurrency token moves.
		for _, slot := range plan.ordered {
			if slot.id == t.ID {
				continue
			}
			if _, uerr := tx.ExecContext(ctx,
				`UPDATE tasks SET sort_order = ? WHERE id = ?`, slot.sort, slot.id); uerr != nil {
				return Task{}, fmt.Errorf("kanban: rebalance: %w", uerr)
			}
		}
	}

	columnChanged := toCol.Name != t.Column
	var fromCol Column
	if columnChanged {
		if found, fok, cerr := m.columnByName(ctx, tx, t.Column); cerr != nil {
			return Task{}, cerr
		} else if fok {
			fromCol = found
		} else {
			fromCol = Column{Name: t.Column}
		}
	}

	var set []string
	var args []any
	set = append(set, `sort_order = ?`, `updated_at = ?`)
	args = append(args, plan.sort, m.now())
	if columnChanged {
		set = append(set, `"column" = ?`)
		args = append(args, toCol.Name)
	}
	// completed_at maintenance (§5.2 rule 4, §4.3): the first entry into any
	// terminal column stamps now; leaving every terminal column clears;
	// terminal-to-terminal keeps the original instant.
	nowDone := toCol.IsDone
	oldDone := fromCol.IsDone
	if columnChanged {
		switch {
		case !oldDone && nowDone && t.CompletedAt == nil:
			at := m.now()
			set = append(set, `completed_at = ?`)
			args = append(args, at)
		case oldDone && !nowDone:
			set = append(set, `completed_at = NULL`)
		}
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE tasks SET `+joinSet(set)+` WHERE id = ?`,
		append(args, t.ID)...)
	if err != nil {
		return Task{}, fmt.Errorf("kanban: move: %w", err)
	}

	if columnChanged {
		moved, _, gerr := m.taskByID(ctx, tx, t.ID)
		if gerr != nil {
			return Task{}, gerr
		}
		var completedPayload *string
		if !oldDone && nowDone {
			if moved.CompletedAt != nil {
				v := optTimeText(moved.CompletedAt)
				if str, okk := v.(string); okk {
					completedPayload = &str
				}
			}
		}
		if eerr := m.emitColumnMoved(ctx, tx, actor, moved, fromCol, toCol, completedPayload); eerr != nil {
			return Task{}, eerr
		}
		t = moved
	}
	refreshed, _, rerr := m.taskByID(ctx, tx, t.ID)
	if rerr != nil {
		return Task{}, rerr
	}
	return refreshed, nil
}

// Move is the TaskService.Move entry: project gate, task gates, then the
// engine inside one seeded transaction (§5.2 Move).
func (s *taskService) Move(ctx context.Context, actor events.Actor, in MoveInput) (Task, error) {
	if err := s.m.assertWritable(ctx); err != nil {
		return Task{}, err
	}
	t, err := s.m.loadGateTask(ctx, in.ID)
	if err != nil {
		return Task{}, err
	}
	if in.Column == "" {
		return Task{}, fmt.Errorf("%w: target column required", ErrInvalidInput)
	}
	a := kanbanActor(actor)
	var out Task
	err = s.m.withSeededTx(ctx, func(tx *sql.Tx) error {
		moved, merr := s.m.applyMove(ctx, tx, a, t, moveTarget{
			column:   in.Column,
			beforeID: in.BeforeID,
			afterID:  in.AfterID,
		})
		if merr != nil {
			return merr
		}
		out = moved
		return nil
	})
	if err != nil {
		return Task{}, err
	}
	return out, nil
}

// Complete appends the task to the end of the leftmost terminal column;
// already-terminal tasks return unchanged without an event (§5.2 Complete,
// AC-27).
func (s *taskService) Complete(ctx context.Context, actor events.Actor, id string) (Task, error) {
	if err := s.m.assertWritable(ctx); err != nil {
		return Task{}, err
	}
	t, err := s.m.loadGateTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	a := kanbanActor(actor)
	var out Task
	err = s.m.withSeededTx(ctx, func(tx *sql.Tx) error {
		doneName, found, derr := s.m.leftmostDoneColumn(ctx, tx)
		if derr != nil {
			return derr
		}
		if !found {
			return ErrColumnNotFound
		}
		if done, ierr := s.m.isTerminalColumn(ctx, tx, t.Column); ierr != nil {
			return ierr
		} else if done {
			out = t // idempotent, no event
			return nil
		}
		moved, merr := s.m.applyMove(ctx, tx, a, t, moveTarget{column: doneName})
		if merr != nil {
			return merr
		}
		out = moved
		return nil
	})
	if err != nil {
		return Task{}, err
	}
	return out, nil
}
