-- ============================================================
-- SPEC-05 Research Notes — follow-up migration (0270)
-- Source: SPEC-05 §4.2 "软删除行保留，唯一性约束只对 deleted_at IS NULL
-- 生效（部分唯一索引）". The v1 baseline index was unconditional, which made
-- a soft-deleted record keep its note page forever and broke the
-- promote → detach → re-promote round-trip (R-05-17).
-- ============================================================

DROP INDEX IF EXISTS ux_db_records_note;
CREATE UNIQUE INDEX ux_db_records_note_live
  ON db_records(note_id)
  WHERE note_id IS NOT NULL AND deleted_at IS NULL;