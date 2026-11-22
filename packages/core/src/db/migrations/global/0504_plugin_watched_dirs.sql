-- ============================================================
-- SPEC-16 §3.2: user-authorized plugin directories (global.db).
-- The plugin never sees `dir_path`; every subsequent call is scoped by
-- the opaque `id` (dirId) and a relative path validated by the host.
-- `project_id` is NULL for workspace-level authorizations and set for
-- project-level ones. Removing a plugin cascades its grants.
-- ============================================================

CREATE TABLE plugin_watched_dirs (
  id           TEXT NOT NULL PRIMARY KEY,
  plugin_id    TEXT NOT NULL REFERENCES plugins(id) ON DELETE CASCADE,
  project_id   TEXT,
  dir_path     TEXT NOT NULL,
  display_name TEXT NOT NULL,
  created_at   TEXT NOT NULL
);

CREATE INDEX idx_plugin_watched_dirs_plugin ON plugin_watched_dirs(plugin_id, project_id);
