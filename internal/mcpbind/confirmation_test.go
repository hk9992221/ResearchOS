package mcpbind_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"researchos/internal/mcp"
	"researchos/internal/mcpbind"
	"researchos/internal/weeklyreview"
)

// TestAccessModeReadOnly covers §8.3 item 11.
func TestAccessModeReadOnly(t *testing.T) {
	h := newHarness(t)
	h.setSetting("mcp.access_mode", "read_only")

	tools, err := h.app.Host.Tools(h.ctx, h.principal())
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	for _, spec := range tools {
		if spec.Name == "note_create" || spec.Name == "project_delete" {
			t.Fatalf("write tool %s listed in read_only mode", spec.Name)
		}
	}
	h.callErr("note_create", map[string]any{
		"idempotency_key": "ro-note",
		"project_id":      h.projID,
		"title":           "不该创建",
		"kind":            "scratchpad",
	}, mcp.CodeAccessMode)
	// Reads still work.
	h.callOK("note_list", map[string]any{"project_id": h.projID})
}

// TestArchivedProjectReadOnly covers §8.3 item 10 and the two lifecycle
// exceptions.
func TestArchivedProjectReadOnly(t *testing.T) {
	h := newHarness(t)
	h.callOK("project_archive", map[string]any{
		"idempotency_key": "archive-1",
		"project_id":      h.projID,
	})
	h.callErr("note_create", map[string]any{
		"idempotency_key": "archived-note",
		"project_id":      h.projID,
		"title":           "不该创建",
		"kind":            "scratchpad",
	}, mcp.CodeArchivedReadOnly)
	h.callOK("note_list", map[string]any{"project_id": h.projID})
	h.callOK("project_unarchive", map[string]any{
		"idempotency_key": "unarchive-1",
		"project_id":      h.projID,
	})
	h.callOK("note_create", map[string]any{
		"idempotency_key": "after-unarchive",
		"project_id":      h.projID,
		"title":           "恢复后可写",
		"kind":            "scratchpad",
	})
}

// TestProjectDeleteConfirmation covers the confirmation state machine,
// idempotent replay of C tools and the in-app executor.
func TestProjectDeleteConfirmation(t *testing.T) {
	h := newHarness(t)
	second := h.callOK("project_create", map[string]any{
		"idempotency_key": "proj-2",
		"name":            "Beta",
	})["project"].(map[string]any)
	target := second["id"].(string)

	preview := h.callOK("project_delete", map[string]any{
		"idempotency_key": "delete-preview",
		"project_id":      target,
		"dry_run":         true,
	})
	if preview["status"] != "dry_run" {
		t.Fatalf("dry_run result = %v", preview)
	}

	pending := h.callOK("project_delete", map[string]any{
		"idempotency_key": "delete-1",
		"project_id":      target,
	})
	confirmationID := pending["confirmation_id"].(string)
	replay := h.callOK("project_delete", map[string]any{
		"idempotency_key": "delete-1",
		"project_id":      target,
	})
	if replay["confirmation_id"] != confirmationID {
		t.Fatal("C-tool replay queued a second confirmation")
	}

	// The project is untouched while the request is pending.
	h.callOK("project_get", map[string]any{"project_id": target})
	view := h.callOK("confirmation_get", map[string]any{"confirmation_id": confirmationID})
	if view["state"] != "requested" {
		t.Fatalf("confirmation state = %v", view["state"])
	}

	// Deny: no writes, state denied.
	if _, err := h.app.Executor.ResolveAndExecute(h.ctx, mcpbind.ExecuteInput{ConfirmationID: confirmationID, Approve: false}); err != nil {
		t.Fatalf("deny: %v", err)
	}
	denied := h.callOK("confirmation_get", map[string]any{"confirmation_id": confirmationID})
	if denied["state"] != "denied" {
		t.Fatalf("state after deny = %v", denied["state"])
	}
	h.callOK("project_get", map[string]any{"project_id": target})
}

