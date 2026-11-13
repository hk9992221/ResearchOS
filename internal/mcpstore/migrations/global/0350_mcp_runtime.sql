-- ============================================================
-- SPEC-11 MCP Server — global.db runtime state (0350–0399 band)
-- 来源：SPEC-11 §4.2/§4.3（mcp_confirmation/mcp_job/mcp_client/mcp_idempotency）。
--
-- 迁移带裁决（编排者 §裁决 1）：SPEC-01 §10.4 原把 0300–0399 整段划给
-- SPEC-11；SPEC-02 的 0110 迁移注释已把索引源注册/脏队列/索引作业表划给
-- SPEC-10，故 0300–0349 归 SPEC-10，本模块只使用 0350–0399，避免版本号冲突。
--
-- 这些表是应用运行态与审计元数据，不是业务实体，不参与项目导出（PRJ-08）。
-- 时间戳一律 TEXT + clock.Format（UTC，毫秒，字典序即时间序）。
-- 主键为服务端生成的 UUIDv7 不透明句柄。
-- ============================================================

-- ------------------------------------------------------------
-- 1. mcp_confirmation（§4.2 Confirmation）
-- state 状态机：requested→approved→executed/failed，或 denied/expired/cancelled。
-- 绑定 (tool, args_hash, project_id, client_id)；不存确认令牌（令牌只在进程内存）。
-- ------------------------------------------------------------
CREATE TABLE mcp_confirmation (
  id           TEXT NOT NULL PRIMARY KEY,
  tool         TEXT NOT NULL,
  args_json    TEXT NOT NULL DEFAULT '{}',
  args_hash    TEXT NOT NULL,
  title        TEXT NOT NULL DEFAULT '',
  summary      TEXT NOT NULL DEFAULT '',
  impact_json  TEXT,
  project_id   TEXT,
  client_id    TEXT NOT NULL,
  state        TEXT NOT NULL CHECK (state IN ('requested','approved','executing','executed','failed','denied','expired','cancelled')),
  created_at   TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  resolved_at  TEXT,
  resolved_by  TEXT,
  result_json  TEXT,
  error_code   TEXT,
  trace_id     TEXT
);
CREATE INDEX idx_mcp_confirmation_state_created ON mcp_confirmation(state, created_at);
CREATE INDEX idx_mcp_confirmation_client_created ON mcp_confirmation(client_id, created_at);
CREATE INDEX idx_mcp_confirmation_project_created ON mcp_confirmation(project_id, created_at);

-- ------------------------------------------------------------
-- 2. mcp_job（§4.2 Job）
-- 进度 0–100 单调递增；stage 为短中文短语。running→failed(interrupted) 在启动恢复中处理。
-- ------------------------------------------------------------
CREATE TABLE mcp_job (
  id           TEXT NOT NULL PRIMARY KEY,
  tool         TEXT NOT NULL,
  project_id   TEXT,
  client_id    TEXT NOT NULL,
  state        TEXT NOT NULL CHECK (state IN ('queued','running','succeeded','failed','cancelled')),
  progress     INTEGER NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
  stage        TEXT NOT NULL DEFAULT '',
  read_only    INTEGER NOT NULL DEFAULT 0 CHECK (read_only IN (0,1)),
  params_json  TEXT,
  result_json  TEXT,
  error_json   TEXT,
  created_at   TEXT NOT NULL,
  started_at   TEXT,
  finished_at  TEXT,
  trace_id     TEXT
);
CREATE INDEX idx_mcp_job_state_created ON mcp_job(state, created_at);
CREATE INDEX idx_mcp_job_project_created ON mcp_job(project_id, created_at);
CREATE INDEX idx_mcp_job_client_created ON mcp_job(client_id, created_at);

-- ------------------------------------------------------------
-- 3. mcp_client（§4.2 Client）
-- 只存令牌哈希，明文仅在创建/轮换时返回一次（§5.8）。
-- ------------------------------------------------------------
CREATE TABLE mcp_client (
  id            TEXT NOT NULL PRIMARY KEY,
  name          TEXT NOT NULL,
  token_hash    TEXT NOT NULL UNIQUE,
  transport     TEXT NOT NULL CHECK (transport IN ('http','stdio')),
  created_at    TEXT NOT NULL,
  last_seen_at  TEXT,
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1))
);

-- ------------------------------------------------------------
-- 4. mcp_idempotency（§4.2 Idempotency）
-- (client_id, key) 唯一；保留 24 小时。pending 表示首次调用仍在执行。
-- ------------------------------------------------------------
CREATE TABLE mcp_idempotency (
  client_id    TEXT NOT NULL,
  key          TEXT NOT NULL,
  tool         TEXT NOT NULL,
  args_hash    TEXT NOT NULL,
  state        TEXT NOT NULL CHECK (state IN ('pending','completed')),
  result_json  TEXT,
  created_at   TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  PRIMARY KEY (client_id, key)
);
CREATE INDEX idx_mcp_idempotency_expires ON mcp_idempotency(expires_at);