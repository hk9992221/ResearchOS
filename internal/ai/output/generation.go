package output

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"researchos/internal/ai/gateway"
	"researchos/internal/ai/protocol"
	"researchos/internal/ai/provider"
	"researchos/internal/ai/role"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
)

// GenerateRequest is one generation call. PromptVersion is mandatory:
// unversioned output is not traceable and gets rejected before the network
// (§5.9 rule 1).
type GenerateRequest struct {
	ProjectID       string
	TaskKind        string
	PromptVersion   string
	Messages        []protocol.ChatMessage
	SourceRefs      []SourceRef
	Entity          *EntityRef
	Temperature     *float64
	MaxOutputTokens int
	ResponseFormat  *protocol.ResponseFormat
	// OnDelta, when non-nil, streams assistant-visible text as it arrives.
	// The persisted draft is always the finished body, so a caller may
	// ignore deltas without changing the result. It runs on the calling
	// goroutine and must be cheap.
	OnDelta func(delta string)
}

// GenerateResult carries the persisted draft plus call metadata.
type GenerateResult struct {
	Output   AIOutput
	Usage    gateway.Usage
	Attempts int
}

// StructuredRequest asks for schema-validated JSON output. Validate may be
// a strict module validator; nil means "must parse" only (§5.9 rule 9).
type StructuredRequest struct {
	ProjectID       string
	TaskKind        string
	PromptVersion   string
	Messages        []protocol.ChatMessage
	SourceRefs      []SourceRef
	Entity          *EntityRef
	JSONSchema      json.RawMessage
	SchemaName      string
	Validate        func(json.RawMessage) error
	RepairAttempts  int
	Temperature     *float64
	MaxOutputTokens int
}

// StructuredResponse carries the validated JSON and the persisted draft.
type StructuredResponse struct {
	Output   AIOutput
	Content  string
	JSON     json.RawMessage
	Usage    gateway.Usage
	Attempts int
}

// ChatRunner is the gateway surface GenerationService needs; the Chat
// request always uses role chat_main (§5.9).
type ChatRunner interface {
	Chat(ctx context.Context, r role.Role, req gateway.ChatRequest) (gateway.ChatResponse, gateway.ProviderSummary, error)
}

// ChatStreamRunner is the optional streaming view of a ChatRunner. When the
// runner has it and the request carries OnDelta, generation streams; a
// runner without it keeps the unary call and the deltas arrive as one body.
type ChatStreamRunner interface {
	ChatStream(ctx context.Context, r role.Role, req gateway.ChatRequest, onDelta func(delta string)) (gateway.ChatResponse, gateway.ProviderSummary, error)
}

// GenerationOptions wires GenerationService to one project.
type GenerationOptions struct {
	Outputs AIOutputService
	Runner  ChatRunner
	// Events emits ai.analysis_completed for entity-bound generations; nil
	// disables events (e.g. headless import tools).
	Events events.Writer
	// ReadOnly blocks generation for archived projects before any network
	// call; wire the same closure used by the outputs service.
	ReadOnly func(ctx context.Context) error
	// ProtocolProbe reports the chat_main protocol so CompleteStructured can
	// decide between native structured output and the prompt-constraint
	// fallback (§5.9 rule 9). nil assumes native support everywhere.
	ProtocolProbe func(ctx context.Context) (provider.Protocol, error)
	// Logger receives event diagnostics; failures never roll back drafts.
	Logger func(msg string, kv ...any)
}

// GenerationService is the shared entry for SPEC-06/07/09 (and MCP through
// the same interface, §5.11).
type GenerationService interface {
	Generate(ctx context.Context, req GenerateRequest) (GenerateResult, error)
	CompleteStructured(ctx context.Context, req StructuredRequest) (StructuredResponse, error)
}

type generator struct {
	gopts GenerationOptions
}

var _ GenerationService = (*generator)(nil)

// NewGenerationService validates the wiring and returns the service.
func NewGenerationService(opt GenerationOptions) (GenerationService, error) {
	if opt.Outputs == nil || opt.Runner == nil {
		return nil, errors.New("output: generation requires an outputs service and a chat runner")
	}
	return &generator{gopts: opt}, nil
}

