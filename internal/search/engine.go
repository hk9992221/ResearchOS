package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"researchos/internal/foundation/clock"
	"researchos/internal/index/store"
)

// tableSet names the SPEC-10-owned runtime tables of one partition. The
// doc/chunk/FTS/vector tables are created by SPEC-02's 0110 migration.
type tableSet struct {
	docs    string
	chunks  string
	dirty   string
	jobs    string
	sources string
}

// tablesFor returns the table names of one partition.
func tablesFor(partition store.Partition) tableSet {
	if partition == store.PartitionGlobal {
		return tableSet{
			docs:    "paper_index_docs",
			chunks:  "paper_chunks",
			dirty:   "paper_index_dirty",
			jobs:    "paper_index_jobs",
			sources: "paper_index_sources",
		}
	}
	return tableSet{
		docs:    "index_docs",
		chunks:  "index_chunks",
		dirty:   "index_dirty",
		jobs:    "index_jobs",
		sources: "index_sources",
	}
}

// indexScope is one writable index database (a project or global.db).
type indexScope struct {
	key       string // project id, or "global"
	db        *sql.DB
	partition store.Partition
	projectID string // empty for the global partition
	tables    tableSet
}

// scopeKey renders a scope as a stable internal key.
func scopeKey(scope Scope) string {
	if scope.Kind == ScopeGlobal {
		return "global"
	}
	return scope.ProjectID
}

// resolveScope opens the database of a scope and validates that it exists.
func (s *Service) resolveScope(ctx context.Context, scope Scope) (*indexScope, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if scope.Kind == ScopeGlobal {
		global := s.ws.Global()
		if global == nil {
			return nil, Errorf(CodeIndexUnavailable, "工作区已关闭")
		}
		return &indexScope{
			key:       "global",
			db:        global.DB(),
			partition: store.PartitionGlobal,
			tables:    tablesFor(store.PartitionGlobal),
		}, nil
	}
	var status string
	err := s.ws.Global().DB().QueryRowContext(ctx, `SELECT status FROM projects WHERE id = ?`, scope.ProjectID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Errorf(CodeScopeNotFound, "项目 %s 不存在", scope.ProjectID)
	}
	if err != nil {
		return nil, fmt.Errorf("search: 读取项目状态: %w", err)
	}
	handle, err := s.ws.Project(ctx, scope.ProjectID)
	if err != nil {
		return nil, WrapError(CodeScopeNotFound, fmt.Sprintf("打开项目 %s 的索引失败", scope.ProjectID), err)
	}
	return &indexScope{
		key:       scope.ProjectID,
		db:        handle.DB(),
		partition: store.PartitionProject,
		projectID: scope.ProjectID,
		tables:    tablesFor(store.PartitionProject),
	}, nil
}

// docState is the stored state used for the hash/version skip rules.
type docState struct {
	Status         string
	ContentHash    string
	ParserVersion  string
	EmbeddingModel string
	EmbeddingDim   int
}

