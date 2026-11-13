package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"researchos/internal/ai/aierr"
	"researchos/internal/ai/aitest"
	"researchos/internal/ai/gateway"
	"researchos/internal/ai/model"
	"researchos/internal/ai/protocol"
	"researchos/internal/ai/provider"
	"researchos/internal/ai/role"
	"researchos/internal/ai/secret"
	"researchos/internal/foundation/foundationtest"
)

const modelsPayload = `{"data":[
	{"id":"chat-m","object":"model"},
	{"id":"emb-m","object":"model"}]}`

type stack struct {
	f      *foundationtest.Fixture
	mem    *secret.Memory
	provID string
	gw     *gateway.Service
	chat   gateway.ChatService
	mu     *sync.Mutex
	hits   int
	sleeps []time.Duration
}

// newStack wires provider+model+role+gateway against one fake OpenAI-style
// upstream. /models is answered generically so role assignments always have
// cached models; everything else goes to the test handler.
func newStack(t *testing.T, handler func(http.ResponseWriter, *http.Request)) *stack {
	t.Helper()
	st := &stack{mu: &sync.Mutex{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(modelsPayload))
			return
		}
		b, _ := io.ReadAll(r.Body)
		st.mu.Lock()
		st.hits++
		st.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	f := aitest.Fixture(t)
	db := f.Global().DB()
	mem := secret.NewMemory()
	factory := protocol.NewFactory(nil)
	rsvc, err := role.New(db, f.Clock)
	if err != nil {
		t.Fatal(err)
	}
	psvc, err := provider.New(db, provider.Options{
		Clock: f.Clock, Secrets: mem, Lister: factory, RoleCaps: role.RequiredCapability,
	})
	if err != nil {
		t.Fatal(err)
	}
	msvc, err := model.New(db, model.Options{Clock: f.Clock, Secrets: mem, Lister: factory})
	if err != nil {
		t.Fatal(err)
	}
	v, err := psvc.Create(context.Background(), provider.CreateProviderInput{
		Name: "fake", Protocol: provider.ProtocolOpenAIChat,
		BaseURL: srv.URL + "/v1", APIKey: "sk-gw", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := msvc.Sync(context.Background(), v.ID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	// emb-m is stored with the full protocol set; trim to embedding-only so
	// chat calls can never silently pick it (and vice versa).
	if _, err := db.Exec(`UPDATE provider_models SET capabilities = '["embedding"]' WHERE model_id = 'emb-m'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE provider_models SET capabilities = '["chat"]' WHERE model_id = 'chat-m'`); err != nil {
		t.Fatal(err)
	}

	st.f, st.mem, st.provID = f, mem, v.ID
	gw, err := gateway.New(gateway.Options{
		Clock:    f.Clock,
		Secrets:  mem,
		Roles:    rsvc,
		Factory:  factory,
		Settings: f.Settings(),
		Sleep: func(ctx context.Context, d time.Duration) error {
			st.mu.Lock()
			st.sleeps = append(st.sleeps, d)
			st.mu.Unlock()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	st.gw = gw
	st.chat = gateway.NewChatService(gw)

	ctx := context.Background()
	if _, err := rsvc.Assign(ctx, role.RoleChatMain, v.ID, "chat-m"); err != nil {
		t.Fatal(err)
	}
	if _, err := rsvc.Assign(ctx, role.RoleEmbedding, v.ID, "emb-m"); err != nil {
		t.Fatal(err)
	}
	return st
}

func (st *stack) nHits() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.hits
}

func (st *stack) nSleeps() []time.Duration {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]time.Duration, len(st.sleeps))
	copy(out, st.sleeps)
	return out
}

func chatReq() gateway.ChatRequest {
	return gateway.ChatRequest{Messages: []protocol.ChatMessage{{Role: protocol.RoleUser, Content: "hi"}}}
}

func wantAIError(t *testing.T, err error) *aierr.AIError {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	var ae *aierr.AIError
	if !errors.As(err, &ae) {
		t.Fatalf("not an AIError: %#v %v", err, err)
	}
	return ae
}

func okChatHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"c1","model":"chat-m","choices":[{"message":{"content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
}

func TestChatHappyPathUsesRoleModel(t *testing.T) {
	var lastBody string
	st := newStack(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		okChatHandler(w, r)
	})
	resp, summary, err := st.gw.Chat(context.Background(), role.RoleChatMain, chatReq())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "answer" || resp.Attempts != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	if summary.ProviderID != st.provID || summary.ProviderName != "fake" ||
		summary.Protocol != provider.ProtocolOpenAIChat || summary.ModelID != "chat-m" {
		t.Fatalf("summary = %+v", summary)
	}
	if !strings.Contains(lastBody, `"model":"chat-m"`) {
		t.Fatalf("model not filled from role: %s", lastBody)
	}
	// ChatService facade defaults to chat_main.
	if resp2, err := st.chat.Chat(context.Background(), chatReq()); err != nil || resp2.Content != "answer" {
		t.Fatalf("facade: %v %v", resp2, err)
	}
}

func TestRetryPolicy(t *testing.T) {
	var count int
	st := newStack(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		switch count {
		case 1, 2:
			http.Error(w, `{"error":{"message":"boom"}}`, http.StatusInternalServerError)
		default:
			okChatHandler(w, r)
		}
	})
	resp, _, err := st.gw.Chat(context.Background(), role.RoleChatMain, chatReq())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Attempts != 3 || count != 3 {
		t.Fatalf("attempts = %d hits = %d", resp.Attempts, count)
	}
	sleeps := st.nSleeps()
	if len(sleeps) != 2 {
		t.Fatalf("sleeps = %v", sleeps)
	}
	if sleeps[0] < 800*time.Millisecond || sleeps[0] > 1200*time.Millisecond {
		t.Fatalf("first backoff = %v want ≈1s±20%%", sleeps[0])
	}
	if sleeps[1] < 3200*time.Millisecond || sleeps[1] > 4800*time.Millisecond {
		t.Fatalf("second backoff = %v want ≈4s±20%%", sleeps[1])
	}
}

func TestExhaustedRetriesCarryMetadata(t *testing.T) {
	st := newStack(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"down"}}`, http.StatusInternalServerError)
	})
	_, _, err := st.gw.Chat(context.Background(), role.RoleChatMain, chatReq())
	ae := wantAIError(t, err)
	if ae.Kind != aierr.ErrKindServer || ae.Attempts != 3 {
		t.Fatalf("ae = %+v", ae)
	}
	if ae.ProviderName != "fake" || ae.Model != "chat-m" || ae.Protocol != string(provider.ProtocolOpenAIChat) {
		t.Fatalf("missing locating context: %v", ae)
	}
}

func TestNoRetryOnAuthFailure(t *testing.T) {
	st := newStack(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"invalid api key sk-gw"}}`, http.StatusUnauthorized)
	})
	_, _, err := st.gw.Chat(context.Background(), role.RoleChatMain, chatReq())
	ae := wantAIError(t, err)
	if ae.Kind != aierr.ErrKindAuth || ae.Attempts != 1 {
		t.Fatalf("auth: %+v", ae)
	}
	if st.nHits() != 1 {
		t.Fatalf("auth retried: hits=%d", st.nHits())
	}
	if strings.Contains(ae.Error(), "sk-gw") {
		t.Fatalf("secret leaked: %s", ae.Error())
	}
}

