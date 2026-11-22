import type {
  PluginContributions,
  PluginInfo,
  PluginManifest,
  PluginPermission,
  PluginSourceType
} from '@researchos/shared'
import { isPluginPermission, isPluginSourceType } from '@researchos/shared'

import { formatTimestamp } from '../../clock'
import type { GlobalHandle } from '../../workspace/handle'
import { pluginNotFound } from './errors'

/** Provenance recorded when an artifact is installed. */
export interface PluginSourceInfo {
  sourceType: PluginSourceType
  sourceUrl: string
  registryId: string
}

/** One installed plugin row, decoded from global.db. */
export interface PluginRecord {
  id: string
  name: string
  version: string
  apiVersion: number
  description: string
  path: string
  enabled: boolean
  permissions: PluginPermission[]
  granted: PluginPermission[]
  networkDomains: string[]
  contributions: PluginContributions
  /** Manifest `renderer` path; null when the plugin has no custom panel. */
  renderer: string | null
  warnings: string[]
  lastError: string | null
  installedAt: string
  updatedAt: string
  sourceType: PluginSourceType
  sourceUrl: string
  registryId: string
  autoUpdate: boolean
  availableVersion: string | null
  updateRequiresApproval: boolean
  lastCheckedAt: string | null
}

interface PluginRow {
  id: string
  name: string
  version: string
  api_version: number
  description: string
  path: string
  enabled: number
  permissions_json: string
  granted_json: string
  network_domains_json: string
  contributions_json: string
  renderer: string | null
  warnings_json: string
  last_error: string | null
  installed_at: string
  updated_at: string
  source_type: string
  source_url: string
  registry_id: string
  auto_update: number
  available_version: string | null
  update_requires_approval: number
  last_checked_at: string | null
}

function parseStringArray<T extends string>(raw: string, guard?: (value: string) => value is T): T[] {
  let parsed: unknown
  try {
    parsed = JSON.parse(raw) as unknown
  } catch {
    return []
  }
  if (!Array.isArray(parsed)) return []
  const out: T[] = []
  for (const item of parsed) {
    if (typeof item !== 'string') continue
    if (guard !== undefined && !guard(item)) continue
    out.push(item as T)
  }
  return out
}

