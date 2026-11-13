package mcpbind_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"researchos/internal/ai/gateway"
	"researchos/internal/mcp"
	"researchos/internal/search"
)

// failingRerank models the v1 gateway: a configured rerank role that cannot
// serve the request (SPEC-10 ruling 4).
type failingRerank struct{}

func (failingRerank) Rerank(context.Context, string, []string, int) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, errors.New("rerank capability unavailable")
}

// indexNote creates one scratchpad note through the MCP surface and indexes it
// synchronously through the search service.
func (h *harness) indexNote(title, body string) string {
	h.t.Helper()
	created := h.callOK("note_create", map[string]any{
		"idempotency_key": "search-note-" + title,
		"project_id":      h.projID,
		"title":           title,
		"kind":            "scratchpad",
		"body_md":         body,
	})
	noteID := created["note"].(map[string]any)["note_id"].(string)
	h.indexEntity(search.EntityNote, noteID)
	return noteID
}

func (h *harness) indexEntity(entityType search.EntityType, entityID string) {
	h.t.Helper()
	if err := h.search.Notify(h.ctx, search.Change{
		ProjectID:  h.projID,
		EntityType: entityType,
		EntityID:   entityID,
		Kind:       search.ChangeUpsert,
	}); err != nil {
		h.t.Fatalf("notify index change: %v", err)
	}
	if err := h.search.Flush(h.ctx, search.Scope{Kind: search.ScopeProject, ProjectID: h.projID}); err != nil {
		h.t.Fatalf("flush index: %v", err)
	}
}

func (h *harness) activityCount() int {
	h.t.Helper()
	handle, err := h.ws.Project(h.ctx, h.projID)
	if err != nil {
		h.t.Fatalf("open project: %v", err)
	}
	var n int
	if err := handle.DB().QueryRowContext(h.ctx, `SELECT count(*) FROM activities`).Scan(&n); err != nil {
		h.t.Fatalf("count activities: %v", err)
	}
	return n
}

// TestSearchToolMatchesService covers SPEC-10 A20: the MCP tool and a direct
// service call return the same result set, and retrieval writes no Activity.
func TestSearchToolMatchesService(t *testing.T) {
	h := newHarness(t)
	h.indexNote("量子点合成记录", "2026 年完成量子点合成第一批实验，产率 78%。")

	before := h.activityCount()
	res := h.callOK("search", map[string]any{
		"query":      "量子点",
		"scope":      "project",
		"project_id": h.projID,
		"rerank":     false,
	})
	after := h.activityCount()
	if before != after {
		t.Fatalf("search wrote activities: %d -> %d", before, after)
	}

	hits := res["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("hits = %v", hits)
	}
	hit := hits[0].(map[string]any)
	if hit["entity_type"] != "note" || hit["title"] != "量子点合成记录" {
		t.Fatalf("hit = %v", hit)
	}
	if !strings.Contains(hit["snippet"].(string), "量子点") {
		t.Fatalf("snippet = %v", hit["snippet"])
	}
	if hit["score"].(float64) <= 0 {
		t.Fatalf("score = %v", hit["score"])
	}
	if hit["project_id"] != h.projID {
		t.Fatalf("project_id = %v", hit["project_id"])
	}
	if _, ok := res["took_ms"]; !ok {
		t.Fatalf("took_ms missing: %v", res)
	}
	if res["warning"] != nil {
		t.Fatalf("unexpected warning with rerank disabled: %v", res["warning"])
	}

	direct, err := h.search.Search(h.ctx, search.SearchRequest{
		Query:  "量子点",
		Scope:  search.Scope{Kind: search.ScopeProject, ProjectID: h.projID},
		Limit:  20,
		Rerank: search.RerankOff,
	})
	if err != nil {
		t.Fatalf("direct search: %v", err)
	}
	if len(direct.Results) != len(hits) {
		t.Fatalf("result count differs: tool %d, service %d", len(hits), len(direct.Results))
	}
	for i, result := range direct.Results {
		toolHit := hits[i].(map[string]any)
		if toolHit["entity_id"] != result.EntityID {
			t.Fatalf("hit %d id: tool %v, service %v", i, toolHit["entity_id"], result.EntityID)
		}
		if toolHit["score"].(float64) != result.Score.Fusion {
			t.Fatalf("hit %d score: tool %v, service %v", i, toolHit["score"], result.Score.Fusion)
		}
	}

	// kinds maps to Filters.EntityTypes.
	empty := h.callOK("search", map[string]any{
		"query":      "量子点",
		"scope":      "project",
		"project_id": h.projID,
		"kinds":      []string{"idea"},
		"rerank":     false,
	})
	if len(empty["hits"].([]any)) != 0 {
		t.Fatalf("kinds=idea returned %v", empty["hits"])
	}
}

