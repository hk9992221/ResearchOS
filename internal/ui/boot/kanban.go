package boot

// The kanban page reaches SPEC-08 through this adapter, the same way the other
// modules reach theirs. Kanban differs from notes and literature: kanban.New
// takes a resolved *workspace.ProjectHandle, not a project id, so the adapter
// resolves the handle per project and caches one module for each open project
// (mirroring internal/mcpbind/kanban.go). A failed resolution drops the cached
// module, so a reopened project rebuilds it. Every call acts as ActorUser.

import (
	"context"
	"sync"

	"researchos/internal/experiments"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
	fworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/ideas"
	"researchos/internal/kanban"
	"researchos/internal/notes"
	"researchos/internal/ui/pages"
)

// kanbanModule serves one open workspace. One kanban.Module is bound to one
// project handle, so calls resolve it from the project id.
type kanbanModule struct {
	ws *fworkspace.Workspace

	mu      sync.Mutex
	modules map[string]*kanban.Module
}

var _ pages.KanbanAPI = (*kanbanModule)(nil)

func newKanbanModule(ws *fworkspace.Workspace) *kanbanModule {
	return &kanbanModule{ws: ws, modules: map[string]*kanban.Module{}}
}

// module returns the SPEC-08 module of one project, building it on first use.
// A resolution failure drops the cache entry so a stale handle never sticks.
func (m *kanbanModule) module(ctx context.Context, projectID string) (*kanban.Module, error) {
	m.mu.Lock()
	cached := m.modules[projectID]
	m.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	handle, err := m.ws.Project(ctx, projectID)
	if err != nil {
		m.drop(projectID)
		return nil, err
	}
	module := kanban.New(handle, kanban.DepsFromWorkspace(m.ws, handle))
	m.mu.Lock()
	m.modules[projectID] = module
	m.mu.Unlock()
	return module, nil
}

func (m *kanbanModule) drop(projectID string) {
	m.mu.Lock()
	delete(m.modules, projectID)
	m.mu.Unlock()
}

// actor is the UI path: every write is the local user's (SPEC-08 §7).
var kanbanActor events.Actor = schema.ActorUser

func (m *kanbanModule) Board(ctx context.Context, projectID string, q kanban.BoardQuery) (kanban.BoardView, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.BoardView{}, err
	}
	return mod.Views().Board(ctx, q)
}

func (m *kanbanModule) Columns(ctx context.Context, projectID string) ([]kanban.Column, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return mod.Columns().List(ctx)
}

func (m *kanbanModule) List(ctx context.Context, projectID string, q kanban.Query) (kanban.Page[kanban.TaskSummary], error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.Page[kanban.TaskSummary]{}, err
	}
	return mod.Tasks().List(ctx, q)
}

func (m *kanbanModule) ListArchived(ctx context.Context, projectID string, q kanban.Query) (kanban.Page[kanban.TaskSummary], error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.Page[kanban.TaskSummary]{}, err
	}
	return mod.Tasks().ListArchived(ctx, q)
}

func (m *kanbanModule) Blocked(ctx context.Context, projectID string) (kanban.Page[kanban.TaskSummary], error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.Page[kanban.TaskSummary]{}, err
	}
	return mod.Views().Blocked(ctx)
}

func (m *kanbanModule) GetTask(ctx context.Context, projectID, taskID string) (kanban.TaskDetail, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.TaskDetail{}, err
	}
	return mod.Tasks().Get(ctx, taskID)
}

func (m *kanbanModule) CreateTask(ctx context.Context, projectID string, in kanban.CreateInput) (kanban.Task, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.Task{}, err
	}
	return mod.Tasks().Create(ctx, kanbanActor, in)
}

func (m *kanbanModule) UpdateTask(ctx context.Context, projectID string, in kanban.UpdateInput) (kanban.Task, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.Task{}, err
	}
	return mod.Tasks().Update(ctx, kanbanActor, in)
}

func (m *kanbanModule) MoveTask(ctx context.Context, projectID string, in kanban.MoveInput) (kanban.Task, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.Task{}, err
	}
	return mod.Tasks().Move(ctx, kanbanActor, in)
}

func (m *kanbanModule) ArchiveTask(ctx context.Context, projectID, taskID string) (kanban.Task, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.Task{}, err
	}
	return mod.Tasks().Archive(ctx, kanbanActor, taskID)
}

func (m *kanbanModule) UnarchiveTask(ctx context.Context, projectID, taskID string) (kanban.Task, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.Task{}, err
	}
	return mod.Tasks().Unarchive(ctx, kanbanActor, taskID)
}

func (m *kanbanModule) DeleteTask(ctx context.Context, projectID, taskID string) error {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return err
	}
	return mod.Tasks().SoftDelete(ctx, kanbanActor, taskID)
}

func (m *kanbanModule) CreateColumn(ctx context.Context, projectID string, in kanban.CreateColumnInput) (kanban.Column, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.Column{}, err
	}
	return mod.Columns().Create(ctx, kanbanActor, in)
}

func (m *kanbanModule) RenameColumn(ctx context.Context, projectID, oldName, newName string) (kanban.Column, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.Column{}, err
	}
	return mod.Columns().Rename(ctx, kanbanActor, oldName, newName)
}

