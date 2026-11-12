// Package relations reads and writes the generic entity_relations triples
// (SPEC-01 §5.7). It validates the direction registry, refuses self relations
// and soft-deleted endpoints, and emits relation.added/relation.removed in the
// caller's transaction with the 5-key tuple payload.
package relations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
)

// Link is one entity_relations triple.
type Link struct {
	SrcType  schema.EntityType
	SrcID    string
	Relation schema.Relation
	DstType  schema.EntityType
	DstID    string
}

// TargetState is the resolution result for a relation target.
type TargetState int

const (
	// TargetAlive means the target exists and is not soft-deleted.
	TargetAlive TargetState = iota
	// TargetSoftDeleted means the target is in the trash; the relation stays.
	TargetSoftDeleted
	// TargetMissing means the target is physically gone; a live relation row
	// pointing at it is dirty data.
	TargetMissing
)

// Service reads and writes relations of one project database, resolving global
// entities (paper, project) through the global database.
type Service struct {
	db     *sql.DB
	global *sql.DB
	events events.Writer
	clock  clock.Clock
}

// New binds the relation service to the project database, the global database
// and the project's event writer.
func New(projectDB, globalDB *sql.DB, writer events.Writer, c clock.Clock) *Service {
	if c == nil {
		c = clock.System{}
	}
	return &Service{db: projectDB, global: globalDB, events: writer, clock: c}
}

// entityTable locates an entity type and records whether it lives in global.db
// and whether it has a deleted_at column.
type entityTable struct {
	table         string
	global        bool
	softDeletable bool
}

var entityTables = map[schema.EntityType]entityTable{
	schema.EntityProject:          {table: "projects", global: true},
	schema.EntityPaper:            {table: "papers", global: true},
	schema.EntityNote:             {table: "notes", softDeletable: true},
	schema.EntityResearchDatabase: {table: "research_databases", softDeletable: true},
	schema.EntityDBField:          {table: "db_fields", softDeletable: true},
	schema.EntityDBRecord:         {table: "db_records", softDeletable: true},
	schema.EntityIdea:             {table: "ideas", softDeletable: true},
	schema.EntityEvidence:         {table: "evidence", softDeletable: true},
	schema.EntityExperiment:       {table: "experiments", softDeletable: true},
	schema.EntityRun:              {table: "runs", softDeletable: true},
	schema.EntitySnapshot:         {table: "snapshots"},
	schema.EntityTask:             {table: "tasks", softDeletable: true},
	schema.EntityWeeklyReport:     {table: "weekly_reports", softDeletable: true},
	schema.EntityEntityRelation:   {table: "entity_relations"},
}

