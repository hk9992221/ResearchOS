-- ============================================================
-- SPEC-02 迁移带 0100–0199（global.db）
-- G-a：providers 测试与模型缓存状态列；G-b：provider_models 模型缓存。
-- role_assignments 已由 SPEC-01 0001_core 建立，本带不改其结构。
-- 时间格式：UTC RFC3339 毫秒，见 SPEC-01 §4.1.2。
-- ============================================================

ALTER TABLE providers ADD COLUMN last_tested_at TEXT;
ALTER TABLE providers ADD COLUMN last_test_ok INTEGER CHECK (last_test_ok IN (0,1));
ALTER TABLE providers ADD COLUMN last_test_latency_ms INTEGER CHECK (last_test_latency_ms >= 0);
ALTER TABLE providers ADD COLUMN last_test_model_count INTEGER CHECK (last_test_model_count >= 0);
ALTER TABLE providers ADD COLUMN last_test_error TEXT;
ALTER TABLE providers ADD COLUMN models_fetched_at TEXT;
ALTER TABLE providers ADD COLUMN models_fetch_error TEXT;

-- 上游模型列表缓存（SPEC-02 §4.3 G-b）。
-- 刷新只置 available=0，不删除行，避免 role_assignments 引用悬空；
-- 手工模型（source='manual'）恒 available=1，不因刷新消失。
CREATE TABLE provider_models (
  provider_id   TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  model_id      TEXT NOT NULL,
  display_name  TEXT NOT NULL,
  capabilities  TEXT NOT NULL,               -- JSON 数组，如 ["chat","embedding"]
  source        TEXT NOT NULL CHECK (source IN ('listed','manual')),
  available     INTEGER NOT NULL DEFAULT 1 CHECK (available IN (0,1)),
  first_seen_at TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL,
  PRIMARY KEY (provider_id, model_id)
);
CREATE INDEX idx_provider_models_provider ON provider_models(provider_id, available);
