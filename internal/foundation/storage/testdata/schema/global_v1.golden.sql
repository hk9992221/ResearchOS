-- index idx_papers_file_hash
CREATE INDEX idx_papers_file_hash   ON papers(file_hash)

-- index idx_papers_file_missing
CREATE INDEX idx_papers_file_missing ON papers(file_state) WHERE file_state = 'missing'

-- index idx_papers_imported_at
CREATE INDEX idx_papers_imported_at ON papers(imported_at)

-- index idx_papers_zotero_key
CREATE INDEX idx_papers_zotero_key  ON papers(zotero_key)

-- index idx_projects_archived_at
CREATE INDEX idx_projects_archived_at ON projects(archived_at)

-- index idx_projects_created_at
CREATE INDEX idx_projects_created_at  ON projects(created_at)

-- index idx_projects_name
CREATE INDEX idx_projects_name        ON projects(name)

-- index idx_projects_status
CREATE INDEX idx_projects_status      ON projects(status)

-- index idx_providers_enabled
CREATE INDEX idx_providers_enabled ON providers(enabled)

-- index idx_role_assignments_provider
CREATE INDEX idx_role_assignments_provider ON role_assignments(provider_id)

-- table papers
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
)

-- table projects
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
)

-- table providers
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
)

-- table role_assignments
CREATE TABLE role_assignments (
  role        TEXT NOT NULL PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE RESTRICT,
  model_id    TEXT NOT NULL,
  updated_at  TEXT NOT NULL
)

-- table schema_migrations
CREATE TABLE schema_migrations (
  version    INTEGER NOT NULL PRIMARY KEY CHECK (version > 0),
  name       TEXT    NOT NULL,
  checksum   TEXT    NOT NULL,             -- sha256(迁移文本)，防止已应用迁移被篡改
  applied_at TEXT    NOT NULL
)

-- table settings
CREATE TABLE settings (
  key        TEXT NOT NULL PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
)

-- index sqlite_autoindex_papers_1
<internal index>

-- index sqlite_autoindex_projects_1
<internal index>

-- index sqlite_autoindex_providers_1
<internal index>

-- index sqlite_autoindex_role_assignments_1
<internal index>

-- index sqlite_autoindex_settings_1
<internal index>

-- index ux_papers_zotero_identity
CREATE UNIQUE INDEX ux_papers_zotero_identity ON papers(zotero_library_id, zotero_key)