func TestLongRetryAfterGoesToManualRetry(t *testing.T) {
	st := newStack(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3600")
		http.Error(w, `{"error":{"message":"quota window"}}`, http.StatusTooManyRequests)
	})
	_, _, err := st.gw.Chat(context.Background(), role.RoleChatMain, chatReq())
	ae := wantAIError(t, err)
	if ae.Kind != aierr.ErrKindRateLimit || ae.Attempts != 1 || len(st.nSleeps()) != 0 {
		t.Fatalf("rate limit long window: %+v", ae)
	}
}

func TestRespectShortRetryAfter(t *testing.T) {
	var count int
	st := newStack(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		if count == 1 {
			w.Header().Set("Retry-After", "3")
			http.Error(w, `{"error":{"message":"slow"}}`, http.StatusTooManyRequests)
			return
		}
		okChatHandler(w, r)
	})
	resp, _, err := st.gw.Chat(context.Background(), role.RoleChatMain, chatReq())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Attempts != 2 || len(st.nSleeps()) != 1 || st.nSleeps()[0] != 3*time.Second {
		t.Fatalf("resp=%+v sleeps=%v", resp, st.nSleeps())
	}
}

func TestPreNetworkFailures(t *testing.T) {
	st := newStack(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("network must not be reached")
	})
	ctx := context.Background()
	db := st.f.Global().DB()

	// rerank role is unassigned → role_unassigned.
	ae := wantAIError(t, chatErr(st, role.RoleRerank, ctx))
	if ae.Kind != aierr.ErrKindRoleUnassigned {
		t.Fatalf("kind = %v", ae)
	}

	// provider_disabled keeps the assignment (AC-04.3).
	if _, err := db.Exec(`UPDATE providers SET enabled = 0 WHERE id = ?`, st.provID); err != nil {
		t.Fatal(err)
	}
	if ae := wantAIError(t, chatErr(st, role.RoleChatMain, ctx)); ae.Kind != aierr.ErrKindProviderDisabled {
		t.Fatalf("kind = %v", ae)
	}
	if _, err := db.Exec(`UPDATE providers SET enabled = 1 WHERE id = ?`, st.provID); err != nil {
		t.Fatal(err)
	}

	// secret_unavailable: drop the entry behind the reference (§4.5).
	if err := st.mem.Delete(ctx, secret.ProviderKeyRef(st.provID)); err != nil {
		t.Fatal(err)
	}
	if ae := wantAIError(t, chatErr(st, role.RoleChatMain, ctx)); ae.Kind != aierr.ErrKindSecretUnavailable {
		t.Fatalf("kind = %v", ae)
	}
	if err := st.mem.Set(ctx, secret.ProviderKeyRef(st.provID), []byte("sk-gw")); err != nil {
		t.Fatal(err)
	}

	// model_unavailable: assignment points at a row that no longer exists.
	if _, err := db.Exec(`UPDATE role_assignments SET model_id = 'ghost' WHERE role = 'chat_main'`); err != nil {
		t.Fatal(err)
	}
	if ae := wantAIError(t, chatErr(st, role.RoleChatMain, ctx)); ae.Kind != aierr.ErrKindModelUnavailable {
		t.Fatalf("kind = %v", ae)
	}
	if _, err := db.Exec(`UPDATE role_assignments SET model_id = 'chat-m' WHERE role = 'chat_main'`); err != nil {
		t.Fatal(err)
	}

	// requested model differs from the role assignment → never switched.
	_, _, err := st.gw.Chat(ctx, role.RoleChatMain, gateway.ChatRequest{
		Model:    "other-model",
		Messages: []protocol.ChatMessage{{Role: protocol.RoleUser, Content: "hi"}},
	})
	if ae := wantAIError(t, err); ae.Kind != aierr.ErrKindModelNotFound {
		t.Fatalf("kind = %v", ae)
	}
	if st.nHits() != 0 {
		t.Fatalf("network hit %d times", st.nHits())
	}
}

