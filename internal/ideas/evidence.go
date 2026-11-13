package ideas

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
)

// requireLiveIdea resolves an Idea that must exist and must not be soft-deleted.
// A deleted Idea yields idea.deleted rather than idea.not_found (AC-20).
func (s *service) requireLiveIdea(ctx context.Context, db *sql.DB, ideaID string) error {
	idea, err := s.ideaByIDDB(ctx, db, ideaID, true)
	if err != nil {
		return err
	}
	if idea.DeletedAt != nil {
		return serviceError(ErrIdeaDeleted, "idea %s is deleted", ideaID)
	}
	return nil
}

// evidenceColumns is the read projection of one evidence row.
const evidenceColumns = `id, idea_id, direction, content, source_type, source_id,
	ai_generated, confirmed, content_hash, provenance, created_at, updated_at, deleted_at`

// evidenceFrom scans one evidence row.
func evidenceFrom(row interface{ Scan(dest ...any) error }) (*Evidence, error) {
	var (
		ev          Evidence
		direction   string
		sourceType  string
		aiGenerated int
		confirmed   int
		provRaw     string
		createdAt   string
		updatedAt   string
		deletedAt   sql.NullString
	)
	if err := row.Scan(&ev.ID, &ev.IdeaID, &direction, &ev.ContentMD, &sourceType, &ev.Source.ID,
		&aiGenerated, &confirmed, &ev.ContentHash, &provRaw, &createdAt, &updatedAt, &deletedAt); err != nil {
		return nil, err
	}
	ev.Direction = Direction(direction)
	ev.Source.Type = SourceType(sourceType)
	ev.AIGenerated = aiGenerated == 1
	ev.Confirmed = confirmed == 1
	if provRaw != "" && provRaw != "{}" && provRaw != "null" {
		var prov Provenance
		if err := json.Unmarshal([]byte(provRaw), &prov); err != nil {
			return nil, fmt.Errorf("ideas: evidence %s provenance: %w", ev.ID, err)
		}
		ev.Provenance = &prov
	}
	at, err := clock.Parse(createdAt)
	if err != nil {
		return nil, fmt.Errorf("ideas: evidence created_at: %w", err)
	}
	ev.CreatedAt = at
	at, err = clock.Parse(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("ideas: evidence updated_at: %w", err)
	}
	ev.UpdatedAt = at
	if deletedAt.Valid {
		t, err := clock.Parse(deletedAt.String)
		if err != nil {
			return nil, fmt.Errorf("ideas: evidence deleted_at: %w", err)
		}
		ev.DeletedAt = &t
	}
	return &ev, nil
}

// requireEvidenceTx reads one Evidence inside tx.
func requireEvidenceTx(ctx context.Context, tx *sql.Tx, id string, includeDeleted bool) (*Evidence, error) {
	if !ids.Valid(id) {
		return nil, serviceError(ErrEvidenceNotFound, "evidence %q not found", id)
	}
	ev, err := evidenceFrom(tx.QueryRowContext(ctx,
		`SELECT `+evidenceColumns+` FROM evidence WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, serviceError(ErrEvidenceNotFound, "evidence %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("ideas: read evidence %s: %w", id, err)
	}
	if ev.DeletedAt != nil && !includeDeleted {
		return nil, serviceError(ErrEvidenceDeleted, "evidence %s is deleted", id)
	}
	return ev, nil
}

// AddEvidence records a manually confirmed Evidence (IDEA-07).
func (s *service) AddEvidence(ctx context.Context, in AddEvidenceInput) (*Evidence, error) {
	actor, err := writeActor(in.Actor)
	if err != nil {
		return nil, err
	}
	if !in.Direction.Valid() {
		return nil, serviceError(ErrInvalidInput, "direction %q must be support or oppose", string(in.Direction))
	}
	if !in.Source.Type.Valid() {
		return nil, serviceError(ErrInvalidSource, "source type %q is not registered", string(in.Source.Type))
	}
	content, err := normalizeContent(in.ContentMD)
	if err != nil {
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
	var title string
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := requireIdeaTx(ctx, tx, in.IdeaID, false); err != nil {
			return err
		}
		title, err = s.validateEvidenceSource(ctx, tx, in.Source)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO evidence(id, idea_id, direction, content, source_type, source_id,
			   ai_generated, confirmed, ai_output_id, provenance, content_hash, created_at, updated_at, deleted_at)
			 VALUES (?, ?, ?, ?, ?, ?, 0, 1, NULL, '{}', ?, ?, ?, NULL)`,
			id, in.IdeaID, string(in.Direction), content, string(in.Source.Type), in.Source.ID,
			hashContent(content), now, now); err != nil {
			return fmt.Errorf("ideas: add evidence: %w", err)
		}
		return handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityCreated,
			EntityType: schema.EntityEvidence,
			EntityID:   id,
			Summary:    fmt.Sprintf("添加%s证据（来源：%s）", in.Direction.DisplayName(), title),
			Actor:      actor,
			At:         s.clock.Now(),
		})
	})
	if err != nil {
		return nil, err
	}
	s.notifyIndex(ctx, schema.EntityEvidence, id, schema.EventEntityCreated)
	return s.evidenceByID(ctx, id)
}

