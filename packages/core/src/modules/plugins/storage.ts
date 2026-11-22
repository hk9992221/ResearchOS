import type { PluginStorageKeyItem } from '@researchos/shared'

import { type Clock, SystemClock, formatTimestamp } from '../../clock'
import { AppErrorCode, appError } from '../../errors'
import type { GlobalHandle } from '../../workspace/handle'

/**
 * Plugin private KV storage. Every statement is keyed by the
 * host-injected `pluginId`, so one plugin can never read, overwrite, list or
 * clear another namespace. Key format, value size, total quota and key count
 * are enforced here, inside the same transaction as the write, so concurrent
 * `Promise.all` calls cannot race past the capacity check.
 */

export const PLUGIN_STORAGE_KEY_MAX_LENGTH = 256
/** `JSON.stringify(value)` UTF-8 bytes per single row. */
export const PLUGIN_STORAGE_MAX_VALUE_BYTES = 2 * 1024 * 1024
/** Sum of `byte_size` across all scopes of one plugin. */
export const PLUGIN_STORAGE_MAX_TOTAL_BYTES = 100 * 1024 * 1024
/** Rows per plugin, across all scopes. */
export const PLUGIN_STORAGE_MAX_KEYS = 50_000
export const PLUGIN_STORAGE_LIST_DEFAULT_LIMIT = 100
export const PLUGIN_STORAGE_LIST_MAX_LIMIT = 500

const KEY_PATTERN = /^[a-zA-Z0-9._:-]{1,256}$/

export interface PluginStorageGetResult {
  found: boolean
  value?: unknown
  updatedAt?: string
}

export interface PluginStorageSetResult {
  byteSize: number
  updatedAt: string
}

export interface PluginStorageListResult {
  keys: PluginStorageKeyItem[]
  nextCursor?: string
}

function validation(message: string, field: string): never {
  // Calls this class VALIDATION_ERROR; the shared taxonomy's
  // Closest stable code is INVALID_ARGUMENT (shared-contract closed set).
  throw appError(AppErrorCode.InvalidArgument, message, { field })
}

/**
 * Maps the optional `projectId` to a `scope_key`: 'global' when absent,
 * 'project:<id>' otherwise. The project is not required to still exist so a
 * plugin can clean caches of archived or deleted projects via clear().
 */
export function pluginStorageScopeKey(projectId: unknown): string {
  if (projectId === undefined || projectId === null) return 'global'
  if (typeof projectId !== 'string' || projectId === '') {
    validation('projectId must be a non-empty string', 'projectId')
  }
  if (projectId.length > 128) validation('projectId exceeds 128 characters', 'projectId')
  return `project:${projectId}`
}

function assertStorageKey(key: unknown): string {
  if (typeof key !== 'string' || !KEY_PATTERN.test(key)) {
    validation(
      `key must be 1..${PLUGIN_STORAGE_KEY_MAX_LENGTH} characters of [a-zA-Z0-9._:-]`,
      'key'
    )
  }
  return key
}

/** Serializes a storage value; rejects the JSON-hostile types outright. */
function serializeValue(value: unknown): { json: string; byteSize: number } {
  if (value === undefined) {
    validation('value must not be undefined; use storage.delete(key) to remove a key', 'value')
  }
  const kind = typeof value
  if (kind === 'function' || kind === 'symbol' || kind === 'bigint') {
    validation(`value must be JSON-serializable, got ${kind}`, 'value')
  }
  let json: string
  try {
    json = JSON.stringify(value) as string
  } catch {
    validation('value is not JSON-serializable (cycles are rejected)', 'value')
  }
  if (json === undefined) validation('value must be JSON-serializable', 'value')
  const byteSize = Buffer.byteLength(json, 'utf8')
  if (byteSize > PLUGIN_STORAGE_MAX_VALUE_BYTES) {
    validation(
      `value exceeds the ${PLUGIN_STORAGE_MAX_VALUE_BYTES} byte per-key limit`,
      'value'
    )
  }
  return { json, byteSize }
}

/** Escapes LIKE wildcards so a prefix matches literally (ESCAPE '\\'). */
function escapeLikePrefix(prefix: string): string {
  return prefix.replace(/[\\%_]/g, (character) => `\\${character}`)
}

interface UsageRow {
  total_bytes: number
  key_count: number
}

interface GetRow {
  value_json: string
  updated_at: string
}

interface ListRow {
  key: string
  byte_size: number
  updated_at: string
}

export class PluginStorageStore {
  private readonly global: GlobalHandle
  private readonly clock: Clock

  constructor(global: GlobalHandle, clock: Clock = new SystemClock()) {
    this.global = global
    this.clock = clock
  }

  get(pluginId: string, params: { key: unknown; projectId?: unknown }): PluginStorageGetResult {
    const key = assertStorageKey(params.key)
    const scopeKey = pluginStorageScopeKey(params.projectId)
    const row = this.global.db
      .prepare('SELECT value_json, updated_at FROM plugin_kv WHERE plugin_id = ? AND scope_key = ? AND key = ?')
      .get(pluginId, scopeKey, key) as GetRow | undefined
    if (row === undefined) return { found: false }
    return { found: true, value: JSON.parse(row.value_json) as unknown, updatedAt: row.updated_at }
  }

