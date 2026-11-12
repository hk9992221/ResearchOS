// Package indexhook defines the search-index extension point (SPEC-01 §4.7).
// It intentionally contains no implementation and no tables: SPEC-02/SPEC-10
// own the index storage and will provide an Indexer.
package indexhook

import (
	"context"

	"researchos/internal/foundation/events"
)

// Scope selects what Rebuild covers. It is deliberately minimal; SPEC-02/10 may
// extend it (per-project rebuilds, global document sets, ...).
type Scope struct {
	ProjectID string
}

// Indexer is the post-commit index hook.
//
// OnEntityChanged is called after the business transaction has committed. A
// failure must not roll back or fail the business write; the caller logs it and
// the index can be rebuilt later. Rebuild is idempotent and is triggered by the
// settings page's "rebuild index" action.
type Indexer interface {
	OnEntityChanged(ctx context.Context, ev events.Activity) error
	Rebuild(ctx context.Context, scope Scope) error
}
