package kanban

import (
	"context"
	"database/sql"
	"fmt"

	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
)

// checklistService implements ChecklistService (SPEC-08 §5.5, TASK-07).
// Checklist operations are editing-only: they never write Activity, never
// touch the parent task's updated_at (so a checkbox click cannot invalidate
// an in-flight task edit) and are physically deleted (no trash, O-7/N3).

type checklistService struct{ m *Module }

var _ ChecklistService = (*checklistService)(nil)

// loadChecklistTarget resolves the task an item operation may touch and
// applies the lifecycle gates: missing/deleted → ErrTaskNotFound;
// archived task or archived parent → ErrTaskArchived.
func (m *Module) loadChecklistTarget(ctx context.Context, taskID string) (Task, error) {
	return m.loadGateTask(ctx, taskID)
}

func (s *checklistService) Add(ctx context.Context, actor events.Actor, taskID, text string, beforeID *string) (ChecklistItem, error) {
	if err := s.m.assertWritable(ctx); err != nil {
		return ChecklistItem{}, err
	}
	if _, err := s.m.loadChecklistTarget(ctx, taskID); err != nil {
		return ChecklistItem{}, err
	}
	clean, err := validateChecklistText(text)
	if err != nil {
		return ChecklistItem{}, err
	}
	if beforeID != nil {
		if err := validateTaskID(*beforeID); err != nil {
			return ChecklistItem{}, err
		}
	}
	var out ChecklistItem
	err = s.m.handle.InTx(ctx, func(tx *sql.Tx) error {
		n, cerr := countChecklistItems(ctx, tx, taskID)
		if cerr != nil {
			return cerr
		}
		if n >= maxChecklistPerTask {
			return fmt.Errorf("%w: %d items on task %s", ErrInvalidInput, n, taskID)
		}
		domain, derr := s.m.checklistDomain(ctx, tx, taskID, "")
		if derr != nil {
			return derr
		}
		anchor := ""
		before := false
		if beforeID != nil {
			item, ok, ierr := s.m.checklistItem(ctx, tx, *beforeID)
			if ierr != nil {
				return ierr
			}
			if !ok || item.TaskID != taskID {
				return fmt.Errorf("%w: checklist anchor %s not on task %s", ErrInvalidInput, *beforeID, taskID)
			}
			anchor, before = *beforeID, true
		}
		plan, perr := planInsert(domain, "", anchor, before)
		if perr != nil {
			return perr
		}
		if plan.ordered != nil {
			for _, slot := range plan.ordered {
				if slot.id == "" {
					continue // the new item's slot, taken from plan.sort
				}
				if _, uerr := tx.ExecContext(ctx,
					`UPDATE task_checklist_items SET sort_order = ? WHERE id = ?`,
					slot.sort, slot.id); uerr != nil {
					return fmt.Errorf("kanban: checklist rebalance: %w", uerr)
				}
			}
		}
		at := s.m.now()
		item := ChecklistItem{
			ID:        ids.MustNew(s.m.clock),
			TaskID:    taskID,
			Text:      clean,
			SortOrder: plan.sort,
		}
		if _, ierr := tx.ExecContext(ctx,
			`INSERT INTO task_checklist_items(id, task_id, text, done, sort_order, created_at, updated_at)
			 VALUES (?, ?, ?, 0, ?, ?, ?)`,
			item.ID, item.TaskID, item.Text, item.SortOrder, at, at); ierr != nil {
			return fmt.Errorf("kanban: add checklist item: %w", ierr)
		}
		out = item
		return nil
	})
	if err != nil {
		return ChecklistItem{}, err
	}
	return out, nil
}

