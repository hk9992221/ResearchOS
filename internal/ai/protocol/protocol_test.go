package protocol_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"researchos/internal/ai/aierr"
	"researchos/internal/ai/protocol"
	"researchos/internal/ai/provider"
)

type upstream struct {
	t         *testing.T
	server    *httptest.Server
	requests  []recordedRequest
	responses []func(w http.ResponseWriter, r *http.Request) // nil → pop next
}

type recordedRequest struct {
	method, path, rawQuery string
	header                 http.Header
	body                   string
}

func newUpstream(t *testing.T, handlers ...func(http.ResponseWriter, *http.Request)) *upstream {
	u := &upstream{t: t, responses: handlers}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.requests = append(u.requests, recordedRequest{
			method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery,
			header: r.Header.Clone(), body: string(b),
		})
		i := len(u.requests) - 1
		r.Body = io.NopCloser(strings.NewReader(u.requests[i].body))
		if i < len(u.responses) {
			u.responses[i](w, r)
			return
		}
		http.Error(w, `{"error":{"message":"no scripted response"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(u.server.Close)
	return u
}

func jsonHandler(status int, payload string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}
}

func cfgFor(proto provider.Protocol, baseURL string) provider.ProviderConfig {
	return provider.ProviderConfig{
		ID: "018c0000-0000-7000-8000-000000000000", Name: "test",
		Protocol: proto, BaseURL: baseURL, APIKeyRef: "ref",
	}
}

func mustChatAdapter(t *testing.T, proto provider.Protocol, u *upstream, key string) protocol.ChatAdapter {
	t.Helper()
	f := protocol.NewFactory(nil)
	adapter, err := f.New(context.Background(), cfgFor(proto, u.server.URL+"/v1"), provider.Secret(key))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := protocol.Chat(adapter)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func TestOpenAIChatRequestsAndParsing(t *testing.T) {
	u := newUpstream(t,
		jsonHandler(http.StatusOK, `{"data":[{"id":"gpt-4o","object":"model"},{"id":"gpt-4o-mini","object":"model"}]}`),
		jsonHandler(http.StatusOK, `{"id":"chatcmpl-9","model":"gpt-4o","choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":5}}`),
		jsonHandler(http.StatusOK, `{"model":"gpt-4o-embed","data":[{"index":0,"embedding":[0.5,0.25]},{"index":1,"embedding":[-1,0]}]}`),
	)
	f := protocol.NewFactory(nil)
	adapter, err := f.New(context.Background(), cfgFor(provider.ProtocolOpenAIChat, u.server.URL+"/v1"), provider.Secret("sk-key-1"))
	if err != nil {
		t.Fatal(err)
	}

	models, err := adapter.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ModelID != "gpt-4o" {
		t.Fatalf("models = %+v", models)
	}
	if u.requests[0].header.Get("Authorization") != "Bearer sk-key-1" {
		t.Fatalf("auth = %q", u.requests[0].header.Get("Authorization"))
	}
	if u.requests[0].path != "/v1/models" {
		t.Fatalf("path = %q", u.requests[0].path)
	}

	temp := 0.2
	resp, err := mustChatAdapter(t, provider.ProtocolOpenAIChat, u, "sk-key-1").Chat(context.Background(), protocol.ChatRequest{
		Model:           "gpt-4o",
		Messages:        []protocol.ChatMessage{{Role: protocol.RoleSystem, Content: "be brief"}, {Role: protocol.RoleUser, Content: "hi"}},
		Temperature:     &temp,
		MaxOutputTokens: 256,
		ResponseFormat:  &protocol.ResponseFormat{Type: "json_object"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hello" || resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 5 || resp.RequestID != "chatcmpl-9" {
		t.Fatalf("resp = %+v", resp)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(u.requests[1].body), &body); err != nil {
		t.Fatal(err)
	}
	if body["max_tokens"].(float64) != 256 || body["temperature"].(float64) != 0.2 {
		t.Fatalf("body = %v", body)
	}
	if rf, ok := body["response_format"].(map[string]any); !ok || rf["type"] != "json_object" {
		t.Fatalf("response_format = %v", body["response_format"])
	}

	ea, err := protocol.Embedding(adapter)
	if err != nil {
		t.Fatal(err)
	}
	emb, err := ea.Embed(context.Background(), protocol.EmbedRequest{Model: "gpt-4o-embed", Inputs: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(emb.Vectors) != 2 || emb.Vectors[1][0] != -1 {
		t.Fatalf("vectors = %v", emb.Vectors)
	}
	if u.requests[2].path != "/v1/embeddings" {
		t.Fatalf("embed path = %q", u.requests[2].path)
	}
}

func TestOpenAIResponsesAdapter(t *testing.T) {
	u := newUpstream(t,
		jsonHandler(http.StatusOK, `{"id":"resp-1","model":"gpt-4.1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"part A"},{"type":"refusal","text":"skip"}]}],"usage":{"input_tokens":3,"output_tokens":4}}`),
	)
	ca := mustChatAdapter(t, provider.ProtocolOpenAIResponses, u, "sk-key")
	resp, err := ca.Chat(context.Background(), protocol.ChatRequest{
		Model:           "gpt-4.1",
		Messages:        []protocol.ChatMessage{{Role: protocol.RoleUser, Content: "go"}},
		MaxOutputTokens: 64,
		ResponseFormat:  &protocol.ResponseFormat{Type: "json_schema", Name: "outline", JSONSchema: json.RawMessage(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "part A" || resp.Usage.OutputTokens != 4 || resp.RequestID != "resp-1" {
		t.Fatalf("resp = %+v", resp)
	}
	if u.requests[0].path != "/v1/responses" {
		t.Fatalf("path = %q", u.requests[0].path)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(u.requests[0].body), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["input"]; !ok {
		t.Fatalf("responses must use input: %v", body)
	}
	if body["max_output_tokens"].(float64) != 64 {
		t.Fatalf("max_output_tokens = %v", body["max_output_tokens"])
	}
	text := body["text"].(map[string]any)
	format := text["format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Fatalf("format = %v", format)
	}
}

func TestKeylessSendsNoAuthHeader(t *testing.T) {
	u := newUpstream(t, jsonHandler(http.StatusOK, `{"data":[]}`))
	f := protocol.NewFactory(nil)
	adapter, err := f.New(context.Background(), cfgFor(provider.ProtocolOpenAIChat, u.server.URL+"/v1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if u.requests[0].header.Get("Authorization") != "" {
		t.Fatal("keyless provider must not send Authorization")
	}
}

func TestGeminiRequests(t *testing.T) {
	u := newUpstream(t,
		// ListModels walks one pageToken page, so two slots are consumed.
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-chat","displayName":"Chat","supportedGenerationMethods":["generateContent","streamGenerateContent"]},{"name":"models/gemini-embed","supportedGenerationMethods":["embedContent","batchEmbedContents"]}],"nextPageToken":"p2"}`))
		},
		jsonHandler(http.StatusOK, `{"models":[{"name":"models/gemini-all"}]}`),
		jsonHandler(http.StatusOK, `{"candidates":[{"content":{"parts":[{"text":"answer"},{"text":" more"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3},"responseId":"gl-1"}`),
		// batch embed: 3 inputs under a 100 cap arrives in one call.
		jsonHandler(http.StatusOK, `{"embeddings":[{"values":[1,0]},{"values":[0,1]},{"values":[1,1]}]}`),
	)
	f := protocol.NewFactory(nil)
	adapter, err := f.New(context.Background(), cfgFor(provider.ProtocolGemini, u.server.URL+"/v1beta"), provider.Secret("gk-1"))
	if err != nil {
		t.Fatal(err)
	}

	models, err := adapter.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("models = %+v", models)
	}
	byID := map[string][]provider.Capability{}
	for _, m := range models {
		byID[m.ModelID] = m.Capabilities
	}
	if strings.Join(caps(byID["gemini-chat"]), ",") != "chat" {
		t.Fatalf("chat caps = %v", byID["gemini-chat"])
	}
	if strings.Join(caps(byID["gemini-embed"]), ",") != "embedding" {
		t.Fatalf("embed caps = %v", byID["gemini-embed"])
	}
	if strings.Join(caps(byID["gemini-all"]), ",") != "chat,embedding" {
		t.Fatalf("fallback caps = %v", byID["gemini-all"])
	}
	if u.requests[1].rawQuery == "" || !strings.Contains(u.requests[1].rawQuery, "pageToken=p2") {
		t.Fatalf("pagination query = %q", u.requests[1].rawQuery)
	}
	if u.requests[0].header.Get("x-goog-api-key") != "gk-1" {
		t.Fatal("goog key header missing")
	}

	ca, err := protocol.Chat(adapter)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ca.Chat(context.Background(), protocol.ChatRequest{
		Model: "gemini-chat",
		Messages: []protocol.ChatMessage{
			{Role: protocol.RoleSystem, Content: "sys"},
			{Role: protocol.RoleUser, Content: "q"},
			{Role: protocol.RoleAssistant, Content: "a"},
			{Role: protocol.RoleUser, Content: "q2"},
		},
		ResponseFormat: &protocol.ResponseFormat{Type: "json_object"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "answer more" || resp.FinishReason != "STOP" || resp.Usage.InputTokens != 7 {
		t.Fatalf("resp = %+v", resp)
	}
	if u.requests[2].path != "/v1beta/models/gemini-chat:generateContent" {
		t.Fatalf("path = %q", u.requests[2].path)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(u.requests[2].body), &body); err != nil {
		t.Fatal(err)
	}
	if body["systemInstruction"] == nil {
		t.Fatal("system instruction missing")
	}
	contents := body["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("contents = %v", contents)
	}
	if contents[1].(map[string]any)["role"] != "model" {
		t.Fatalf("assistant must map to model: %v", contents[1])
	}
	gen := body["generationConfig"].(map[string]any)
	if gen["responseMimeType"] != "application/json" {
		t.Fatalf("gen config = %v", gen)
	}

	ea, err := protocol.Embedding(adapter)
	if err != nil {
		t.Fatal(err)
	}
	emb, err := ea.Embed(context.Background(), protocol.EmbedRequest{Model: "gemini-embed", Inputs: []string{"x", "y", "z"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(emb.Vectors) != 3 || u.requests[3].path != "/v1beta/models/gemini-embed:batchEmbedContents" {
		t.Fatalf("emb = %+v path = %q", emb, u.requests[3].path)
	}
	var embedBody map[string]any
	json.Unmarshal([]byte(u.requests[3].body), &embedBody)
	if reqs, ok := embedBody["requests"].([]any); !ok || len(reqs) != 3 {
		t.Fatalf("embed requests = %v", embedBody["requests"])
	}
}

func TestGeminiBatchSplit(t *testing.T) {
	// 250 inputs split into 100 + 100 + 50; each slot asserts its size.
	u := newUpstream(t,
		batchEmbedHandler(t, 100, 1),
		batchEmbedHandler(t, 100, 2),
		batchEmbedHandler(t, 50, 3),
	)
	f := protocol.NewFactory(nil)
	adapter, _ := f.New(context.Background(), cfgFor(provider.ProtocolGemini, u.server.URL), nil)
	ea, err := protocol.Embedding(adapter)
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]string, 250)
	for i := range inputs {
		inputs[i] = "t"
	}
	emb, err := ea.Embed(context.Background(), protocol.EmbedRequest{Model: "e", Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if len(emb.Vectors) != 250 || emb.Vectors[99][0] != 1 || emb.Vectors[100][0] != 2 || emb.Vectors[200][0] != 3 {
		t.Fatalf("vectors split wrong: %d %+v", len(emb.Vectors), emb.Vectors[200])
	}
	if len(u.requests) != 3 {
		t.Fatalf("batches = %d", len(u.requests))
	}
}

func TestAnthropicRequests(t *testing.T) {
	u := newUpstream(t,
		jsonHandler(http.StatusOK, `{"data":[{"id":"claude-a","display_name":"Claude A"},{"id":"claude-b"}],"has_more":true,"last_id":"claude-b"}`),
		jsonHandler(http.StatusOK, `{"data":[{"id":"claude-c","display_name":"Claude C"}],"has_more":false}`),
		jsonHandler(http.StatusOK, `{"id":"msg_1","model":"claude-x","content":[{"type":"text","text":"hi "},{"type":"thinking","text":"ignore"},{"type":"text","text":"there"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`),
	)
	f := protocol.NewFactory(nil)
	adapter, err := f.New(context.Background(), cfgFor(provider.ProtocolAnthropic, u.server.URL+"/v1"), provider.Secret("ak-1"))
	if err != nil {
		t.Fatal(err)
	}
	models, err := adapter.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 || models[2].DisplayName != "Claude C" {
		t.Fatalf("models = %+v", models)
	}
	if !strings.Contains(u.requests[1].rawQuery, "after=claude-b") {
		t.Fatalf("cursor = %q", u.requests[1].rawQuery)
	}
	if u.requests[0].header.Get("x-api-key") != "ak-1" || u.requests[0].header.Get("anthropic-version") == "" {
		t.Fatal("anthropic auth headers missing")
	}

	ca, err := protocol.Chat(adapter)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ca.Chat(context.Background(), protocol.ChatRequest{
		Model: "claude-x",
		Messages: []protocol.ChatMessage{
			{Role: protocol.RoleSystem, Content: "be nice"},
			{Role: protocol.RoleUser, Content: "q"},
			{Role: protocol.RoleAssistant, Content: "a"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hi there" || resp.FinishReason != "end_turn" || resp.Usage.OutputTokens != 3 {
		t.Fatalf("resp = %+v", resp)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(u.requests[2].body), &body); err != nil {
		t.Fatal(err)
	}
	if body["system"] != "be nice" {
		t.Fatalf("system = %v", body["system"])
	}
	// max_tokens is mandatory: callers omitting it get the default.
	if body["max_tokens"].(float64) != 4096 {
		t.Fatalf("max_tokens = %v", body["max_tokens"])
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 2 || msgs[1].(map[string]any)["role"] != "assistant" {
		t.Fatalf("messages = %v", msgs)
	}

	// Anthropic must never expose embeddings (§5.2, AC-04.2 floor).
	if _, err := protocol.Embedding(adapter); err == nil {
		t.Fatal("anthropic must not satisfy EmbeddingAdapter")
	}
	if _, err := f.New(context.Background(), cfgFor(provider.ProtocolAnthropic, u.server.URL+"/v1"), provider.Secret("ak")); err != nil {
		t.Fatal(err)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		payload string
		want    aierr.ErrorKind
		after   time.Duration
	}{
		{"auth401", http.StatusUnauthorized, `{"error":{"message":"bad key sk-secret","type":"authentication_error"}}`, aierr.ErrKindAuth, 0},
		{"rate429", http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`, aierr.ErrKindRateLimit, 0},
		{"server503", http.StatusServiceUnavailable, `upstream busy`, aierr.ErrKindServer, 0},
		{"model404", http.StatusNotFound, `{"error":{"message":"no such model"}}`, aierr.ErrKindModelNotFound, 0},
		{"context400", http.StatusBadRequest, `{"error":{"message":"This model's maximum context length is 8192 tokens","code":"context_length_exceeded"}}`, aierr.ErrKindContextTooLong, 0},
		{"invalid400", http.StatusBadRequest, `{"error":{"message":"bad input"}}`, aierr.ErrKindInvalidRequest, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newUpstream(t, jsonHandler(tc.status, tc.payload))
			ca := mustChatAdapter(t, provider.ProtocolOpenAIChat, u, "sk-secret")
			_, err := ca.Chat(context.Background(), protocol.ChatRequest{Model: "m", Messages: []protocol.ChatMessage{{Role: protocol.RoleUser, Content: "x"}}})
			var ae *aierr.AIError
			if !errors.As(err, &ae) {
				t.Fatalf("not AIError: %v", err)
			}
			if ae.Kind != tc.want || ae.StatusCode != tc.status {
				t.Fatalf("kind = %s status = %d want %s/%d", ae.Kind, ae.StatusCode, tc.want, tc.status)
			}
			if strings.Contains(ae.Error(), "sk-secret") {
				t.Fatalf("secret leaked: %v", ae.Error())
			}
			if tc.after > 0 && ae.RetryAfter != tc.after {
				t.Fatalf("retry after = %v", ae.RetryAfter)
			}
		})
	}
}

func TestRetryAfterHeaderParsed(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		http.Error(w, `{"error":{"message":"quota"}}`, http.StatusTooManyRequests)
	})
	ca := mustChatAdapter(t, provider.ProtocolOpenAIChat, u, "k")
	_, err := ca.Chat(context.Background(), protocol.ChatRequest{Model: "m", Messages: []protocol.ChatMessage{{Role: protocol.RoleUser, Content: "x"}}})
	var ae *aierr.AIError
	if !errors.As(err, &ae) {
		t.Fatal(err)
	}
	if ae.RetryAfter != 7*time.Second {
		t.Fatalf("RetryAfter = %v", ae.RetryAfter)
	}
}

func TestRelayWithoutModelsEndpoint(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"Not Found"}`, http.StatusNotFound)
	})
	f := protocol.NewFactory(nil)
	adapter, err := f.New(context.Background(), cfgFor(provider.ProtocolOpenAIChat, u.server.URL), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.ListModels(context.Background())
	var ae *aierr.AIError
	if !errors.As(err, &ae) {
		t.Fatal(err)
	}
	if ae.Kind != aierr.ErrKindModelNotFound && ae.Kind != aierr.ErrKindUnsupported {
		t.Fatalf("kind = %s", ae.Kind)
	}
	if strings.Contains(ae.Error(), "Authorization") {
		t.Fatal("header echoed")
	}
}

func TestTransportTimeoutAndCancel(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		jsonHandler(http.StatusOK, `{"choices":[{"message":{"content":"late"},"finish_reason":"stop"}]}`)(w, nil)
	})
	ca := mustChatAdapter(t, provider.ProtocolOpenAIChat, u, "k")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := ca.Chat(ctx, protocol.ChatRequest{Model: "m", Messages: []protocol.ChatMessage{{Role: protocol.RoleUser, Content: "x"}}})
	var ae *aierr.AIError
	if !errors.As(err, &ae) || ae.Kind != aierr.ErrKindTimeout {
		t.Fatalf("timeout err = %v", err)
	}

	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	_, err = ca.Chat(cctx, protocol.ChatRequest{Model: "m", Messages: []protocol.ChatMessage{{Role: protocol.RoleUser, Content: "x"}}})
	if !errors.As(err, &ae) || ae.Kind != aierr.ErrKindCanceled {
		t.Fatalf("canceled err = %v", err)
	}
}

func batchEmbedHandler(t *testing.T, want int, value float32) func(http.ResponseWriter, *http.Request) {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Requests []any `json:"requests"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode batch: %v", err)
		}
		if len(body.Requests) != want {
			http.Error(w, fmt.Sprintf("want %d got %d", want, len(body.Requests)), 500)
			return
		}
		vals := make([]map[string]any, 0, want)
		for range body.Requests {
			vals = append(vals, map[string]any{"values": []float32{value}})
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mustJSON(t, map[string]any{"embeddings": vals}))
	}
}

func caps(cs []provider.Capability) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
