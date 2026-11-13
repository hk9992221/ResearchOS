package gateway

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	"researchos/internal/ai/aierr"
	"researchos/internal/ai/protocol"
	"researchos/internal/ai/provider"
	"researchos/internal/ai/role"
)

// dimSettingsKey is the workspace settings key namespace caching the last
// observed embedding dimension per profile.
func dimSettingsKey(profileID string) string { return "ai.embedding_dim." + profileID }

// embedConcurrency is the per-provider in-flight cap for embedding calls
// (§5.12).
const embedConcurrency = 2

// Service implements Gateway, EmbeddingService and RerankService.
type Service struct {
	opts    Options
	mu      sync.Mutex
	slots   map[string]chan struct{}
	slotsMu sync.Mutex
}

var (
	_ Gateway          = (*Service)(nil)
	_ ChatService      = (*chatFacade)(nil)
	_ EmbeddingService = (*Service)(nil)
	_ RerankService    = (*Service)(nil)
)

// New validates the wiring and returns the gateway service.
func New(opts Options) (*Service, error) {
	if opts.Secrets == nil || opts.Roles == nil || opts.Factory == nil {
		return nil, errors.New("gateway: secrets, roles and factory are required")
	}
	if opts.Clock == nil {
		return nil, errors.New("gateway: clock is required")
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = DefaultAttempts
	}
	if len(opts.Backoff) == 0 {
		opts.Backoff = []time.Duration{time.Second, 4 * time.Second}
	}
	if opts.Jitter == 0 {
		opts.Jitter = 0.2
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepCtx
	}
	return &Service{opts: opts, slots: map[string]chan struct{}{}}, nil
}

// NewChatService wraps g with the chat_main default for modules that keep
// their own drafts (SPEC-02 §5.8 note on ChatService).
func NewChatService(g Gateway) ChatService { return &chatFacade{gw: g} }

type chatFacade struct{ gw Gateway }

func (c *chatFacade) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	resp, _, err := c.gw.Chat(ctx, role.RoleChatMain, req)
	return resp, err
}

// Chat resolves the role, builds the adapter and applies the retry policy.
func (s *Service) Chat(ctx context.Context, r role.Role, req ChatRequest) (ChatResponse, ProviderSummary, error) {
	if err := canceledIfDone(ctx); err != nil {
		return ChatResponse{}, ProviderSummary{}, err
	}
	rr, err := s.resolve(ctx, r, provider.CapabilityChat)
	if err != nil {
		return ChatResponse{}, ProviderSummary{}, err
	}
	summary := summaryOf(rr)
	if req.Model == "" {
		req.Model = rr.Model.ModelID
	} else if req.Model != rr.Model.ModelID {
		return ChatResponse{}, summary, &aierr.AIError{
			Kind: aierr.ErrKindModelNotFound, ProviderID: rr.Provider.ID,
			ProviderName: rr.Provider.Name, Protocol: string(rr.Provider.Protocol),
			Model: req.Model, Message: "requested model differs from the role assignment",
		}
	}
	key, err := s.secretFor(ctx, rr.Provider)
	if err != nil {
		return ChatResponse{}, summary, err
	}
	adapter, err := s.opts.Factory.New(ctx, rr.Provider, key)
	if err != nil {
		return ChatResponse{}, summary, annotate(err, rr)
	}
	ca, err := protocol.Chat(adapter)
	if err != nil {
		return ChatResponse{}, summary, annotate(err, rr)
	}
	start := s.opts.Clock.Now()
	var resp ChatResponse
	attempts, err := s.run(ctx, "chat", rr, TimeoutChat, func(callCtx context.Context) error {
		var callErr error
		resp, callErr = ca.Chat(callCtx, req)
		return callErr
	})
	resp.Attempts = attempts
	if err != nil {
		s.log(rr, "chat", attempts, start, err)
		return ChatResponse{}, summary, err
	}
	s.log(rr, "chat", attempts, start, nil)
	return resp, summary, nil
}

// Embed resolves the embedding role and embeds one batch under the
// per-provider concurrency cap. The observed dimension is cached per profile
// for CurrentProfile (§5.8).
func (s *Service) Embed(ctx context.Context, inputs []string) (EmbedResponse, error) {
	if len(inputs) == 0 {
		return EmbedResponse{}, nil
	}
	if err := canceledIfDone(ctx); err != nil {
		return EmbedResponse{}, err
	}
	rr, err := s.resolve(ctx, role.RoleEmbedding, provider.CapabilityEmbedding)
	if err != nil {
		return EmbedResponse{}, err
	}
	key, err := s.secretFor(ctx, rr.Provider)
	if err != nil {
		return EmbedResponse{}, err
	}
	adapter, err := s.opts.Factory.New(ctx, rr.Provider, key)
	if err != nil {
		return EmbedResponse{}, annotate(err, rr)
	}
	ea, err := protocol.Embedding(adapter)
	if err != nil {
		return EmbedResponse{}, annotate(err, rr)
	}

	release := s.acquire(rr.Provider.ID)
	defer release()

	start := s.opts.Clock.Now()
	var resp EmbedResponse
	attempts, err := s.run(ctx, "embed", rr, TimeoutEmbed, func(callCtx context.Context) error {
		var callErr error
		resp, callErr = ea.Embed(callCtx, protocol.EmbedRequest{Model: rr.Model.ModelID, Inputs: inputs})
		return callErr
	})
	if err != nil {
		s.log(rr, "embed", attempts, start, err)
		return EmbedResponse{}, err
	}
	if len(resp.Vectors) > 0 {
		s.cacheDim(ctx, rr, len(resp.Vectors[0]))
	}
	s.log(rr, "embed", attempts, start, nil)
	return resp, nil
}