// UpdateEvidence edits content and (drafts only) direction. No Activity event is
// emitted (SPEC-06 §5.5, A6).
func (s *service) UpdateEvidence(ctx context.Context, in UpdateEvidenceInput) (*Evidence, error) {
	if _, err := writeActor(in.Actor); err != nil {
		return nil, err
	}
	handle, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	var updated *Evidence
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		ev, err := requireEvidenceTx(ctx, tx, in.EvidenceID, false)
		if err != nil {
			return err
		}
		if _, err := requireIdeaTx(ctx, tx, ev.IdeaID, false); err != nil {
			return err
		}
		direction := ev.Direction
		if in.Direction != nil {
			if ev.Confirmed {
				return serviceError(ErrEvidenceDirectionLocked,
					"direction of confirmed evidence cannot change; delete and recreate instead")
			}
			if !in.Direction.Valid() {
				return serviceError(ErrInvalidInput, "direction %q must be support or oppose", string(*in.Direction))
			}
			direction = *in.Direction
		}
		content := ev.ContentMD
		hash := ev.ContentHash
		if in.ContentMD != nil {
			content, err = normalizeContent(*in.ContentMD)
			if err != nil {
				return err
			}
			hash = hashContent(content)
		}
		if in.Direction == nil && in.ContentMD == nil {
			updated = ev
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE evidence SET direction = ?, content = ?, content_hash = ?, updated_at = ? WHERE id = ?`,
			string(direction), content, hash, s.now(), ev.ID); err != nil {
			return fmt.Errorf("ideas: update evidence: %w", err)
		}
		updated, err = s.evidenceByIDTx(ctx, tx, ev.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.notifyIndex(ctx, schema.EntityEvidence, updated.ID, schema.EventEntityUpdated)
	return updated, nil
}

// ConfirmEvidence confirms AI drafts one by one, each in its own transaction
// (SPEC-06 §5.6, O7).
func (s *service) ConfirmEvidence(ctx context.Context, in ConfirmEvidenceInput) (ConfirmEvidenceResult, error) {
	actor, err := writeActor(in.Actor)
	if err != nil {
		return ConfirmEvidenceResult{}, err
	}
	handle, err := s.begin(ctx)
	if err != nil {
		return ConfirmEvidenceResult{}, err
	}
	if err := s.requireLiveIdea(ctx, handle.DB(), in.IdeaID); err != nil {
		return ConfirmEvidenceResult{}, err
	}
	var result ConfirmEvidenceResult
	for _, evidenceID := range in.EvidenceIDs {
		failure, err := s.confirmOne(ctx, handle, in.IdeaID, evidenceID, actor)
		if err != nil {
			return result, err
		}
		if failure != nil {
			result.Failed = append(result.Failed, *failure)
			continue
		}
		result.Confirmed = append(result.Confirmed, evidenceID)
	}
	return result, nil
}

// confirmOne returns a non-nil failure for a rejected item.
func (s *service) confirmOne(ctx context.Context, handle *workspace.ProjectHandle,
	ideaID, evidenceID string, actor events.Actor) (*EvidenceFailure, error) {
	if !ids.Valid(evidenceID) {
		return &EvidenceFailure{ID: evidenceID, Code: ErrEvidenceNotFound, Message: "invalid evidence id"}, nil
	}
	handleErr := handle.InTx(ctx, func(tx *sql.Tx) error {
		ev, err := requireEvidenceTx(ctx, tx, evidenceID, false)
		if err != nil {
			return err
		}
		if ev.IdeaID != ideaID {
			return serviceError(ErrEvidenceNotFound, "evidence %s does not belong to idea %s", evidenceID, ideaID)
		}
		if _, err := requireIdeaTx(ctx, tx, ideaID, false); err != nil {
			return err
		}
		if !ev.AIGenerated {
			return serviceError(ErrEvidenceNotAIDraft, "evidence %s is not an AI draft", evidenceID)
		}
		if ev.Confirmed {
			return nil // already effective: idempotent
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE evidence SET confirmed = 1, updated_at = ? WHERE id = ?`, s.now(), ev.ID); err != nil {
			return fmt.Errorf("ideas: confirm evidence: %w", err)
		}
		title := s.resolvedSourceTitle(ctx, tx, ev.Source)
		oldObj := events.Object{"status": "draft"}
		newObj := events.Object{"status": "confirmed"}
		return handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventStatusChanged,
			EntityType: schema.EntityEvidence,
			EntityID:   ev.ID,
			Old:        &oldObj,
			New:        &newObj,
			Summary:    fmt.Sprintf("确认%s证据（来源：%s）", ev.Direction.DisplayName(), title),
			Actor:      actor,
			At:         s.clock.Now(),
		})
	})
	if handleErr != nil {
		code := CodeOf(handleErr)
		if code == "" {
			return nil, handleErr
		}
		return &EvidenceFailure{ID: evidenceID, Code: code, Message: handleErr.Error()}, nil
	}
	return nil, nil
}

