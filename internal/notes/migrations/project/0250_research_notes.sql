-- ============================================================
-- SPEC-05 Research Notes — project.db additions (0200–0299 band)
-- 来源：SPEC-05 §4.2、§4.3 S-05-02/S-05-03、§5.2、§5.4；SPEC-00 v1.1 已追认
-- tags/entity_tags 与 db_fields.role（模块迁移带，见 SPEC-01 §4.8.1）。
-- 迁移机制保证本文件只执行一次，故使用普通 ALTER / CREATE。
-- 所有时间戳 TEXT，UTC，clock.Format（SPEC-01 §4.1.2）。
-- ============================================================

-- ------------------------------------------------------------
-- 1. research_databases.description（SPEC-05 §4.2）
-- 数据库列表的说明文字；空串而非 NULL，避免 NULL 语义。
-- ------------------------------------------------------------
ALTER TABLE research_databases ADD COLUMN description TEXT NOT NULL DEFAULT '';

-- ------------------------------------------------------------
-- 2. db_fields.role（SPEC-05 §4.2、§5.2 NOTE-02）
-- 每个 database 恰有一行 role='title' 且未删除；标题字段固定 type=text。
-- 部分唯一索引只约束未删除行，软删除后角色可转移（D-05-06）。
-- ------------------------------------------------------------
ALTER TABLE db_fields ADD COLUMN role TEXT NOT NULL DEFAULT 'plain'
  CHECK (role IN ('title','plain'));

CREATE UNIQUE INDEX ux_db_fields_title_role
  ON db_fields(database_id)
  WHERE role = 'title' AND deleted_at IS NULL;

-- 库内字段名唯一，大小写不敏感；软删除行让位（SPEC-05 §4.2）。
CREATE UNIQUE INDEX ux_db_fields_name
  ON db_fields(database_id, lower(name))
  WHERE deleted_at IS NULL;

-- ------------------------------------------------------------
-- 3. tags（SPEC-05 §4.2、§5.4 NOTE-06；S-05-02）
-- 项目级标签字典，Note 与 Record 共用。
-- 软删除时保留 entity_tags 行，恢复后标签重新可见（D-05-07）。
-- ------------------------------------------------------------
CREATE TABLE tags (
  id         TEXT NOT NULL PRIMARY KEY,
  name       TEXT NOT NULL CHECK (length(name) > 0),
  color      TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
);

-- 标签名唯一，大小写不敏感；软删除行让位（SPEC-05 §4.2）。
CREATE UNIQUE INDEX ux_tags_name
  ON tags(lower(name))
  WHERE deleted_at IS NULL;

CREATE INDEX idx_tags_deleted ON tags(deleted_at);

-- ------------------------------------------------------------
-- 4. entity_tags（SPEC-05 §4.2、§5.4；S-05-03）
-- 标签同时作用于 Note 与 Record，不引入新的关系类型。
-- 主键去重使 Assign 幂等；tag 真删时级联清理 join 行（软删不触发）。
-- ------------------------------------------------------------
CREATE TABLE entity_tags (
  tag_id      TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  entity_type TEXT NOT NULL,                 -- schema.EntityType 取值（note/db_record/…）
  entity_id   TEXT NOT NULL,                 -- UUIDv7，跨类型不强制外键
  created_at  TEXT NOT NULL,
  PRIMARY KEY (tag_id, entity_type, entity_id)
);

CREATE INDEX idx_entity_tags_entity ON entity_tags(entity_type, entity_id);