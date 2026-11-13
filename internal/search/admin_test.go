package search

import (
	"context"
	"errors"
	"strings"
	"testing"

	"researchos/internal/foundation/clock"
)

// TestReindexJobAndStatus covers the job id, status fields and progress.
func TestReindexJobAndStatus(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "状态项目")
	insertNote(t, ws, projectID, "scratchpad", "状态", "status report content", "")
	insertIdea(t, ws, projectID, "状态想法", "status idea body", "New", "")
	svc := newSearchService(t, ws, Options{})
	ctx := context.Background()
	scope := Scope{Kind: ScopeProject, ProjectID: projectID}

	jobID, err := svc.Reindex(ctx, ReindexRequest{Scope: scope})
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if jobID == "" {
		t.Fatal("缺少 job id")
	}
	status := waitIdle(t, svc, scope)
	if status.Docs != 2 || status.Chunks == 0 {
		t.Fatalf("文档/块统计错误: %+v", status)
	}
	if status.SchemaVersion != "1" {
		t.Fatalf("schema version: %q", status.SchemaVersion)
	}
	if !strings.Contains(status.ParserVersion, "md-1") {
		t.Fatalf("parser version: %q", status.ParserVersion)
	}
	if status.EmbeddingModel != "" {
		t.Fatalf("未配置 embedding 时不应有模型: %q", status.EmbeddingModel)
	}
	if status.LastIndexedAt.IsZero() {
		t.Fatal("缺少最近索引时间")
	}
	if status.Progress != 1 {
		t.Fatalf("完成作业的进度应为 1，实际 %v", status.Progress)
	}
}

// TestCancelJob covers cancellation and the not-found contract.
func TestCancelJob(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "取消项目")
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	jobID := mustID(t)
	now := clock.Format(clock.System{}.Now())
	if _, err := handle.DB().Exec(
		`INSERT INTO index_jobs (job_id, scope_kind, scope_id, status, created_at) VALUES (?, 'project', ?, 'pending', ?)`,
		jobID, projectID, now); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	svc := newSearchService(t, ws, Options{})
	if err := svc.CancelJob(context.Background(), jobID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	var status string
	if err := handle.DB().QueryRow(`SELECT status FROM index_jobs WHERE job_id = ?`, jobID).Scan(&status); err != nil {
		t.Fatalf("job status: %v", err)
	}
	if status != "canceled" {
		t.Fatalf("待运行作业应直接取消，实际 %q", status)
	}

	err = svc.CancelJob(context.Background(), mustID(t))
	var searchErr *Error
	if !errors.As(err, &searchErr) || searchErr.Code != CodeJobNotFound {
		t.Fatalf("未知作业错误码错误: %v", err)
	}
}

// TestEmbeddingFailureDegrades covers the query-time embedding degradation.
func TestEmbeddingFailureDegrades(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "嵌入失败")
	noteID := insertNote(t, ws, projectID, "scratchpad", "嵌入", "keyword fallback content", "")
	good := &fakeEmbedder{model: "fake-embed"}
	indexer := newSearchService(t, ws, Options{Embedding: good})
	indexEntities(t, indexer, projectID, Change{EntityType: EntityNote, EntityID: noteID})

	failing := &fakeEmbedder{model: "fake-embed", err: errors.New("upstream down")}
	querySvc := newSearchService(t, ws, Options{Embedding: failing})
	resp := search(t, querySvc, SearchRequest{Query: "keyword", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	if !hasDegrade(resp, DegradeEmbeddingFailed) {
		t.Fatalf("缺少 embedding_failed: %v", resp.Degraded)
	}
	if !containsDoc(resp, DocIDFor(EntityNote, noteID)) {
		t.Fatalf("向量失败不应影响关键词结果: %v", resultDocIDs(resp))
	}
}

// TestOpenTargetUnsupported covers the non-PDF navigation fallback.
func TestOpenTargetUnsupported(t *testing.T) {
	ws := newWorkspace(t)
	svc := newSearchService(t, ws, Options{})
	target := JumpTarget{Kind: "note", ProjectID: mustID(t), EntityID: mustID(t), Locator: Locator{Heading: "方法"}}
	err := svc.OpenTarget(context.Background(), target)
	var searchErr *Error
	if !errors.As(err, &searchErr) || searchErr.Code != CodeOpenUnsupported {
		t.Fatalf("缺少 open_unsupported: %v", err)
	}
	if !strings.Contains(err.Error(), "方法") {
		t.Fatalf("退化信息缺少定位: %v", err)
	}

	// An injected navigator makes the same target openable.
	var navigated JumpTarget
	nav := newSearchService(t, ws, Options{Navigator: func(ctx context.Context, t JumpTarget) error {
		navigated = t
		return nil
	}})
	if err := nav.OpenTarget(context.Background(), target); err != nil {
		t.Fatalf("navigator open: %v", err)
	}
	if navigated.EntityID != target.EntityID {
		t.Fatalf("navigator 收到错误目标: %+v", navigated)
	}
}
