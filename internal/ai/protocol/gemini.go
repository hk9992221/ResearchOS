package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"researchos/internal/ai/aierr"
	"researchos/internal/ai/provider"
)

// geminiBatchEmbedLimit is the per-request cap of batchEmbedContents.
const geminiBatchEmbedLimit = 100

// geminiAdapter implements the Gemini generateContent / batchEmbedContents
// surface (§5.7 table).
type geminiAdapter struct{ base }

func (a *geminiAdapter) Protocol() provider.Protocol { return provider.ProtocolGemini }

type geminiListResponse struct {
	Models []struct {
		Name                       string   `json:"name"`
		DisplayName                string   `json:"displayName"`
		Description                string   `json:"description"`
		SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		InputTokenLimit            int      `json:"inputTokenLimit"`
		OutputTokenLimit           int      `json:"outputTokenLimit"`
	} `json:"models"`
	NextPageToken string `json:"nextPageToken"`
}

// ListModels walks pageToken pagination and narrows protocol capabilities
// with supportedGenerationMethods where present (§3.2 rule 1: narrowing only).
func (a *geminiAdapter) ListModels(ctx context.Context) ([]provider.UpstreamModel, error) {
	var models []provider.UpstreamModel
	pageToken := ""
	for {
		path := "/models?pageSize=1000"
		if pageToken != "" {
			path += "&pageToken=" + url.QueryEscape(pageToken)
		}
		var out geminiListResponse
		if err := a.request(ctx, "GET", path, nil, &out); err != nil {
			return nil, err
		}
		for _, m := range out.Models {
			id := strings.TrimPrefix(m.Name, "models/")
			if id == "" {
				continue
			}
			caps := narrowGeminiCaps(m.SupportedGenerationMethods)
			display := m.DisplayName
			if display == "" {
				display = id
			}
			raw, _ := json.Marshal(m)
			models = append(models, provider.UpstreamModel{
				ModelID:      id,
				DisplayName:  display,
				Capabilities: caps,
				Raw:          raw,
			})
		}
		if out.NextPageToken == "" {
			return models, nil
		}
		pageToken = out.NextPageToken
	}
}

// narrowGeminiCaps maps supportedGenerationMethods onto the protocol's
// capability set; an empty method list yields the full protocol set (§5.7
// "可为空，由协议能力兜底").
func narrowGeminiCaps(methods []string) []provider.Capability {
	if len(methods) == 0 {
		return provider.CapabilitiesList(provider.ProtocolGemini)
	}
	chat, embed := false, false
	for _, m := range methods {
		switch {
		case m == "generateContent" || m == "streamGenerateContent":
			chat = true
		case m == "embedContent" || m == "batchEmbedContents" || m == "embed":
			embed = true
		}
	}
	var caps []provider.Capability
	if chat {
		caps = append(caps, provider.CapabilityChat)
	}
	if embed {
		caps = append(caps, provider.CapabilityEmbedding)
	}
	return caps
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

func (a *geminiAdapter) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	var system []string
	var contents []geminiContent
	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem:
			system = append(system, m.Content)
		case RoleAssistant:
			contents = append(contents, geminiContent{Role: "model", Parts: []geminiPart{{Text: m.Content}}})
		default:
			contents = append(contents, geminiContent{Role: "user", Parts: []geminiPart{{Text: m.Content}}})
		}
	}
	body := map[string]any{"contents": contents}
	if len(system) > 0 {
		body["systemInstruction"] = geminiContent{Role: "user", Parts: []geminiPart{{Text: strings.Join(system, "\n\n")}}}
	}
	gen := map[string]any{}
	if req.Temperature != nil {
		gen["temperature"] = *req.Temperature
	}
	if req.MaxOutputTokens > 0 {
		gen["maxOutputTokens"] = req.MaxOutputTokens
	}
	if req.ResponseFormat != nil && (req.ResponseFormat.Type == "json_object" || req.ResponseFormat.Type == "json_schema") {
		gen["responseMimeType"] = "application/json"
		if req.ResponseFormat.Type == "json_schema" && len(req.ResponseFormat.JSONSchema) > 0 {
			gen["responseSchema"] = json.RawMessage(req.ResponseFormat.JSONSchema)
		}
	}
	if len(gen) > 0 {
		body["generationConfig"] = gen
	}

	var out struct {
		Candidates []struct {
			Content struct {
				Role  string       `json:"role"`
				Parts []geminiPart `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
		ResponseID string `json:"responseId"`
	}
	path := fmt.Sprintf("/models/%s:generateContent", url.PathEscape(req.Model))
	if err := a.request(ctx, "POST", path, body, &out); err != nil {
		return ChatResponse{}, err
	}
	if len(out.Candidates) == 0 {
		return ChatResponse{}, a.wrapKind(aierr.ErrKindServer, "gemini response carried no candidates")
	}
	var texts []string
	for _, p := range out.Candidates[0].Content.Parts {
		texts = append(texts, p.Text)
	}
	return ChatResponse{
		Content:      strings.Join(texts, ""),
		Model:        req.Model,
		Usage:        Usage{InputTokens: out.UsageMetadata.PromptTokenCount, OutputTokens: out.UsageMetadata.CandidatesTokenCount},
		FinishReason: out.Candidates[0].FinishReason,
		RequestID:    out.ResponseID,
	}, nil
}

// Embed splits Inputs into batchEmbedContents calls of at most 100 items and
// keeps global order (§5.7 "大批量拆分").
func (a *geminiAdapter) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	all := make([][]float32, 0, len(req.Inputs))
	for start := 0; start < len(req.Inputs); start += geminiBatchEmbedLimit {
		end := min(start+geminiBatchEmbedLimit, len(req.Inputs))
		requests := make([]map[string]any, 0, end-start)
		for _, text := range req.Inputs[start:end] {
			requests = append(requests, map[string]any{
				"model":   "models/" + req.Model,
				"content": geminiContent{Parts: []geminiPart{{Text: text}}},
			})
		}
		var out struct {
			Embeddings []struct {
				Values []float32 `json:"values"`
			} `json:"embeddings"`
		}
		path := fmt.Sprintf("/models/%s:batchEmbedContents", url.PathEscape(req.Model))
		if err := a.request(ctx, "POST", path, map[string]any{"requests": requests}, &out); err != nil {
			return EmbedResponse{}, err
		}
		if len(out.Embeddings) != end-start {
			return EmbedResponse{}, a.wrapKind(aierr.ErrKindServer,
				fmt.Sprintf("batchEmbedContents returned %d vectors for %d inputs", len(out.Embeddings), end-start))
		}
		for _, e := range out.Embeddings {
			all = append(all, e.Values)
		}
	}
	return EmbedResponse{Vectors: all, Model: req.Model}, nil
}
