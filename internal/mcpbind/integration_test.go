package mcpbind_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	aimigrations "researchos/internal/ai/migrations"
	"researchos/internal/export"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/settings"
	"researchos/internal/foundation/storage"
	fworkspace "researchos/internal/foundation/workspace"
	ideasmigrations "researchos/internal/ideas/migrations"
	kanbanmigrations "researchos/internal/kanban/migrations"
	literaturemigrations "researchos/internal/literature/migrations"
	"researchos/internal/mcpbind"
	"researchos/internal/mcpfacade"
	mcpstoremigrations "researchos/internal/mcpstore/migrations"
	notesmigrations "researchos/internal/notes/migrations"
	"researchos/internal/overview"
	"researchos/internal/project"
	"researchos/internal/search"
	searchmigrations "researchos/internal/search/migrations"

	"researchos/internal/mcp"
)

type harness struct {
	t       *testing.T
	ctx     context.Context
	ws      *fworkspace.Workspace
	app     *mcpbind.App
	project *project.Service
	projID  string
	store   *settings.Store
	confirm confirm.Service
	search  *search.Service
}

func allMigrations(t *testing.T) map[storage.Kind][]storage.Migration {
	t.Helper()
	merged := map[storage.Kind][]storage.Migration{}
	add := func(m map[storage.Kind][]storage.Migration) {
		for kind, list := range m {
			merged[kind] = append(merged[kind], list...)
		}
	}
	add(notesmigrations.MustAll())
	add(ideasmigrations.MustAll())
	add(kanbanmigrations.MustAll())
	add(literaturemigrations.MustAll())
	add(aimigrations.MustAll())
	add(searchmigrations.MustAll())
	add(mcpstoremigrations.MustAll())
	return merged
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, search.Options{}, true)
}

// newHarnessWith builds the harness with an explicit SPEC-10 option set.
// withSearch=false leaves the search service unassembled so the module
// readiness trimming can be exercised (§2.2).
func newHarnessWith(t *testing.T, searchOpt search.Options, withSearch bool) *harness {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	confirmSvc := confirm.New(confirm.Options{})
	ws, err := fworkspace.Init(ctx, root, fworkspace.Options{
		SkipBackup: true,
		Migrations: allMigrations(t),
		Confirm:    confirmSvc,
	})
	if err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	projectSvc := project.New(project.Options{Workspace: ws})
	proj, err := projectSvc.Create(ctx, project.CreateInput{Name: "Alpha"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	var searchSvc *search.Service
	if withSearch {
		searchSvc, err = search.New(ws, searchOpt)
		if err != nil {
			t.Fatalf("search service: %v", err)
		}
		t.Cleanup(func() { _ = searchSvc.Close() })
	}
	facade := mcpfacade.New(mcpfacade.Options{
		Project:  projectSvc,
		Overview: overview.New(overview.Options{Workspace: ws}),
		Export:   export.New(export.Options{Workspace: ws}),
	})
	store := settings.NewWorkspace(ws.Global().DB()).WithClock(clock.System{})
	app, err := mcpbind.Wire(mcpbind.WireOptions{
		Workspace:    ws,
		Version:      "test",
		Confirm:      confirmSvc,
		Settings:     store,
		Facade:       facade,
		ProjectAdmin: projectSvc,
		Search:       searchSvc,
		Limiter:      mcp.NewLimiter(1_000_000, 1_000_000, nil),
	})
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	return &harness{
		t:       t,
		ctx:     ctx,
		ws:      ws,
		app:     app,
		project: projectSvc,
		projID:  proj.ID,
		store:   store,
		confirm: confirmSvc,
		search:  searchSvc,
	}
}

func (h *harness) setSetting(key, value string) {
	h.t.Helper()
	err := h.ws.Global().InTx(h.ctx, func(tx *sql.Tx) error {
		return h.store.Set(h.ctx, tx, key, value)
	})
	if err != nil {
		h.t.Fatalf("set setting %s: %v", key, err)
	}
}

// waitJob polls a job until it reaches a terminal state.
func (h *harness) waitJob(id string) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		res := h.callOK("job_get", map[string]any{"job_id": id})
		switch res["state"] {
		case "succeeded", "failed", "cancelled":
			return res
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("job %s did not finish: %v", id, res)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) principal() mcp.Principal {
	return mcp.Principal{ClientID: "client-test", ClientName: "test", Transport: mcp.TransportStdio, ConnectionID: "conn-test"}
}

func (h *harness) call(name string, args map[string]any) mcp.CallResult {
	h.t.Helper()
	res, err := h.app.Host.Call(h.ctx, h.principal(), mcp.CallRequest{Name: name, Arguments: mustJSON(args)})
	if err != nil {
		h.t.Fatalf("%s: host error: %v", name, err)
	}
	return res
}

func (h *harness) callOK(name string, args map[string]any) map[string]any {
	h.t.Helper()
	res := h.call(name, args)
	if res.IsError {
		h.t.Fatalf("%s: unexpected error: %v", name, res.Structured)
	}
	return structuredMap(h.t, res.Structured)
}

func (h *harness) callErr(name string, args map[string]any, want mcp.Code) *mcp.Error {
	h.t.Helper()
	res := h.call(name, args)
	if !res.IsError {
		h.t.Fatalf("%s: expected error %s, got success %v", name, want, res.Structured)
	}
	body, _ := res.Structured.(map[string]any)
	inner, _ := body["error"].(errorBody)
	_ = inner
	raw, _ := json.Marshal(body["error"])
	var parsed struct {
		Code    mcp.Code       `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		h.t.Fatalf("%s: decode error body: %v", name, err)
	}
	if parsed.Code != want {
		h.t.Fatalf("%s: error code = %s, want %s (message %q)", name, parsed.Code, want, parsed.Message)
	}
	return &mcp.Error{Code: parsed.Code, Message: parsed.Message, Details: parsed.Details}
}

type errorBody struct{}

func structuredMap(t *testing.T, v any) map[string]any {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode structured: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode structured: %v", err)
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	body, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return body
}
