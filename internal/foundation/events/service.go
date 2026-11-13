package events

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
)

const (
	maxSummaryRunes = 200
	defaultLimit    = 50
	maxLimit        = 500
	dayLayout       = "2006-01-02"
	mergeKeyPrefix  = "note.updated:"
)

// Options configures a Service.
type Options struct {
	// Clock supplies event timestamps and IDs. Nil means clock.System.
	Clock clock.Clock
	// Location defines the natural-day boundary for note.updated merging and
	// day aggregation. Nil means time.Local; tests pin a fixed zone.
	Location *time.Location
}

// Service implements Writer and Reader over one project database.
type Service struct {
	db    *sql.DB
	clock clock.Clock
	loc   *time.Location
}

var (
	_ Writer = (*Service)(nil)
	_ Reader = (*Service)(nil)
)

// New returns the events service for the project database db.
func New(db *sql.DB, opt Options) *Service {
	c := opt.Clock
	if c == nil {
		c = clock.System{}
	}
	loc := opt.Location
	if loc == nil {
		loc = time.Local
	}
	return &Service{db: db, clock: c, loc: loc}
}

// ParseObject decodes raw JSON into an old/new Object. Arrays, scalars, null
// and trailing data are rejected with ErrBadPayloadShape.
func ParseObject(raw []byte) (*Object, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, shapeError("old/new must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var obj Object
	if err := dec.Decode(&obj); err != nil {
		return nil, shapeError("old/new: %v", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, shapeError("old/new: trailing data after the object")
	}
	return &obj, nil
}

// Emit validates ev and writes it inside the caller transaction.
func (s *Service) Emit(ctx context.Context, tx *sql.Tx, ev Event) error {
	if tx == nil {
		return errors.New("events: Emit requires a caller transaction (use EmitStandalone otherwise)")
	}
	prepared, err := s.prepare(ev)
	if err != nil {
		return err
	}
	return s.insert(ctx, tx, prepared)
}

// EmitStandalone validates ev and writes it in its own transaction.
func (s *Service) EmitStandalone(ctx context.Context, ev Event) error {
	prepared, err := s.prepare(ev)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("events: begin tx: %w", err)
	}
	if err := s.insert(ctx, tx, prepared); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("events: commit: %w", err)
	}
	return nil
}

// EmitNoteUpdated writes or merges the note.updated row for the local day of
// ev.At.
func (s *Service) EmitNoteUpdated(ctx context.Context, tx *sql.Tx, ev NoteUpdatedEvent) error {
	if tx == nil {
		return errors.New("events: EmitNoteUpdated requires a caller transaction")
	}
	if !ids.Valid(ev.NoteID) {
		return fmt.Errorf("%w: %q", errs.ErrBadEntityID, ev.NoteID)
	}
	if !ev.Actor.Valid() {
		return fmt.Errorf("%w: %q", errs.ErrBadActor, ev.Actor)
	}
	at := ev.At
	if at.IsZero() {
		at = s.clock.Now()
	}
	id, err := ids.New(s.clock)
	if err != nil {
		return fmt.Errorf("events: generate id: %w", err)
	}
	payload := Payload{
		EntityType:  schema.EntityNote,
		EntityID:    ev.NoteID,
		Summary:     truncateSummary(ev.Summary),
		Actor:       ev.Actor,
		SaveCount:   1,
		LastSavedAt: clock.Format(at),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return shapeError("note.updated payload is not serializable: %v", err)
	}
	mergeKey := mergeKeyPrefix + ev.NoteID + ":" + s.dayKey(at)
	if _, err := tx.ExecContext(ctx, noteUpdatedUpsert,
		id, string(schema.EventNoteUpdated), string(schema.EntityNote), ev.NoteID,
		string(body), string(ev.Actor), mergeKey, clock.Format(at),
	); err != nil {
		return fmt.Errorf("events: emit note.updated: %w", err)
	}
	return nil
}

