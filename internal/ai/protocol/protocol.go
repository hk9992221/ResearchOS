// Package protocol implements the four upstream wire protocols behind one
// adapter surface (SPEC-02 §5.7). Adapters build requests, decode responses,
// map every upstream failure onto *aierr.AIError and never retry — retries
// belong to the gateway. Request bodies and auth headers are never logged.
package protocol

import (
	"context"
	"encoding/json"

	"researchos/internal/ai/provider"
)

// ChatRole orders a chat turn.
type ChatRole string

// Roles accepted by ChatMessage (SPEC-02 §5.7).
const (
	RoleSystem    ChatRole = "system"
	RoleUser      ChatRole = "user"
	RoleAssistant ChatRole = "assistant"
)

// ChatMessage is one text-only turn; v1 carries no images (§1.3).
type ChatMessage struct {
	Role    ChatRole
	Content string
}

// ResponseFormat requests structured output. Type is text | json_object |
// json_schema; adapters map or ignore it per protocol support.
type ResponseFormat struct {
	Type       string
	Name       string
	JSONSchema json.RawMessage
}

// ChatRequest is the protocol-neutral chat call.
type ChatRequest struct {
	Model           string
	Messages        []ChatMessage
	Temperature     *float64
	MaxOutputTokens int
	ResponseFormat  *ResponseFormat
}

// Usage reports upstream token accounting where available.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// ChatResponse is the non-streamed result (O-09: v1 non-streaming).
type ChatResponse struct {
	Content      string
	Model        string
	Usage        Usage
	FinishReason string
	RequestID    string
	Attempts     int // filled by the gateway, not the adapter
}

// EmbedRequest carries a batch of texts.
type EmbedRequest struct {
	Model  string
	Inputs []string
}

// EmbedResponse keeps Vectors aligned with Inputs order.
type EmbedResponse struct {
	Vectors [][]float32
	Model   string
	Usage   Usage
}

// RerankRequest is the reserved rerank shape (O-01).
type RerankRequest struct {
	Model     string
	Query     string
	Documents []string
	TopN      int
}

// RerankHit scores one input document by index.
type RerankHit struct {
	Index int
	Score float64
}

// RerankResponse is the reserved rerank result shape.
type RerankResponse struct {
	Hits  []RerankHit
	Model string
	Usage Usage
}

// ProtocolAdapter is the capability floor: every protocol can list models.
type ProtocolAdapter interface {
	Protocol() provider.Protocol
	ListModels(ctx context.Context) ([]provider.UpstreamModel, error)
}

// ChatAdapter is implemented by all four protocols.
type ChatAdapter interface {
	ProtocolAdapter
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

// EmbeddingAdapter is implemented by openai_chat, openai_responses and gemini
// but never by anthropic (§5.2).
type EmbeddingAdapter interface {
	ProtocolAdapter
	Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error)
}

// RerankAdapter is fixed before a protocol exists so future implementations
// do not break callers; v1 ships no implementation (O-01).
type RerankAdapter interface {
	ProtocolAdapter
	Rerank(ctx context.Context, req RerankRequest) (RerankResponse, error)
}

// AdapterFactory builds one adapter per provider configuration. The factory
// also satisfies provider.ModelLister so provider and model services depend
// only on that narrow seam.
type AdapterFactory interface {
	New(ctx context.Context, cfg provider.ProviderConfig, key provider.Secret) (ProtocolAdapter, error)
}
