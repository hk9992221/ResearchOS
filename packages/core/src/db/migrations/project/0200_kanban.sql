-- ============================================================
-- SPEC-08 Kanban — project.db additions (0200–0299 band)
-- 来源：SPEC-08 §4.2（M-01~M-06）；SPEC-01 §10.4 S-03 已登记本迁移带。
-- 迁移机制保证本文件只执行一次，故使用普通 ALTER / CREATE。
-- 默认五列的种子数据不在此写入：SPEC-08 §4.3 要求「首次进入 Kanban 时
-- 事务补种、幂等、只补缺不覆盖」，由 kanban 服务层用注入时钟写入。
-- 所有时间戳 TEXT，UTC，clock.Format（SPEC-01 §4.1.2）。
-- ============================================================

-- ------------------------------------------------------------
-- 1. task_columns（M-01；SPEC-08 §4.2）
-- name 既是主键也是 tasks."column" 的取值（SPEC-01 v1 文本列名，S-09 不采纳）。
-- 结构性配置：无 deleted_at，删除即删且必须先迁移任务（SPEC-08 §5.4、O-7）。
-- ------------------------------------------------------------
CREATE TABLE task_columns (
  name        TEXT NOT NULL PRIMARY KEY,
  kind        TEXT NOT NULL,                 -- backlog|todo|in_progress|review|done|custom
  is_done     INTEGER NOT NULL DEFAULT 0 CHECK (is_done IN (0,1)),
  position    INTEGER NOT NULL,              -- 列从左到右，步长 1024
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX idx_task_columns_position ON task_columns(position);

-- ------------------------------------------------------------
-- 2. tasks 追加列（M-02~M-05）
-- parent_task_id 自引用仅两层（服务层拒绝三层嵌套，TASK-06）；
-- description_md 为 Markdown 正文，长度上限 100k 在应用层校验（§4.2 说明）；
-- completed_at 首次进入终止列时写入（§4.3）；archived_at 与 deleted_at 可区分（TASK-10）。
-- ------------------------------------------------------------
ALTER TABLE tasks ADD COLUMN parent_task_id TEXT NULL;
ALTER TABLE tasks ADD COLUMN description_md TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN completed_at TEXT NULL;
ALTER TABLE tasks ADD COLUMN archived_at TEXT NULL;
CREATE INDEX idx_tasks_parent   ON tasks(parent_task_id) WHERE parent_task_id IS NOT NULL;
CREATE INDEX idx_tasks_archived ON tasks(archived_at)    WHERE archived_at IS NOT NULL;

-- ------------------------------------------------------------
-- 3. task_checklist_items（M-06；TASK-07）
-- 编辑性附属数据：物理删除、不进回收站、无 deleted_at（O-7）。
-- ------------------------------------------------------------
CREATE TABLE task_checklist_items (
  id          TEXT NOT NULL PRIMARY KEY,
  task_id     TEXT NOT NULL,
  text        TEXT NOT NULL CHECK (length(text) > 0),
  done        INTEGER NOT NULL DEFAULT 0 CHECK (done IN (0,1)),
  sort_order  INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX idx_task_checklist ON task_checklist_items(task_id, sort_order);
