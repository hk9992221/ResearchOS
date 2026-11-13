package search

import (
	"context"
	"testing"
	"time"
)

// TestA10GlobalFederation covers A10: global search returns projects (archived
// ones flagged) plus global literature, and project search is limited to the
// project's linked papers.
func TestA10GlobalFederation(t *testing.T) {
	ws := newWorkspace(t)
	projectA := createProject(t, ws, "联邦A")
	projectB := createProject(t, ws, "联邦B")
	archiveProject(t, ws, projectB)
	noteA := insertNote(t, ws, projectA, "scratchpad", "A", "shared federation keyword alpha", "")
	noteB := insertNote(t, ws, projectB, "scratchpad", "B", "shared federation keyword beta", "")
	linkedPaper := insertPaper(t, ws, "Shared Linked Paper", "2023", buildPDF([]string{"shared linked fulltext"}))
	unlinkedPaper := insertPaper(t, ws, "Shared Unlinked Paper", "2023", buildPDF([]string{"shared unlinked fulltext"}))
	linkPaper(t, ws, projectA, linkedPaper)
	svc := newSearchService(t, ws, Options{})
	indexEntities(t, svc, projectA, Change{EntityType: EntityNote, EntityID: noteA})
	indexEntities(t, svc, projectB, Change{EntityType: EntityNote, EntityID: noteB})
	indexEntities(t, svc, "", Change{EntityType: EntityPaper, EntityID: linkedPaper})
	indexEntities(t, svc, "", Change{EntityType: EntityPaper, EntityID: unlinkedPaper})

	global := search(t, svc, SearchRequest{Query: "shared", Scope: Scope{Kind: ScopeGlobal}, Limit: 100})
	if !containsDoc(global, DocIDFor(EntityNote, noteA)) || !containsDoc(global, DocIDFor(EntityNote, noteB)) {
		t.Fatalf("全局检索缺少项目结果: %v", resultDocIDs(global))
	}
	archived := findResult(t, global, DocIDFor(EntityNote, noteB))
	if !archived.Archived {
		t.Fatal("归档项目结果必须标注 Archived")
	}
	if findResult(t, global, DocIDFor(EntityNote, noteA)).Archived {
		t.Fatal("活跃项目不应标注 Archived")
	}
	if !containsDoc(global, FullTextDocID(linkedPaper)) || !containsDoc(global, FullTextDocID(unlinkedPaper)) {
		t.Fatalf("全局检索缺少文献全文: %v", resultDocIDs(global))
	}

	// Project scope sees its own docs plus its linked literature only.
	project := search(t, svc, SearchRequest{Query: "shared", Scope: Scope{Kind: ScopeProject, ProjectID: projectA}, Limit: 100})
	if containsDoc(project, DocIDFor(EntityNote, noteB)) {
		t.Fatalf("项目检索返回了其他项目文档: %v", resultDocIDs(project))
	}
	if !containsDoc(project, FullTextDocID(linkedPaper)) {
		t.Fatalf("项目检索缺少关联文献: %v", resultDocIDs(project))
	}
	if containsDoc(project, FullTextDocID(unlinkedPaper)) || containsDoc(project, DocIDFor(EntityPaper, unlinkedPaper)) {
		t.Fatalf("项目检索返回了未关联文献: %v", resultDocIDs(project))
	}
}

// TestPaperGroupMerge covers the paper metadata/full-text group merge.
func TestPaperGroupMerge(t *testing.T) {
	ws := newWorkspace(t)
	paperID := insertPaper(t, ws, "Grouped Paper Title", "2024", buildPDF([]string{"grouped body text"}))
	svc := newSearchService(t, ws, Options{})
	indexEntities(t, svc, "", Change{EntityType: EntityPaper, EntityID: paperID})
	resp := search(t, svc, SearchRequest{Query: "grouped", Scope: Scope{Kind: ScopeGlobal}})
	if len(resp.Results) != 1 {
		t.Fatalf("同一 Paper 的元数据与全文应合并为一条: %v", resultDocIDs(resp))
	}
	result := resp.Results[0]
	if result.Source != SourcePaperFullText {
		t.Fatalf("全文命中应优先: %+v", result.Source)
	}
	if len(result.Snippets) == 0 || result.Locator.PageNo != 1 {
		t.Fatalf("合并结果缺少全文定位: %+v", result)
	}
	// A metadata-only query keeps the metadata document as the group source.
	meta := search(t, svc, SearchRequest{Query: "Grouped Paper Title", Scope: Scope{Kind: ScopeGlobal}})
	if len(meta.Results) == 0 || meta.Results[0].Target.Kind != "paper" {
		t.Fatalf("元数据查询失败: %+v", meta.Results)
	}
}

// TestA11FederationTimeout covers A11: one slow project yields a partial answer
// with project_timeout while the others return.
func TestA11FederationTimeout(t *testing.T) {
	ws := newWorkspace(t)
	fastA := createProject(t, ws, "快速A")
	slow := createProject(t, ws, "慢速B")
	fastC := createProject(t, ws, "快速C")
	for projectID, body := range map[string]string{
		fastA: "timeout federation alpha",
		slow:  "timeout federation slow",
		fastC: "timeout federation gamma",
	} {
		noteID := insertNote(t, ws, projectID, "scratchpad", "t", body, "")
		_ = noteID
	}
	svc := newSearchService(t, ws, Options{
		ProjectBudget: func(projectID string) time.Duration {
			if projectID == slow {
				return 0 // budget exhausted: deterministic timeout
			}
			return 2 * time.Second
		},
	})
	ctx := context.Background()
	// Index each project through its own reindex job.
	for _, projectID := range []string{fastA, slow, fastC} {
		if _, err := svc.Reindex(ctx, ReindexRequest{Scope: Scope{Kind: ScopeProject, ProjectID: projectID}}); err != nil {
			t.Fatalf("reindex %s: %v", projectID, err)
		}
		waitIdle(t, svc, Scope{Kind: ScopeProject, ProjectID: projectID})
	}

	start := time.Now()
	resp := search(t, svc, SearchRequest{Query: "timeout", Scope: Scope{Kind: ScopeGlobal}, Limit: 100})
	elapsed := time.Since(start)
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("联邦检索超出预算: %v", elapsed)
	}
	if !resp.Partial {
		t.Fatal("单项目超时必须标记 Partial")
	}
	if !hasDegrade(resp, DegradeProjectTimeout) {
		t.Fatalf("缺少 project_timeout: %v", resp.Degraded)
	}
	if len(resp.Results) == 0 {
		t.Fatal("其余项目结果必须返回")
	}
	for _, result := range resp.Results {
		if result.ProjectID == slow {
			t.Fatalf("超时项目不应有结果: %+v", result)
		}
	}
}