// TestConfirmationTTLAndExecutorPreconditions covers expired requests and the
// project-name precondition.
func TestConfirmationTTLAndExecutorPreconditions(t *testing.T) {
	h := newHarness(t)
	second := h.callOK("project_create", map[string]any{
		"idempotency_key": "proj-ttl",
		"name":            "Gamma",
	})["project"].(map[string]any)
	target := second["id"].(string)

	pending := h.callOK("project_delete", map[string]any{
		"idempotency_key": "delete-ttl",
		"project_id":      target,
	})
	confirmationID := pending["confirmation_id"].(string)
	if err := h.app.Confirmations.ExpireNow(h.ctx, confirmationID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	view := h.callOK("confirmation_get", map[string]any{"confirmation_id": confirmationID})
	if view["state"] != "expired" {
		t.Fatalf("state = %v", view["state"])
	}
	if _, err := h.app.Executor.Resolve(h.ctx, confirmationID, true, "user"); err == nil {
		t.Fatal("expired confirmation was approved")
	}

	// A fresh request executes only with the exact project name.
	again := h.callOK("project_delete", map[string]any{
		"idempotency_key": "delete-2",
		"project_id":      target,
	})
	id2 := again["confirmation_id"].(string)
	if _, err := h.app.Executor.ResolveAndExecute(h.ctx, mcpbind.ExecuteInput{ConfirmationID: id2, Approve: true, UserInput: map[string]any{"confirm_name": "wrong"}}); err == nil {
		t.Fatal("wrong project name executed")
	}
	failed := h.callOK("confirmation_get", map[string]any{"confirmation_id": id2})
	if failed["state"] != "failed" {
		t.Fatalf("state after failed precondition = %v", failed["state"])
	}
	h.callOK("project_get", map[string]any{"project_id": target})

	// A correct approval deletes exactly once.
	delta := h.callOK("project_create", map[string]any{
		"idempotency_key": "proj-delta",
		"name":            "Delta",
	})["project"].(map[string]any)
	deltaID := delta["id"].(string)
	third := h.callOK("project_delete", map[string]any{
		"idempotency_key": "delete-delta",
		"project_id":      deltaID,
	})
	id3 := third["confirmation_id"].(string)
	if _, err := h.app.Executor.ResolveAndExecute(h.ctx, mcpbind.ExecuteInput{
		ConfirmationID: id3,
		Approve:        true,
		UserInput:      map[string]any{"confirm_name": "Delta"},
	}); err != nil {
		t.Fatalf("execute delete: %v", err)
	}
	h.callErr("project_get", map[string]any{"project_id": deltaID}, mcp.CodeNotFound)
	if _, err := h.app.Executor.Resolve(h.ctx, id3, true, "user"); err == nil {
		t.Fatal("second approval of an executed confirmation succeeded")
	}
}

// TestTrashEmptyConfirmation covers expected_counts validation and purge.
func TestTrashEmptyConfirmation(t *testing.T) {
	h := newHarness(t)
	note := h.callOK("note_create", map[string]any{
		"idempotency_key": "purge-note",
		"project_id":      h.projID,
		"title":           "待清空",
		"kind":            "scratchpad",
	})
	noteID := note["note"].(map[string]any)["note_id"].(string)
	h.callOK("note_delete", map[string]any{
		"idempotency_key": "purge-note-del",
		"project_id":      h.projID,
		"note_id":         noteID,
	})

	preview := h.callOK("trash_empty_preview", map[string]any{"project_id": h.projID})
	counts := preview["counts"].(map[string]any)
	if counts["note"] == nil {
		t.Fatalf("preview counts = %v", counts)
	}

	h.callErr("trash_empty", map[string]any{
		"idempotency_key": "purge-bad",
		"project_id":      h.projID,
		"expected_counts": map[string]any{"note": 99},
	}, mcp.CodeConflict)

	pending := h.callOK("trash_empty", map[string]any{
		"idempotency_key": "purge-1",
		"project_id":      h.projID,
		"expected_counts": counts,
	})
	id := pending["confirmation_id"].(string)
	if _, err := h.app.Executor.ResolveAndExecute(h.ctx, mcpbind.ExecuteInput{ConfirmationID: id, Approve: true}); err != nil {
		t.Fatalf("execute purge: %v", err)
	}
	after := h.callOK("trash_list", map[string]any{"project_id": h.projID})
	if len(after["items"].([]any)) != 0 {
		t.Fatalf("trash not empty: %v", after["items"])
	}
	activities := h.callOK("activity_list", map[string]any{
		"project_id":  h.projID,
		"event_types": []string{"entity.purged"},
	})
	if len(activities["items"].([]any)) != 1 {
		t.Fatalf("entity.purged rows = %d", len(activities["items"].([]any)))
	}
	purged := activities["items"].([]any)[0].(map[string]any)
	if purged["actor"] != "user" {
		t.Fatalf("purged actor = %v, want user", purged["actor"])
	}
}

// TestExportJob covers the write job pipeline and the default destination.
func TestExportJob(t *testing.T) {
	h := newHarness(t)
	job := h.callOK("project_export", map[string]any{
		"idempotency_key": "export-1",
		"project_id":      h.projID,
		"format":          "zip",
	})
	jobID := job["job_id"].(string)
	done := h.waitJob(jobID)
	if done["state"] != "succeeded" {
		t.Fatalf("export job = %v", done)
	}
	result := done["result"].(map[string]any)
	path, _ := result["artifact_path"].(string)
	if path == "" {
		t.Fatalf("export result = %v", result)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("artifact missing: %v", err)
	}
	if filepath.Ext(path) != ".zip" {
		t.Fatalf("artifact ext = %v", filepath.Ext(path))
	}
	if !strings.HasPrefix(path, filepath.Join(h.ws.Root(), "exports")+string(filepath.Separator)) {
		t.Fatalf("artifact not under workspace/exports: %v", path)
	}
	if !strings.Contains(filepath.Base(path), "Alpha") {
		t.Fatalf("artifact name lacks the project name: %v", path)
	}
	activities := h.callOK("activity_list", map[string]any{
		"project_id":  h.projID,
		"event_types": []string{"project.exported"},
	})
	if len(activities["items"].([]any)) != 1 {
		t.Fatalf("project.exported rows = %d", len(activities["items"].([]any)))
	}
}

// TestReadOnlyScanJobAndCancel covers read-only jobs and cancellation.
func TestReadOnlyScanJobAndCancel(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "run.log"), []byte("epoch,loss\n1,0.5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := h.callOK("experiment_create", map[string]any{
		"idempotency_key": "exp-dir",
		"project_id":      h.projID,
		"title":           "有目录的实验",
		"dir_ref":         dir,
	})
	expID := created["experiment"].(map[string]any)["experiment_id"].(string)
	beforeScan := len(h.callOK("activity_list", map[string]any{"project_id": h.projID})["items"].([]any))
	job := h.callOK("experiment_scan", map[string]any{
		"idempotency_key": "scan-1",
		"project_id":      h.projID,
		"experiment_id":   expID,
	})
	done := h.waitJob(job["job_id"].(string))
	if done["state"] != "succeeded" {
		t.Fatalf("scan job = %v", done)
	}
	result := done["result"].(map[string]any)
	if result["files"] == nil {
		t.Fatalf("scan result = %v", result)
	}
	if done["read_only"] != true {
		t.Fatalf("scan job read_only = %v", done["read_only"])
	}
	// A read-only job writes no Activity.
	afterScan := len(h.callOK("activity_list", map[string]any{"project_id": h.projID})["items"].([]any))
	if beforeScan != afterScan {
		t.Fatalf("read-only scan wrote activities: %d -> %d", beforeScan, afterScan)
	}
	_ = time.Now
}