// List returns activities matching q, newest first.
func (s *Service) List(ctx context.Context, q Query) ([]Activity, error) {
	where, args := s.where(q)
	query := `SELECT id, event_type, entity_type, entity_id, payload, actor, created_at
	          FROM activities` + where + ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, normalizedLimit(q.Limit), normalizedOffset(q.Offset))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("events: list: %w", err)
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		activity, err := s.scanActivity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, activity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: list: %w", err)
	}
	return out, nil
}

// Count returns the number of activities matching q (limit and offset are
// ignored).
func (s *Service) Count(ctx context.Context, q Query) (int, error) {
	where, args := s.where(q)
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM activities`+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("events: count: %w", err)
	}
	return n, nil
}

// Aggregate groups the full filtered set (limit and offset are ignored).
func (s *Service) Aggregate(ctx context.Context, aq AggregateQuery) ([]Bucket, error) {
	where, args := s.where(aq.Query)
	switch aq.Bucket {
	case BucketDay:
		return s.aggregateDay(ctx, where, args)
	case BucketEntityType, BucketEventType, BucketActor:
		return s.aggregateColumn(ctx, string(aq.Bucket), where, args)
	default:
		return nil, fmt.Errorf("events: unknown aggregate bucket %q", aq.Bucket)
	}
}

func (s *Service) aggregateDay(ctx context.Context, where string, args []any) ([]Bucket, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT created_at FROM activities`+where, args...)
	if err != nil {
		return nil, fmt.Errorf("events: aggregate day: %w", err)
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("events: aggregate day: %w", err)
		}
		at, err := clock.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("events: aggregate day: %w", err)
		}
		counts[at.In(s.loc).Format(dayLayout)]++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: aggregate day: %w", err)
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]Bucket, 0, len(keys))
	for _, key := range keys {
		out = append(out, Bucket{Key: key, Count: counts[key]})
	}
	return out, nil
}

func (s *Service) aggregateColumn(ctx context.Context, column, where string, args []any) ([]Bucket, error) {
	query := `SELECT ` + column + `, count(*) FROM activities` + where +
		` GROUP BY ` + column + ` ORDER BY count(*) DESC, ` + column + ` ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("events: aggregate %s: %w", column, err)
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(&b.Key, &b.Count); err != nil {
			return nil, fmt.Errorf("events: aggregate %s: %w", column, err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: aggregate %s: %w", column, err)
	}
	return out, nil
}

// preparedEvent is a fully validated row ready to insert.
type preparedEvent struct {
	id         string
	typ        Type
	entityType EntityType
	entityID   string
	payload    []byte
	actor      Actor
	at         time.Time
}

func (s *Service) prepare(ev Event) (preparedEvent, error) {
	if ev.Type == schema.EventNoteUpdated {
		return preparedEvent{}, fmt.Errorf("%w: note.updated must be emitted with EmitNoteUpdated", errs.ErrWrongEmitPath)
	}
	if !ev.Type.Valid() {
		return preparedEvent{}, fmt.Errorf("%w: %q", errs.ErrBadEventType, ev.Type)
	}
	if !ev.Actor.Valid() {
		return preparedEvent{}, fmt.Errorf("%w: %q", errs.ErrBadActor, ev.Actor)
	}
	if !ev.EntityType.Valid() {
		return preparedEvent{}, fmt.Errorf("%w: %q", errs.ErrBadEntityType, ev.EntityType)
	}
	if !ids.Valid(ev.EntityID) {
		return preparedEvent{}, fmt.Errorf("%w: %q", errs.ErrBadEntityID, ev.EntityID)
	}
	if err := validateShape(ev); err != nil {
		return preparedEvent{}, err
	}
	at := ev.At
	if at.IsZero() {
		at = s.clock.Now()
	}
	id, err := ids.New(s.clock)
	if err != nil {
		return preparedEvent{}, fmt.Errorf("events: generate id: %w", err)
	}
	payload := Payload{
		EntityType: ev.EntityType,
		EntityID:   ev.EntityID,
		Old:        ev.Old,
		New:        ev.New,
		Summary:    truncateSummary(ev.Summary),
		Actor:      ev.Actor,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return preparedEvent{}, shapeError("payload is not serializable: %v", err)
	}
	return preparedEvent{
		id:         id,
		typ:        ev.Type,
		entityType: ev.EntityType,
		entityID:   ev.EntityID,
		payload:    body,
		actor:      ev.Actor,
		at:         at,
	}, nil
}

func (s *Service) insert(ctx context.Context, tx *sql.Tx, p preparedEvent) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO activities(id, event_type, entity_type, entity_id, payload, actor, merge_key, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, NULL, ?)`,
		p.id, string(p.typ), string(p.entityType), p.entityID, string(p.payload), string(p.actor), clock.Format(p.at))
	if err != nil {
		return fmt.Errorf("events: emit %s: %w", p.typ, err)
	}
	return nil
}

