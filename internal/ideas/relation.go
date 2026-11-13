package ideas

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
)

// triple is one entity_relations row.
type triple struct {
	SrcType  string
	SrcID    string
	Relation string
	DstType  string
	DstID    string
}

func (t triple) object() events.Object {
	return events.Object{
		"src_type": t.SrcType,
		"src_id":   t.SrcID,
		"relation": t.Relation,
		"dst_type": t.DstType,
		"dst_id":   t.DstID,
	}
}

// linkTriple inserts a relation triple and emits relation.added with the Idea as
// the event subject (SPEC-06 §7.1, A4 degradation). An existing triple is
// idempotent: created=false, no event.
//
// The paper endpoint is written directly instead of through
// foundation/relations because SPEC-01 §5.7 only permits references → paper to
// cross databases, while SPEC-06 A7 settles on related_to → paper for Ideas
// until the shared contract adopts a dedicated relation type.
func (s *service) linkTriple(ctx context.Context, tx *sql.Tx, handle *workspace.ProjectHandle,
	t triple, actor events.Actor, summary string) (bool, error) {
	if !schema.EntityType(t.SrcType).Valid() || !schema.EntityType(t.DstType).Valid() {
		return false, serviceError(ErrInvalidSource, "invalid relation endpoints %s → %s", t.SrcType, t.DstType)
	}
	rel := schema.Relation(t.Relation)
	if !rel.Valid() || !schema.RelationAllowed(schema.EntityType(t.SrcType), rel, schema.EntityType(t.DstType)) {
		return false, serviceError(ErrInvalidInput, "relation %s → %s → %s is not allowed", t.SrcType, t.Relation, t.DstType)
	}
	id, found, err := findTriple(ctx, tx, t)
	if err != nil {
		return false, err
	}
	if found {
		return false, nil
	}
	id, err = s.newID()
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO entity_relations(id, src_type, src_id, relation, dst_type, dst_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, t.SrcType, t.SrcID, t.Relation, t.DstType, t.DstID, s.now()); err != nil {
		return false, fmt.Errorf("ideas: link relation: %w", err)
	}
	obj := t.object()
	return true, handle.Events().Emit(ctx, tx, events.Event{
		Type:       schema.EventRelationAdded,
		EntityType: schema.EntityIdea,
		EntityID:   eventSubject(t),
		New:        &obj,
		Summary:    summary,
		Actor:      actor,
	})
}

// unlinkTriple deletes the triple and emits relation.removed. A missing triple
// is a no-op (removed=false).
func (s *service) unlinkTriple(ctx context.Context, tx *sql.Tx, handle *workspace.ProjectHandle,
	t triple, actor events.Actor, summary string) (bool, error) {
	id, found, err := findTriple(ctx, tx, t)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM entity_relations WHERE id = ?`, id); err != nil {
		return false, fmt.Errorf("ideas: unlink relation: %w", err)
	}
	obj := t.object()
	return true, handle.Events().Emit(ctx, tx, events.Event{
		Type:       schema.EventRelationRemoved,
		EntityType: schema.EntityIdea,
		EntityID:   eventSubject(t),
		Old:        &obj,
		Summary:    summary,
		Actor:      actor,
	})
}

// eventSubject is the Idea side of a triple (SPEC-06 §7.1: the initiating side
// is the event entity).
func eventSubject(t triple) string {
	if t.SrcType == string(schema.EntityIdea) {
		return t.SrcID
	}
	return t.DstID
}

func findTriple(ctx context.Context, tx *sql.Tx, t triple) (string, bool, error) {
	var id string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM entity_relations
		 WHERE src_type = ? AND src_id = ? AND relation = ? AND dst_type = ? AND dst_id = ?`,
		t.SrcType, t.SrcID, t.Relation, t.DstType, t.DstID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("ideas: find relation: %w", err)
	}
	return id, true, nil
}

