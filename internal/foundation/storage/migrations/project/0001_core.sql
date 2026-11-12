-- ============================================================
-- project.db schema v1（内容为 SPEC-01 v1.1）
-- migration 0001，版本带 0001–0099
-- 每个项目一份；目录 projects/<project-id>/project.db
-- ============================================================

CREATE TABLE schema_migrations (
  version    INTEGER NOT NULL PRIMARY KEY CHECK (version > 0),
  name       TEXT    NOT NULL,
  checksum   TEXT    NOT NULL,
  applied_at TEXT    NOT NULL
);

-- 笔记元数据；正文在 projects/<pid>/notes/<note-id>.md，文件为正文权威。
-- v1.1 注：paper_id / meta_json 归 SPEC-04 迁移带（0200–0299），本层不建。
CREATE TABLE notes (
  id         TEXT NOT NULL PRIMARY KEY,
  title      TEXT NOT NULL DEFAULT '',
  file_path  TEXT NOT NULL,                      -- 相对工作区根
  kind       TEXT NOT NULL
             CHECK (kind IN ('scratchpad','paper_note','db_record','overview')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT,
  file_hash  TEXT,                               -- sha256(hex)；NULL = 尚未建立基线
  file_mtime TEXT,                               -- 最近一次确认的文件 mtime，快速比对用
  file_state TEXT NOT NULL DEFAULT 'ok'
             CHECK (file_state IN ('ok','missing'))
);
CREATE UNIQUE INDEX ux_notes_file_path ON notes(file_path);
CREATE INDEX idx_notes_kind_deleted ON notes(kind, deleted_at);
CREATE INDEX idx_notes_deleted_at   ON notes(deleted_at);
CREATE INDEX idx_notes_updated_at   ON notes(updated_at);
CREATE INDEX idx_notes_file_missing ON notes(file_state) WHERE file_state = 'missing';

-- Research Database 定义（description 归 SPEC-05 迁移带）
CREATE TABLE research_databases (
  id         TEXT NOT NULL PRIMARY KEY,
  name       TEXT NOT NULL CHECK (length(name) > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
);
CREATE INDEX idx_research_databases_deleted ON research_databases(deleted_at);

-- 自定义字段。type 取值集合与 role 列（title|plain）由 SPEC-05 定义/在 0200 带添加。
CREATE TABLE db_fields (
  id          TEXT NOT NULL PRIMARY KEY,
  database_id TEXT NOT NULL REFERENCES research_databases(id) ON DELETE CASCADE,
  name        TEXT NOT NULL CHECK (length(name) > 0),
  type        TEXT NOT NULL,
  config      TEXT NOT NULL DEFAULT '{}',
  sort_order  INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  deleted_at  TEXT
);
CREATE INDEX idx_db_fields_database ON db_fields(database_id, sort_order);

-- 记录。data 为 {field_id: value} 的 JSON；note_id 指向可打开的记录页。
CREATE TABLE db_records (
  id          TEXT NOT NULL PRIMARY KEY,
  database_id TEXT NOT NULL REFERENCES research_databases(id) ON DELETE CASCADE,
  note_id     TEXT REFERENCES notes(id) ON DELETE SET NULL,
  title       TEXT NOT NULL DEFAULT '',
  data        TEXT NOT NULL DEFAULT '{}',
  sort_order  INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  deleted_at  TEXT
);
CREATE INDEX idx_db_records_database ON db_records(database_id, sort_order);
CREATE INDEX idx_db_records_deleted  ON db_records(deleted_at);
CREATE INDEX idx_db_records_note     ON db_records(note_id);
CREATE UNIQUE INDEX ux_db_records_note ON db_records(note_id) WHERE note_id IS NOT NULL;

-- Idea。status 枚举来自 SPEC-00 §4，任意迁移都发 status.changed。
CREATE TABLE ideas (
  id               TEXT NOT NULL PRIMARY KEY,
  title            TEXT NOT NULL CHECK (length(title) > 0),
  description      TEXT NOT NULL DEFAULT '',
  status           TEXT NOT NULL DEFAULT 'New'
                   CHECK (status IN ('New','Researching','Validating','Supported',
                                     'PartiallySupported','Inconclusive','Rejected','OnHold')),
  rejection_reason TEXT NOT NULL DEFAULT '',
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL,
  deleted_at       TEXT
);
CREATE INDEX idx_ideas_status  ON ideas(status, deleted_at);
CREATE INDEX idx_ideas_deleted ON ideas(deleted_at);
CREATE INDEX idx_ideas_updated ON ideas(updated_at);

-- Evidence：来源用自身字段表达，不重复进 entity_relations（SPEC-00 §5）。
-- provenance 为 AI 生成时的快照（provider/model/prompt_version/sources/rationale…），
-- ai_output_id 指向 SPEC-02 ai_outputs（跨迁移带，无外键）。
CREATE TABLE evidence (
  id           TEXT NOT NULL PRIMARY KEY,
  idea_id      TEXT NOT NULL REFERENCES ideas(id) ON DELETE CASCADE,
  direction    TEXT NOT NULL CHECK (direction IN ('support','oppose')),
  content      TEXT NOT NULL DEFAULT '',
  source_type  TEXT NOT NULL,               -- 实体类型注册表取值；'paper' 可跨库
  source_id    TEXT NOT NULL,
  ai_generated INTEGER NOT NULL DEFAULT 0 CHECK (ai_generated IN (0,1)),
  confirmed    INTEGER NOT NULL DEFAULT 0 CHECK (confirmed IN (0,1)),
  ai_output_id TEXT,
  provenance   TEXT NOT NULL DEFAULT '{}',
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  deleted_at   TEXT
);
CREATE INDEX idx_evidence_idea   ON evidence(idea_id, direction, deleted_at);
CREATE INDEX idx_evidence_source ON evidence(source_type, source_id);
CREATE INDEX idx_evidence_ai_output ON evidence(ai_output_id) WHERE ai_output_id IS NOT NULL;

-- Experiment：外部目录引用（绝对路径），健康状态见 external_refs。
CREATE TABLE experiments (
  id         TEXT NOT NULL PRIMARY KEY,
  title      TEXT NOT NULL CHECK (length(title) > 0),
  purpose    TEXT NOT NULL DEFAULT '',
  dir_ref    TEXT,
  scan_rules TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
);
CREATE INDEX idx_experiments_deleted ON experiments(deleted_at);
CREATE INDEX idx_experiments_title   ON experiments(title);

-- Run：两级结构按 SPEC-00 §4 建；status 默认 unknown，取值集合与语义由 SPEC-07 定义
-- （SPEC-00 §11 v1.1：不做自动完成检测，由用户手动设置）。
CREATE TABLE runs (
  id            TEXT NOT NULL PRIMARY KEY,
  experiment_id TEXT NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
  dir_ref       TEXT,
  params        TEXT NOT NULL DEFAULT '{}',
  git_info      TEXT NOT NULL DEFAULT '{}',
  status        TEXT NOT NULL DEFAULT 'unknown',
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  deleted_at    TEXT
);
CREATE INDEX idx_runs_experiment ON runs(experiment_id, deleted_at);
CREATE INDEX idx_runs_status     ON runs(status, deleted_at);

-- Snapshot：不可变历史，无 updated_at / deleted_at。
CREATE TABLE snapshots (
  id            TEXT NOT NULL PRIMARY KEY,
  target_type   TEXT NOT NULL CHECK (target_type IN ('experiment','run')),
  target_id     TEXT NOT NULL,
  file_manifest TEXT NOT NULL DEFAULT '[]',   -- [{path, hash, size}]，path 为外部目录内相对路径
  git_info      TEXT NOT NULL DEFAULT '{}',
  metrics       TEXT NOT NULL DEFAULT '{}',
  ai_analysis   TEXT,
  ai_output_id  TEXT,                         -- 指向 SPEC-02 ai_outputs（无外键）
  model_info    TEXT NOT NULL DEFAULT '{}',
  created_at    TEXT NOT NULL
);
CREATE INDEX idx_snapshots_target ON snapshots(target_type, target_id, created_at DESC);
CREATE INDEX idx_snapshots_ai_output ON snapshots(ai_output_id) WHERE ai_output_id IS NOT NULL;

-- Task。"column" 为 SQL 关键字，查询时必须加双引号（SPEC-00 v1.1 不采纳改名）。
-- v1.1 注：parent_task_id/description_md/completed_at/archived_at 与 task_columns、
-- task_checklist_items 归 SPEC-08 迁移带（0200–0299）。
CREATE TABLE tasks (
  id         TEXT NOT NULL PRIMARY KEY,
  title      TEXT NOT NULL CHECK (length(title) > 0),
  "column"   TEXT NOT NULL,
  priority   TEXT,
  deadline   TEXT,
  blocked    INTEGER NOT NULL DEFAULT 0 CHECK (blocked IN (0,1)),
  labels     TEXT NOT NULL DEFAULT '[]',
  week_plan  TEXT,                            -- ISO 周 'YYYY-Www'（周一起算）；空 = 未入周计划
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
);
CREATE INDEX idx_tasks_column   ON tasks("column", sort_order);
CREATE INDEX idx_tasks_week     ON tasks(week_plan, deleted_at);
CREATE INDEX idx_tasks_blocked  ON tasks(blocked, deleted_at);
CREATE INDEX idx_tasks_deadline ON tasks(deadline);
CREATE INDEX idx_tasks_deleted  ON tasks(deleted_at);

CREATE TABLE weekly_reports (
  id           TEXT NOT NULL PRIMARY KEY,
  period_start TEXT NOT NULL,                 -- 自然日起 'YYYY-MM-DD'
  period_end   TEXT NOT NULL,
  content_md   TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'draft'
               CHECK (status IN ('draft','confirmed')),
  meta         TEXT NOT NULL DEFAULT '{}',    -- provenance 指针、coverage、校验结果（SPEC-09 §4.3）
  confirmed_at TEXT,
  ai_output_id TEXT,                          -- 指向 SPEC-02 ai_outputs（无外键）
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  deleted_at   TEXT
);
CREATE INDEX idx_weekly_reports_period ON weekly_reports(period_start, period_end);
CREATE INDEX idx_weekly_reports_status ON weekly_reports(status, deleted_at);
CREATE INDEX idx_weekly_reports_ai_output ON weekly_reports(ai_output_id) WHERE ai_output_id IS NOT NULL;

-- 事件：append-only（唯一例外是 note.updated 的合并行，见 §5.6.2）。
CREATE TABLE activities (
  id          TEXT NOT NULL PRIMARY KEY,
  event_type  TEXT NOT NULL CHECK (event_type IN (
                'entity.created','entity.deleted','entity.restored',
                'status.changed','relation.added','relation.removed',
                'note.updated','ai.analysis_completed',
                'paper.imported','paper.overwrite_confirmed',
                'report.generated','project.exported',
                'entity.updated','entity.purged','entity.relocated',
                'paper.metadata_updated')),
  entity_type TEXT NOT NULL,
  entity_id   TEXT NOT NULL,
  payload     TEXT NOT NULL,                  -- {entity_type,entity_id,old?,new?,summary,actor,...}
  actor       TEXT NOT NULL CHECK (actor IN ('user','mcp','ai')),
  merge_key   TEXT,                           -- 仅 note.updated 使用
  created_at  TEXT NOT NULL
);
CREATE INDEX idx_activities_created ON activities(created_at DESC);
CREATE INDEX idx_activities_entity  ON activities(entity_type, entity_id, created_at);
CREATE INDEX idx_activities_type    ON activities(event_type, created_at);
CREATE INDEX idx_activities_actor   ON activities(actor, created_at);
CREATE UNIQUE INDEX ux_activities_merge_key ON activities(merge_key) WHERE merge_key IS NOT NULL;

-- 通用三元组关系。relation 枚举来自 SPEC-00 §5；新增类型须改契约 + 迁移重建本表。
-- 项目↔Paper 关联不走本表，见 project_papers。
CREATE TABLE entity_relations (
  id         TEXT NOT NULL PRIMARY KEY,
  src_type   TEXT NOT NULL,
  src_id     TEXT NOT NULL,
  relation   TEXT NOT NULL CHECK (relation IN (
               'references','derived_from','validates','deliverable_of','related_to')),
  dst_type   TEXT NOT NULL,
  dst_id     TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX ux_entity_relations_tuple
  ON entity_relations(src_type, src_id, relation, dst_type, dst_id);
CREATE INDEX idx_entity_relations_src      ON entity_relations(src_type, src_id);
CREATE INDEX idx_entity_relations_dst      ON entity_relations(dst_type, dst_id);
CREATE INDEX idx_entity_relations_relation ON entity_relations(relation);

-- 外部引用健康登记。path 必须与拥有者表上的路径列一致（服务层保证）。
CREATE TABLE external_refs (
  id              TEXT NOT NULL PRIMARY KEY,
  owner_type      TEXT NOT NULL,
  owner_id        TEXT NOT NULL,
  field           TEXT NOT NULL,              -- 'dir_ref' | 'git_repo'
  ref_kind        TEXT NOT NULL CHECK (ref_kind IN ('directory','file','git_repository')),
  path            TEXT NOT NULL,              -- 绝对路径；只读，不写入
  status          TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok','broken')),
  last_checked_at TEXT,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);
CREATE UNIQUE INDEX ux_external_refs_owner ON external_refs(owner_type, owner_id, field);
CREATE INDEX idx_external_refs_status ON external_refs(status);

-- 项目 ↔ 全局 Paper 关联（SPEC-00 §4/§5 v1.1）。解除即删行，由 relation.removed 留痕。
-- project_id 冗余存在以便导出与联邦查询自描述；服务层强制等于当前项目。
CREATE TABLE project_papers (
  project_id TEXT NOT NULL,
  paper_id   TEXT NOT NULL,                   -- 全局 Paper.id，跨库逻辑外键
  linked_at  TEXT NOT NULL,
  linked_by  TEXT NOT NULL CHECK (linked_by IN ('user','mcp')),
  PRIMARY KEY (project_id, paper_id)
);
CREATE INDEX idx_project_papers_paper ON project_papers(paper_id);

-- 项目级设置（SPEC-00 §4 v1.1，S-02）。库即项目作用域，故无 project_id 列。
CREATE TABLE project_settings (
  key        TEXT NOT NULL PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- 回收站统一视图（不含 db_fields：字段是配置项，随所属 database 处理）。
CREATE VIEW v_trash AS
  SELECT 'note' AS entity_type, id, title, deleted_at
    FROM notes WHERE deleted_at IS NOT NULL
  UNION ALL
  SELECT 'research_database', id, name, deleted_at
    FROM research_databases WHERE deleted_at IS NOT NULL
  UNION ALL
  SELECT 'db_record', id, title, deleted_at
    FROM db_records WHERE deleted_at IS NOT NULL
  UNION ALL
  SELECT 'idea', id, title, deleted_at
    FROM ideas WHERE deleted_at IS NOT NULL
  UNION ALL
  SELECT 'evidence', id, substr(content, 1, 80), deleted_at
    FROM evidence WHERE deleted_at IS NOT NULL
  UNION ALL
  SELECT 'experiment', id, title, deleted_at
    FROM experiments WHERE deleted_at IS NOT NULL
  UNION ALL
  SELECT 'run', id, id, deleted_at
    FROM runs WHERE deleted_at IS NOT NULL
  UNION ALL
  SELECT 'task', id, title, deleted_at
    FROM tasks WHERE deleted_at IS NOT NULL
  UNION ALL
  SELECT 'weekly_report', id, period_start || ' ~ ' || period_end, deleted_at
    FROM weekly_reports WHERE deleted_at IS NOT NULL;
