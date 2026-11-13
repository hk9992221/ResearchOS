package boot

// The Research Notes page reaches SPEC-05 through this adapter, the same way
// the other pages reach their module: the composition root builds the service,
// the page only calls the narrow interface. Construction mirrors the MCP
// binding (internal/mcpbind/notes.go) so the two entry points behave the same,
// and every call acts as ActorUser.

import (
	"context"
	"path"

	"researchos/internal/foundation/clock"
	fworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/notes"
	"researchos/internal/ports"
	"researchos/internal/ui/pages"
)

// notesModule serves one open workspace. The note service is bound to a
// project, so a call resolves the project's service from the workspace; the
// adapter holds no database handle of its own.
type notesModule struct {
	ws *fworkspace.Workspace
}

var _ pages.NotesAPI = (*notesModule)(nil)

func newNotesModule(ws *fworkspace.Workspace) *notesModule {
	return &notesModule{ws: ws}
}

// service returns the note service of one project.
func (m *notesModule) service(projectID string) notes.NoteService {
	return notes.NewNoteService(m.ws, projectID, notes.ServiceOptions{Clock: clock.System{}})
}

func (m *notesModule) Create(ctx context.Context, projectID string, in notes.CreateNoteInput) (*notes.Note, error) {
	return m.service(projectID).Create(ctx, in)
}

func (m *notesModule) Get(ctx context.Context, projectID, noteID string) (*notes.Note, error) {
	return m.service(projectID).Get(ctx, noteID)
}

func (m *notesModule) List(ctx context.Context, projectID string, q notes.ListNotesQuery) (notes.NotePage, error) {
	return m.service(projectID).List(ctx, q)
}

func (m *notesModule) LoadBody(ctx context.Context, projectID, noteID string) (*notes.Body, error) {
	return m.service(projectID).LoadBody(ctx, noteID)
}

func (m *notesModule) SaveBody(ctx context.Context, projectID string, in notes.SaveBodyInput) (*notes.SaveResult, error) {
	return m.service(projectID).SaveBody(ctx, in)
}

func (m *notesModule) UpdateMeta(ctx context.Context, projectID string, in notes.UpdateMetaInput) (*notes.Note, error) {
	return m.service(projectID).UpdateMeta(ctx, in)
}

func (m *notesModule) SoftDelete(ctx context.Context, projectID, noteID string, actor ports.Actor) error {
	return m.service(projectID).SoftDelete(ctx, noteID, actor)
}

func (m *notesModule) ResolveTarget(ctx context.Context, projectID string, ref notes.EntityRef) (*notes.LinkTarget, error) {
	return m.service(projectID).ResolveTarget(ctx, ref)
}

// ImageBaseDir resolves the directory a note's Markdown image paths are
// relative to: projects/<project>/notes. Markdown keeps attachments as
// ../attachments/<note-id>/<file> (SPEC-05 §5.6), so the preview resolves them
// without the UI ever joining workspace paths.
func (m *notesModule) ImageBaseDir(projectID, noteID string) (string, error) {
	rel := path.Dir(m.ws.Paths().NoteFile(projectID, noteID))
	return m.ws.Paths().Abs(rel)
}
