package experiments

import (
	"context"
	"database/sql"
	"fmt"

	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
)

// LinkIdea writes one validates relation Experiment → Idea (§3.6, §5.8). The
// foundation relation service emits relation.added once; repeats are no-ops.
func (s *Service) LinkIdea(ctx context.Context, experimentID, ideaID string, actor events.Actor) error {
	if !ids.Valid(ideaID) {
		return fmt.Errorf("%w: invalid idea id %q", ErrInvalidInput, ideaID)
	}
	handle, err := s.handle(ctx)
	if err != nil {
		return err
	}
	if err := s.assertWritable(ctx); err != nil {
		return err
	}
	link := relations.Link{
		SrcType:  schema.EntityExperiment,
		SrcID:    experimentID,
		Relation: schema.RelationValidates,
		DstType:  schema.EntityIdea,
		DstID:    ideaID,
	}
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.loadExperiment(ctx, tx, experimentID, false); err != nil {
			return err
		}
		if _, err := s.relationsService(handle).Link(ctx, tx, link, actorOf(actor)); err != nil {
			return fmt.Errorf("experiments: link idea: %w", err)
		}
		return nil
	})
}

// UnlinkIdea removes the validates relation. A missing relation is a no-op; the
// event comes from the foundation service.
func (s *Service) UnlinkIdea(ctx context.Context, experimentID, ideaID string, actor events.Actor) error {
	if !ids.Valid(ideaID) {
		return fmt.Errorf("%w: invalid idea id %q", ErrInvalidInput, ideaID)
	}
	handle, err := s.handle(ctx)
	if err != nil {
		return err
	}
	if err := s.assertWritable(ctx); err != nil {
		return err
	}
	link := relations.Link{
		SrcType:  schema.EntityExperiment,
		SrcID:    experimentID,
		Relation: schema.RelationValidates,
		DstType:  schema.EntityIdea,
		DstID:    ideaID,
	}
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.loadExperiment(ctx, tx, experimentID, false); err != nil {
			return err
		}
		if err := s.relationsService(handle).Unlink(ctx, tx, link, actorOf(actor)); err != nil {
			return fmt.Errorf("experiments: unlink idea: %w", err)
		}
		return nil
	})
}

// ListIdeas returns the linked ideas with their soft-deletion state (§5.8).
func (s *Service) ListIdeas(ctx context.Context, experimentID string) ([]IdeaRef, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.loadExperiment(ctx, handle.DB(), experimentID, true); err != nil {
		return nil, err
	}
	rows, err := handle.DB().QueryContext(ctx,
		`SELECT er.dst_id, COALESCE(i.title, ''), i.deleted_at
		   FROM entity_relations er
		   LEFT JOIN ideas i ON i.id = er.dst_id
		  WHERE er.src_type = 'experiment' AND er.src_id = ?
		    AND er.relation = 'validates' AND er.dst_type = 'idea'
		  ORDER BY er.created_at, er.dst_id`, experimentID)
	if err != nil {
		return nil, fmt.Errorf("experiments: list linked ideas: %w", err)
	}
	defer rows.Close()
	out := make([]IdeaRef, 0, 4)
	for rows.Next() {
		var (
			ref       IdeaRef
			deletedAt sql.NullString
		)
		if err := rows.Scan(&ref.ID, &ref.Title, &deletedAt); err != nil {
			return nil, fmt.Errorf("experiments: list linked ideas: %w", err)
		}
		ref.TargetDeleted = deletedAt.Valid
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("experiments: list linked ideas: %w", err)
	}
	return out, nil
}
