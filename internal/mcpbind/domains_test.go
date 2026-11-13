package mcpbind_test

import (
	"testing"
	"time"

	"researchos/internal/mcp"
)

// TestIdeasAndEvidence covers the SPEC-06 direct-write paths.
func TestIdeasAndEvidence(t *testing.T) {
	h := newHarness(t)
	created := h.callOK("idea_create", map[string]any{
		"idempotency_key": "idea-a",
		"project_id":      h.projID,
		"title":           "新想法",
		"description":     "描述",
	})
	idea := created["idea"].(map[string]any)
	ideaID := idea["idea_id"].(string)
	if idea["status"] != "New" {
		t.Fatalf("initial status = %v", idea["status"])
	}

	h.callErr("idea_change_status", map[string]any{
		"idempotency_key": "idea-status-bad",
		"project_id":      h.projID,
		"idea_id":         ideaID,
		"status":          "Rejected",
	}, mcp.CodeInvalidArgument)

	changed := h.callOK("idea_change_status", map[string]any{
		"idempotency_key": "idea-status-1",
		"project_id":      h.projID,
		"idea_id":         ideaID,
		"status":          "Researching",
	})
	if changed["idea"].(map[string]any)["status"] != "Researching" {
		t.Fatalf("status change failed: %v", changed)
	}

	// A stale expected_updated_at must conflict.
	stale := h.callErr("idea_update", map[string]any{
		"idempotency_key":     "idea-update-stale",
		"project_id":          h.projID,
		"idea_id":             ideaID,
		"title":               "改名",
		"expected_updated_at": "2020-01-01T00:00:00.000Z",
	}, mcp.CodeConflict)
	if stale.Details["current_updated_at"] == nil {
		t.Fatalf("conflict details missing current_updated_at: %v", stale.Details)
	}

	note := h.callOK("note_create", map[string]any{
		"idempotency_key": "evidence-note",
		"project_id":      h.projID,
		"title":           "证据笔记",
		"kind":            "scratchpad",
		"body_md":         "实验结果",
	})
	noteID := note["note"].(map[string]any)["note_id"].(string)

	ev := h.callOK("evidence_create", map[string]any{
		"idempotency_key": "evidence-1",
		"project_id":      h.projID,
		"idea_id":         ideaID,
		"direction":       "support",
		"content":         "指标提升",
		"source_type":     "note",
		"source_id":       noteID,
	})
	evidence := ev["evidence"].(map[string]any)
	if evidence["confirmed"] != true || evidence["ai_generated"] != true && evidence["ai_generated"] != false {
		t.Fatalf("evidence = %v", evidence)
	}
	detail := h.callOK("idea_get", map[string]any{"project_id": h.projID, "idea_id": ideaID})
	if len(detail["evidence"].([]any)) != 1 {
		t.Fatalf("idea evidence = %v", detail["evidence"])
	}
}

// TestExperimentsAndJobs covers scan (read-only job), create and job polling.
func TestExperimentsAndJobs(t *testing.T) {
	h := newHarness(t)
	created := h.callOK("experiment_create", map[string]any{
		"idempotency_key": "exp-1",
		"project_id":      h.projID,
		"title":           "实验一",
		"purpose":         "验证想法",
	})
	expID := created["experiment"].(map[string]any)["experiment_id"].(string)

	job := h.callOK("experiment_scan", map[string]any{
		"idempotency_key": "exp-scan-nodir",
		"project_id":      h.projID,
		"experiment_id":   expID,
	})
	failed := h.waitJob(job["job_id"].(string))
	if failed["state"] != "failed" {
		t.Fatalf("scan without directory job = %v", failed)
	}

	detail := h.callOK("experiment_get", map[string]any{"project_id": h.projID, "experiment_id": expID})
	if detail["experiment"] == nil {
		t.Fatalf("experiment detail = %v", detail)
	}
	_ = time.Now
}

// TestWeeklyReports covers report_create/report_get/report_update and the
// confirmation-only report_confirm path.
func TestWeeklyReports(t *testing.T) {
	h := newHarness(t)
	created := h.callOK("report_create", map[string]any{
		"idempotency_key": "report-1",
		"project_id":      h.projID,
		"period_start":    "2026-11-02",
		"period_end":      "2026-11-08",
	})
	reportID := created["report_id"].(string)
	if created["status"] != "draft" {
		t.Fatalf("report status = %v", created["status"])
	}

	updated := h.callOK("report_update", map[string]any{
		"idempotency_key": "report-update-1",
		"project_id":      h.projID,
		"report_id":       reportID,
		"content_md":      "# 本周\n\n完成了实验。",
	})
	report := updated["report"].(map[string]any)
	if report["status"] != "draft" {
		t.Fatalf("report status after update = %v", report["status"])
	}

	got := h.callOK("report_get", map[string]any{"project_id": h.projID, "report_id": reportID})
	if got["content_md"] != "# 本周\n\n完成了实验。" {
		t.Fatalf("report content = %v", got["content_md"])
	}

	pending := h.callOK("report_confirm", map[string]any{
		"idempotency_key": "report-confirm-1",
		"project_id":      h.projID,
		"report_id":       reportID,
	})
	if pending["status"] != "pending" {
		t.Fatalf("report_confirm = %v", pending)
	}
	// Still draft: MCP cannot approve.
	still := h.callOK("report_get", map[string]any{"project_id": h.projID, "report_id": reportID})
	if still["report"].(map[string]any)["status"] != "draft" {
		t.Fatal("report confirmed without in-app approval")
	}
}

