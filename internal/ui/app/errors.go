package app

import "researchos/internal/ui/apperr"

// ErrNoProjectService reports a project action in a window without a
// workspace, where no project service is wired. The value lives in apperr so
// that errtext can match it; it is exported here under the name callers use.
var ErrNoProjectService = apperr.ErrNoProjectService