  /**
   * Quota check + upsert in one transaction: the projected total (current
   * bytes minus the replaced row plus the new bytes) and the key count are
   * read and enforced against the exact state the write lands on.
   */
  set(
    pluginId: string,
    params: { key: unknown; value: unknown; projectId?: unknown }
  ): PluginStorageSetResult {
    const key = assertStorageKey(params.key)
    const scopeKey = pluginStorageScopeKey(params.projectId)
    const { json, byteSize } = serializeValue(params.value)
    const updatedAt = formatTimestamp(this.clock.now())
    return this.global.inTx(() => {
      const db = this.global.db
      const existing = db
        .prepare('SELECT byte_size FROM plugin_kv WHERE plugin_id = ? AND scope_key = ? AND key = ?')
        .get(pluginId, scopeKey, key) as { byte_size: number } | undefined
      const usage = db
        .prepare(
          'SELECT COALESCE(SUM(byte_size), 0) AS total_bytes, COUNT(*) AS key_count FROM plugin_kv WHERE plugin_id = ?'
        )
        .get(pluginId) as UsageRow
      if (existing === undefined && usage.key_count >= PLUGIN_STORAGE_MAX_KEYS) {
        validation(
          `plugin storage quota exceeded: a plugin may hold at most ${PLUGIN_STORAGE_MAX_KEYS} keys`,
          'quota'
        )
      }
      const projected = usage.total_bytes - (existing?.byte_size ?? 0) + byteSize
      if (projected > PLUGIN_STORAGE_MAX_TOTAL_BYTES) {
        validation(
          `plugin storage quota exceeded: ${PLUGIN_STORAGE_MAX_TOTAL_BYTES} bytes per plugin (projected ${projected})`,
          'quota'
        )
      }
      db.prepare(
        `INSERT INTO plugin_kv(plugin_id, scope_key, key, value_json, byte_size, updated_at)
         VALUES (?, ?, ?, ?, ?, ?)
         ON CONFLICT(plugin_id, scope_key, key)
         DO UPDATE SET value_json = excluded.value_json, byte_size = excluded.byte_size, updated_at = excluded.updated_at`
      ).run(pluginId, scopeKey, key, json, byteSize, updatedAt)
      return { byteSize, updatedAt }
    })
  }

  remove(pluginId: string, params: { key: unknown; projectId?: unknown }): { deleted: boolean } {
    const key = assertStorageKey(params.key)
    const scopeKey = pluginStorageScopeKey(params.projectId)
    const result = this.global.db
      .prepare('DELETE FROM plugin_kv WHERE plugin_id = ? AND scope_key = ? AND key = ?')
      .run(pluginId, scopeKey, key)
    return { deleted: result.changes > 0 }
  }

  list(
    pluginId: string,
    params: { prefix?: unknown; projectId?: unknown; limit?: unknown; cursor?: unknown }
  ): PluginStorageListResult {
    const scopeKey = pluginStorageScopeKey(params.projectId)
    let limit = PLUGIN_STORAGE_LIST_DEFAULT_LIMIT
    if (params.limit !== undefined && params.limit !== null) {
      if (
        typeof params.limit !== 'number' ||
        !Number.isInteger(params.limit) ||
        params.limit < 1 ||
        params.limit > PLUGIN_STORAGE_LIST_MAX_LIMIT
      ) {
        validation(
          `limit must be an integer between 1 and ${PLUGIN_STORAGE_LIST_MAX_LIMIT}`,
          'limit'
        )
      }
      limit = params.limit
    }
    let cursor: string | undefined
    if (params.cursor !== undefined && params.cursor !== null) {
      if (typeof params.cursor !== 'string' || params.cursor === '') {
        validation('cursor must be a non-empty string', 'cursor')
      }
      cursor = params.cursor
    }
    let prefix: string | undefined
    if (params.prefix !== undefined && params.prefix !== null) {
      if (typeof params.prefix !== 'string') validation('prefix must be a string', 'prefix')
      prefix = params.prefix
    }
    const conditions = ['plugin_id = ?', 'scope_key = ?']
    const values: unknown[] = [pluginId, scopeKey]
    if (cursor !== undefined) {
      conditions.push('key > ?')
      values.push(cursor)
    }
    if (prefix !== undefined && prefix !== '') {
      conditions.push(`key LIKE ? ESCAPE '\\'`)
      values.push(`${escapeLikePrefix(prefix)}%`)
    }
    // Fetch one extra row to decide `nextCursor` without a second query.
    const rows = this.global.db
      .prepare(
        `SELECT key, byte_size, updated_at FROM plugin_kv
         WHERE ${conditions.join(' AND ')}
         ORDER BY key COLLATE BINARY ASC
         LIMIT ?`
      )
      .all(...values, limit + 1) as ListRow[]
    const hasMore = rows.length > limit
    const page = hasMore ? rows.slice(0, limit) : rows
    const keys: PluginStorageKeyItem[] = page.map((row) => ({
      key: row.key,
      byteSize: row.byte_size,
      updatedAt: row.updated_at
    }))
    const result: PluginStorageListResult = { keys }
    if (hasMore) result.nextCursor = keys[keys.length - 1]!.key
    return result
  }

  clear(pluginId: string, params: { projectId?: unknown }): { deletedCount: number } {
    const scopeKey = pluginStorageScopeKey(params.projectId)
    const result = this.global.db
      .prepare('DELETE FROM plugin_kv WHERE plugin_id = ? AND scope_key = ?')
      .run(pluginId, scopeKey)
    return { deletedCount: result.changes }
  }
}
