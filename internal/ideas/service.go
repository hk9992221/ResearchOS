package ideas

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/indexhook"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
)

// Field limits of SPEC-06 §4.
const (
	maxTitleRunes       = 200
	maxDescriptionRunes = 100000
	maxReasonRunes      = 4000
	maxContentRunes     = 8000
)

// Options configures the Ideas service.
type Options struct {
	// Clock supplies timestamps and UUIDv7 prefixes. Nil means clock.System.
	Clock clock.Clock
	// AI is the SPEC-02 structured-chat adapter. Nil disables the skill with
	// ai.disabled and leaves manual evidence fully usable.
	AI ChatClient
	// Sources resolves experiment/run/note text. Nil makes the skill fail with
	// evidence.source_unavailable, which is the documented degradation until
	// SPEC-05/07 adapters are wired (SPEC-06 §2.1).
	Sources SourceResolver
	// Indexer is the post-commit search-index hook. A nil indexer disables
	// indexing; its failures never fail a business write.
	Indexer indexhook.Indexer
	// Logger receives non-fatal warnings.
	Logger workspace.Logger
}

type service struct {
	ws        *workspace.Workspace
	projectID string
	clock     clock.Clock
	ai        ChatClient
	sources   SourceResolver
	indexer   indexhook.Indexer
	logger    workspace.Logger
}

var _ Service = (*service)(nil)

// New binds the Ideas service to one project of ws. The project database is
// resolved lazily, so the service stays valid across re-opens.
func New(ws *workspace.Workspace, projectID string, opt Options) Service {
	c := opt.Clock
	if c == nil {
		c = clock.System{}
	}
	return &service{
		ws:        ws,
		projectID: projectID,
		clock:     c,
		ai:        opt.AI,
		sources:   opt.Sources,
		indexer:   opt.Indexer,
		logger:    opt.Logger,
	}
}

// handle resolves the project handle.
func (s *service) handle(ctx context.Context) (*workspace.ProjectHandle, error) {
	if s.ws == nil {
		return nil, errors.New("ideas: service has no workspace")
	}
	return s.ws.Project(ctx, s.projectID)
}

// begin resolves the project handle and enforces the archived read-only rule
// for a write operation.
func (s *service) begin(ctx context.Context) (*workspace.ProjectHandle, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireWritable(ctx); err != nil {
		return nil, err
	}
	return handle, nil
}

// requireWritable rejects writes on an archived project (SPEC-06 §5.2).
func (s *service) requireWritable(ctx context.Context) error {
	if s.ws == nil || s.ws.Global() == nil {
		return errors.New("ideas: service has no global database")
	}
	var status string
	err := s.ws.Global().DB().QueryRowContext(ctx,
		`SELECT status FROM projects WHERE id = ?`, s.projectID).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return serviceError(ErrProjectNotFound, "project %s does not exist", s.projectID)
	case err != nil:
		return fmt.Errorf("ideas: read project status: %w", err)
	case schema.ProjectStatus(status) == schema.ProjectArchived:
		return serviceError(ErrProjectReadOnly, "project %s is archived", s.projectID)
	default:
		return nil
	}
}

// writeActor resolves the actor of a write. An empty actor means the in-app
// user; ai is never accepted (SPEC-06 §4.2.4, AC-31).
func writeActor(a events.Actor) (events.Actor, error) {
	switch a {
	case "":
		return schema.ActorUser, nil
	case schema.ActorUser, schema.ActorMCP:
		return a, nil
	default:
		return "", serviceError(ErrInvalidActor, "actor %q may not perform this write", string(a))
	}
}

func (s *service) now() string { return clock.Format(s.clock.Now()) }

func (s *service) newID() (string, error) {
	id, err := ids.New(s.clock)
	if err != nil {
		return "", fmt.Errorf("ideas: generate id: %w", err)
	}
	return id, nil
}

// normalizeTitle trims and validates an Idea title (1..200 runes).
func normalizeTitle(raw string) (string, error) {
	title := strings.TrimSpace(raw)
	if title == "" {
		return "", serviceError(ErrInvalidInput, "title must not be empty")
	}
	if n := utf8.RuneCountInString(title); n > maxTitleRunes {
		return "", serviceError(ErrInvalidInput, "title must be at most %d runes, got %d", maxTitleRunes, n)
	}
	return title, nil
}

