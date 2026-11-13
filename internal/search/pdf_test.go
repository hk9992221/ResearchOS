package search

import (
	"context"
	"errors"
	"strings"
	"testing"

	"researchos/internal/foundation/workspace"
)

// TestPDFParserPages covers the real pure-Go parser: page count, per-page
// blocks, rune offsets and paragraph grouping.
func TestPDFParserPages(t *testing.T) {
	ws := newWorkspace(t)
	pdf := buildPDF(
		[]string{"First page alpha text.", "Second paragraph beta text."},
		[]string{"Second page gamma text."},
		[]string{"Third page delta text."},
	)
	abs := writeFile(t, ws, "library/papers/test/source.pdf", pdf)
	doc, err := GoPDFParser{}.Parse(context.Background(), abs, ParseOptions{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.PageCount != 3 {
		t.Fatalf("页数错误: %d", doc.PageCount)
	}
	byPage := map[int][]PdfBlock{}
	for _, block := range doc.Blocks {
		byPage[block.PageNo] = append(byPage[block.PageNo], block)
	}
	if len(byPage[1]) != 2 {
		t.Fatalf("第 1 页应有 2 段，实际 %d: %+v", len(byPage[1]), byPage[1])
	}
	if !strings.Contains(byPage[2][0].Text, "gamma") || byPage[2][0].PageNo != 2 {
		t.Fatalf("第 2 页文本/页码错误: %+v", byPage[2][0])
	}
	if byPage[1][1].CharStart <= byPage[1][0].CharEnd {
		t.Fatalf("字符偏移未推进: %+v %+v", byPage[1][0], byPage[1][1])
	}
	// Paging is resumable: StartPage 2 returns pages 2..3 only.
	partial, err := GoPDFParser{}.Parse(context.Background(), abs, ParseOptions{StartPage: 2, MaxPage: 1})
	if err != nil {
		t.Fatalf("resume parse: %v", err)
	}
	for _, block := range partial.Blocks {
		if block.PageNo != 2 {
			t.Fatalf("断点续做返回了第 %d 页", block.PageNo)
		}
	}
}

// TestPDFParserNoTextLayer covers the scan-only PDF contract.
func TestPDFParserNoTextLayer(t *testing.T) {
	ws := newWorkspace(t)
	pdf := buildPDF([]string{})
	abs := writeFile(t, ws, "library/papers/scan/source.pdf", pdf)
	_, err := GoPDFParser{}.Parse(context.Background(), abs, ParseOptions{})
	if err == nil {
		t.Fatal("空 PDF 必须返回错误")
	}
	if !errors.Is(err, ErrNoTextLayer) {
		t.Fatalf("错误应为 ErrNoTextLayer: %v", err)
	}
	var searchErr *Error
	if !errors.As(err, &searchErr) || searchErr.Code != CodeNoTextLayer {
		t.Fatalf("错误码应为 %s: %v", CodeNoTextLayer, err)
	}
}

// TestA6A7PDFPageLocator covers A6/A7 with the real parser: the page and block
// of a hit match the PDF, two paragraphs on one page keep distinct offsets and
// no chunk spans pages.
func TestA6A7PDFPageLocator(t *testing.T) {
	ws := newWorkspace(t)
	pdf := buildPDF(
		[]string{"Alpha first paragraph.", "Beta second paragraph."},
		[]string{"Gamma third page text."},
	)
	paperID := insertPaper(t, ws, "Page Locator Paper", "2018", pdf)
	svc := newSearchService(t, ws, Options{}) // real GoPDFParser
	scope := Scope{Kind: ScopeGlobal}
	indexEntities(t, svc, "", Change{EntityType: EntityPaper, EntityID: paperID})

	gamma := search(t, svc, SearchRequest{Query: "Gamma", Scope: scope})
	fullID := FullTextDocID(paperID)
	page2 := findResult(t, gamma, fullID)
	if page2.Locator.PageNo != 2 {
		t.Fatalf("页码应为 2: %+v", page2.Locator)
	}
	if page2.Target.Kind != "paper" || page2.Target.PaperID != paperID {
		t.Fatalf("文献跳转目标错误: %+v", page2.Target)
	}

	beta := search(t, svc, SearchRequest{Query: "Beta", Scope: scope})
	b := findResult(t, beta, fullID)
	if b.Locator.PageNo != 1 || b.Locator.BlockNo != 1 {
		t.Fatalf("Beta 应定位到第 1 页第 2 段: %+v", b.Locator)
	}
	if b.Locator.CharStart == 0 {
		t.Fatalf("第二段字符偏移应为非零: %+v", b.Locator)
	}

	// Every stored chunk of the full text belongs to exactly one page.
	chunks := loadChunksForTest(t, ws, fullID)
	if len(chunks) < 2 {
		t.Fatalf("分块数不足: %d", len(chunks))
	}
	seen := map[int]bool{}
	for _, chunk := range chunks {
		seen[chunk.Locator.PageNo] = true
		if chunk.Locator.PageNo == 1 && (chunk.Locator.CharStart < 0 || chunk.Locator.CharEnd <= chunk.Locator.CharStart) {
			t.Fatalf("第 1 页块偏移错误: %+v", chunk.Locator)
		}
	}
	if !seen[1] || !seen[2] {
		t.Fatalf("分块未覆盖两页: %v", seen)
	}
}

// TestPaperNoteTarget covers a paper note's jump target: it resolves the paper
// and the local PDF path without writing anything.
func TestPaperNoteTarget(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "论文笔记")
	paperID := insertPaper(t, ws, "Note Target Paper", "2021", buildPDF([]string{"note target pdf body"}))
	linkPaper(t, ws, projectID, paperID)
	noteID := insertNote(t, ws, projectID, "paper_note", "论文笔记", "paper note body about calibration", paperID)
	svc := newSearchService(t, ws, Options{})
	indexEntities(t, svc, projectID, Change{EntityType: EntityNote, EntityID: noteID})

	resp := search(t, svc, SearchRequest{Query: "calibration", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	result := findResult(t, resp, DocIDFor(EntityNote, noteID))
	if result.Target.Kind != "note" || result.Target.PaperID != paperID || result.Target.PDFPath == "" {
		t.Fatalf("论文笔记跳转目标不完整: %+v", result.Target)
	}

	target, err := svc.ResolveTarget(context.Background(), EntityRef{
		EntityType: EntityPaper, EntityID: paperID,
	}, Locator{PageNo: 1})
	if err != nil {
		t.Fatalf("resolve paper: %v", err)
	}
	if target.Kind != "paper" || target.PDFPath == "" || target.Locator.PageNo != 1 {
		t.Fatalf("文献跳转目标不完整: %+v", target)
	}
}

// loadChunksForTest reads the stored chunks of a document through the extractor
// read path.
func loadChunksForTest(t *testing.T, ws *workspace.Workspace, docID string) []Chunk {
	t.Helper()
	chunks, err := loadStoredChunks(context.Background(), ws.Global().DB(), "paper_chunks", docID)
	if err != nil {
		t.Fatalf("load chunks: %v", err)
	}
	return chunks
}
