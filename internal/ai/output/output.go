// Package output owns the draft → confirmed AI output lifecycle, provenance
// persistence and the shared generation entry points (SPEC-02 §5.9). AI
// rows are immutable generation attempts: regeneration appends a new draft
// and confirmed content never downgrades.
package output

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"researchos/internal/ai/gateway"
	"researchos/internal/ai/provider"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
)

// OutputStatus is the review state of generated content (§4.2).
type OutputStatus string

// Registered statuses.
const (
	OutputDraft     OutputStatus = "draft"
	OutputConfirmed OutputStatus = "confirmed"
)

// Actor identifies who performs an action on an output (§4.2).
type Actor string

// Registered actors.
const (
	ActorUser Actor = "user"
	ActorMCP  Actor = "mcp"
	ActorAI   Actor = "ai"
)

// Validation and state-machine sentinels.
var (
	ErrNotFound                     = errors.New("ai output not found")
	ErrInvalidPromptVersion         = errors.New("invalid_prompt_version")
	ErrUnknownTaskKind              = errors.New("unregistered task_kind")
	ErrConfirmationRequiresUser     = errors.New("confirmation_requires_user")
	ErrConfirmedImmutableForMachine = errors.New("confirmed_immutable_for_machine")
	ErrProjectReadOnly              = errors.New("project is archived; AI generation, confirmation and editing are read-only")
	ErrInvalidEntity                = errors.New("entity binding must reference a registered entity type with a valid id")
	ErrSchemaRequired               = errors.New("structured requests require a JSON schema")
	ErrOutputValidation             = errors.New("ai output validation failed")
)

// EntityRef binds an output to a consuming module entity.
type EntityRef struct {
	Type string
	ID   string
}

// SourceRef is one provenance source; locators follow SPEC-10 semantics and
// are only displayed (§7.2).
type SourceRef struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Locator    string `json:"locator,omitempty"`
	Title      string `json:"title,omitempty"`
}

// Provenance answers who/when/which prompt version/on which sources (§7.3).
type Provenance struct {
	ProviderID    string
	ProviderName  string
	Protocol      provider.Protocol
	ModelID       string
	PromptVersion string
	SourceRefs    []SourceRef
	Parameters    map[string]any
	GeneratedAt   time.Time
}

// AIOutput is one persisted generation attempt.
type AIOutput struct {
	ID          string
	ProjectID   string
	TaskKind    string
	Entity      *EntityRef
	ContentMD   string
	Status      OutputStatus
	Provenance  Provenance
	Usage       *gateway.Usage
	HumanEdited bool
	CreatedAt   time.Time
	ConfirmedAt *time.Time
}

// CreateDraftInput feeds one persisted draft.
type CreateDraftInput struct {
	ProjectID  string
	TaskKind   string
	Entity     *EntityRef
	ContentMD  string
	Provenance Provenance
	Usage      *gateway.Usage
}

// OutputPersistError wraps a persistence failure after a successful
// generation. The full input travels with it so the UI can retry saving
// without losing content (§5.9.3).
type OutputPersistError struct {
	Input CreateDraftInput
	Cause error
}

func (e *OutputPersistError) Error() string {
	return "ai output persist failed: " + e.Cause.Error()
}

func (e *OutputPersistError) Unwrap() error { return e.Cause }

// AIOutputService manages the output rows and the state machine.
type AIOutputService interface {
	CreateDraft(ctx context.Context, in CreateDraftInput) (AIOutput, error)
	Get(ctx context.Context, id string) (AIOutput, error)
	ListByEntity(ctx context.Context, ref EntityRef) ([]AIOutput, error)
	UpdateContent(ctx context.Context, id, contentMD string, actor Actor) (AIOutput, error)
	Confirm(ctx context.Context, id string, actor Actor) (AIOutput, error)
	// ConfirmTx confirms inside a caller project.db transaction so module
	// entities and the output flip atomically (§5.9 rule 5).
	ConfirmTx(ctx context.Context, tx *sql.Tx, id string, actor Actor) (AIOutput, error)
}

// taskKinds is the code-side prompt/task registry (§4.2, O-10); there is no
// prompts table and prompt text is never stored.
var taskKinds = map[string]bool{
	"evidence_extraction":    true,
	"experiment_outline":     true,
	"experiment_results":     true,
	"weekly_report":          true,
	"document_understanding": true,
	"other":                  true,
}