// DiscardEvidence soft-deletes AI drafts of one Idea.
func (s *service) DiscardEvidence(ctx context.Context, in DiscardEvidenceInput) (DiscardEvidenceResult, error) {
	actor, err := writeActor(in.Actor)
	if err != nil {
		return DiscardEvidenceResult{}, err
	}
	handle, err := s.begin(ctx)
	if err != nil {
		return DiscardEvidenceResult{}, err
	}
	if err := s.requireLiveIdea(ctx, handle.DB(), in.IdeaID); err != nil {
		return DiscardEvidenceResult{}, err
	}
	var result DiscardEvidenceResult
	for _, evidenceID := range in.EvidenceIDs {
		failure, err := s.discardOne(ctx, handle, in.IdeaID, evidenceID, actor)
		if err != nil {
			return result, err
		}
		if failure != nil {
			result.Failed = append(result.Failed, *failure)
			continue
		}
		result.Discarded = append(result.Discarded, evidenceID)
	}
	return result, nil
}

func (s *service) discardOne(ctx context.Context, handle *workspace.ProjectHandle,
	ideaID, evidenceID string, actor events.Actor) (*EvidenceFailure, error) {
	if !ids.Valid(evidenceID) {
		return &EvidenceFailure{ID: evidenceID, Code: ErrEvidenceNotFound, Message: "invalid evidence id"}, nil
	}
	handleErr := handle.InTx(ctx, func(tx *sql.Tx) error {
		ev, err := requireEvidenceTx(ctx, tx, evidenceID, false)
		if err != nil {
			return err
		}
		if ev.IdeaID != ideaID {
			return serviceError(ErrEvidenceNotFound, "evidence %s does not belong to idea %s", evidenceID, ideaID)
		}
		if _, err := requireIdeaTx(ctx, tx, ideaID, false); err != nil {
			return err
		}
		if ev.Confirmed {
			return serviceError(ErrEvidenceConfirmed, "confirmed evidence cannot be discarded; delete it instead")
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE evidence SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, s.now(), ev.ID); err != nil {
			return fmt.Errorf("ideas: discard evidence: %w", err)
		}
		return handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityDeleted,
			EntityType: schema.EntityEvidence,
			EntityID:   ev.ID,
			Summary:    "丢弃 AI 证据草稿",
			Actor:      actor,
			At:         s.clock.Now(),
		})
	})
	if handleErr != nil {
		code := CodeOf(handleErr)
		if code == "" {
			return nil, handleErr
		}
		return &EvidenceFailure{ID: evidenceID, Code: code, Message: handleErr.Error()}, nil
	}
	return nil, nil
}

