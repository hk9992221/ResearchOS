-- ============================================================
-- SPEC-10 迁移带 0300–0349（project.db）——检索模块运行态表。
-- 编排者裁决（SPEC 9–11 并行契约）：SPEC-01 §10.4 原把 0300–0399 整段
-- 留给 SPEC-11；因 SPEC-02 的 0110 迁移注释已把「源注册 / 持久化脏队列 /
-- 索引作业表」划归 SPEC-10，本带只用 0300–0349，0350–0399 留给 SPEC-11。
--
-- 文档/块/FTS/向量表组由 SPEC-02 的 0110 迁移建立，本带不重建、不改名：
--   index_docs / index_chunks / index_chunks_fts / chunk_embeddings
-- index_docs 的 parser_version / embedding_model_id / embedding_dim /
-- index_status / indexed_at / error 列由 SPEC-10 维护。
--
-- 时间列一律 clock.Format（UTC RFC3339 毫秒），见 SPEC-01 §4.1.2。
-- ============================================================

-- 检索源注册：每类源一套版本基线，供版本对账使用（SPEC-10 §5.3、N3）。
CREATE TABLE index_sources (
  source         TEXT NOT NULL PRIMARY KEY,
  enabled        INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  schema_version INTEGER NOT NULL DEFAULT 1,
  parser_version TEXT NOT NULL DEFAULT '',
  updated_at     TEXT NOT NULL
);

INSERT INTO index_sources (source, enabled, schema_version, parser_version, updated_at) VALUES
  ('paper_note',          1, 1, '', '1970-01-01T00:00:00.000Z'),
  ('db_record',           1, 1, '', '1970-01-01T00:00:00.000Z'),
  ('scratchpad',          1, 1, '', '1970-01-01T00:00:00.000Z'),
  ('idea',                1, 1, '', '1970-01-01T00:00:00.000Z'),
  ('experiment_outline',  1, 1, '', '1970-01-01T00:00:00.000Z'),
  ('experiment_results',  1, 1, '', '1970-01-01T00:00:00.000Z');

-- 持久化脏队列：崩溃后启动扫描续做（SPEC-10 §4.3、§5.3）。
-- 同一 (project, entity) 只保留一行；连续编辑合并为一次索引。
CREATE TABLE index_dirty (
  project_id  TEXT NOT NULL,
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
CREATE INDEX idx_index_dirty_due ON index_dirty(run_after);

-- 索引作业：重建/重解析/重嵌的进度与取消（SPEC-10 §5.3）。
CREATE TABLE index_jobs (
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
CREATE INDEX idx_index_jobs_status ON index_jobs(status, created_at);