// RegisterTaskKinds adds kinds for modules that own their prompt constants
// (O-10). Call only during initialization.
func RegisterTaskKinds(kinds ...string) {
	for _, k := range kinds {
		taskKinds[k] = true
	}
}

// ValidTaskKind reports registry membership.
func ValidTaskKind(k string) bool { return taskKinds[k] }

// OutputOptions configures the output service (one project.db).
type OutputOptions struct {
	Clock clock.Clock
	// ProjectID names the project this service is bound to; drafts must
	// carry the same id (ai_outputs rows live inside that project.db).
	ProjectID string
	// ReadOnly, when non-nil, returns an error while the project is
	// archived; creation, editing and confirmation are then blocked (§5.9
	// rules 5–6).
	ReadOnly func(ctx context.Context) error
}

type outputStore struct {
	db    *sql.DB
	clock clock.Clock
	opt   OutputOptions
}

var _ AIOutputService = (*outputStore)(nil)

// NewOutputService returns the AI output service over one project.db.
func NewOutputService(db *sql.DB, opt OutputOptions) (AIOutputService, error) {
	if db == nil {
		return nil, errors.New("output: nil database")
	}
	c := opt.Clock
	if c == nil {
		c = clock.System{}
	}
	return &outputStore{db: db, clock: c, opt: opt}, nil
}

// NewProjectReadOnly builds the archived-project check against global.db
// (SPEC-00 §9: archived projects are read-only).
func NewProjectReadOnly(globalDB *sql.DB, projectID string) func(context.Context) error {
	return func(ctx context.Context) error {
		var status string
		err := globalDB.QueryRowContext(ctx, `SELECT status FROM projects WHERE id = ?`, projectID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("output: project %s not found", projectID)
		}
		if err != nil {
			return fmt.Errorf("output: project status: %w", err)
		}
		if status == string(schema.ProjectArchived) {
			return ErrProjectReadOnly
		}
		return nil
	}
}

func (s *outputStore) checkWritable(ctx context.Context) error {
	if s.opt.ReadOnly != nil {
		return s.opt.ReadOnly(ctx)
	}
	return nil
}