// DeleteEvidence soft-deletes one Evidence (confirmed or draft).
func (s *service) DeleteEvidence(ctx context.Context, evidenceID string, actor Actor) error {
	who, err := writeActor(actor)
	if err != nil {
		return err
	}
	handle, err := s.begin(ctx)
	if err != nil {
		return err
	}
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		ev, err := requireEvidenceTx(ctx, tx, evidenceID, true)
		if err != nil {
			return err
		}
		if _, err := requireIdeaTx(ctx, tx, ev.IdeaID, false); err != nil {
			return err
		}
		if ev.DeletedAt != nil {
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE evidence SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, s.now(), ev.ID); err != nil {
			return fmt.Errorf("ideas: delete evidence: %w", err)
		}
		return handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityDeleted,
			EntityType: schema.EntityEvidence,
			EntityID:   ev.ID,
			Summary:    fmt.Sprintf("删除%s证据", ev.Direction.DisplayName()),
			Actor:      who,
			At:         s.clock.Now(),
		})
	})
	if err != nil {
		return err
	}
	s.notifyIndex(ctx, schema.EntityEvidence, evidenceID, schema.EventEntityDeleted)
	return nil
}

// RestoreEvidence clears deleted_at. A restored draft stays a draft (AC-18).
func (s *service) RestoreEvidence(ctx context.Context, evidenceID string, actor Actor) error {
	who, err := writeActor(actor)
	if err != nil {
		return err
	}
	handle, err := s.begin(ctx)
	if err != nil {
		return err
	}
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		ev, err := requireEvidenceTx(ctx, tx, evidenceID, true)
		if err != nil {
			return err
		}
		if _, err := requireIdeaTx(ctx, tx, ev.IdeaID, false); err != nil {
			return err
		}
		if ev.DeletedAt == nil {
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE evidence SET deleted_at = NULL WHERE id = ?`, ev.ID); err != nil {
			return fmt.Errorf("ideas: restore evidence: %w", err)
		}
		return handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityRestored,
			EntityType: schema.EntityEvidence,
			EntityID:   ev.ID,
			Summary:    "恢复证据",
			Actor:      who,
			At:         s.clock.Now(),
		})
	})
	if err != nil {
		return err
	}
	s.notifyIndex(ctx, schema.EntityEvidence, evidenceID, schema.EventEntityRestored)
	return nil
}

// ListEvidence reads one Idea's Evidence with keyset pagination.
func (s *service) ListEvidence(ctx context.Context, q ListEvidenceQuery) (Page[EvidenceView], error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return Page[EvidenceView]{}, err
	}
	if _, err := s.ideaByIDDB(ctx, handle.DB(), q.IdeaID, true); err != nil {
		return Page[EvidenceView]{}, err
	}
	clauses := []string{"idea_id = ?"}
	args := []any{q.IdeaID}
	if q.IncludeDeleted {
		clauses = append(clauses, "deleted_at IS NOT NULL")
	} else {
		clauses = append(clauses, "deleted_at IS NULL")
	}
	if len(q.Directions) > 0 {
		values := make([]string, len(q.Directions))
		for i, d := range q.Directions {
			if !d.Valid() {
				return Page[EvidenceView]{}, serviceError(ErrInvalidInput, "direction %q must be support or oppose", string(d))
			}
			values[i] = string(d)
		}
		clauses = append(clauses, "direction IN ("+placeholders(len(values))+")")
		args = append(args, anyArgs(values)...)
	}
	if q.Confirmed != nil {
		if *q.Confirmed {
			clauses = append(clauses, "confirmed = 1")
		} else {
			clauses = append(clauses, "confirmed = 0")
		}
	}
	cursor, err := decodeCursor(q.Cursor)
	if err != nil {
		return Page[EvidenceView]{}, err
	}
	if cursor.ID != "" {
		clauses = append(clauses,
			"(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, cursor.Key, cursor.Key, cursor.ID)
	}
	where := " WHERE " + joinAnd(clauses)
	var total int
	if err := handle.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM evidence`+where, args...).Scan(&total); err != nil {
		return Page[EvidenceView]{}, fmt.Errorf("ideas: count evidence: %w", err)
	}
	limit := normalizedPageSize(q.PageSize)
	rows, err := handle.DB().QueryContext(ctx,
		`SELECT `+evidenceColumns+` FROM evidence`+where+
			` ORDER BY created_at DESC, id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return Page[EvidenceView]{}, fmt.Errorf("ideas: list evidence: %w", err)
	}
	defer rows.Close()
	evidences := make([]Evidence, 0, limit)
	for rows.Next() {
		ev, err := evidenceFrom(rows)
		if err != nil {
			return Page[EvidenceView]{}, fmt.Errorf("ideas: scan evidence: %w", err)
		}
		evidences = append(evidences, *ev)
	}
	if err := rows.Err(); err != nil {
		return Page[EvidenceView]{}, fmt.Errorf("ideas: list evidence: %w", err)
	}
	if err := rows.Close(); err != nil {
		return Page[EvidenceView]{}, fmt.Errorf("ideas: list evidence: %w", err)
	}
	items := make([]EvidenceView, 0, limit)
	for _, ev := range evidences {
		view, err := s.evidenceView(ctx, handle.DB(), ev)
		if err != nil {
			return Page[EvidenceView]{}, err
		}
		items = append(items, view)
	}
	page := Page[EvidenceView]{Items: items, Total: total}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		page.NextCursor = encodeCursor(listCursor{
			Key: clock.Format(last.CreatedAt),
			ID:  last.ID,
		})
	}
	return page, nil
}

func (s *service) evidenceView(ctx context.Context, q querier, ev Evidence) (EvidenceView, error) {
	info, err := s.resolveTarget(ctx, q, string(ev.Source.Type), ev.Source.ID)
	if err != nil {
		return EvidenceView{}, err
	}
	return EvidenceView{
		Evidence:      ev,
		SourceTitle:   info.Title,
		SourceDeleted: info.Deleted,
		SourceBroken:  info.Broken,
	}, nil
}

func (s *service) evidenceByID(ctx context.Context, id string) (*Evidence, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	ev, err := evidenceFrom(handle.DB().QueryRowContext(ctx,
		`SELECT `+evidenceColumns+` FROM evidence WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, serviceError(ErrEvidenceNotFound, "evidence %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("ideas: read evidence %s: %w", id, err)
	}
	return ev, nil
}

func (s *service) evidenceByIDTx(ctx context.Context, tx *sql.Tx, id string) (*Evidence, error) {
	ev, err := evidenceFrom(tx.QueryRowContext(ctx,
		`SELECT `+evidenceColumns+` FROM evidence WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, serviceError(ErrEvidenceNotFound, "evidence %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("ideas: read evidence %s: %w", id, err)
	}
	return ev, nil
}

func joinAnd(clauses []string) string {
	out := ""
	for i, c := range clauses {
		if i > 0 {
			out += " AND "
		}
		out += c
	}
	return out
}
