package protocol

import (
	"bytes"
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
	body := chatCompletionsBody(req)

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

// chatCompletionsBody builds the shared Chat Completions request body; the
// streamed call only adds "stream".
func chatCompletionsBody(req ChatRequest) map[string]any {
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
	return body
}

// ChatStream reads the Chat Completions SSE feed and accumulates
// choices[0].delta.content. Reasoning deltas only keep the connection alive:
// they are not part of the stored draft.
func (a *openaiChatAdapter) ChatStream(ctx context.Context, req ChatRequest, sink StreamSink) (ChatResponse, error) {
	body := chatCompletionsBody(req)
	body["stream"] = true

	var (
		content   strings.Builder
		model     string
		requestID string
		finish    string
		usage     Usage
		sawChunk  bool
		terminal  bool
	)
	err := a.stream(ctx, "/chat/completions", body, func(ev sseEvent) error {
		sink.Alive()
		data := bytes.TrimSpace(ev.Data)
		if len(data) == 0 {
			return nil
		}
		if string(data) == "[DONE]" {
			terminal = true
			return nil
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
			Error *struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if uerr := json.Unmarshal(data, &chunk); uerr != nil {
			return a.wrapKind(aierr.ErrKindServer,
				"decode stream chunk: "+uerr.Error()+" body="+firstFragment(data))
		}
		if chunk.Error != nil {
			return a.wrapKind(aierr.ErrKindServer,
				strings.TrimSpace(chunk.Error.Type+" "+chunk.Error.Code+": "+chunk.Error.Message))
		}
		if chunk.ID != "" {
			requestID = chunk.ID
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			usage = Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}
		}
		if len(chunk.Choices) == 0 {
			return nil // usage-only trailer
		}
		sawChunk = true
		c := chunk.Choices[0]
		if c.FinishReason != "" {
			finish = c.FinishReason
			terminal = true
		}
		if c.Delta.Content != "" {
			content.WriteString(c.Delta.Content)
			sink.Emit(c.Delta.Content)
		}
		return nil
	})
	if err != nil {
		return ChatResponse{}, err
	}
	if !sawChunk {
		return ChatResponse{}, a.wrapKind(aierr.ErrKindServer, "chat stream carried no choices")
	}
	if !terminal {
		return ChatResponse{}, a.wrapKind(aierr.ErrKindServer, "chat stream ended without a finish event")
	}
	if model == "" {
		model = req.Model
	}
	return ChatResponse{
		Content:      content.String(),
		Model:        model,
		Usage:        usage,
		FinishReason: finish,
		RequestID:    requestID,
	}, nil
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
	body := responsesBody(req)

	var out responsesResult
	if err := a.request(ctx, "POST", "/responses", body, &out); err != nil {
		return ChatResponse{}, err
	}
	// A 200 body may still carry an embedded error object.
	if out.Error != nil {
		return ChatResponse{}, a.wrapKind(aierr.ErrKindServer,
			strings.TrimSpace(out.Error.Code+": "+out.Error.Message))
	}
	model := out.Model
	if model == "" {
		model = req.Model
	}
	return ChatResponse{
		Content:      out.text(),
		Model:        model,
		Usage:        Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens},
		FinishReason: out.Status,
		RequestID:    out.ID,
	}, nil
}

func (a *openaiResponsesAdapter) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	return embeddingsOpenAI(a.base, ctx, req)
}

// responsesBody builds the shared Responses request body; the streamed call
// only adds "stream".
func responsesBody(req ChatRequest) map[string]any {
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
	return body
}

// responsesResult is the decoded Responses object, shared by the unary body
// and the terminal event of a stream.
type responsesResult struct {
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

// text returns output_text, or the concatenated message parts when the relay
// only fills the structured output array.
func (r *responsesResult) text() string {
	if r.OutputText != "" {
		return r.OutputText
	}
	var parts []string
	for _, item := range r.Output {
		if item.Type != "message" {
			continue
		}
		for _, c := range item.Content {
			if c.Type == "output_text" {
				parts = append(parts, c.Text)
			}
		}
	}
	return strings.Join(parts, "")
}

// ChatStream walks the Responses SSE feed. Only response.output_text.delta
// becomes draft text; reasoning and summary deltas merely keep the
// connection alive. The stream must end with a terminal event, so a dropped
// connection never passes off a truncated answer as complete.
func (a *openaiResponsesAdapter) ChatStream(ctx context.Context, req ChatRequest, sink StreamSink) (ChatResponse, error) {
	body := responsesBody(req)
	body["stream"] = true

	var (
		content  strings.Builder
		final    *responsesResult
		model    string
		status   string
		complete bool
	)
	err := a.stream(ctx, "/responses", body, func(ev sseEvent) error {
		sink.Alive()
		data := bytes.TrimSpace(ev.Data)
		if len(data) == 0 || string(data) == "[DONE]" {
			return nil
		}
		var payload struct {
			Type  string           `json:"type"`
			Delta string           `json:"delta"`
			Code  string           `json:"code"`
			Msg   string           `json:"message"`
			Resp  *responsesResult `json:"response"`
		}
		if uerr := json.Unmarshal(data, &payload); uerr != nil {
			return a.wrapKind(aierr.ErrKindServer,
				"decode stream event: "+uerr.Error()+" body="+firstFragment(data))
		}
		name := ev.Name
		if name == "" {
			name = payload.Type
		}
		switch name {
		case "response.output_text.delta":
			if payload.Delta != "" {
				content.WriteString(payload.Delta)
				sink.Emit(payload.Delta)
			}
		case "response.completed":
			if payload.Resp == nil {
				return a.wrapKind(aierr.ErrKindServer, "response.completed carried no response object")
			}
			final = payload.Resp
			complete = true
		case "response.incomplete":
			// Truncation by max_output_tokens is a normal stop; the
			// unary path reports it as a finish reason too.
			if payload.Resp != nil {
				final = payload.Resp
			}
			complete = true
		case "response.failed":
			if payload.Resp != nil && payload.Resp.Error != nil {
				return a.wrapKind(aierr.ErrKindServer,
					strings.TrimSpace(payload.Resp.Error.Code+": "+payload.Resp.Error.Message))
			}
			return a.wrapKind(aierr.ErrKindServer, "response failed without an error object")
		case "error":
			return a.wrapKind(aierr.ErrKindServer,
				strings.TrimSpace(payload.Code+": "+payload.Msg))
		}
		return nil
	})
	if err != nil {
		return ChatResponse{}, err
	}
	if !complete {
		return ChatResponse{}, a.wrapKind(aierr.ErrKindServer, "response stream ended without a terminal event")
	}
	text := content.String()
	usage := Usage{}
	requestID := ""
	if final != nil {
		if final.OutputText != "" || len(final.Output) > 0 {
			text = final.text()
		}
		model = final.Model
		status = final.Status
		requestID = final.ID
		usage = Usage{InputTokens: final.Usage.InputTokens, OutputTokens: final.Usage.OutputTokens}
		if final.Error != nil {
			return ChatResponse{}, a.wrapKind(aierr.ErrKindServer,
				strings.TrimSpace(final.Error.Code+": "+final.Error.Message))
		}
	}
	if model == "" {
		model = req.Model
	}
	return ChatResponse{
		Content:      text,
		Model:        model,
		Usage:        usage,
		FinishReason: status,
		RequestID:    requestID,
	}, nil
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