// Generate performs one chat_main call and persists the result as draft.
// With req.OnDelta set the call streams deltas to the caller; the stored
// draft is always the finished body.
// Successful content is never dropped silently: persistence failures
// return *OutputPersistError with the complete input (§5.9.3, AC-05.5).
func (g *generator) Generate(ctx context.Context, req GenerateRequest) (GenerateResult, error) {
	if err := g.preflight(ctx, req.ProjectID, req.TaskKind, req.PromptVersion, req.Entity); err != nil {
		return GenerateResult{}, err
	}
	chatReq := gateway.ChatRequest{
		Messages:        req.Messages,
		Temperature:     req.Temperature,
		MaxOutputTokens: req.MaxOutputTokens,
		ResponseFormat:  req.ResponseFormat,
	}
	resp, summary, err := g.chat(ctx, chatReq, req.OnDelta)
	if err != nil {
		return GenerateResult{}, err
	}
	in := g.buildDraft(req.ProjectID, req.TaskKind, req.PromptVersion, req.Entity, req.SourceRefs, req.Temperature, req.MaxOutputTokens, summary, resp.Model, resp.Content, &resp.Usage, resp.Attempts)
	out, err := g.gopts.Outputs.CreateDraft(ctx, in)
	if err != nil {
		return GenerateResult{}, &OutputPersistError{Input: in, Cause: err}
	}
	g.emitAnalysis(ctx, out)
	return GenerateResult{Output: out, Usage: resp.Usage, Attempts: resp.Attempts}, nil
}

// chat runs one chat_main call, streaming when the runner can and the caller
// asked for deltas. A runner without the streaming surface falls back to the
// unary call and reports the finished body as one delta, so OnDelta semantics
// never depend on the provider.
func (g *generator) chat(ctx context.Context, req gateway.ChatRequest, onDelta func(delta string)) (gateway.ChatResponse, gateway.ProviderSummary, error) {
	if onDelta == nil {
		return g.gopts.Runner.Chat(ctx, role.RoleChatMain, req)
	}
	if sr, ok := g.gopts.Runner.(ChatStreamRunner); ok {
		return sr.ChatStream(ctx, role.RoleChatMain, req, onDelta)
	}
	resp, summary, err := g.gopts.Runner.Chat(ctx, role.RoleChatMain, req)
	if err == nil && resp.Content != "" {
		onDelta(resp.Content)
	}
	return resp, summary, err
}

func (g *generator) buildDraft(projectID, taskKind, promptVersion string, entity *EntityRef, refs []SourceRef, temp *float64, maxTokens int, summary gateway.ProviderSummary, model, content string, usage *gateway.Usage, attempts int) CreateDraftInput {
	params := map[string]any{}
	if temp != nil {
		params["temperature"] = *temp
	}
	if maxTokens > 0 {
		params["max_output_tokens"] = maxTokens
	}
	if model == "" {
		model = summary.ModelID
	}
	return CreateDraftInput{
		ProjectID: projectID,
		TaskKind:  taskKind,
		Entity:    entity,
		ContentMD: content,
		Provenance: Provenance{
			ProviderID:    summary.ProviderID,
			ProviderName:  summary.ProviderName,
			Protocol:      summary.Protocol,
			ModelID:       model,
			PromptVersion: promptVersion,
			SourceRefs:    refs,
			Parameters:    params,
		},
		Usage: usage,
	}
}

// preflight rejects untraceable requests and archived projects before any
// network call (§5.9 rule 1, §6.6).
func (g *generator) preflight(ctx context.Context, projectID, taskKind, promptVersion string, entity *EntityRef) error {
	if promptVersion == "" {
		return ErrInvalidPromptVersion
	}
	if !ValidTaskKind(taskKind) {
		return fmt.Errorf("%w: %q", ErrUnknownTaskKind, taskKind)
	}
	if projectID == "" {
		return errors.New("output: project id is required")
	}
	if entity != nil {
		if !schema.EntityType(entity.Type).Valid() {
			return fmt.Errorf("%w: %q", ErrInvalidEntity, entity.Type)
		}
	}
	if g.gopts.ReadOnly != nil {
		if err := g.gopts.ReadOnly(ctx); err != nil {
			return err
		}
	}
	return nil
}

// emitAnalysis sends ai.analysis_completed for entity-bound drafts only;
// event failure is a diagnostic, never a rollback (§5.9 rule 4).
func (g *generator) emitAnalysis(ctx context.Context, out AIOutput) {
	if g.gopts.Events == nil || out.Entity == nil {
		return
	}
	summary := fmt.Sprintf("%s via %s/%s", out.TaskKind, out.Provenance.ProviderName, out.Provenance.ModelID)
	err := g.gopts.Events.EmitStandalone(ctx, events.Event{
		Type:       schema.EventAIAnalysisCompleted,
		EntityType: schema.EntityType(out.Entity.Type),
		EntityID:   out.Entity.ID,
		Summary:    summary,
		Actor:      schema.ActorAI,
	})
	if err != nil && g.gopts.Logger != nil {
		g.gopts.Logger("ai event emit failed", "output", out.ID, "err", err.Error())
	}
}

