// Package errtext turns an error into the one sentence the interface shows.
//
// Errors travel from the services as Go values: sentinels from
// foundation/errs or a business package, wrapped with context, or plain errors
// from the operating system. The UI must not show those strings to a reader of
// Chinese, so Text translates the errors whose meaning it knows and leaves
// every other error as it is, because its text is already the detail the
// caller added.
//
// The translation is a key of the errors area of a language pack
// ("error.<sentinel>" or "error.<module>_<condition>"), which keeps the
// messages updatable with the pack rather than compiled into the page that
// shows them.
//
// Scope: the sentinels reachable from today's interface are mapped below.
// Module sentinels whose UI does not exist yet (literature, kanban, notes,
// weekly review, ideas, overview) are deliberately left unmapped: those
// packages already carry machine error codes, and their pages localise by
// code once they are built, rather than by a growing table here.
package errtext

import (
	"errors"

	"researchos/internal/adapters"
	"researchos/internal/foundation/errs"
	"researchos/internal/i18n"
	"researchos/internal/project"
	"researchos/internal/ui/apperr"
	"researchos/internal/ui/theme"
	"researchos/internal/workspace"
)

// mapping binds a sentinel to its message key. The order does not matter: the
// sentinels are distinct and an error wraps at most one.
var mapping = []struct {
	err error
	key string
}{
	// Foundation.
	{errs.ErrPathEscape, "error.path_escape"},
	{errs.ErrNotWorkspace, "error.not_workspace"},
	{errs.ErrWorkspaceCorrupt, "error.workspace_corrupt"},
	{errs.ErrDirNotEmpty, "error.dir_not_empty"},
	{errs.ErrMigrationTampered, "error.migration_tampered"},
	{errs.ErrSchemaTooNew, "error.schema_too_new"},
	{errs.ErrBadSettingKey, "error.bad_setting_key"},
	{errs.ErrConfigCorrupt, "error.config_corrupt"},
	{errs.ErrRefNotFound, "error.ref_not_found"},
	{errs.ErrNoteFileMissing, "error.note_file_missing"},
	{errs.ErrEntityDeleted, "error.entity_deleted"},
	{errs.ErrUserConfirmationRequired, "error.user_confirmation_required"},
	{errs.ErrConfirmScopeMismatch, "error.confirm_scope_mismatch"},
	{errs.ErrBadEventType, "error.bad_event_type"},
	{errs.ErrBadActor, "error.bad_actor"},
	{errs.ErrBadEntityType, "error.bad_entity_type"},
	{errs.ErrBadEntityID, "error.bad_entity_id"},
	{errs.ErrBadPayloadShape, "error.bad_payload_shape"},
	{errs.ErrWrongEmitPath, "error.wrong_emit_path"},
	{errs.ErrRelationNotAllowed, "error.relation_not_allowed"},
	{errs.ErrSelfRelation, "error.self_relation"},

	// Projects. The typed ArchivedError is handled before this table so its
	// project id survives.
	{project.ErrNotFound, "error.project_not_found"},
	{project.ErrArchived, "error.project_archived"},
	{project.ErrNameInvalid, "error.project_name_invalid"},
	{project.ErrInvalidInput, "error.project_invalid_input"},
	{project.ErrBusy, "error.project_busy"},
	{project.ErrDeleteNotConfirmed, "error.project_delete_not_confirmed"},
	{project.ErrInvalidActor, "error.project_invalid_actor"},
	{project.ErrEventMissing, "error.project_event_missing"},

	// Workspace migrations.
	{workspace.ErrBusy, "error.workspace_busy"},
	{workspace.ErrInvalidPlan, "error.workspace_invalid_plan"},
	{workspace.ErrInsufficientSpace, "error.workspace_insufficient_space"},
	{workspace.ErrTargetNotEmpty, "error.workspace_target_not_empty"},

	// Adapters, the theme and the UI state layer.
	{adapters.ErrUnavailable, "error.adapters_unavailable"},
	{theme.ErrInvalidMode, "error.theme_invalid_mode"},
	{apperr.ErrNoProjectService, "error.app_no_project_service"},
}

// Text returns the message to show for err. Nil is "". A mapped error,
// wrapped or not, becomes its translated sentence in the current language; the
// typed project archival error keeps its project id; every other error keeps
// its own text unchanged.
func Text(err error) string {
	if err == nil {
		return ""
	}
	var archived *project.ArchivedError
	if errors.As(err, &archived) {
		return i18n.Tf("error.project_archived_id", map[string]string{"id": archived.ProjectID})
	}
	for _, m := range mapping {
		if errors.Is(err, m.err) {
			return i18n.T(m.key)
		}
	}
	return err.Error()
}