// LinkEntity associates an Idea with a paper, note or experiment (IDEA-04/05/06).
func (s *service) LinkEntity(ctx context.Context, in LinkInput) error {
	actor, err := writeActor(in.Actor)
	if err != nil {
		return err
	}
	tr, err := relationTriple(in.IdeaID, in.TargetType, in.TargetID)
	if err != nil {
		return err
	}
	handle, err := s.begin(ctx)
	if err != nil {
		return err
	}
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := requireIdeaTx(ctx, tx, in.IdeaID, false); err != nil {
			return err
		}
		if _, found, err := findTriple(ctx, tx, tr); err != nil {
			return err
		} else if found {
			return nil
		}
		info, err := s.resolveTarget(ctx, tx, in.TargetType, in.TargetID)
		if err != nil {
			return err
		}
		switch {
		case info.Broken:
			return serviceError(ErrTargetNotFound, "%s %s does not exist or is not linked to the project",
				in.TargetType, in.TargetID)
		case info.Deleted:
			return serviceError(ErrTargetDeleted, "%s %s is deleted", in.TargetType, in.TargetID)
		}
		summary := "关联" + relationLabel(in.TargetType) + "：" + info.Title
		_, err = s.linkTriple(ctx, tx, handle, tr, actor, summary)
		return err
	})
}

// UnlinkEntity removes one relation; a missing relation is a no-op.
func (s *service) UnlinkEntity(ctx context.Context, in LinkInput) error {
	actor, err := writeActor(in.Actor)
	if err != nil {
		return err
	}
	tr, err := relationTriple(in.IdeaID, in.TargetType, in.TargetID)
	if err != nil {
		return err
	}
	handle, err := s.begin(ctx)
	if err != nil {
		return err
	}
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := requireIdeaTx(ctx, tx, in.IdeaID, false); err != nil {
			return err
		}
		title := s.resolvedSourceTitle(ctx, tx, SourceRef{Type: SourceType(in.TargetType), ID: in.TargetID})
		summary := "解除" + relationLabel(in.TargetType) + "：" + title
		_, err := s.unlinkTriple(ctx, tx, handle, tr, actor, summary)
		return err
	})
}

// relationTriple maps a LinkInput onto its fixed triple shape (SPEC-06 §5.4).
func relationTriple(ideaID, targetType, targetID string) (triple, error) {
	switch targetType {
	case TargetPaper:
		return triple{
			SrcType: string(schema.EntityIdea), SrcID: ideaID,
			Relation: string(schema.RelationRelatedTo),
			DstType:  "paper", DstID: targetID,
		}, nil
	case TargetNote:
		return triple{
			SrcType: string(schema.EntityIdea), SrcID: ideaID,
			Relation: string(schema.RelationRelatedTo),
			DstType:  "note", DstID: targetID,
		}, nil
	case TargetExperiment:
		return triple{
			SrcType: "experiment", SrcID: targetID,
			Relation: string(schema.RelationValidates),
			DstType:  string(schema.EntityIdea), DstID: ideaID,
		}, nil
	default:
		return triple{}, serviceError(ErrInvalidInput, "target type %q must be paper, note or experiment", targetType)
	}
}

// ListRelations resolves every relation of an Idea, including soft-deleted
// targets (SPEC-06 §5.4).
func (s *service) ListRelations(ctx context.Context, ideaID string) (RelationSet, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return RelationSet{}, err
	}
	if _, err := s.ideaByIDDB(ctx, handle.DB(), ideaID, true); err != nil {
		return RelationSet{}, err
	}
	var out RelationSet
	rows, err := handle.DB().QueryContext(ctx,
		`SELECT src_type, src_id, relation, dst_type, dst_id FROM entity_relations
		 WHERE (src_type = 'idea' AND src_id = ?) OR (dst_type = 'idea' AND dst_id = ?)
		 ORDER BY created_at, id`, ideaID, ideaID)
	if err != nil {
		return RelationSet{}, fmt.Errorf("ideas: list relations: %w", err)
	}
	defer rows.Close()
	var triples []triple
	for rows.Next() {
		var t triple
		if err := rows.Scan(&t.SrcType, &t.SrcID, &t.Relation, &t.DstType, &t.DstID); err != nil {
			return RelationSet{}, fmt.Errorf("ideas: scan relation: %w", err)
		}
		triples = append(triples, t)
	}
	if err := rows.Err(); err != nil {
		return RelationSet{}, fmt.Errorf("ideas: list relations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return RelationSet{}, fmt.Errorf("ideas: list relations: %w", err)
	}
	for _, t := range triples {
		switch {
		case t.SrcType == "idea" && t.SrcID == ideaID && t.Relation == "related_to" && t.DstType == "paper":
			ref, err := s.refOf(ctx, handle.DB(), "paper", t.DstID)
			if err != nil {
				return RelationSet{}, err
			}
			out.Papers = append(out.Papers, ref)
		case t.SrcType == "idea" && t.SrcID == ideaID && t.Relation == "related_to" && t.DstType == "note":
			ref, err := s.refOf(ctx, handle.DB(), "note", t.DstID)
			if err != nil {
				return RelationSet{}, err
			}
			out.Notes = append(out.Notes, ref)
		case t.SrcType == "experiment" && t.Relation == "validates" && t.DstType == "idea" && t.DstID == ideaID:
			ref, err := s.refOf(ctx, handle.DB(), "experiment", t.SrcID)
			if err != nil {
				return RelationSet{}, err
			}
			out.Experiments = append(out.Experiments, ref)
		case t.SrcType == "idea" && t.SrcID == ideaID && t.Relation == "derived_from":
			ref, err := s.refOf(ctx, handle.DB(), "idea", t.DstID)
			if err != nil {
				return RelationSet{}, err
			}
			out.DerivedFrom = append(out.DerivedFrom, ref)
		case t.SrcType == "idea" && t.Relation == "derived_from" && t.DstType == "idea" && t.DstID == ideaID:
			ref, err := s.refOf(ctx, handle.DB(), "idea", t.SrcID)
			if err != nil {
				return RelationSet{}, err
			}
			out.Derived = append(out.Derived, ref)
		}
	}
	return out, nil
}

