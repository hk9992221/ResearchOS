package ideas

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
)

// History merges the Idea's own events, its Evidence events and the synthesized
// derived-Idea entries, newest first (SPEC-06 §5.8). It is read-only and never
// writes.
func (s *service) History(ctx context.Context, q HistoryQuery) (Page[HistoryEntry], error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return Page[HistoryEntry]{}, err
	}
	idea, err := s.ideaByIDDB(ctx, handle.DB(), q.IdeaID, true)
	if err != nil {
		return Page[HistoryEntry]{}, err
	}
	entries, err := s.historyEntries(ctx, handle, idea)
	if err != nil {
		return Page[HistoryEntry]{}, err
	}
	if len(q.Kinds) > 0 {
		wanted := make(map[HistoryKind]bool, len(q.Kinds))
		for _, k := range q.Kinds {
			wanted[k] = true
		}
		filtered := entries[:0]
		for _, e := range entries {
			if wanted[e.Kind] {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].At.Equal(entries[j].At) {
			return entries[i].At.After(entries[j].At)
		}
		return entryID(entries[i]) > entryID(entries[j])
	})
	total := len(entries)
	if q.Cursor != "" {
		cursor, err := decodeCursor(q.Cursor)
		if err != nil {
			return Page[HistoryEntry]{}, err
		}
		at, err := clock.Parse(cursor.Key)
		if err != nil {
			return Page[HistoryEntry]{}, serviceError(ErrInvalidInput, "invalid cursor")
		}
		start := 0
		for start < len(entries) {
			e := entries[start]
			if e.At.Before(at) || (e.At.Equal(at) && entryID(e) < cursor.ID) {
				break
			}
			start++
		}
		entries = entries[start:]
	}
	limit := normalizedPageSize(q.PageSize)
	page := Page[HistoryEntry]{Total: total}
	if len(entries) > limit {
		page.Items = entries[:limit]
		last := page.Items[limit-1]
		page.NextCursor = encodeCursor(listCursor{Key: clock.Format(last.At), ID: entryID(last)})
	} else {
		page.Items = entries
	}
	return page, nil
}

func entryID(e HistoryEntry) string {
	if e.ActivityID != "" {
		return e.ActivityID
	}
	if e.Synthesized && e.Ref != nil {
		return "derived:" + e.Ref.ID
	}
	if e.Ref != nil {
		return e.Ref.ID
	}
	return ""
}

// historyEntries loads and classifies every candidate entry.
func (s *service) historyEntries(ctx context.Context, handle *workspace.ProjectHandle,
	idea *Idea) ([]HistoryEntry, error) {
	rows, err := handle.DB().QueryContext(ctx,
		`SELECT id, event_type, entity_type, entity_id, payload, actor, created_at
		 FROM activities
		 WHERE (entity_type = 'idea' AND entity_id = ?)
		    OR (entity_type = 'evidence' AND entity_id IN (SELECT id FROM evidence WHERE idea_id = ?))`,
		idea.ID, idea.ID)
	if err != nil {
		return nil, fmt.Errorf("ideas: history activities: %w", err)
	}
	defer rows.Close()
	ideaRef := &EntityRef{Type: "idea", ID: idea.ID, Title: idea.Title, Deleted: idea.DeletedAt != nil}
	var out []HistoryEntry
	for rows.Next() {
		var (
			id         string
			eventType  string
			entityType string
			entityID   string
			payloadRaw string
			actor      string
			createdAt  string
		)
		if err := rows.Scan(&id, &eventType, &entityType, &entityID, &payloadRaw, &actor, &createdAt); err != nil {
			return nil, fmt.Errorf("ideas: scan history: %w", err)
		}
		var payload events.Payload
		if err := json.Unmarshal([]byte(payloadRaw), &payload); err != nil {
			return nil, fmt.Errorf("ideas: history payload: %w", err)
		}
		at, err := clock.Parse(createdAt)
		if err != nil {
			return nil, fmt.Errorf("ideas: history created_at: %w", err)
		}
		entry := HistoryEntry{
			At:         at,
			ActivityID: id,
			Kind:       classify(eventType, entityType),
			EventType:  eventType,
			Summary:    payload.Summary,
			Actor:      events.Actor(actor),
			Old:        statusValue(payload.Old),
			New:        statusValue(payload.New),
		}
		if schema.EntityType(entityType) == schema.EntityIdea {
			entry.Ref = ideaRef
		} else if schema.EntityType(entityType) == schema.EntityEvidence {
			entry.Ref = &EntityRef{Type: "evidence", ID: entityID}
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ideas: history: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("ideas: history: %w", err)
	}
	derived, err := s.synthesizedDerived(ctx, handle.DB(), idea.ID)
	if err != nil {
		return nil, err
	}
	return append(out, derived...), nil
}

// synthesizedDerived builds the "衍生出 Idea" entries that have no event of
// their own (SPEC-06 §5.8 rule 3).
func (s *service) synthesizedDerived(ctx context.Context, db *sql.DB, ideaID string) ([]HistoryEntry, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT i.id, i.title, i.deleted_at, i.created_at
		 FROM entity_relations r
		 JOIN ideas i ON i.id = r.src_id
		 WHERE r.src_type = 'idea' AND r.relation = 'derived_from'
		   AND r.dst_type = 'idea' AND r.dst_id = ?
		 ORDER BY i.created_at, i.id`, ideaID)
	if err != nil {
		return nil, fmt.Errorf("ideas: history derived: %w", err)
	}
	defer rows.Close()
	var out []HistoryEntry
	for rows.Next() {
		var (
			childID    string
			title      string
			deletedAt  sql.NullString
			createdRaw string
		)
		if err := rows.Scan(&childID, &title, &deletedAt, &createdRaw); err != nil {
			return nil, fmt.Errorf("ideas: scan derived history: %w", err)
		}
		at, err := clock.Parse(createdRaw)
		if err != nil {
			return nil, fmt.Errorf("ideas: derived created_at: %w", err)
		}
		out = append(out, HistoryEntry{
			At:          at,
			Kind:        HistoryDerived,
			Summary:     "衍生出 Idea：" + title,
			Ref:         &EntityRef{Type: "idea", ID: childID, Title: title, Deleted: deletedAt.Valid},
			Synthesized: true,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ideas: history derived: %w", err)
	}
	return out, nil
}

func classify(eventType, entityType string) HistoryKind {
	if schema.EntityType(entityType) == schema.EntityEvidence {
		return HistoryEvidence
	}
	switch schema.EventType(eventType) {
	case schema.EventEntityCreated:
		return HistoryCreated
	case schema.EventStatusChanged:
		return HistoryStatus
	case schema.EventRelationAdded, schema.EventRelationRemoved:
		return HistoryRelation
	case schema.EventEntityDeleted:
		return HistoryDeleted
	case schema.EventEntityRestored:
		return HistoryRestored
	case schema.EventAIAnalysisCompleted:
		return HistoryAI
	default:
		return HistoryCreated
	}
}

func statusValue(obj *events.Object) string {
	if obj == nil {
		return ""
	}
	if v, ok := (*obj)["status"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
