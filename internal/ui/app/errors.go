package app

import "errors"

// errNoProjectService reports a project action in a window without a
// workspace, where no project service is wired.
var errNoProjectService = errors.New("app: no project service in this window")
