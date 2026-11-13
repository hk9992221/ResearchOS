package ideas

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
)

// listCursor is the opaque keyset cursor shared by list queries.
type listCursor struct {
	Sort string `json:"s"`
	Key  string `json:"k"`
	ID   string `json:"i"`
}

func encodeCursor(c listCursor) string {
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(raw string) (listCursor, error) {
	if raw == "" {
		return listCursor{}, nil
	}
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return listCursor{}, serviceError(ErrInvalidInput, "invalid cursor")
	}
	var c listCursor
	if err := json.Unmarshal(body, &c); err != nil {
		return listCursor{}, serviceError(ErrInvalidInput, "invalid cursor")
	}
	return c, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// CreateIdea authors a new Idea in status New (IDEA-01).
func (s *service) CreateIdea(ctx context.Context, in CreateIdeaInput) (*Idea, error) {
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
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ideas(id, title, description, status, rejection_reason, created_at, updated_at, deleted_at)
			 VALUES (?, ?, ?, ?, '', ?, ?, NULL)`,
			id, title, in.DescriptionMD, string(StatusNew), now, now); err != nil {
			return fmt.Errorf("ideas: create: %w", err)
		}
		return handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityCreated,
			EntityType: schema.EntityIdea,
			EntityID:   id,
			Summary:    "创建 Idea：" + title,
			Actor:      actor,
			At:         s.clock.Now(),
		})
	})
	if err != nil {
		return nil, err
	}
	s.notifyIndex(ctx, schema.EntityIdea, id, schema.EventEntityCreated)
	return s.ideaByID(ctx, id, true)
}

// UpdateIdea edits title/description. It writes no Activity event (SPEC-06 A6)
// and only moves updated_at.
func (s *service) UpdateIdea(ctx context.Context, in UpdateIdeaInput) (*Idea, error) {
	if _, err := writeActor(in.Actor); err != nil {
		return nil, err
	}
	handle, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	var (
		title   *string
		desc    *string
		updated *Idea
	)
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		idea, err := requireIdeaTx(ctx, tx, in.ID, false)
		if err != nil {
			return err
		}
		if in.Title != nil {
			n, err := normalizeTitle(*in.Title)
			if err != nil {
				return err
			}
			title = &n
		}
		if in.DescriptionMD != nil {
			if err := validateDescription(*in.DescriptionMD); err != nil {
				return err
			}
			desc = in.DescriptionMD
		}
		if title == nil && desc == nil {
			updated = idea
			return nil
		}
		now := s.now()
		if _, err := tx.ExecContext(ctx,
			`UPDATE ideas SET
			   title = COALESCE(?, title),
			   description = COALESCE(?, description),
			   updated_at = ?
			 WHERE id = ?`,
			title, desc, now, idea.ID); err != nil {
			return fmt.Errorf("ideas: update: %w", err)
		}
		updated, err = s.ideaByIDTx(ctx, tx, idea.ID, false)
		return err
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// GetIdea reads one Idea, including a soft-deleted one (trash detail).
func (s *service) GetIdea(ctx context.Context, ideaID string) (*IdeaDetail, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	idea, err := s.ideaByID(ctx, ideaID, true)
	if err != nil {
		return nil, err
	}
	counts, err := evidenceCounts(ctx, handle.DB(), ideaID)
	if err != nil {
		return nil, err
	}
	relations, err := s.ListRelations(ctx, ideaID)
	if err != nil {
		return nil, err
	}
	history, err := s.History(ctx, HistoryQuery{IdeaID: ideaID, PageSize: 5})
	if err != nil {
		return nil, err
	}
	return &IdeaDetail{
		Idea:         *idea,
		Evidence:     counts,
		Relations:    relations,
		HistoryBrief: history.Items,
	}, nil
}

// ideaByID reads one Idea outside a transaction.
func (s *service) ideaByID(ctx context.Context, id string, includeDeleted bool) (*Idea, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	return s.ideaByIDDB(ctx, handle.DB(), id, includeDeleted)
}

func (s *service) ideaByIDDB(ctx context.Context, db *sql.DB, id string, includeDeleted bool) (*Idea, error) {
	var row *sql.Row
	if includeDeleted {
		row = db.QueryRowContext(ctx, `SELECT `+ideaColumns+` FROM ideas WHERE id = ?`, id)
	} else {
		row = db.QueryRowContext(ctx, `SELECT `+ideaColumns+` FROM ideas WHERE id = ? AND deleted_at IS NULL`, id)
	}
	idea, err := ideaFrom(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, serviceError(ErrIdeaNotFound, "idea %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("ideas: read idea %s: %w", id, err)
	}
	return idea, nil
}

func (s *service) ideaByIDTx(ctx context.Context, tx *sql.Tx, id string, includeDeleted bool) (*Idea, error) {
	query := `SELECT ` + ideaColumns + ` FROM ideas WHERE id = ?`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	idea, err := ideaFrom(tx.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, serviceError(ErrIdeaNotFound, "idea %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("ideas: read idea %s: %w", id, err)
	}
	return idea, nil
}

// ListIdeas reads the live list or the trash list with keyset pagination.
func (s *service) ListIdeas(ctx context.Context, q ListIdeasQuery) (Page[IdeaSummary], error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return Page[IdeaSummary]{}, err
	}
	sort := q.Sort
	if sort == "" {
		sort = SortUpdatedDesc
	}
	var orderBy, keyExpr, cmp string
	switch sort {
	case SortUpdatedDesc:
		orderBy, keyExpr, cmp = "i.updated_at DESC, i.id DESC", "i.updated_at", "<"
	case SortCreatedDesc:
		orderBy, keyExpr, cmp = "i.created_at DESC, i.id DESC", "i.created_at", "<"
	case SortTitleAsc:
		orderBy, keyExpr, cmp = "i.title ASC, i.id ASC", "i.title", ">"
	default:
		return Page[IdeaSummary]{}, serviceError(ErrInvalidInput, "unknown sort %q", sort)
	}

	clauses := []string{}
	var args []any
	if q.IncludeDeleted {
		clauses = append(clauses, "i.deleted_at IS NOT NULL")
	} else {
		clauses = append(clauses, "i.deleted_at IS NULL")
	}
	if len(q.Statuses) > 0 {
		values := make([]string, len(q.Statuses))
		for i, st := range q.Statuses {
			if !st.Valid() {
				return Page[IdeaSummary]{}, serviceError(ErrInvalidStatus, "status %q is not registered", string(st))
			}
			values[i] = string(st)
		}
		clauses = append(clauses, "i.status IN ("+placeholders(len(values))+")")
		args = append(args, anyArgs(values)...)
	}
	if text := strings.TrimSpace(q.Text); text != "" {
		pattern := "%" + escapeLike(text) + "%"
		clauses = append(clauses, `(i.title LIKE ? ESCAPE '\' OR i.description LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern)
	}
	cursor, err := decodeCursor(q.Cursor)
	if err != nil {
		return Page[IdeaSummary]{}, err
	}
	if cursor.Sort != "" && cursor.Sort != sort {
		return Page[IdeaSummary]{}, serviceError(ErrInvalidInput, "cursor belongs to another sort")
	}
	if cursor.ID != "" {
		clauses = append(clauses,
			fmt.Sprintf("(%s %s ? OR (%s = ? AND i.id %s ?))", keyExpr, cmp, keyExpr, cmp))
		args = append(args, cursor.Key, cursor.Key, cursor.ID)
	}
	where := " WHERE " + strings.Join(clauses, " AND ")

	var total int
	if err := handle.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM ideas i`+where, args...).Scan(&total); err != nil {
		return Page[IdeaSummary]{}, fmt.Errorf("ideas: count: %w", err)
	}

	limit := normalizedPageSize(q.PageSize)
	query := `SELECT i.id, i.title, i.description, i.status, i.rejection_reason,
	                 i.created_at, i.updated_at, i.deleted_at,
	                 (SELECT count(*) FROM evidence e WHERE e.idea_id = i.id AND e.direction = 'support'
	                    AND e.confirmed = 1 AND e.deleted_at IS NULL),
	                 (SELECT count(*) FROM evidence e WHERE e.idea_id = i.id AND e.direction = 'oppose'
	                    AND e.confirmed = 1 AND e.deleted_at IS NULL),
	                 (SELECT count(*) FROM evidence e WHERE e.idea_id = i.id AND e.ai_generated = 1
	                    AND e.confirmed = 0 AND e.deleted_at IS NULL),
	                 (SELECT count(*) FROM entity_relations r WHERE r.src_type = 'idea' AND r.src_id = i.id
	                    AND r.relation = 'related_to' AND r.dst_type = 'paper'),
	                 (SELECT count(*) FROM entity_relations r WHERE r.src_type = 'idea' AND r.src_id = i.id
	                    AND r.relation = 'related_to' AND r.dst_type = 'note'),
	                 (SELECT count(*) FROM entity_relations r WHERE r.src_type = 'experiment'
	                    AND r.relation = 'validates' AND r.dst_type = 'idea' AND r.dst_id = i.id),
	                 COALESCE((SELECT r.dst_id FROM entity_relations r WHERE r.src_type = 'idea' AND r.src_id = i.id
	                    AND r.relation = 'derived_from' ORDER BY r.created_at, r.id LIMIT 1), '')
	          FROM ideas i` + where + ` ORDER BY ` + orderBy + ` LIMIT ?`
	rows, err := handle.DB().QueryContext(ctx, query, append(args, limit+1)...)
	if err != nil {
		return Page[IdeaSummary]{}, fmt.Errorf("ideas: list: %w", err)
	}
	defer rows.Close()
	items := make([]IdeaSummary, 0, limit)
	var next listCursor
	for rows.Next() {
		var (
			summary IdeaSummary
			status  string
			created string
			updated string
			deleted sql.NullString
		)
		if err := rows.Scan(&summary.ID, &summary.Title, &summary.DescriptionMD, &status,
			&summary.RejectionReason, &created, &updated, &deleted,
			&summary.SupportCount, &summary.OpposeCount, &summary.DraftCount,
			&summary.LinkedPapers, &summary.LinkedNotes, &summary.LinkedExps,
			&summary.DerivedFromID); err != nil {
			return Page[IdeaSummary]{}, fmt.Errorf("ideas: scan list: %w", err)
		}
		summary.Status = Status(status)
		if summary.CreatedAt, err = clock.Parse(created); err != nil {
			return Page[IdeaSummary]{}, fmt.Errorf("ideas: list created_at: %w", err)
		}
		if summary.UpdatedAt, err = clock.Parse(updated); err != nil {
			return Page[IdeaSummary]{}, fmt.Errorf("ideas: list updated_at: %w", err)
		}
		if deleted.Valid {
			t, err := clock.Parse(deleted.String)
			if err != nil {
				return Page[IdeaSummary]{}, fmt.Errorf("ideas: list deleted_at: %w", err)
			}
			summary.DeletedAt = &t
		}
		items = append(items, summary)
	}
	if err := rows.Err(); err != nil {
		return Page[IdeaSummary]{}, fmt.Errorf("ideas: list: %w", err)
	}
	page := Page[IdeaSummary]{Items: items, Total: total}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		key := clock.Format(last.UpdatedAt)
		switch sort {
		case SortCreatedDesc:
			key = clock.Format(last.CreatedAt)
		case SortTitleAsc:
			key = last.Title
		}
		next = listCursor{Sort: sort, Key: key, ID: last.ID}
		page.NextCursor = encodeCursor(next)
	}
	return page, nil
}

func normalizedPageSize(n int) int {
	switch {
	case n <= 0:
		return 50
	case n > 200:
		return 200
	default:
		return n
	}
}

// ChangeStatus applies an explicit user/MCP status change (IDEA-03/08).
func (s *service) ChangeStatus(ctx context.Context, in ChangeStatusInput) (*Idea, error) {
	actor, err := writeActor(in.Actor)
	if err != nil {
		return nil, err
	}
	if !in.To.Valid() {
		return nil, serviceError(ErrInvalidStatus, "status %q is not registered", string(in.To))
	}
	handle, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	var updated *Idea
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		idea, err := requireIdeaTx(ctx, tx, in.ID, false)
		if err != nil {
			return err
		}
		if idea.Status == in.To {
			updated = idea
			return nil
		}
		reason := idea.RejectionReason
		if in.To == StatusRejected {
			reason, err = normalizeReason(in.RejectionReason)
			if err != nil {
				return err
			}
		}
		now := s.now()
		if _, err := tx.ExecContext(ctx,
			`UPDATE ideas SET status = ?, rejection_reason = ?, updated_at = ? WHERE id = ?`,
			string(in.To), reason, now, idea.ID); err != nil {
			return fmt.Errorf("ideas: change status: %w", err)
		}
		old := idea.Status
		summary := old.DisplayName() + " → " + in.To.DisplayName()
		if in.To == StatusRejected {
			summary += "：" + reason
		}
		oldObj := events.Object{"status": string(old)}
		newObj := events.Object{"status": string(in.To)}
		if err := handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventStatusChanged,
			EntityType: schema.EntityIdea,
			EntityID:   idea.ID,
			Old:        &oldObj,
			New:        &newObj,
			Summary:    summary,
			Actor:      actor,
			At:         s.clock.Now(),
		}); err != nil {
			return err
		}
		updated, err = s.ideaByIDTx(ctx, tx, idea.ID, false)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.notifyIndex(ctx, schema.EntityIdea, updated.ID, schema.EventStatusChanged)
	return updated, nil
}

// UpdateRejectionReason edits the reason of a Rejected Idea without changing
// its status (SPEC-06 §5.3, A5 degradation).
func (s *service) UpdateRejectionReason(ctx context.Context, in UpdateRejectionReasonInput) (*Idea, error) {
	actor, err := writeActor(in.Actor)
	if err != nil {
		return nil, err
	}
	reason, err := normalizeReason(in.Reason)
	if err != nil {
		return nil, err
	}
	handle, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	var updated *Idea
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		idea, err := requireIdeaTx(ctx, tx, in.ID, false)
		if err != nil {
			return err
		}
		if idea.Status != StatusRejected {
			return serviceError(ErrNotRejected, "idea %s is not rejected", idea.ID)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE ideas SET rejection_reason = ?, updated_at = ? WHERE id = ?`,
			reason, s.now(), idea.ID); err != nil {
			return fmt.Errorf("ideas: update rejection reason: %w", err)
		}
		oldObj := events.Object{"status": string(StatusRejected)}
		newObj := events.Object{"status": string(StatusRejected)}
		if err := handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventStatusChanged,
			EntityType: schema.EntityIdea,
			EntityID:   idea.ID,
			Old:        &oldObj,
			New:        &newObj,
			Summary:    "更新否定原因：" + reason,
			Actor:      actor,
			At:         s.clock.Now(),
		}); err != nil {
			return err
		}
		updated, err = s.ideaByIDTx(ctx, tx, idea.ID, false)
		return err
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// DeleteIdea soft-deletes the Idea. Evidence and relations are untouched
// (IDEA-09, AC-08).
func (s *service) DeleteIdea(ctx context.Context, ideaID string, actor Actor) error {
	who, err := writeActor(actor)
	if err != nil {
		return err
	}
	handle, err := s.begin(ctx)
	if err != nil {
		return err
	}
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		idea, err := requireIdeaTx(ctx, tx, ideaID, true)
		if err != nil {
			return err
		}
		if idea.DeletedAt != nil {
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE ideas SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`,
			s.now(), idea.ID); err != nil {
			return fmt.Errorf("ideas: delete: %w", err)
		}
		return handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityDeleted,
			EntityType: schema.EntityIdea,
			EntityID:   idea.ID,
			Summary:    "删除 Idea：" + idea.Title,
			Actor:      who,
			At:         s.clock.Now(),
		})
	})
	if err != nil {
		return err
	}
	s.notifyIndex(ctx, schema.EntityIdea, ideaID, schema.EventEntityDeleted)
	return nil
}

// RestoreIdea clears deleted_at and emits entity.restored.
func (s *service) RestoreIdea(ctx context.Context, ideaID string, actor Actor) error {
	who, err := writeActor(actor)
	if err != nil {
		return err
	}
	handle, err := s.begin(ctx)
	if err != nil {
		return err
	}
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		idea, err := requireIdeaTx(ctx, tx, ideaID, true)
		if err != nil {
			return err
		}
		if idea.DeletedAt == nil {
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE ideas SET deleted_at = NULL WHERE id = ?`, idea.ID); err != nil {
			return fmt.Errorf("ideas: restore: %w", err)
		}
		return handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityRestored,
			EntityType: schema.EntityIdea,
			EntityID:   idea.ID,
			Summary:    "恢复 Idea：" + idea.Title,
			Actor:      who,
			At:         s.clock.Now(),
		})
	})
	if err != nil {
		return err
	}
	s.notifyIndex(ctx, schema.EntityIdea, ideaID, schema.EventEntityRestored)
	return nil
}

// evidenceCounts returns the confirmed support/oppose and draft counts.
func evidenceCounts(ctx context.Context, db *sql.DB, ideaID string) (EvidenceSummary, error) {
	var out EvidenceSummary
	if err := db.QueryRowContext(ctx,
		`SELECT
		   COALESCE(sum(CASE WHEN direction = 'support' AND confirmed = 1 AND deleted_at IS NULL THEN 1 ELSE 0 END), 0),
		   COALESCE(sum(CASE WHEN direction = 'oppose'  AND confirmed = 1 AND deleted_at IS NULL THEN 1 ELSE 0 END), 0),
		   COALESCE(sum(CASE WHEN ai_generated = 1 AND confirmed = 0 AND deleted_at IS NULL THEN 1 ELSE 0 END), 0)
		 FROM evidence WHERE idea_id = ?`, ideaID).
		Scan(&out.Support, &out.Oppose, &out.Drafts); err != nil {
		return out, fmt.Errorf("ideas: evidence counts: %w", err)
	}
	return out, nil
}
