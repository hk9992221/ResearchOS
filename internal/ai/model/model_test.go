package model_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"researchos/internal/ai/aitest"
	"researchos/internal/ai/model"
	"researchos/internal/ai/provider"
	"researchos/internal/ai/role"
	"researchos/internal/ai/secret"
	"researchos/internal/foundation/foundationtest"
)

type stubLister struct {
	models []provider.UpstreamModel
	err    error
	calls  int
}

func (s *stubLister) ListModels(context.Context, provider.ProviderConfig, provider.Secret) ([]provider.UpstreamModel, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.models, nil
}

type env struct {
	f       *foundationtest.Fixture
	db      *sql.DB
	mem     *secret.Memory
	lister  *stubLister
	svc     model.Service
	provID  string
	baseURL string
}

func newEnv(t *testing.T, proto provider.Protocol) *env {
	t.Helper()
	f := aitest.Fixture(t)
	db := f.Global().DB()
	mem := secret.NewMemory()
	lister := &stubLister{}
	svc, err := model.New(db, model.Options{Clock: f.Clock, Secrets: mem, Lister: lister})
	if err != nil {
		t.Fatal(err)
	}
	psvc, err := provider.New(db, provider.Options{
		Clock: f.Clock, Secrets: mem, Lister: lister, RoleCaps: role.RequiredCapability,
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err := psvc.Create(context.Background(), provider.CreateProviderInput{
		Name: "p", Protocol: proto, BaseURL: "https://x.invalid/v1",
		APIKey: "sk-1", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &env{f: f, db: db, mem: mem, lister: lister, svc: svc, provID: v.ID}
}

func TestSyncUpsertsAndMarksUnavailable(t *testing.T) {
	e := newEnv(t, provider.ProtocolOpenAIChat)
	ctx := context.Background()
	e.lister.models = []provider.UpstreamModel{
		{ModelID: "a", DisplayName: "A"},
		{ModelID: "b"},
	}
	res, err := e.svc.Sync(ctx, e.provID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 || res.Total != 2 || res.MarkedUnavailable != 0 {
		t.Fatalf("res = %+v", res)
	}
	var fetched int
	e.db.QueryRow(`SELECT count(*) FROM providers WHERE models_fetched_at IS NOT NULL AND models_fetch_error IS NULL`).Scan(&fetched)
	if fetched != 1 {
		t.Fatal("fetch stamp not updated")
	}

	// Second sync: b disappears → available=0; a stays; nothing deleted.
	e.lister.models = []provider.UpstreamModel{{ModelID: "a", DisplayName: "A+"}}
	res, err = e.svc.Sync(ctx, e.provID)
	if err != nil {
		t.Fatal(err)
	}
	if res.MarkedUnavailable != 1 || res.Added != 0 {
		t.Fatalf("res = %+v", res)
	}
	list, err := e.svc.List(ctx, model.ModelQuery{ProviderID: e.provID})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ModelID != "a" || list[0].DisplayName != "A+" {
		t.Fatalf("list = %+v", list)
	}
	hidden, _ := e.svc.List(ctx, model.ModelQuery{ProviderID: e.provID, IncludeUnavailable: true})
	if len(hidden) != 2 || hidden[0].Available == hidden[1].Available {
		t.Fatalf("unavailable visibility = %+v", hidden)
	}

	// Manual rows survive refreshes and are never marked unavailable.
	if err := e.svc.AddManual(ctx, e.provID, "hand", "Hand"); err != nil {
		t.Fatal(err)
	}
	e.lister.models = nil
	if _, err := e.svc.Sync(ctx, e.provID); err != nil {
		t.Fatal(err)
	}
	list, _ = e.svc.List(ctx, model.ModelQuery{ProviderID: e.provID, IncludeUnavailable: true})
	var sources = map[string]string{}
	for _, m := range list {
		sources[m.ModelID] = string(m.Source)
	}
	if sources["hand"] != string(model.SourceManual) || sources["a"] != string(model.SourceListed) {
		t.Fatalf("sources = %v", sources)
	}
	if len(list) != 3 { // hand + a + b (both kept)
		t.Fatalf("rows after empty sync = %+v", list)
	}
}

func TestSyncFailureKeepsCache(t *testing.T) {
	e := newEnv(t, provider.ProtocolOpenAIChat)
	ctx := context.Background()
	e.lister.models = []provider.UpstreamModel{{ModelID: "a"}}
	if _, err := e.svc.Sync(ctx, e.provID); err != nil {
		t.Fatal(err)
	}
	e.lister.err = errors.New("upstream exploded")
	if _, err := e.svc.Sync(ctx, e.provID); err == nil {
		t.Fatal("expected sync error")
	}
	// Cache and availability untouched; error recorded (§5.5.2, AC-03.2).
	var fetchErr sql.NullString
	e.db.QueryRow(`SELECT models_fetch_error FROM providers WHERE id = ?`, e.provID).Scan(&fetchErr)
	if !fetchErr.Valid || fetchErr.String == "" {
		t.Fatal("models_fetch_error not written")
	}
	list, _ := e.svc.List(ctx, model.ModelQuery{ProviderID: e.provID})
	if len(list) != 1 {
		t.Fatalf("cache lost on failure: %+v", list)
	}
}

func TestSyncGeminiNarrowingStored(t *testing.T) {
	e := newEnv(t, provider.ProtocolGemini)
	e.lister.models = []provider.UpstreamModel{
		{ModelID: "gen", Capabilities: []provider.Capability{provider.CapabilityChat}},
		{ModelID: "emb", Capabilities: []provider.Capability{provider.CapabilityEmbedding}},
		{ModelID: "full"},
	}
	if _, err := e.svc.Sync(context.Background(), e.provID); err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.List(context.Background(), model.ModelQuery{
		ProviderID: e.provID, Capability: provider.CapabilityEmbedding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ModelID != "emb" || list[1].ModelID != "full" {
		t.Fatalf("embedding candidates = %+v", list) // AC-03.4
	}
}

func TestManualAddRemoveAndConflicts(t *testing.T) {
	e := newEnv(t, provider.ProtocolOpenAIChat)
	ctx := context.Background()
	if err := e.svc.AddManual(ctx, e.provID, "  ", "x"); !errors.Is(err, model.ErrEmptyModelID) {
		t.Fatalf("empty id: %v", err)
	}
	if err := e.svc.AddManual(ctx, e.provID, "hand", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AddManual(ctx, e.provID, "hand", "dup"); !errors.Is(err, model.ErrModelExists) {
		t.Fatalf("duplicate: %v", err)
	}
	// Protocol capability floor applies to manual rows too.
	list, _ := e.svc.List(ctx, model.ModelQuery{ProviderID: e.provID, Capability: provider.CapabilityChat})
	if len(list) != 1 {
		t.Fatalf("manual chat candidates = %+v", list)
	}
	// Remove listed → rejected; remove in-use manual → rejected; free manual ok.
	e.lister.models = []provider.UpstreamModel{{ModelID: "listed"}}
	if _, err := e.svc.Sync(ctx, e.provID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RemoveManual(ctx, e.provID, "listed"); !errors.Is(err, model.ErrNotManual) {
		t.Fatalf("remove listed: %v", err)
	}
	rsvc, err := role.New(e.db, e.f.Clock)
	if err != nil {
		t.Fatal(err)
	}
	res, err := rsvc.Assign(ctx, role.RoleChatMain, e.provID, "hand")
	if err != nil {
		t.Fatal(err)
	}
	_ = res
	err = e.svc.RemoveManual(ctx, e.provID, "hand")
	var inUse *model.InUseError
	if !errors.As(err, &inUse) || len(inUse.Roles) != 1 {
		t.Fatalf("remove in-use manual: %v", err)
	}
	if _, err := e.db.Exec(`DELETE FROM role_assignments`); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RemoveManual(ctx, e.provID, "hand"); err != nil {
		t.Fatalf("remove manual: %v", err)
	}
	if err := e.svc.RemoveManual(ctx, e.provID, "hand"); !errors.Is(err, model.ErrModelNotFound) {
		t.Fatalf("remove missing: %v", err)
	}
}

func TestSyncSecretOutage(t *testing.T) {
	e := newEnv(t, provider.ProtocolOpenAIChat)
	ctx := context.Background()
	e.mem.Unavailable = true
	if _, err := e.svc.Sync(ctx, e.provID); err == nil {
		t.Fatal("expected error")
	}
	if e.lister.calls != 0 {
		t.Fatal("lister called despite secret outage")
	}
	var fetchErr string
	e.db.QueryRow(`SELECT COALESCE(models_fetch_error,'') FROM providers WHERE id = ?`, e.provID).Scan(&fetchErr)
	if fetchErr == "" {
		t.Fatal("secret outage not recorded")
	}
}
