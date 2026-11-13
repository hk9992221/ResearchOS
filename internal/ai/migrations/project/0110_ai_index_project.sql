-- ============================================================
-- SPEC-02 迁移带 0100–0199（project.db）——项目检索索引存储表组。
-- 表结构与命名按 SPEC-10 §4.2（N2/N3 并入，SPEC-00 v1.1 裁决）：
-- index_docs / index_chunks / index_chunks_fts / chunk_embeddings。
-- 源注册（index_sources）、脏队列与作业表归 SPEC-10 迁移，本带不建。
-- ============================================================

CREATE TABLE index_docs (
  doc_id             TEXT NOT NULL PRIMARY KEY,
  project_id         TEXT NOT NULL,
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
CREATE INDEX idx_index_docs_entity ON index_docs(entity_type, entity_id);
CREATE INDEX idx_index_docs_project ON index_docs(project_id);

CREATE TABLE index_chunks (
  chunk_id     TEXT NOT NULL PRIMARY KEY,
  doc_id       TEXT NOT NULL REFERENCES index_docs(doc_id) ON DELETE CASCADE,
  ordinal      INTEGER NOT NULL,
  text         TEXT NOT NULL,
  locator_json TEXT NOT NULL DEFAULT '',           -- store 不解释（AC-08）
  content_hash TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_index_chunks_doc ON index_chunks(doc_id, ordinal);

-- 关键词索引（O-02：FTS5 trigram + LIKE 回退，见 global 0110 注释）。
CREATE VIRTUAL TABLE index_chunks_fts USING fts5(
  chunk_id UNINDEXED,
  body,
  tokenize='trigram'
);

-- 向量索引：按 profile 隔离（SPEC-02 §5.8/§5.10）。
CREATE TABLE chunk_embeddings (
  profile_id TEXT NOT NULL,
  doc_id     TEXT NOT NULL,                          -- 归属冗余列：向量可按文档独立回收（无级联外键）
  chunk_id   TEXT NOT NULL,
  dim        INTEGER NOT NULL CHECK (dim > 0),
  vector     BLOB NOT NULL,                        -- float32 little-endian
  PRIMARY KEY (profile_id, chunk_id)
);
CREATE INDEX idx_chunk_embeddings_chunk ON chunk_embeddings(chunk_id);
CREATE INDEX idx_chunk_embeddings_doc ON chunk_embeddings(doc_id);