// readDocState returns the stored state of one document (zero when absent).
func readDocState(ctx context.Context, db *sql.DB, tables tableSet, docID string) (docState, bool, error) {
	var st docState
	err := db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT index_status, content_hash, parser_version, embedding_model_id, embedding_dim
			FROM %s WHERE doc_id = ?`, tables.docs), docID).
		Scan(&st.Status, &st.ContentHash, &st.ParserVersion, &st.EmbeddingModel, &st.EmbeddingDim)
	if errors.Is(err, sql.ErrNoRows) {
		return docState{}, false, nil
	}
	if err != nil {
		return docState{}, false, fmt.Errorf("search: 读取索引状态: %w", err)
	}
	return st, true, nil
}

// writeOptions tunes one index write.
type writeOptions struct {
	// Force ignores the content-hash skip (reparse/rebuild-vector jobs).
	Force bool
}

// profile resolves the current embedding profile; the empty profile and false
// mean vector indexing is unavailable and documents become vector_pending.
func (s *Service) profile(ctx context.Context) (string, int, bool) {
	if s.cfg.Embedding == nil {
		return "", 0, false
	}
	id, dim, err := s.cfg.Embedding.CurrentProfile(ctx)
	if err != nil || id == "" {
		return "", 0, false
	}
	return id, dim, true
}

// applyDoc writes one extracted document. It is called only by the single
// writer of its scope, so FTS and vector writes never race.
func (s *Service) applyDoc(ctx context.Context, sc *indexScope, doc IndexDoc, opt writeOptions) error {
	profileID, _, vectorReady := s.profile(ctx)
	prev, exists, err := readDocState(ctx, sc.db, sc.tables, doc.DocID)
	if err != nil {
		return err
	}
	if !opt.Force && exists && s.canSkip(prev, doc, vectorReady, profileID) {
		return nil
	}
	parserVersion := doc.ParserVersion
	if parserVersion == "" {
		parserVersion = parserVersionMarkdown
	}

	keyword, err := store.NewKeywordIndex(sc.db, sc.partition, s.cfg.Clock)
	if err != nil {
		return WrapError(CodeIndexUnavailable, "打开关键词索引失败", err)
	}
	storeDoc := store.IndexDoc{
		ProjectID:   doc.ProjectID,
		DocID:       doc.DocID,
		Source:      string(doc.Source),
		EntityType:  string(doc.EntityType),
		EntityID:    doc.EntityID,
		Title:       doc.Title,
		ContentHash: doc.ContentHash,
	}
	storeChunks := make([]store.IndexChunk, 0, len(doc.Chunks))
	for _, c := range doc.Chunks {
		locator, err := json.Marshal(c.Locator)
		if err != nil {
			return fmt.Errorf("search: 序列化定位信息: %w", err)
		}
		storeChunks = append(storeChunks, store.IndexChunk{
			ChunkID:     c.ChunkID,
			DocID:       c.DocID,
			ProjectID:   doc.ProjectID,
			Ordinal:     c.Ordinal,
			Text:        c.Text,
			ContentHash: c.ContentHash,
			LocatorJSON: locator,
		})
	}
	if err := keyword.ReplaceDoc(ctx, storeDoc, storeChunks); err != nil {
		return WrapError(CodeIndexWriteFailed, fmt.Sprintf("写入文档 %s 的关键词索引失败", doc.DocID), err)
	}

	status := StatusOK
	var statusErr string
	modelID := ""
	dim := 0
	if doc.NoText {
		status = StatusNoText
	} else if len(doc.Chunks) > 0 {
		if !vectorReady {
			status = StatusVectorPending
			statusErr = "embedding 未配置，已保留关键词索引"
		} else {
			modelID = profileID
			dim, err = s.writeVectors(ctx, sc, doc, profileID)
			if err != nil {
				status = StatusVectorPending
				statusErr = err.Error()
				modelID = ""
				dim = 0
			}
		}
	}
	if err := s.updateDocMeta(ctx, sc, doc, status, parserVersion, modelID, dim, statusErr); err != nil {
		return err
	}
	s.notifyDocIndexed(sc.projectID, doc.DocID, status)
	return nil
}

// canSkip implements the content-hash / version skip rules (§3.2, §5.3).
func (s *Service) canSkip(prev docState, doc IndexDoc, vectorReady bool, profileID string) bool {
	if prev.ContentHash != doc.ContentHash || prev.ParserVersion != doc.ParserVersion {
		return false
	}
	switch prev.Status {
	case StatusOK:
		return !(vectorReady && len(doc.Chunks) > 0 && prev.EmbeddingModel != profileID)
	case StatusNoText:
		return true
	case StatusVectorPending:
		// Retry the vector write once a profile is available; otherwise the
		// keyword index is already complete.
		return !vectorReady
	case StatusDeleted:
		return false
	default:
		return false
	}
}

// writeVectors embeds the document chunks and stores them under profileID.
func (s *Service) writeVectors(ctx context.Context, sc *indexScope, doc IndexDoc, profileID string) (int, error) {
	vectors, err := s.embedChunks(ctx, doc.Chunks)
	if err != nil {
		return 0, err
	}
	vectorIndex, err := store.NewVectorIndex(sc.db, sc.partition, s.cfg.Clock)
	if err != nil {
		return 0, WrapError(CodeIndexUnavailable, "打开向量索引失败", err)
	}
	storeDoc := store.IndexDoc{
		ProjectID:   doc.ProjectID,
		DocID:       doc.DocID,
		Source:      string(doc.Source),
		EntityType:  string(doc.EntityType),
		EntityID:    doc.EntityID,
		Title:       doc.Title,
		ContentHash: doc.ContentHash,
	}
	storeChunks := make([]store.IndexChunk, 0, len(doc.Chunks))
	for _, c := range doc.Chunks {
		locator, _ := json.Marshal(c.Locator)
		storeChunks = append(storeChunks, store.IndexChunk{
			ChunkID: c.ChunkID, DocID: c.DocID, ProjectID: doc.ProjectID,
			Ordinal: c.Ordinal, Text: c.Text, ContentHash: c.ContentHash, LocatorJSON: locator,
		})
	}
	if err := vectorIndex.ReplaceDoc(ctx, profileID, storeDoc, storeChunks, vectors); err != nil {
		return 0, WrapError(CodeIndexWriteFailed, "写入向量索引失败", err)
	}
	if len(vectors) > 0 {
		return len(vectors[0].Vector), nil
	}
	return 0, nil
}

// embedChunks embeds chunk texts in bounded batches (SPEC-02 gateway limits).
func (s *Service) embedChunks(ctx context.Context, chunks []Chunk) ([]store.ChunkVector, error) {
	batch := s.cfg.embedBatchSize
	if batch <= 0 {
		batch = defaultEmbedBatchSize
	}
	out := make([]store.ChunkVector, 0, len(chunks))
	for start := 0; start < len(chunks); start += batch {
		end := min(start+batch, len(chunks))
		texts := make([]string, 0, end-start)
		for _, c := range chunks[start:end] {
			texts = append(texts, c.Text)
		}
		resp, err := s.cfg.Embedding.Embed(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("embedding 失败: %w", err)
		}
		if len(resp.Vectors) != len(texts) {
			return nil, fmt.Errorf("embedding 返回 %d 个向量，期望 %d", len(resp.Vectors), len(texts))
		}
		for i, vec := range resp.Vectors {
			out = append(out, store.ChunkVector{ChunkID: chunks[start+i].ChunkID, Vector: vec})
		}
	}
	return out, nil
}

// updateDocMeta writes the SPEC-10-owned metadata columns. The document
// updated_at is the entity's own timestamp (browse ordering and UpdatedAfter
// filters use it), not the index write time.
func (s *Service) updateDocMeta(ctx context.Context, sc *indexScope, doc IndexDoc, status, parserVersion, modelID string, dim int, errText string) error {
	var errValue any
	if strings.TrimSpace(errText) != "" {
		errValue = errText
	}
	updatedAt := doc.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = s.cfg.Clock.Now()
	}
	projectCond := ""
	args := []any{
		string(doc.Source), string(doc.EntityType), doc.EntityID, doc.Title, doc.Lang,
		status, parserVersion, modelID, dim, clock.Format(s.cfg.Clock.Now()),
		errValue, clock.Format(updatedAt),
	}
	if sc.partition == store.PartitionProject {
		projectCond = ", project_id = ?"
		args = append(args, doc.ProjectID)
	}
	args = append(args, doc.DocID)
	_, err := sc.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET source = ?, entity_type = ?, entity_id = ?, title = ?, lang = ?,
			index_status = ?, parser_version = ?, embedding_model_id = ?, embedding_dim = ?,
			indexed_at = ?, error = ?, updated_at = ?%s WHERE doc_id = ?`, sc.tables.docs, projectCond),
		args...)
	if err != nil {
		return fmt.Errorf("search: 更新索引元数据 %s: %w", doc.DocID, err)
	}
	return nil
}