// refOf resolves one endpoint into an EntityRef.
func (s *service) refOf(ctx context.Context, q querier, typ, id string) (EntityRef, error) {
	info, err := s.resolveTarget(ctx, q, typ, id)
	if err != nil {
		return EntityRef{}, err
	}
	return EntityRef{
		Type:      typ,
		ID:        id,
		Title:     info.Title,
		Deleted:   info.Deleted,
		BrokenRef: info.Broken,
	}, nil
}

// DeriveIdea creates a new Idea and the derived_from relation (SPEC-06 §4.2.3).
func (s *service) DeriveIdea(ctx context.Context, in DeriveIdeaInput) (*Idea, error) {
	actor, err := writeActor(in.Actor)
	if err != nil {
		return nil, err
	}
	title, err := normalizeTitle(in.Title)
	if err != nil {
		return nil, err
	}
	if err := validateDescription(in.DescriptionMD); err != nil {
		return nil, err
	}
	handle, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := s.newID()
	if err != nil {
		return nil, err
	}
	now := s.now()
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		source, err := requireIdeaTx(ctx, tx, in.SourceIdeaID, false)
		if err != nil {
			return err
		}
		if err := checkLineageDepth(ctx, tx, source.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ideas(id, title, description, status, rejection_reason, created_at, updated_at, deleted_at)
			 VALUES (?, ?, ?, ?, '', ?, ?, NULL)`,
			id, title, in.DescriptionMD, string(StatusNew), now, now); err != nil {
			return fmt.Errorf("ideas: derive insert: %w", err)
		}
		if err := handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityCreated,
			EntityType: schema.EntityIdea,
			EntityID:   id,
			Summary:    "由「" + source.Title + "」衍生",
			Actor:      actor,
			At:         s.clock.Now(),
		}); err != nil {
			return err
		}
		tr := triple{
			SrcType: "idea", SrcID: id,
			Relation: string(schema.RelationDerivedFrom),
			DstType:  "idea", DstID: source.ID,
		}
		_, err = s.linkTriple(ctx, tx, handle, tr, actor, "derived_from → "+source.Title)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.notifyIndex(ctx, schema.EntityIdea, id, schema.EventEntityCreated)
	return s.ideaByID(ctx, id, false)
}

// checkLineageDepth walks derived_from ancestors and enforces the 20-level cap
// (SPEC-06 §5.4).
func checkLineageDepth(ctx context.Context, tx *sql.Tx, ideaID string) error {
	seen := map[string]bool{}
	current := ideaID
	depth := 0
	for current != "" {
		if seen[current] {
			return serviceError(ErrLineageTooDeep, "derived_from cycle detected at idea %s", current)
		}
		seen[current] = true
		var parent string
		err := tx.QueryRowContext(ctx,
			`SELECT dst_id FROM entity_relations
			 WHERE src_type = 'idea' AND src_id = ? AND relation = 'derived_from' AND dst_type = 'idea'
			 LIMIT 1`, current).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("ideas: read lineage: %w", err)
		}
		depth++
		if depth >= maxLineageDepth {
			return serviceError(ErrLineageTooDeep, "derived_from chain exceeds %d levels", maxLineageDepth)
		}
		current = parent
	}
	return nil
}

const maxLineageDepth = 20
