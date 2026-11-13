package ideas

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"researchos/internal/ai/output"
	"researchos/internal/ai/protocol"
	"researchos/internal/foundation/workspace"
)

// NewSourceResolver wires the SPEC-06 skill's source port to the project
// database and the workspace note files (SPEC-04/05/07 are all present).
//
// experiment/run resolve to the experiment purpose plus the latest confirmed
// AI Results snapshot; note reads the Markdown body from disk. A missing or
// deleted target is reported through ResolvedSource.Deleted/Broken state or an
// error, never silently treated as empty.
func NewSourceResolver(ws *workspace.Workspace) SourceResolver {
	return &sourceResolver{ws: ws}
}

type sourceResolver struct {
	ws *workspace.Workspace
}

var _ SourceResolver = (*sourceResolver)(nil)

func (r *sourceResolver) Resolve(ctx context.Context, projectID string, ref SourceRef) (ResolvedSource, error) {
	if r.ws == nil {
		return ResolvedSource{}, errors.New("ideas: source resolver has no workspace")
	}
	handle, err := r.ws.Project(ctx, projectID)
	if err != nil {
		return ResolvedSource{}, err
	}
	out := ResolvedSource{Ref: ref}
	switch ref.Type {
	case SourceExperiment:
		return r.resolveExperiment(ctx, handle.DB(), ref, out)
	case SourceRun:
		return r.resolveRun(ctx, handle.DB(), ref, out)
	case SourceNote:
		return r.resolveNote(ctx, handle.DB(), ref, out)
	default:
		return ResolvedSource{}, fmt.Errorf("ideas: unsupported source type %q", ref.Type)
	}
}

func (r *sourceResolver) resolveExperiment(ctx context.Context, db *sql.DB, ref SourceRef, out ResolvedSource) (ResolvedSource, error) {
	var (
		title, purpose, updated string
		deleted                 sql.NullString
	)
	err := db.QueryRowContext(ctx,
		`SELECT title, purpose, COALESCE(updated_at, ''), deleted_at FROM experiments WHERE id = ?`, ref.ID).
		Scan(&title, &purpose, &updated, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return ResolvedSource{}, serviceError(ErrSourceEmpty, "source %s/%s has no content", ref.Type, ref.ID)
	}
	if err != nil {
		return ResolvedSource{}, fmt.Errorf("ideas: read experiment source: %w", err)
	}
	out.Title = title
	out.Deleted = deleted.Valid
	out.Version = updated
	analysis, err := latestConfirmedResults(ctx, db, "experiment", ref.ID)
	if err != nil {
		return ResolvedSource{}, err
	}
	if strings.TrimSpace(analysis) != "" {
		out.Text = analysis
		return out, nil
	}
	if strings.TrimSpace(purpose) == "" {
		return ResolvedSource{}, serviceError(ErrSourceEmpty, "source %s/%s has no content", ref.Type, ref.ID)
	}
	out.Text = purpose
	return out, nil
}

func (r *sourceResolver) resolveRun(ctx context.Context, db *sql.DB, ref SourceRef, out ResolvedSource) (ResolvedSource, error) {
	var (
		experimentID, title, purpose, updated string
		runDeleted, expDeleted                sql.NullString
	)
	err := db.QueryRowContext(ctx,
		`SELECT r.experiment_id, e.title, e.purpose, COALESCE(r.updated_at, ''),
		        r.deleted_at, e.deleted_at
		   FROM runs r JOIN experiments e ON e.id = r.experiment_id
		  WHERE r.id = ?`, ref.ID).
		Scan(&experimentID, &title, &purpose, &updated, &runDeleted, &expDeleted)
	if errors.Is(err, sql.ErrNoRows) {
		return ResolvedSource{}, serviceError(ErrSourceEmpty, "source %s/%s has no content", ref.Type, ref.ID)
	}
	if err != nil {
		return ResolvedSource{}, fmt.Errorf("ideas: read run source: %w", err)
	}
	out.Title = runTitle(title, ref.ID)
	out.Deleted = runDeleted.Valid || expDeleted.Valid
	out.Version = updated
	analysis, err := latestConfirmedResults(ctx, db, "run", ref.ID)
	if err != nil {
		return ResolvedSource{}, err
	}
	if strings.TrimSpace(analysis) != "" {
		out.Text = analysis
		return out, nil
	}
	if strings.TrimSpace(purpose) == "" {
		return ResolvedSource{}, serviceError(ErrSourceEmpty, "source %s/%s has no content", ref.Type, ref.ID)
	}
	out.Text = purpose
	return out, nil
}

