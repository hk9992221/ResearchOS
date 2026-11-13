package search

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/workspace"
	"researchos/internal/index/store"
)

// perfProject and perfChunkDoc define the A19 fixture: one project with 500
// docs × 20 chunks = 10,000 chunks (the number used by the acceptance row).
const (
	perfDocsPerProject   = 500
	perfChunksPerDoc     = 20
	perfGlobalProjects   = 10
	perfGlobalDocsEach   = 50
	perfGlobalChunksEach = 20
)

// buildPerfProject writes a synthetic, fully keyword-indexed project.
func buildPerfProject(tb testing.TB, ws *workspace.Workspace, name string, docs, chunksPerDoc int) string {
	tb.Helper()
	projectID := createProject(tb, ws, name)
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		tb.Fatalf("project handle: %v", err)
	}
	kw, err := store.NewKeywordIndex(handle.DB(), store.PartitionProject, clock.System{})
	if err != nil {
		tb.Fatalf("keyword index: %v", err)
	}
	now := clock.Format(clock.System{}.Now())
	ctx := context.Background()
	for docIndex := range docs {
		docID := fmt.Sprintf("note:perf-%s-%04d", name, docIndex)
		doc := store.IndexDoc{
			ProjectID:   projectID,
			DocID:       docID,
			Source:      string(SourceScratchpad),
			EntityType:  string(EntityNote),
			EntityID:    docID,
			Title:       fmt.Sprintf("性能文档 %d", docIndex),
			ContentHash: hashText(docID),
		}
		chunks := make([]store.IndexChunk, 0, chunksPerDoc)
		for chunkIndex := range chunksPerDoc {
			text := fmt.Sprintf("performance benchmark chunk %d in document %d: retrieval latency measurement text", chunkIndex, docIndex)
			chunks = append(chunks, store.IndexChunk{
				ChunkID:     docID + "#" + fmt.Sprint(chunkIndex),
				DocID:       docID,
				ProjectID:   projectID,
				Ordinal:     chunkIndex,
				Text:        text,
				ContentHash: hashText(text),
				LocatorJSON: []byte(`{}`),
			})
		}
		if err := kw.ReplaceDoc(ctx, doc, chunks); err != nil {
			tb.Fatalf("replace doc: %v", err)
		}
	}
	if _, err := handle.DB().Exec(
		`UPDATE index_docs SET index_status = 'ok', parser_version = ?, schema_version = 1, lang = 'en', indexed_at = ?`,
		parserVersionMarkdown, now); err != nil {
		tb.Fatalf("mark docs: %v", err)
	}
	return projectID
}

// percentile returns the p-th percentile of a sorted duration slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

// measureRuns runs the request n times, returning the sorted durations and the
// last response.
func measureRuns(tb testing.TB, svc *Service, req SearchRequest, runs int) ([]time.Duration, SearchResponse) {
	tb.Helper()
	timings := make([]time.Duration, 0, runs)
	var last SearchResponse
	for range runs {
		start := time.Now()
		resp, err := svc.Search(context.Background(), req)
		if err != nil {
			tb.Fatalf("search: %v", err)
		}
		timings = append(timings, time.Since(start))
		last = resp
	}
	sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
	return timings, last
}

// TestA19ProjectLatency measures the A19 project threshold. Machine: the same
// developer Windows x64 box used for the wave; the assertion is deliberately
// generous (fail only on a pathological regression) so CI variance cannot make
// it flaky. Measured p50/p95 are printed for the acceptance record.
func TestA19ProjectLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("A19 fixture is slow")
	}
	ws := newWorkspace(t)
	projectID := buildPerfProject(t, ws, "perf", perfDocsPerProject, perfChunksPerDoc)
	svc := newSearchService(t, ws, Options{})
	req := SearchRequest{Query: "performance benchmark", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}}
	// Warm up prepared statements and the page cache.
	if _, err := svc.Search(context.Background(), req); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	timings, resp := measureRuns(t, svc, req, 30)
	status := waitIdle(t, svc, Scope{Kind: ScopeProject, ProjectID: projectID})
	t.Logf("A19 项目检索（%d 块，keyword-only）：p50=%v p95=%v max=%v，结果 %d 条，timings=%v",
		status.Chunks, percentile(timings, 0.50), percentile(timings, 0.95), timings[len(timings)-1], len(resp.Results), resp.Timings)
	if got := status.Chunks; got != perfDocsPerProject*perfChunksPerDoc {
		t.Fatalf("夹具块数错误: %d", got)
	}
	if p95 := percentile(timings, 0.95); p95 > 3*time.Second {
		t.Fatalf("项目检索出现病态退化: p95=%v", p95)
	}
}

// TestA19GlobalLatency measures the A19 global threshold over ten projects.
func TestA19GlobalLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("A19 fixture is slow")
	}
	ws := newWorkspace(t)
	for i := range perfGlobalProjects {
		buildPerfProject(t, ws, fmt.Sprintf("global-%02d", i), perfGlobalDocsEach, perfGlobalChunksEach)
	}
	svc := newSearchService(t, ws, Options{})
	req := SearchRequest{Query: "performance benchmark", Scope: Scope{Kind: ScopeGlobal}, Limit: 20}
	if _, err := svc.Search(context.Background(), req); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	timings, resp := measureRuns(t, svc, req, 15)
	t.Logf("A19 全局检索（%d 项目 × %d 块）：p50=%v p95=%v max=%v，结果 %d 条，partial=%v timings=%v",
		perfGlobalProjects, perfGlobalDocsEach*perfGlobalChunksEach,
		percentile(timings, 0.50), percentile(timings, 0.95), timings[len(timings)-1], len(resp.Results), resp.Partial, resp.Timings)
	if p95 := percentile(timings, 0.95); p95 > 5*time.Second {
		t.Fatalf("全局检索出现病态退化: p95=%v", p95)
	}
}

// BenchmarkSearch10kChunks is the A19 benchmark over the 10,000-chunk fixture.
func BenchmarkSearch10kChunks(b *testing.B) {
	ws := newWorkspace(b)
	projectID := buildPerfProject(b, ws, "bench", perfDocsPerProject, perfChunksPerDoc)
	svc, err := New(ws, Options{})
	if err != nil {
		b.Fatalf("search.New: %v", err)
	}
	defer svc.Close()
	req := SearchRequest{Query: "performance benchmark", Scope: Scope{Kind: ScopeProject, ProjectID: projectID}}
	b.ResetTimer()
	for range b.N {
		if _, err := svc.Search(context.Background(), req); err != nil {
			b.Fatalf("search: %v", err)
		}
	}
}
