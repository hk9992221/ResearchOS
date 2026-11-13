-- ============================================================
-- SPEC-05 Research Notes — follow-up migration (0310)
-- Source: note files now carry YAML frontmatter and the editor edits the
-- description as separate metadata. project.db stays the query authority, so
-- the description needs a column of its own.
-- 所有时间戳 TEXT，UTC，clock.Format（SPEC-01 §4.1.2）。
-- 版本号说明：本迁移最初写作 0280，与 literature 的 0280_literature_notes
-- 冲突。迁移引擎只应用高于数据库当前最高版本的迁移，而已有 project.db 已经
-- 到 0300（search_index_state），因此重编为 0290 不会被应用；这里取 0300 之后
-- 的空闲版本 0310。
-- ============================================================

-- ------------------------------------------------------------
-- 1. notes.description
-- 空串而非 NULL，避免 NULL 语义；与 research_databases.description 一致。
-- ------------------------------------------------------------
ALTER TABLE notes ADD COLUMN description TEXT NOT NULL DEFAULT '';