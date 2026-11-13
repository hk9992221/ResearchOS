package search

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"researchos/internal/foundation/clock"
	"researchos/internal/index/store"
)

// searchTarget is one database participating in a search.
type searchTarget struct {
	projectID    string // empty for the global literature partition
	name         string
	archived     bool
	db           *sql.DB
	partition    store.Partition
	tables       tableSet
	restrictDocs []string // project scope: the linked global paper docs
}

// key returns the dedupe key of the target.
func (t *searchTarget) key() string {
	if t.partition == store.PartitionGlobal {
		return "global"
	}
	return t.projectID
}

// Search runs one complete retrieval pipeline (SPEC-10 §5.2). Degradations are
// reported in the response instead of as errors; only invalid parameters, a
// missing scope and an unreadable index return errors.
func (s *Service) Search(ctx context.Context, req SearchRequest) (SearchResponse, error) {
	start := time.Now()
	if err := req.Scope.Validate(); err != nil {
		return SearchResponse{}, err
	}
	if req.Offset < 0 {
		return SearchResponse{}, Errorf(CodeInvalidRequest, "offset 不能为负数")
	}
	if req.Limit < 0 {
		return SearchResponse{}, Errorf(CodeInvalidRequest, "limit 不能为负数")
	}
	mode := req.Rerank
	if mode == "" {
		mode = RerankAuto
	}
	switch mode {
	case RerankAuto, RerankOff, RerankForce:
	default:
		return SearchResponse{}, Errorf(CodeInvalidRequest, "未知 rerank 模式 %q", string(mode))
	}
	if req.Limit == 0 {
		req.Limit = s.cfg.limit
	}
	if req.Limit > s.cfg.maxLimit {
		req.Limit = s.cfg.maxLimit
	}
	normalizeStart := time.Now()
	nq, err := normalizeQuery(req.Query)
	if err != nil {
		return SearchResponse{}, err
	}
	normalizeDur := time.Since(normalizeStart)
	targets, err := s.targets(ctx, req)
	if err != nil {
		return SearchResponse{}, err
	}
	if len(targets) == 0 {
		return SearchResponse{Results: []SearchResult{}, Total: 0, Timings: s.timings(start, map[string]time.Duration{"normalize": normalizeDur})}, nil
	}

	// Browse mode: no query text, updated_at descending (SPEC-10 §5.1).
	if nq.Text == "" && len(nq.Phrases) == 0 {
		resp, err := s.browse(ctx, req, targets, start)
		if err == nil {
			resp.Timings["normalize"] = normalizeDur.Milliseconds()
		}
		return resp, err
	}

	budget := s.cfg.globalBudget
	fanCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	var (
		mu       sync.Mutex
		degraded []DegradeReason
		partial  bool
	)
	addDegrade := func(reasons ...DegradeReason) {
		mu.Lock()
		defer mu.Unlock()
		for _, r := range reasons {
			found := false
			for _, existing := range degraded {
				if existing == r {
					found = true
					break
				}
			}
			if !found {
				degraded = append(degraded, r)
			}
		}
	}
	markPartial := func() {
		mu.Lock()
		partial = true
		mu.Unlock()
	}

	// Phase 1: query normalization and keyword recall run in parallel with the
	// query embedding (SPEC-10 §5.2 step 1 and 2).
	type keywordResult struct {
		hits    []store.ChunkHit
		filter  docFilter
		err     error
		timeout bool
	}
	keywordResults := make([]keywordResult, len(targets))
	var keywordWG sync.WaitGroup
	sem := make(chan struct{}, s.cfg.maxConcurrency)
	keywordStart := time.Now()
	for i, target := range targets {
		keywordWG.Add(1)
		go func(i int, target *searchTarget) {
			defer keywordWG.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, stop := targetContext(fanCtx, s.budgetFor(target))
			defer stop()
			// An already-expired budget deterministically marks the target
			// timed out even if the in-process SQLite driver could answer.
			if cctx.Err() != nil {
				keywordResults[i] = keywordResult{err: cctx.Err(), timeout: true}
				return
			}
			filter, err := s.resolveDocFilter(cctx, target, req.Filters, req.IncludeDeleted)
			if err != nil {
				res := keywordResult{err: err}
				if cctx.Err() != nil {
					res.timeout = true
				}
				keywordResults[i] = res
				return
			}
			if filter.Restricted && len(filter.Allowed) == 0 {
				keywordResults[i] = keywordResult{filter: filter}
				return
			}
			hits, err := s.keywordRecall(cctx, target, filter, nq)
			if cctx.Err() != nil {
				// Budget expired during recall: the answer is partial by
				// contract, whatever the driver returned.
				keywordResults[i] = keywordResult{filter: filter, err: cctx.Err(), timeout: true}
				return
			}
			keywordResults[i] = keywordResult{hits: hits, filter: filter, err: err}
		}(i, target)
	}

	vec, embedReason := s.queryEmbedding(ctx, nq)
	keywordWG.Wait()
	keywordDur := time.Since(keywordStart)

	for i := range keywordResults {
		res := keywordResults[i]
		if res.err == nil {
			continue
		}
		if res.timeout {
			// A per-target budget expiry never fails the whole search: the
			// remaining targets answer and the response is partial (§5.1/A11).
			markPartial()
			addDegrade(DegradeProjectTimeout)
			if s.cfg.Logger != nil {
				s.cfg.Logger.Warn("search: 项目检索超时", "project", targets[i].projectID, "error", res.err)
			}
			continue
		}
		if req.Scope.Kind == ScopeProject {
			return SearchResponse{}, res.err
		}
		markPartial()
		if s.cfg.Logger != nil {
			s.cfg.Logger.Warn("search: 项目检索失败", "project", targets[i].projectID, "error", res.err)
		}
	}
	if embedReason != "" {
		addDegrade(embedReason)
	}

	// Phase 2: vector recall per target, using the filters resolved above.
	vectorStart := time.Now()
	vectorResults := make([][]store.ChunkHit, len(targets))
	if len(vec) > 0 {
		var vectorWG sync.WaitGroup
		for i, target := range targets {
			if keywordResults[i].err != nil {
				continue // the target already failed or timed out in phase 1
			}
			vectorWG.Add(1)
			go func(i int, target *searchTarget) {
				defer vectorWG.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				cctx, stop := targetContext(fanCtx, s.budgetFor(target))
				defer stop()
				filter := keywordResults[i].filter
				if filter.Restricted && len(filter.Allowed) == 0 {
					return
				}
				hits, err := s.vectorRecall(cctx, target, filter, vec, nq)
				if cctx.Err() != nil {
					if req.Scope.Kind != ScopeProject {
						markPartial()
						addDegrade(DegradeProjectTimeout)
					}
					return
				}
				if err != nil {
					// A vector failure is a degradation, never a hard error
					// (SPEC-10 §5.1: AI failure must not block search).
					return
				}
				vectorResults[i] = hits
			}(i, target)
		}
		vectorWG.Wait()
	}
	vectorDur := time.Since(vectorStart)

	// Step 3: chunk-level RRF fusion and doc-level aggregation.
	fusionStart := time.Now()
	aggregated := make([][]*docAgg, len(targets))
	for i, target := range targets {
		metas, err := loadDocMetaFromHits(ctx, target, keywordResults[i].hits, vectorResults[i])
		if err != nil {
			if req.Scope.Kind == ScopeProject {
				return SearchResponse{}, err
			}
			markPartial()
			continue
		}
		aggregated[i] = fuseTarget(target, keywordResults[i].hits, vectorResults[i], metas, s.cfg)
	}
	merged := mergeResults(aggregated)
	sortMerged(merged)
	fusionDur := time.Since(fusionStart)

	// Step 4: rerank on the first N documents (SPEC-10 §5.2).
	rerankStart := time.Now()
	if mode != RerankOff {
		if s.cfg.Rerank == nil {
			if mode == RerankForce {
				addDegrade(DegradeRerankUnavailable)
			}
		} else if len(merged) > 0 {
			reason := s.rerank(ctx, nq.Raw, merged, mode)
			if reason != "" {
				addDegrade(reason)
			}
		}
	}
	rerankDur := time.Since(rerankStart)

	// Step 5: result assembly and pagination.
	epoch, err := s.indexEpoch(ctx, targets)
	if err != nil {
		return SearchResponse{}, err
	}
	total := len(merged)
	page := paginate(merged, req.Offset, req.Limit)
	results := make([]SearchResult, 0, len(page))
	for _, m := range page {
		result, err := s.assembleResult(ctx, m, nq.Terms)
		if err != nil {
			if req.Scope.Kind == ScopeProject {
				return SearchResponse{}, err
			}
			if s.cfg.Logger != nil {
				s.cfg.Logger.Warn("search: 组装结果失败", "doc", m.sortedKey(), "error", err)
			}
			continue
		}
		results = append(results, result)
	}
	mu.Lock()
	reasons := append([]DegradeReason(nil), degraded...)
	isPartial := partial
	mu.Unlock()
	return SearchResponse{
		Results:  results,
		Total:    total,
		HasMore:  req.Offset+len(results) < total,
		Partial:  isPartial,
		Degraded: reasons,
		Timings: s.timings(start, map[string]time.Duration{
			"normalize": normalizeDur,
			"keyword":   keywordDur,
			"vector":    vectorDur,
			"fusion":    fusionDur,
			"rerank":    rerankDur,
		}),
		IndexEpoch: epoch,
	}, nil
}

