package kanban

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"researchos/internal/foundation/events"
)

// labelService implements LabelService (SPEC-08 §5.3, TASK-08). Labels live
// inside tasks.labels JSON arrays only; there is no global dictionary,
// comparison is case-sensitive and Add/Replace never emit events.

type labelService struct{ m *Module }

var _ LabelService = (*labelService)(nil)

// List aggregates label counts over live, unarchived tasks and orders the
// result by count DESC, then label ASC for a stable UI suggestion (§5.3).
func (s *labelService) List(ctx context.Context) ([]LabelCount, error) {
	raws, err := s.m.liveLabelRows(ctx)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, raw := range raws {
		for _, l := range decodeLabels(raw) {
			counts[l]++
		}
	}
	out := make([]LabelCount, 0, len(counts))
	for label, n := range counts {
		out = append(out, LabelCount{Label: label, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Label < out[j].Label
	})
	return out, nil
}

// Add appends one label; an existing label is a no-op that still advances
// nothing (idempotent, AC-18). The whole-group validation (dedup, caps) is
// shared with Update.
func (s *labelService) Add(ctx context.Context, actor events.Actor, taskID, label string, expected *Version) (Task, error) {
	clean, err := validateLabel(label)
	if err != nil {
		return Task{}, err
	}
	t, err := s.m.loadGateTask(ctx, taskID)
	if err != nil {
		return Task{}, err
	}
	for _, l := range t.Labels {
		if l == clean {
			return t, nil // present already: no write, no event
		}
	}
	labels := append(append([]string{}, t.Labels...), clean)
	return s.m.replaceLabels(ctx, actor, t, labels, expected)
}

// Remove drops one label; a missing label is a no-op.
func (s *labelService) Remove(ctx context.Context, actor events.Actor, taskID, label string, expected *Version) (Task, error) {
	clean, err := validateLabel(label)
	if err != nil {
		return Task{}, err
	}
	t, err := s.m.loadGateTask(ctx, taskID)
	if err != nil {
		return Task{}, err
	}
	var kept []string
	found := false
	for _, l := range t.Labels {
		if l == clean {
			found = true
			continue
		}
		kept = append(kept, l)
	}
	if !found {
		return t, nil
	}
	return s.m.replaceLabels(ctx, actor, t, kept, expected)
}

// replaceLabels performs the token-guarded whole-group write shared by
// Update/LabelService. Label edits never emit (TASK-08, §7.1).
func (m *Module) replaceLabels(ctx context.Context, actor events.Actor, t Task, labels []string, expected *Version) (Task, error) {
	if err := m.assertWritable(ctx); err != nil {
		return Task{}, err
	}
	clean, err := sanitizeLabels(labels)
	if err != nil {
		return Task{}, err
	}
	if expected != nil && *expected != t.Version() {
		return Task{}, ErrStaleRevision
	}
	encoded, err := encodeLabels(clean)
	if err != nil {
		return Task{}, err
	}
	var out Task
	err = m.handle.InTx(ctx, func(tx *sql.Tx) error {
		var res sql.Result
		if expected != nil {
			res, err = tx.ExecContext(ctx,
				`UPDATE tasks SET labels = ?, updated_at = ?
				 WHERE id = ? AND deleted_at IS NULL AND updated_at = ?`,
				encoded, m.now(), t.ID, *expected)
		} else {
			res, err = tx.ExecContext(ctx,
				`UPDATE tasks SET labels = ?, updated_at = ?
				 WHERE id = ? AND deleted_at IS NULL`,
				encoded, m.now(), t.ID)
		}
		if err != nil {
			return fmt.Errorf("kanban: replace labels: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if expected != nil {
				return fmt.Errorf("%w: task %s", ErrStaleRevision, t.ID)
			}
			return fmt.Errorf("%w: task %s", ErrTaskNotFound, t.ID)
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
