// Package apperr holds the sentinel errors of the UI state layer (app) so the
// text helper can match them without importing app: app imports the component
// layer, and the component layer imports errtext, so errtext importing app
// would close an import cycle. app re-exports the values under its own names,
// which are the ones callers use.
package apperr

import "errors"

// ErrNoProjectService reports a project action in a window without a
// workspace, where no project service is wired.
var ErrNoProjectService = errors.New("app: no project service in this window")