func countChecklistItems(ctx context.Context, tx *sql.Tx, taskID string) (int, error) {
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM task_checklist_items WHERE task_id = ?`, taskID).Scan(&n); err != nil {
		return 0, fmt.Errorf("kanban: checklist count: %w", err)
	}
	return n, nil
}

func (s *checklistService) Update(ctx context.Context, actor events.Actor, itemID, text string) (ChecklistItem, error) {
	if err := s.m.assertWritable(ctx); err != nil {
		return ChecklistItem{}, err
	}
	item, err := s.gateItem(ctx, itemID)
	if err != nil {
		return ChecklistItem{}, err
	}
	clean, err := validateChecklistText(text)
	if err != nil {
		return ChecklistItem{}, err
	}
	if clean == item.Text {
		return item, nil
	}
	err = s.m.handle.InTx(ctx, func(tx *sql.Tx) error {
		_, uerr := tx.ExecContext(ctx,
			`UPDATE task_checklist_items SET text = ?, updated_at = ? WHERE id = ?`,
			clean, s.m.now(), item.ID)
		return uerr
	})
	if err != nil {
		return ChecklistItem{}, err
	}
	item.Text = clean
	return item, nil
}

func (s *checklistService) SetDone(ctx context.Context, actor events.Actor, itemID string, done bool) (ChecklistItem, error) {
	if err := s.m.assertWritable(ctx); err != nil {
		return ChecklistItem{}, err
	}
	item, err := s.gateItem(ctx, itemID)
	if err != nil {
		return ChecklistItem{}, err
	}
	if item.Done == done {
		return item, nil
	}
	err = s.m.handle.InTx(ctx, func(tx *sql.Tx) error {
		_, uerr := tx.ExecContext(ctx,
			`UPDATE task_checklist_items SET done = ?, updated_at = ? WHERE id = ?`,
			boolInt(done), s.m.now(), item.ID)
		return uerr
	})
	if err != nil {
		return ChecklistItem{}, err
	}
	item.Done = done
	return item, nil
}

// Remove physically deletes the item (SPEC-08 §4.2 M-06, O-7).
func (s *checklistService) Remove(ctx context.Context, actor events.Actor, itemID string) error {
	if err := s.m.assertWritable(ctx); err != nil {
		return err
	}
	item, err := s.gateItem(ctx, itemID)
	if err != nil {
		return err
	}
	return s.m.handle.InTx(ctx, func(tx *sql.Tx) error {
		_, derr := tx.ExecContext(ctx, `DELETE FROM task_checklist_items WHERE id = ?`, item.ID)
		return derr
	})
}

// Reorder assigns sort_order by the given sequence; the id set must match
// the task's current items exactly (§5.5).
func (s *checklistService) Reorder(ctx context.Context, actor events.Actor, taskID string, orderedIDs []string) error {
	if err := s.m.assertWritable(ctx); err != nil {
		return err
	}
	if _, err := s.m.loadChecklistTarget(ctx, taskID); err != nil {
		return err
	}
	seen := make(map[string]bool, len(orderedIDs))
	for _, id := range orderedIDs {
		if !ids.Valid(id) {
			return fmt.Errorf("%w: item id %q", ErrInvalidInput, id)
		}
		if seen[id] {
			return fmt.Errorf("%w: duplicate item id %q", ErrInvalidInput, id)
		}
		seen[id] = true
	}
	return s.m.handle.InTx(ctx, func(tx *sql.Tx) error {
		current, err := s.m.listChecklist(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if len(current) != len(orderedIDs) {
			return fmt.Errorf("%w: reorder covers %d of %d items", ErrInvalidInput, len(orderedIDs), len(current))
		}
		for _, c := range current {
			if !seen[c.ID] {
				return fmt.Errorf("%w: item %s not on task %s", ErrInvalidInput, c.ID, taskID)
			}
		}
		at := s.m.now()
		for i, id := range orderedIDs {
			if _, err := tx.ExecContext(ctx,
				`UPDATE task_checklist_items SET sort_order = ?, updated_at = ? WHERE id = ?`,
				(i+1)*sortStep, at, id); err != nil {
				return fmt.Errorf("kanban: reorder checklist: %w", err)
			}
		}
		return nil
	})
}

// gateItem loads an item and gates its owning task.
func (s *checklistService) gateItem(ctx context.Context, itemID string) (ChecklistItem, error) {
	if !ids.Valid(itemID) {
		return ChecklistItem{}, fmt.Errorf("%w: checklist item id %q", ErrInvalidInput, itemID)
	}
	item, ok, err := s.m.checklistItem(ctx, s.m.db(), itemID)
	if err != nil {
		return ChecklistItem{}, err
	}
	if !ok {
		return ChecklistItem{}, fmt.Errorf("%w: checklist item %s", ErrTaskNotFound, itemID)
	}
	if _, err := s.m.loadChecklistTarget(ctx, item.TaskID); err != nil {
		return ChecklistItem{}, err
	}
	return item, nil
}