// TestSearchIndexStatusAndReindex covers the index maintenance tools.
func TestSearchIndexStatusAndReindex(t *testing.T) {
	h := newHarness(t)
	h.indexNote("索引维护", "重建索引之前的笔记正文。")

	status := h.callOK("search_index_status", map[string]any{
		"scope":      "project",
		"project_id": h.projID,
	})
	if status["scope"].(map[string]any)["kind"] != "project" {
		t.Fatalf("scope = %v", status["scope"])
	}
	if status["docs"].(float64) < 1 {
		t.Fatalf("docs = %v", status["docs"])
	}
	if _, ok := status["schema_version"]; !ok {
		t.Fatalf("schema_version missing: %v", status)
	}

	before := h.activityCount()
	job := h.callOK("search_reindex", map[string]any{
		"scope":      "project",
		"project_id": h.projID,
	})
	if job["status"] != "job" || job["job_id"] == "" {
		t.Fatalf("reindex result = %v", job)
	}
	done := h.waitJob(job["job_id"].(string))
	if done["state"] != "succeeded" {
		t.Fatalf("reindex job = %v", done)
	}
	if done["read_only"] != true {
		t.Fatalf("reindex job is not read-only: %v", done)
	}
	result := done["result"].(map[string]any)
	if result["job_id"] == "" || result["job_id"] == job["job_id"] {
		t.Fatalf("index job id = %v (mcp job %v)", result["job_id"], job["job_id"])
	}
	if result["scope"].(map[string]any)["project_id"] != h.projID {
		t.Fatalf("index scope = %v", result["scope"])
	}
	if after := h.activityCount(); after != before {
		t.Fatalf("reindex wrote activities: %d -> %d", before, after)
	}

	// The background index job carries no Activity either. Indexing is
	// asynchronous, so wait for it to settle before cleanup.
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := h.search.Status(h.ctx, search.Scope{Kind: search.ScopeProject, ProjectID: h.projID})
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if !st.Indexing && st.Docs > 0 {
			if st.Failed != 0 {
				t.Fatalf("index job failed: %+v", st)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("index rebuild did not settle: %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if after := h.activityCount(); after != before {
		t.Fatalf("index rebuild wrote activities: %d -> %d", before, after)
	}
}

// TestSearchRerankDegrades covers the rerank warning: a configured but
// unusable rerank keeps the fusion order and attaches warning, never an error.
func TestSearchRerankDegrades(t *testing.T) {
	h := newHarnessWith(t, search.Options{Rerank: failingRerank{}}, true)
	h.indexNote("rerank 降级", "量子点 rerank 降级测试正文。")

	res := h.callOK("search", map[string]any{
		"query":      "量子点",
		"scope":      "project",
		"project_id": h.projID,
	})
	warning, ok := res["warning"].(map[string]any)
	if !ok || warning["code"] != "RERANK_UNAVAILABLE" {
		t.Fatalf("warning = %v", res["warning"])
	}
	if len(res["hits"].([]any)) != 1 {
		t.Fatalf("hits = %v", res["hits"])
	}
	degraded := res["degraded"].([]any)
	found := false
	for _, reason := range degraded {
		if reason == "rerank_unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("degraded = %v", degraded)
	}

	off := h.callOK("search", map[string]any{
		"query":      "量子点",
		"scope":      "project",
		"project_id": h.projID,
		"rerank":     false,
	})
	if off["warning"] != nil {
		t.Fatalf("rerank=false still warns: %v", off["warning"])
	}
}

// TestSearchReadOnlyMode covers §5.4 rule 1: the read tools and the read-only
// reindex job stay visible; other writes do not.
func TestSearchReadOnlyMode(t *testing.T) {
	h := newHarness(t)
	p := h.principal()
	p.AccessMode = mcp.AccessReadOnly

	tools, err := h.app.Host.Tools(h.ctx, p)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	seen := map[string]bool{}
	for _, spec := range tools {
		seen[spec.Name] = true
	}
	for _, name := range []string{"search", "search_index_status", "search_reindex"} {
		if !seen[name] {
			t.Fatalf("read-only mode hides %s", name)
		}
	}
	if seen["note_create"] {
		t.Fatal("read-only mode exposes a write tool")
	}

	res, err := h.app.Host.Call(h.ctx, p, mcp.CallRequest{
		Name:      "search_reindex",
		Arguments: mustJSON(map[string]any{"scope": "project", "project_id": h.projID}),
	})
	if err != nil {
		t.Fatalf("reindex under read-only: %v", err)
	}
	if res.IsError {
		t.Fatalf("reindex under read-only: %v", res.Structured)
	}
	body := structuredMap(t, res.Structured)
	if body["job_id"] == "" {
		t.Fatalf("reindex result = %v", body)
	}
}

// TestSearchScopeValidation covers §5.7: a malformed scope is INVALID_ARGUMENT.
func TestSearchScopeValidation(t *testing.T) {
	h := newHarness(t)
	h.callErr("search", map[string]any{
		"query": "x", "scope": "project",
	}, mcp.CodeInvalidArgument)
	h.callErr("search", map[string]any{
		"query": "x", "scope": "global", "project_id": h.projID,
	}, mcp.CodeInvalidArgument)
	h.callErr("search_index_status", map[string]any{
		"scope": "global", "project_id": h.projID,
	}, mcp.CodeInvalidArgument)
	h.callErr("search_reindex", map[string]any{
		"scope": "project",
	}, mcp.CodeInvalidArgument)
}

// TestSearchModuleNotReady covers §2.2: without the service the whole module is
// trimmed from tools/list and calls report NOT_FOUND.
func TestSearchModuleNotReady(t *testing.T) {
	h := newHarnessWith(t, search.Options{}, false)
	tools, err := h.app.Host.Tools(h.ctx, h.principal())
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	for _, spec := range tools {
		if spec.Module == mcp.ModuleSearch {
			t.Fatalf("search tool %s published without a service", spec.Name)
		}
	}
	h.callErr("search", map[string]any{"query": "x", "scope": "global"}, mcp.CodeNotFound)
}
