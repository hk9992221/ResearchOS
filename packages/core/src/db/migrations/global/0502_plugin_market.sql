-- ============================================================
-- ResearchOS — global.db plugin market & auto-update metadata
-- SPEC-14 §3. Provenance columns on `plugins` record where an installed
-- plugin came from and what the background checker knows about it;
-- `plugin_registries` holds the static `registry.json` sources with their
-- ETag cache so search and filtering stay local and offline-capable.
-- ============================================================

ALTER TABLE plugins ADD COLUMN source_type TEXT NOT NULL DEFAULT 'local'
  CHECK (source_type IN ('local', 'registry', 'github'));
ALTER TABLE plugins ADD COLUMN source_url TEXT NOT NULL DEFAULT '';
ALTER TABLE plugins ADD COLUMN registry_id TEXT NOT NULL DEFAULT '';
ALTER TABLE plugins ADD COLUMN auto_update INTEGER NOT NULL DEFAULT 1
  CHECK (auto_update IN (0, 1));
ALTER TABLE plugins ADD COLUMN available_version TEXT;
ALTER TABLE plugins ADD COLUMN update_requires_approval INTEGER NOT NULL DEFAULT 0
  CHECK (update_requires_approval IN (0, 1));
ALTER TABLE plugins ADD COLUMN last_checked_at TEXT;

CREATE TABLE plugin_registries (
  id          TEXT NOT NULL PRIMARY KEY,
  name        TEXT NOT NULL,
  url         TEXT NOT NULL UNIQUE,
  enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  etag        TEXT,
  cached_json TEXT,
  fetched_at  TEXT
);

-- The official index repository is seeded so a fresh workspace can browse the
-- market without configuration; it is not removable (SPEC-14 §5).
INSERT INTO plugin_registries(id, name, url, enabled)
VALUES (
  'official',
  'ResearchOS Official',
  'https://raw.githubusercontent.com/ResearchOS/plugin-registry/main/registry.json',
  1
);
