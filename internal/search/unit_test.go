package search

import (
	"strings"
	"testing"
)

// TestNormalizeQuery covers the deterministic query pipeline: trim, NFKC,
// full-width folding, case folding, language detection and phrases.
func TestNormalizeQuery(t *testing.T) {
	nq, err := normalizeQuery("  ＲＥＳＥＡＲＣＨ   System  ")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if nq.Text != "research system" {
		t.Fatalf("规范化错误: %q", nq.Text)
	}
	if nq.Lang != "en" {
		t.Fatalf("语言检测错误: %q", nq.Lang)
	}

	zh, err := normalizeQuery("研究操作系统")
	if err != nil {
		t.Fatalf("normalize zh: %v", err)
	}
	if zh.Lang != "zh" {
		t.Fatalf("中文语言检测错误: %q", zh.Lang)
	}

	phrased, err := normalizeQuery(`study "exact phrase" here`)
	if err != nil {
		t.Fatalf("normalize phrase: %v", err)
	}
	if len(phrased.Phrases) != 1 || phrased.Phrases[0] != "exact phrase" {
		t.Fatalf("短语提取错误: %+v", phrased.Phrases)
	}
	if strings.Contains(phrased.Text, "exact") {
		t.Fatalf("短语未从检索文本剔除: %q", phrased.Text)
	}

	if _, err := normalizeQuery(strings.Repeat("字", maxQueryRunes+1)); err == nil {
		t.Fatal("超长查询必须报错")
	} else {
		var searchErr *Error
		if !asSearchError(err, &searchErr) || searchErr.Code != CodeInvalidRequest {
			t.Fatalf("错误码错误: %v", err)
		}
	}
}

// asSearchError checks errors.As without importing errors twice in this file.
func asSearchError(err error, dst **Error) bool {
	e, ok := err.(*Error)
	if !ok {
		return false
	}
	*dst = e
	return true
}

