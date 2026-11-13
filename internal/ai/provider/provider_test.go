package provider_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"researchos/internal/ai/aitest"
	"researchos/internal/ai/provider"
	"researchos/internal/ai/role"
	"researchos/internal/ai/secret"
	"researchos/internal/foundation/foundationtest"
)

type fakeLister struct {
	models  []provider.UpstreamModel
	err     error
	calls   int
	lastCfg provider.ProviderConfig
	lastKey provider.Secret
}

func (f *fakeLister) ListModels(_ context.Context, cfg provider.ProviderConfig, key provider.Secret) ([]provider.UpstreamModel, error) {
	f.calls++
	f.lastCfg = cfg
	f.lastKey = key
	if f.err != nil {
		return nil, f.err
	}
	return f.models, nil
}

func newService(t *testing.T, f *foundationtest.Fixture, secrets secret.Store, lister provider.ModelLister) provider.Service {
	t.Helper()
	svc, err := provider.New(f.Global().DB(), provider.Options{
		Clock:    f.Clock,
		Secrets:  secrets,
		Lister:   lister,
		RoleCaps: role.RequiredCapability,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func seedRoleAssignment(t *testing.T, db *sql.DB, roleID, providerID, modelID string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO role_assignments (role, provider_id, model_id, updated_at)
		VALUES (?, ?, ?, '2026-01-02T03:04:05.000Z')`, roleID, providerID, modelID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCreateValidations(t *testing.T) {
	f := aitest.Fixture(t)
	mem := secret.NewMemory()
	svc := newService(t, f, mem, &fakeLister{})
	ctx := context.Background()

	if _, err := svc.Create(ctx, provider.CreateProviderInput{Name: "", Protocol: provider.ProtocolGemini, BaseURL: "https://x.invalid"}); !errors.Is(err, provider.ErrInvalidName) {
		t.Fatalf("empty name: %v", err)
	}
	if _, err := svc.Create(ctx, provider.CreateProviderInput{Name: "n", Protocol: "mystery", BaseURL: "https://x.invalid"}); !errors.Is(err, provider.ErrInvalidProtocol) {
		t.Fatalf("bad protocol: %v", err)
	}
	if _, err := svc.Create(ctx, provider.CreateProviderInput{Name: "n", Protocol: provider.ProtocolOpenAIChat, BaseURL: ""}); !errors.Is(err, provider.ErrInvalidBaseURL) {
		t.Fatalf("empty url: %v", err)
	}
	if _, err := svc.Create(ctx, provider.CreateProviderInput{Name: "n", Protocol: provider.ProtocolOpenAIChat, BaseURL: "https://user:pw@x.invalid/v1"}); !errors.Is(err, provider.ErrInvalidBaseURL) {
		t.Fatalf("userinfo url: %v", err)
	}
	if _, err := svc.Create(ctx, provider.CreateProviderInput{Name: "n", Protocol: provider.ProtocolOpenAIChat, BaseURL: "ftp://x.invalid"}); !errors.Is(err, provider.ErrInvalidBaseURL) {
		t.Fatalf("ftp scheme: %v", err)
	}

	// Trailing slash trimmed; localhost allowed; empty key = keyless.
	v, err := svc.Create(ctx, provider.CreateProviderInput{
		Name: "local", Protocol: provider.ProtocolOpenAIChat,
		BaseURL: "http://127.0.0.1:1234/v1/", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.BaseURL != "http://127.0.0.1:1234/v1" {
		t.Fatalf("base url = %q", v.BaseURL)
	}
	if v.HasKey {
		t.Fatal("keyless provider must report HasKey=false")
	}

	// Keyed provider: key lands in the store, never in the database.
	v, err = svc.Create(ctx, provider.CreateProviderInput{
		Name: "cloud", Protocol: provider.ProtocolAnthropic,
		BaseURL: "https://api.anthropic.com/v1", APIKey: "sk-supersecret-1", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !v.HasKey || strings.Contains(v.ID, "sk-supersecret") {
		t.Fatalf("view: %+v", v)
	}
	if !mem.Has(secret.ProviderKeyRef(v.ID)) {
		t.Fatal("key not stored")
	}
	var raw string
	err = f.Global().DB().QueryRow(`SELECT base_url || ' ' || api_key_ref || ' ' || name FROM providers WHERE id = ?`, v.ID).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "sk-supersecret") {
		t.Fatal("secret leaked into providers row")
	}
}

func TestCreateSecretFailureAborts(t *testing.T) {
	f := aitest.Fixture(t)
	mem := secret.NewMemory()
	mem.Unavailable = true
	svc := newService(t, f, mem, &fakeLister{})

	_, err := svc.Create(context.Background(), provider.CreateProviderInput{
		Name: "p", Protocol: provider.ProtocolOpenAIChat,
		BaseURL: "https://x.invalid/v1", APIKey: "sk-1",
	})
	if !errors.Is(err, secret.ErrSecretStoreUnavailable) {
		t.Fatalf("create with unavailable store: %v", err)
	}
	var n int
	f.Global().DB().QueryRow(`SELECT count(*) FROM providers`).Scan(&n)
	if n != 0 {
		t.Fatalf("provider row created despite secret failure")
	}
}

func TestCreateInsertFailureCompensatesSecret(t *testing.T) {
	f := aitest.Fixture(t)
	db := f.Global().DB()
	mem := secret.NewMemory()
	// Force the INSERT to fail: a duplicate primary key after id generation
	// is impossible to stage without touching ids, so we drop the table's
	// uniqueness target instead: create a provider then break NOT NULL via
	// a protocol typo is validation-caught. Simulate by making api_key_ref
	// column invalid after secret write: temporarily replace providers with
	// a view-less corrupt schema is heavy; assert the compensation path
	// directly through a failing insert on a pre-seeded id conflict.
	svc := newService(t, f, mem, &fakeLister{})
	v, err := svc.Create(context.Background(), provider.CreateProviderInput{
		Name: "ok", Protocol: provider.ProtocolOpenAIChat,
		BaseURL: "https://x.invalid/v1", APIKey: "sk-abc", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate post-secret insert failure: make a second Create fail by
	// removing the providers table (DML will error after the secret write).
	if _, err := db.Exec(`DROP TABLE providers`); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create(context.Background(), provider.CreateProviderInput{
		Name: "fail", Protocol: provider.ProtocolOpenAIChat,
		BaseURL: "https://y.invalid/v1", APIKey: "sk-xyz", Enabled: true,
	})
	if err == nil {
		t.Fatal("expected insert failure")
	}
	// Only the first provider's key may remain: the compensation deleted
	// the freshly written one.
	if mem.Len() != 1 {
		t.Fatalf("orphan secret after failed insert: entries=%d", mem.Len())
	}
	_ = v
}

func TestUpdateProtocolConflictAndCacheReset(t *testing.T) {
	f := aitest.Fixture(t)
	db := f.Global().DB()
	mem := secret.NewMemory()
	svc := newService(t, f, mem, &fakeLister{})
	ctx := context.Background()

	v, err := svc.Create(ctx, provider.CreateProviderInput{
		Name: "oa", Protocol: provider.ProtocolOpenAIChat,
		BaseURL: "https://api.openai.com/v1", APIKey: "sk-1", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Cache one listed model and one manual model.
	if _, err := db.Exec(`INSERT INTO provider_models (provider_id, model_id, display_name, capabilities, source, available, first_seen_at, last_seen_at)
		VALUES (?, 'gpt-x', 'GPT X', '["chat","embedding"]', 'listed', 1, '2026-01-01T00:00:00.000Z', '2026-01-02T00:00:00.000Z'),
		 (?, 'hand-1', 'hand-1', '["chat","embedding"]', 'manual', 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		v.ID, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE providers SET models_fetched_at = '2026-01-02T00:00:00.000Z', models_fetch_error = NULL,
		last_tested_at = '2026-01-02T00:00:00.000Z', last_test_ok = 1 WHERE id = ?`, v.ID); err != nil {
		t.Fatal(err)
	}
	seedRoleAssignment(t, db, "embedding", v.ID, "gpt-x")

	// openai_chat → anthropic must fail while the embedding role points here.
	newProto := provider.ProtocolAnthropic
	_, err = svc.Update(ctx, v.ID, provider.UpdateProviderInput{Protocol: &newProto})
	var conflict *provider.ProtocolConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("protocol change with embedding role: %v", err)
	}
	if len(conflict.Conflicts) != 1 || conflict.Conflicts[0].Role != "embedding" {
		t.Fatalf("conflicts = %+v", conflict.Conflicts)
	}

	// With no conflicting roles, the change succeeds and resets caches.
	if _, err := db.Exec(`DELETE FROM role_assignments`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM provider_models WHERE source = 'listed'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO provider_models (provider_id, model_id, display_name, capabilities, source, available, first_seen_at, last_seen_at)
		VALUES (?, 'gpt-x', 'GPT X', '["chat","embedding"]', 'listed', 1, '2026-01-01T00:00:00.000Z', '2026-01-02T00:00:00.000Z')`,
		v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Update(ctx, v.ID, provider.UpdateProviderInput{Protocol: &newProto}); err != nil {
		t.Fatal(err)
	}
	var (
		listed, manual int
		fetchedOK      bool
		manualCaps     string
	)
	if err := db.QueryRow(`SELECT count(*) FROM provider_models WHERE provider_id = ? AND source = 'listed'`, v.ID).Scan(&listed); err != nil {
		t.Fatal(err)
	}
	if listed != 0 {
		t.Fatalf("listed cache not cleared: %d", listed)
	}
	if err := db.QueryRow(`SELECT count(*), capabilities FROM provider_models WHERE provider_id = ? AND source = 'manual'`, v.ID).Scan(&manual, &manualCaps); err != nil {
		t.Fatal(err)
	}
	if manual != 1 {
		t.Fatalf("manual model must survive: %d", manual)
	}
	var caps []string
	if err := json.Unmarshal([]byte(manualCaps), &caps); err != nil {
		t.Fatal(err)
	}
	if len(caps) != 1 || caps[0] != "chat" {
		t.Fatalf("manual caps not recomputed for anthropic: %v", caps)
	}
	if err := db.QueryRow(`SELECT models_fetched_at IS NULL AND last_tested_at IS NULL FROM providers WHERE id = ?`, v.ID).Scan(&fetchedOK); err != nil {
		t.Fatal(err)
	}
	if !fetchedOK {
		t.Fatal("fetch/test stamps not reset")
	}
}

func TestUpdateKeySemantics(t *testing.T) {
	f := aitest.Fixture(t)
	mem := secret.NewMemory()
	svc := newService(t, f, mem, &fakeLister{})
	ctx := context.Background()

	v, err := svc.Create(ctx, provider.CreateProviderInput{
		Name: "p", Protocol: provider.ProtocolOpenAIChat,
		BaseURL: "https://x.invalid/v1", APIKey: "sk-1", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := "secret"
	if _, err := svc.Update(ctx, v.ID, provider.UpdateProviderInput{APIKey: &key, ClearKey: true}); !errors.Is(err, provider.ErrKeyConflict) {
		t.Fatalf("APIKey+ClearKey: %v", err)
	}
	newKey := "sk-2"
	if _, err := svc.Update(ctx, v.ID, provider.UpdateProviderInput{APIKey: &newKey}); err != nil {
		t.Fatal(err)
	}
	got, err := mem.Get(ctx, secret.ProviderKeyRef(v.ID))
	if err != nil || string(got) != "sk-2" {
		t.Fatalf("replacement: %q %v", got, err)
	}
	// ref stays identical: DB untouched by a pure key replacement.
	view, _ := svc.Get(ctx, v.ID)
	if !view.HasKey {
		t.Fatal("HasKey lost")
	}
	if _, err := svc.Update(ctx, v.ID, provider.UpdateProviderInput{ClearKey: true}); err != nil {
		t.Fatal(err)
	}
	view, _ = svc.Get(ctx, v.ID)
	if view.HasKey {
		t.Fatal("key not cleared")
	}
	if mem.Has(secret.ProviderKeyRef(v.ID)) {
		t.Fatal("secret entry left behind")
	}
	// Empty-string APIKey also clears (§5.3.2).
	re := "sk-3"
	if _, err := svc.Update(ctx, v.ID, provider.UpdateProviderInput{APIKey: &re}); err != nil {
		t.Fatal(err)
	}
	empty := ""
	if _, err := svc.Update(ctx, v.ID, provider.UpdateProviderInput{APIKey: &empty}); err != nil {
		t.Fatal(err)
	}
	if view, _ = svc.Get(ctx, v.ID); view.HasKey {
		t.Fatal("empty APIKey must clear")
	}
}

func TestDeleteGuardsAndOrder(t *testing.T) {
	f := aitest.Fixture(t)
	db := f.Global().DB()
	mem := secret.NewMemory()
	svc := newService(t, f, mem, &fakeLister{})
	ctx := context.Background()

	v, err := svc.Create(ctx, provider.CreateProviderInput{
		Name: "p", Protocol: provider.ProtocolOpenAIChat,
		BaseURL: "https://x.invalid/v1", APIKey: "sk-1", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO provider_models (provider_id, model_id, display_name, capabilities, source, available, first_seen_at, last_seen_at)
		VALUES (?, 'm', 'm', '["chat"]', 'manual', 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, v.ID); err != nil {
		t.Fatal(err)
	}
	seedRoleAssignment(t, db, "chat_main", v.ID, "m")

	err = svc.Delete(ctx, v.ID)
	var inUse *provider.InUseError
	if !errors.As(err, &inUse) || len(inUse.Roles) != 1 || inUse.Roles[0] != "chat_main" {
		t.Fatalf("delete in use: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM role_assignments`); err != nil {
		t.Fatal(err)
	}
	// Secret store outage aborts the delete and keeps the row (§4.5).
	mem.Unavailable = true
	if err := svc.Delete(ctx, v.ID); !errors.Is(err, secret.ErrSecretStoreUnavailable) {
		t.Fatalf("delete with store outage: %v", err)
	}
	mem.Unavailable = false
	var n int
	db.QueryRow(`SELECT count(*) FROM providers`).Scan(&n)
	if n != 1 {
		t.Fatalf("provider row removed too early")
	}

	if err := svc.Delete(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT count(*) FROM providers WHERE id = ?`, v.ID).Scan(&n)
	if n != 0 {
		t.Fatal("provider row left")
	}
	db.QueryRow(`SELECT count(*) FROM provider_models WHERE provider_id = ?`, v.ID).Scan(&n)
	if n != 0 {
		t.Fatal("model cache left")
	}
	if mem.Has(secret.ProviderKeyRef(v.ID)) {
		t.Fatal("secret left behind")
	}
}

func TestSetEnabledSecretCheck(t *testing.T) {
	f := aitest.Fixture(t)
	mem := secret.NewMemory()
	svc := newService(t, f, mem, &fakeLister{})
	ctx := context.Background()

	v, err := svc.Create(ctx, provider.CreateProviderInput{
		Name: "keyed", Protocol: provider.ProtocolOpenAIChat,
		BaseURL: "https://x.invalid/v1", APIKey: "sk-1", Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetEnabled(ctx, v.ID, true); err != nil {
		t.Fatalf("enable with readable key: %v", err)
	}
	if err := svc.SetEnabled(ctx, v.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := mem.Delete(ctx, secret.ProviderKeyRef(v.ID)); err != nil {
		t.Fatal(err)
	}
	err = svc.SetEnabled(ctx, v.ID, true)
	if !errors.Is(err, provider.ErrSecretStoreGone) {
		t.Fatalf("enable with lost key: %v", err)
	}
	// Keyless local services bypass the check (§4.5).
	k, err := svc.Create(ctx, provider.CreateProviderInput{
		Name: "keyless", Protocol: provider.ProtocolOpenAIChat,
		BaseURL: "http://localhost:11434/v1", Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	mem.Unavailable = true
	if err := svc.SetEnabled(ctx, k.ID, true); err != nil {
		t.Fatalf("keyless enable during outage: %v", err)
	}
}

func TestTestConnectionPersistsWithoutCache(t *testing.T) {
	f := aitest.Fixture(t)
	db := f.Global().DB()
	mem := secret.NewMemory()
	lister := &fakeLister{models: []provider.UpstreamModel{{ModelID: "gpt-a"}, {ModelID: "gpt-b"}}}
	svc := newService(t, f, mem, lister)
	ctx := context.Background()

	v, err := svc.Create(ctx, provider.CreateProviderInput{
		Name: "p", Protocol: provider.ProtocolOpenAIChat,
		BaseURL: "https://x.invalid/v1", APIKey: "sk-top", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Test(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.ModelCount != 2 || res.LatencyMS < 0 {
		t.Fatalf("result = %+v", res)
	}
	if string(lister.lastKey) != "sk-top" {
		t.Fatalf("lister key = %q", lister.lastKey)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM provider_models`).Scan(&n)
	if n != 0 {
		t.Fatal("Test must not write the model cache (§5.3.5)")
	}
	view, _ := svc.Get(ctx, v.ID)
	if view.LastTest == nil || !view.LastTest.OK || view.LastTest.ModelCount != 2 {
		t.Fatalf("persisted last test = %+v", view.LastTest)
	}

	// Failing test: kind + sanitized message persisted, no panic path.
	lister.err = errors.New("boom sk-top leaked")
	res, err = svc.Test(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("expected failure")
	}
	if strings.Contains(res.Message, "sk-top") {
		t.Fatalf("unsanitized message: %q", res.Message)
	}
	view, _ = svc.Get(ctx, v.ID)
	if view.LastTest == nil || view.LastTest.OK {
		t.Fatal("failed test not persisted")
	}
	if view.LastTest.Message != "" && strings.Contains(view.LastTest.Message, "sk-top") {
		t.Fatalf("secret in persisted test error: %q", view.LastTest.Message)
	}

	// Secret unreadable → secret_unavailable and no lister call.
	lister.calls = 0
	if err := mem.Delete(ctx, secret.ProviderKeyRef(v.ID)); err != nil {
		t.Fatal(err)
	}
	res, err = svc.Test(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.ErrorKind != string("secret_unavailable") || lister.calls != 0 {
		t.Fatalf("secret outage test = %+v calls=%d", res, lister.calls)
	}
}

func TestListAndGet(t *testing.T) {
	f := aitest.Fixture(t)
	mem := secret.NewMemory()
	svc := newService(t, f, mem, &fakeLister{})
	ctx := context.Background()
	var ids []string
	for _, name := range []string{"a", "b"} {
		v, err := svc.Create(ctx, provider.CreateProviderInput{
			Name: name, Protocol: provider.ProtocolGemini,
			BaseURL: "https://generativelanguage.googleapis.com/v1beta",
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, v.ID)
	}
	list, err := svc.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d %v", len(list), err)
	}
	if _, err := svc.Get(ctx, "00000000-0000-7000-8000-000000000000"); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}
	_ = ids
}
