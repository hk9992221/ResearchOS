package ideas

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
)

// The evidence.normalize skill identity (SPEC-06 §5.7.1).
const (
	skillName     = "evidence.normalize"
	promptVersion = "evidence.normalize/1.0"
)

// Input budgets of SPEC-06 §5.7.2.
const (
	maxSourceChars = 4000
	maxTotalChars  = 24000
	maxDraftsLimit = 20
)

// StructuredRequest is the minimal SPEC-02 CompleteStructured call surface this
// module consumes. SPEC-02 adapts its own request type onto it.
type StructuredRequest struct {
	Purpose         string
	System          string
	Prompt          string
	JSONSchema      string
	MaxOutputTokens int
}

// StructuredResponse is the structured completion result.
type StructuredResponse struct {
	Content      string
	Provider     string
	Model        string
	InputTokens  int
	OutputTokens int
}

// ChatClient is the SPEC-02 structured-chat port. A nil client disables the
// skill with ai.disabled; manual evidence is unaffected (AC-21).
type ChatClient interface {
	CompleteStructured(ctx context.Context, req StructuredRequest) (StructuredResponse, error)
}

// aiOutput is the strict JSON contract of §5.7.3.
type aiOutput struct {
	Items []aiItem `json:"items"`
}

type aiItem struct {
	Direction   string `json:"direction"`
	Content     string `json:"content"`
	SourceIndex int    `json:"source_index"`
	Rationale   string `json:"rationale"`
}

// skillSource is one resolved, truncated input.
type skillSource struct {
	Index     int
	Ref       SourceRef
	Title     string
	Text      string
	Version   string
	Truncated bool
}

// normHashnormalizes text for hashing and comparison: NFC plus collapsed
// whitespace (SPEC-06 §5.7.4).
func normalizeText(s string) string {
	return strings.Join(strings.Fields(norm.NFC.String(s)), " ")
}

// hashContent returns the lowercase SHA-256 of the normalized content.
func hashContent(content string) string {
	sum := sha256.Sum256([]byte(normalizeText(content)))
	return hex.EncodeToString(sum[:])
}

