-- ============================================================
-- SPEC-04 Literature — project.db additions (0280)
-- 来源：SPEC-04 §4.4、§4.5、§4.8（notes.paper_id / meta_json）、
-- SPEC-00 v1.1 S-04 追认；SPEC-01 §4.8.1 模块业务增补带。
-- 迁移机制保证本文件只执行一次，故使用普通 ALTER / CREATE。
-- ============================================================

-- Paper Note 直接索引全局 Paper；与 references 关系同事务维护。
ALTER TABLE notes ADD COLUMN paper_id TEXT NULL;

-- 功能说明 §4.3 结构化字段（PaperNoteFields JSON）；空串表示未填写。
ALTER TABLE notes ADD COLUMN meta_json TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_notes_paper ON notes(paper_id) WHERE paper_id IS NOT NULL;

-- 同一 (project, paper) 至多一个未删除 Paper Note（§4.4/§4.8）。
CREATE UNIQUE INDEX ux_notes_paper_note_live
  ON notes(paper_id)
  WHERE kind = 'paper_note' AND deleted_at IS NULL AND paper_id IS NOT NULL;

-- 每项目一个 Literature Overview（§4.8）。
CREATE UNIQUE INDEX ux_notes_overview_live
  ON notes(kind)
  WHERE kind = 'overview' AND deleted_at IS NULL;