// targets resolves the participating databases of a request.
func (s *Service) targets(ctx context.Context, req SearchRequest) ([]*searchTarget, error) {
	if req.Scope.Kind == ScopeProject {
		name, archived, err := s.projectDisplay(ctx, req.Scope.ProjectID)
		if err != nil {
			return nil, err
		}
		handle, err := s.ws.Project(ctx, req.Scope.ProjectID)
		if err != nil {
			return nil, WrapError(CodeScopeNotFound, fmt.Sprintf("打开项目 %s 失败", req.Scope.ProjectID), err)
		}
		out := []*searchTarget{{
			projectID: req.Scope.ProjectID,
			name:      name,
			archived:  archived,
			db:        handle.DB(),
			partition: store.PartitionProject,
			tables:    tablesFor(store.PartitionProject),
		}}
		linked, err := s.linkedPaperDocIDs(ctx, req.Scope.ProjectID)
		if err != nil {
			return nil, err
		}
		if len(linked) > 0 {
			out = append(out, s.globalTarget(linked))
		}
		return out, nil
	}
	rows, err := s.ws.Global().DB().QueryContext(ctx,
		`SELECT id, name, status FROM projects ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("search: 枚举项目: %w", err)
	}
	type projectRow struct {
		id, name, status string
	}
	var projects []projectRow
	for rows.Next() {
		var p projectRow
		if err := rows.Scan(&p.id, &p.name, &p.status); err != nil {
			rows.Close()
			return nil, fmt.Errorf("search: 枚举项目: %w", err)
		}
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("search: 枚举项目: %w", err)
	}
	rows.Close()
	var out []*searchTarget
	for _, p := range projects {
		// Archived projects always participate and are flagged on the result
		// (SPEC-00 §3, SPEC-10 §5.1, A10).
		handle, err := s.ws.Project(ctx, p.id)
		if err != nil {
			if s.cfg.Logger != nil {
				s.cfg.Logger.Warn("search: 打开项目失败，已跳过", "project", p.id, "error", err)
			}
			continue
		}
		out = append(out, &searchTarget{
			projectID: p.id,
			name:      p.name,
			archived:  p.status == "archived",
			db:        handle.DB(),
			partition: store.PartitionProject,
			tables:    tablesFor(store.PartitionProject),
		})
	}
	out = append(out, s.globalTarget(nil))
	return out, nil
}

// globalTarget builds the global literature index target.
func (s *Service) globalTarget(restrict []string) *searchTarget {
	return &searchTarget{
		db:           s.ws.Global().DB(),
		partition:    store.PartitionGlobal,
		tables:       tablesFor(store.PartitionGlobal),
		restrictDocs: restrict,
	}
}

// budgetFor returns the per-target timeout. A non-positive value is defined as
// "budget already exhausted", which lets callers (and tests) force the timeout
// deterministically instead of relying on timer granularity.
func (s *Service) budgetFor(t *searchTarget) time.Duration {
	if s.cfg.projectBudget != nil {
		return s.cfg.projectBudget(t.projectID)
	}
	return s.cfg.projectTimeout
}

// targetContext applies the per-target budget. A non-positive budget yields an
// already canceled context so the timeout path never depends on timing.
func targetContext(parent context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	if budget <= 0 {
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, func() {}
	}
	return context.WithTimeout(parent, budget)
}

// toIndexFilter converts the doc filter to the storage primitive filter.
func toIndexFilter(f docFilter) store.IndexFilter {
	out := store.IndexFilter{ExcludeDocIDs: f.Excluded}
	if f.Restricted {
		out.DocIDs = f.Allowed
	}
	return out
}

// keywordRecall runs the keyword query and enforces the quoted-phrase must
// conditions (the storage primitive is OR-based by design).
func (s *Service) keywordRecall(ctx context.Context, t *searchTarget, filter docFilter, nq normalizedQuery) ([]store.ChunkHit, error) {
	kw, err := store.NewKeywordIndex(t.db, t.partition, s.cfg.Clock)
	if err != nil {
		return nil, WrapError(CodeIndexUnavailable, "打开关键词索引失败", err)
	}
	hits, err := kw.Query(ctx, t.projectID, nq.retrievalText(), s.cfg.keywordTopK, toIndexFilter(filter))
	if err != nil {
		return nil, WrapError(CodeIndexUnavailable, "关键词检索失败", err)
	}
	if len(nq.Phrases) == 0 {
		return hits, nil
	}
	out := hits[:0]
	for _, hit := range hits {
		if containsPhrases(foldRunes(hit.Text), nq.Phrases) {
			out = append(out, hit)
		}
	}
	return out, nil
}

// vectorRecall runs the semantic query with two-times oversampling so pushed
// filters cannot starve the vector list (SPEC-10 §5.2 step 3).
func (s *Service) vectorRecall(ctx context.Context, t *searchTarget, filter docFilter, vec []float32, nq normalizedQuery) ([]store.ChunkHit, error) {
	profileID, _, ok := s.profile(ctx)
	if !ok {
		return nil, nil
	}
	vectorIndex, err := store.NewVectorIndex(t.db, t.partition, s.cfg.Clock)
	if err != nil {
		return nil, WrapError(CodeIndexUnavailable, "打开向量索引失败", err)
	}
	hits, err := vectorIndex.Query(ctx, profileID, vec, s.cfg.vectorTopK*2, toIndexFilter(filter))
	if err != nil {
		return nil, WrapError(CodeIndexUnavailable, "向量检索失败", err)
	}
	if len(nq.Phrases) == 0 {
		return hits, nil
	}
	out := hits[:0]
	for _, hit := range hits {
		if containsPhrases(foldRunes(hit.Text), nq.Phrases) {
			out = append(out, hit)
		}
	}
	return out, nil
}

// queryEmbedding embeds the normalized query; an empty slice means semantic
// recall is unavailable and the reason is reported as a degradation.
func (s *Service) queryEmbedding(ctx context.Context, nq normalizedQuery) ([]float32, DegradeReason) {
	if s.cfg.Embedding == nil {
		return nil, DegradeVectorUnavailable
	}
	if _, _, ok := s.profile(ctx); !ok {
		return nil, DegradeVectorUnavailable
	}
	embedCtx, cancel := context.WithTimeout(ctx, s.cfg.rerankTimeout)
	defer cancel()
	resp, err := s.cfg.Embedding.Embed(embedCtx, []string{nq.retrievalText()})
	if err != nil {
		if s.cfg.Logger != nil {
			s.cfg.Logger.Warn("search: 查询向量化失败", "error", err)
		}
		return nil, DegradeEmbeddingFailed
	}
	if len(resp.Vectors) != 1 || len(resp.Vectors[0]) == 0 {
		return nil, DegradeEmbeddingFailed
	}
	return resp.Vectors[0], ""
}

// loadDocMetaFromHits loads the metadata of every doc appearing in the hits.
func loadDocMetaFromHits(ctx context.Context, t *searchTarget, lists ...[]store.ChunkHit) (map[string]docMeta, error) {
	seen := map[string]bool{}
	var ids []string
	for _, list := range lists {
		for _, hit := range list {
			if !seen[hit.DocID] {
				seen[hit.DocID] = true
				ids = append(ids, hit.DocID)
			}
		}
	}
	return loadDocMeta(ctx, t, ids)
}

// chunkAgg is one fused chunk.
type chunkAgg struct {
	hit         store.ChunkHit
	score       float64
	keywordRank int
	vectorRank  int
}

// docAgg is one document with its best chunks.
type docAgg struct {
	target      *searchTarget
	meta        docMeta
	chunks      []*chunkAgg
	score       float64
	keywordRank int
	vectorRank  int
	primary     bool // full-text doc of a paper group
}

// fuseTarget applies chunk-level RRF and aggregates to documents.
func fuseTarget(t *searchTarget, keywordHits, vectorHits []store.ChunkHit, metas map[string]docMeta, cfg config) []*docAgg {
	byChunk := map[string]*chunkAgg{}
	order := make([]string, 0, len(keywordHits)+len(vectorHits))
	get := func(hit store.ChunkHit) *chunkAgg {
		if c, ok := byChunk[hit.ChunkID]; ok {
			return c
		}
		c := &chunkAgg{hit: hit}
		byChunk[hit.ChunkID] = c
		order = append(order, hit.ChunkID)
		return c
	}
	for i, hit := range keywordHits {
		c := get(hit)
		c.keywordRank = i + 1
		c.score += cfg.keywordWeight / float64(cfg.rrfK+c.keywordRank)
	}
	for i, hit := range vectorHits {
		c := get(hit)
		c.vectorRank = i + 1
		c.score += cfg.vectorWeight / float64(cfg.rrfK+c.vectorRank)
	}
	byDoc := map[string]*docAgg{}
	var order2 []string
	for _, chunkID := range order {
		c := byChunk[chunkID]
		d, ok := byDoc[c.hit.DocID]
		if !ok {
			d = &docAgg{target: t, meta: metas[c.hit.DocID]}
			byDoc[c.hit.DocID] = d
			order2 = append(order2, c.hit.DocID)
		}
		d.chunks = append(d.chunks, c)
	}
	out := make([]*docAgg, 0, len(order2))
	for _, docID := range order2 {
		d := byDoc[docID]
		sort.SliceStable(d.chunks, func(i, j int) bool {
			if d.chunks[i].score != d.chunks[j].score {
				return d.chunks[i].score > d.chunks[j].score
			}
			return d.chunks[i].hit.ChunkID < d.chunks[j].hit.ChunkID
		})
		if len(d.chunks) > 3 {
			d.chunks = d.chunks[:3]
		}
		for _, c := range d.chunks {
			if c.score > d.score {
				d.score = c.score
			}
			if c.keywordRank > 0 && (d.keywordRank == 0 || c.keywordRank < d.keywordRank) {
				d.keywordRank = c.keywordRank
			}
			if c.vectorRank > 0 && (d.vectorRank == 0 || c.vectorRank < d.vectorRank) {
				d.vectorRank = c.vectorRank
			}
		}
		d.primary = d.meta.Source == string(SourcePaperFullText)
		out = append(out, d)
	}
	return out
}

// mergedResult groups one document; a global paper may merge its full-text and
// metadata documents into one literature result (SPEC-10 §6.3).
type mergedResult struct {
	docs        []*docAgg
	score       float64
	keywordRank int
	vectorRank  int
	rerank      *float64
}

// sortedKey is the deterministic tie-break key.
func (m *mergedResult) sortedKey() string {
	if len(m.docs) == 0 {
		return ""
	}
	return m.docs[0].target.key() + "/" + m.docs[0].meta.DocID
}

// mergeResults combines per-target document aggregations, merging the two
// documents of one global paper.
func mergeResults(aggregated [][]*docAgg) []*mergedResult {
	groups := map[string]*mergedResult{}
	var order []string
	for _, docs := range aggregated {
		for _, d := range docs {
			key := d.target.key() + "/" + d.meta.DocID
			if d.target.partition == store.PartitionGlobal && d.meta.EntityType == string(EntityPaper) {
				key = "global/paper:" + d.meta.EntityID
			}
			m, ok := groups[key]
			if !ok {
				m = &mergedResult{}
				groups[key] = m
				order = append(order, key)
			}
			m.docs = append(m.docs, d)
			if d.score > m.score {
				m.score = d.score
			}
			if d.keywordRank > 0 && (m.keywordRank == 0 || d.keywordRank < m.keywordRank) {
				m.keywordRank = d.keywordRank
			}
			if d.vectorRank > 0 && (m.vectorRank == 0 || d.vectorRank < m.vectorRank) {
				m.vectorRank = d.vectorRank
			}
		}
	}
	out := make([]*mergedResult, 0, len(order))
	for _, key := range order {
		m := groups[key]
		// Full-text documents lead their group so snippets prefer PDF text.
		sort.SliceStable(m.docs, func(i, j int) bool { return m.docs[i].primary && !m.docs[j].primary })
		out = append(out, m)
	}
	return out
}

// sortMerged orders by fusion score descending with a stable key tie-break.
func sortMerged(merged []*mergedResult) {
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].score != merged[j].score {
			return merged[i].score > merged[j].score
		}
		return merged[i].sortedKey() < merged[j].sortedKey()
	})
}

// rerank reorders the first N merged results. A failure or timeout keeps the
// fusion order and returns the degradation reason (SPEC-10 §5.2).
func (s *Service) rerank(ctx context.Context, query string, merged []*mergedResult, mode RerankMode) DegradeReason {
	n := min(s.cfg.rerankTopN, len(merged))
	if n == 0 {
		return ""
	}
	docs := make([]string, 0, n)
	for _, m := range merged[:n] {
		docs = append(docs, m.rerankText())
	}
	rctx, cancel := context.WithTimeout(ctx, s.cfg.rerankTimeout)
	defer cancel()
	resp, err := s.cfg.Rerank.Rerank(rctx, query, docs, n)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || rctx.Err() != nil {
			return DegradeRerankTimeout
		}
		return DegradeRerankUnavailable
	}
	if len(resp.Hits) == 0 {
		return DegradeRerankUnavailable
	}
	scores := make(map[int]float64, len(resp.Hits))
	for _, hit := range resp.Hits {
		if hit.Index >= 0 && hit.Index < n {
			scores[hit.Index] = hit.Score
		}
	}
	head := append([]*mergedResult(nil), merged[:n]...)
	headScores := make(map[*mergedResult]float64, len(head))
	for i, m := range head {
		if score, ok := scores[i]; ok {
			v := score
			m.rerank = &v
			headScores[m] = score
		}
	}
	sort.SliceStable(head, func(i, j int) bool {
		si, iok := headScores[head[i]]
		sj, jok := headScores[head[j]]
		switch {
		case iok && jok:
			if si != sj {
				return si > sj
			}
			return head[i].score > head[j].score
		case iok:
			return true
		case jok:
			return false
		default:
			return head[i].score > head[j].score
		}
	})
	copy(merged[:n], head)
	return ""
}

// rerankText builds the rerank input of one merged result.
func (m *mergedResult) rerankText() string {
	var b strings.Builder
	if len(m.docs) > 0 {
		b.WriteString(m.docs[0].meta.Title)
	}
	count := 0
	for _, d := range m.docs {
		for _, c := range d.chunks {
			if count >= 2 {
				break
			}
			b.WriteString("\n")
			b.WriteString(c.hit.Text)
			count++
		}
	}
	return b.String()
}

// paginate slices the merged list.
func paginate(merged []*mergedResult, offset, limit int) []*mergedResult {
	if offset >= len(merged) {
		return nil
	}
	end := min(offset+limit, len(merged))
	return merged[offset:end]
}

// assembleResult builds one SearchResult from a merged group.
func (s *Service) assembleResult(ctx context.Context, m *mergedResult, terms []string) (SearchResult, error) {
	if len(m.docs) == 0 {
		return SearchResult{}, errors.New("search: empty result group")
	}
	primary := m.docs[0]
	best := bestChunk(m)
	meta := primary.meta
	result := SearchResult{
		DocID:       meta.DocID,
		Source:      SourceType(meta.Source),
		EntityType:  EntityType(meta.EntityType),
		EntityID:    meta.EntityID,
		ProjectID:   primary.target.projectID,
		ProjectName: primary.target.name,
		Archived:    primary.target.archived,
		Deleted:     meta.Status == StatusDeleted,
		Title:       meta.Title,
		Locator:     locatorFromJSON(best.hit.LocatorJSON),
		Score: Score{
			KeywordRank: m.keywordRank,
			VectorRank:  m.vectorRank,
			Fusion:      m.score,
			Rerank:      m.rerank,
		},
		UpdatedAt: meta.UpdatedAt,
	}
	for _, d := range m.docs {
		if d.meta.UpdatedAt.After(result.UpdatedAt) {
			result.UpdatedAt = d.meta.UpdatedAt
		}
		if d.meta.Status == StatusDeleted {
			result.Deleted = true
		}
	}
	if strings.TrimSpace(result.Title) == "" && len(best.hit.Text) > 0 {
		result.Title = truncateRunes(best.hit.Text, 80)
	}
	for _, d := range m.docs {
		for _, c := range d.chunks {
			if len(result.Snippets) >= 3 {
				break
			}
			result.Snippets = append(result.Snippets, buildSnippet(c.hit.Text, terms, s.cfg.snippetRunes, locatorFromJSON(c.hit.LocatorJSON)))
		}
	}
	target, err := s.jumpTarget(ctx, primary.target, result)
	if err != nil {
		return SearchResult{}, err
	}
	result.Target = target
	return result, nil
}

// bestChunk returns the highest-scoring chunk of a group.
func bestChunk(m *mergedResult) *chunkAgg {
	var best *chunkAgg
	for _, d := range m.docs {
		for _, c := range d.chunks {
			if best == nil || c.score > best.score {
				best = c
			}
		}
	}
	if best == nil {
		return &chunkAgg{}
	}
	return best
}

// buildSnippet trims one chunk to maxRunes around the first match and computes
// rune-range highlights (SPEC-10 §5.2 step 5).
func buildSnippet(text string, terms []string, maxRunes int, loc Locator) Snippet {
	runes := []rune(text)
	if len(runes) == 0 {
		return Snippet{Locator: loc}
	}
	folded := make([]rune, len(runes))
	for i, r := range runes {
		folded[i] = unicode.ToLower(r)
	}
	first := -1
	for _, term := range terms {
		tr := []rune(strings.ToLower(term))
		if len(tr) == 0 {
			continue
		}
		if idx := indexRunes(folded, tr); idx >= 0 && (first < 0 || idx < first) {
			first = idx
		}
	}
	start := 0
	if first > maxRunes/4 {
		start = first - maxRunes/4
	}
	end := min(start+maxRunes, len(runes))
	if end-start < maxRunes && start > 0 {
		start = max(0, end-maxRunes)
	}
	window := runes[start:end]
	windowFolded := folded[start:end]
	var highlights []Range
	for _, term := range terms {
		tr := []rune(strings.ToLower(term))
		if len(tr) == 0 {
			continue
		}
		for offset := 0; offset+len(tr) <= len(windowFolded); {
			idx := indexRunes(windowFolded[offset:], tr)
			if idx < 0 {
				break
			}
			begin := offset + idx
			highlights = append(highlights, Range{Start: begin, End: begin + len(tr)})
			offset = begin + len(tr)
		}
	}
	sort.Slice(highlights, func(i, j int) bool {
		if highlights[i].Start != highlights[j].Start {
			return highlights[i].Start < highlights[j].Start
		}
		return highlights[i].End < highlights[j].End
	})
	highlights = mergeRanges(highlights)
	return Snippet{Text: string(window), Highlights: highlights, Locator: loc}
}

// indexRunes returns the first index of needle in haystack, or -1.
func indexRunes(haystack, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// mergeRanges merges overlapping highlight ranges.
func mergeRanges(in []Range) []Range {
	if len(in) == 0 {
		return nil
	}
	out := []Range{in[0]}
	for _, r := range in[1:] {
		last := &out[len(out)-1]
		if r.Start <= last.End {
			if r.End > last.End {
				last.End = r.End
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// foldRunes lowercases rune by rune, preserving rune count and offsets.
func foldRunes(s string) string {
	runes := []rune(s)
	for i, r := range runes {
		runes[i] = unicode.ToLower(r)
	}
	return string(runes)
}

// truncateRunes shortens s to n runes.
func truncateRunes(s string, n int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= n {
		return string(runes)
	}
	return string(runes[:n]) + "…"
}

// browse implements the empty-query mode: updated_at descending, no vector.
func (s *Service) browse(ctx context.Context, req SearchRequest, targets []*searchTarget, start time.Time) (SearchResponse, error) {
	need := req.Offset + req.Limit
	type browseRow struct {
		target *searchTarget
		meta   docMeta
	}
	var rows []browseRow
	total := 0
	for _, target := range targets {
		filter, err := s.resolveDocFilter(ctx, target, req.Filters, req.IncludeDeleted)
		if err != nil {
			if req.Scope.Kind == ScopeProject {
				return SearchResponse{}, err
			}
			continue
		}
		items, count, err := s.browseDocs(ctx, target, filter, req.IncludeDeleted, need)
		if err != nil {
			if req.Scope.Kind == ScopeProject {
				return SearchResponse{}, err
			}
			continue
		}
		total += count
		for _, item := range items {
			rows = append(rows, browseRow{target: target, meta: item})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].meta.UpdatedAt.Equal(rows[j].meta.UpdatedAt) {
			return rows[i].meta.UpdatedAt.After(rows[j].meta.UpdatedAt)
		}
		return rows[i].meta.DocID < rows[j].meta.DocID
	})
	page := rows
	if req.Offset < len(page) {
		page = page[req.Offset:]
	} else {
		page = nil
	}
	if len(page) > req.Limit {
		page = page[:req.Limit]
	}
	results := make([]SearchResult, 0, len(page))
	for _, row := range page {
		result := SearchResult{
			DocID:       row.meta.DocID,
			Source:      SourceType(row.meta.Source),
			EntityType:  EntityType(row.meta.EntityType),
			EntityID:    row.meta.EntityID,
			ProjectID:   row.target.projectID,
			ProjectName: row.target.name,
			Archived:    row.target.archived,
			Deleted:     row.meta.Status == StatusDeleted,
			Title:       row.meta.Title,
			UpdatedAt:   row.meta.UpdatedAt,
		}
		target, err := s.jumpTarget(ctx, row.target, result)
		if err != nil {
			continue
		}
		result.Target = target
		results = append(results, result)
	}
	epoch, err := s.indexEpoch(ctx, targets)
	if err != nil {
		return SearchResponse{}, err
	}
	return SearchResponse{
		Results:    results,
		Total:      total,
		HasMore:    req.Offset+len(results) < total,
		Timings:    s.timings(start, map[string]time.Duration{}),
		IndexEpoch: epoch,
	}, nil
}

// browseDocs returns one page of documents ordered by updated_at descending
// plus the unpaged count of the target.
func (s *Service) browseDocs(ctx context.Context, t *searchTarget, filter docFilter, includeDeleted bool, limit int) ([]docMeta, int, error) {
	var (
		conds []string
		args  []any
	)
	if t.partition == store.PartitionProject {
		conds = append(conds, "project_id = ?")
		args = append(args, t.projectID)
	}
	if filter.Restricted {
		if len(filter.Allowed) == 0 {
			return nil, 0, nil
		}
		conds = append(conds, "doc_id IN ("+placeholders(len(filter.Allowed))+")")
		for _, id := range filter.Allowed {
			args = append(args, id)
		}
	}
	if !includeDeleted {
		conds = append(conds, "index_status <> ?")
		args = append(args, StatusDeleted)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var count int
	if err := t.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM %s%s`, t.tables.docs, where), args...).Scan(&count); err != nil {
		return nil, 0, fmt.Errorf("search: 浏览计数: %w", err)
	}
	query := fmt.Sprintf(`SELECT doc_id, source, entity_type, entity_id, title, lang, updated_at, index_status
		FROM %s%s ORDER BY updated_at DESC, doc_id LIMIT ?`, t.tables.docs, where)
	args = append(args, limit)
	rows, err := t.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("search: 浏览文档: %w", err)
	}
	defer rows.Close()
	var out []docMeta
	for rows.Next() {
		var (
			m       docMeta
			updated string
		)
		if err := rows.Scan(&m.DocID, &m.Source, &m.EntityType, &m.EntityID, &m.Title, &m.Lang, &updated, &m.Status); err != nil {
			return nil, 0, fmt.Errorf("search: 浏览文档: %w", err)
		}
		if ts, err := clock.Parse(updated); err == nil {
			m.UpdatedAt = ts
		}
		out = append(out, m)
	}
	return out, count, rows.Err()
}

// indexEpoch derives a stable epoch string from the involved index batches.
func (s *Service) indexEpoch(ctx context.Context, targets []*searchTarget) (string, error) {
	var parts []string
	for _, t := range targets {
		var (
			count       int
			lastIndexed sql.NullString
		)
		if err := t.db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COUNT(*), MAX(indexed_at) FROM %s`, t.tables.docs)).
			Scan(&count, &lastIndexed); err != nil {
			return "", fmt.Errorf("search: 索引批次: %w", err)
		}
		parts = append(parts, fmt.Sprintf("%s:%d:%s", t.key(), count, lastIndexed.String))
	}
	return hashText(strings.Join(parts, "|"))[:16], nil
}

// timings converts the measured durations to the documented millisecond map.
func (s *Service) timings(start time.Time, stages map[string]time.Duration) map[string]int64 {
	out := map[string]int64{}
	for key, d := range stages {
		out[key] = d.Milliseconds()
	}
	out["total"] = time.Since(start).Milliseconds()
	return out
}
