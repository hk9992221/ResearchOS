// Package errs defines the typed sentinel errors of the foundation layer.
//
// Every value is a plain sentinel so callers can match with errors.Is; call
// sites wrap them with %w and contextual detail. They are declared here (even
// the ones consumed only by later milestones) so that error identity is stable
// across the whole application.
package errs

import "errors"

var (
	// Paths.

	// ErrPathEscape reports a path that is absolute, drives-qualified, contains
	// a ".." segment, or resolves (through a symlink) outside the workspace root.
	ErrPathEscape = errors.New("path escapes the workspace root")

	// Workspace lifecycle.

	// ErrNotWorkspace reports a directory that has neither a marker file nor a
	// global database.
	ErrNotWorkspace = errors.New("not a ResearchOS workspace")
	// ErrWorkspaceCorrupt reports an inconsistent workspace: marker and database
	// disagree, the marker is invalid, or the layout is damaged.
	ErrWorkspaceCorrupt = errors.New("workspace layout or marker is corrupt")
	// ErrDirNotEmpty reports an initialization target that already holds foreign
	// files and cannot be taken over implicitly.
	ErrDirNotEmpty = errors.New("directory is not empty")

	// Schema migrations.

	// ErrMigrationTampered reports that an already-applied migration no longer
	// matches its recorded checksum (or disappeared).
	ErrMigrationTampered = errors.New("applied migration was modified")
	// ErrSchemaTooNew reports a database whose schema version exceeds the target
	// of this build.
	ErrSchemaTooNew = errors.New("database schema is newer than this build")

	// Configuration and settings.

	// ErrBadSettingKey reports a setting key outside the registered namespace.
	ErrBadSettingKey = errors.New("invalid setting key")
	// ErrConfigCorrupt reports an unreadable application configuration file.
	ErrConfigCorrupt = errors.New("config file is corrupt")

	// External references.

	// ErrRefNotFound reports an external reference path that does not exist.
	ErrRefNotFound = errors.New("external reference not found")

	// Entities and note files.

	// ErrNoteFileMissing reports a note whose Markdown body file is gone.
	ErrNoteFileMissing = errors.New("note body file is missing")
	// ErrEntityDeleted reports an operation on a soft-deleted entity.
	ErrEntityDeleted = errors.New("entity is deleted")

	// Destructive operations.

	// ErrUserConfirmationRequired reports a destructive operation without a valid
	// in-app confirmation token.
	ErrUserConfirmationRequired = errors.New("user confirmation required")
	// ErrConfirmScopeMismatch reports a token whose scope does not cover the
	// requested operation.
	ErrConfirmScopeMismatch = errors.New("confirmation scope mismatch")

	// Events.

	// ErrBadEventType reports an event type outside the 16-entry registry.
	ErrBadEventType = errors.New("invalid event type")
	// ErrBadActor reports an actor outside {user, mcp, ai}.
	ErrBadActor = errors.New("invalid actor")
	// ErrBadEntityType reports an entity type outside the registry.
	ErrBadEntityType = errors.New("invalid entity type")
	// ErrBadEntityID reports an entity id that is not a lowercase UUIDv7.
	ErrBadEntityID = errors.New("invalid entity id")
	// ErrBadPayloadShape reports an old/new object with an invalid shape.
	ErrBadPayloadShape = errors.New("invalid event payload shape")
	// ErrWrongEmitPath reports an event emitted through the wrong writer method
	// (note.updated must use EmitNoteUpdated).
	ErrWrongEmitPath = errors.New("event must be emitted through the dedicated path")

	// Relations.

	// ErrRelationNotAllowed reports a (src_type, relation, dst_type) combination
	// outside the direction registry.
	ErrRelationNotAllowed = errors.New("relation type not allowed between these entity types")
	// ErrSelfRelation reports a relation whose source and target are the same
	// entity.
	ErrSelfRelation = errors.New("self relation is not allowed")
)
