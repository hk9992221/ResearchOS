package kanban

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"researchos/internal/foundation/events"
)

// columnService implements ColumnService (SPEC-08 §5.4, TASK-03). Column
// management writes no Activity (the registry has no column event type);
// every write still enforces the archived-project gate (§5.4).

type columnService struct{ m *Module }

var _ ColumnService = (*columnService)(nil)

func (s *columnService) List(ctx context.Context) ([]Column, error) {
	var out []Column
	err := s.m.withSeededTx(ctx, func(tx *sql.Tx) error {
		cols, err := s.m.listColumns(ctx, tx)
		if err != nil {
			return err
		}
		out = cols
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *columnService) Create(ctx context.Context, actor events.Actor, in CreateColumnInput) (Column, error) {
	if err := s.m.assertWritable(ctx); err != nil {
		return Column{}, err
	}
	name, err := validateColumnName(in.Name)
	if err != nil {
		return Column{}, err
	}
	kind, err := normalizeColumnKind(in.Kind)
	if err != nil {
		return Column{}, err
	}
	var out Column
	err = s.m.withSeededTx(ctx, func(tx *sql.Tx) error {
		if _, ok, err := s.m.columnByName(ctx, tx, name); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("%w: %q", ErrDuplicateColumnName, name)
		}
		order, err := s.m.listColumns(ctx, tx)
		if err != nil {
			return err
		}
		// Compute the display order with the newcomer inserted, then
		// renumber every position with the 1024 step: positions stay
		// dense and never need a gap split (§5.4 Create).
		insertAt := len(order)
		if in.After != nil {
			afterName := *in.After
			found := false
			for i, c := range order {
				if c.Name == afterName {
					insertAt = i + 1
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%w: column %q", ErrColumnNotFound, afterName)
			}
		}
		ordered := make([]string, 0, len(order)+1)
		ordered = append(ordered, namesOf(order[:insertAt])...)
		ordered = append(ordered, name)
		ordered = append(ordered, namesOf(order[insertAt:])...)
		at := s.m.now()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_columns(name, kind, is_done, position, created_at, updated_at)
			 VALUES (?, ?, ?, 0, ?, ?)`, name, kind, boolInt(in.IsDone), at, at); err != nil {
			return fmt.Errorf("kanban: create column: %w", err)
		}
		if err := s.m.renumberColumns(ctx, tx, ordered); err != nil {
			return err
		}
		col, ok, err := s.m.columnByName(ctx, tx, name)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: column %q vanished", ErrColumnNotFound, name)
		}
		out = col
		return nil
	})
	if err != nil {
		return Column{}, err
	}
	return out, nil
}

func namesOf(cols []Column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}

// renumberColumns writes position = (index+1)*1024 in one pass.
func (m *Module) renumberColumns(ctx context.Context, tx *sql.Tx, ordered []string) error {
	at := m.now()
	for i, name := range ordered {
		if _, err := tx.ExecContext(ctx,
			`UPDATE task_columns SET position = ?, updated_at = ? WHERE name = ?`,
			(i+1)*sortStep, at, name); err != nil {
			return fmt.Errorf("kanban: renumber columns: %w", err)
		}
	}
	return nil
}

func (s *columnService) Rename(ctx context.Context, actor events.Actor, oldName, newName string) (Column, error) {
	if err := s.m.assertWritable(ctx); err != nil {
		return Column{}, err
	}
	oldName = strings.TrimSpace(oldName)
	newName, err := validateColumnName(newName)
	if err != nil {
		return Column{}, err
	}
	if oldName == newName {
		return Column{}, fmt.Errorf("%w: new name equals old name", ErrInvalidInput)
	}
	var out Column
	err = s.m.handle.InTx(ctx, func(tx *sql.Tx) error {
		if _, ok, err := s.m.columnByName(ctx, tx, oldName); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("%w: column %q", ErrColumnNotFound, oldName)
		}
		// Uniqueness first: the trim-exact comparison matches the PK (§5.4).
		if _, ok, err := s.m.columnByName(ctx, tx, newName); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("%w: %q", ErrDuplicateColumnName, newName)
		}
		// Cascade the cards before the definition row so the intermediate
		// state never leaves tasks pointing at a missing column (§5.4).
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET "column" = ? WHERE "column" = ?`, newName, oldName); err != nil {
			return fmt.Errorf("kanban: rename cascade: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE task_columns SET name = ?, updated_at = ? WHERE name = ?`,
			newName, s.m.now(), oldName); err != nil {
			return fmt.Errorf("kanban: rename column: %w", err)
		}
		col, _, err := s.m.columnByName(ctx, tx, newName)
		if err != nil {
			return err
		}
		out = col
		return nil
	})
	if err != nil {
		return Column{}, err
	}
	return out, nil
}

// Move places the column at the 0-based index toPosition (clamped) and
// renumbers; task rows are untouched (§5.4 Move, AC-08).
func (s *columnService) Move(ctx context.Context, actor events.Actor, name string, toPosition int) ([]Column, error) {
	if err := s.m.assertWritable(ctx); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	var out []Column
	err := s.m.withSeededTx(ctx, func(tx *sql.Tx) error {
		order, err := s.m.listColumns(ctx, tx)
		if err != nil {
			return err
		}
		idx := -1
		for i, c := range order {
			if c.Name == name {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("%w: column %q", ErrColumnNotFound, name)
		}
		names := namesOf(order)
		names = append(names[:idx], names[idx+1:]...)
		if toPosition < 0 {
			toPosition = 0
		}
		if toPosition > len(names) {
			toPosition = len(names)
		}
		names = append(names[:toPosition:toPosition], append([]string{name}, names[toPosition:]...)...)
		if err := s.m.renumberColumns(ctx, tx, names); err != nil {
			return err
		}
		out, err = s.m.listColumns(ctx, tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetDone marks/unmarks a terminal column. Unmarking the last terminal
// column is rejected; already-set completed_at values survive as historical
// facts (§5.4 SetDone, AC-10). Marking a column terminal does not stamp the
// tasks already sitting in it: completion is a transition event (§4.3).
func (s *columnService) SetDone(ctx context.Context, actor events.Actor, name string, done bool) (Column, error) {
	if err := s.m.assertWritable(ctx); err != nil {
		return Column{}, err
	}
	name = strings.TrimSpace(name)
	var out Column
	err := s.m.withSeededTx(ctx, func(tx *sql.Tx) error {
		col, ok, err := s.m.columnByName(ctx, tx, name)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: column %q", ErrColumnNotFound, name)
		}
		if col.IsDone == done {
			out = col
			return nil // idempotent
		}
		if !done {
			var n int
			if err := tx.QueryRowContext(ctx,
				`SELECT count(*) FROM task_columns WHERE is_done = 1 AND name <> ?`, name).Scan(&n); err != nil {
				return fmt.Errorf("kanban: done count: %w", err)
			}
			if n == 0 {
				return ErrLastDoneColumn
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE task_columns SET is_done = ?, updated_at = ? WHERE name = ?`,
			boolInt(done), s.m.now(), name); err != nil {
			return fmt.Errorf("kanban: set done: %w", err)
		}
		col.IsDone = done
		out = col
		return nil
	})
	if err != nil {
		return Column{}, err
	}
	return out, nil
}

// Delete migrates the column's undeleted tasks to migrateTo (in original
// order, appended to the target domains, completed_at maintained) and then
// removes the definition. No per-task events are emitted for the structural
// migration (§5.4 Delete, §7.2, AC-09/AC-10).
func (s *columnService) Delete(ctx context.Context, actor events.Actor, name, migrateTo string) error {
	if err := s.m.assertWritable(ctx); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	migrateTo = strings.TrimSpace(migrateTo)
	return s.m.withSeededTx(ctx, func(tx *sql.Tx) error {
		col, ok, err := s.m.columnByName(ctx, tx, name)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: column %q", ErrColumnNotFound, name)
		}
		var total int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM task_columns`).Scan(&total); err != nil {
			return fmt.Errorf("kanban: column count: %w", err)
		}
		if total <= 1 {
			return ErrLastColumn
		}
		// §4.3: deleting a column must never leave the project without a
		// terminal column.
		if col.IsDone {
			var others int
			if err := tx.QueryRowContext(ctx,
				`SELECT count(*) FROM task_columns WHERE is_done = 1 AND name <> ?`, name).Scan(&others); err != nil {
				return fmt.Errorf("kanban: done count: %w", err)
			}
			if others == 0 {
				return ErrLastDoneColumn
			}
		}
		movers, err := s.columnMovers(ctx, tx, name)
		if err != nil {
			return err
		}
		if len(movers) > 0 && migrateTo == "" {
			return fmt.Errorf("%w: %d tasks in %q", ErrColumnNotEmpty, len(movers), name)
		}
		if migrateTo != "" {
			if migrateTo == name {
				return fmt.Errorf("%w: migration target is the deleted column", ErrInvalidInput)
			}
			if _, ok, err := s.m.columnByName(ctx, tx, migrateTo); err != nil {
				return err
			} else if !ok {
				return fmt.Errorf("%w: column %q", ErrColumnNotFound, migrateTo)
			}
		}
		if len(movers) > 0 {
			return s.migrateColumnTasks(ctx, tx, col, migrateTo, movers)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_columns WHERE name = ?`, name); err != nil {
			return fmt.Errorf("kanban: delete column: %w", err)
		}
		return nil
	})
}

// columnMovers lists the undeleted tasks sitting on a column in board order.
// Archived tasks move too: they keep referencing the column definition.
func (s *columnService) columnMovers(ctx context.Context, tx *sql.Tx, name string) ([]Task, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+taskColumns+` FROM tasks
		 WHERE "column" = ? AND deleted_at IS NULL ORDER BY sort_order, id`, name)
	if err != nil {
		return nil, fmt.Errorf("kanban: column movers: %w", err)
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, ok, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// migrateColumnTasks appends the movers to the target column per parent
// domain, preserving the original order, and drops the definition. The
// structural migration never emits events and never moves updated_at
// (§5.4, §7.2 final note).
func (s *columnService) migrateColumnTasks(ctx context.Context, tx *sql.Tx, from Column, to string, movers []Task) error {
	toCol, _, err := s.m.columnByName(ctx, tx, to)
	if err != nil {
		return err
	}
	// Per-parent-domain cursors continue each target domain after the
	// previous moved task of the same domain.
	next := make(map[string]int)
	for _, t := range movers {
		key := ""
		if t.ParentTaskID != nil {
			key = *t.ParentTaskID
		}
		sort, ok := next[key]
		if !ok {
			base, nerr := s.m.nextTaskSort(ctx, tx, to, t.ParentTaskID)
			if nerr != nil {
				return nerr
			}
			sort = base
		}
		next[key] = sort + sortStep

		completed := t.CompletedAt
		if !from.IsDone && toCol.IsDone && completed == nil {
			at := s.m.clock.Now().UTC()
			completed = &at
		}
		if from.IsDone && !toCol.IsDone {
			completed = nil
		}
		if _, uerr := tx.ExecContext(ctx,
			`UPDATE tasks SET "column" = ?, sort_order = ?, completed_at = ? WHERE id = ?`,
			to, sort, optTimeText(completed), t.ID); uerr != nil {
			return fmt.Errorf("kanban: migrate task %s: %w", t.ID, uerr)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_columns WHERE name = ?`, from.Name); err != nil {
		return fmt.Errorf("kanban: delete column: %w", err)
	}
	return nil
}
