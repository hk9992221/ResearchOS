-- ============================================================
-- global.db schema v1（内容为 SPEC-01 v1.1）
-- migration 0001，版本带 0001–0099
-- 时间格式：UTC RFC3339 毫秒，见 SPEC-01 §4.1.2
-- ============================================================

CREATE TABLE schema_migrations (
  version    INTEGER NOT NULL PRIMARY KEY CHECK (version > 0),
  name       TEXT    NOT NULL,
  checksum   TEXT    NOT NULL,             -- sha256(迁移文本)，防止已应用迁移被篡改
  applied_at TEXT    NOT NULL
);

-- 项目注册。目录名用 id，不用 name（SPEC-00 §1）。
CREATE TABLE projects (
  id          TEXT NOT NULL PRIMARY KEY,
  name        TEXT NOT NULL CHECK (length(name) > 0),
  summary     TEXT NOT NULL DEFAULT '',
  goals       TEXT NOT NULL DEFAULT '',
  focus_md    TEXT NOT NULL DEFAULT '',    -- Current Focus，v1.1（SPEC-03）
  status      TEXT NOT NULL DEFAULT 'active'
              CHECK (status IN ('active','archived')),
  archived_at TEXT,                        -- 归档时间；active 时为 NULL（v1.1，SPEC-03）
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX idx_projects_status      ON projects(status);
CREATE INDEX idx_projects_archived_at ON projects(archived_at);
CREATE INDEX idx_projects_created_at  ON projects(created_at);
CREATE INDEX idx_projects_name        ON projects(name);

-- 全局文献库：PDF 副本全局唯一，跨项目共享（SPEC-00 §1/§8）。
-- Zotero key 仅在 library 内唯一，去重键为 (zotero_library_id, zotero_key)。
CREATE TABLE papers (
  id                TEXT NOT NULL PRIMARY KEY,
  zotero_library_id TEXT NOT NULL DEFAULT '0',   -- 个人库默认 '0'；组库用 Zotero library id
  zotero_key        TEXT NOT NULL,
  attachment_key    TEXT,                        -- Zotero 附件 key；同步时直接定位原件
  metadata          TEXT NOT NULL DEFAULT '{}',   -- Zotero 条目元数据 JSON，原样字段名
  file_path         TEXT,                        -- 相对工作区根：library/papers/<id>/source.pdf；无副本为 NULL
  file_hash         TEXT,                        -- sha256(hex)；无副本为 NULL
  file_size         INTEGER,                     -- 字节数；无副本为 NULL
  file_state        TEXT NOT NULL DEFAULT 'ok'
                    CHECK (file_state IN ('ok','missing')),          -- 本地副本状态
  source_state      TEXT NOT NULL DEFAULT 'ok'
                    CHECK (source_state IN ('ok','missing','unreadable')), -- Zotero 原件可解析状态
  last_synced_at    TEXT,                        -- 最近一次手动同步成功时间
  imported_at       TEXT NOT NULL,
  updated_at        TEXT NOT NULL
);
CREATE UNIQUE INDEX ux_papers_zotero_identity ON papers(zotero_library_id, zotero_key);
CREATE INDEX idx_papers_zotero_key  ON papers(zotero_key);
CREATE INDEX idx_papers_imported_at ON papers(imported_at);
CREATE INDEX idx_papers_file_hash   ON papers(file_hash);
CREATE INDEX idx_papers_file_missing ON papers(file_state) WHERE file_state = 'missing';

-- AI 平台配置（字段语义由 SPEC-00 §7 定义；本表只负责存储）。
CREATE TABLE providers (
  id          TEXT NOT NULL PRIMARY KEY,
  name        TEXT NOT NULL CHECK (length(name) > 0),
  protocol    TEXT NOT NULL
              CHECK (protocol IN ('openai_chat','openai_responses','gemini','anthropic')),
  base_url    TEXT NOT NULL,
  api_key_ref TEXT NOT NULL,               -- 指向 OS 密钥存储的引用键；明文密钥不入库
  enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX idx_providers_enabled ON providers(enabled);

-- 用途 → provider + model。role 取值集合由 SPEC-02 定义（契约 §7 为开放集合 "…"），本层不加 CHECK。
CREATE TABLE role_assignments (
  role        TEXT NOT NULL PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE RESTRICT,
  model_id    TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX idx_role_assignments_provider ON role_assignments(provider_id);

-- 工作区级设置 KV。键命名空间见 §5.5。
CREATE TABLE settings (
  key        TEXT NOT NULL PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
