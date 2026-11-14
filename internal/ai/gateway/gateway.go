// Package gateway is the single Chat/Embedding/Rerank entry point: it
// resolves roles, loads secrets transiently, enforces per-call timeouts and
// the limited retry policy, and annotates every failure with provider
// context (SPEC-02 §5.8). It never switches provider or model automatically.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"researchos/internal/ai/aierr"
	"researchos/internal/ai/protocol"
	"researchos/internal/ai/provider"
	"researchos/internal/ai/role"
	"researchos/internal/ai/secret"
	"researchos/internal/foundation/settings"
)

// Per-call timeouts (§5.8.2); callers may lower them but the gateway applies
// these as the ceiling.
const (
	TimeoutChat  = 120 * time.Second
	TimeoutEmbed = 60 * time.Second
)

// DefaultAttempts is the total attempt budget: one initial call plus two
// retries (§5.8.3).
const DefaultAttempts = 3

// ProviderSummary identifies what actually served a Chat call; consumers
// embed it into provenance snapshots (§5.8).
type ProviderSummary struct {
	ProviderID   string
	ProviderName string
	Protocol     provider.Protocol
	ModelID      string
}

// Gateway is the unified AI invocation surface.
type Gateway interface {
	Chat(ctx context.Context, r role.Role, req ChatRequest) (ChatResponse, ProviderSummary, error)
	// ChatStream is Chat with incremental delivery: onDelta receives
	// assistant-visible text as it arrives. The finished ChatResponse is
	// still returned, so callers that only want the final body can ignore
	// the callback. Protocols without SSE fall back to one unary request and
	// deliver the whole body as a single delta.
	ChatStream(ctx context.Context, r role.Role, req ChatRequest, onDelta func(delta string)) (ChatResponse, ProviderSummary, error)
	Embed(ctx context.Context, inputs []string) (EmbedResponse, error)
	Rerank(ctx context.Context, query string, docs []string, topN int) (RerankResponse, error)
}

// ChatService is the thin facade for modules that manage their own drafts;
// role defaults to chat_main and no ai_outputs row is created (§5.8).
type ChatService interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

// EmbeddingService is the stable entry for the SPEC-10 index pipeline; the
// vector-space isolation is exposed through CurrentProfile (§5.8).
type EmbeddingService interface {
	Embed(ctx context.Context, texts []string) (EmbedResponse, error)
	// CurrentProfile returns the embedding role's derived profile and the
	// known dimension (0 when nothing was embedded yet). Unassigned roles
	// fail with an aierr role_unassigned AIError.
	CurrentProfile(ctx context.Context) (profileID string, dim int, err error)
}

// RerankService is reserved until a rerank protocol is selected (O-01).
type RerankService interface {
	Rerank(ctx context.Context, query string, docs []string, topN int) (RerankResponse, error)
}

// EmbeddingProfileID derives the opaque, stable vector-space key from the
// provider id and model id so same-named models on different providers never
// share a space (§5.8). It must not be parsed back.
func EmbeddingProfileID(providerID, modelID string) string {
	sum := sha256.Sum256([]byte("researchos/embedding-profile/v1|" + providerID + "|" + modelID))
	return hex.EncodeToString(sum[:])
}

// ChatRequest is the protocol DTO re-exported so callers do not import the
// protocol package for ordinary calls.
type ChatRequest = protocol.ChatRequest

// ChatResponse re-exports the protocol response type.
type ChatResponse = protocol.ChatResponse

// EmbedRequest re-exports the protocol embedding request type.
type EmbedRequest = protocol.EmbedRequest

// EmbedResponse re-exports the protocol embedding response type.
type EmbedResponse = protocol.EmbedResponse

// RerankResponse re-exports the reserved rerank result type.
type RerankResponse = protocol.RerankResponse

// Usage re-exports the token accounting type.
type Usage = protocol.Usage

// Factory is the adapter construction seam; protocol.Factory satisfies it.
type Factory = protocol.AdapterFactory

// Roles is the resolution seam; role.Service satisfies it.
type Roles interface {
	Resolve(ctx context.Context, r role.Role) (role.ResolvedRole, error)
}

// Logger receives call metadata only — never prompts, bodies or keys
// (§5.8.8).
type Logger interface {
	LogCall(CallMeta)
}

// CallMeta is the metadata-safe audit record of one gateway call.
type CallMeta struct {
	Operation    string // chat | embed | rerank
	ProviderID   string
	ProviderName string
	Protocol     provider.Protocol
	Model        string
	Attempts     int
	Elapsed      time.Duration
	OK           bool
	ErrorKind    aierr.ErrorKind
	StatusCode   int
}

// Options wires the gateway.
type Options struct {
	Clock       clockLike
	Secrets     secret.Store
	Roles       Roles
	Factory     Factory
	Settings    *settings.Store // workspace settings; nil disables dim caching
	MaxAttempts int
	// Backoff holds the base delays for retries after attempts 1 and 2.
	Backoff []time.Duration
	// Jitter is the fractional spread applied to each delay (±Jitter).
	Jitter float64
	// Sleep waits d or until ctx is done; tests inject their own.
	Sleep func(ctx context.Context, d time.Duration) error
	// Logger records call metadata; nil discards it.
	Logger Logger
}

// clockLike keeps the clock import surface minimal for tests.
type clockLike interface{ Now() time.Time }
