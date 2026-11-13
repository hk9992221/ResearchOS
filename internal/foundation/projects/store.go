// Package projects is the minimal store over global.db.projects (SPEC-01
// §5.13). It never writes Activity events: entity.updated/status.changed belong
// to SPEC-03, which composes these calls inside its own transaction.
package projects

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
)

// Project is one row of global.db.projects.
type Project struct {
	ID         string
	Name       string
	Summary    string
	Goals      string
	FocusMD    string
	Status     schema.ProjectStatus
	ArchivedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ProjectPatch updates a project. A nil field means "leave unchanged".
type ProjectPatch struct {
	Name    *string
	Summary *string
	Goals   *string
	FocusMD *string
}

// Store reads and writes global.db.projects.
type Store struct {
	db    *sql.DB
	clock clock.Clock
}

// New binds a store to the global database. A nil clock means clock.System.
func New(db *sql.DB, c clock.Clock) *Store {
	if c == nil {
		c = clock.System{}
	}
	return &Store{db: db, clock: c}
}

// Create inserts a project with status 'active' and returns its new UUIDv7.
func (s *Store) Create(ctx context.Context, tx *sql.Tx, name, summary, goals string) (string, error) {
	id, err := ids.New(s.clock)
	if err != nil {
		return "", fmt.Errorf("projects: generate id: %w", err)
	}
	if err := s.CreateWithID(ctx, tx, id, name, summary, goals); err != nil {
		return "", err
	}
	return id, nil
}

// CreateWithID inserts a project with a caller-supplied id. SPEC-03 generates
// the UUIDv7 itself so that the data directory name equals the project id
// (§5.4); the store only validates the shape.
func (s *Store) CreateWithID(ctx context.Context, tx *sql.Tx, id, name, summary, goals string) error {
	if !ids.Valid(id) {
		return fmt.Errorf("projects: create: invalid project id %q", id)
	}
	now := clock.Format(s.clock.Now())
	_, err := s.exec(ctx, tx,
		`INSERT INTO projects(id, name, summary, goals, focus_md, status, archived_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, '', 'active', NULL, ?, ?)`,
		id, name, summary, goals, now, now)
	if err != nil {
		return fmt.Errorf("projects: create: %w", err)
	}
	return nil
}

// Get returns one project. A missing row surfaces sql.ErrNoRows.
func (s *Store) Get(ctx context.Context, id string) (Project, error) {
	row := s.db.QueryRowContext(ctx, selectColumns+` FROM projects WHERE id = ?`, id)
	p, err := scanProject(row)
	if err != nil {
		return Project{}, fmt.Errorf("projects: get %s: %w", id, err)
	}
	return p, nil
}

// List returns projects ordered by created_at then id. A non-nil status filters
// the result.
func (s *Store) List(ctx context.Context, status *schema.ProjectStatus) ([]Project, error) {
	query := selectColumns + ` FROM projects`
	var args []any
	if status != nil {
		query += ` WHERE status = ?`
		args = append(args, string(*status))
	}
	query += ` ORDER BY created_at, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("projects: list: %w", err)
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("projects: list: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projects: list: %w", err)
	}
	return out, nil
}

// Update writes the non-nil patch fields and refreshes updated_at. An empty
// patch is a no-op. A missing row surfaces sql.ErrNoRows.
func (s *Store) Update(ctx context.Context, tx *sql.Tx, id string, p ProjectPatch) error {
	var sets []string
	var args []any
	if p.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *p.Name)
	}
	if p.Summary != nil {
		sets = append(sets, "summary = ?")
		args = append(args, *p.Summary)
	}
	if p.Goals != nil {
		sets = append(sets, "goals = ?")
		args = append(args, *p.Goals)
	}
	if p.FocusMD != nil {
		sets = append(sets, "focus_md = ?")
		args = append(args, *p.FocusMD)
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, clock.Format(s.clock.Now()), id)
	res, err := s.exec(ctx, tx, `UPDATE projects SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("projects: update %s: %w", id, err)
	}
	return requireOneRow(res, "update", id)
}

// SetStatus writes status and archived_at: archived sets the timestamp, active
// clears it. The actor is accepted for signature compatibility with SPEC-03,
// which emits status.changed; this store never writes events.
func (s *Store) SetStatus(ctx context.Context, tx *sql.Tx, id string, st schema.ProjectStatus, actor events.Actor) error {
	if !st.Valid() {
		return fmt.Errorf("projects: set status %s: invalid status %q", id, st)
	}
	now := clock.Format(s.clock.Now())
	var archivedAt any
	if st == schema.ProjectArchived {
		archivedAt = now
	}
	res, err := s.exec(ctx, tx,
		`UPDATE projects SET status = ?, archived_at = ?, updated_at = ? WHERE id = ?`,
		string(st), archivedAt, now, id)
	if err != nil {
		return fmt.Errorf("projects: set status %s: %w", id, err)
	}
	return requireOneRow(res, "set status", id)
}

// Delete removes the registration row. It is only used by the project deletion
// flow (§5.9).
func (s *Store) Delete(ctx context.Context, tx *sql.Tx, id string) error {
	res, err := s.exec(ctx, tx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("projects: delete %s: %w", id, err)
	}
	return requireOneRow(res, "delete", id)
}

const selectColumns = `SELECT id, name, summary, goals, focus_md, status, archived_at, created_at, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanProject(row rowScanner) (Project, error) {
	var (
		p          Project
		status     string
		archivedAt sql.NullString
		createdAt  string
		updatedAt  string
	)
	if err := row.Scan(&p.ID, &p.Name, &p.Summary, &p.Goals, &p.FocusMD, &status,
		&archivedAt, &createdAt, &updatedAt); err != nil {
		return Project{}, err
	}
	p.Status = schema.ProjectStatus(status)
	var err error
	if p.CreatedAt, err = clock.Parse(createdAt); err != nil {
		return Project{}, fmt.Errorf("created_at: %w", err)
	}
	if p.UpdatedAt, err = clock.Parse(updatedAt); err != nil {
		return Project{}, fmt.Errorf("updated_at: %w", err)
	}
	if archivedAt.Valid {
		t, err := clock.Parse(archivedAt.String)
		if err != nil {
			return Project{}, fmt.Errorf("archived_at: %w", err)
		}
		p.ArchivedAt = &t
	}
	return p, nil
}

func (s *Store) exec(ctx context.Context, tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	if tx != nil {
		return tx.ExecContext(ctx, query, args...)
	}
	return s.db.ExecContext(ctx, query, args...)
}

func requireOneRow(res sql.Result, op, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("projects: %s %s: %w", op, id, err)
	}
	if n == 0 {
		return fmt.Errorf("projects: %s %s: %w", op, id, sql.ErrNoRows)
	}
	return nil
}