func (r *sourceResolver) resolveNote(ctx context.Context, db *sql.DB, ref SourceRef, out ResolvedSource) (ResolvedSource, error) {
	var (
		title, filePath, state, updated string
		deleted                         sql.NullString
	)
	err := db.QueryRowContext(ctx,
		`SELECT title, file_path, file_state, updated_at, deleted_at FROM notes WHERE id = ?`, ref.ID).
		Scan(&title, &filePath, &state, &updated, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return ResolvedSource{}, serviceError(ErrSourceEmpty, "source %s/%s has no content", ref.Type, ref.ID)
	}
	if err != nil {
		return ResolvedSource{}, fmt.Errorf("ideas: read note source: %w", err)
	}
	out.Title = title
	out.Deleted = deleted.Valid
	out.Version = updated
	if state == "missing" {
		return ResolvedSource{}, fmt.Errorf("ideas: note %s body is missing", ref.ID)
	}
	abs, err := r.ws.Paths().Abs(filePath)
	if err != nil {
		return ResolvedSource{}, fmt.Errorf("ideas: note %s path: %w", ref.ID, err)
	}
	body, err := os.ReadFile(filepath.Clean(abs))
	if err != nil {
		return ResolvedSource{}, fmt.Errorf("ideas: read note %s: %w", ref.ID, err)
	}
	if strings.TrimSpace(string(body)) == "" {
		return ResolvedSource{}, serviceError(ErrSourceEmpty, "source %s/%s has no content", ref.Type, ref.ID)
	}
	out.Text = string(body)
	return out, nil
}

// latestConfirmedResults returns the content of the newest confirmed
// experiment_results AI output for one target.
func latestConfirmedResults(ctx context.Context, db *sql.DB, targetType, targetID string) (string, error) {
	var content string
	err := db.QueryRowContext(ctx,
		`SELECT o.content_md
		   FROM snapshots s
		   JOIN ai_outputs o ON o.id = s.ai_output_id
		  WHERE s.target_type = ? AND s.target_id = ?
		    AND o.status = 'confirmed' AND o.task_kind = 'experiment_results'
		  ORDER BY s.created_at DESC, s.id DESC LIMIT 1`, targetType, targetID).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("ideas: read confirmed analysis: %w", err)
	}
	return content, nil
}

// NewSpec02ChatClient adapts SPEC-02's GenerationService onto the ideas
// ChatClient port. SPEC-02 owns the single repair round; this adapter disables
// any additional repair (SPEC-06 §2.3, §5.7.3 rule 1).
func NewSpec02ChatClient(gen output.GenerationService, projectID string) ChatClient {
	return &spec02ChatClient{gen: gen, projectID: projectID}
}

type spec02ChatClient struct {
	gen       output.GenerationService
	projectID string
}

var _ ChatClient = (*spec02ChatClient)(nil)

func (c *spec02ChatClient) CompleteStructured(ctx context.Context, req StructuredRequest) (StructuredResponse, error) {
	if c.gen == nil {
		return StructuredResponse{}, errors.New("ideas: SPEC-02 generation service is nil")
	}
	res, err := c.gen.CompleteStructured(ctx, output.StructuredRequest{
		ProjectID:     c.projectID,
		TaskKind:      "evidence_extraction",
		PromptVersion: promptVersion,
		Messages: []protocol.ChatMessage{
			{Role: protocol.RoleSystem, Content: req.System},
			{Role: protocol.RoleUser, Content: req.Prompt},
		},
		JSONSchema:      json.RawMessage(req.JSONSchema),
		SchemaName:      "evidence",
		MaxOutputTokens: req.MaxOutputTokens,
	})
	if err != nil {
		return StructuredResponse{}, err
	}
	resp := StructuredResponse{
		Content:  res.Content,
		Provider: res.Output.Provenance.ProviderID,
		Model:    res.Output.Provenance.ModelID,
	}
	if res.Output.Usage != nil {
		resp.InputTokens = res.Output.Usage.InputTokens
		resp.OutputTokens = res.Output.Usage.OutputTokens
	}
	return resp, nil
}
