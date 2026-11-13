// Package adapters binds sibling-module services to the consumer-side
// interfaces declared in internal/ports (SPEC-03 §5.2).
//
// The UI and the MCP facade construct these adapters once and inject them where
// internal/ports expects a port. Keeping the binding here means wiring code
// never imports a sibling module's internals, and internal/ports stays free of
// imports from those modules, so no import cycle can form.
package adapters

import (
	"errors"

	"researchos/internal/foundation/schema"
	"researchos/internal/ports"
)

// ErrUnavailable reports an adapter that was constructed without its
// collaborating service, for example before optional module wiring ran.
var ErrUnavailable = errors.New("adapters: service unavailable")

// actorOrUser treats an empty actor as the local user, matching the sibling
// modules' shared convention (SPEC-05 inputs are driven by both UI and MCP).
func actorOrUser(a ports.Actor) schema.Actor {
	if a == "" {
		return schema.ActorUser
	}
	return schema.Actor(a)
}
