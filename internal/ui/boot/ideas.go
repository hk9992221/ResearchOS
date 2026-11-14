package boot

// The Ideas page reaches SPEC-06 through this adapter, the same way the other
// pages reach their module: the composition root builds the service, the page
// only calls the narrow interface. Construction mirrors the MCP binding
// (internal/mcpbind/ideas.go) so the two entry points behave the same, and
// every call acts as ActorUser.
//
// The SPEC-02 generation stack needs one output service and one generation
// service per project database, so the adapter caches them by project id; a
// workspace without a stack (the AI build failed) still constructs the module
// with a nil ChatClient, which the service documents as ai.disabled.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"researchos/internal/ai/output"
	"researchos/internal/ai/wire"
	"researchos/internal/foundation/clock"
	fworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/ideas"
	"researchos/internal/ui/pages"
)

// ideaCandidateLimit caps one candidate query: a picker is a short list, not a
// browser.
const ideaCandidateLimit = 20

// ideasModule serves one open workspace. The Ideas service is bound to a
// project, so a call resolves the project's service from the workspace; the
// adapter holds no project database handle of its own beyond the cached
// generation stack.
type ideasModule struct {
	ws    *fworkspace.Workspace
	stack *wire.Stack
	// ready reports whether a chat provider is assigned; nil disables the AI
	// entry, exactly like a nil stack.
	ready func() bool

	mu   sync.Mutex
	gens map[string]output.GenerationService
}

var _ pages.IdeasAPI = (*ideasModule)(nil)

func newIdeasModule(ws *fworkspace.Workspace, stack *wire.Stack, ready func() bool) *ideasModule {
	return &ideasModule{ws: ws, stack: stack, ready: ready, gens: map[string]output.GenerationService{}}
}

// service returns the Ideas service of one project.
func (m *ideasModule) service(ctx context.Context, projectID string) ideas.Service {
	opts := ideas.Options{
		Clock:   clock.System{},
		Sources: ideas.NewSourceResolver(m.ws),
		Logger:  appLogger{},
	}
	if gen := m.generation(ctx, projectID); gen != nil {
		opts.AI = ideas.NewSpec02ChatClient(gen, projectID)
	}
	return ideas.New(m.ws, projectID, opts)
}

// generation returns the per-project SPEC-02 generation service, building it
// once. A nil stack, or a project whose database cannot be resolved, disables
// the AI skill only: manual evidence keeps working.
func (m *ideasModule) generation(ctx context.Context, projectID string) output.GenerationService {
	if m == nil || m.stack == nil || m.stack.Gateway == nil {
		return nil
	}
	m.mu.Lock()
	if gen, ok := m.gens[projectID]; ok {
		m.mu.Unlock()
		return gen
	}
	m.mu.Unlock()

	handle, err := m.ws.Project(ctx, projectID)
	if err != nil {
		return nil
	}
	readOnly := output.NewProjectReadOnly(m.ws.Global().DB(), projectID)
	outputs, err := output.NewOutputService(handle.DB(), output.OutputOptions{
		Clock:     clock.System{},
		ProjectID: projectID,
		ReadOnly:  readOnly,
	})
	if err != nil {
		return nil
	}
	gen, err := output.NewGenerationService(output.GenerationOptions{
		Outputs:  outputs,
		Runner:   m.stack.Gateway,
		Events:   handle.Events(),
		ReadOnly: readOnly,
		Logger:   func(msg string, kv ...any) { appLogger{}.Warn(msg, kv...) },
	})
	if err != nil {
		return nil
	}
	m.mu.Lock()
	if m.gens == nil {
		m.gens = map[string]output.GenerationService{}
	}
	m.gens[projectID] = gen
	m.mu.Unlock()
	return gen
}

// AIAvailable reports whether a chat provider is assigned for this workspace:
// the SPEC-02 stack must exist and chat_main must resolve locally. A false
// value disables the evidence skill with the 去设置 hint (SPEC-06 §6.6).
func (m *ideasModule) AIAvailable() bool {
	return m != nil && m.stack != nil && m.stack.Gateway != nil && m.ready != nil && m.ready()
}

func (m *ideasModule) CreateIdea(ctx context.Context, projectID string, in ideas.CreateIdeaInput) (*ideas.Idea, error) {
	return m.service(ctx, projectID).CreateIdea(ctx, in)
}

func (m *ideasModule) UpdateIdea(ctx context.Context, projectID string, in ideas.UpdateIdeaInput) (*ideas.Idea, error) {
	return m.service(ctx, projectID).UpdateIdea(ctx, in)
}

func (m *ideasModule) GetIdea(ctx context.Context, projectID, ideaID string) (*ideas.IdeaDetail, error) {
	return m.service(ctx, projectID).GetIdea(ctx, ideaID)
}