// noteUpdatedUpsert is the §5.6.2 statement. The conflict target must repeat
// the partial-index predicate. created_at intentionally stays at the first save
// of the day; payload.save_count/last_saved_at/actor/summary move forward.
const noteUpdatedUpsert = `INSERT INTO activities(id, event_type, entity_type, entity_id, payload, actor, merge_key, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(merge_key) WHERE merge_key IS NOT NULL DO UPDATE SET
  payload = json_set(activities.payload,
                     '$.save_count',    json_extract(activities.payload, '$.save_count') + 1,
                     '$.last_saved_at', excluded.created_at,
                     '$.actor',         excluded.actor,
                     '$.summary',       json_extract(excluded.payload, '$.summary'))`

func (s *Service) dayKey(at time.Time) string { return at.In(s.loc).Format(dayLayout) }

func (s *Service) where(q Query) (string, []any) {
	var clauses []string
	var args []any
	if q.Since != nil {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, clock.Format(*q.Since))
	}
	if q.Until != nil {
		clauses = append(clauses, "created_at <= ?")
		args = append(args, clock.Format(*q.Until))
	}
	if q.EntityID != "" {
		clauses = append(clauses, "entity_id = ?")
		args = append(args, q.EntityID)
	}
	if len(q.EntityTypes) > 0 {
		clauses = append(clauses, "entity_type IN ("+placeholders(len(q.EntityTypes))+")")
		for _, v := range q.EntityTypes {
			args = append(args, string(v))
		}
	}
	if len(q.EventTypes) > 0 {
		clauses = append(clauses, "event_type IN ("+placeholders(len(q.EventTypes))+")")
		for _, v := range q.EventTypes {
			args = append(args, string(v))
		}
	}
	if len(q.Actors) > 0 {
		clauses = append(clauses, "actor IN ("+placeholders(len(q.Actors))+")")
		for _, v := range q.Actors {
			args = append(args, string(v))
		}
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

type rowScanner interface{ Scan(dest ...any) error }

func (s *Service) scanActivity(row rowScanner) (Activity, error) {
	var (
		a           Activity
		typ         string
		entityType  string
		entityID    string
		payloadJSON string
		actor       string
		createdAt   string
	)
	if err := row.Scan(&a.ID, &typ, &entityType, &entityID, &payloadJSON, &actor, &createdAt); err != nil {
		return Activity{}, fmt.Errorf("events: scan activity: %w", err)
	}
	var payload Payload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return Activity{}, fmt.Errorf("events: activity %s payload: %w", a.ID, err)
	}
	if string(payload.EntityType) != entityType || payload.EntityID != entityID {
		return Activity{}, fmt.Errorf(
			"events: activity %s payload/column mismatch: payload=(%s,%s) columns=(%s,%s)",
			a.ID, payload.EntityType, payload.EntityID, entityType, entityID)
	}
	// note.updated merge updates payload.actor but intentionally not the actor
	// column (which stays the first save's actor), so only merged rows may
	// diverge.
	if string(payload.Actor) != actor && !(Type(typ) == schema.EventNoteUpdated && payload.SaveCount > 1) {
		return Activity{}, fmt.Errorf(
			"events: activity %s payload/column actor mismatch: payload=%s column=%s", a.ID, payload.Actor, actor)
	}
	a.Type = Type(typ)
	a.EntityType = EntityType(entityType)
	a.EntityID = entityID
	a.Payload = payload
	a.Actor = Actor(actor)
	at, err := clock.Parse(createdAt)
	if err != nil {
		return Activity{}, fmt.Errorf("events: activity %s created_at: %w", a.ID, err)
	}
	a.CreatedAt = at
	return a, nil
}

