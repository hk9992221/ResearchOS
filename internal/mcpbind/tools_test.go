package mcpbind_test

import (
	"testing"

	"researchos/internal/mcp"
)

// TestProjectAndNotes covers the direct-write and read paths of §5.3.1/§5.3.3.
func TestProjectAndNotes(t *testing.T) {
	h := newHarness(t)

	list := h.callOK("project_list", map[string]any{})
	if len(list["items"].([]any)) != 1 {
		t.Fatalf("project_list items = %v", list["items"])
	}
	got := h.callOK("project_get", map[string]any{"project_id": h.projID})
	if got["dir_status"] != "ok" {
		t.Fatalf("dir_status = %v", got["dir_status"])
	}

	created := h.callOK("note_create", map[string]any{
		"idempotency_key": "note-create-1",
		"project_id":      h.projID,
		"title":           "第一篇笔记",
		"kind":            "scratchpad",
		"body_md":         "初始正文",
	})
	note := created["note"].(map[string]any)
	noteID := note["note_id"].(string)

	gotNote := h.callOK("note_get", map[string]any{"project_id": h.projID, "note_id": noteID})
	if gotNote["body_md"] != "初始正文" {
		t.Fatalf("note body = %v", gotNote["body_md"])
	}
	bodyHash := gotNote["body_hash"].(string)

	saved := h.callOK("note_save_body", map[string]any{
		"idempotency_key": "note-save-1",
		"project_id":      h.projID,
		"note_id":         noteID,
		"body_md":         "第二版正文",
		"expected_hash":   bodyHash,
	})
	newHash := saved["body_hash"].(string)
	if newHash == bodyHash {
		t.Fatal("body hash did not change")
	}

	h.callErr("note_save_body", map[string]any{
		"idempotency_key": "note-save-2",
		"project_id":      h.projID,
		"note_id":         noteID,
		"body_md":         "过期哈希写入",
		"expected_hash":   bodyHash,
	}, mcp.CodeConflict)

	// Same-day saves merge into one note.updated event with save_count 2.
	h.callOK("note_save_body", map[string]any{
		"idempotency_key": "note-save-3",
		"project_id":      h.projID,
		"note_id":         noteID,
		"body_md":         "第三版正文",
		"expected_hash":   newHash,
	})

	activities := h.callOK("activity_list", map[string]any{
		"project_id":  h.projID,
		"event_types": []string{"note.updated"},
	})
	items := activities["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("note.updated rows = %d, want 1 (%v)", len(items), items)
	}
	payload := items[0].(map[string]any)["payload"].(map[string]any)
	if count, _ := payload["save_count"].(float64); int(count) != 2 {
		t.Fatalf("save_count = %v, want 2", payload["save_count"])
	}

	h.callOK("note_delete", map[string]any{
		"idempotency_key": "note-del-1",
		"project_id":      h.projID,
		"note_id":         noteID,
	})
	trashItems := h.callOK("trash_list", map[string]any{"project_id": h.projID})
	if len(trashItems["items"].([]any)) != 1 {
		t.Fatalf("trash items = %v", trashItems["items"])
	}
	h.callOK("trash_restore", map[string]any{
		"idempotency_key": "trash-restore-1",
		"project_id":      h.projID,
		"entity_type":     "note",
		"entity_id":       noteID,
	})
	restored := h.callOK("note_get", map[string]any{"project_id": h.projID, "note_id": noteID})
	if restored["note"].(map[string]any)["deleted_at"] != nil {
		t.Fatal("note still deleted after restore")
	}
	restoredEvents := h.callOK("activity_list", map[string]any{
		"project_id":  h.projID,
		"event_types": []string{"entity.restored"},
	})
	items = restoredEvents["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["actor"] != "mcp" {
		t.Fatalf("entity.restored events = %v", items)
	}
}

// TestIdempotencyReplayAndConflict covers §8.3 item 12.
func TestIdempotencyReplayAndConflict(t *testing.T) {
	h := newHarness(t)
	args := map[string]any{
		"idempotency_key": "idea-1",
		"project_id":      h.projID,
		"title":           "幂等 Idea",
	}
	first := h.callOK("idea_create", args)
	second := h.callOK("idea_create", args)
	if first["idea"].(map[string]any)["idea_id"] != second["idea"].(map[string]any)["idea_id"] {
		t.Fatal("replay returned a different entity")
	}
	conflict := map[string]any{
		"idempotency_key": "idea-1",
		"project_id":      h.projID,
		"title":           "不同标题",
	}
	h.callErr("idea_create", conflict, mcp.CodeConflict)

	h.callErr("idea_create", map[string]any{
		"project_id": h.projID,
		"title":      "缺少幂等键",
	}, mcp.CodeInvalidArgument)
}

// TestKanbanAndChecklist covers task writes, column deletion confirmation and
// the checklist invariant.
func TestKanbanAndChecklist(t *testing.T) {
	h := newHarness(t)
	cols := h.callOK("column_list", map[string]any{"project_id": h.projID})
	names := cols["columns"].([]any)
	if len(names) != 5 {
		t.Fatalf("default columns = %d", len(names))
	}
	created := h.callOK("task_create", map[string]any{
		"idempotency_key": "task-1",
		"project_id":      h.projID,
		"title":           "写论文",
		"priority":        "high",
	})
	task := created["task"].(map[string]any)
	taskID := task["task_id"].(string)
	updatedAt := task["updated_at"]

	item := h.callOK("checklist_add", map[string]any{
		"idempotency_key": "check-1",
		"project_id":      h.projID,
		"task_id":         taskID,
		"text":            "先写提纲",
	})
	itemID := item["item"].(map[string]any)["item_id"].(string)
	h.callOK("checklist_set_done", map[string]any{
		"idempotency_key": "check-2",
		"project_id":      h.projID,
		"item_id":         itemID,
		"done":            true,
	})
	detail := h.callOK("task_get", map[string]any{"project_id": h.projID, "task_id": taskID})
	if detail["updated_at"] != updatedAt {
		t.Fatalf("checklist changed task updated_at: %v -> %v", updatedAt, detail["updated_at"])
	}

	pending := h.callOK("column_delete", map[string]any{
		"idempotency_key": "coldel-1",
		"project_id":      h.projID,
		"name":            "Review",
		"migrate_to":      "Done",
	})
	if pending["status"] != "pending" || pending["confirmation_id"] == nil {
		t.Fatalf("column_delete result = %v", pending)
	}
	// Nothing changes before the in-app approval.
	after := h.callOK("column_list", map[string]any{"project_id": h.projID})
	if len(after["columns"].([]any)) != 5 {
		t.Fatal("column deleted by MCP without confirmation")
	}
}