// Rerank has no implementation in v1: every protocol's capability matrix
// lacks rerank, so callers receive unsupported_capability without a network
// attempt (O-01, §5.2).
func (s *Service) Rerank(ctx context.Context, _ string, _ []string, _ int) (RerankResponse, error) {
	err := &aierr.AIError{
		Kind:    aierr.ErrKindUnsupported,
		Message: "no rerank protocol is registered yet; SPEC-10 must degrade to unranked fusion results",
	}
	return RerankResponse{}, err
}

// CurrentProfile exposes the embedding vector space: the derived profile id
// and the last known dimension (0 = never embedded).
func (s *Service) CurrentProfile(ctx context.Context) (string, int, error) {
	rr, err := s.resolve(ctx, role.RoleEmbedding, provider.CapabilityEmbedding)
	if err != nil {
		return "", 0, err
	}
	profileID := EmbeddingProfileID(rr.Provider.ID, rr.Model.ModelID)
	if s.opts.Settings == nil {
		return profileID, 0, nil
	}
	raw, ok, err := s.opts.Settings.Get(ctx, dimSettingsKey(profileID))
	if err != nil || !ok {
		return profileID, 0, nil
	}
	dim, err := strconv.Atoi(raw)
	if err != nil || dim < 0 {
		dim = 0
	}
	return profileID, dim, nil
}

func (s *Service) cacheDim(ctx context.Context, rr role.ResolvedRole, dim int) {
	if s.opts.Settings == nil || dim <= 0 {
		return
	}
	profileID := EmbeddingProfileID(rr.Provider.ID, rr.Model.ModelID)
	_ = s.opts.Settings.Set(ctx, nil, dimSettingsKey(profileID), strconv.Itoa(dim))
}

// resolve maps role problems onto the gateway error contract; nothing is
// sent to the network before it passes (§5.8 rule 1).
func (s *Service) resolve(ctx context.Context, r role.Role, need provider.Capability) (role.ResolvedRole, error) {
	rr, err := s.opts.Roles.Resolve(ctx, r)
	if err != nil {
		return rr, toAIError(err, r)
	}
	if rr.Capability != need {
		return rr, &aierr.AIError{
			Kind: aierr.ErrKindUnsupported, ProviderID: rr.Provider.ID,
			ProviderName: rr.Provider.Name, Protocol: string(rr.Provider.Protocol),
			Model:   rr.Model.ModelID,
			Message: fmt.Sprintf("role %s serves %s, not %s", r, rr.Capability, need),
		}
	}
	return rr, nil
}

// secretFor reads the key just long enough for the adapter; no caching
// (§5.4 rules). An empty reference yields a nil key: keyless local services
// send no auth header.
func (s *Service) secretFor(ctx context.Context, cfg provider.ProviderConfig) (provider.Secret, error) {
	if cfg.APIKeyRef == "" {
		return nil, nil
	}
	raw, err := s.opts.Secrets.Get(ctx, cfg.APIKeyRef)
	if err != nil {
		return nil, &aierr.AIError{
			Kind:         aierr.ErrKindSecretUnavailable,
			ProviderID:   cfg.ID,
			ProviderName: cfg.Name,
			Protocol:     string(cfg.Protocol),
			Message:      aierr.Truncate(err.Error()),
			Cause:        err,
		}
	}
	return provider.Secret(raw), nil
}

func (s *Service) acquire(providerID string) func() {
	s.slotsMu.Lock()
	ch, ok := s.slots[providerID]
	if !ok {
		ch = make(chan struct{}, embedConcurrency)
		s.slots[providerID] = ch
	}
	s.slotsMu.Unlock()
	ch <- struct{}{}
	return func() { <-ch }
}

// run executes fn under Timeout with the §5.8.3/§5.8.4 retry policy.
// Retried kinds: network, timeout, rate_limited, server_error. Everything
// else returns immediately; cancellation surfaces as canceled.
func (s *Service) run(ctx context.Context, op string, rr role.ResolvedRole, timeout time.Duration, fn func(context.Context) error) (int, error) {
	var lastErr error
	for attempt := 1; attempt <= s.opts.MaxAttempts; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		lastErr = fn(callCtx)
		cancel()
		if lastErr == nil {
			return attempt, nil
		}
		if ctx.Err() != nil {
			return attempt, canceledError(ctx, rr)
		}
		ae := annotate(lastErr, rr)
		ae.Attempts = attempt
		if !retryable(ae.Kind) {
			return attempt, ae
		}
		if attempt == s.opts.MaxAttempts {
			return attempt, ae
		}
		delay, ok := s.delayFor(attempt, ae)
		if !ok {
			// Retry-After beyond the manual-retry threshold (§5.8.3).
			return attempt, ae
		}
		if err := s.opts.Sleep(ctx, delay); err != nil {
			return attempt, canceledError(ctx, rr)
		}
	}
	return s.opts.MaxAttempts, annotate(lastErr, rr)
}

