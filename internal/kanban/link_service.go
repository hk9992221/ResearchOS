package kanban

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
)

// linkService implements LinkService (SPEC-08 §5.6, TASK-11). Every write
// delegates to foundation/relations (validation, idempotency and the
// relation.added/removed events all belong to the foundation layer); this
// module only maps task semantics onto it.

type linkService struct{ m *Module }

var _ LinkService = (*linkService)(nil)

// allowedTaskRelations is the TASK-11 whitelist; the direction table in
// SPEC-01 §4.6 still has the final word (task → paper stays rejected, O-13).
var allowedTaskRelations = map[schema.Relation]bool{
	schema.RelationRelatedTo:     true,
	schema.RelationDeliverableOf: true,
}

// Add links the task to the target. Triple re-linking is idempotent and
// writes nothing (AC-25). Rejected: illegal direction combinations
// (ErrRelationNotAllowed, AC-26), targets outside this project
// (ErrInvalidInput — a foreign UUID simply does not resolve here), and
// soft-deleted targets (errs.ErrEntityDeleted from the foundation).
func (s *linkService) Add(ctx context.Context, taskID string, rel schema.Relation, dstType schema.EntityType, dstID string, actor events.Actor) (string, error) {
	t, err := s.m.loadGateTask(ctx, taskID)
	if err != nil {
		return "", err
	}
	if err := s.m.assertWritable(ctx); err != nil {
		return "", err
	}
	if !rel.Valid() || !allowedTaskRelations[rel] {
		return "", fmt.Errorf("%w: task --%s--> %s", errs.ErrRelationNotAllowed, rel, dstType)
	}
	if !dstType.Valid() {
		return "", fmt.Errorf("%w: %q", errs.ErrRelationNotAllowed, dstType)
	}
	// Direction table first (SPEC-01 §4.6): task → paper never reaches the
	// existence check (AC-26, O-13).
	if !schema.RelationAllowed(schema.EntityTask, rel, dstType) {
		return "", fmt.Errorf("%w: task --%s--> %s", errs.ErrRelationNotAllowed, rel, dstType)
	}
	if !ids.Valid(dstID) {
		return "", fmt.Errorf("%w: target id %q", ErrInvalidInput, dstID)
	}
	// Existence first so a cross-project UUID reports the v1 scope rejection
	// instead of the foundation's raw not-found (AC-26: relations never
	// leave the project database).
	if s.m.opts.Relations == nil {
		return "", errors.New("kanban: relations service not configured")
	}
	state, err := s.m.opts.Relations.Resolve(ctx, dstType, dstID)
	if err != nil {
		return "", err
	}
	if state == relations.TargetMissing {
		return "", fmt.Errorf("%w: target %s %s is not in this project", errs.ErrRelationNotAllowed, dstType, dstID)
	}
	var id string
	err = s.m.handle.InTx(ctx, func(tx *sql.Tx) error {
		linked, lerr := s.m.opts.Relations.Link(ctx, tx, relations.Link{
			SrcType: schema.EntityTask, SrcID: t.ID,
			Relation: rel,
			DstType:  dstType, DstID: dstID,
		}, kanbanActor(actor))
		if lerr != nil {
			return lerr
		}
		id = linked
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Remove unlinks the triple; a missing triple is a no-op (§5.6).
func (s *linkService) Remove(ctx context.Context, taskID string, rel schema.Relation, dstType schema.EntityType, dstID string, actor events.Actor) error {
	t, err := s.m.loadGateTask(ctx, taskID)
	if err != nil {
		return err
	}
	if err := s.m.assertWritable(ctx); err != nil {
		return err
	}
	if !rel.Valid() {
		return fmt.Errorf("%w: %q", errs.ErrRelationNotAllowed, rel)
	}
	if !dstType.Valid() {
		return fmt.Errorf("%w: %q", errs.ErrRelationNotAllowed, dstType)
	}
	if !ids.Valid(dstID) {
		return fmt.Errorf("%w: target id %q", ErrInvalidInput, dstID)
	}
	if s.m.opts.Relations == nil {
		return errors.New("kanban: relations service not configured")
	}
	return s.m.handle.InTx(ctx, func(tx *sql.Tx) error {
		return s.m.opts.Relations.Unlink(ctx, tx, relations.Link{
			SrcType: schema.EntityTask, SrcID: t.ID,
			Relation: rel,
			DstType:  dstType, DstID: dstID,
		}, kanbanActor(actor))
	})
}

// List returns the task's outbound relations with titles and target states.
// The relation id comes from a read-only projection (the ids are part of
// the display contract, EntityLink.RelationID); target state always goes
// through relations.Resolve. TargetMissing rows are dirty data: dropped with
// a warning (§5.6). TargetSoftDeleted rows stay and render "目标已删除"
// (AC-24).
func (s *linkService) List(ctx context.Context, taskID string) ([]EntityLink, error) {
	if err := validateTaskID(taskID); err != nil {
		return nil, err
	}
	if _, ok, err := s.m.taskByID(ctx, s.m.db(), taskID); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%w: task %s", ErrTaskNotFound, taskID)
	}
	if s.m.opts.Relations == nil {
		return nil, errors.New("kanban: relations service not configured")
	}
	rows, err := s.m.db().QueryContext(ctx,
		`SELECT id, relation, dst_type, dst_id FROM entity_relations
		 WHERE src_type = ? AND src_id = ? ORDER BY created_at, id`,
		string(schema.EntityTask), taskID)
	if err != nil {
		return nil, fmt.Errorf("kanban: list links: %w", err)
	}
	defer rows.Close()
	type raw struct {
		id    string
		rel   schema.Relation
		dstT  schema.EntityType
		dstID string
	}
	var found []raw
	for rows.Next() {
		var r raw
		var rel, dstType string
		if err := rows.Scan(&r.id, &rel, &dstType, &r.dstID); err != nil {
			return nil, fmt.Errorf("kanban: list links: %w", err)
		}
		r.rel = schema.Relation(rel)
		r.dstT = schema.EntityType(dstType)
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kanban: list links: %w", err)
	}
	rows.Close()

	out := make([]EntityLink, 0, len(found))
	for _, r := range found {
		state, err := s.m.opts.Relations.Resolve(ctx, r.dstT, r.dstID)
		if err != nil {
			return nil, err
		}
		if state == relations.TargetMissing {
			if s.m.opts.Logger != nil {
				s.m.opts.Logger.Warn("kanban: relation target vanished",
					"relation_id", r.id, "dst_type", string(r.dstT), "dst_id", r.dstID)
			}
			continue
		}
		out = append(out, EntityLink{
			RelationID:  r.id,
			Relation:    r.rel,
			DstType:     r.dstT,
			DstID:       r.dstID,
			DstTitle:    s.targetTitle(ctx, r.dstT, r.dstID),
			TargetState: state,
		})
	}
	return out, nil
}

// ListTasksByTarget is the back-link used by the other entity's detail view
// (relations.To filtered to src_type=task). Deleted and archived tasks are
// excluded; live subtasks included (they are tasks too).
func (s *linkService) ListTasksByTarget(ctx context.Context, dstType schema.EntityType, dstID string) ([]TaskSummary, error) {
	if !dstType.Valid() {
		return nil, fmt.Errorf("%w: %q", errs.ErrBadEntityType, dstType)
	}
	if !ids.Valid(dstID) {
		return nil, fmt.Errorf("%w: target id %q", ErrInvalidInput, dstID)
	}
	if s.m.opts.Relations == nil {
		return nil, errors.New("kanban: relations service not configured")
	}
	links, err := s.m.opts.Relations.To(ctx, dstType, dstID, "")
	if err != nil {
		return nil, err
	}
	var taskIDs []string
	for _, l := range links {
		if l.SrcType == schema.EntityTask {
			taskIDs = append(taskIDs, l.SrcID)
		}
	}
	if len(taskIDs) == 0 {
		return nil, nil
	}
	where := []string{"1 = 1", "t.deleted_at IS NULL", "t.archived_at IS NULL",
		"t.id IN (" + placeholders(len(taskIDs)) + ")"}
	args := make([]any, 0, len(taskIDs))
	for _, id := range taskIDs {
		args = append(args, id)
	}
	items, _, err := s.m.querySummaries(ctx, s.m.db(), where, args,
		sortSpecFor(SortPositionAsc), listMaxLimit, "")
	if err != nil {
		return nil, err
	}
	return items, nil
}

// targetTitle resolves the display title of one relation target from the
// owning tables' read-only columns (SPEC-08 §2.2: target titles come from
// relations.Resolve plus module read queries). Run and weekly report have no
// title column, so their labels are derived, mirroring their UI list rules.
func (s *linkService) targetTitle(ctx context.Context, dstType schema.EntityType, dstID string) string {
	db := s.m.handle.DB()
	var (
		title string
		err   error
	)
	switch dstType {
	case schema.EntityNote, schema.EntityIdea, schema.EntityExperiment, schema.EntityTask:
		err = db.QueryRowContext(ctx,
			`SELECT title FROM `+targetTableName(dstType)+` WHERE id = ?`, dstID).Scan(&title)
	case schema.EntityRun:
		// Runs have no title column: derive the label from the owning
		// experiment (SPEC-07 §Table 显示名派生), falling back to "Run".
		var e sql.NullString
		err = db.QueryRowContext(ctx,
			`SELECT COALESCE((SELECT x.title FROM experiments x
		           JOIN runs r ON r.id = ? AND r.experiment_id = x.id), '')`, dstID).Scan(&e)
		if err == nil {
			if e.String == "" {
				title = "Run"
			} else {
				title = e.String + " · Run"
			}
		}
	case schema.EntityWeeklyReport:
		var start, end sql.NullString
		err = db.QueryRowContext(ctx,
			`SELECT period_start, period_end FROM weekly_reports WHERE id = ?`, dstID).Scan(&start, &end)
		if err == nil {
			title = "周报 " + start.String + " ~ " + end.String
		}
	default:
		return dstID
	}
	if err != nil {
		return dstID
	}
	return title
}

// targetTableName maps the target types this module can resolve titles for.
func targetTableName(t schema.EntityType) string {
	switch t {
	case schema.EntityNote:
		return "notes"
	case schema.EntityIdea:
		return "ideas"
	case schema.EntityExperiment:
		return "experiments"
	case schema.EntityTask:
		return "tasks"
	default:
		return ""
	}
}