// CompleteStructured forces JSON output, validates it (parse + optional
// strict validator), retries with one repair prompt on failure and persists
// a draft ONLY when validation finally passes (§5.9 rule 9).
//
// The call goes through the streaming path with a discarded delta sink: a
// structured reply can take minutes, and the gateway's unary ceiling is total
// wall time (TimeoutChat × attempts), while ChatStream bounds silence instead.
// The answer, the repair rounds and the persisted draft are identical either
// way; a runner without the streaming surface falls back to one unary call.
func (g *generator) CompleteStructured(ctx context.Context, req StructuredRequest) (StructuredResponse, error) {
	if len(req.JSONSchema) == 0 {
		return StructuredResponse{}, ErrSchemaRequired
	}
	if err := g.preflight(ctx, req.ProjectID, req.TaskKind, req.PromptVersion, req.Entity); err != nil {
		return StructuredResponse{}, err
	}
	// Default one repair round (StructuredRequest.RepairAttempts); a
	// negative value disables repair entirely.
	repairs := req.RepairAttempts
	switch {
	case repairs == 0:
		repairs = 1
	case repairs < 0:
		repairs = 0
	}
	messages := append([]protocol.ChatMessage(nil), req.Messages...)
	if !g.nativeStructured(ctx) {
		messages = append([]protocol.ChatMessage{{
			Role:    protocol.RoleSystem,
			Content: jsonConstraintPrompt(req.SchemaName, req.JSONSchema),
		}}, messages...)
	}

	var format *protocol.ResponseFormat
	if g.nativeStructured(ctx) {
		format = &protocol.ResponseFormat{Type: "json_schema", Name: req.SchemaName, JSONSchema: req.JSONSchema}
	}

	validate := req.Validate
	totalUsage := gateway.Usage{}
	totalAttempts := 0
	var lastErr error
	for round := 0; ; round++ {
		resp, summary, err := g.chat(ctx, gateway.ChatRequest{
			Messages:        messages,
			Temperature:     req.Temperature,
			MaxOutputTokens: req.MaxOutputTokens,
			ResponseFormat:  format,
		}, func(string) {})
		if err != nil {
			return StructuredResponse{}, err
		}
		totalAttempts += max(resp.Attempts, 1)
		totalUsage.InputTokens += resp.Usage.InputTokens
		totalUsage.OutputTokens += resp.Usage.OutputTokens

		raw, extractErr := extractJSON(resp.Content)
		lastErr = extractErr
		if lastErr == nil && validate != nil {
			lastErr = validate(raw)
		}
		if lastErr == nil {
			usage := totalUsage
			in := g.buildDraft(req.ProjectID, req.TaskKind, req.PromptVersion, req.Entity, req.SourceRefs, req.Temperature, req.MaxOutputTokens, summary, resp.Model, string(raw), &usage, totalAttempts)
			out, perr := g.gopts.Outputs.CreateDraft(ctx, in)
			if perr != nil {
				return StructuredResponse{}, &OutputPersistError{Input: in, Cause: perr}
			}
			g.emitAnalysis(ctx, out)
			return StructuredResponse{Output: out, Content: string(raw), JSON: raw, Usage: totalUsage, Attempts: totalAttempts}, nil
		}
		if round >= repairs {
			break
		}
		// One repair round: feed the invalid output back and demand
		// corrected JSON only — never more than RepairAttempts rounds.
		messages = append(messages,
			protocol.ChatMessage{Role: protocol.RoleAssistant, Content: resp.Content},
			protocol.ChatMessage{Role: protocol.RoleUser, Content: repairPrompt(lastErr)},
		)
	}
	return StructuredResponse{}, fmt.Errorf("%w: %v", ErrOutputValidation, lastErr)
}

// nativeStructured reports whether the provider likely supports structured
// output natively; without a probe GenerationService keeps it enabled for
// OpenAI-style/Gemini protocols (the default), and the prompt fallback is
// selected only when the probe says otherwise (§5.9.9).
func (g *generator) nativeStructured(ctx context.Context) bool {
	if g.gopts.ProtocolProbe == nil {
		return true
	}
	proto, err := g.gopts.ProtocolProbe(ctx)
	if err != nil {
		return true
	}
	return proto != provider.ProtocolAnthropic
}

func jsonConstraintPrompt(name string, schema json.RawMessage) string {
	label := name
	if label == "" {
		label = "response"
	}
	return fmt.Sprintf("Respond with a single valid JSON object for schema %q matching this JSON Schema, and nothing else (no prose, no code fences):\n%s", label, string(schema))
}

func repairPrompt(err error) string {
	return fmt.Sprintf("Your previous reply failed validation: %v. Reply again with ONLY the corrected JSON object; no prose, no code fences.", err)
}

// extractJSON finds the first complete JSON object in a model reply,
// tolerating code fences and surrounding prose.
func extractJSON(content string) (json.RawMessage, error) {
	s := strings.TrimSpace(content)
	s = stripCodeFence(s)
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return nil, errors.New("no JSON object found")
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				raw := json.RawMessage(s[start : i+1])
				if !json.Valid(raw) {
					return nil, errors.New("JSON object is malformed")
				}
				return raw, nil
			}
		}
	}
	return nil, errors.New("unbalanced JSON object")
}

func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if j := strings.LastIndex(s, "```"); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}
