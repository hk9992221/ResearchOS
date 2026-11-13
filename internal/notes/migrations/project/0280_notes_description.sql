-- ============================================================
-- SPEC-05 Research Notes — follow-up migration (0280)
-- Source: note files now carry YAML frontmatter and the editor edits the
-- description as separate metadata. project.db stays the query authority, so
-- the description needs a column of its own.
-- 所有时间戳 TEXT，UTC，clock.Format（SPEC-01 §4.1.2）。
-- ============================================================

-- ------------------------------------------------------------
-- 1. notes.description
-- 空串而非 NULL，避免 NULL 语义；与 research_databases.description 一致。
-- ------------------------------------------------------------
ALTER TABLE notes ADD COLUMN description TEXT NOT NULL DEFAULT '';