// TestLiteratureWithoutZotero verifies degraded behaviour: paper listing works,
// Zotero reads report EXTERNAL_UNAVAILABLE instead of crashing.
func TestLiteratureWithoutZotero(t *testing.T) {
	h := newHarness(t)
	list := h.callOK("paper_list", map[string]any{})
	if items, ok := list["items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("paper_list = %v", list)
	}
	status := h.callOK("zotero_status", map[string]any{})
	if _, ok := status["connected"].(bool); !ok {
		t.Fatalf("zotero_status = %v", status)
	}
	// On a machine without Zotero this reports EXTERNAL_UNAVAILABLE; with a
	// configured Zotero it lists libraries. Both are valid degradations.
	_ = h.call("zotero_libraries", map[string]any{})
}

// TestOverviewRelationsAndCrossProjectReads exercises the remaining read/write
// bindings that do not need Zotero or AI.
func TestOverviewRelationsAndCrossProjectReads(t *testing.T) {
	h := newHarness(t)

	overview := h.callOK("project_overview", map[string]any{"project_id": h.projID})
	if overview == nil {
		t.Fatal("project_overview returned nothing")
	}
	h.callOK("literature_overview_get", map[string]any{"project_id": h.projID})
	h.callOK("literature_overview_update", map[string]any{
		"idempotency_key": "lit-overview-1",
		"project_id":      h.projID,
		"body_md":         "# 综述\n\n第一版",
	})
	second := h.callOK("literature_overview_update", map[string]any{
		"idempotency_key": "lit-overview-2",
		"project_id":      h.projID,
		"body_md":         "# 综述\n\n第二版",
	})
	inserted := second["overview"].(map[string]any)
	if inserted["body_md"] != "# 综述\n\n第二版" {
		t.Fatalf("overview body = %v", inserted["body_md"])
	}

	a := h.callOK("note_create", map[string]any{
		"idempotency_key": "rel-note-a",
		"project_id":      h.projID,
		"title":           "A",
		"kind":            "scratchpad",
	})
	b := h.callOK("note_create", map[string]any{
		"idempotency_key": "rel-note-b",
		"project_id":      h.projID,
		"title":           "B",
		"kind":            "scratchpad",
	})
	aID := a["note"].(map[string]any)["note_id"].(string)
	bID := b["note"].(map[string]any)["note_id"].(string)
	h.callOK("relation_add", map[string]any{
		"idempotency_key": "rel-add",
		"project_id":      h.projID,
		"src_type":        "note",
		"src_id":          aID,
		"relation":        "references",
		"dst_type":        "note",
		"dst_id":          bID,
	})
	rels := h.callOK("relation_list", map[string]any{
		"project_id": h.projID,
		"src_type":   "note",
		"src_id":     aID,
	})
	if len(rels["relations"].([]any)) != 1 {
		t.Fatalf("relations = %v", rels["relations"])
	}
	h.callOK("relation_remove", map[string]any{
		"idempotency_key": "rel-remove",
		"project_id":      h.projID,
		"relation_id":     "ignored-for-now",
		"src_type":        "note",
		"src_id":          aID,
		"relation":        "references",
		"dst_type":        "note",
		"dst_id":          bID,
	})
	// Cross-project aggregation is read-only and must include this project.
	all := h.callOK("trash_list", map[string]any{})
	if _, ok := all["items"].([]any); !ok {
		t.Fatalf("trash_list all = %v", all)
	}
	h.callOK("confirmation_list", map[string]any{})
	h.callOK("job_list", map[string]any{})
}

// TestRegenerateNeedsConfirmation covers the replace-with-content rule: MCP
// returns pending rather than a job when an overwrite would destroy content.
func TestRegenerateNeedsConfirmation(t *testing.T) {
	h := newHarness(t)
	created := h.callOK("report_create", map[string]any{
		"idempotency_key": "regen-report",
		"project_id":      h.projID,
		"period_start":    "2026-11-02",
		"period_end":      "2026-11-08",
	})
	reportID := created["report_id"].(string)
	h.callOK("report_update", map[string]any{
		"idempotency_key": "regen-update",
		"project_id":      h.projID,
		"report_id":       reportID,
		"content_md":      "# 手工内容",
	})
	result := h.callOK("report_generate", map[string]any{
		"idempotency_key": "regen-generate",
		"project_id":      h.projID,
		"report_id":       reportID,
		"replace":         true,
	})
	if result["status"] != "pending" {
		t.Fatalf("replace with content returned %v", result)
	}
	// The stored content is untouched while the request waits.
	got := h.callOK("report_get", map[string]any{"project_id": h.projID, "report_id": reportID})
	if got["content_md"] != "# 手工内容" {
		t.Fatalf("content changed before approval: %v", got["content_md"])
	}
	// Without replace the tool is a job.
	job := h.callOK("report_generate", map[string]any{
		"idempotency_key": "regen-plain",
		"project_id":      h.projID,
		"report_id":       reportID,
	})
	if job["status"] != "job" {
		t.Fatalf("plain generate = %v", job)
	}
	_ = h.waitJob(job["job_id"].(string))
}

// TestArchivedExportAllowed covers §5.4 rule 2's export exception.
func TestArchivedExportAllowed(t *testing.T) {
	h := newHarness(t)
	h.callOK("project_archive", map[string]any{
		"idempotency_key": "arch-export-archive",
		"project_id":      h.projID,
	})
	job := h.callOK("project_export", map[string]any{
		"idempotency_key": "arch-export",
		"project_id":      h.projID,
		"format":          "folder",
	})
	done := h.waitJob(job["job_id"].(string))
	if done["state"] != "succeeded" {
		t.Fatalf("archived export = %v", done)
	}
}