function parseContributions(raw: string): PluginContributions {
  let parsed: unknown
  try {
    parsed = JSON.parse(raw) as unknown
  } catch {
    return {}
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return {}
  return parsed as PluginContributions
}

function decode(row: PluginRow): PluginRecord {
  return {
    id: row.id,
    name: row.name,
    version: row.version,
    apiVersion: row.api_version,
    description: row.description,
    path: row.path,
    enabled: row.enabled === 1,
    permissions: parseStringArray<PluginPermission>(row.permissions_json, isPluginPermission),
    granted: parseStringArray<PluginPermission>(row.granted_json, isPluginPermission),
    networkDomains: parseStringArray(row.network_domains_json),
    contributions: parseContributions(row.contributions_json),
    renderer: row.renderer ?? null,
    warnings: parseStringArray(row.warnings_json),
    lastError: row.last_error,
    installedAt: row.installed_at,
    updatedAt: row.updated_at,
    sourceType: isPluginSourceType(row.source_type) ? row.source_type : 'local',
    sourceUrl: row.source_url,
    registryId: row.registry_id,
    autoUpdate: row.auto_update === 1,
    availableVersion: row.available_version,
    updateRequiresApproval: row.update_requires_approval === 1,
    lastCheckedAt: row.last_checked_at
  }
}

export class PluginStore {
  private readonly global: GlobalHandle

  constructor(global: GlobalHandle) {
    this.global = global
  }

  list(): PluginRecord[] {
    const rows = this.global.db
      .prepare('SELECT * FROM plugins ORDER BY name COLLATE NOCASE, id')
      .all() as PluginRow[]
    return rows.map(decode)
  }

  get(pluginId: string): PluginRecord | undefined {
    const row = this.global.db.prepare('SELECT * FROM plugins WHERE id = ?').get(pluginId) as
      | PluginRow
      | undefined
    return row === undefined ? undefined : decode(row)
  }

  require(pluginId: string): PluginRecord {
    const record = this.get(pluginId)
    if (record === undefined) pluginNotFound(pluginId)
    return record
  }

  insert(record: PluginRecord, now: Date): void {
    const timestamp = formatTimestamp(now)
    this.global.inTx(() => {
      this.global.db
        .prepare(
          `INSERT INTO plugins(
             id, name, version, api_version, description, path, enabled,
             permissions_json, granted_json, network_domains_json,
             contributions_json, renderer, warnings_json, last_error, installed_at, updated_at,
             source_type, source_url, registry_id, auto_update,
             available_version, update_requires_approval, last_checked_at
           ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
        )
        .run(
          record.id,
          record.name,
          record.version,
          record.apiVersion,
          record.description,
          record.path,
          record.enabled ? 1 : 0,
          JSON.stringify(record.permissions),
          JSON.stringify(record.granted),
          JSON.stringify(record.networkDomains),
          JSON.stringify(record.contributions),
          record.renderer,
          JSON.stringify(record.warnings),
          record.lastError,
          record.installedAt === '' ? timestamp : record.installedAt,
          timestamp,
          record.sourceType,
          record.sourceUrl,
          record.registryId,
          record.autoUpdate ? 1 : 0,
          record.availableVersion,
          record.updateRequiresApproval ? 1 : 0,
          record.lastCheckedAt
        )
    })
  }

  /** Replaces the manifest snapshot and derived contributions after an update. */
  replaceSnapshot(record: PluginRecord, now: Date): void {
    this.global.inTx(() => {
      this.global.db
        .prepare(
          `UPDATE plugins SET name = ?, version = ?, api_version = ?, description = ?,
             permissions_json = ?, contributions_json = ?, renderer = ?, warnings_json = ?,
             updated_at = ?
           WHERE id = ?`
        )
        .run(
          record.name,
          record.version,
          record.apiVersion,
          record.description,
          JSON.stringify(record.permissions),
          JSON.stringify(record.contributions),
          record.renderer,
          JSON.stringify(record.warnings),
          formatTimestamp(now),
          record.id
        )
    })
  }

  /** Refreshes the custom-panel entry discovered from the manifest. */
  setRenderer(pluginId: string, renderer: string | null, now: Date): void {
    this.global.db
      .prepare('UPDATE plugins SET renderer = ?, updated_at = ? WHERE id = ?')
      .run(renderer, formatTimestamp(now), pluginId)
  }

  setEnabled(pluginId: string, enabled: boolean, now: Date, lastError: string | null = null): void {
    this.global.db
      .prepare('UPDATE plugins SET enabled = ?, last_error = ?, updated_at = ? WHERE id = ?')
      .run(enabled ? 1 : 0, lastError, formatTimestamp(now), pluginId)
  }

  setGrants(
    pluginId: string,
    granted: readonly PluginPermission[],
    networkDomains: readonly string[],
    now: Date
  ): void {
    this.global.db
      .prepare(
        `UPDATE plugins SET granted_json = ?, network_domains_json = ?, updated_at = ? WHERE id = ?`
      )
      .run(JSON.stringify(granted), JSON.stringify(networkDomains), formatTimestamp(now), pluginId)
  }

  setRuntimeState(
    pluginId: string,
    state: { warnings?: readonly string[]; lastError?: string | null; contributions?: PluginContributions },
    now: Date
  ): void {
    const record = this.require(pluginId)
    this.global.db
      .prepare(
        `UPDATE plugins SET warnings_json = ?, last_error = ?, contributions_json = ?, updated_at = ?
         WHERE id = ?`
      )
      .run(
        JSON.stringify(state.warnings ?? record.warnings),
        state.lastError === undefined ? record.lastError : state.lastError,
        JSON.stringify(state.contributions ?? record.contributions),
        formatTimestamp(now),
        pluginId
      )
  }

  delete(pluginId: string): void {
    this.global.db.prepare('DELETE FROM plugins WHERE id = ?').run(pluginId)
  }

  /** Records where an installed artifact came from. */
  setSource(pluginId: string, source: PluginSourceInfo, now: Date): void {
    this.global.db
      .prepare(
        `UPDATE plugins SET source_type = ?, source_url = ?, registry_id = ?, auto_update = ?,
           updated_at = ?
         WHERE id = ?`
      )
      .run(
        source.sourceType,
        source.sourceUrl,
        source.registryId,
        // Provenance decides whether background updates apply at all.
        source.sourceType === 'local' ? 0 : 1,
        formatTimestamp(now),
        pluginId
      )
  }

  setAutoUpdate(pluginId: string, enabled: boolean, now: Date): void {
    this.global.db
      .prepare('UPDATE plugins SET auto_update = ?, updated_at = ? WHERE id = ?')
      .run(enabled ? 1 : 0, formatTimestamp(now), pluginId)
  }

  /** Writes the outcome of one update check without touching the snapshot. */
  setCheckResult(
    pluginId: string,
    result: {
      availableVersion?: string | null
      updateRequiresApproval?: boolean
      lastCheckedAt?: string | null
      lastError?: string | null
    },
    now: Date
  ): void {
    const record = this.require(pluginId)
    const availableVersion =
      result.availableVersion === undefined ? record.availableVersion : result.availableVersion
    const requiresApproval =
      result.updateRequiresApproval === undefined
        ? record.updateRequiresApproval
        : result.updateRequiresApproval
    const lastCheckedAt =
      result.lastCheckedAt === undefined ? record.lastCheckedAt : result.lastCheckedAt
    const lastError = result.lastError === undefined ? record.lastError : result.lastError
    this.global.inTx(() => {
      this.global.db
        .prepare(
          `UPDATE plugins SET available_version = ?, update_requires_approval = ?, last_checked_at = ?,
             last_error = ?, updated_at = ? WHERE id = ?`
        )
        .run(
          availableVersion,
          requiresApproval ? 1 : 0,
          lastCheckedAt,
          lastError,
          formatTimestamp(now),
          pluginId
        )
    })
  }
}

/** Effective PluginInfo projection. */
export function toPluginInfo(record: PluginRecord): PluginInfo {
  return {
    id: record.id,
    name: record.name,
    version: record.version,
    apiVersion: record.apiVersion,
    description: record.description,
    enabled: record.enabled,
    path: record.path,
    installedAt: record.installedAt,
    updatedAt: record.updatedAt,
    sourceType: record.sourceType,
    sourceUrl: record.sourceUrl,
    registryId: record.registryId,
    autoUpdate: record.autoUpdate,
    availableVersion: record.availableVersion,
    updateRequiresApproval: record.updateRequiresApproval,
    lastCheckedAt: record.lastCheckedAt,
    permissions: [...record.permissions],
    granted: [...record.granted],
    networkDomains: [...record.networkDomains],
    renderer: record.renderer,
    contributions: {
      commands: [...(record.contributions.commands ?? [])],
      sidebar: [...(record.contributions.sidebar ?? [])],
      inspector: [...(record.contributions.inspector ?? [])],
      editor: [...(record.contributions.editor ?? [])]
    },
    warnings: [...record.warnings],
    lastError: record.lastError
  }
}

/** Snapshot of a parsed manifest, ready to insert. */
export function recordFromManifest(
  manifest: PluginManifest,
  contributions: PluginContributions,
  now: Date,
  source: PluginSourceInfo = { sourceType: 'local', sourceUrl: '', registryId: '' }
): PluginRecord {
  return {
    id: manifest.id,
    name: manifest.name,
    version: manifest.version,
    apiVersion: manifest.apiVersion,
    description: manifest.description ?? '',
    path: `plugins/${manifest.id}`,
    enabled: false,
    permissions: [...manifest.permissions],
    granted: [],
    networkDomains: [],
    contributions,
    renderer: manifest.renderer ?? null,
    warnings: [],
    lastError: null,
    installedAt: formatTimestamp(now),
    updatedAt: formatTimestamp(now),
    ...source,
    // Background updates apply to remote sources only.
    autoUpdate: source.sourceType !== 'local',
    availableVersion: null,
    updateRequiresApproval: false,
    lastCheckedAt: null
  }
}