// GenerateEvidenceDrafts runs the evidence.normalize skill. Valid items are
// persisted as unconfirmed drafts; the caller confirms them explicitly.
func (s *service) GenerateEvidenceDrafts(ctx context.Context, in GenerateEvidenceInput) (*GenerateEvidenceResult, error) {
	if _, err := writeActor(in.Actor); err != nil {
		return nil, err
	}
	if s.ai == nil {
		return nil, serviceError(ErrAIDisabled, "no chat provider is configured")
	}
	if s.sources == nil {
		return nil, serviceError(ErrSourceUnavailable, "no source resolver is configured").
			withDetails(map[string]any{"failed": []any{}})
	}
	if len(in.Sources) == 0 || len(in.Sources) > 8 {
		return nil, serviceError(ErrInvalidInput, "sources must contain 1..8 entries")
	}
	if n := utf8.RuneCountInString(in.Instruction); n > 500 {
		return nil, serviceError(ErrInvalidInput, "instruction must be at most 500 runes, got %d", n)
	}
	maxDrafts := in.MaxDrafts
	if maxDrafts == 0 {
		maxDrafts = 8
	}
	if maxDrafts < 1 || maxDrafts > maxDraftsLimit {
		return nil, serviceError(ErrInvalidInput, "max drafts must be 1..%d, got %d", maxDraftsLimit, maxDrafts)
	}
	handle, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	idea, err := s.ideaByIDDB(ctx, handle.DB(), in.IdeaID, false)
	if err != nil {
		return nil, err
	}

	inputs, err := s.resolveSkillSources(ctx, in.Sources)
	if err != nil {
		return nil, err
	}

	request := s.skillRequest(idea, inputs, in.Instruction, maxDrafts)
	response, out, err := s.completeWithRepair(ctx, request)
	if err != nil {
		return nil, err
	}

	candidates, skipped, schemaValid := validateAIItems(out.Items, inputs, maxDrafts)
	if len(out.Items) > 0 && schemaValid == 0 {
		return nil, serviceError(ErrAIOutputInvalid, "the model returned no schema-valid evidence items")
	}

	runID, err := s.newID()
	if err != nil {
		return nil, err
	}
	now := s.now()
	generatedAt := s.clock.Now()
	provenanceSources := make([]ProvenanceSource, len(inputs))
	anyTruncated := false
	for i, src := range inputs {
		provenanceSources[i] = ProvenanceSource{Type: src.Ref.Type, ID: src.Ref.ID, Version: src.Version}
		anyTruncated = anyTruncated || src.Truncated
	}

	var drafts []Evidence
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := requireIdeaTx(ctx, tx, idea.ID, false); err != nil {
			return err
		}
		seen := make(map[string]bool, len(candidates))
		final := make([]aiCandidate, 0, len(candidates))
		for _, c := range candidates {
			if seen[c.hash] {
				skipped = append(skipped, SkippedDraft{Source: c.ref, Reason: "duplicate_batch", ContentHash: c.hash})
				continue
			}
			seen[c.hash] = true
			var exists int
			if err := tx.QueryRowContext(ctx,
				`SELECT count(*) FROM evidence
				 WHERE idea_id = ? AND source_type = ? AND source_id = ? AND content_hash = ?
				   AND deleted_at IS NULL`,
				idea.ID, string(c.ref.Type), c.ref.ID, c.hash).Scan(&exists); err != nil {
				return fmt.Errorf("ideas: check duplicate evidence: %w", err)
			}
			if exists > 0 {
				skipped = append(skipped, SkippedDraft{Source: c.ref, Reason: "duplicate_existing", ContentHash: c.hash})
				continue
			}
			final = append(final, c)
		}
		for _, c := range final {
			id, err := s.newID()
			if err != nil {
				return err
			}
			prov := Provenance{
				ProviderID:    response.Provider,
				ModelID:       response.Model,
				PromptVersion: promptVersion,
				Skill:         skillName,
				Sources:       provenanceSources,
				Rationale:     c.rationale,
				GeneratedAt:   generatedAt,
				Truncated:     anyTruncated,
			}
			provRaw, err := json.Marshal(prov)
			if err != nil {
				return fmt.Errorf("ideas: marshal provenance: %w", err)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO evidence(id, idea_id, direction, content, source_type, source_id,
				   ai_generated, confirmed, ai_output_id, provenance, content_hash, created_at, updated_at, deleted_at)
				 VALUES (?, ?, ?, ?, ?, ?, 1, 0, NULL, ?, ?, ?, ?, NULL)`,
				id, idea.ID, string(c.direction), c.content, string(c.ref.Type), c.ref.ID,
				string(provRaw), c.hash, now, now); err != nil {
				return fmt.Errorf("ideas: insert draft evidence: %w", err)
			}
			drafts = append(drafts, Evidence{
				ID:          id,
				IdeaID:      idea.ID,
				Direction:   c.direction,
				ContentMD:   c.content,
				Source:      c.ref,
				AIGenerated: true,
				Confirmed:   false,
				ContentHash: c.hash,
				Provenance:  &prov,
			})
		}
		summary := fmt.Sprintf("Evidence 规范化：生成 %d 条草稿（来源 %d 项，跳过 %d 条）",
			len(drafts), len(inputs), len(skipped))
		return handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventAIAnalysisCompleted,
			EntityType: schema.EntityIdea,
			EntityID:   idea.ID,
			Summary:    summary,
			Actor:      schema.ActorAI,
			At:         s.clock.Now(),
		})
	})
	if err != nil {
		return nil, err
	}
	return &GenerateEvidenceResult{
		RunID:    runID,
		Drafts:   drafts,
		Skipped:  skipped,
		Usage:    TokenUsage{InputTokens: response.InputTokens, OutputTokens: response.OutputTokens},
		Provider: response.Provider,
		Model:    response.Model,
	}, nil
}

// aiCandidate is one schema-valid item bound to its source.
type aiCandidate struct {
	direction Direction
	content   string
	ref       SourceRef
	rationale string
	hash      string
}

// resolveSkillSources resolves and truncates the selected sources. Any failure
// aborts the whole run (SPEC-06 §5.7.2).
func (s *service) resolveSkillSources(ctx context.Context, refs []SourceRef) ([]skillSource, error) {
	seen := make(map[string]bool, len(refs))
	failed := make([]any, 0, len(refs))
	var inputs []skillSource
	for _, ref := range refs {
		if ref.Type != SourceExperiment && ref.Type != SourceRun && ref.Type != SourceNote {
			return nil, serviceError(ErrInvalidSource,
				"skill sources must be experiment, run or note, got %q", string(ref.Type))
		}
		key := string(ref.Type) + ":" + ref.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		resolved, err := s.sources.Resolve(ctx, s.projectID, ref)
		if err != nil || strings.TrimSpace(resolved.Text) == "" {
			reason := "empty"
			if err != nil {
				reason = err.Error()
			}
			failed = append(failed, map[string]any{
				"type": string(ref.Type), "id": ref.ID, "reason": reason,
			})
			continue
		}
		if resolved.Deleted {
			failed = append(failed, map[string]any{
				"type": string(ref.Type), "id": ref.ID, "reason": "target deleted",
			})
			continue
		}
		inputs = append(inputs, skillSource{
			Index:   len(inputs) + 1,
			Ref:     ref,
			Title:   resolved.Title,
			Text:    resolved.Text,
			Version: resolved.Version,
		})
	}
	if len(failed) > 0 {
		return nil, serviceError(ErrSourceUnavailable, "one or more sources could not be resolved").
			withDetails(map[string]any{"failed": failed})
	}
	remaining := maxTotalChars
	for i := range inputs {
		budget := maxSourceChars
		if remaining < budget {
			budget = remaining
		}
		if budget < 0 {
			budget = 0
		}
		text, truncated := truncateParagraphs(inputs[i].Text, budget)
		inputs[i].Text = text
		inputs[i].Truncated = truncated
		remaining -= utf8.RuneCountInString(text)
	}
	return inputs, nil
}

// truncateParagraphs cuts text at a paragraph boundary within budget runes.
func truncateParagraphs(text string, budget int) (string, bool) {
	if budget <= 0 {
		return "", utf8.RuneCountInString(text) > 0
	}
	if utf8.RuneCountInString(text) <= budget {
		return text, false
	}
	var b strings.Builder
	used := 0
	for _, paragraph := range strings.Split(text, "\n\n") {
		n := utf8.RuneCountInString(paragraph)
		add := n
		if b.Len() > 0 {
			add += 2
		}
		if used+add > budget {
			break
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(paragraph)
		used += add
	}
	if b.Len() == 0 {
		runes := []rune(text)
		if len(runes) > budget {
			runes = runes[:budget]
		}
		return string(runes), true
	}
	return b.String(), true
}

// skillRequest builds the SPEC-06 §5.7.3 prompt.
func (s *service) skillRequest(idea *Idea, inputs []skillSource, instruction string, maxDrafts int) StructuredRequest {
	var b strings.Builder
	b.WriteString("研究假设（仅作上下文，不得判断其成立与否）：\n")
	b.WriteString("标题：")
	b.WriteString(idea.Title)
	b.WriteString("\n描述：\n")
	b.WriteString(idea.DescriptionMD)
	b.WriteString("\n\n")
	if strings.TrimSpace(instruction) != "" {
		b.WriteString("筛选侧重：")
		b.WriteString(strings.TrimSpace(instruction))
		b.WriteString("\n\n")
	}
	b.WriteString("来源材料：\n")
	for _, src := range inputs {
		fmt.Fprintf(&b, "\n[%d] type=%s title=%s", src.Index, src.Ref.Type, src.Title)
		if src.Truncated {
			b.WriteString("（已截断）")
		}
		b.WriteString("\n")
		b.WriteString(src.Text)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\n请输出不超过 %d 条 Evidence 草稿。严格返回 JSON：\n", maxDrafts)
	b.WriteString(`{"items":[{"direction":"support|oppose","content":"陈述句","source_index":1,"rationale":"依据来源的哪一部分"}]}`)
	b.WriteString("\ncontent 为 20..1000 字符的陈述句；每条必须绑定一个来源序号；rationale 不超过 300 字符；")
	b.WriteString("不得给出假设成立/不成立的结论，不得引入来源之外的事实。\n")
	return StructuredRequest{
		Purpose:         skillName,
		System:          "你是科研证据规范化助手，只从给定来源文本中抽取可支持或反对研究假设的事实性陈述。",
		Prompt:          b.String(),
		JSONSchema:      `{"type":"object","properties":{"items":{"type":"array","maxItems":` + fmt.Sprint(maxDrafts) + `,"items":{"type":"object","required":["direction","content","source_index","rationale"],"properties":{"direction":{"enum":["support","oppose"]},"content":{"type":"string","minLength":20,"maxLength":1000},"source_index":{"type":"integer","minimum":1},"rationale":{"type":"string","maxLength":300}}}},"required":["items"]}`,
		MaxOutputTokens: 2048,
	}
}

// completeWithRepair calls the provider and allows exactly one repair retry
// when the JSON cannot be parsed (SPEC-06 §5.7.3 rule 1).
func (s *service) completeWithRepair(ctx context.Context, req StructuredRequest) (StructuredResponse, aiOutput, error) {
	response, err := s.ai.CompleteStructured(ctx, req)
	if err != nil {
		return StructuredResponse{}, aiOutput{}, err
	}
	out, parseErr := parseAIOutput(response.Content)
	if parseErr == nil {
		return response, out, nil
	}
	repair := req
	repair.Prompt = req.Prompt + "\n\n上一次输出不是合法 JSON（" + parseErr.Error() +
		"）。请只返回符合给定 schema 的 JSON，不要包含代码块或解释。"
	response, err = s.ai.CompleteStructured(ctx, repair)
	if err != nil {
		return StructuredResponse{}, aiOutput{}, err
	}
	out, parseErr = parseAIOutput(response.Content)
	if parseErr != nil {
		return StructuredResponse{}, aiOutput{}, serviceError(ErrAIOutputInvalid,
			"model output is not valid JSON after one repair retry: %v", parseErr)
	}
	return response, out, nil
}

// parseAIOutput decodes strict JSON, tolerating a Markdown code fence.
func parseAIOutput(raw string) (aiOutput, error) {
	text := strings.TrimSpace(raw)
	if strings.HasPrefix(text, "```") {
		if i := strings.Index(text, "\n"); i >= 0 {
			text = text[i+1:]
		}
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return aiOutput{}, errors.New("no JSON object found")
	}
	text = text[start : end+1]
	var out aiOutput
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return aiOutput{}, err
	}
	return out, nil
}

// validateAIItems enforces the schema and returns the schema-valid candidates
// plus the schema-invalid skips. schemaValid counts items that passed the
// per-item validation.
func validateAIItems(items []aiItem, inputs []skillSource, maxDrafts int) ([]aiCandidate, []SkippedDraft, int) {
	var candidates []aiCandidate
	var skipped []SkippedDraft
	schemaValid := 0
	for _, item := range items {
		if len(candidates) >= maxDrafts {
			skipped = append(skipped, SkippedDraft{Reason: "schema"})
			continue
		}
		direction := Direction(strings.TrimSpace(item.Direction))
		content := strings.TrimSpace(item.Content)
		reason, ok := validateAIItem(direction, content, item.SourceIndex, len(inputs))
		if !ok {
			skip := SkippedDraft{Reason: reason, ContentHash: hashContent(content)}
			if item.SourceIndex >= 1 && item.SourceIndex <= len(inputs) {
				skip.Source = inputs[item.SourceIndex-1].Ref
			}
			skipped = append(skipped, skip)
			continue
		}
		schemaValid++
		rationale := strings.TrimSpace(item.Rationale)
		if n := utf8.RuneCountInString(rationale); n > 300 {
			rationale = string([]rune(rationale)[:300])
		}
		candidates = append(candidates, aiCandidate{
			direction: direction,
			content:   content,
			ref:       inputs[item.SourceIndex-1].Ref,
			rationale: rationale,
			hash:      hashContent(content),
		})
	}
	return candidates, skipped, schemaValid
}

func validateAIItem(direction Direction, content string, sourceIndex, sourceCount int) (string, bool) {
	if !direction.Valid() {
		return "schema", false
	}
	n := utf8.RuneCountInString(content)
	if n < 20 || n > 1000 {
		return "schema", false
	}
	if sourceIndex < 1 || sourceIndex > sourceCount {
		return "schema", false
	}
	return "", true
}
