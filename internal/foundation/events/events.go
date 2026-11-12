// Package events holds the Activity event contract (SPEC-01 §5.6).
//
// Milestone M1 ships only the shared type aliases so that the normative service
// signatures (for example projects.Store.SetStatus) can reference events.Actor
// from day one. The Writer/Reader implementation lands with M2.
package events

import "researchos/internal/foundation/schema"

// Type is an event type from the 16-entry registry.
type Type = schema.EventType

// Actor is the origin of an event: user, mcp or ai.
type Actor = schema.Actor

// EntityType is an entity type from the 14-entry registry.
type EntityType = schema.EntityType
