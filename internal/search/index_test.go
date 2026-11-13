package search

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/workspace"
)

// TestA13IncrementalDebounce covers A13: a saved note becomes searchable
// quickly and ten rapid notifications produce one index write.
func TestA13IncrementalDebounce(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "增量项目")
	noteID := insertNote(t, ws, projectID, "scratchpad", "增量", "初始内容 initial", "")
	var writes atomic.Int64
	svc := newSearchService(t, ws, Options{
		Debounce: 120 * time.Millisecond,
		Hooks: Hooks{DocIndexed: func(scopeKey, docID, status string) {
			writes.Add(1)
		}},
	})
	ctx := context.Background()
	start := time.Now()
	for i := range 10 {
		rewriteNote(t, ws, projectID, noteID, "并发保存 debounce-final-内容 "+string(rune('a'+i)))
		if err := svc.Notify(ctx, Change{ProjectID: projectID, EntityType: EntityNote, EntityID: noteID, Kind: ChangeUpsert}); err != nil {
			t.Fatalf("notify %d: %v", i, err)
		}
	}
	waitFor(t, 3*time.Second, "笔记可检索", func() bool {
		resp, err := svc.Search(ctx, SearchRequest{
			Query: "debounce-final", Scope: Scope{Kind: ScopeProject, ProjectID: projectID},
		})
		return err == nil && containsDoc(resp, DocIDFor(EntityNote, noteID))
	})
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("增量索引超时: %v", elapsed)
	}
	waitIdle(t, svc, Scope{Kind: ScopeProject, ProjectID: projectID})
	if got := writes.Load(); got != 1 {
		t.Fatalf("十次连续保存应只写一次索引，实际 %d 次", got)
	}
	resp := search(t, svc, SearchRequest{Query: "debounce-final", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	result := findResult(t, resp, DocIDFor(EntityNote, noteID))
	if len(result.Snippets) == 0 {
		t.Fatal("缺少片段")
	}
}

// TestA14DeleteRestore covers A14: soft delete hides a note, include_deleted
// retrieves it, restore brings it back.
func TestA14DeleteRestore(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "删除项目")
	noteID := insertNote(t, ws, projectID, "scratchpad", "回收站", "restore me content", "")
	svc := newSearchService(t, ws, Options{})
	indexEntities(t, svc, projectID, Change{EntityType: EntityNote, EntityID: noteID})

	// The notes service soft-deletes the row; the change signal drives the index.
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	now := clock.Format(clock.System{}.Now())
	if _, err := handle.DB().Exec(`UPDATE notes SET deleted_at = ? WHERE id = ?`, now, noteID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	indexEntities(t, svc, projectID, Change{EntityType: EntityNote, EntityID: noteID, Kind: ChangeDelete})
	scope := Scope{Kind: ScopeProject, ProjectID: projectID}
	if containsDoc(search(t, svc, SearchRequest{Query: "restore", Scope: scope}), DocIDFor(EntityNote, noteID)) {
		t.Fatal("软删除后默认检索不到")
	}
	resp := search(t, svc, SearchRequest{Query: "restore", Scope: scope, IncludeDeleted: true})
	if !findResult(t, resp, DocIDFor(EntityNote, noteID)).Deleted {
		t.Fatal("include_deleted 必须标注删除")
	}

	if _, err := handle.DB().Exec(`UPDATE notes SET deleted_at = NULL WHERE id = ?`, noteID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	indexEntities(t, svc, projectID, Change{EntityType: EntityNote, EntityID: noteID, Kind: ChangeRestore})
	if !containsDoc(search(t, svc, SearchRequest{Query: "restore", Scope: scope}), DocIDFor(EntityNote, noteID)) {
		t.Fatal("恢复后必须再次可检索")
	}
}

// TestA15PDFOverwrite covers A15: an overwritten PDF re-parses and replaces the
// text; an unchanged hash never parses again.
func TestA15PDFOverwrite(t *testing.T) {
	ws := newWorkspace(t)
	paperID := insertPaper(t, ws, "Overwrite Paper", "2020", []byte("placeholder"))
	rel := "library/papers/" + paperID + "/source.pdf"
	abs, err := ws.Paths().Abs(rel)
	if err != nil {
		t.Fatalf("paper path: %v", err)
	}
	oldDoc := PdfDocument{PageCount: 1, Blocks: []PdfBlock{
		{PageNo: 1, BlockNo: 0, Text: "legacy alpha text", CharStart: 0, CharEnd: 16},
	}}
	newDoc := PdfDocument{PageCount: 1, Blocks: []PdfBlock{
		{PageNo: 1, BlockNo: 0, Text: "replacement beta text", CharStart: 0, CharEnd: 21},
	}}
	parser := &fakeParser{docs: map[string]PdfDocument{abs: oldDoc}}
	svc := newSearchService(t, ws, Options{Parser: parser})
	scope := Scope{Kind: ScopeGlobal}
	indexEntities(t, svc, "", Change{EntityType: EntityPaper, EntityID: paperID})
	if got := parser.calls.Load(); got != 1 {
		t.Fatalf("首次索引应解析一次，实际 %d", got)
	}
	if !containsDoc(search(t, svc, SearchRequest{Query: "legacy", Scope: scope}), FullTextDocID(paperID)) {
		t.Fatal("旧文本未命中")
	}

	// Unchanged hash: the stored chunks are reused, no second parse.
	indexEntities(t, svc, "", Change{EntityType: EntityPaper, EntityID: paperID})
	if got := parser.calls.Load(); got != 1 {
		t.Fatalf("哈希未变不应重复解析，实际 %d 次", got)
	}

	// Overwrite the copy and its hash, then re-index.
	parser.docs[abs] = newDoc
	body := []byte("new pdf bytes")
	if err := os.WriteFile(abs, body, 0o644); err != nil {
		t.Fatalf("overwrite pdf: %v", err)
	}
	sum := sha256.Sum256(body)
	if _, err := ws.Global().DB().Exec(
		`UPDATE papers SET file_hash = ?, updated_at = ? WHERE id = ?`,
		hex.EncodeToString(sum[:]), clock.Format(clock.System{}.Now()), paperID); err != nil {
		t.Fatalf("update paper hash: %v", err)
	}
	indexEntities(t, svc, "", Change{EntityType: EntityPaper, EntityID: paperID})
	if got := parser.calls.Load(); got != 2 {
		t.Fatalf("覆盖后应重新解析，实际 %d 次", got)
	}
	if containsDoc(search(t, svc, SearchRequest{Query: "legacy", Scope: scope}), FullTextDocID(paperID)) {
		t.Fatal("旧文本仍可命中")
	}
	if !containsDoc(search(t, svc, SearchRequest{Query: "replacement", Scope: scope}), FullTextDocID(paperID)) {
		t.Fatal("新文本未命中")
	}
}

// TestA16CrashRecovery covers A16: a dirty queue row survives a restart and is
// completed by the next service instance.
func TestA16CrashRecovery(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "恢复项目")
	noteID := insertNote(t, ws, projectID, "scratchpad", "恢复", "crash recovery content", "")
	ctx := context.Background()

	first := newSearchService(t, ws, Options{Debounce: 10 * time.Second})
	if err := first.Notify(ctx, Change{ProjectID: projectID, EntityType: EntityNote, EntityID: noteID}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	if err := first.Close(); err != nil { // simulate the crash before the debounce fired
		t.Fatalf("close: %v", err)
	}

	second := newSearchService(t, ws, Options{})
	if err := second.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := second.Flush(ctx, Scope{Kind: ScopeProject, ProjectID: projectID}); err != nil {
		t.Fatalf("flush: %v", err)
	}
	status := waitIdle(t, second, Scope{Kind: ScopeProject, ProjectID: projectID})
	if status.Docs != 1 || status.Dirty != 0 {
		t.Fatalf("崩溃恢复后索引状态错误: %+v", status)
	}
	resp := search(t, second, SearchRequest{Query: "crash", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	if !containsDoc(resp, DocIDFor(EntityNote, noteID)) {
		t.Fatalf("恢复后内容不可检索: %v", resultDocIDs(resp))
	}
}

// TestA17RebuildIdempotency covers A17: two full rebuilds leave identical doc
// and chunk counts and identical result sets.
func TestA17RebuildIdempotency(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "重建项目")
	noteID := insertNote(t, ws, projectID, "scratchpad", "重建", "rebuild deterministic content", "")
	insertIdea(t, ws, projectID, "重建想法", "rebuild idea body", "Researching", "")
	insertRecord(t, ws, projectID, "重建记录")
	insertExperiment(t, ws, projectID, "重建实验", "outline", "# 计划\n\nrebuild outline")
	insertExperiment(t, ws, projectID, "重建实验2", "results", "# 结果\n\nrebuild results")
	svc := newSearchService(t, ws, Options{})
	ctx := context.Background()
	scope := Scope{Kind: ScopeProject, ProjectID: projectID}

	if _, err := svc.Reindex(ctx, ReindexRequest{Scope: scope}); err != nil {
		t.Fatalf("reindex 1: %v", err)
	}
	first := waitIdle(t, svc, scope)
	firstResp := search(t, svc, SearchRequest{Query: "rebuild", Scope: scope})

	if _, err := svc.Reindex(ctx, ReindexRequest{Scope: scope}); err != nil {
		t.Fatalf("reindex 2: %v", err)
	}
	second := waitIdle(t, svc, scope)
	secondResp := search(t, svc, SearchRequest{Query: "rebuild", Scope: scope})

	if first.Docs != second.Docs || first.Chunks != second.Chunks {
		t.Fatalf("重建不幂等: docs %d/%d chunks %d/%d", first.Docs, second.Docs, first.Chunks, second.Chunks)
	}
	if len(firstResp.Results) == 0 {
		t.Fatal("重建后没有结果")
	}
	if got, want := joinIDs(resultDocIDs(secondResp)), joinIDs(resultDocIDs(firstResp)); got != want {
		t.Fatalf("重建后结果集不一致:\n got %s\nwant %s", got, want)
	}
	// The experiment Outline and Results are separate documents.
	if first.Docs != 5 {
		t.Fatalf("期望 5 个文档，实际 %d", first.Docs)
	}
	if len(firstResp.Results) < 4 {
		t.Fatalf("期望至少 4 个命中，实际 %v", resultDocIDs(firstResp))
	}
	var experimentID string
	if err := handleDB(t, ws, projectID).QueryRow(`SELECT id FROM experiments WHERE title = '重建实验'`).Scan(&experimentID); err != nil {
		t.Fatalf("experiment id: %v", err)
	}
	if !containsDoc(firstResp, ExperimentDocID(experimentID, SourceExperimentOutline)) {
		t.Fatalf("实验 Outline 未索引: %v", resultDocIDs(firstResp))
	}
	_ = noteID
}

// handleDB returns the project database.
func handleDB(t *testing.T, ws interface {
	Project(ctx context.Context, id string) (*workspace.ProjectHandle, error)
}, projectID string) *sql.DB {
	t.Helper()
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	return handle.DB()
}

// joinIDs joins ids for comparison.
func joinIDs(ids []string) string {
	body, _ := json.Marshal(ids)
	return string(body)
}

// TestIndexRetryBackoff covers the three-attempt retry and terminal failure:
// dropping the FTS table makes every write fail deterministically.
func TestIndexRetryBackoff(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "重试项目")
	noteID := insertNote(t, ws, projectID, "scratchpad", "重试", "retry content", "")
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	if _, err := handle.DB().Exec(`DROP TABLE index_chunks_fts`); err != nil {
		t.Fatalf("drop fts: %v", err)
	}
	svc := newSearchService(t, ws, Options{MaxAttempts: 3, Backoff: []time.Duration{time.Millisecond}})
	ctx := context.Background()
	if err := svc.Notify(ctx, Change{ProjectID: projectID, EntityType: EntityNote, EntityID: noteID}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	scope := Scope{Kind: ScopeProject, ProjectID: projectID}
	for range 4 {
		if err := svc.Flush(ctx, scope); err != nil {
			t.Fatalf("flush: %v", err)
		}
	}
	var attempts int
	if err := handle.DB().QueryRow(`SELECT attempts FROM index_dirty WHERE entity_id = ?`, noteID).Scan(&attempts); err != nil {
		t.Fatalf("dirty row: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("重试次数应为 3，实际 %d", attempts)
	}
	status, err := svc.Status(ctx, scope)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Failed < 1 || status.LastError == "" {
		t.Fatalf("失败状态不可见: %+v", status)
	}
}

// TestVectorPendingDegradation covers the keyword-ok/vector-failed rule.
func TestVectorPendingDegradation(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "向量降级")
	noteID := insertNote(t, ws, projectID, "scratchpad", "降级", "keyword still works", "")
	svc := newSearchService(t, ws, Options{})
	indexEntities(t, svc, projectID, Change{EntityType: EntityNote, EntityID: noteID})

	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	var status string
	if err := handle.DB().QueryRow(`SELECT index_status FROM index_docs WHERE doc_id = ?`,
		DocIDFor(EntityNote, noteID)).Scan(&status); err != nil {
		t.Fatalf("doc status: %v", err)
	}
	if status != StatusVectorPending {
		t.Fatalf("未配置 embedding 时应为 vector_pending，实际 %q", status)
	}
	if !containsDoc(search(t, svc, SearchRequest{Query: "keyword", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}}),
		DocIDFor(EntityNote, noteID)) {
		t.Fatal("向量缺失不影响关键词检索")
	}
}

// TestStaleReconciliation covers version reconciliation: a document written by
// an older parser version is marked stale and re-indexed by Start.
func TestStaleReconciliation(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "对账项目")
	noteID := insertNote(t, ws, projectID, "scratchpad", "对账", "stale reconciliation content", "")
	svc := newSearchService(t, ws, Options{})
	indexEntities(t, svc, projectID, Change{EntityType: EntityNote, EntityID: noteID})
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	if _, err := handle.DB().Exec(
		`UPDATE index_docs SET parser_version = 'md-0' WHERE doc_id = ?`, DocIDFor(EntityNote, noteID)); err != nil {
		t.Fatalf("age doc: %v", err)
	}
	ctx := context.Background()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	var status string
	if err := handle.DB().QueryRow(`SELECT index_status FROM index_docs WHERE doc_id = ?`,
		DocIDFor(EntityNote, noteID)).Scan(&status); err != nil {
		t.Fatalf("doc status: %v", err)
	}
	if status != StatusStale {
		t.Fatalf("版本不一致应标记 stale，实际 %q", status)
	}
	if report, err := svc.Status(ctx, Scope{Kind: ScopeProject, ProjectID: projectID}); err != nil || !report.Stale {
		t.Fatalf("Status.Stale 应为 true: %+v err=%v", report, err)
	}
	if err := svc.Flush(ctx, Scope{Kind: ScopeProject, ProjectID: projectID}); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := handle.DB().QueryRow(`SELECT index_status FROM index_docs WHERE doc_id = ?`,
		DocIDFor(EntityNote, noteID)).Scan(&status); err != nil {
		t.Fatalf("doc status: %v", err)
	}
	if status != StatusVectorPending && status != StatusOK {
		t.Fatalf("重新索引后状态应恢复，实际 %q", status)
	}
}

// TestNoTextLayer covers the scan-only PDF degradation.
func TestNoTextLayer(t *testing.T) {
	ws := newWorkspace(t)
	paperID := insertPaper(t, ws, "Scan Only", "2019", []byte("pdf"))
	abs, err := ws.Paths().Abs("library/papers/" + paperID + "/source.pdf")
	if err != nil {
		t.Fatalf("paper path: %v", err)
	}
	parser := &fakeParser{err: WrapError(CodeNoTextLayer, "PDF 没有文本层", ErrNoTextLayer)}
	svc := newSearchService(t, ws, Options{Parser: parser})
	indexEntities(t, svc, "", Change{EntityType: EntityPaper, EntityID: paperID})
	var status string
	if err := ws.Global().DB().QueryRow(`SELECT index_status FROM paper_index_docs WHERE doc_id = ?`,
		FullTextDocID(paperID)).Scan(&status); err != nil {
		t.Fatalf("fulltext status: %v", err)
	}
	if status != StatusNoText {
		t.Fatalf("无文本层应标 no_text，实际 %q", status)
	}
	if containsDoc(search(t, svc, SearchRequest{Query: "scan", Scope: Scope{Kind: ScopeGlobal}}), FullTextDocID(paperID)) {
		t.Fatal("无文本层文档不该有全文命中")
	}
	_ = abs
}