func (m *kanbanModule) MoveColumn(ctx context.Context, projectID, name string, toPosition int) ([]kanban.Column, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return mod.Columns().Move(ctx, kanbanActor, name, toPosition)
}

func (m *kanbanModule) SetColumnDone(ctx context.Context, projectID, name string, done bool) (kanban.Column, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.Column{}, err
	}
	return mod.Columns().SetDone(ctx, kanbanActor, name, done)
}

func (m *kanbanModule) DeleteColumn(ctx context.Context, projectID, name, migrateTo string) error {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return err
	}
	return mod.Columns().Delete(ctx, kanbanActor, name, migrateTo)
}

func (m *kanbanModule) ChecklistAdd(ctx context.Context, projectID, taskID, text string, beforeID *string) (kanban.ChecklistItem, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.ChecklistItem{}, err
	}
	return mod.Checklist().Add(ctx, kanbanActor, taskID, text, beforeID)
}

func (m *kanbanModule) ChecklistUpdate(ctx context.Context, projectID, itemID, text string) (kanban.ChecklistItem, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.ChecklistItem{}, err
	}
	return mod.Checklist().Update(ctx, kanbanActor, itemID, text)
}

func (m *kanbanModule) ChecklistSetDone(ctx context.Context, projectID, itemID string, done bool) (kanban.ChecklistItem, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.ChecklistItem{}, err
	}
	return mod.Checklist().SetDone(ctx, kanbanActor, itemID, done)
}

func (m *kanbanModule) ChecklistRemove(ctx context.Context, projectID, itemID string) error {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return err
	}
	return mod.Checklist().Remove(ctx, kanbanActor, itemID)
}

func (m *kanbanModule) ChecklistReorder(ctx context.Context, projectID, taskID string, orderedIDs []string) error {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return err
	}
	return mod.Checklist().Reorder(ctx, kanbanActor, taskID, orderedIDs)
}

func (m *kanbanModule) AddLabel(ctx context.Context, projectID, taskID, label string, expected *kanban.Version) (kanban.Task, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.Task{}, err
	}
	return mod.Labels().Add(ctx, kanbanActor, taskID, label, expected)
}

func (m *kanbanModule) RemoveLabel(ctx context.Context, projectID, taskID, label string, expected *kanban.Version) (kanban.Task, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return kanban.Task{}, err
	}
	return mod.Labels().Remove(ctx, kanbanActor, taskID, label, expected)
}

func (m *kanbanModule) Links(ctx context.Context, projectID, taskID string) ([]kanban.EntityLink, error) {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return mod.Links().List(ctx, taskID)
}

// AddLink relates the task to a target. The UI only offers related_to
// (SPEC-08 §5.6); deliverable_of stays for the module that owns deliverables.
func (m *kanbanModule) AddLink(ctx context.Context, projectID, taskID string, dstType schema.EntityType, dstID string) error {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return err
	}
	_, err = mod.Links().Add(ctx, taskID, schema.RelationRelatedTo, dstType, dstID, kanbanActor)
	return err
}

func (m *kanbanModule) RemoveLink(ctx context.Context, projectID, taskID string, dstType schema.EntityType, dstID string) error {
	mod, err := m.module(ctx, projectID)
	if err != nil {
		return err
	}
	return mod.Links().Remove(ctx, taskID, schema.RelationRelatedTo, dstType, dstID, kanbanActor)
}

// LinkCandidates groups the project's notes, ideas, experiments and runs for
// the relation picker. The lists are the modules' own read projections: the
// picker never queries SQLite of its own.
func (m *kanbanModule) LinkCandidates(ctx context.Context, projectID string) ([]pages.LinkCandidate, error) {
	var out []pages.LinkCandidate
	notesSvc := notes.NewNoteService(m.ws, projectID, notes.ServiceOptions{Clock: clock.System{}})
	if page, err := notesSvc.List(ctx, notes.ListNotesQuery{Limit: 200}); err == nil {
		for _, note := range page.Notes {
			out = append(out, pages.LinkCandidate{Type: schema.EntityNote, ID: note.ID, Title: note.Title})
		}
	}
	ideaSvc := ideas.New(m.ws, projectID, ideas.Options{
		Clock:   clock.System{},
		Sources: ideas.NewSourceResolver(m.ws),
	})
	if page, err := ideaSvc.ListIdeas(ctx, ideas.ListIdeasQuery{PageSize: 200}); err == nil {
		for _, idea := range page.Items {
			out = append(out, pages.LinkCandidate{Type: schema.EntityIdea, ID: idea.ID, Title: idea.Title})
		}
	}
	expSvc := experiments.New(m.ws, projectID, experiments.Options{Clock: clock.System{}})
	exps, err := expSvc.ListExperiments(ctx, experiments.ExperimentFilter{Limit: 50})
	if err != nil {
		return out, nil
	}
	for _, exp := range exps {
		out = append(out, pages.LinkCandidate{Type: schema.EntityExperiment, ID: exp.ID, Title: exp.Title})
	}
	for _, exp := range exps {
		runs, err := expSvc.ListRuns(ctx, exp.ID, false)
		if err != nil {
			continue
		}
		for _, run := range runs {
			out = append(out, pages.LinkCandidate{
				Type:  schema.EntityRun,
				ID:    run.ID,
				Title: exp.Title + " · " + run.DisplayName,
			})
		}
	}
	return out, nil
}
