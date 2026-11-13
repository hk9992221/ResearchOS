package notes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
)

// relationService adapts the foundation relation registry to the notes-facing
// API (SPEC-05 §5.5). It adds no relation types and writes one edge per pair
// (D-05-08: reverse lookups go through ListTo).
//
// A record's edges are stored on its backing note, because the frozen relation
// registry registers references for note sources only (SPEC-01 §4.6) and
// D-05-03 makes the backing note the record's page. The service therefore
// normalizes db_record sources to their note so callers can treat a record as
// the relation owner.
type relationService struct {
	ws        *workspace.Workspace
	projectID string
	clock     clock.Clock
}

var _ RelationService = (*relationService)(nil)

// NewRelationService binds the relation service to one project of ws.
func NewRelationService(ws *workspace.Workspace, projectID string, opt ServiceOptions) *relationService {
	if opt.Clock == nil {
		opt.Clock = clock.System{}
	}
	return &relationService{ws: ws, projectID: projectID, clock: opt.Clock}
}

// Add creates the edge. Duplicates are idempotent (the foundation returns the
// existing id without a second event) and self-relations are rejected by the
// foundation.
func (s *relationService) Add(ctx context.Context, edge RelationEdge) error {
	handle, err := s.ws.Project(ctx, s.projectID)
	if err != nil {
		return err
	}
	src, err := s.normalizeOutbound(ctx, handle, edge.Src)
	if err != nil {
		return err
	}
	link, err := relationLink(src, edge.Dst, edge.Relation)
	if err != nil {
		return err
	}
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		_, err := s.service(handle).Link(ctx, tx, link, noteActor(edge.Actor))
		return err
	})
}

// Remove deletes the edge. A missing edge is a no-op.
func (s *relationService) Remove(ctx context.Context, edge RelationEdge) error {
	handle, err := s.ws.Project(ctx, s.projectID)
	if err != nil {
		return err
	}
	src, err := s.normalizeOutbound(ctx, handle, edge.Src)
	if err != nil {
		return err
	}
	link, err := relationLink(src, edge.Dst, edge.Relation)
	if err != nil {
		return err
	}
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		return s.service(handle).Unlink(ctx, tx, link, noteActor(edge.Actor))
	})
}

// ListFrom returns the outgoing edges of ref with each target resolved. A
// db_record ref reads the edges of its backing note, mirroring Add.
func (s *relationService) ListFrom(ctx context.Context, ref EntityRef, relation string) ([]ResolvedLink, error) {
	if err := noteValidateRef(ref); err != nil {
		return nil, err
	}
	rel, err := relationFilter(relation)
	if err != nil {
		return nil, err
	}
	handle, err := s.ws.Project(ctx, s.projectID)
	if err != nil {
		return nil, err
	}
	src, err := s.normalizeOutbound(ctx, handle, ref)
	if err != nil {
		return nil, err
	}
	links, err := s.service(handle).From(ctx, src.Type, src.ID, rel)
	if err != nil {
		return nil, err
	}
	resolver := noteResolver{ws: s.ws, projectID: s.projectID}
	out := make([]ResolvedLink, 0, len(links))
	for _, link := range links {
		target, err := resolver.resolve(ctx, handle, EntityRef{Type: link.DstType, ID: link.DstID})
		if err != nil {
			return nil, err
		}
		out = append(out, ResolvedLink{
			Src:      EntityRef{Type: link.SrcType, ID: link.SrcID},
			Relation: link.Relation,
			Dst:      EntityRef{Type: link.DstType, ID: link.DstID},
			Ref:      EntityRef{Type: link.DstType, ID: link.DstID},
			Title:    target.Title,
			Deleted:  target.Deleted,
			Missing:  target.Missing,
		})
	}
	return out, nil
}

// ListTo returns the inbound edges of ref (the inlinks of SPEC-05 §5.5) with
// each source resolved. It deliberately does not normalize db_record targets:
// inbound edges may legitimately point at a record.
func (s *relationService) ListTo(ctx context.Context, ref EntityRef, relation string) ([]ResolvedLink, error) {
	if err := noteValidateRef(ref); err != nil {
		return nil, err
	}
	rel, err := relationFilter(relation)
	if err != nil {
		return nil, err
	}
	handle, err := s.ws.Project(ctx, s.projectID)
	if err != nil {
		return nil, err
	}
	links, err := s.service(handle).To(ctx, ref.Type, ref.ID, rel)
	if err != nil {
		return nil, err
	}
	resolver := noteResolver{ws: s.ws, projectID: s.projectID}
	out := make([]ResolvedLink, 0, len(links))
	for _, link := range links {
		source, err := resolver.resolve(ctx, handle, EntityRef{Type: link.SrcType, ID: link.SrcID})
		if err != nil {
			return nil, err
		}
		out = append(out, ResolvedLink{
			Src:      EntityRef{Type: link.SrcType, ID: link.SrcID},
			Relation: link.Relation,
			Dst:      EntityRef{Type: link.DstType, ID: link.DstID},
			Ref:      EntityRef{Type: link.SrcType, ID: link.SrcID},
			Title:    source.Title,
			Deleted:  source.Deleted,
			Missing:  source.Missing,
		})
	}
	return out, nil
}

