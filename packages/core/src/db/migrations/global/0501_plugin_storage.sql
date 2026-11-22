-- ============================================================
-- ResearchOS — global.db plugin private KV storage (0500–0599 band)
-- ============================================================
-- SPEC-13: per-plugin key/value persistence (`researchos.storage`).
-- Rows live strictly inside a plugin namespace; `pluginId` is injected by
-- the host RPC router, never by plugin params. Disabling or updating a
-- plugin keeps its rows; uninstalling deletes the `plugins` row and the
-- foreign key cascades this table clean. Not part of any project export.

CREATE TABLE plugin_kv (
  plugin_id  TEXT NOT NULL REFERENCES plugins(id) ON DELETE CASCADE,
  scope_key  TEXT NOT NULL, -- 'global' 或 'project:<projectId>'
  key        TEXT NOT NULL,
  value_json TEXT NOT NULL,
  byte_size  INTEGER NOT NULL CHECK (byte_size >= 0),
  updated_at TEXT NOT NULL,
  PRIMARY KEY (plugin_id, scope_key, key)
);

CREATE INDEX idx_plugin_kv_plugin_scope ON plugin_kv(plugin_id, scope_key);
