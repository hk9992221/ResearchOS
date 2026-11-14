package protocol

import (
	"context"
	"errors"
	"net/http"

	"researchos/internal/ai/aierr"
	"researchos/internal/ai/provider"
)

// Factory builds protocol adapters from a ProviderConfig plus the transient
// secret. It also satisfies provider.ModelLister, the single seam provider
// and model services use upstream listing through.
type Factory struct {
	client *http.Client
}

var (
	_ AdapterFactory       = (*Factory)(nil)
	_ provider.ModelLister = (*Factory)(nil)
)

// NewFactory returns a factory; a nil client uses http.DefaultClient.
// Timeouts are governed by the caller's context (gateway §5.8.2), so the
// client itself carries no deadline.
func NewFactory(client *http.Client) *Factory {
	return &Factory{client: client}
}

// New constructs the adapter pair for cfg.Protocol.
func (f *Factory) New(_ context.Context, cfg provider.ProviderConfig, key provider.Secret) (ProtocolAdapter, error) {
	if !provider.ValidProtocol(cfg.Protocol) {
		return nil, errors.New("protocol: " + ErrUnknownProtocol.Error() + ": " + string(cfg.Protocol))
	}
	b := base{cfg: cfg, key: key, client: f.client}
	switch cfg.Protocol {
	case provider.ProtocolOpenAIChat:
		return &openaiChatAdapter{base: b}, nil
	case provider.ProtocolOpenAIResponses:
		return &openaiResponsesAdapter{base: b}, nil
	case provider.ProtocolGemini:
		return &geminiAdapter{base: b}, nil
	case provider.ProtocolAnthropic:
		return &anthropicAdapter{base: b}, nil
	}
	return nil, ErrUnknownProtocol
}

// ListModels implements provider.ModelLister by building the adapter and
// delegating.
func (f *Factory) ListModels(ctx context.Context, cfg provider.ProviderConfig, key provider.Secret) ([]provider.UpstreamModel, error) {
	adapter, err := f.New(ctx, cfg, key)
	if err != nil {
		return nil, err
	}
	return adapter.ListModels(ctx)
}

// ErrUnknownProtocol rejects configs outside the four registered protocols.
var ErrUnknownProtocol = errors.New("unknown protocol")

// Embedding returns the embedding-capable view of an adapter, mapping the
// missing interface onto the shared AIError kind so callers never see Go
// type-assertion noise (§5.2 capability matrix).
func Embedding(adapter ProtocolAdapter) (EmbeddingAdapter, error) {
	ea, ok := adapter.(EmbeddingAdapter)
	if !ok {
		return nil, &aierr.AIError{
			Kind:     aierr.ErrKindUnsupported,
			Protocol: string(adapter.Protocol()),
			Message:  "protocol does not support embedding",
		}
	}
	return ea, nil
}

// Chat returns the chat-capable view of an adapter. All four protocols are
// chat-capable today, so this only guards against future additions.
func Chat(adapter ProtocolAdapter) (ChatAdapter, error) {
	ca, ok := adapter.(ChatAdapter)
	if !ok {
		return nil, &aierr.AIError{
			Kind:     aierr.ErrKindUnsupported,
			Protocol: string(adapter.Protocol()),
			Message:  "protocol does not support chat",
		}
	}
	return ca, nil
}

// Stream returns the streaming view of an adapter. Protocols that cannot
// stream (anthropic, gemini today) surface as unsupported_capability so the
// gateway can serve the same call through unary Chat.
func Stream(adapter ProtocolAdapter) (StreamAdapter, error) {
	sa, ok := adapter.(StreamAdapter)
	if !ok {
		return nil, &aierr.AIError{
			Kind:     aierr.ErrKindUnsupported,
			Protocol: string(adapter.Protocol()),
			Message:  "protocol does not support streaming chat",
		}
	}
	return sa, nil
}