// service binds the foundation relation service to handle.
func (s *relationService) service(handle *workspace.ProjectHandle) *relations.Service {
	return relations.New(handle.DB(), s.ws.Global().DB(), handle.Events(), s.clock)
}

// relationLink validates an edge and maps it to the foundation triple, applying
// the SPEC-05 §5.5 relation table. src must already be normalized: db_record
// sources are rewritten to their backing note by normalizeOutbound, so the
// frozen direction registry (references: note → note/paper) always holds.
func relationLink(src, dst EntityRef, relation schema.Relation) (relations.Link, error) {
	if err := noteValidateRef(src); err != nil {
		return relations.Link{}, err
	}
	if err := noteValidateRef(dst); err != nil {
		return relations.Link{}, err
	}
	mapped, ok := noteMappedRelation(src.Type, dst.Type)
	if !ok {
		return relations.Link{}, fmt.Errorf("%w: %s -> %s", errs.ErrRelationNotAllowed, src.Type, dst.Type)
	}
	if relation != "" && relation != mapped {
		return relations.Link{}, fmt.Errorf(
			"%w: %s -> %s must use %q, got %q",
			errs.ErrRelationNotAllowed, src.Type, dst.Type, mapped, relation)
	}
	return relations.Link{
		SrcType:  src.Type,
		SrcID:    src.ID,
		Relation: mapped,
		DstType:  dst.Type,
		DstID:    dst.ID,
	}, nil
}

// noteMappedRelation implements the §5.5 table: notes reference papers and
// notes, and are related to ideas, experiments and records.
func noteMappedRelation(src, dst schema.EntityType) (schema.Relation, bool) {
	if src != schema.EntityNote {
		return "", false
	}
	switch dst {
	case schema.EntityPaper, schema.EntityNote:
		return schema.RelationReferences, true
	case schema.EntityIdea, schema.EntityExperiment, schema.EntityDBRecord:
		return schema.RelationRelatedTo, true
	default:
		return "", false
	}
}

// normalizeOutbound maps a db_record owner to its backing note. A record whose
// row is gone or trashed, or whose page note is absent or trashed, yields a
// readable validation error instead of a foundation "not found".
func (s *relationService) normalizeOutbound(ctx context.Context, handle *workspace.ProjectHandle, ref EntityRef) (EntityRef, error) {
	if ref.Type != schema.EntityDBRecord {
		return ref, nil
	}
	if err := noteValidateRef(ref); err != nil {
		return EntityRef{}, err
	}
	var (
		noteID  sql.NullString
		deleted sql.NullString
	)
	err := handle.DB().QueryRowContext(ctx,
		`SELECT note_id, deleted_at FROM db_records WHERE id = ?`, ref.ID).Scan(&noteID, &deleted)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return EntityRef{}, fmt.Errorf("notes: db_record %s not found", ref.ID)
	case err != nil:
		return EntityRef{}, fmt.Errorf("notes: normalize db_record %s: %w", ref.ID, err)
	case deleted.Valid:
		return EntityRef{}, fmt.Errorf("%w: db_record %s", errs.ErrEntityDeleted, ref.ID)
	case !noteID.Valid || noteID.String == "":
		return EntityRef{}, fmt.Errorf("notes: db_record %s has no backing note", ref.ID)
	}

	var noteDeleted sql.NullString
	err = handle.DB().QueryRowContext(ctx, `SELECT deleted_at FROM notes WHERE id = ?`, noteID.String).
		Scan(&noteDeleted)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return EntityRef{}, fmt.Errorf("notes: db_record %s backing note %s not found", ref.ID, noteID.String)
	case err != nil:
		return EntityRef{}, fmt.Errorf("notes: normalize db_record %s: %w", ref.ID, err)
	case noteDeleted.Valid:
		return EntityRef{}, fmt.Errorf("%w: backing note %s of db_record %s", errs.ErrEntityDeleted, noteID.String, ref.ID)
	}
	return EntityRef{Type: schema.EntityNote, ID: noteID.String}, nil
}

// relationFilter validates the optional relation filter of ListFrom/ListTo.
func relationFilter(relation string) (schema.Relation, error) {
	if relation == "" {
		return "", nil
	}
	rel := schema.Relation(relation)
	if !rel.Valid() {
		return "", fmt.Errorf("%w: %q", errs.ErrRelationNotAllowed, relation)
	}
	return rel, nil
}
