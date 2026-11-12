// Package events holds the Activity contract and its single writer/reader
// (SPEC-01 §5.6, SPEC-00 §6).
//
// Activities are append-only. The only exception is the same-day merge of
// note.updated rows, implemented as the partial-unique-index UPSERT from
// §5.6.2. Business code must never write the activities table directly.
package events

import (
	"context"
	"database/sql"
	"time"

	"researchos/internal/foundation/schema"
)

// Type is an event type from the 16-entry registry.
type Type = schema.EventType

// Actor is the origin of an event: user, mcp or ai.
type Actor = schema.Actor

// EntityType is an entity type from the 14-entry registry.
type EntityType = schema.EntityType

// Object is the only legal shape for event old/new: a JSON object at the top
// level (SPEC-00 §6 v1.1). Arrays, scalars and null are invalid.
type Object map[string]any

// Payload is the fixed payload structure. Additional keys are limited to
// save_count and last_saved_at, both note.updated only (§5.6.3).
type Payload struct {
	EntityType  EntityType `json:"entity_type"`
	EntityID    string     `json:"entity_id"`
	Old         *Object    `json:"old,omitempty"`
	New         *Object    `json:"new,omitempty"`
	Summary     string     `json:"summary"`
	Actor       Actor      `json:"actor"`
	SaveCount   int        `json:"save_count,omitempty"`
	LastSavedAt string     `json:"last_saved_at,omitempty"`
}

// Event is one activity to write. A zero At means the writer clock.
type Event struct {
	Type       Type
	EntityType EntityType
	EntityID   string
	Old, New   *Object
	Summary    string
	Actor      Actor
	At         time.Time
}

// NoteUpdatedEvent drives the merged note.updated path.
type NoteUpdatedEvent struct {
	NoteID  string
	Summary string
	Actor   Actor
	At      time.Time
}

// Writer is the only entry point for activity writes.
type Writer interface {
	// Emit uses the caller transaction so business writes and the event commit
	// atomically.
	Emit(ctx context.Context, tx *sql.Tx, ev Event) error
	// EmitStandalone opens its own transaction for events that accompany no
	// business write.
	EmitStandalone(ctx context.Context, ev Event) error
	// EmitNoteUpdated uses the same-day merge path.
	EmitNoteUpdated(ctx context.Context, tx *sql.Tx, ev NoteUpdatedEvent) error
}

// Reader queries activities.
type Reader interface {
	List(ctx context.Context, q Query) ([]Activity, error)
	Count(ctx context.Context, q Query) (int, error)
	Aggregate(ctx context.Context, q AggregateQuery) ([]Bucket, error)
}

// Query filters activities. Limit defaults to 50 and is capped at 500.
type Query struct {
	Since       *time.Time
	Until       *time.Time
	EntityTypes []EntityType
	EntityID    string
	EventTypes  []Type
	Actors      []Actor
	Limit       int
	Offset      int
}

// BucketKind selects the Aggregate grouping.
type BucketKind string

const (
	// BucketDay groups by the local calendar day of created_at.
	BucketDay BucketKind = "day"
	// BucketEntityType groups by entity_type.
	BucketEntityType BucketKind = "entity_type"
	// BucketEventType groups by event_type.
	BucketEventType BucketKind = "event_type"
	// BucketActor groups by actor.
	BucketActor BucketKind = "actor"
)

// AggregateQuery is a Query plus the grouping.
type AggregateQuery struct {
	Query
	Bucket BucketKind
}

// Bucket is one aggregate result row.
type Bucket struct {
	Key   string
	Count int
}

// Activity is one stored activity. Payload is decoded from the payload column
// and cross-checked against the projection columns.
type Activity struct {
	ID         string
	Type       Type
	EntityType EntityType
	EntityID   string
	Payload    Payload
	Actor      Actor
	CreatedAt  time.Time
}
