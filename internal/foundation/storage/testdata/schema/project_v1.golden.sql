-- table activities
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
)

-- table db_fields
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
)

-- table db_records
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
)

-- table entity_relations
CREATE TABLE entity_relations (
  id         TEXT NOT NULL PRIMARY KEY,
  src_type   TEXT NOT NULL,
  src_id     TEXT NOT NULL,
  relation   TEXT NOT NULL CHECK (relation IN (
               'references','derived_from','validates','deliverable_of','related_to')),
  dst_type   TEXT NOT NULL,
  dst_id     TEXT NOT NULL,
  created_at TEXT NOT NULL
)

-- table evidence
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
)

-- table experiments
CREATE TABLE experiments (
  id         TEXT NOT NULL PRIMARY KEY,
  title      TEXT NOT NULL CHECK (length(title) > 0),
  purpose    TEXT NOT NULL DEFAULT '',
  dir_ref    TEXT,
  scan_rules TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
)

-- table external_refs
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
)

-- table ideas
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
)

-- index idx_activities_actor
CREATE INDEX idx_activities_actor   ON activities(actor, created_at)

-- index idx_activities_created
CREATE INDEX idx_activities_created ON activities(created_at DESC)

-- index idx_activities_entity
CREATE INDEX idx_activities_entity  ON activities(entity_type, entity_id, created_at)

-- index idx_activities_type
CREATE INDEX idx_activities_type    ON activities(event_type, created_at)

-- index idx_db_fields_database
CREATE INDEX idx_db_fields_database ON db_fields(database_id, sort_order)

-- index idx_db_records_database
CREATE INDEX idx_db_records_database ON db_records(database_id, sort_order)

-- index idx_db_records_deleted
CREATE INDEX idx_db_records_deleted  ON db_records(deleted_at)

-- index idx_db_records_note
CREATE INDEX idx_db_records_note     ON db_records(note_id)

-- index idx_entity_relations_dst
CREATE INDEX idx_entity_relations_dst      ON entity_relations(dst_type, dst_id)

-- index idx_entity_relations_relation
CREATE INDEX idx_entity_relations_relation ON entity_relations(relation)

-- index idx_entity_relations_src
CREATE INDEX idx_entity_relations_src      ON entity_relations(src_type, src_id)

-- index idx_evidence_ai_output
CREATE INDEX idx_evidence_ai_output ON evidence(ai_output_id) WHERE ai_output_id IS NOT NULL

-- index idx_evidence_idea
CREATE INDEX idx_evidence_idea   ON evidence(idea_id, direction, deleted_at)

-- index idx_evidence_source
CREATE INDEX idx_evidence_source ON evidence(source_type, source_id)

-- index idx_experiments_deleted
CREATE INDEX idx_experiments_deleted ON experiments(deleted_at)

-- index idx_experiments_title
CREATE INDEX idx_experiments_title   ON experiments(title)

-- index idx_external_refs_status
CREATE INDEX idx_external_refs_status ON external_refs(status)

-- index idx_ideas_deleted
CREATE INDEX idx_ideas_deleted ON ideas(deleted_at)

-- index idx_ideas_status
CREATE INDEX idx_ideas_status  ON ideas(status, deleted_at)

-- index idx_ideas_updated
CREATE INDEX idx_ideas_updated ON ideas(updated_at)

-- index idx_notes_deleted_at
CREATE INDEX idx_notes_deleted_at   ON notes(deleted_at)

-- index idx_notes_file_missing
CREATE INDEX idx_notes_file_missing ON notes(file_state) WHERE file_state = 'missing'

-- index idx_notes_kind_deleted
CREATE INDEX idx_notes_kind_deleted ON notes(kind, deleted_at)

-- index idx_notes_updated_at
CREATE INDEX idx_notes_updated_at   ON notes(updated_at)

-- index idx_project_papers_paper
CREATE INDEX idx_project_papers_paper ON project_papers(paper_id)

-- index idx_research_databases_deleted
CREATE INDEX idx_research_databases_deleted ON research_databases(deleted_at)

-- index idx_runs_experiment
CREATE INDEX idx_runs_experiment ON runs(experiment_id, deleted_at)

-- index idx_runs_status
CREATE INDEX idx_runs_status     ON runs(status, deleted_at)