// Link creates a relation. An existing triple is returned unchanged (same id,
// no write, no event). soft-deleted endpoints and the wrong direction yield
// errors before anything is written.
func (s *Service) Link(ctx context.Context, tx *sql.Tx, l Link, actor events.Actor) (string, error) {
	if tx == nil {
		return "", errors.New("relations: Link requires a transaction")
	}
	if !l.SrcType.Valid() || !l.DstType.Valid() {
		return "", fmt.Errorf("%w: source %q target %q", errs.ErrBadEntityType, l.SrcType, l.DstType)
	}
	if !l.Relation.Valid() {
		return "", fmt.Errorf("%w: %q", errs.ErrRelationNotAllowed, l.Relation)
	}
	if !ids.Valid(l.SrcID) || !ids.Valid(l.DstID) {
		return "", fmt.Errorf("%w: source %q target %q", errs.ErrBadEntityID, l.SrcID, l.DstID)
	}
	if l.SrcType == l.DstType && l.SrcID == l.DstID {
		return "", fmt.Errorf("%w: %s %s", errs.ErrSelfRelation, l.SrcType, l.SrcID)
	}
	if !schema.RelationAllowed(l.SrcType, l.Relation, l.DstType) {
		return "", fmt.Errorf("%w: %s --%s--> %s", errs.ErrRelationNotAllowed, l.SrcType, l.Relation, l.DstType)
	}
	if err := s.checkCrossDB(l); err != nil {
		return "", err
	}

	// Idempotency first: an existing triple wins even if endpoints have since
	// been soft-deleted.
	if id, ok, err := s.find(ctx, tx, l); err != nil {
		return "", err
	} else if ok {
		return id, nil
	}

	if err := s.requireAlive(ctx, tx, l.SrcType, l.SrcID, "source"); err != nil {
		return "", err
	}
	if err := s.requireAlive(ctx, tx, l.DstType, l.DstID, "target"); err != nil {
		return "", err
	}

	id, err := ids.New(s.clock)
	if err != nil {
		return "", fmt.Errorf("relations: generate id: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO entity_relations(id, src_type, src_id, relation, dst_type, dst_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(src_type, src_id, relation, dst_type, dst_id) DO NOTHING`,
		id, string(l.SrcType), l.SrcID, string(l.Relation), string(l.DstType), l.DstID,
		clock.Format(s.clock.Now()))
	if err != nil {
		return "", fmt.Errorf("relations: link: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("relations: link: %w", err)
	}
	if rows == 0 {
		existing, ok, err := s.find(ctx, tx, l)
		if err != nil {
			return "", err
		}
		if ok {
			return existing, nil
		}
		return "", fmt.Errorf("relations: link: conflict without an existing row")
	}
	obj := tuple(l)
	if err := s.events.Emit(ctx, tx, events.Event{
		Type:       schema.EventRelationAdded,
		EntityType: l.SrcType,
		EntityID:   l.SrcID,
		New:        &obj,
		Summary:    "建立关系",
		Actor:      actor,
	}); err != nil {
		return "", err
	}
	return id, nil
}

// Unlink deletes the triple. A missing triple is a no-op.
func (s *Service) Unlink(ctx context.Context, tx *sql.Tx, l Link, actor events.Actor) error {
	if tx == nil {
		return errors.New("relations: Unlink requires a transaction")
	}
	id, ok, err := s.find(ctx, tx, l)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM entity_relations WHERE id = ?`, id); err != nil {
		return fmt.Errorf("relations: unlink: %w", err)
	}
	obj := tuple(l)
	return s.events.Emit(ctx, tx, events.Event{
		Type:       schema.EventRelationRemoved,
		EntityType: l.SrcType,
		EntityID:   l.SrcID,
		Old:        &obj,
		Summary:    "解除关系",
		Actor:      actor,
	})
}

// From returns the relations with the given source. An empty rel means all.
func (s *Service) From(ctx context.Context, srcType schema.EntityType, srcID string, rel schema.Relation) ([]Link, error) {
	query := `SELECT src_type, src_id, relation, dst_type, dst_id
	          FROM entity_relations WHERE src_type = ? AND src_id = ?`
	args := []any{string(srcType), srcID}
	if rel != "" {
		query += ` AND relation = ?`
		args = append(args, string(rel))
	}
	query += ` ORDER BY created_at, id`
	return s.queryLinks(ctx, query, args...)
}

// To returns the relations with the given target. An empty rel means all.
func (s *Service) To(ctx context.Context, dstType schema.EntityType, dstID string, rel schema.Relation) ([]Link, error) {
	query := `SELECT src_type, src_id, relation, dst_type, dst_id
	          FROM entity_relations WHERE dst_type = ? AND dst_id = ?`
	args := []any{string(dstType), dstID}
	if rel != "" {
		query += ` AND relation = ?`
		args = append(args, string(rel))
	}
	query += ` ORDER BY created_at, id`
	return s.queryLinks(ctx, query, args...)
}

// Resolve reports whether the target exists, is soft-deleted, or is gone.
func (s *Service) Resolve(ctx context.Context, dstType schema.EntityType, dstID string) (TargetState, error) {
	target, ok := entityTables[dstType]
	if !ok {
		return TargetMissing, fmt.Errorf("%w: %q", errs.ErrBadEntityType, dstType)
	}
	db := s.db
	if target.global {
		db = s.global
	}
	if db == nil {
		return TargetMissing, fmt.Errorf("relations: no database for %s", dstType)
	}
	if target.softDeletable {
		var deleted sql.NullString
		err := db.QueryRowContext(ctx,
			`SELECT deleted_at FROM `+target.table+` WHERE id = ?`, dstID).Scan(&deleted)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return TargetMissing, nil
		case err != nil:
			return TargetMissing, fmt.Errorf("relations: resolve %s %s: %w", dstType, dstID, err)
		case deleted.Valid:
			return TargetSoftDeleted, nil
		default:
			return TargetAlive, nil
		}
	}
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM `+target.table+` WHERE id = ?`, dstID).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return TargetMissing, nil
	case err != nil:
		return TargetMissing, fmt.Errorf("relations: resolve %s %s: %w", dstType, dstID, err)
	default:
		return TargetAlive, nil
	}
}

