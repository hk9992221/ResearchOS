// Package projectpapers stores the project ↔ global-Paper association in
// project.db.project_papers (SPEC-01 §5.13, SPEC-00 §4/§5 v1.1). It is the only
// place that links a project to a paper: entity_relations must never carry this
// relation.
package projectpapers

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
)

// Link is one project_papers row.
type Link struct {
	ProjectID string
	PaperID   string
	LinkedAt  time.Time
	LinkedBy  events.Actor // user | mcp
}

// Store is bound to one project database and knows its project id.
type Store struct {
	projectID string
	db        *sql.DB
	events    events.Writer
	clock     clock.Clock
}

// New binds a store to the project database db of projectID.
func New(projectID string, db *sql.DB, writer events.Writer, c clock.Clock) *Store {
	if c == nil {
		c = clock.System{}
	}
	return &Store{projectID: projectID, db: db, events: writer, clock: c}
}

// Link associates a paper with the project. A duplicate association is
// idempotent: created=false, no write, no event.
func (s *Store) Link(ctx context.Context, tx *sql.Tx, l Link) (bool, error) {
	return s.link(ctx, tx, l, true)
}

// LinkQuiet associates a paper with the project exactly like Link but never
// emits relation.added. SPEC-04's import pipeline uses it because that flow
// records paper.imported instead of a manual relation event; manual Link keeps
// its audit event.
func (s *Store) LinkQuiet(ctx context.Context, tx *sql.Tx, l Link) (bool, error) {
	return s.link(ctx, tx, l, false)
}

func (s *Store) link(ctx context.Context, tx *sql.Tx, l Link, emitEvent bool) (bool, error) {
	if tx == nil {
		return false, fmt.Errorf("projectpapers: Link requires a transaction")
	}
	if l.ProjectID != s.projectID {
		return false, fmt.Errorf("projectpapers: link project %q does not match the store project %q", l.ProjectID, s.projectID)
	}
	if !ids.Valid(l.ProjectID) || !ids.Valid(l.PaperID) {
		return false, fmt.Errorf("%w: project %q paper %q", errs.ErrBadEntityID, l.ProjectID, l.PaperID)
	}
	switch l.LinkedBy {
	case schema.ActorUser, schema.ActorMCP:
	default:
		return false, fmt.Errorf("%w: linked_by %q must be user or mcp", errs.ErrBadActor, l.LinkedBy)
	}
	at := l.LinkedAt
	if at.IsZero() {
		at = s.clock.Now()
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO project_papers(project_id, paper_id, linked_at, linked_by)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(project_id, paper_id) DO NOTHING`,
		l.ProjectID, l.PaperID, clock.Format(at), string(l.LinkedBy))
	if err != nil {
		return false, fmt.Errorf("projectpapers: link %s: %w", l.PaperID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("projectpapers: link %s: %w", l.PaperID, err)
	}
	if rows == 0 {
		return false, nil
	}
	if !emitEvent {
		return true, nil
	}
	obj := tuple(l.ProjectID, l.PaperID)
	if err := s.events.Emit(ctx, tx, events.Event{
		Type:       schema.EventRelationAdded,
		EntityType: schema.EntityPaper,
		EntityID:   l.PaperID,
		New:        &obj,
		Summary:    "关联文献",
		Actor:      l.LinkedBy,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// Unlink removes the association. A missing row is a no-op (removed=false).
func (s *Store) Unlink(ctx context.Context, tx *sql.Tx, projectID, paperID string, actor events.Actor) (bool, error) {
	if tx == nil {
		return false, fmt.Errorf("projectpapers: Unlink requires a transaction")
	}
	if projectID != s.projectID {
		return false, fmt.Errorf("projectpapers: unlink project %q does not match the store project %q", projectID, s.projectID)
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM project_papers WHERE project_id = ? AND paper_id = ?`, projectID, paperID)
	if err != nil {
		return false, fmt.Errorf("projectpapers: unlink %s: %w", paperID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("projectpapers: unlink %s: %w", paperID, err)
	}
	if rows == 0 {
		return false, nil
	}
	obj := tuple(projectID, paperID)
	if err := s.events.Emit(ctx, tx, events.Event{
		Type:       schema.EventRelationRemoved,
		EntityType: schema.EntityPaper,
		EntityID:   paperID,
		Old:        &obj,
		Summary:    "解除文献关联",
		Actor:      actor,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// ByProject returns the associations of one project.
func (s *Store) ByProject(ctx context.Context, projectID string) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT project_id, paper_id, linked_at, linked_by
		 FROM project_papers WHERE project_id = ? ORDER BY linked_at, paper_id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("projectpapers: by project: %w", err)
	}
	defer rows.Close()
	return scanLinks(rows)
}

// ByPaper returns the associations of one paper.
func (s *Store) ByPaper(ctx context.Context, paperID string) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT project_id, paper_id, linked_at, linked_by
		 FROM project_papers WHERE paper_id = ? ORDER BY linked_at, project_id`, paperID)
	if err != nil {
		return nil, fmt.Errorf("projectpapers: by paper: %w", err)
	}
	defer rows.Close()
	return scanLinks(rows)
}

// PaperIDs returns every referenced paper id (the reference-count candidate
// set of §5.9).
func (s *Store) PaperIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT paper_id FROM project_papers ORDER BY paper_id`)
	if err != nil {
		return nil, fmt.Errorf("projectpapers: paper ids: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("projectpapers: paper ids: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projectpapers: paper ids: %w", err)
	}
	return out, nil
}

// CountRefs returns how many associations this project has for paperID and
// whether the table was readable (healthy=false means the project_papers
// structure is broken, which must abort paper GC).
func (s *Store) CountRefs(ctx context.Context, paperID string) (int, bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM project_papers WHERE paper_id = ?`, paperID).Scan(&n); err != nil {
		return 0, false, fmt.Errorf("projectpapers: count refs: %w", err)
	}
	return n, true, nil
}

func scanLinks(rows *sql.Rows) ([]Link, error) {
	var out []Link
	for rows.Next() {
		var (
			link     Link
			linkedAt string
			linkedBy string
		)
		if err := rows.Scan(&link.ProjectID, &link.PaperID, &linkedAt, &linkedBy); err != nil {
			return nil, fmt.Errorf("projectpapers: scan: %w", err)
		}
		at, err := clock.Parse(linkedAt)
		if err != nil {
			return nil, fmt.Errorf("projectpapers: linked_at: %w", err)
		}
		link.LinkedAt = at
		link.LinkedBy = events.Actor(linkedBy)
		out = append(out, link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projectpapers: scan: %w", err)
	}
	return out, nil
}

// tuple is the 5-key relation payload shared with relation.* events (§7.1).
func tuple(projectID, paperID string) events.Object {
	return events.Object{
		"src_type": string(schema.EntityProject),
		"src_id":   projectID,
		"relation": string(schema.RelationRelatedTo),
		"dst_type": string(schema.EntityPaper),
		"dst_id":   paperID,
	}
}
