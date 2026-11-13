package project

import (
	"errors"
	"fmt"

	"researchos/internal/foundation/events"
)

// Sentinel errors of the project service (SPEC-03 §5.3). Callers match them
// with errors.Is.
var (
	// ErrNotFound reports a project id without a registration row.
	ErrNotFound = errors.New("project: not found")
	// ErrArchived reports a write attempt on an archived (read-only) project.
	ErrArchived = errors.New("project: archived and read-only")
	// ErrNameInvalid reports a name that is empty after trimming, longer than
	// 200 runes, or contains control characters.
	ErrNameInvalid = errors.New("project: name invalid")
	// ErrInvalidInput reports another input field outside its limit. SPEC-03
	// only names the name error; this sentinel is the additive counterpart for
	// summary/goals/focus limits.
	ErrInvalidInput = errors.New("project: invalid input")
	// ErrBusy reports a workspace (migration) lock that another operation holds.
	ErrBusy = errors.New("project: workspace busy")
	// ErrDeleteNotConfirmed reports a permanent delete without a matching
	// project name or confirmation token (§5.4).
	ErrDeleteNotConfirmed = errors.New("project: delete not confirmed by user")
	// ErrInvalidActor reports an actor outside {user, mcp} for a state change.
	ErrInvalidActor = errors.New("project: actor must be user or mcp")
	// ErrEventMissing reports a write whose Activity event could not be stored.
	// The business change is committed; only the audit record is missing, so
	// the caller should surface "saved, activity missing" instead of failing the
	// operation (SPEC-03 §5.4 cross-database rule).
	ErrEventMissing = errors.New("project: activity event was not written")
	// errNoWorkspace reports a service built without a usable workspace.
	errNoWorkspace = errors.New("project: service has no workspace")
)

// ArchivedError is the typed read-only rejection. errors.Is(err, ErrArchived)
// holds, so UI, service and MCP layers can share one check (§5.8).
type ArchivedError struct {
	ProjectID string
}

// Error implements error. The message carries the localised UI wording
// required by §5.8 ("项目已归档，只读").
func (e *ArchivedError) Error() string {
	return fmt.Sprintf("project: %s is archived (项目已归档，只读)", e.ProjectID)
}

// Is makes errors.Is(err, ErrArchived) true.
func (e *ArchivedError) Is(target error) bool { return target == ErrArchived }

// InvalidFieldError reports one rejected input field. It unwraps to
// ErrNameInvalid for names and to ErrInvalidInput for the other limits, so
// callers can match either the precise field or the category.
type InvalidFieldError struct {
	Field  string
	Reason string
	Err    error
}

// Error implements error.
func (e *InvalidFieldError) Error() string {
	return fmt.Sprintf("project: %s: %s", e.Field, e.Reason)
}

// Unwrap exposes the category sentinel.
func (e *InvalidFieldError) Unwrap() error { return e.Err }

// InvalidActorError reports an actor that may not perform a state change.
// errors.Is(err, ErrInvalidActor) holds.
type InvalidActorError struct {
	Actor events.Actor
}

// Error implements error.
func (e *InvalidActorError) Error() string {
	return fmt.Sprintf("project: actor %q is not allowed (must be user or mcp)", string(e.Actor))
}

// Is makes errors.Is(err, ErrInvalidActor) true.
func (e *InvalidActorError) Is(target error) bool { return target == ErrInvalidActor }