func (s *Service) queryLinks(ctx context.Context, query string, args ...any) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("relations: query: %w", err)
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var (
			l       Link
			srcType string
			rel     string
			dstType string
		)
		if err := rows.Scan(&srcType, &l.SrcID, &rel, &dstType, &l.DstID); err != nil {
			return nil, fmt.Errorf("relations: query: %w", err)
		}
		l.SrcType = schema.EntityType(srcType)
		l.Relation = schema.Relation(rel)
		l.DstType = schema.EntityType(dstType)
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("relations: query: %w", err)
	}
	return out, nil
}

func (s *Service) find(ctx context.Context, tx *sql.Tx, l Link) (string, bool, error) {
	var id string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM entity_relations
		 WHERE src_type = ? AND src_id = ? AND relation = ? AND dst_type = ? AND dst_id = ?`,
		string(l.SrcType), l.SrcID, string(l.Relation), string(l.DstType), l.DstID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("relations: find: %w", err)
	}
	return id, true, nil
}

// requireAlive checks that the entity exists and is not soft-deleted. When tx
// is non-nil and the entity is project-local, the caller transaction is used.
func (s *Service) requireAlive(ctx context.Context, tx *sql.Tx, t schema.EntityType, id, role string) error {
	target, ok := entityTables[t]
	if !ok {
		return fmt.Errorf("%w: %q", errs.ErrBadEntityType, t)
	}
	query := func(q string, args ...any) *sql.Row {
		if tx != nil && !target.global {
			return tx.QueryRowContext(ctx, q, args...)
		}
		db := s.db
		if target.global {
			db = s.global
		}
		if db == nil {
			return nil
		}
		return db.QueryRowContext(ctx, q, args...)
	}
	if target.softDeletable {
		var deleted sql.NullString
		row := query(`SELECT deleted_at FROM `+target.table+` WHERE id = ?`, id)
		if row == nil {
			return fmt.Errorf("relations: no database for %s", t)
		}
		err := row.Scan(&deleted)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("relations: %s %s %s not found", role, t, id)
		}
		if err != nil {
			return fmt.Errorf("relations: %s %s: %w", role, t, err)
		}
		if deleted.Valid {
			return fmt.Errorf("%w: %s %s %s is soft-deleted", errs.ErrEntityDeleted, role, t, id)
		}
		return nil
	}
	var one int
	row := query(`SELECT 1 FROM `+target.table+` WHERE id = ?`, id)
	if row == nil {
		return fmt.Errorf("relations: no database for %s", t)
	}
	err := row.Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("relations: %s %s %s not found", role, t, id)
	}
	if err != nil {
		return fmt.Errorf("relations: %s %s: %w", role, t, err)
	}
	return nil
}

// checkCrossDB enforces that only references → paper may cross database
// boundaries (§5.7) and that global entities cannot be relation sources.
func (s *Service) checkCrossDB(l Link) error {
	if entityTables[l.SrcType].global {
		return fmt.Errorf("%w: source %s lives in global.db", errs.ErrRelationNotAllowed, l.SrcType)
	}
	if entityTables[l.DstType].global {
		if l.Relation != schema.RelationReferences || l.DstType != schema.EntityPaper {
			return fmt.Errorf("%w: only references -> paper may cross databases", errs.ErrRelationNotAllowed)
		}
	}
	return nil
}

// tuple is the 5-key payload object shared with the event writer.
func tuple(l Link) events.Object {
	return events.Object{
		"src_type": string(l.SrcType),
		"src_id":   l.SrcID,
		"relation": string(l.Relation),
		"dst_type": string(l.DstType),
		"dst_id":   l.DstID,
	}
}