-- index idx_snapshots_ai_output
CREATE INDEX idx_snapshots_ai_output ON snapshots(ai_output_id) WHERE ai_output_id IS NOT NULL

-- index idx_snapshots_target
CREATE INDEX idx_snapshots_target ON snapshots(target_type, target_id, created_at DESC)

-- index idx_tasks_blocked
CREATE INDEX idx_tasks_blocked  ON tasks(blocked, deleted_at)

-- index idx_tasks_column
CREATE INDEX idx_tasks_column   ON tasks("column", sort_order)

-- index idx_tasks_deadline
CREATE INDEX idx_tasks_deadline ON tasks(deadline)

-- index idx_tasks_deleted
CREATE INDEX idx_tasks_deleted  ON tasks(deleted_at)

-- index idx_tasks_week
CREATE INDEX idx_tasks_week     ON tasks(week_plan, deleted_at)

-- index idx_weekly_reports_ai_output
CREATE INDEX idx_weekly_reports_ai_output ON weekly_reports(ai_output_id) WHERE ai_output_id IS NOT NULL

-- index idx_weekly_reports_period
CREATE INDEX idx_weekly_reports_period ON weekly_reports(period_start, period_end)

-- index idx_weekly_reports_status
CREATE INDEX idx_weekly_reports_status ON weekly_reports(status, deleted_at)

-- table notes
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
)

-- table project_papers
CREATE TABLE project_papers (
  project_id TEXT NOT NULL,
  paper_id   TEXT NOT NULL,                   -- 全局 Paper.id，跨库逻辑外键
  linked_at  TEXT NOT NULL,
  linked_by  TEXT NOT NULL CHECK (linked_by IN ('user','mcp')),
  PRIMARY KEY (project_id, paper_id)
)

-- table project_settings
CREATE TABLE project_settings (
  key        TEXT NOT NULL PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
)

-- table research_databases
CREATE TABLE research_databases (
  id         TEXT NOT NULL PRIMARY KEY,
  name       TEXT NOT NULL CHECK (length(name) > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
)

-- table runs
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
)

-- table schema_migrations
CREATE TABLE schema_migrations (
  version    INTEGER NOT NULL PRIMARY KEY CHECK (version > 0),
  name       TEXT    NOT NULL,
  checksum   TEXT    NOT NULL,
  applied_at TEXT    NOT NULL
)

-- table snapshots
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
)

-- index sqlite_autoindex_activities_1
<internal index>

-- index sqlite_autoindex_db_fields_1
<internal index>

-- index sqlite_autoindex_db_records_1
<internal index>

-- index sqlite_autoindex_entity_relations_1
<internal index>

-- index sqlite_autoindex_evidence_1
<internal index>

-- index sqlite_autoindex_experiments_1
<internal index>

-- index sqlite_autoindex_external_refs_1
<internal index>

-- index sqlite_autoindex_ideas_1
<internal index>

-- index sqlite_autoindex_notes_1
<internal index>

-- index sqlite_autoindex_project_papers_1
<internal index>

-- index sqlite_autoindex_project_settings_1
<internal index>

-- index sqlite_autoindex_research_databases_1
<internal index>

-- index sqlite_autoindex_runs_1
<internal index>

-- index sqlite_autoindex_snapshots_1
<internal index>

-- index sqlite_autoindex_tasks_1
<internal index>

-- index sqlite_autoindex_weekly_reports_1
<internal index>

-- table tasks
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
)

-- index ux_activities_merge_key
CREATE UNIQUE INDEX ux_activities_merge_key ON activities(merge_key) WHERE merge_key IS NOT NULL

-- index ux_db_records_note
CREATE UNIQUE INDEX ux_db_records_note ON db_records(note_id) WHERE note_id IS NOT NULL

-- index ux_entity_relations_tuple
CREATE UNIQUE INDEX ux_entity_relations_tuple
  ON entity_relations(src_type, src_id, relation, dst_type, dst_id)

-- index ux_external_refs_owner
CREATE UNIQUE INDEX ux_external_refs_owner ON external_refs(owner_type, owner_id, field)

-- index ux_notes_file_path
CREATE UNIQUE INDEX ux_notes_file_path ON notes(file_path)

-- view v_trash
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
    FROM weekly_reports WHERE deleted_at IS NOT NULL

-- table weekly_reports
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
)

