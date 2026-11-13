package protocol

import (
	"context"
	"encoding/json"
	"strings"

	"researchos/internal/ai/aierr"
	"researchos/internal/ai/provider"
)

// openaiListResponse is the shared GET /models payload for both OpenAI-style
// protocols and most relays (§5.5 table).
type openaiListResponse struct {
	Data []struct {
		ID      string          `json:"id"`
		Object  string          `json:"object"`
		Created json.RawMessage `json:"created"`
		OwnedBy string          `json:"owned_by"`
	} `json:"data"`
}

// listModelsOpenAI fetches the model list; a relay without /models answers
// 404/405 and the error surfaces as unsupported (§5.3.5).
func listModelsOpenAI(b base, ctx context.Context) ([]provider.UpstreamModel, error) {
	var out openaiListResponse
	if err := b.request(ctx, "GET", "/models", nil, &out); err != nil {
		return nil, err
	}
	models := make([]provider.UpstreamModel, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID == "" {
			continue
		}
		raw, _ := json.Marshal(m)
		models = append(models, provider.UpstreamModel{
			ModelID:     m.ID,
			DisplayName: m.ID,
			Raw:         raw,
		})
	}
	return models, nil
}

// embeddingsOpenAI POSTs {base}/embeddings with a string-array input and
// keeps response order aligned to the request (§5.7).
func embeddingsOpenAI(b base, ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	var out struct {
		Model string `json:"model"`
		Data  []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	body := map[string]any{"model": req.Model, "input": req.Inputs}
	if err := b.request(ctx, "POST", "/embeddings", body, &out); err != nil {
		return EmbedResponse{}, err
	}
	vectors := make([][]float32, len(req.Inputs))
	for _, d := range out.Data {
		if d.Index >= 0 && d.Index < len(vectors) {
			vectors[d.Index] = d.Embedding
		}
	}
	for _, v := range vectors {
		if v == nil {
			return EmbedResponse{}, b.wrapKind(aierr.ErrKindServer,
				"embeddings response missing an input slot")
		}
	}
	model := out.Model
	if model == "" {
		model = req.Model
	}
	return EmbedResponse{
		Vectors: vectors,
		Model:   model,
		Usage:   Usage{InputTokens: out.Usage.PromptTokens},
	}, nil
}

// responseFormatChatCompletions maps the neutral response format onto Chat
// Completions' response_format object.
func responseFormatChatCompletions(f *ResponseFormat) any {
	if f == nil || f.Type == "" || f.Type == "text" {
		return nil
	}
	switch f.Type {
	case "json_object":
		return map[string]any{"type": "json_object"}
	case "json_schema":
		name := f.Name
		if name == "" {
			name = "response"
		}
		return map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   name,
				"schema": json.RawMessage(f.JSONSchema),
				"strict": false,
			},
		}
	}
	return nil
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func toChatMsgs(msgs []ChatMessage) []chatMsg {
	out := make([]chatMsg, len(msgs))
	for i, m := range msgs {
		out[i] = chatMsg{Role: string(m.Role), Content: m.Content}
	}
	return out
}

// openaiChatAdapter implements openai_chat: Chat Completions + Embeddings.
type openaiChatAdapter struct{ base }

func (a *openaiChatAdapter) Protocol() provider.Protocol { return provider.ProtocolOpenAIChat }

func (a *openaiChatAdapter) ListModels(ctx context.Context) ([]provider.UpstreamModel, error) {
	return listModelsOpenAI(a.base, ctx)
}

func (a *openaiChatAdapter) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	body := map[string]any{
		"model":    req.Model,
		"messages": toChatMsgs(req.Messages),
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxOutputTokens > 0 {
		body["max_tokens"] = req.MaxOutputTokens
	}
	if rf := responseFormatChatCompletions(req.ResponseFormat); rf != nil {
		body["response_format"] = rf
	}

	var out struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := a.request(ctx, "POST", "/chat/completions", body, &out); err != nil {
		return ChatResponse{}, err
	}
	if len(out.Choices) == 0 {
		return ChatResponse{}, a.wrapKind(aierr.ErrKindServer, "chat response carried no choices")
	}
	c := out.Choices[0]
	model := out.Model
	if model == "" {
		model = req.Model
	}
	return ChatResponse{
		Content:      c.Message.Content,
		Model:        model,
		Usage:        Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens},
		FinishReason: c.FinishReason,
		RequestID:    out.ID,
	}, nil
}

func (a *openaiChatAdapter) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	return embeddingsOpenAI(a.base, ctx, req)
}

// openaiResponsesAdapter implements the Responses API; embeddings still go
// through the standard embeddings endpoint (§5.2 note).
type openaiResponsesAdapter struct{ base }

func (a *openaiResponsesAdapter) Protocol() provider.Protocol {
	return provider.ProtocolOpenAIResponses
}

func (a *openaiResponsesAdapter) ListModels(ctx context.Context) ([]provider.UpstreamModel, error) {
	return listModelsOpenAI(a.base, ctx)
}

func (a *openaiResponsesAdapter) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	body := map[string]any{
		"model": req.Model,
		"input": toChatMsgs(req.Messages),
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxOutputTokens > 0 {
		body["max_output_tokens"] = req.MaxOutputTokens
	}
	if tf := responsesTextFormat(req.ResponseFormat); tf != nil {
		body["text"] = tf
	}

	var out struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		Status     string `json:"status"`
		OutputText string `json:"output_text"`
		Output     []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := a.request(ctx, "POST", "/responses", body, &out); err != nil {
		return ChatResponse{}, err
	}
	// A 200 body may still carry an embedded error object.
	if out.Error != nil {
		return ChatResponse{}, a.wrapKind(aierr.ErrKindServer,
			strings.TrimSpace(out.Error.Code+": "+out.Error.Message))
	}
	content := out.OutputText
	if content == "" {
		var parts []string
		for _, item := range out.Output {
			if item.Type != "message" {
				continue
			}
			for _, c := range item.Content {
				if c.Type == "output_text" {
					parts = append(parts, c.Text)
				}
			}
		}
		content = strings.Join(parts, "")
	}
	model := out.Model
	if model == "" {
		model = req.Model
	}
	return ChatResponse{
		Content:      content,
		Model:        model,
		Usage:        Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens},
		FinishReason: out.Status,
		RequestID:    out.ID,
	}, nil
}

func (a *openaiResponsesAdapter) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	return embeddingsOpenAI(a.base, ctx, req)
}

// responsesTextFormat maps structured output onto the Responses API
// text.format block.
func responsesTextFormat(f *ResponseFormat) any {
	if f == nil || f.Type == "" || f.Type == "text" {
		return nil
	}
	switch f.Type {
	case "json_object":
		return map[string]any{"format": map[string]any{"type": "json_object"}}
	case "json_schema":
		name := f.Name
		if name == "" {
			name = "response"
		}
		return map[string]any{"format": map[string]any{
			"type":   "json_schema",
			"name":   name,
			"schema": json.RawMessage(f.JSONSchema),
		}}
	}
	return nil
}
