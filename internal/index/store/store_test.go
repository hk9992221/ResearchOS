package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"researchos/internal/ai/aitest"
	"researchos/internal/index/store"
)

func doc(id string) store.IndexDoc {
	return store.IndexDoc{
		ProjectID: "proj-1", DocID: id, Source: "note",
		EntityType: "note", EntityID: id + "-ent", Title: "T " + id, ContentHash: "h-" + id,
	}
}

func chunks(docID string, texts ...string) []store.IndexChunk {
	out := make([]store.IndexChunk, 0, len(texts))
	for i, t := range texts {
		out = append(out, store.IndexChunk{
			ChunkID:     docID + "#" + string(rune('a'+i)),
			DocID:       docID,
			ProjectID:   "proj-1",
			Ordinal:     i,
			Text:        t,
			ContentHash: "ch" + docID,
			LocatorJSON: json.RawMessage(`{"page_no":` + itoa(i+1) + `,"char_start":0,"char_end":10}`),
		})
	}
	return out
}

func itoa(i int) string {
	return strings.TrimSpace(jsonNumber(i))
}

func jsonNumber(i int) string {
	raw, _ := json.Marshal(i)
	return string(raw)
}

func bothIndexes(t *testing.T) (store.KeywordIndex, store.VectorIndex, func() int, func() int) {
	t.Helper()
	f := aitest.FixtureWithProject(t)
	db := f.Project.DB()
	ki, err := store.NewKeywordIndex(db, store.PartitionProject, f.Clock)
	if err != nil {
		t.Fatal(err)
	}
	vi, err := store.NewVectorIndex(db, store.PartitionProject, f.Clock)
	if err != nil {
		t.Fatal(err)
	}
	countChunks := func() int {
		var n int
		db.QueryRow(`SELECT count(*) FROM index_chunks`).Scan(&n)
		return n
	}
	countVecs := func() int {
		var n int
		db.QueryRow(`SELECT count(*) FROM chunk_embeddings`).Scan(&n)
		return n
	}
	return ki, vi, countChunks, countVecs
}

