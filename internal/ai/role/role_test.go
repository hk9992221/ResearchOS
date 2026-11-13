package role_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"researchos/internal/ai/aitest"
	"researchos/internal/ai/model"
	"researchos/internal/ai/provider"
	"researchos/internal/ai/role"
	"researchos/internal/ai/secret"
	"researchos/internal/foundation/foundationtest"
)

func services(t *testing.T) (*foundationtest.Fixture, *sql.DB, provider.Service, model.Service, role.Service) {
	t.Helper()
	f := aitest.Fixture(t)
	db := f.Global().DB()
	mem := secret.NewMemory()
	lister := &fakeLister{}
	psvc, err := provider.New(db, provider.Options{
		Clock: f.Clock, Secrets: mem, Lister: lister, RoleCaps: role.RequiredCapability,
	})
	if err != nil {
		t.Fatal(err)
	}
	msvc, err := model.New(db, model.Options{Clock: f.Clock, Secrets: mem, Lister: lister})
	if err != nil {
		t.Fatal(err)
	}
	rsvc, err := role.New(db, f.Clock)
	if err != nil {
		t.Fatal(err)
	}
	return f, db, psvc, msvc, rsvc
}

type fakeLister struct{}

func (fakeLister) ListModels(context.Context, provider.ProviderConfig, provider.Secret) ([]provider.UpstreamModel, error) {
	return []provider.UpstreamModel{{ModelID: "chat-m"}, {ModelID: "emb-m", Capabilities: []provider.Capability{provider.CapabilityEmbedding}}}, nil
}