func (m *ideasModule) ListIdeas(ctx context.Context, projectID string, q ideas.ListIdeasQuery) (ideas.Page[ideas.IdeaSummary], error) {
	return m.service(ctx, projectID).ListIdeas(ctx, q)
}

func (m *ideasModule) DeleteIdea(ctx context.Context, projectID, ideaID string, actor ideas.Actor) error {
	return m.service(ctx, projectID).DeleteIdea(ctx, ideaID, actor)
}

func (m *ideasModule) ChangeStatus(ctx context.Context, projectID string, in ideas.ChangeStatusInput) (*ideas.Idea, error) {
	return m.service(ctx, projectID).ChangeStatus(ctx, in)
}

func (m *ideasModule) UpdateRejectionReason(ctx context.Context, projectID string, in ideas.UpdateRejectionReasonInput) (*ideas.Idea, error) {
	return m.service(ctx, projectID).UpdateRejectionReason(ctx, in)
}

func (m *ideasModule) LinkEntity(ctx context.Context, projectID string, in ideas.LinkInput) error {
	return m.service(ctx, projectID).LinkEntity(ctx, in)
}

func (m *ideasModule) UnlinkEntity(ctx context.Context, projectID string, in ideas.LinkInput) error {
	return m.service(ctx, projectID).UnlinkEntity(ctx, in)
}

func (m *ideasModule) ListRelations(ctx context.Context, projectID, ideaID string) (ideas.RelationSet, error) {
	return m.service(ctx, projectID).ListRelations(ctx, ideaID)
}

func (m *ideasModule) DeriveIdea(ctx context.Context, projectID string, in ideas.DeriveIdeaInput) (*ideas.Idea, error) {
	return m.service(ctx, projectID).DeriveIdea(ctx, in)
}

func (m *ideasModule) AddEvidence(ctx context.Context, projectID string, in ideas.AddEvidenceInput) (*ideas.Evidence, error) {
	return m.service(ctx, projectID).AddEvidence(ctx, in)
}

func (m *ideasModule) UpdateEvidence(ctx context.Context, projectID string, in ideas.UpdateEvidenceInput) (*ideas.Evidence, error) {
	return m.service(ctx, projectID).UpdateEvidence(ctx, in)
}

func (m *ideasModule) ConfirmEvidence(ctx context.Context, projectID string, in ideas.ConfirmEvidenceInput) (ideas.ConfirmEvidenceResult, error) {
	return m.service(ctx, projectID).ConfirmEvidence(ctx, in)
}

func (m *ideasModule) DiscardEvidence(ctx context.Context, projectID string, in ideas.DiscardEvidenceInput) (ideas.DiscardEvidenceResult, error) {
	return m.service(ctx, projectID).DiscardEvidence(ctx, in)
}

func (m *ideasModule) DeleteEvidence(ctx context.Context, projectID, evidenceID string, actor ideas.Actor) error {
	return m.service(ctx, projectID).DeleteEvidence(ctx, evidenceID, actor)
}

func (m *ideasModule) ListEvidence(ctx context.Context, projectID string, q ideas.ListEvidenceQuery) (ideas.Page[ideas.EvidenceView], error) {
	return m.service(ctx, projectID).ListEvidence(ctx, q)
}

func (m *ideasModule) GenerateEvidenceDrafts(ctx context.Context, projectID string, in ideas.GenerateEvidenceInput) (*ideas.GenerateEvidenceResult, error) {
	return m.service(ctx, projectID).GenerateEvidenceDrafts(ctx, in)
}

func (m *ideasModule) History(ctx context.Context, projectID string, q ideas.HistoryQuery) (ideas.Page[ideas.HistoryEntry], error) {
	return m.service(ctx, projectID).History(ctx, q)
}

// ResolveSource previews one source through the same resolver the skill uses,
// so the picker shows the text a generation would read.
func (m *ideasModule) ResolveSource(ctx context.Context, projectID string, ref ideas.SourceRef) (ideas.ResolvedSource, error) {
	return ideas.NewSourceResolver(m.ws).Resolve(ctx, projectID, ref)
}

