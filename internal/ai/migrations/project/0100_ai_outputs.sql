-- ============================================================
-- SPEC-02 迁移带 0100–0199（project.db）——P-d：ai_outputs。
-- 一次生成尝试一行，不可变（除 content_md / status / human_edited / confirmed_at）。
-- provider_id 是生成时快照引用，不是跨库外键（SPEC-02 §4.4）。
-- ============================================================

CREATE TABLE ai_outputs (
  id             TEXT NOT NULL PRIMARY KEY,
  task_kind      TEXT NOT NULL,
  entity_type    TEXT,                             -- 绑定实体；可空
  entity_id      TEXT,
  status         TEXT NOT NULL CHECK (status IN ('draft','confirmed')),
  content_md     TEXT NOT NULL DEFAULT '',
  provider_id    TEXT NOT NULL,
  provider_name  TEXT NOT NULL,
  protocol       TEXT NOT NULL CHECK (protocol IN ('openai_chat','openai_responses','gemini','anthropic')),
  model_id       TEXT NOT NULL,
  prompt_version TEXT NOT NULL CHECK (length(prompt_version) > 0),
  source_refs    TEXT NOT NULL DEFAULT '[]',        -- JSON 数组 {entity_type, entity_id, locator, title}
  parameters     TEXT NOT NULL DEFAULT '{}',        -- JSON，不含密钥
  usage          TEXT,                              -- JSON 或 NULL
  human_edited   INTEGER NOT NULL DEFAULT 0 CHECK (human_edited IN (0,1)),
  created_at     TEXT NOT NULL,
  confirmed_at   TEXT
);
CREATE INDEX idx_ai_outputs_entity ON ai_outputs(entity_type, entity_id, created_at);
CREATE INDEX idx_ai_outputs_created ON ai_outputs(created_at);
