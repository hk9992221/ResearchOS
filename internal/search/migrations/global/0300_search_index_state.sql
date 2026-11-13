-- ============================================================
-- SPEC-10 迁移带 0300–0349（global.db）——全局文献索引运行态表。
-- 编排者裁决：SPEC-01 §10.4 的 0300–0399 段按 0300–0349（SPEC-10）与
-- 0350–0399（SPEC-11）拆半，避免版本号冲突，见 project/0300 头注释。
--
-- 存储表组 paper_index_docs / paper_chunks / paper_fts / paper_chunk_vectors
-- 由 SPEC-02 的 0110 迁移建立，本带不重建、不改名；paper_index_docs 的
-- parser_version / embedding_model_id / embedding_dim / index_status /
-- indexed_at / error 列由 SPEC-10 维护（SPEC-10 §4.4 N1/N3）。
-- 时间列一律 clock.Format（UTC RFC3339 毫秒），见 SPEC-01 §4.1.2。
-- ============================================================

-- 全局文献源注册（paper_metadata / paper_fulltext）。
CREATE TABLE paper_index_sources (
  source         TEXT NOT NULL PRIMARY KEY,
  enabled        INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  schema_version INTEGER NOT NULL DEFAULT 1,
  parser_version TEXT NOT NULL DEFAULT '',
  updated_at     TEXT NOT NULL
);

INSERT INTO paper_index_sources (source, enabled, schema_version, parser_version, updated_at) VALUES
  ('paper_metadata', 1, 1, '', '1970-01-01T00:00:00.000Z'),
  ('paper_fulltext', 1, 1, '', '1970-01-01T00:00:00.000Z');

-- PDF 解析断点：文件哈希与解析器版本未变时跳过重复解析（SPEC-10 §3.2、
-- §5.3、§5.5「文件哈希变化才重新解析」）。解析结果本身只存索引表。
CREATE TABLE paper_parse_state (
  paper_id       TEXT NOT NULL PRIMARY KEY,
  file_hash      TEXT NOT NULL DEFAULT '',
  parser_version TEXT NOT NULL,
  page_count     INTEGER NOT NULL DEFAULT 0,
  parsed_at      TEXT NOT NULL
);

-- 持久化脏队列（global 侧）：同一 Paper 只保留一行。
CREATE TABLE paper_index_dirty (
  project_id  TEXT NOT NULL DEFAULT '',
  entity_type TEXT NOT NULL,
  entity_id   TEXT NOT NULL,
  op          TEXT NOT NULL CHECK (op IN ('upsert','delete','restore')),
  attempts    INTEGER NOT NULL DEFAULT 0,
  run_after   TEXT NOT NULL,
  last_error  TEXT,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  PRIMARY KEY (project_id, entity_type, entity_id)
);
CREATE INDEX idx_paper_index_dirty_due ON paper_index_dirty(run_after);

-- 全局文献索引作业（SPEC-10 §4.4 N1）。
CREATE TABLE paper_index_jobs (
  job_id           TEXT NOT NULL PRIMARY KEY,
  scope_kind       TEXT NOT NULL CHECK (scope_kind IN ('project','global')),
  scope_id         TEXT NOT NULL DEFAULT '',
  reparse_pdf      INTEGER NOT NULL DEFAULT 0 CHECK (reparse_pdf IN (0,1)),
  rebuild_vector   INTEGER NOT NULL DEFAULT 0 CHECK (rebuild_vector IN (0,1)),
  status           TEXT NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending','running','done','failed','canceled')),
  total            INTEGER NOT NULL DEFAULT 0,
  processed        INTEGER NOT NULL DEFAULT 0,
  failed           INTEGER NOT NULL DEFAULT 0,
  last_error       TEXT,
  cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0,1)),
  created_at       TEXT NOT NULL,
  started_at       TEXT,
  finished_at      TEXT
);
CREATE INDEX idx_paper_index_jobs_status ON paper_index_jobs(status, created_at);