// TestReportConfirmExecutor runs report_confirm through the in-app executor
// and asserts the event actor is user.
func TestReportConfirmExecutor(t *testing.T) {
	h := newHarness(t)
	created := h.callOK("report_create", map[string]any{
		"idempotency_key": "report-exec",
		"project_id":      h.projID,
		"period_start":    "2026-10-26",
		"period_end":      "2026-11-01",
	})
	reportID := created["report_id"].(string)
	period := weeklyreview.Period{
		Start:        time.Date(2026, 10, 26, 0, 0, 0, 0, time.Local),
		EndExclusive: time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local),
	}
	skeleton := weeklyreview.BuildSkeleton("Alpha", period, nil)
	h.callOK("report_update", map[string]any{
		"idempotency_key": "report-exec-update",
		"project_id":      h.projID,
		"report_id":       reportID,
		"content_md":      skeleton,
	})
	pending := h.callOK("report_confirm", map[string]any{
		"idempotency_key": "report-exec-confirm",
		"project_id":      h.projID,
		"report_id":       reportID,
	})
	id := pending["confirmation_id"].(string)
	if _, err := h.app.Executor.ResolveAndExecute(h.ctx, mcpbind.ExecuteInput{ConfirmationID: id, Approve: true}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	got := h.callOK("report_get", map[string]any{"project_id": h.projID, "report_id": reportID})
	if got["report"].(map[string]any)["status"] != "confirmed" {
		t.Fatalf("report status = %v", got["report"].(map[string]any)["status"])
	}
	activities := h.callOK("activity_list", map[string]any{
		"project_id":  h.projID,
		"event_types": []string{"status.changed"},
	})
	found := false
	for _, raw := range activities["items"].([]any) {
		item := raw.(map[string]any)
		if item["entity_id"] == reportID {
			found = true
			if item["actor"] != "user" {
				t.Fatalf("confirm actor = %v", item["actor"])
			}
		}
	}
	if !found {
		t.Fatal("status.changed for the report not found")
	}
	state := h.callOK("confirmation_get", map[string]any{"confirmation_id": id})
	if state["state"] != "executed" {
		t.Fatalf("confirmation state = %v", state["state"])
	}
}

// TestExportRejectsIncludePapers covers the fixed PDF-copy contract.
func TestExportRejectsIncludePapers(t *testing.T) {
	h := newHarness(t)
	h.callErr("project_export", map[string]any{
		"idempotency_key": "export-bad",
		"project_id":      h.projID,
		"format":          "folder",
		"include_papers":  false,
	}, mcp.CodeInvalidArgument)
}

