-- ============================================================
-- ResearchOS — global.db official index repository rename (0503)
-- SPEC-14 §2.1: the marketplace catalog is its own official-source
-- repository `researchos-plugin-registry`; this product repository ships
-- no catalog. The seeded `official` row is repointed only while it still
-- holds the built-in default, so a mirror or self-hosted index chosen in
-- settings survives; the stale ETag cache is dropped with the URL.
-- No Go counterpart (same precedent as 0510).
-- ============================================================

UPDATE plugin_registries
SET url = 'https://raw.githubusercontent.com/ResearchOS/researchos-plugin-registry/main/registry.json',
    etag = NULL,
    cached_json = NULL,
    fetched_at = NULL
WHERE id = 'official'
  AND url = 'https://raw.githubusercontent.com/ResearchOS/plugin-registry/main/registry.json';
