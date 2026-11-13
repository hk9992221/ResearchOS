-- ============================================================
-- SPEC-02 迁移带 0100–0199（global.db）——文献索引存储原语表组。
-- 表结构与命名按 SPEC-10 §4.2（N1/N3 并入，SPEC-00 v1.1 裁决）：
-- paper_index_docs / paper_chunks / paper_fts / paper_chunk_vectors。
-- 源注册、脏队列与索引作业（paper_index_jobs 等）归 SPEC-10 迁移，本带不建。
-- SPEC-02 的 internal/index/store 只读写本表组（§5.10）。
-- ============================================================

CREATE TABLE paper_index_docs (
  doc_id             TEXT NOT NULL PRIMARY KEY,
  source             TEXT NOT NULL,
  entity_type        TEXT NOT NULL,
  entity_id          TEXT NOT NULL,
  title              TEXT NOT NULL DEFAULT '',
  lang               TEXT NOT NULL DEFAULT '',
  content_hash       TEXT NOT NULL DEFAULT '',
  updated_at         TEXT NOT NULL,
  schema_version     INTEGER NOT NULL DEFAULT 1,   -- N3
  parser_version     TEXT NOT NULL DEFAULT '',     -- N3（由 SPEC-10 维护）
  embedding_model_id TEXT NOT NULL DEFAULT '',     -- N3（由 SPEC-10 维护）
  embedding_dim      INTEGER NOT NULL DEFAULT 0,   -- N3（由 SPEC-10 维护）
  index_status       TEXT NOT NULL DEFAULT '',     -- N3（由 SPEC-10 维护）
  indexed_at         TEXT,                         -- N3（由 SPEC-10 维护）
  error              TEXT                          -- N3（由 SPEC-10 维护）
);
CREATE INDEX idx_paper_index_docs_entity ON paper_index_docs(entity_type, entity_id);

CREATE TABLE paper_chunks (
  chunk_id     TEXT NOT NULL PRIMARY KEY,
  doc_id       TEXT NOT NULL REFERENCES paper_index_docs(doc_id) ON DELETE CASCADE,
  ordinal      INTEGER NOT NULL,
  text         TEXT NOT NULL,
  locator_json TEXT NOT NULL DEFAULT '',           -- 页码/字符区间，store 不解释（AC-08）
  content_hash TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_paper_chunks_doc ON paper_chunks(doc_id, ordinal);

-- 关键词索引：FTS5 trigram 分词（O-02 spike 结论：modernc SQLite 3.54 内置 FTS5
-- 与 trigram；不足 3 字的 CJK 查询由 store 以 LIKE 回退实现）。
CREATE VIRTUAL TABLE paper_fts USING fts5(
  chunk_id UNINDEXED,
  body,
  tokenize='trigram'
);

-- 向量索引：按 profile 隔离（SPEC-02 §5.8 EmbeddingProfileID）；
-- vector 为 float32 little-endian 紧凑编码（O-03 spike 结论：v1 暴力 cosine）。
CREATE TABLE paper_chunk_vectors (
  profile_id TEXT NOT NULL,
  doc_id     TEXT NOT NULL,                          -- 归属冗余列：向量可按文档独立回收（无级联外键）
  chunk_id   TEXT NOT NULL,
  dim        INTEGER NOT NULL CHECK (dim > 0),
  vector     BLOB NOT NULL,
  PRIMARY KEY (profile_id, chunk_id)
);
CREATE INDEX idx_paper_chunk_vectors_chunk ON paper_chunk_vectors(chunk_id);
CREATE INDEX idx_paper_chunk_vectors_doc ON paper_chunk_vectors(doc_id);