func validateShape(ev Event) error {
	switch ev.Type {
	case schema.EventRelationAdded:
		if ev.New == nil {
			return shapeError("relation.added requires new with the 5-key tuple")
		}
		if err := validateRelationObject(*ev.New); err != nil {
			return err
		}
		if ev.Old != nil {
			return validateRelationObject(*ev.Old)
		}
	case schema.EventRelationRemoved:
		if ev.Old == nil {
			return shapeError("relation.removed requires old with the 5-key tuple")
		}
		if err := validateRelationObject(*ev.Old); err != nil {
			return err
		}
		if ev.New != nil {
			return validateRelationObject(*ev.New)
		}
	case schema.EventEntityUpdated:
		if ev.Old == nil && ev.New == nil {
			return shapeError("entity.updated requires old or new")
		}
	case schema.EventEntityRelocated:
		if ev.Old == nil || ev.New == nil {
			return shapeError("entity.relocated requires old and new")
		}
		if !hasKey(*ev.Old, "path") || !hasKey(*ev.New, "path") {
			return shapeError("entity.relocated old and new must carry path")
		}
	case schema.EventEntityPurged:
		if ev.Old != nil || ev.New != nil {
			return shapeError("entity.purged must not carry old or new")
		}
	case schema.EventStatusChanged:
		if ev.Old == nil && ev.New == nil {
			return shapeError("status.changed requires old or new")
		}
		// SPEC-00 §6: the shape follows the entity convention. task uses
		// {column, column_name, is_done} / {blocked} / {archived} (plus
		// completed_at on transitions); idea/evidence/report/run/
		// experiment/project carry {status}.
		if ev.EntityType == schema.EntityTask {
			oldOK := ev.Old != nil && taskStatusShape(*ev.Old)
			newOK := ev.New != nil && taskStatusShape(*ev.New)
			if !oldOK && !newOK {
				return shapeError("status.changed for task must carry column, blocked or archived")
			}
			break
		}
		if ev.Old != nil && !hasKey(*ev.Old, "status") {
			return shapeError("status.changed old must carry status")
		}
		if ev.New != nil && !hasKey(*ev.New, "status") {
			return shapeError("status.changed new must carry status")
		}
	}
	return nil
}

var relationTupleKeys = []string{"src_type", "src_id", "relation", "dst_type", "dst_id"}

func validateRelationObject(obj Object) error {
	if len(obj) != len(relationTupleKeys) {
		return shapeError("relation payload must contain exactly %d keys, got %d", len(relationTupleKeys), len(obj))
	}
	for _, key := range relationTupleKeys {
		value, ok := obj[key]
		if !ok {
			return shapeError("relation payload is missing %q", key)
		}
		if _, ok := value.(string); !ok {
			return shapeError("relation payload %q must be a string", key)
		}
	}
	return nil
}

func hasKey(obj Object, key string) bool {
	_, ok := obj[key]
	return ok
}

// taskStatusShape reports whether one side carries a SPEC-00 §6 task
// status.changed key set: {column...} for column transitions, {blocked} for
// the blocked marker or {archived} for archiving. completed_at rides along
// on column transitions and never names the shape by itself.
func taskStatusShape(obj Object) bool {
	return hasKey(obj, "column") || hasKey(obj, "blocked") || hasKey(obj, "archived")
}

func shapeError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errs.ErrBadPayloadShape, fmt.Sprintf(format, args...))
}

func truncateSummary(s string) string {
	runes := []rune(s)
	if len(runes) <= maxSummaryRunes {
		return s
	}
	return string(runes[:maxSummaryRunes])
}

func normalizedLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultLimit
	case limit > maxLimit:
		return maxLimit
	default:
		return limit
	}
}

func normalizedOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
