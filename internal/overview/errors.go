package overview

import "errors"

// Sentinel errors of the Overview service (SPEC-03 §5.6, §5.8). Callers match
// them with errors.Is.
var (
	// ErrNotFound reports a project id without a registration row in
	// global.db.projects. It is the only ordinary failure Load reports: every
	// widget problem degrades a single widget instead (§5.6).
	ErrNotFound = errors.New("overview: project not found")

	// ErrArchived reports a quick action attempted on an archived project. It
	// is the §5.8 read-only guard of this package; when Options.CheckWritable is
	// injected that hook owns the decision and its error is passed through
	// unchanged instead.
	ErrArchived = errors.New("overview: project is archived and read-only")

	// ErrFeatureUnavailable reports a quick action whose vendor port is not
	// registered (SPEC-08 Kanban or SPEC-05 Notes). UI hides the entry point
	// (§5.6 "端口未注册时返回 ErrFeatureUnavailable，UI 隐藏入口").
	ErrFeatureUnavailable = errors.New("overview: feature unavailable")

	// errNoWorkspace reports a service built without a usable workspace, or a
	// workspace whose global database is already closed.
	errNoWorkspace = errors.New("overview: no workspace")
)