// CreateDraft validates the provenance floor and inserts one draft row.
// It is called by GenerationService and trusted importers, never by UI/MCP
// directly (§5.9 rule 2).
func (s *outputStore) CreateDraft(ctx context.Context, in CreateDraftInput) (AIOutput, error) {
	if err := s.checkWritable(ctx); err != nil {
		return AIOutput{}, err
	}
	if err := validateDraft(in, s.opt.ProjectID); err != nil {
		return AIOutput{}, err
	}
	id, err := ids.New(s.clock)
	if err != nil {
		return AIOutput{}, fmt.Errorf("output: id: %w", err)
	}
	now := s.clock.Now().UTC()
	prov := in.Provenance
	if prov.GeneratedAt.IsZero() {
		prov.GeneratedAt = now
	}
	refsJSON := "[]"
	if len(prov.SourceRefs) > 0 {
		raw, err := json.Marshal(prov.SourceRefs)
		if err != nil {
			return AIOutput{}, err
		}
		refsJSON = string(raw)
	}
	params := prov.Parameters
	if params == nil {
		params = map[string]any{}
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return AIOutput{}, err
	}
	var usageJSON any
	if in.Usage != nil {
		raw, err := json.Marshal(in.Usage)
		if err != nil {
			return AIOutput{}, err
		}
		usageJSON = string(raw)
	}
	var entType, entID any
	if in.Entity != nil {
		entType, entID = in.Entity.Type, in.Entity.ID
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO ai_outputs (id, task_kind, entity_type, entity_id, status, content_md,
			provider_id, provider_name, protocol, model_id, prompt_version, source_refs,
			parameters, usage, human_edited, created_at)
		 VALUES (?, ?, ?, ?, 'draft', ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		id, in.TaskKind, entType, entID, in.ContentMD,
		prov.ProviderID, prov.ProviderName, string(prov.Protocol), prov.ModelID,
		prov.PromptVersion, refsJSON, string(paramsJSON), usageJSON, clock.Format(now))
	if err != nil {
		return AIOutput{}, fmt.Errorf("output: insert: %w", err)
	}
	return s.Get(ctx, id)
}

func validateDraft(in CreateDraftInput, bound string) error {
	if !ValidTaskKind(in.TaskKind) {
		return fmt.Errorf("%w: %q", ErrUnknownTaskKind, in.TaskKind)
	}
	if in.Provenance.PromptVersion == "" {
		return ErrInvalidPromptVersion
	}
	if !provider.ValidProtocol(in.Provenance.Protocol) {
		return fmt.Errorf("%w: %q", provider.ErrInvalidProtocol, in.Provenance.Protocol)
	}
	if in.Provenance.ProviderID == "" || in.Provenance.ModelID == "" {
		return errors.New("output: provenance requires provider and model ids")
	}
	if in.ProjectID == "" {
		return errors.New("output: project id is required")
	}
	if bound != "" && in.ProjectID != bound {
		return fmt.Errorf("output: draft project %q does not match bound project %q", in.ProjectID, bound)
	}
	if in.Entity != nil {
		if in.Entity.Type == "" || in.Entity.ID == "" {
			return ErrInvalidEntity
		}
		if !schema.EntityType(in.Entity.Type).Valid() || !ids.Valid(in.Entity.ID) {
			return fmt.Errorf("%w: %s/%s", ErrInvalidEntity, in.Entity.Type, in.Entity.ID)
		}
	}
	return nil
}

const outputSelect = `SELECT id, task_kind, entity_type, entity_id, status, content_md,
	provider_id, provider_name, protocol, model_id, prompt_version, source_refs,
	parameters, usage, human_edited, created_at, confirmed_at FROM ai_outputs`

func scanOutput(row interface{ Scan(...any) error }) (AIOutput, error) {
	var (
		o                    AIOutput
		entType, entID       sql.NullString
		status, proto        string
		refsJSON, paramsJSON string
		usageJSON            sql.NullString
		humanEdited          int64
		created              string
		confirmed            sql.NullString
	)
	err := row.Scan(&o.ID, &o.TaskKind, &entType, &entID, &status, &o.ContentMD,
		&o.Provenance.ProviderID, &o.Provenance.ProviderName, &proto, &o.Provenance.ModelID,
		&o.Provenance.PromptVersion, &refsJSON, &paramsJSON, &usageJSON,
		&humanEdited, &created, &confirmed)
	if err != nil {
		return o, err
	}
	o.Status = OutputStatus(status)
	o.Provenance.Protocol = provider.Protocol(proto)
	o.HumanEdited = humanEdited != 0
	if entType.Valid && entID.Valid {
		o.Entity = &EntityRef{Type: entType.String, ID: entID.String}
	}
	if err := json.Unmarshal([]byte(refsJSON), &o.Provenance.SourceRefs); err != nil {
		return o, fmt.Errorf("output: source_refs %s: %w", o.ID, err)
	}
	o.Provenance.Parameters = map[string]any{}
	if paramsJSON != "" {
		if err := json.Unmarshal([]byte(paramsJSON), &o.Provenance.Parameters); err != nil {
			return o, fmt.Errorf("output: parameters %s: %w", o.ID, err)
		}
	}
	if usageJSON.Valid && usageJSON.String != "" {
		var u gateway.Usage
		if err := json.Unmarshal([]byte(usageJSON.String), &u); err != nil {
			return o, fmt.Errorf("output: usage %s: %w", o.ID, err)
		}
		o.Usage = &u
	}
	if o.CreatedAt, err = clock.Parse(created); err != nil {
		return o, err
	}
	o.Provenance.GeneratedAt = o.CreatedAt
	if confirmed.Valid {
		t, err := clock.Parse(confirmed.String)
		if err != nil {
			return o, err
		}
		o.ConfirmedAt = &t
	}
	return o, nil
}

// Get loads one output by id.
func (s *outputStore) Get(ctx context.Context, oID string) (AIOutput, error) {
	row := s.db.QueryRowContext(ctx, outputSelect+` WHERE id = ?`, oID)
	o, err := scanOutput(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AIOutput{}, fmt.Errorf("%w: %s", ErrNotFound, oID)
	}
	if err != nil {
		return AIOutput{}, fmt.Errorf("output: get: %w", err)
	}
	return o, nil
}

// ListByEntity returns every output bound to one entity, newest first;
// regeneration history stays visible (AC-06.4).
func (s *outputStore) ListByEntity(ctx context.Context, ref EntityRef) ([]AIOutput, error) {
	rows, err := s.db.QueryContext(ctx,
		outputSelect+` WHERE entity_type = ? AND entity_id = ? ORDER BY created_at DESC, id DESC`,
		ref.Type, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("output: list by entity: %w", err)
	}
	defer rows.Close()
	var out []AIOutput
	for rows.Next() {
		o, err := scanOutput(rows)
		if err != nil {
			return nil, err
		}
		o.ProjectID = s.opt.ProjectID
		out = append(out, o)
	}
	return out, rows.Err()
}

// UpdateContent applies the §5.9 edit rules: drafts accept user/mcp/ai
// edits (human_edited only for user), confirmed content accepts user edits
// only and keeps its status (§6.5).
func (s *outputStore) UpdateContent(ctx context.Context, oID, contentMD string, actor Actor) (AIOutput, error) {
	if err := s.checkWritable(ctx); err != nil {
		return AIOutput{}, err
	}
	if actor != ActorUser && actor != ActorMCP && actor != ActorAI {
		return AIOutput{}, fmt.Errorf("output: unknown actor %q", actor)
	}
	o, err := s.Get(ctx, oID)
	if err != nil {
		return AIOutput{}, err
	}
	if o.Status == OutputConfirmed && actor != ActorUser {
		return AIOutput{}, ErrConfirmedImmutableForMachine
	}
	human := 0
	if actor == ActorUser {
		human = 1
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE ai_outputs SET content_md = ?, human_edited = MAX(human_edited, ?) WHERE id = ?`,
		contentMD, human, oID)
	if err != nil {
		return AIOutput{}, fmt.Errorf("output: update content: %w", err)
	}
	return s.Get(ctx, oID)
}

// Confirm flips a draft to confirmed; only user actors may confirm (§5.9
// rule 5, AC-06.1). Re-confirming a confirmed output is idempotent.
func (s *outputStore) Confirm(ctx context.Context, oID string, actor Actor) (AIOutput, error) {
	if err := s.checkWritable(ctx); err != nil {
		return AIOutput{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AIOutput{}, fmt.Errorf("output: begin: %w", err)
	}
	out, err := confirmInTx(ctx, tx, oID, actor, s.clock, s.opt.ProjectID)
	if err != nil {
		_ = tx.Rollback()
		return AIOutput{}, err
	}
	if err := tx.Commit(); err != nil {
		return AIOutput{}, fmt.Errorf("output: commit: %w", err)
	}
	return out, nil
}

// ConfirmTx performs Confirm inside the caller's project.db transaction so
// module state and output state flip atomically (§5.9 rule 5, AC-06.2).
func (s *outputStore) ConfirmTx(ctx context.Context, tx *sql.Tx, oID string, actor Actor) (AIOutput, error) {
	if tx == nil {
		return AIOutput{}, errors.New("output: ConfirmTx requires a transaction")
	}
	if err := s.checkWritable(ctx); err != nil {
		return AIOutput{}, err
	}
	return confirmInTx(ctx, tx, oID, actor, s.clock, s.opt.ProjectID)
}

func confirmInTx(ctx context.Context, tx *sql.Tx, oID string, actor Actor, c clock.Clock, bound string) (AIOutput, error) {
	if actor != ActorUser {
		return AIOutput{}, ErrConfirmationRequiresUser
	}
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM ai_outputs WHERE id = ?`, oID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return AIOutput{}, fmt.Errorf("%w: %s", ErrNotFound, oID)
	}
	if err != nil {
		return AIOutput{}, fmt.Errorf("output: confirm lookup: %w", err)
	}
	if OutputStatus(status) == OutputConfirmed {
		// Idempotent: return the current row unchanged.
		out, err := scanOutput(tx.QueryRowContext(ctx, outputSelect+` WHERE id = ?`, oID))
		out.ProjectID = bound
		return out, err
	}
	now := clock.Format(c.Now())
	if _, err := tx.ExecContext(ctx,
		`UPDATE ai_outputs SET status = 'confirmed', confirmed_at = ?
		 WHERE id = ? AND status = 'draft'`, now, oID); err != nil {
		return AIOutput{}, fmt.Errorf("output: confirm: %w", err)
	}
	out, err := scanOutput(tx.QueryRowContext(ctx, outputSelect+` WHERE id = ?`, oID))
	if err != nil {
		return AIOutput{}, fmt.Errorf("output: confirm reload: %w", err)
	}
	out.ProjectID = bound
	return out, nil
}
