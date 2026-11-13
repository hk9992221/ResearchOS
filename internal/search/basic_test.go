package search

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"researchos/internal/ai/gateway"
)

// TestA1ChineseFullText covers A1: a 2–4 rune Chinese query hits a Scratchpad
// note after indexing and the snippet highlights the matched runes.
func TestA1ChineseFullText(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "检索项目")
	noteID := insertNote(t, ws, projectID, "scratchpad", "调度实验",
		"# 方法\n\n我们研究操作系统的调度算法，重点关注响应时间与公平性。\n\n## 结果\n\n吞吐量提升明显，延迟下降。", "")
	svc := newSearchService(t, ws, Options{})
	indexEntities(t, svc, projectID, Change{EntityType: EntityNote, EntityID: noteID})

	resp := search(t, svc, SearchRequest{Query: "调度", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	if !containsDoc(resp, DocIDFor(EntityNote, noteID)) {
		t.Fatalf("中文检索未命中，结果=%v", resultDocIDs(resp))
	}
	result := findResult(t, resp, DocIDFor(EntityNote, noteID))
	if len(result.Snippets) == 0 {
		t.Fatal("没有片段")
	}
	if len(result.Snippets[0].Highlights) == 0 {
		t.Fatalf("片段没有高亮: %+v", result.Snippets[0])
	}
	if !strings.Contains(result.Snippets[0].Text, "调度") {
		t.Fatalf("片段未包含命中词: %q", result.Snippets[0].Text)
	}
	hl := result.Snippets[0].Highlights[0]
	runes := []rune(result.Snippets[0].Text)
	if hl.End > len(runes) || string(runes[hl.Start:hl.End]) != "调度" {
		t.Fatalf("高亮区间错误: %+v 文本 %q", hl, result.Snippets[0].Text)
	}
	if result.Locator.Heading == "" {
		t.Fatalf("缺少标题路径定位: %+v", result.Locator)
	}
}

// TestA2EnglishMixed covers A2: English terms hit in mixed documents and match
// case-insensitively.
func TestA2EnglishMixed(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "混合项目")
	noteID := insertNote(t, ws, projectID, "scratchpad", "Hybrid",
		"Transformer architectures improve retrieval quality.\n\n混合检索结合 BM25 与向量召回。", "")
	svc := newSearchService(t, ws, Options{})
	indexEntities(t, svc, projectID, Change{EntityType: EntityNote, EntityID: noteID})

	for _, query := range []string{"transformer", "TRANSFORMER", "bm25"} {
		resp := search(t, svc, SearchRequest{Query: query, Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
		if !containsDoc(resp, DocIDFor(EntityNote, noteID)) {
			t.Fatalf("查询 %q 未命中，结果=%v", query, resultDocIDs(resp))
		}
	}
}

// TestA3Semantic covers A3: a synonym query with no shared keyword still hits
// through vector recall when embedding is configured.
func TestA3Semantic(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "语义项目")
	noteID := insertNote(t, ws, projectID, "scratchpad", "动物观察",
		"the cat sat quietly on the warm mat", "")
	embedder := &fakeEmbedder{model: "fake-sem", groups: [][]string{{"feline", "cat"}}}
	svc := newSearchService(t, ws, Options{Embedding: embedder})
	indexEntities(t, svc, projectID, Change{EntityType: EntityNote, EntityID: noteID})

	resp := search(t, svc, SearchRequest{Query: "feline", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	if !containsDoc(resp, DocIDFor(EntityNote, noteID)) {
		t.Fatalf("近义查询未命中，结果=%v degraded=%v", resultDocIDs(resp), resp.Degraded)
	}
	result := findResult(t, resp, DocIDFor(EntityNote, noteID))
	if result.Score.VectorRank == 0 {
		t.Fatalf("语义命中缺少向量排名: %+v", result.Score)
	}
	if result.Score.KeywordRank != 0 {
		t.Fatalf("不应有无关键词命中: %+v", result.Score)
	}
}

// TestA4HybridFusion covers A4: keyword-only and vector-only documents both
// appear, with the RRF breakdown recorded.
func TestA4HybridFusion(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "融合项目")
	keywordNote := insertNote(t, ws, projectID, "scratchpad", "优化",
		"optimization algorithms converge quickly", "")
	vectorNote := insertNote(t, ws, projectID, "scratchpad", "鸟类",
		"observational studies of bird migration", "")
	embedder := &fakeEmbedder{
		model:  "fake-mix",
		groups: [][]string{{"optimization", "bird"}},
	}
	svc := newSearchService(t, ws, Options{Embedding: embedder})
	indexEntities(t, svc, projectID,
		Change{EntityType: EntityNote, EntityID: keywordNote},
		Change{EntityType: EntityNote, EntityID: vectorNote})

	resp := search(t, svc, SearchRequest{Query: "optimization", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	kw := findResult(t, resp, DocIDFor(EntityNote, keywordNote))
	vec := findResult(t, resp, DocIDFor(EntityNote, vectorNote))
	if kw.Score.KeywordRank == 0 {
		t.Fatalf("关键词文档缺少关键词排名: %+v", kw.Score)
	}
	if vec.Score.VectorRank == 0 {
		t.Fatalf("向量文档缺少向量排名: %+v", vec.Score)
	}
	if kw.Score.Fusion <= 0 || vec.Score.Fusion <= 0 {
		t.Fatalf("融合分数必须为正: kw=%+v vec=%+v", kw.Score, vec.Score)
	}
	// RRF is recomputable from the reported ranks (k = 60).
	want := 0.0
	if kw.Score.KeywordRank > 0 {
		want += 1.0 / float64(60+kw.Score.KeywordRank)
	}
	if kw.Score.VectorRank > 0 {
		want += 1.0 / float64(60+kw.Score.VectorRank)
	}
	if diff := kw.Score.Fusion - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("融合分不可复算: got %v want %v", kw.Score.Fusion, want)
	}
}

// TestA5RerankDegradation covers A5: an unsupported rerank provider returns
// results in fusion order with the degradation reason and no error.
func TestA5RerankDegradation(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "重排项目")
	for _, body := range []string{"alpha retrieval content", "beta retrieval content"} {
		insertNote(t, ws, projectID, "scratchpad", "笔记", body, "")
	}

	rerankSvc := newSearchService(t, ws, Options{Rerank: &fakeReranker{err: unsupportedRerankError()}})
	// Reindex through the rerank service so both services see the same index.
	if _, err := rerankSvc.Reindex(context.Background(), ReindexRequest{Scope: Scope{Kind: ScopeProject, ProjectID: projectID}}); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	waitIdle(t, rerankSvc, Scope{Kind: ScopeProject, ProjectID: projectID})

	resp := search(t, rerankSvc, SearchRequest{Query: "retrieval", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	if len(resp.Results) != 2 {
		t.Fatalf("结果数不对: %v", resultDocIDs(resp))
	}
	if !hasDegrade(resp, DegradeRerankUnavailable) {
		t.Fatalf("缺少 rerank_unavailable: %v", resp.Degraded)
	}
	for _, result := range resp.Results {
		if result.Score.Rerank != nil {
			t.Fatalf("降级时不应有 rerank 分数: %+v", result.Score)
		}
	}
	// Force mode without any configured reranker reports the same degradation.
	force := search(t, newSearchService(t, ws, Options{}), SearchRequest{
		Query: "retrieval", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}, Rerank: RerankForce,
	})
	if !hasDegrade(force, DegradeRerankUnavailable) {
		t.Fatalf("force 模式缺少 rerank_unavailable: %v", force.Degraded)
	}
}

// TestA8ProjectIsolation covers A8: a project search never returns another
// project's documents.
func TestA8ProjectIsolation(t *testing.T) {
	ws := newWorkspace(t)
	projectA := createProject(t, ws, "项目A")
	projectB := createProject(t, ws, "项目B")
	noteA := insertNote(t, ws, projectA, "scratchpad", "A", "unique shared term in project A", "")
	noteB := insertNote(t, ws, projectB, "scratchpad", "B", "unique shared term in project B", "")
	svc := newSearchService(t, ws, Options{})
	indexEntities(t, svc, projectA, Change{EntityType: EntityNote, EntityID: noteA})
	indexEntities(t, svc, projectB, Change{EntityType: EntityNote, EntityID: noteB})

	resp := search(t, svc, SearchRequest{Query: "shared", Scope: Scope{Kind: ScopeProject, ProjectID: projectA}})
	if !containsDoc(resp, DocIDFor(EntityNote, noteA)) {
		t.Fatalf("项目A 未命中自己的文档: %v", resultDocIDs(resp))
	}
	if containsDoc(resp, DocIDFor(EntityNote, noteB)) {
		t.Fatalf("项目A 返回了项目B 的文档: %v", resultDocIDs(resp))
	}
}

// TestA9NoAI covers A9: without embedding/rerank the keyword search still works
// and reports the degradation.
func TestA9NoAI(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "离线项目")
	noteID := insertNote(t, ws, projectID, "scratchpad", "离线", "离线关键词检索必须可用", "")
	svc := newSearchService(t, ws, Options{})
	indexEntities(t, svc, projectID, Change{EntityType: EntityNote, EntityID: noteID})

	resp := search(t, svc, SearchRequest{Query: "关键词", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	if !containsDoc(resp, DocIDFor(EntityNote, noteID)) {
		t.Fatalf("离线检索未命中: %v", resultDocIDs(resp))
	}
	if !hasDegrade(resp, DegradeVectorUnavailable) {
		t.Fatalf("缺少 vector_unavailable: %v", resp.Degraded)
	}
}

// TestA12SourceReference covers A12: every result carries an entity and a jump
// target, and a deleted document is still reachable with the deleted marker.
func TestA12SourceReference(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "来源项目")
	noteID := insertNote(t, ws, projectID, "scratchpad", "来源笔记", "来源引用的正文", "")
	svc := newSearchService(t, ws, Options{})
	indexEntities(t, svc, projectID, Change{EntityType: EntityNote, EntityID: noteID})

	resp := search(t, svc, SearchRequest{Query: "来源引用", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	result := findResult(t, resp, DocIDFor(EntityNote, noteID))
	if result.Target.Kind != "note" || result.Target.EntityID != noteID || result.Target.ProjectID != projectID {
		t.Fatalf("跳转目标不完整: %+v", result.Target)
	}
	if result.ProjectName != "来源项目" {
		t.Fatalf("项目名缺失: %q", result.ProjectName)
	}

	// Soft delete: hidden by default, visible with include_deleted and marked.
	indexEntities(t, svc, projectID, Change{EntityType: EntityNote, EntityID: noteID, Kind: ChangeDelete})
	resp = search(t, svc, SearchRequest{Query: "来源引用", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	if containsDoc(resp, DocIDFor(EntityNote, noteID)) {
		t.Fatal("软删除文档默认不应出现")
	}
	resp = search(t, svc, SearchRequest{
		Query: "来源引用", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}, IncludeDeleted: true,
	})
	deleted := findResult(t, resp, DocIDFor(EntityNote, noteID))
	if !deleted.Deleted {
		t.Fatal("include_deleted 结果必须标注已删除")
	}
	if deleted.Target.EntityID != noteID {
		t.Fatalf("已删除结果仍要定位到实体: %+v", deleted.Target)
	}

	target, err := svc.ResolveTarget(context.Background(), EntityRef{
		ProjectID: projectID, EntityType: EntityNote, EntityID: noteID,
	}, Locator{Heading: "来源笔记"})
	if err != nil {
		t.Fatalf("resolve target: %v", err)
	}
	if target.Kind != "note" || target.Locator.Heading != "来源笔记" {
		t.Fatalf("解析的跳转目标错误: %+v", target)
	}
}

// TestRerankReorders covers the successful rerank path: configured providers
// reorder the top documents and the score is exposed.
func TestRerankReorders(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "重排成功")
	for _, body := range []string{"alpha rerank content", "beta rerank content"} {
		insertNote(t, ws, projectID, "scratchpad", "笔记", body, "")
	}
	plain := newSearchService(t, ws, Options{})
	ctx := context.Background()
	if _, err := plain.Reindex(ctx, ReindexRequest{Scope: Scope{Kind: ScopeProject, ProjectID: projectID}}); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	waitIdle(t, plain, Scope{Kind: ScopeProject, ProjectID: projectID})
	base := search(t, plain, SearchRequest{Query: "rerank", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}, Rerank: RerankOff})
	if len(base.Results) != 2 {
		t.Fatalf("结果数错误: %v", resultDocIDs(base))
	}
	reranked := newSearchService(t, ws, Options{Rerank: &fakeReranker{order: func(query string, docs []string) []int {
		out := make([]int, 0, len(docs))
		for i := len(docs) - 1; i >= 0; i-- {
			out = append(out, i)
		}
		return out
	}}})
	got := search(t, reranked, SearchRequest{Query: "rerank", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	if len(got.Results) != 2 {
		t.Fatalf("重排后结果数错误: %v", resultDocIDs(got))
	}
	if got.Results[0].DocID != base.Results[1].DocID {
		t.Fatalf("重排未生效: got %v base %v", resultDocIDs(got), resultDocIDs(base))
	}
	if got.Results[0].Score.Rerank == nil {
		t.Fatalf("缺少 rerank 分数: %+v", got.Results[0].Score)
	}
	if hasDegrade(got, DegradeRerankUnavailable) {
		t.Fatalf("成功的重排不应降级: %v", got.Degraded)
	}
}

// blockingReranker blocks until the timeout fires.
type blockingReranker struct{}

// Rerank implements gateway.RerankService.
func (blockingReranker) Rerank(ctx context.Context, query string, docs []string, topN int) (gateway.RerankResponse, error) {
	<-ctx.Done()
	return gateway.RerankResponse{}, ctx.Err()
}

// TestRerankTimeout covers the 800ms-style rerank timeout degradation.
func TestRerankTimeout(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "重排超时")
	noteID := insertNote(t, ws, projectID, "scratchpad", "超时", "timeout rerank content", "")
	svc := newSearchService(t, ws, Options{Rerank: blockingReranker{}, RerankTimeout: 20 * time.Millisecond})
	indexEntities(t, svc, projectID, Change{EntityType: EntityNote, EntityID: noteID})
	resp := search(t, svc, SearchRequest{Query: "timeout", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	if !hasDegrade(resp, DegradeRerankTimeout) {
		t.Fatalf("缺少 rerank_timeout: %v", resp.Degraded)
	}
	if !containsDoc(resp, DocIDFor(EntityNote, noteID)) {
		t.Fatalf("重排超时不应丢结果: %v", resultDocIDs(resp))
	}
}

// TestA20ServiceConsistency covers A20 (service layer): the same query returns
// the same result set twice and writes no Activity (MCP registers the same
// service call in SPEC-11).
func TestA20ServiceConsistency(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "一致性项目")
	noteID := insertNote(t, ws, projectID, "scratchpad", "一致", "identical query results", "")
	svc := newSearchService(t, ws, Options{})
	indexEntities(t, svc, projectID, Change{EntityType: EntityNote, EntityID: noteID})
	before := activityCount(t, ws, projectID)

	req := SearchRequest{Query: "identical", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}}
	first := search(t, svc, req)
	second := search(t, svc, req)
	if strings.Join(resultDocIDs(first), "|") != strings.Join(resultDocIDs(second), "|") {
		t.Fatalf("同一查询结果不一致: %v vs %v", resultDocIDs(first), resultDocIDs(second))
	}
	if first.Total != second.Total || first.IndexEpoch != second.IndexEpoch {
		t.Fatalf("同一查询统计不一致: %+v vs %+v", first, second)
	}
	if after := activityCount(t, ws, projectID); after != before {
		t.Fatalf("检索写入了 Activity: before=%d after=%d (%v)", before, after, activityTypes(t, ws, projectID))
	}
}

// TestFiltersAndBrowse covers source filters, paper metadata filters and the
// empty-query browse mode.
func TestFiltersAndBrowse(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "过滤项目")
	noteID := insertNote(t, ws, projectID, "scratchpad", "笔记", "browse target content", "")
	ideaID := insertIdea(t, ws, projectID, "想法", "browse target idea", "New", "")
	paperID := insertPaper(t, ws, "Filtering Paper", "2021", nil)
	svc := newSearchService(t, ws, Options{})
	indexEntities(t, svc, projectID,
		Change{EntityType: EntityNote, EntityID: noteID},
		Change{EntityType: EntityIdea, EntityID: ideaID})
	indexEntities(t, svc, "", Change{EntityType: EntityPaper, EntityID: paperID})

	resp := search(t, svc, SearchRequest{
		Query: "browse", Scope: Scope{Kind: ScopeProject, ProjectID: projectID},
		Filters: Filters{Sources: []SourceType{SourceIdea}},
	})
	if len(resp.Results) != 1 || resp.Results[0].Source != SourceIdea {
		t.Fatalf("来源过滤失败: %+v", resultDocIDs(resp))
	}

	// Paper metadata filters apply to the global literature index.
	minYear := 2020
	resp = search(t, svc, SearchRequest{
		Query: "Filtering", Scope: Scope{Kind: ScopeGlobal},
		Filters: Filters{YearMin: &minYear, Authors: []string{"lovelace"}},
	})
	if !containsDoc(resp, DocIDFor(EntityPaper, paperID)) {
		t.Fatalf("文献过滤未命中: %v", resultDocIDs(resp))
	}
	maxYear := 2019
	resp = search(t, svc, SearchRequest{
		Query: "Filtering", Scope: Scope{Kind: ScopeGlobal},
		Filters: Filters{YearMax: &maxYear},
	})
	if len(resp.Results) != 0 {
		t.Fatalf("年份过滤失效: %v", resultDocIDs(resp))
	}

	// Browse mode: empty query, updated_at descending, paginated.
	browse := search(t, svc, SearchRequest{Scope: Scope{Kind: ScopeProject, ProjectID: projectID}, Limit: 1})
	if len(browse.Results) != 1 {
		t.Fatalf("浏览模式分页失败: %+v", browse)
	}
	if browse.Total < 2 || !browse.HasMore {
		t.Fatalf("浏览模式统计错误: total=%d hasMore=%v", browse.Total, browse.HasMore)
	}
	second := search(t, svc, SearchRequest{Scope: Scope{Kind: ScopeProject, ProjectID: projectID}, Limit: 1, Offset: 1})
	if len(second.Results) != 1 || second.Results[0].DocID == browse.Results[0].DocID {
		t.Fatalf("浏览分页/offset 错误: %+v vs %+v", browse.Results, second.Results)
	}
	if second.Total != browse.Total {
		t.Fatalf("offset 改变了 Total: %d vs %d", second.Total, browse.Total)
	}
	all := search(t, svc, SearchRequest{Scope: Scope{Kind: ScopeProject, ProjectID: projectID}, Limit: 100})
	for i := 1; i < len(all.Results); i++ {
		if all.Results[i-1].UpdatedAt.Before(all.Results[i].UpdatedAt) {
			t.Fatalf("浏览模式未按 updated_at 降序")
		}
	}
	if all.Results[0].UpdatedAt.IsZero() {
		t.Fatal("浏览结果缺少时间")
	}
}

// TestA18ZeroSideEffects covers A18: repeated searches change no Activity, no
// entity updated_at and no relation.
func TestA18ZeroSideEffects(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "零副作用项目")
	noteID := insertNote(t, ws, projectID, "scratchpad", "零副作用", "side effect free query", "")
	svc := newSearchService(t, ws, Options{})
	indexEntities(t, svc, projectID, Change{EntityType: EntityNote, EntityID: noteID})

	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	var updatedBefore string
	if err := handle.DB().QueryRow(`SELECT updated_at FROM notes WHERE id = ?`, noteID).Scan(&updatedBefore); err != nil {
		t.Fatalf("note updated_at: %v", err)
	}
	activitiesBefore := len(activityTypes(t, ws, projectID))
	relationsBefore := countRows(t, handle.DB(), `SELECT COUNT(*) FROM entity_relations`)

	for range 20 {
		search(t, svc, SearchRequest{Query: "side effect", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	}
	search(t, svc, SearchRequest{Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})

	var updatedAfter string
	if err := handle.DB().QueryRow(`SELECT updated_at FROM notes WHERE id = ?`, noteID).Scan(&updatedAfter); err != nil {
		t.Fatalf("note updated_at: %v", err)
	}
	if updatedAfter != updatedBefore {
		t.Fatalf("检索修改了实体 updated_at: %s -> %s", updatedBefore, updatedAfter)
	}
	if got := len(activityTypes(t, ws, projectID)); got != activitiesBefore {
		t.Fatalf("检索写入了 Activity: %v", activityTypes(t, ws, projectID))
	}
	if got := countRows(t, handle.DB(), `SELECT COUNT(*) FROM entity_relations`); got != relationsBefore {
		t.Fatalf("检索写入了关系: %d -> %d", relationsBefore, got)
	}
}

// TestOpenTargetPaper checks the external-open default and the page-number
// fallback without touching any entity.
func TestOpenTargetPaper(t *testing.T) {
	ws := newWorkspace(t)
	pdf := buildPDF([]string{"open target page one"}, []string{"open target page two"})
	paperID := insertPaper(t, ws, "Open Target", "2022", pdf)
	var opened string
	svc := newSearchService(t, ws, Options{
		Opener: func(ctx context.Context, target string) error {
			opened = target
			return nil
		},
	})
	scope := Scope{Kind: ScopeGlobal}
	if err := svc.Flush(context.Background(), scope); err != nil {
		t.Fatalf("flush: %v", err)
	}
	indexEntities(t, svc, "", Change{EntityType: EntityPaper, EntityID: paperID})

	resp := search(t, svc, SearchRequest{Query: "page two", Scope: scope})
	var result *SearchResult
	for i := range resp.Results {
		if resp.Results[i].Source == SourcePaperFullText {
			result = &resp.Results[i]
		}
	}
	if result == nil {
		t.Fatalf("全文结果缺失: %v", resultDocIDs(resp))
	}
	if result.Locator.PageNo != 2 {
		t.Fatalf("页码错误: %+v", result.Locator)
	}
	if result.Target.PDFPath == "" {
		t.Fatal("缺少 PDF 路径")
	}
	if err := svc.OpenTarget(context.Background(), result.Target); err != nil {
		t.Fatalf("open target: %v", err)
	}
	if opened != result.Target.PDFPath {
		t.Fatalf("opener 未收到 PDF 路径: %q", opened)
	}
}

// TestResolveTargetMissing covers the target-not-found contract.
func TestResolveTargetMissing(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "目标项目")
	svc := newSearchService(t, ws, Options{})
	_, err := svc.ResolveTarget(context.Background(), EntityRef{
		ProjectID: projectID, EntityType: EntityNote, EntityID: mustID(t),
	}, Locator{})
	if err == nil {
		t.Fatal("不存在的目标必须报错")
	}
	var searchErr *Error
	if !errors.As(err, &searchErr) || searchErr.Code != CodeTargetNotFound {
		t.Fatalf("错误码错误: %v", err)
	}
}
