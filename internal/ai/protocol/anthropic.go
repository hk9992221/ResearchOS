package protocol

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"researchos/internal/ai/provider"
)

// anthropicDefaultMaxTokens: Messages API requires max_tokens; callers who
// omit it get this bound (§5.7 mapping table).
const anthropicDefaultMaxTokens = 4096

// anthropicAdapter implements the Messages API. It deliberately does NOT
// implement EmbeddingAdapter — Anthropic stays chat-only (§5.2).
type anthropicAdapter struct{ base }

func (a *anthropicAdapter) Protocol() provider.Protocol { return provider.ProtocolAnthropic }

// ListModels walks the cursor-paginated /models listing.
func (a *anthropicAdapter) ListModels(ctx context.Context) ([]provider.UpstreamModel, error) {
	var models []provider.UpstreamModel
	after := ""
	for {
		path := "/models?limit=100"
		if after != "" {
			path += "&after=" + url.QueryEscape(after)
		}
		var out struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
				Type        string `json:"type"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := a.request(ctx, "GET", path, nil, &out); err != nil {
			return nil, err
		}
		for _, m := range out.Data {
			if m.ID == "" {
				continue
			}
			display := m.DisplayName
			if display == "" {
				display = m.ID
			}
			raw, _ := json.Marshal(m)
			models = append(models, provider.UpstreamModel{
				ModelID:      m.ID,
				DisplayName:  display,
				Capabilities: []provider.Capability{provider.CapabilityChat},
				Raw:          raw,
			})
		}
		if !out.HasMore || out.LastID == "" {
			return models, nil
		}
		after = out.LastID
	}
}

func (a *anthropicAdapter) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	var system []string
	var msgs []chatMsg
	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem:
			system = append(system, m.Content)
		case RoleAssistant:
			msgs = append(msgs, chatMsg{Role: "assistant", Content: m.Content})
		default:
			msgs = append(msgs, chatMsg{Role: "user", Content: m.Content})
		}
	}
	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxTokens
	}
	body := map[string]any{
		"model":      req.Model,
		"messages":   msgs,
		"max_tokens": maxTokens,
	}
	if len(system) > 0 {
		body["system"] = strings.Join(system, "\n\n")
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}

	var out struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := a.request(ctx, "POST", "/messages", body, &out); err != nil {
		return ChatResponse{}, err
	}
	var parts []string
	for _, block := range out.Content {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}
	return ChatResponse{
		Content:      strings.Join(parts, ""),
		Model:        out.Model,
		Usage:        Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens},
		FinishReason: out.StopReason,
		RequestID:    out.ID,
	}, nil
}