// markDeleted keeps the document and its chunks but hides it from default
// search (include_deleted can still retrieve it, SPEC-10 §5.1).
func (s *Service) markDeleted(ctx context.Context, sc *indexScope, entityType EntityType, entityID string) error {
	var err error
	if sc.partition == store.PartitionProject {
		_, err = sc.db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET index_status = ?, indexed_at = ?, error = NULL
			 WHERE entity_type = ? AND entity_id = ? AND project_id = ?`, sc.tables.docs),
			StatusDeleted, clock.Format(s.cfg.Clock.Now()), string(entityType), entityID, sc.projectID)
	} else {
		_, err = sc.db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET index_status = ?, indexed_at = ?, error = NULL
			 WHERE entity_type = ? AND entity_id = ?`, sc.tables.docs),
			StatusDeleted, clock.Format(s.cfg.Clock.Now()), string(entityType), entityID)
	}
	if err != nil {
		return fmt.Errorf("search: 标记删除 %s:%s: %w", entityType, entityID, err)
	}
	return nil
}

// markProblem keeps the last index but flags the document index_stale (§3.4).
func (s *Service) markProblem(ctx context.Context, sc *indexScope, docID string, cause error) error {
	message := "正文文件缺失，已保留上次索引"
	if cause != nil {
		message = cause.Error()
	}
	_, err := sc.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET index_status = ?, error = ? WHERE doc_id = ?`, sc.tables.docs),
		StatusFileStale, message, docID)
	if err != nil {
		return fmt.Errorf("search: 标记索引陈旧 %s: %w", docID, err)
	}
	return nil
}

// markFailed records the terminal failure of one entity after retries.
func (s *Service) markFailed(ctx context.Context, sc *indexScope, entityType EntityType, entityID string, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	var err error
	if sc.partition == store.PartitionProject {
		_, err = sc.db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET index_status = ?, error = ? WHERE entity_type = ? AND entity_id = ? AND project_id = ?`, sc.tables.docs),
			StatusFailed, message, string(entityType), entityID, sc.projectID)
	} else {
		_, err = sc.db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET index_status = ?, error = ? WHERE entity_type = ? AND entity_id = ?`, sc.tables.docs),
			StatusFailed, message, string(entityType), entityID)
	}
	if err != nil {
		return fmt.Errorf("search: 标记索引失败: %w", err)
	}
	return nil
}