func chatErr(st *stack, r role.Role, ctx context.Context) error {
	_, _, err := st.gw.Chat(ctx, r, chatReq())
	return err
}

func TestCancellation(t *testing.T) {
	st := newStack(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(30 * time.Millisecond)
		okChatHandler(w, nil)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ae := wantAIError(t, chatErr(st, role.RoleChatMain, ctx))
	if ae.Kind != aierr.ErrKindCanceled {
		t.Fatalf("kind = %v", ae)
	}
}

func TestEmbedAndProfile(t *testing.T) {
	st := newStack(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "emb-m",
			"data": []any{
				map[string]any{"index": 0, "embedding": []float32{0.1, 0.2, 0.3, 0.4}},
				map[string]any{"index": 1, "embedding": []float32{0.5, 0.6, 0.7, 0.8}},
			},
		})
	})
	ctx := context.Background()
	emb, err := st.gw.Embed(ctx, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(emb.Vectors) != 2 || len(emb.Vectors[0]) != 4 {
		t.Fatalf("emb = %+v", emb)
	}
	profileID, dim, err := st.gw.CurrentProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if profileID == "" || dim != 4 {
		t.Fatalf("profile = %q dim=%d", profileID, dim)
	}
	if gateway.EmbeddingProfileID("another-provider", "emb-m") == profileID {
		t.Fatal("profile id must encode the provider")
	}
	// Empty input is a no-op without network.
	before := st.nHits()
	if _, err := st.gw.Embed(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if st.nHits() != before {
		t.Fatal("empty embed must not call upstream")
	}
}

func TestRerankUnsupported(t *testing.T) {
	st := newStack(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("rerank must not touch the network (O-01)")
	})
	ae := wantAIError(t, func() error { _, err := st.gw.Rerank(context.Background(), "q", []string{"d"}, 1); return err }())
	if ae.Kind != aierr.ErrKindUnsupported {
		t.Fatalf("rerank: %v", ae)
	}
}