func createProvider(t *testing.T, ctx context.Context, psvc provider.Service, name string, proto provider.Protocol) string {
	t.Helper()
	v, err := psvc.Create(ctx, provider.CreateProviderInput{
		Name: name, Protocol: proto, BaseURL: "https://" + name + ".invalid/v1", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v.ID
}

func TestAssignValidationOrder(t *testing.T) {
	ctx := context.Background()
	_, _, psvc, msvc, rsvc := services(t)

	// Unknown role (rule 1).
	if _, err := rsvc.Assign(ctx, "mystery", "p", "m"); !errors.Is(err, role.ErrUnknownRole) {
		t.Fatalf("unknown role: %v", err)
	}
	// Missing provider (rule 2).
	if _, err := rsvc.Assign(ctx, role.RoleChatMain, "00000000-0000-7000-8000-000000000000", "m"); err == nil {
		t.Fatal("missing provider should fail")
	}
	// Disabled provider → provider_disabled.
	oaID := createProvider(t, ctx, psvc, "oa", provider.ProtocolOpenAIChat)
	if err := psvc.SetEnabled(ctx, oaID, false); err != nil {
		t.Fatal(err)
	}
	_, err := rsvc.Assign(ctx, role.RoleChatMain, oaID, "chat-m")
	var re *role.Error
	if !errors.As(err, &re) || re.Problem != role.ProblemProviderDisabled {
		t.Fatalf("disabled provider: %v", err)
	}
	if err := psvc.SetEnabled(ctx, oaID, true); err != nil {
		t.Fatal(err)
	}
	// Model not in cache → model_unavailable.
	if _, err := rsvc.Assign(ctx, role.RoleChatMain, oaID, "ghost"); err == nil {
		t.Fatal("ghost model accepted")
	}
	// Cache the models, then chat assignment succeeds.
	if _, err := msvc.Sync(ctx, oaID); err != nil {
		t.Fatal(err)
	}
	res, err := rsvc.Assign(ctx, role.RoleChatMain, oaID, "chat-m")
	if err != nil || res.Assignment.ModelID != "chat-m" {
		t.Fatalf("assign chat_main: %+v %v", res, err)
	}
	if res.ReindexRequired {
		t.Fatal("reindex flag belongs to embedding only")
	}

	// Anthropic + embedding → capability_missing even through the service
	// API directly (AC-04.2).
	anID := createProvider(t, ctx, psvc, "an", provider.ProtocolAnthropic)
	if _, err := msvc.Sync(ctx, anID); err != nil {
		t.Fatal(err)
	}
	_, err = rsvc.Assign(ctx, role.RoleEmbedding, anID, "chat-m")
	if !errors.As(err, &re) || re.Problem != role.ProblemCapabilityMissing {
		t.Fatalf("anthropic embedding: %v", err)
	}

	// A listed model marked unavailable is rejected after sync drift.
	if err := msvc.AddManual(ctx, oaID, "hand", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := rsvc.Assign(ctx, role.RoleEmbedding, oaID, "emb-m"); err != nil {
		t.Fatalf("assign embedding to cached model: %v", err)
	}
}

func TestEmbeddingReindexFlag(t *testing.T) {
	ctx := context.Background()
	_, _, psvc, msvc, rsvc := services(t)
	oa := createProvider(t, ctx, psvc, "oa", provider.ProtocolOpenAIChat)
	if _, err := msvc.Sync(ctx, oa); err != nil {
		t.Fatal(err)
	}
	other := createProvider(t, ctx, psvc, "oa2", provider.ProtocolOpenAIChat)
	if _, err := msvc.Sync(ctx, other); err != nil {
		t.Fatal(err)
	}

	res, err := rsvc.Assign(ctx, role.RoleEmbedding, oa, "emb-m")
	if err != nil {
		t.Fatal(err)
	}
	if !res.ReindexRequired {
		t.Fatal("first embedding assignment must require reindex (AC-04.4)")
	}
	res, err = rsvc.Assign(ctx, role.RoleEmbedding, oa, "emb-m")
	if err != nil || res.ReindexRequired {
		t.Fatalf("same assignment reindex: %+v %v", res, err)
	}
	res, err = rsvc.Assign(ctx, role.RoleEmbedding, other, "emb-m")
	if err != nil || !res.ReindexRequired {
		t.Fatalf("provider switch reindex: %+v %v", res, err)
	}
}

func TestResolveAndListView(t *testing.T) {
	ctx := context.Background()
	_, db, psvc, msvc, rsvc := services(t)
	oa := createProvider(t, ctx, psvc, "oa", provider.ProtocolOpenAIChat)
	if _, err := msvc.Sync(ctx, oa); err != nil {
		t.Fatal(err)
	}
	if _, err := rsvc.Assign(ctx, role.RoleChatMain, oa, "chat-m"); err != nil {
		t.Fatal(err)
	}

	rr, err := rsvc.Resolve(ctx, role.RoleChatMain)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Provider.ID != oa || rr.Model.ModelID != "chat-m" || rr.Capability != provider.CapabilityChat {
		t.Fatalf("resolved = %+v", rr)
	}

	// Unassigned roles resolve with role_unassigned and never fall back
	// silently (§5.6 last line).
	if _, err := rsvc.Resolve(ctx, role.RoleEmbedding); err == nil {
		t.Fatal("unassigned embedding resolved")
	}

	// Disabling the provider flips resolution to provider_disabled while
	// the assignment row stays (AC-01.3).
	if err := psvc.SetEnabled(ctx, oa, false); err != nil {
		t.Fatal(err)
	}
	_, err = rsvc.Resolve(ctx, role.RoleChatMain)
	var re *role.Error
	if !errors.As(err, &re) || re.Problem != role.ProblemProviderDisabled {
		t.Fatalf("disabled resolve: %v", err)
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM role_assignments`).Scan(&count)
	if count != 1 {
		t.Fatal("disable must not delete role assignment")
	}

	views, err := rsvc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("views = %+v", views)
	}
	problems := map[string]string{}
	for _, v := range views {
		problems[string(v.Role)] = v.Problem
	}
	if problems["chat_main"] != role.ProblemProviderDisabled ||
		problems["embedding"] != role.ProblemRoleUnassigned ||
		problems["rerank"] != role.ProblemRoleUnassigned {
		t.Fatalf("problems = %+v", views)
	}

	// Clear releases the reference so the provider can be deleted.
	if err := rsvc.Clear(ctx, role.RoleChatMain); err != nil {
		t.Fatal(err)
	}
	if err := rsvc.Clear(ctx, role.RoleChatMain); err != nil {
		t.Fatalf("clear unset must be no-op: %v", err)
	}
	if err := psvc.SetEnabled(ctx, oa, true); err != nil {
		t.Fatal(err)
	}
	if err := psvc.Delete(ctx, oa); err != nil {
		t.Fatalf("delete after clear: %v", err)
	}
}

func TestRerankRoleCapabilityFloor(t *testing.T) {
	ctx := context.Background()
	_, _, psvc, msvc, rsvc := services(t)
	oa := createProvider(t, ctx, psvc, "oa", provider.ProtocolOpenAIChat)
	if _, err := msvc.Sync(ctx, oa); err != nil {
		t.Fatal(err)
	}
	// No protocol offers rerank in v1 (O-01): assigning the rerank role must
	// surface capability_missing for every protocol.
	_, err := rsvc.Assign(ctx, role.RoleRerank, oa, "chat-m")
	var re *role.Error
	if !errors.As(err, &re) || re.Problem != role.ProblemCapabilityMissing {
		t.Fatalf("rerank assign: %v", err)
	}
	if !strings.Contains(re.Error(), "rerank") {
		t.Fatalf("problem detail = %v", re)
	}
}
