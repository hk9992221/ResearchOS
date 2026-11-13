package project

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
	"researchos/internal/ports"
)

// ActivityQuery filters the Activity page (§5.5, PRJ-06). From/To default to
// the last 30 days. Cursor is opaque: base64(JSON{offset,last id}).
type ActivityQuery struct {
	From        *time.Time
	To          *time.Time
	EntityTypes []string
	EventTypes  []string
	Actors      []ports.Actor
	Cursor      string
	Limit       int
}

// ActivityPage is one page of the activity timeline.
type ActivityPage struct {
	Items      []ActivityItem
	NextCursor string
}

// ActivityItem is one timeline entry with its navigation target.
type ActivityItem struct {
	ID         string
	EventType  string
	EntityType string
	EntityID   string
	Summary    string
	Actor      ports.Actor
	CreatedAt  time.Time
	EntityGone bool
	Route      ports.Route
}

const (
	activityDefaultLimit = 50
	activityMaxLimit     = 200
	activityDefaultDays  = 30
)

// Activity reads the project activity timeline. It never writes an event.
func (s *Service) Activity(ctx context.Context, projectID string, q ActivityQuery) (*ActivityPage, error) {
	p, err := s.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if s.ws == nil {
		return nil, errNoWorkspace
	}
	h, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("project: activity %s: %w", projectID, err)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = activityDefaultLimit
	}
	if limit > activityMaxLimit {
		limit = activityMaxLimit
	}

	base := activityEventsQuery(q, s.clock.Now())
	offset := 0
	if cur, ok := decodeActivityCursor(q.Cursor); ok {
		if cur.Offset > 0 {
			// Validate that the item before the next offset is still the last
			// item the client saw. A deleted/shifted window resets to page 1
			// instead of silently skipping or duplicating rows.
			probe := base
			probe.Limit = 1
			probe.Offset = cur.Offset - 1
			got, err := h.Reader().List(ctx, probe)
			if err != nil {
				return nil, fmt.Errorf("project: activity %s: %w", projectID, err)
			}
			if len(got) == 1 && got[0].ID == cur.LastID {
				offset = cur.Offset
			}
		}
	}

	page := base
	page.Limit = limit + 1
	page.Offset = offset
	rows, err := h.Reader().List(ctx, page)
	if err != nil {
		return nil, fmt.Errorf("project: activity %s: %w", projectID, err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	trashed, err := s.trashedSet(ctx, h.DB())
	if err != nil {
		return nil, fmt.Errorf("project: activity %s: %w", projectID, err)
	}
	archived := p.Status == StatusArchived
	items := make([]ActivityItem, 0, len(rows))
	for _, row := range rows {
		entityType := string(row.EntityType)
		gone := archived || trashed[entityType+"\x00"+row.EntityID]
		item := ActivityItem{
			ID:         row.ID,
			EventType:  string(row.Type),
			EntityType: entityType,
			EntityID:   row.EntityID,
			Summary:    row.Payload.Summary,
			Actor:      row.Actor,
			CreatedAt:  row.CreatedAt,
			EntityGone: gone,
			Route:      activityRoute(projectID, entityType, row.EntityID, trashed[entityType+"\x00"+row.EntityID]),
		}
		items = append(items, item)
	}

	out := &ActivityPage{Items: items}
	if hasMore && len(items) > 0 {
		out.NextCursor = encodeActivityCursor(activityCursor{
			Offset: offset + limit,
			LastID: items[len(items)-1].ID,
		})
	}
	return out, nil
}

// activityEventsQuery applies the time window and the filters.
func activityEventsQuery(q ActivityQuery, now time.Time) events.Query {
	to := now
	if q.To != nil {
		to = *q.To
	}
	from := to.AddDate(0, 0, -activityDefaultDays)
	if q.From != nil {
		from = *q.From
	}
	query := events.Query{
		Since: &from,
		Until: &to,
		Limit: q.Limit,
	}
	for _, t := range q.EntityTypes {
		query.EntityTypes = append(query.EntityTypes, events.EntityType(t))
	}
	for _, t := range q.EventTypes {
		query.EventTypes = append(query.EventTypes, events.Type(t))
	}
	for _, a := range q.Actors {
		query.Actors = append(query.Actors, events.Actor(a))
	}
	return query
}

// trashedSet loads v_trash once per page.
func (s *Service) trashedSet(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT entity_type, id FROM v_trash`)
	if err != nil {
		return nil, fmt.Errorf("trash lookup: %w", err)
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var entityType, id string
		if err := rows.Scan(&entityType, &id); err != nil {
			return nil, err
		}
		out[entityType+"\x00"+id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// activityCursor is the opaque pagination state.
type activityCursor struct {
	Offset int    `json:"o"`
	LastID string `json:"l"`
}

func encodeActivityCursor(c activityCursor) string {
	body, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(body)
}

func decodeActivityCursor(raw string) (activityCursor, bool) {
	if raw == "" {
		return activityCursor{}, false
	}
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return activityCursor{}, false
	}
	var c activityCursor
	if err := json.Unmarshal(body, &c); err != nil {
		return activityCursor{}, false
	}
	if c.Offset < 0 {
		return activityCursor{}, false
	}
	return c, true
}

// activityRoute maps an entity type to its UI route. A soft-deleted target
// points at the trash with the entity type as a view parameter (§6.3).
func activityRoute(projectID, entityType, entityID string, deleted bool) ports.Route {
	if deleted {
		return ports.Route{
			Kind:      ports.RouteTrash,
			ProjectID: projectID,
			EntityID:  entityID,
			Params:    map[string]string{"entity_type": entityType},
		}
	}
	kind := ports.RouteProject
	switch schema.EntityType(entityType) {
	case schema.EntityNote:
		kind = ports.RouteNote
	case schema.EntityIdea:
		kind = ports.RouteIdea
	case schema.EntityExperiment:
		kind = ports.RouteExperiment
	case schema.EntityRun:
		kind = ports.RouteRun
	case schema.EntityTask:
		kind = ports.RouteTask
	case schema.EntityPaper:
		kind = ports.RoutePaper
	case schema.EntityWeeklyReport:
		kind = ports.RouteWeeklyReport
	}
	return ports.Route{Kind: kind, ProjectID: projectID, EntityID: entityID}
}
