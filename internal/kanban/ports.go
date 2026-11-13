package kanban

import (
	"context"

	"researchos/internal/foundation/events"
	"researchos/internal/foundation/workspace"
	"researchos/internal/ports"
)

// Completer adapts SPEC-08 to the SPEC-03 Overview port TaskCompleter
// (SPEC-08 §5.10). The port is keyed by projectID, so the adapter resolves
// the project handle through the workspace and delegates to TaskService.
// Complete keeps SPEC-11 semantics: the actor passes straight through to the
// event writer (AC-27).
type Completer struct {
	ws *workspace.Workspace
}

var _ ports.TaskCompleter = (*Completer)(nil)

// NewCompleter wires the port to an open workspace.
func NewCompleter(ws *workspace.Workspace) *Completer { return &Completer{ws: ws} }

// CompleteTask implements ports.TaskCompleter: Open(projectID) →
// kanban.Complete(actor, taskID).
func (c *Completer) CompleteTask(ctx context.Context, projectID, taskID string, actor events.Actor) error {
	handle, err := c.ws.Project(ctx, projectID)
	if err != nil {
		return err
	}
	m := New(handle, DepsFromWorkspace(c.ws, handle))
	_, err = m.Tasks().Complete(ctx, actor, taskID)
	return err
}