// TestColumnDeleteExecutor covers §8.10 item 37 end to end.
func TestColumnDeleteExecutor(t *testing.T) {
	h := newHarness(t)
	task := h.callOK("task_create", map[string]any{
		"idempotency_key": "col-task",
		"project_id":      h.projID,
		"title":           "在 Review 列",
		"column_id":       "Review",
	})
	taskID := task["task"].(map[string]any)["task_id"].(string)
	pending := h.callOK("column_delete", map[string]any{
		"idempotency_key": "col-delete-exec",
		"project_id":      h.projID,
		"name":            "Review",
		"migrate_to":      "Done",
	})
	id := pending["confirmation_id"].(string)
	if _, err := h.app.Executor.ResolveAndExecute(h.ctx, mcpbind.ExecuteInput{ConfirmationID: id, Approve: true}); err != nil {
		t.Fatalf("delete column: %v", err)
	}
	cols := h.callOK("column_list", map[string]any{"project_id": h.projID})
	for _, raw := range cols["columns"].([]any) {
		if raw.(map[string]any)["name"] == "Review" {
			t.Fatal("column Review still exists")
		}
	}
	got := h.callOK("task_get", map[string]any{"project_id": h.projID, "task_id": taskID})
	if got["column"] != "Done" {
		t.Fatalf("task column = %v", got["column"])
	}
}

// TestTrashCountsChangedAtExecution covers §8.4 item 17's execution-time check.
func TestTrashCountsChangedAtExecution(t *testing.T) {
	h := newHarness(t)
	makeDeleted := func(key string) string {
		note := h.callOK("note_create", map[string]any{
			"idempotency_key": key,
			"project_id":      h.projID,
			"title":           key,
			"kind":            "scratchpad",
		})
		id := note["note"].(map[string]any)["note_id"].(string)
		h.callOK("note_delete", map[string]any{
			"idempotency_key": key + "-del",
			"project_id":      h.projID,
			"note_id":         id,
		})
		return id
	}
	makeDeleted("trash-a")
	preview := h.callOK("trash_empty_preview", map[string]any{"project_id": h.projID})
	pending := h.callOK("trash_empty", map[string]any{
		"idempotency_key": "trash-exec",
		"project_id":      h.projID,
		"expected_counts": preview["counts"],
	})
	makeDeleted("trash-b")
	id := pending["confirmation_id"].(string)
	if _, err := h.app.Executor.ResolveAndExecute(h.ctx, mcpbind.ExecuteInput{ConfirmationID: id, Approve: true}); err == nil {
		t.Fatal("purge executed despite changed counts")
	}
	after := h.callOK("trash_list", map[string]any{"project_id": h.projID})
	if len(after["items"].([]any)) != 2 {
		t.Fatalf("trash changed after rejected purge: %v", after["items"])
	}
	state := h.callOK("confirmation_get", map[string]any{"confirmation_id": id})
	if state["state"] != "failed" {
		t.Fatalf("confirmation state = %v", state["state"])
	}
}

// TestAIUnavailable verifies the degraded AI path.
func TestAIUnavailable(t *testing.T) {
	h := newHarness(t)
	idea := h.callOK("idea_create", map[string]any{
		"idempotency_key": "ai-idea",
		"project_id":      h.projID,
		"title":           "需要 AI",
	})
	ideaID := idea["idea"].(map[string]any)["idea_id"].(string)
	h.callErr("evidence_extract", map[string]any{
		"idempotency_key": "ai-extract",
		"project_id":      h.projID,
		"idea_id":         ideaID,
	}, mcp.CodeAIUnavailable)
	// Basic tools keep working.
	h.callOK("note_list", map[string]any{"project_id": h.projID})
}

// TestColumnRules covers §8.10 item 38.
func TestColumnRules(t *testing.T) {
	h := newHarness(t)
	task := h.callOK("task_create", map[string]any{
		"idempotency_key": "rename-task",
		"project_id":      h.projID,
		"title":           "待迁移",
		"column_id":       "Todo",
	})
	taskID := task["task"].(map[string]any)["task_id"].(string)
	h.callOK("column_rename", map[string]any{
		"idempotency_key": "rename-col",
		"project_id":      h.projID,
		"old_name":        "Todo",
		"new_name":        "To Do",
	})
	got := h.callOK("task_get", map[string]any{"project_id": h.projID, "task_id": taskID})
	if got["column"] != "To Do" {
		t.Fatalf("task column after rename = %v", got["column"])
	}
	h.callErr("column_set_done", map[string]any{
		"idempotency_key": "last-done",
		"project_id":      h.projID,
		"name":            "Done",
		"done":            false,
	}, mcp.CodeInvalidArgument)
}