// TestPhraseIsMust covers the quoted-phrase must condition.
func TestPhraseIsMust(t *testing.T) {
	ws := newWorkspace(t)
	projectID := createProject(t, ws, "短语项目")
	exact := insertNote(t, ws, projectID, "scratchpad", "exact", "gradient descent optimization works", "")
	scattered := insertNote(t, ws, projectID, "scratchpad", "scattered", "gradient based descent optimization works", "")
	svc := newSearchService(t, ws, Options{})
	indexEntities(t, svc, projectID,
		Change{EntityType: EntityNote, EntityID: exact},
		Change{EntityType: EntityNote, EntityID: scattered})

	resp := search(t, svc, SearchRequest{Query: `"gradient descent"`, Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	if !containsDoc(resp, DocIDFor(EntityNote, exact)) {
		t.Fatalf("短语命中缺失: %v", resultDocIDs(resp))
	}
	if containsDoc(resp, DocIDFor(EntityNote, scattered)) {
		t.Fatalf("短语 must 条件未生效: %v", resultDocIDs(resp))
	}
}

// TestChunkMarkdownOffsets covers Markdown chunking: heading paths, stable
// ordinals, original character offsets and overlap.
func TestChunkMarkdownOffsets(t *testing.T) {
	var body strings.Builder
	body.WriteString("# Method\n\n")
	for i := range 12 {
		body.WriteString("Paragraph ")
		body.WriteString(string(rune('A' + i)))
		body.WriteString(" explains the experiment design and the measurement procedure in detail. ")
		body.WriteString("This sentence adds enough tokens to force a new chunk boundary when repeated. ")
		body.WriteString("Another sentence repeats the measurement context so the token budget is exceeded. ")
		body.WriteString("A final sentence keeps the paragraph long enough for several chunks to exist.\n\n")
	}
	body.WriteString("## Results\n\nFinal accuracy reached ninety percent. " +
		strings.Repeat("The measured throughput also improved across every configuration we tested. ", 12) + "\n")
	markdown := body.String()
	chunks := chunkMarkdown("note:demo", markdown)
	if len(chunks) < 2 {
		t.Fatalf("期望多个分块，实际 %d", len(chunks))
	}
	runes := []rune(markdown)
	for i, chunk := range chunks {
		if chunk.Ordinal != i || chunk.ChunkID != chunkID("note:demo", i) {
			t.Fatalf("块序号不稳定: %+v", chunk)
		}
		if chunk.Locator.CharStart < 0 || chunk.Locator.CharEnd > len(runes) ||
			chunk.Locator.CharEnd <= chunk.Locator.CharStart {
			t.Fatalf("字符偏移越界: %+v", chunk.Locator)
		}
		original := string(runes[chunk.Locator.CharStart:chunk.Locator.CharEnd])
		if strings.TrimSpace(original) != strings.TrimSpace(chunk.Text) {
			t.Fatalf("块 %d 文本与原文偏移不一致", i)
		}
		if chunk.Locator.Heading == "" {
			t.Fatalf("块 %d 缺少标题路径", i)
		}
		if estimateTokens(chunk.Text) > markdownMaxTokens+estimateTokens("一字") {
			t.Fatalf("块 %d 过大: %d tokens", i, estimateTokens(chunk.Text))
		}
	}
	overlap := false
	for i := 1; i < len(chunks); i++ {
		if chunks[i].Locator.CharStart < chunks[i-1].Locator.CharEnd {
			overlap = true
		}
	}
	if !overlap {
		t.Fatal("相邻块缺少约 50 token 的重叠")
	}
	// The Results heading appears in a chunk whose heading path contains it.
	found := false
	for _, chunk := range chunks {
		if strings.Contains(chunk.Text, "Final accuracy") && strings.Contains(chunk.Locator.Heading, "Results") {
			found = true
		}
	}
	if !found {
		t.Fatal("未定位到 Results 标题路径")
	}
}

// TestChunkPDFBlocksNeverCrossPages covers the per-page chunk rule.
func TestChunkPDFBlocksNeverCrossPages(t *testing.T) {
	blocks := []PdfBlock{
		{PageNo: 1, BlockNo: 0, Text: "first page block", CharStart: 0, CharEnd: 16},
		{PageNo: 1, BlockNo: 1, Text: "second page block", CharStart: 17, CharEnd: 34},
		{PageNo: 2, BlockNo: 0, Text: "third page block", CharStart: 0, CharEnd: 16},
	}
	chunks := chunkPDFBlocks("paper:p:fulltext", blocks)
	if len(chunks) != 3 {
		t.Fatalf("块数错误: %d", len(chunks))
	}
	for i, chunk := range chunks {
		if chunk.Locator.PageNo != blocks[i].PageNo || chunk.Locator.BlockNo != blocks[i].BlockNo {
			t.Fatalf("块 %d 页码/序号错误: %+v", i, chunk.Locator)
		}
		text := chunk.Text
		if chunk.Locator.PageNo == 1 && strings.Contains(text, "third") {
			t.Fatalf("块跨页: %+v", chunk)
		}
	}
}

// TestBuildSnippet covers the 200-rune snippet window and highlight ranges.
func TestBuildSnippet(t *testing.T) {
	prefix := strings.Repeat("背景文字。", 40)
	text := prefix + "关键结论是调度算法稳定。" + strings.Repeat("补充说明。", 40)
	snippet := buildSnippet(text, []string{"调度算法"}, 200, Locator{Heading: "方法"})
	runes := []rune(snippet.Text)
	if len(runes) > 200 {
		t.Fatalf("片段过长: %d", len(runes))
	}
	if len(snippet.Highlights) == 0 {
		t.Fatal("缺少高亮")
	}
	for _, hl := range snippet.Highlights {
		if hl.End > len(runes) || hl.Start < 0 {
			t.Fatalf("高亮越界: %+v", hl)
		}
		if got := string(runes[hl.Start:hl.End]); got != "调度算法" {
			t.Fatalf("高亮文本错误: %q", got)
		}
	}
	if snippet.Locator.Heading != "方法" {
		t.Fatalf("片段定位丢失: %+v", snippet.Locator)
	}
}

// TestDocIDsAreDerived covers the idempotent document key rules.
func TestDocIDsAreDerived(t *testing.T) {
	if got := DocIDFor(EntityNote, "abc"); got != "note:abc" {
		t.Fatalf("doc id: %q", got)
	}
	if got := FullTextDocID("abc"); got != "paper:abc:fulltext" {
		t.Fatalf("fulltext doc id: %q", got)
	}
	if ExperimentDocID("exp", SourceExperimentResults) != "experiment:exp:results" {
		t.Fatalf("results doc id")
	}
	if ExperimentDocID("exp", SourceExperimentOutline) != "experiment:exp:outline" {
		t.Fatalf("outline doc id")
	}
}
