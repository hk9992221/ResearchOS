-- ============================================================
-- ResearchOS Electron rewrite — global.db plugin registry (0500–0599 band)
-- Plan §3.7/§4.12. Plugin folders live at <workspace>/plugins/<id>; the row is
-- the installed registry, never part of project export.
--
-- The row records the manifest snapshot, the human-approved grants and the
-- derived contribution set. `enabled=0` until the install permission prompt is
-- confirmed. Timestamps are TEXT + clock format (UTC, milliseconds).
-- ============================================================

CREATE TABLE plugins (
  id                   TEXT NOT NULL PRIMARY KEY,
  name                 TEXT NOT NULL CHECK (length(name) > 0),
  version              TEXT NOT NULL,
  api_version          INTEGER NOT NULL CHECK (api_version = 1),
  description          TEXT NOT NULL DEFAULT '',
  path                 TEXT NOT NULL,
  enabled              INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
  permissions_json     TEXT NOT NULL DEFAULT '[]',
  granted_json         TEXT NOT NULL DEFAULT '[]',
  network_domains_json TEXT NOT NULL DEFAULT '[]',
  contributions_json   TEXT NOT NULL DEFAULT '{}',
  warnings_json        TEXT NOT NULL DEFAULT '[]',
  last_error           TEXT,
  installed_at         TEXT NOT NULL,
  updated_at           TEXT NOT NULL
);

CREATE UNIQUE INDEX idx_plugins_path ON plugins(path);
CREATE INDEX idx_plugins_enabled ON plugins(enabled);