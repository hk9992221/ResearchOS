package mcpbind_test

import (
	"os"
	"path/filepath"
	"testing"

	"researchos/internal/mcp"
)

// TestReadToolsWriteNoActivity covers §8.6 item 25.
func TestReadToolsWriteNoActivity(t *testing.T) {
	h := newHarness(t)
	before := len(h.callOK("activity_list", map[string]any{"project_id": h.projID})["items"].([]any))
	h.callOK("note_list", map[string]any{"project_id": h.projID})
	h.callOK("project_get", map[string]any{"project_id": h.projID})
	h.callOK("column_list", map[string]any{"project_id": h.projID})
	after := len(h.callOK("activity_list", map[string]any{"project_id": h.projID})["items"].([]any))
	if before != after {
		t.Fatalf("read tools added activities: %d -> %d", before, after)
	}
}

// TestWriteActorIsMCP covers §7.3 for a direct write.
func TestWriteActorIsMCP(t *testing.T) {
	h := newHarness(t)
	h.callOK("note_create", map[string]any{
		"idempotency_key": "actor-note",
		"project_id":      h.projID,
		"title":           "审计",
		"kind":            "scratchpad",
	})
	activities := h.callOK("activity_list", map[string]any{
		"project_id":  h.projID,
		"event_types": []string{"entity.created"},
		"entity_type": "note",
	})
	items := activities["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("entity.created rows = %d", len(items))
	}
	if items[0].(map[string]any)["actor"] != "mcp" {
		t.Fatalf("actor = %v", items[0].(map[string]any)["actor"])
	}
}

// TestPathGuards covers §8.8 item 33.
func TestPathGuards(t *testing.T) {
	h := newHarness(t)
	projectDir := filepath.Join(h.ws.Root(), "projects", h.projID)

	job := h.callOK("project_export", map[string]any{
		"idempotency_key": "export-inside",
		"project_id":      h.projID,
		"format":          "zip",
		"dest_dir":        projectDir,
	})
	failed := h.waitJob(job["job_id"].(string))
	if failed["state"] != "failed" {
		t.Fatalf("export into project dir = %v", failed)
	}

	h.callErr("experiment_create", map[string]any{
		"idempotency_key": "exp-inside",
		"project_id":      h.projID,
		"title":           "非法目录",
		"dir_ref":         projectDir,
	}, mcp.CodeInvalidArgument)

	// A relative directory is rejected before the service is reached.
	h.callErr("experiment_create", map[string]any{
		"idempotency_key": "exp-relative",
		"project_id":      h.projID,
		"title":           "相对目录",
		"dir_ref":         "relative/dir",
	}, mcp.CodeInvalidArgument)
}

// TestPermissionNonEscalation covers §8.8 item 34.
func TestPermissionNonEscalation(t *testing.T) {
	h := newHarness(t)
	note := h.callOK("note_create", map[string]any{
		"idempotency_key": "guard-note",
		"project_id":      h.projID,
		"title":           "笔记",
		"kind":            "scratchpad",
	})
	noteID := note["note"].(map[string]any)["note_id"].(string)
	got := h.callOK("note_get", map[string]any{"project_id": h.projID, "note_id": noteID})

	// Cannot change ownership fields through save_body.
	h.callErr("note_save_body", map[string]any{
		"idempotency_key": "guard-save",
		"project_id":      h.projID,
		"note_id":         noteID,
		"body_md":         "x",
		"expected_hash":   got["body_hash"],
		"kind":            "paper_note",
		"paper_id":        "01a1d000-0000-7000-8000-000000000000",
	}, mcp.CodeInvalidArgument)

	// paper_import cannot take an arbitrary local path.
	h.callErr("paper_import", map[string]any{
		"idempotency_key": "guard-import",
		"project_id":      h.projID,
		"path":            "C:/tmp/x.pdf",
	}, mcp.CodeInvalidArgument)

	// Unknown relation types are rejected by the schema.
	h.callErr("relation_add", map[string]any{
		"idempotency_key": "guard-relation",
		"project_id":      h.projID,
		"src_type":        "note",
		"src_id":          noteID,
		"relation":        "parent_of",
		"dst_type":        "note",
		"dst_id":          noteID,
	}, mcp.CodeInvalidArgument)
	_ = os.Remove
}

// TestServerStatus covers the core status tool.
func TestServerStatus(t *testing.T) {
	h := newHarness(t)
	status := h.callOK("server_status", map[string]any{})
	if status["access_mode"] != "read_write" {
		t.Fatalf("access_mode = %v", status["access_mode"])
	}
	versions := status["mcp_versions"].([]any)
	if len(versions) != 2 {
		t.Fatalf("mcp_versions = %v", versions)
	}
	modules := status["modules"].([]any)
	if len(modules) == 0 {
		t.Fatalf("modules = %v", modules)
	}
	if status["workspace_root"] != h.ws.Root() {
		t.Fatalf("workspace_root = %v", status["workspace_root"])
	}
}
