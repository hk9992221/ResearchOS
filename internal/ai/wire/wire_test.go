package wire_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"researchos/internal/ai/aierr"
	"researchos/internal/ai/aitest"
	"researchos/internal/ai/gateway"
	"researchos/internal/ai/migrations"
	"researchos/internal/ai/output"
	"researchos/internal/ai/protocol"
	"researchos/internal/ai/provider"
	"researchos/internal/ai/role"
	"researchos/internal/ai/secret"
	"researchos/internal/ai/wire"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/storage"
)

func fakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"wire-chat"},{"id":"wire-emb"}]}`))
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"id":"w1","model":"wire-chat","choices":[{"message":{"content":"wired answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":3}}`))
		case "/v1/embeddings":
			_, _ = w.Write([]byte(`{"model":"wire-emb","data":[{"index":0,"embedding":[0.1,0.2]}]}`))
		default:
			http.Error(w, `{"error":{"message":"unexpected path"}}`, 400)
		}
	}))
}

func chatReq(text string) gateway.ChatRequest {
	return gateway.ChatRequest{Messages: []protocol.ChatMessage{{Role: protocol.RoleUser, Content: text}}}
}

func TestFullStackRoundTrip(t *testing.T) {
	srv := fakeServer(t)
	defer srv.Close()
	f := aitest.FixtureWithProject(t)
	mem := secret.NewMemory()

	stack, err := wire.New(wire.Options{
		Global:            f.Global().DB(),
		Clock:             f.Clock,
		Secrets:           mem,
		WorkspaceSettings: f.Settings(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	view, err := stack.Providers.Create(ctx, provider.CreateProviderInput{
		Name: "wire", Protocol: provider.ProtocolOpenAIChat,
		BaseURL: srv.URL + "/v1", APIKey: "sk-wire", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.Models.Sync(ctx, view.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := stack.Roles.Assign(ctx, role.RoleChatMain, view.ID, "wire-chat"); err != nil {
		t.Fatal(err)
	}

	// ChatService facade with the chat_main default.
	resp, err := stack.Chat.Chat(ctx, chatReq("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "wired answer" || resp.Usage.OutputTokens != 3 {
		t.Fatalf("resp = %+v", resp)
	}

	// Embedding service before assignment → role_unassigned, no network.
	if _, _, err := stack.Embedding.CurrentProfile(ctx); err == nil {
		t.Fatal("expected unassigned embedding")
	} else {
		var ae *aierr.AIError
		if !errors.As(err, &ae) || ae.Kind != aierr.ErrKindRoleUnassigned {
			t.Fatalf("kind = %v", err)
		}
	}
	if _, err := stack.Roles.Assign(ctx, role.RoleEmbedding, view.ID, "wire-emb"); err != nil {
		t.Fatal(err)
	}
	emb, err := stack.Embedding.Embed(ctx, []string{"one"})
	if err != nil || len(emb.Vectors) != 1 {
		t.Fatalf("emb = %+v %v", emb, err)
	}
	profileID, dim, err := stack.Embedding.CurrentProfile(ctx)
	if err != nil || profileID == "" || dim != 2 {
		t.Fatalf("profile = %q dim=%d err=%v", profileID, dim, err)
	}

	// Generation through the gateway end-to-end: draft row + event.
	outs, err := output.NewOutputService(f.Project.DB(), output.OutputOptions{
		ProjectID: f.ProjectID, Clock: f.Clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	genSvc, err := output.NewGenerationService(output.GenerationOptions{
		Outputs: outs, Runner: stack.Gateway, Events: f.Project.Events(),
	})
	if err != nil {
		t.Fatal(err)
	}
	entID := ids.MustNew(f.Clock)
	res, err := genSvc.Generate(ctx, output.GenerateRequest{
		ProjectID: f.ProjectID, TaskKind: "document_understanding", PromptVersion: "wire-v1",
		Messages: []protocol.ChatMessage{{Role: protocol.RoleUser, Content: "summarize"}},
		Entity:   &output.EntityRef{Type: "idea", ID: entID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output.Provenance.ProviderName != "wire" || res.Output.ContentMD != "wired answer" {
		t.Fatalf("output = %+v", res.Output)
	}
	acts, err := f.Project.Reader().List(ctx, events.Query{EventTypes: []events.Type{"ai.analysis_completed"}})
	if err != nil || len(acts) != 1 {
		t.Fatalf("events = %+v %v", acts, err)
	}
	if acts[0].EntityID != entID || acts[0].Payload.Summary == "" {
		t.Fatalf("event = %+v", acts[0])
	}

	// Rerank stays unsupported (O-01).
	_, err = stack.Rerank.Rerank(ctx, "q", []string{"d"}, 1)
	var ae *aierr.AIError
	if !errors.As(err, &ae) || ae.Kind != aierr.ErrKindUnsupported {
		t.Fatalf("rerank = %v", err)
	}

	// The provider view (UI/MCP shape) carries neither key nor ref (AC-14.1).
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-wire") || strings.Contains(strings.ToLower(string(raw)), "api_key_ref") {
		t.Fatalf("view leaked key material: %s", raw)
	}
}

func TestMigrationsIdempotentAndBanded(t *testing.T) {
	f := aitest.FixtureWithProject(t)
	ctx := context.Background()
	migs, err := migrations.All()
	if err != nil {
		t.Fatal(err)
	}

	// SPEC-02 tables exist in both databases.
	type want struct {
		db   *sql.DB
		name string
		kind storage.Kind
		path string
		tbls []string
	}
	cases := []want{
		{f.Global().DB(), "global", storage.KindGlobal, filepath.Join(f.Root, "global.db"),
			[]string{"provider_models", "paper_index_docs", "paper_chunks", "paper_fts", "paper_chunk_vectors"}},
		{f.Project.DB(), "project", storage.KindProject, filepath.Join(f.Root, "projects", f.ProjectID, "project.db"),
			[]string{"ai_outputs", "index_docs", "index_chunks", "index_chunks_fts", "chunk_embeddings"}},
	}
	for _, c := range cases {
		for _, tbl := range c.tbls {
			var n string
			err := c.db.QueryRowContext(ctx,
				`SELECT name FROM sqlite_master WHERE name = ?`, tbl).Scan(&n)
			if err != nil {
				t.Fatalf("%s: table %s missing: %v", c.name, tbl, err)
			}
		}
		var band int
		if err := c.db.QueryRowContext(ctx,
			`SELECT count(*) FROM schema_migrations WHERE version BETWEEN 100 AND 199`).Scan(&band); err != nil {
			t.Fatal(err)
		}
		if band != 2 {
			t.Fatalf("%s: SPEC-02 band rows = %d want 2", c.name, band)
		}
		// A second Migrate with the same set is a verified no-op (AC-15.3).
		res, err := storage.Migrate(ctx, c.db, c.kind, c.path, storage.Options{SkipBackup: true, Extra: migs})
		if err != nil {
			t.Fatalf("%s: remigrate: %v", c.name, err)
		}
		if len(res.Applied) != 0 {
			t.Fatalf("%s: re-applied %v", c.name, res.Applied)
		}
	}
}
