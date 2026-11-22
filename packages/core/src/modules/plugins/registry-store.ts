import type { GlobalHandle } from '../../workspace/handle'
import { formatTimestamp } from '../../clock'
import { AppErrorCode, appError } from '../../errors'
import { PLUGIN_MARKET_OFFICIAL_REGISTRY_ID } from '@researchos/shared'

/**
 * `plugin_registries` access. Only validated documents are
 * ever written back to `cached_json`, so the offline browsing path never has to
 * re-trust a stored payload; the `etag` makes the next refresh conditional.
 */

export interface RegistryRow {
  id: string
  name: string
  url: string
  enabled: boolean
  etag: string | null
  cachedJson: string | null
  fetchedAt: string | null
}

interface RegistryDbRow {
  id: string
  name: string
  url: string
  enabled: number
  etag: string | null
  cached_json: string | null
  fetched_at: string | null
}

const COLUMNS = 'id, name, url, enabled, etag, cached_json, fetched_at'

function decode(row: RegistryDbRow): RegistryRow {
  return {
    id: row.id,
    name: row.name,
    url: row.url,
    enabled: row.enabled === 1,
    etag: row.etag,
    cachedJson: row.cached_json,
    fetchedAt: row.fetched_at
  }
}

/** Slug ids stay readable in the source dialog (`scholar-lab`) and safe in paths. */
export function registryIdOf(name: string, url: string): string {
  const fromName = name
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/^[._-]+|[._-]+$/g, '')
    .slice(0, 48)
  if (fromName !== '') return fromName
  const host = (() => {
    try {
      return new URL(url).hostname.split('.')[0] ?? ''
    } catch {
      return ''
    }
  })().toLowerCase()
  return host === '' ? 'community' : host
}

export class PluginRegistryStore {
  private readonly global: GlobalHandle

  constructor(global: GlobalHandle) {
    this.global = global
  }

  list(): RegistryRow[] {
    const rows = this.global.db
      .prepare(`SELECT ${COLUMNS} FROM plugin_registries ORDER BY name COLLATE NOCASE, id`)
      .all() as RegistryDbRow[]
    return rows.map(decode)
  }

  get(id: string): RegistryRow | undefined {
    const row = this.global.db.prepare(`SELECT ${COLUMNS} FROM plugin_registries WHERE id = ?`).get(id) as
      | RegistryDbRow
      | undefined
    return row === undefined ? undefined : decode(row)
  }

  enabled(): RegistryRow[] {
    return this.list().filter((row) => row.enabled)
  }

  /** Inserts a community source; the id is deduplicated with a numeric suffix. */
  insert(name: string, url: string): RegistryRow {
    const base = registryIdOf(name, url)
    let id = base
    for (let suffix = 2; this.get(id) !== undefined; suffix += 1) {
      if (suffix > 100) {
        throw appError(AppErrorCode.Conflict, `cannot name a registry ${base}`, { name })
      }
      id = `${base}-${String(suffix)}`
    }
    this.global.db
      .prepare(
        `INSERT INTO plugin_registries(id, name, url, enabled) VALUES (?, ?, ?, 1)`
      )
      .run(id, name, url)
    const row = this.get(id)
    if (row === undefined) {
      throw appError(AppErrorCode.Internal, `registry ${id} was not stored`, { id })
    }
    return row
  }

  /** Only non-official sources are removable. */
  remove(id: string): boolean {
    if (id === PLUGIN_MARKET_OFFICIAL_REGISTRY_ID) {
      throw appError(AppErrorCode.Conflict, 'the official registry cannot be removed', { id })
    }
    return this.global.db.prepare('DELETE FROM plugin_registries WHERE id = ?').run(id).changes > 0
  }

  setEnabled(id: string, enabled: boolean): void {
    this.global.db
      .prepare('UPDATE plugin_registries SET enabled = ? WHERE id = ?')
      .run(enabled ? 1 : 0, id)
  }

  /** Points the official source somewhere else (mirror or self-hosted index). */
  setUrl(id: string, url: string): void {
    this.global.db.prepare('UPDATE plugin_registries SET url = ? WHERE id = ?').run(url, id)
  }

  /** Stores a validated document plus its validator; clears any stale validator. */
  setCache(id: string, documentJson: string, etag: string | null, now: Date): void {
    this.global.db
      .prepare('UPDATE plugin_registries SET cached_json = ?, etag = ?, fetched_at = ? WHERE id = ?')
      .run(documentJson, etag === '' ? null : etag, formatTimestamp(now), id)
  }

  /** Forces the next refresh to fetch again (after a URL or source change). */
  invalidate(id: string): void {
    this.global.db
      .prepare('UPDATE plugin_registries SET etag = NULL, cached_json = NULL, fetched_at = NULL WHERE id = ?')
      .run(id)
  }
}
