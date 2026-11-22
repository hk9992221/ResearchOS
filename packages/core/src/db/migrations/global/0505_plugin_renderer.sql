-- ============================================================
-- SPEC-16 §2.2: the renderer entry must be readable by the host page
-- without re-reading the plugin folder. Store the manifest's optional
-- `renderer` path alongside the rest of the snapshot.
-- ============================================================

ALTER TABLE plugins ADD COLUMN renderer TEXT;