// LinkCandidates lists the live papers, notes, experiments and runs of the
// project, filtered by title text. The tables are the ones the Ideas service
// itself resolves at read time (internal/ideas/source.go), so a candidate the
// picker offers is one the write accepts.
func (m *ideasModule) LinkCandidates(ctx context.Context, projectID, targetType, text string) ([]ideas.EntityRef, error) {
	handle, err := m.ws.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	text = strings.TrimSpace(text)
	like := "%" + text + "%"
	switch targetType {
	case ideas.TargetPaper:
		return m.paperCandidates(ctx, handle.DB(), projectID, text)
	case ideas.TargetNote:
		return queryCandidates(ctx, handle.DB(),
			`SELECT id, title FROM notes
			 WHERE deleted_at IS NULL AND (? = '' OR title LIKE ?)
			 ORDER BY updated_at DESC LIMIT ?`,
			string(ideas.SourceNote), text, like)
	case ideas.TargetExperiment:
		return queryCandidates(ctx, handle.DB(),
			`SELECT id, title FROM experiments
			 WHERE deleted_at IS NULL AND (? = '' OR title LIKE ?)
			 ORDER BY updated_at DESC LIMIT ?`,
			string(ideas.SourceExperiment), text, like)
	case string(ideas.SourceRun):
		rows, err := handle.DB().QueryContext(ctx,
			`SELECT r.id, e.title FROM runs r JOIN experiments e ON e.id = r.experiment_id
			 WHERE r.deleted_at IS NULL AND e.deleted_at IS NULL AND (? = '' OR e.title LIKE ?)
			 ORDER BY r.created_at DESC LIMIT ?`, text, like, ideaCandidateLimit)
		if err != nil {
			return nil, fmt.Errorf("ideas: list runs: %w", err)
		}
		defer rows.Close()
		out := make([]ideas.EntityRef, 0, ideaCandidateLimit)
		for rows.Next() {
			var id, title string
			if err := rows.Scan(&id, &title); err != nil {
				return nil, fmt.Errorf("ideas: scan run: %w", err)
			}
			out = append(out, ideas.EntityRef{
				Type:  string(ideas.SourceRun),
				ID:    id,
				Title: runCandidateTitle(title, id),
			})
		}
		return out, rows.Err()
	}
	return nil, &ideas.ServiceError{
		Code:    ideas.ErrInvalidSource,
		Message: fmt.Sprintf("candidate type %q is not supported", targetType),
	}
}

// queryCandidates reads one id/title candidate query.
func queryCandidates(ctx context.Context, db *sql.DB, query, typ, text, like string) ([]ideas.EntityRef, error) {
	rows, err := db.QueryContext(ctx, query, text, like, ideaCandidateLimit)
	if err != nil {
		return nil, fmt.Errorf("ideas: list %s candidates: %w", typ, err)
	}
	defer rows.Close()
	out := make([]ideas.EntityRef, 0, ideaCandidateLimit)
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, fmt.Errorf("ideas: scan %s candidate: %w", typ, err)
		}
		out = append(out, ideas.EntityRef{Type: typ, ID: id, Title: title})
	}
	return out, rows.Err()
}

// paperCandidates lists the papers linked to the project. The link lives in
// project.db and the title in global.db, so the two are read separately.
func (m *ideasModule) paperCandidates(ctx context.Context, projectDB *sql.DB, projectID, text string) ([]ideas.EntityRef, error) {
	rows, err := projectDB.QueryContext(ctx,
		`SELECT paper_id FROM project_papers WHERE project_id = ? ORDER BY linked_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("ideas: list project papers: %w", err)
	}
	ids := make([]string, 0, ideaCandidateLimit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("ideas: scan project paper: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("ideas: list project papers: %w", err)
	}
	rows.Close()

	out := make([]ideas.EntityRef, 0, len(ids))
	for _, id := range ids {
		if len(out) >= ideaCandidateLimit {
			break
		}
		var title string
		err := m.ws.Global().DB().QueryRowContext(ctx,
			`SELECT COALESCE(json_extract(metadata, '$.title'), '') FROM papers WHERE id = ?`, id).
			Scan(&title)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("ideas: read paper %s: %w", id, err)
		}
		if text != "" && !strings.Contains(strings.ToLower(title), strings.ToLower(text)) {
			continue
		}
		out = append(out, ideas.EntityRef{Type: ideas.TargetPaper, ID: id, Title: title})
	}
	return out, nil
}

// runCandidateTitle names a run the way the Ideas service names it at read
// time: the experiment title plus a short run id.
func runCandidateTitle(experimentTitle, runID string) string {
	short := runID
	if len(short) > 8 {
		short = short[:8]
	}
	if strings.TrimSpace(experimentTitle) == "" {
		return "Run " + short
	}
	return experimentTitle + " · Run " + short
}

// PaperURL resolves the Zotero selection URI of a paper: the library owns the
// item, so the page opens it there instead of opening a local page.
func (m *ideasModule) PaperURL(ctx context.Context, projectID, paperID string) (string, error) {
	var key string
	err := m.ws.Global().DB().QueryRowContext(ctx,
		`SELECT zotero_key FROM papers WHERE id = ?`, paperID).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && key == "") {
		return "", &ideas.ServiceError{
			Code:    ideas.ErrTargetNotFound,
			Message: fmt.Sprintf("paper %s has no Zotero key", paperID),
		}
	}
	if err != nil {
		return "", fmt.Errorf("ideas: read paper %s: %w", paperID, err)
	}
	return "zotero://select/library/items/" + key, nil
}
