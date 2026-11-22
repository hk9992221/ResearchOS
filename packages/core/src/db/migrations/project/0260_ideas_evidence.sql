-- ============================================================
-- SPEC-06 Ideas — project.db additions (0200–0299 band)
-- 来源：SPEC-06 §4.1、§4.3、§4.4 A1/A2；SPEC-01 §4.8.1 模块迁移带。
-- 迁移机制保证本文件只执行一次，故使用普通 ALTER / CREATE。
-- 时间戳沿用 SPEC-01 TEXT / UTC / clock.Format。
-- ============================================================

-- ------------------------------------------------------------
-- 1. evidence.content_hash（SPEC-06 §4.3.1、A2）
-- 规范化内容（NFC + 空白折叠）的 SHA-256 hex，用于 AI 生成去重。
-- 既有行由服务层在下次写入时补齐；空串表示尚未计算。
-- ------------------------------------------------------------
ALTER TABLE evidence ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';

-- ------------------------------------------------------------
-- 2. Evidence 索引（SPEC-06 §4.3.3）
-- 去重索引不设 UNIQUE：重复记录可由用户有意保留，AI 路径由服务层跳过。
-- ------------------------------------------------------------
CREATE INDEX idx_evidence_live_idea
  ON evidence(idea_id, direction, confirmed)
  WHERE deleted_at IS NULL;

CREATE INDEX idx_evidence_live_source
  ON evidence(source_type, source_id)
  WHERE deleted_at IS NULL;

CREATE INDEX idx_evidence_dedupe
  ON evidence(idea_id, source_type, source_id, content_hash)
  WHERE deleted_at IS NULL;

-- ------------------------------------------------------------
-- 3. Idea 活跃查询索引（SPEC-06 §4.1）
-- ------------------------------------------------------------
CREATE INDEX idx_ideas_live_status
  ON ideas(status, updated_at DESC)
  WHERE deleted_at IS NULL;

CREATE INDEX idx_ideas_live_updated
  ON ideas(updated_at DESC)
  WHERE deleted_at IS NULL;

CREATE INDEX idx_ideas_trash
  ON ideas(deleted_at)
  WHERE deleted_at IS NOT NULL;