func retryable(k aierr.ErrorKind) bool {
	switch k {
	case aierr.ErrKindNetwork, aierr.ErrKindTimeout, aierr.ErrKindRateLimit, aierr.ErrKindServer:
		return true
	}
	return false
}

// delayFor picks the jittered backoff or the upstream Retry-After when it is
// within 30s. ok=false means the caller must hand over to manual retry.
func (s *Service) delayFor(attempt int, ae *aierr.AIError) (time.Duration, bool) {
	if ae.RetryAfter > 0 {
		if ae.RetryAfter > 30*time.Second {
			return 0, false
		}
		return ae.RetryAfter, true
	}
	base := s.opts.Backoff[min(attempt-1, len(s.opts.Backoff)-1)]
	delta := float64(base) * s.opts.Jitter
	return time.Duration(float64(base) + (rand.Float64()*2-1)*delta), true
}

// annotate fills provider context into errors that lack it; existing fields
// are never overwritten (§5.8.6).
func annotate(err error, rr role.ResolvedRole) *aierr.AIError {
	ae := toAIErrorPlain(err)
	if ae.ProviderID == "" {
		ae.ProviderID = rr.Provider.ID
	}
	if ae.ProviderName == "" {
		ae.ProviderName = rr.Provider.Name
	}
	if ae.Protocol == "" {
		ae.Protocol = string(rr.Provider.Protocol)
	}
	if ae.Model == "" {
		ae.Model = rr.Model.ModelID
	}
	return ae
}

// toAIError converts role resolution problems into the shared kind space.
func toAIError(err error, r role.Role) error {
	var re *role.Error
	if errors.As(err, &re) {
		return &aierr.AIError{
			Kind:    roleProblemKind(re.Problem),
			Message: re.Error(),
			Cause:   err,
		}
	}
	var ae *aierr.AIError
	if errors.As(err, &ae) {
		return ae
	}
	return &aierr.AIError{Kind: aierr.ErrKindRoleUnassigned, Message: err.Error(), Cause: err}
}

func roleProblemKind(problem string) aierr.ErrorKind {
	switch problem {
	case role.ProblemRoleUnassigned:
		return aierr.ErrKindRoleUnassigned
	case role.ProblemProviderDisabled:
		return aierr.ErrKindProviderDisabled
	case role.ProblemModelUnavailable:
		return aierr.ErrKindModelUnavailable
	case role.ProblemCapabilityMissing:
		return aierr.ErrKindUnsupported
	}
	return aierr.ErrKindRoleUnassigned
}

func toAIErrorPlain(err error) *aierr.AIError {
	var ae *aierr.AIError
	if errors.As(err, &ae) {
		c := *ae
		return &c
	}
	switch {
	case errors.Is(err, context.Canceled):
		return &aierr.AIError{Kind: aierr.ErrKindCanceled, Message: aierr.Truncate(err.Error()), Cause: err}
	case errors.Is(err, context.DeadlineExceeded):
		return &aierr.AIError{Kind: aierr.ErrKindTimeout, Message: aierr.Truncate(err.Error()), Cause: err}
	default:
		return &aierr.AIError{Kind: aierr.ErrKindNetwork, Message: aierr.Truncate(err.Error()), Cause: err}
	}
}

func canceledError(ctx context.Context, rr role.ResolvedRole) error {
	return annotate(&aierr.AIError{Kind: aierr.ErrKindCanceled, Message: "canceled by caller", Cause: ctx.Err()}, rr)
}

func summaryOf(rr role.ResolvedRole) ProviderSummary {
	return ProviderSummary{
		ProviderID:   rr.Provider.ID,
		ProviderName: rr.Provider.Name,
		Protocol:     rr.Provider.Protocol,
		ModelID:      rr.Model.ModelID,
	}
}

func (s *Service) log(rr role.ResolvedRole, op string, attempts int, start time.Time, err error) {
	if s.opts.Logger == nil {
		return
	}
	meta := CallMeta{
		Operation: op, ProviderID: rr.Provider.ID, ProviderName: rr.Provider.Name,
		Protocol: rr.Provider.Protocol, Model: rr.Model.ModelID,
		Attempts: attempts, Elapsed: s.opts.Clock.Now().Sub(start), OK: err == nil,
	}
	if err != nil {
		ae := toAIErrorPlain(err)
		meta.ErrorKind = ae.Kind
		meta.StatusCode = ae.StatusCode
	}
	s.opts.Logger.LogCall(meta)
}

// canceledIfDone reports ErrKindCanceled before resolution so a canceled
// caller never surfaces as role_unassigned (§5.8 rule 4).
func canceledIfDone(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return &aierr.AIError{Kind: aierr.ErrKindCanceled, Message: "canceled by caller", Cause: err}
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