func TestKeywordReplaceQueryDelete(t *testing.T) {
	ctx := context.Background()
	ki, _, countChunks, _ := bothIndexes(t)

	d := doc("d1")
	if err := ki.ReplaceDoc(ctx, d, chunks("d1",
		"Introduction to retrieval augmented generation",
		"混合检索使用 embedding 向量与关键词匹配")); err != nil {
		t.Fatal(err)
	}
	if countChunks() != 2 {
		t.Fatalf("chunks = %d", countChunks())
	}

	// AC-07.1: query returns hits with full metadata.
	hits, err := ki.Query(ctx, "proj-1", "retrieval augmented", 10, store.IndexFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].DocID != "d1" || hits[0].ProjectID != "proj-1" {
		t.Fatalf("hits = %+v", hits)
	}
	if !strings.Contains(hits[0].Text, "retrieval") {
		t.Fatalf("hit text = %q", hits[0].Text)
	}
	if len(hits[0].MatchedTerms) == 0 {
		t.Fatal("matched terms missing")
	}

	// AC-08.1: locator bytes round-trip untouched.
	if string(hits[0].LocatorJSON) != string(chunks("d1", "x")[0].LocatorJSON) {
		t.Fatalf("locator drift: %s", hits[0].LocatorJSON)
	}

	// AC-07.2: replace is idempotent — same chunk ids replace, no dupes.
	if err := ki.ReplaceDoc(ctx, d, chunks("d1", "only one chunk now")); err != nil {
		t.Fatal(err)
	}
	if countChunks() != 1 {
		t.Fatalf("chunks after replace = %d", countChunks())
	}
	empty, _ := ki.Query(ctx, "proj-1", "retrieval augmented", 10, store.IndexFilter{})
	if len(empty) != 0 {
		t.Fatalf("stale hit after replace: %+v", empty)
	}

	// AC-07.4: delete + idempotent second delete.
	if err := ki.DeleteDoc(ctx, "proj-1", "d1"); err != nil {
		t.Fatal(err)
	}
	if err := ki.DeleteDoc(ctx, "proj-1", "d1"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if countChunks() != 0 {
		t.Fatal("chunks survive delete")
	}
}

func TestKeywordShortCJKFallsBackToLike(t *testing.T) {
	ctx := context.Background()
	ki, _, _, _ := bothIndexes(t)
	if err := ki.ReplaceDoc(ctx, doc("d1"), chunks("d1", "这是中文内容测试片段")); err != nil {
		t.Fatal(err)
	}
	// Two-character terms are below the trigram floor; the LIKE path keeps
	// retrieval alive (O-02 isolation).
	hits, err := ki.Query(ctx, "proj-1", "中文", 10, store.IndexFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ChunkID != "d1#a" {
		t.Fatalf("CJK short query hits = %+v", hits)
	}
	// Three-character terms take the FTS path.
	hits, _ = ki.Query(ctx, "proj-1", "中文内容", 10, store.IndexFilter{})
	if len(hits) != 1 {
		t.Fatalf("CJK trigram hits = %+v", hits)
	}
}

func TestKeywordFiltersAndPartition(t *testing.T) {
	ctx := context.Background()
	ki, _, _, _ := bothIndexes(t)
	for _, d := range []string{"d1", "d2", "d3"} {
		if err := ki.ReplaceDoc(ctx, doc(d), chunks(d, "shared keyword retrieval for filtering tests")); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := ki.Query(ctx, "proj-1", "retrieval", 10, store.IndexFilter{})
	if len(all) != 3 {
		t.Fatalf("baseline = %d", len(all))
	}
	// AC-10.4: DocIDs restricts, ExcludeDocIDs removes, before top-k.
	only, _ := ki.Query(ctx, "proj-1", "retrieval", 10, store.IndexFilter{DocIDs: []string{"d2"}})
	if len(only) != 1 || only[0].DocID != "d2" {
		t.Fatalf("include filter = %+v", only)
	}
	not2, _ := ki.Query(ctx, "proj-1", "retrieval", 10, store.IndexFilter{ExcludeDocIDs: []string{"d2"}})
	if len(not2) != 2 {
		t.Fatalf("exclude filter = %+v", not2)
	}
	// topK truncation.
	two, _ := ki.Query(ctx, "proj-1", "retrieval", 2, store.IndexFilter{})
	if len(two) != 2 {
		t.Fatalf("topK = %+v", two)
	}
	// Project partition: another project id sees nothing.
	other, _ := ki.Query(ctx, "other-project", "retrieval", 10, store.IndexFilter{})
	if len(other) != 0 {
		t.Fatalf("partition leak: %+v", other)
	}
}

func vec(dim int, seed float32) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = seed + float32(i)*0.1
	}
	return v
}

func vectorsFor(cs []store.IndexChunk, seed float32) []store.ChunkVector {
	out := make([]store.ChunkVector, len(cs))
	for i, c := range cs {
		out[i] = store.ChunkVector{ChunkID: c.ChunkID, Vector: vec(4, seed+float32(i))}
	}
	return out
}

// unitVecs gives every chunk its own axis so cosine ranking is unambiguous.
func unitVecs(cs []store.IndexChunk) []store.ChunkVector {
	out := make([]store.ChunkVector, len(cs))
	for i, c := range cs {
		v := make([]float32, 4)
		v[i] = 1
		out[i] = store.ChunkVector{ChunkID: c.ChunkID, Vector: v}
	}
	return out
}

func TestVectorProfileIsolationAndReplace(t *testing.T) {
	ctx := context.Background()
	ki, vi, _, countVecs := bothIndexes(t)
	d := doc("d1")
	cs := chunks("d1", "alpha text", "beta text")
	if err := ki.ReplaceDoc(ctx, d, cs); err != nil {
		t.Fatal(err)
	}

	if err := vi.ReplaceDoc(ctx, "profile-A", d, cs, unitVecs(cs)); err != nil {
		t.Fatal(err)
	}
	// Same doc, second profile: profiles coexist (§5.10.3, AC-07.3).
	shiftedB := []store.ChunkVector{
		{ChunkID: "d1#a", Vector: []float32{0, 0, 1, 0}},
		{ChunkID: "d1#b", Vector: []float32{0, 0, 0, 1}},
	}
	if err := vi.ReplaceDoc(ctx, "profile-B", d, cs, shiftedB); err != nil {
		t.Fatal(err)
	}
	if countVecs() != 4 {
		t.Fatalf("vector rows = %d", countVecs())
	}

	hitsA, err := vi.Query(ctx, "profile-A", []float32{1, 0, 0, 0}, 10, store.IndexFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hitsA) != 2 || hitsA[0].ChunkID != "d1#a" || hitsA[0].Score < 0.999 || hitsA[1].Score > 0.001 {
		t.Fatalf("profile A ranking = %+v", hitsA)
	}
	// AC-10.2: profile B answers only from its own space: the query vector
	// is orthogonal to every B embedding.
	hitsB, _ := vi.Query(ctx, "profile-B", []float32{1, 0, 0, 0}, 10, store.IndexFilter{})
	if len(hitsB) != 2 || hitsB[0].Score > 0.001 {
		t.Fatalf("profile mixup = %+v", hitsB)
	}

	// Replacing profile A is idempotent, never doubles rows.
	if err := vi.ReplaceDoc(ctx, "profile-A", d, cs, unitVecs(cs)); err != nil {
		t.Fatal(err)
	}
	if countVecs() != 4 {
		t.Fatalf("replace duplicated: %d", countVecs())
	}

	// vector DeleteDoc wipes ALL profiles of the doc (AC-07.4).
	if err := vi.DeleteDoc(ctx, "proj-1", "d1"); err != nil {
		t.Fatal(err)
	}
	if err := vi.DeleteDoc(ctx, "proj-1", "d1"); err != nil {
		t.Fatalf("idempotent: %v", err)
	}
	if countVecs() != 0 {
		t.Fatalf("vectors survive doc delete: %d", countVecs())
	}
	// Keyword DeleteDoc never destroys profile vectors directly; the join
	// hides stale rows until VectorIndex.DeleteDoc reclaims them, which is
	// SPEC-10's job (§5.10.3 rule 2).
	if err := vi.ReplaceDoc(ctx, "profile-A", d, cs, unitVecs(cs)); err != nil {
		t.Fatal(err)
	}
	if err := ki.DeleteDoc(ctx, "proj-1", "d1"); err != nil {
		t.Fatal(err)
	}
	hidden, err := vi.Query(ctx, "profile-A", []float32{1, 0, 0, 0}, 10, store.IndexFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden) != 0 {
		t.Fatalf("deleted doc vectors still visible: %+v", hidden)
	}
}

func TestVectorValidations(t *testing.T) {
	ctx := context.Background()
	ki, vi, _, countVecs := bothIndexes(t)
	d := doc("d1")
	cs := chunks("d1", "one", "two")
	if err := ki.ReplaceDoc(ctx, d, cs); err != nil {
		t.Fatal(err)
	}

	// AC-10.5: dimension mismatch against the stored profile → error, no
	// rows written (AC-09.2: never half vectors).
	if err := vi.ReplaceDoc(ctx, "profile-A", d, cs, vectorsFor(cs, 1)); err != nil {
		t.Fatal(err)
	}
	before := countVecs()
	mixed := []store.ChunkVector{
		{ChunkID: "d1#a", Vector: vec(4, 1)},
		{ChunkID: "d1#b", Vector: vec(3, 1)},
	}
	if err := vi.ReplaceDoc(ctx, "profile-A", d, cs, mixed); !errors.Is(err, store.ErrVectorDim) {
		t.Fatalf("mixed dims: %v", err)
	}
	if countVecs() != before {
		t.Fatal("dimension failure wrote rows")
	}
	wrongDim := []store.ChunkVector{
		{ChunkID: "d1#a", Vector: vec(8, 1)},
		{ChunkID: "d1#b", Vector: vec(8, 2)},
	}
	if err := vi.ReplaceDoc(ctx, "profile-A", d, cs, wrongDim); !errors.Is(err, store.ErrVectorDim) {
		t.Fatalf("profile dim clash: %v", err)
	}
	if countVecs() != before {
		t.Fatal("profile clash wrote rows")
	}

	// Half batches rejected: vectors must cover exactly the chunks.
	if err := vi.ReplaceDoc(ctx, "profile-B", d, cs, vectorsFor(cs, 1)[:1]); !errors.Is(err, store.ErrVectorCount) {
		t.Fatalf("half batch: %v", err)
	}
	// Unknown chunk ids rejected.
	rogue := []store.ChunkVector{
		{ChunkID: "d1#a", Vector: vec(4, 1)},
		{ChunkID: "elsewhere#a", Vector: vec(4, 2)},
	}
	if err := vi.ReplaceDoc(ctx, "profile-B", d, cs, rogue); !errors.Is(err, store.ErrVectorCount) {
		t.Fatalf("rogue chunk: %v", err)
	}
	// Vector write before keyword ReplaceDoc fails cleanly.
	d2 := doc("d2")
	cs2 := chunks("d2", "x")
	if err := vi.ReplaceDoc(ctx, "profile-A", d2, cs2, vectorsFor(cs2, 1)); err == nil {
		t.Fatal("vector write succeeded for un-indexed doc")
	}
	if countVecs() != before {
		t.Fatal("failed precheck wrote rows")
	}
	// Empty profile id rejected.
	if err := vi.ReplaceDoc(ctx, "", d, cs, vectorsFor(cs, 1)); !errors.Is(err, store.ErrEmptyProfile) {
		t.Fatalf("empty profile: %v", err)
	}
	// Filters apply to vector queries too.
	hits, _ := vi.Query(ctx, "profile-A", vec(4, 1), 10, store.IndexFilter{ExcludeDocIDs: []string{"d1"}})
	if len(hits) != 0 {
		t.Fatalf("vector filter leak: %+v", hits)
	}
}

func TestKeywordReplaceValidation(t *testing.T) {
	ctx := context.Background()
	ki, _, _, _ := bothIndexes(t)
	if err := ki.ReplaceDoc(ctx, store.IndexDoc{}, nil); !errors.Is(err, store.ErrEmptyDocID) {
		t.Fatalf("empty doc: %v", err)
	}
	cs := chunks("other", "text")
	if err := ki.ReplaceDoc(ctx, doc("d1"), cs); !errors.Is(err, store.ErrChunkDocMismatch) {
		t.Fatalf("chunk/doc mismatch: %v", err)
	}
	dup := chunks("d1", "a")
	dup = append(dup, dup[0])
	if err := ki.ReplaceDoc(ctx, doc("d1"), dup); !errors.Is(err, store.ErrDuplicateChunkID) {
		t.Fatalf("dup chunk ids: %v", err)
	}
}

func TestGlobalPartitionPaperTables(t *testing.T) {
	ctx := context.Background()
	f := aitest.Fixture(t)
	db := f.Global().DB()
	ki, err := store.NewKeywordIndex(db, store.PartitionGlobal, f.Clock)
	if err != nil {
		t.Fatal(err)
	}
	vi, err := store.NewVectorIndex(db, store.PartitionGlobal, f.Clock)
	if err != nil {
		t.Fatal(err)
	}
	// RQ-09: PDF chunk metadata stored verbatim; global docs carry the
	// paper_pdf source with an empty project key.
	d := store.IndexDoc{DocID: "paper:p1:fulltext", Source: "paper_pdf", EntityType: "paper", EntityID: "p1", Title: "PDF"}
	pdfChunks := []store.IndexChunk{{
		ChunkID: "paper:p1:fulltext#0", DocID: d.DocID, Ordinal: 0,
		Text:        "Methods section discusses trigram indexing of PDF pages",
		ContentHash: "abc",
		LocatorJSON: json.RawMessage(`{"page_no":7,"char_start":120,"char_end":480}`),
	}}
	if err := ki.ReplaceDoc(ctx, d, pdfChunks); err != nil {
		t.Fatal(err)
	}
	hits, err := ki.Query(ctx, "", "trigram indexing", 5, store.IndexFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || string(hits[0].LocatorJSON) != string(pdfChunks[0].LocatorJSON) {
		t.Fatalf("global hits = %+v", hits)
	}
	if err := vi.ReplaceDoc(ctx, "profile-P", d, pdfChunks, []store.ChunkVector{{ChunkID: pdfChunks[0].ChunkID, Vector: vec(4, 1)}}); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM paper_chunk_vectors`).Scan(&n)
	if n != 1 {
		t.Fatalf("paper vectors = %d", n)
	}
	// Keyword delete hides the paper vectors through the join.
	if err := ki.DeleteDoc(ctx, "", d.DocID); err != nil {
		t.Fatal(err)
	}
	hits, _ = ki.Query(ctx, "", "trigram indexing", 5, store.IndexFilter{})
	if len(hits) != 0 {
		t.Fatalf("paper chunks survive: %+v", hits)
	}
	vhits, err := vi.Query(ctx, "profile-P", []float32{1, 0, 0, 0}, 5, store.IndexFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(vhits) != 0 {
		t.Fatalf("paper vectors leaked visibly: %+v", vhits)
	}
	// Explicit vector delete then reclaims the rows.
	if err := vi.DeleteDoc(ctx, "", d.DocID); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT count(*) FROM paper_chunk_vectors`).Scan(&n)
	if n != 0 {
		t.Fatalf("paper vectors not reclaimed: %d", n)
	}
}