func validateDescription(md string) error {
	if n := utf8.RuneCountInString(md); n > maxDescriptionRunes {
		return serviceError(ErrInvalidInput, "description must be at most %d runes, got %d", maxDescriptionRunes, n)
	}
	return nil
}

func normalizeReason(raw string) (string, error) {
	reason := strings.TrimSpace(raw)
	if reason == "" {
		return "", serviceError(ErrRejectionReasonRequired, "a rejection reason is required")
	}
	if n := utf8.RuneCountInString(reason); n > maxReasonRunes {
		return "", serviceError(ErrInvalidInput, "rejection reason must be at most %d runes, got %d", maxReasonRunes, n)
	}
	return reason, nil
}

func normalizeContent(raw string) (string, error) {
	content := strings.TrimSpace(raw)
	if content == "" {
		return "", serviceError(ErrInvalidInput, "evidence content must not be empty")
	}
	if n := utf8.RuneCountInString(content); n > maxContentRunes {
		return "", serviceError(ErrInvalidInput, "evidence content must be at most %d runes, got %d", maxContentRunes, n)
	}
	return content, nil
}

// ideaFrom scans one ideas row.
func ideaFrom(row interface{ Scan(dest ...any) error }) (*Idea, error) {
	var (
		idea      Idea
		status    string
		createdAt string
		updatedAt string
		deletedAt sql.NullString
	)
	if err := row.Scan(&idea.ID, &idea.Title, &idea.DescriptionMD, &status,
		&idea.RejectionReason, &createdAt, &updatedAt, &deletedAt); err != nil {
		return nil, err
	}
	idea.Status = Status(status)
	at, err := clock.Parse(createdAt)
	if err != nil {
		return nil, fmt.Errorf("ideas: created_at: %w", err)
	}
	idea.CreatedAt = at
	at, err = clock.Parse(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("ideas: updated_at: %w", err)
	}
	idea.UpdatedAt = at
	if deletedAt.Valid {
		t, err := clock.Parse(deletedAt.String)
		if err != nil {
			return nil, fmt.Errorf("ideas: deleted_at: %w", err)
		}
		idea.DeletedAt = &t
	}
	return &idea, nil
}

const ideaColumns = `id, title, description, status, rejection_reason, created_at, updated_at, deleted_at`

// requireIdeaTx reads one Idea inside tx. A soft-deleted Idea yields
// ErrIdeaDeleted unless includeDeleted is set.
func requireIdeaTx(ctx context.Context, tx *sql.Tx, id string, includeDeleted bool) (*Idea, error) {
	if !ids.Valid(id) {
		return nil, serviceError(ErrIdeaNotFound, "idea %q not found", id)
	}
	idea, err := ideaFrom(tx.QueryRowContext(ctx,
		`SELECT `+ideaColumns+` FROM ideas WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, serviceError(ErrIdeaNotFound, "idea %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("ideas: read idea %s: %w", id, err)
	}
	if idea.DeletedAt != nil && !includeDeleted {
		return nil, serviceError(ErrIdeaDeleted, "idea %s is deleted", id)
	}
	return idea, nil
}

// notifyIndex fires the post-commit index hook. Failures are warnings only.
func (s *service) notifyIndex(ctx context.Context, entityType schema.EntityType, entityID string, typ schema.EventType) {
	if s.indexer == nil {
		return
	}
	ev := events.Activity{
		Type:       typ,
		EntityType: entityType,
		EntityID:   entityID,
		Payload: events.Payload{
			EntityType: entityType,
			EntityID:   entityID,
			Summary:    "index hook",
			Actor:      schema.ActorUser,
		},
	}
	if err := s.indexer.OnEntityChanged(ctx, ev); err != nil && s.logger != nil {
		s.logger.Warn("ideas: index hook failed", "entity_id", entityID, "event", string(typ), "error", err)
	}
}

// placeholders returns n comma-separated bind parameters.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// stringPlaceholders returns a SQL list literal for a fixed, safe enum list.
func anyArgs(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}
