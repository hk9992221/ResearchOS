package adapters

import (
	"context"
	"fmt"

	"researchos/internal/foundation/workspace"
	"researchos/internal/notes"
	"researchos/internal/ports"
)

// Notes adapts the SPEC-05 note service to ports.NoteCreator. Notes are created
// through notes.NewNoteService per call: the service resolves the project
// database through the workspace, so it stays valid across close/reopen.
type Notes struct {
	ws   *workspace.Workspace
	opts notes.ServiceOptions
}

var _ ports.NoteCreator = (*Notes)(nil)

// NewNotes binds the adapter to ws and the optional collaborators of the notes
// service (clock, index hook, logger).
func NewNotes(ws *workspace.Workspace, opts notes.ServiceOptions) *Notes {
	return &Notes{ws: ws, opts: opts}
}

// CreateScratchpad creates a scratchpad note and returns its entity reference.
// Validation of the project id and title belongs to the notes module; this
// adapter passes both through unchanged. An empty actor means the local user.
func (n *Notes) CreateScratchpad(ctx context.Context, projectID, title, bodyMD string, actor ports.Actor) (ports.EntityRef, error) {
	if n == nil || n.ws == nil {
		return ports.EntityRef{}, fmt.Errorf("adapters: notes: %w", ErrUnavailable)
	}
	note, err := notes.NewNoteService(n.ws, projectID, n.opts).Create(ctx, notes.CreateNoteInput{
		Kind:  notes.KindScratchpad,
		Title: title,
		Body:  bodyMD,
		Actor: actorOrUser(actor),
	})
	if err != nil {
		return ports.EntityRef{}, err
	}
	return ports.EntityRef{Type: "note", ID: note.ID}, nil
}
