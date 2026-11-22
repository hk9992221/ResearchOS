/**
 * Write idempotency.
 *
 * Scope is `(client_id, key)`. The stored `args_hash` is the SHA-256 of the
 * canonical (key-sorted) arguments minus `idempotency_key`, so semantically
 * identical JSON always replays and a changed argument is a CONFLICT. Replay
 * returns the first completed result with `_meta.replayed=true`; a failed call
 * releases its reservation so the client may retry the same key.
 */
import { createHash } from 'node:crypto'

import type { JsonObject } from '@researchos/shared'
import { AppErrorCode } from '@researchos/shared'

import { appError } from '../../errors'
import { formatTimestamp, type Clock } from '../../clock'
import { type Db, type RuntimeOptions, IDEMPOTENCY_TTL_SECONDS, clockOf, jsonObject, nowText } from './store'
import { IDEMPOTENCY_KEY_FIELD } from './tools'

export type IdempotencyClaim =
  | { readonly kind: 'claimed' }
  | { readonly kind: 'replay'; readonly result: JsonObject }

export const IDEMPOTENCY_KEY_MIN = 1
export const IDEMPOTENCY_KEY_MAX = 128

interface IdempotencyRow {
  client_id: string
  key: string
  tool: string
  args_hash: string
  state: string
  result_json: string | null
  created_at: string
  expires_at: string
}

/** RFC 8785-ish canonical JSON: object keys sorted, no undefined. */
export function canonicalJson(value: unknown): string {
  if (value === null || typeof value !== 'object') return JSON.stringify(value) ?? 'null'
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(',')}]`
  const record = value as Record<string, unknown>
  const parts: string[] = []
  for (const key of Object.keys(record).sort()) {
    const item = record[key]
    if (item === undefined) continue
    parts.push(`${JSON.stringify(key)}:${canonicalJson(item)}`)
  }
  return `{${parts.join(',')}}`
}

/** SHA-256 of canonical arguments, with the idempotency key removed. */
export function argsHash(args: JsonObject): string {
  const { [IDEMPOTENCY_KEY_FIELD]: _ignored, ...rest } = args
  return createHash('sha256').update(canonicalJson(rest), 'utf8').digest('hex')
}

export function validateIdempotencyKey(key: string): string {
  const length = [...key].length
  if (length < IDEMPOTENCY_KEY_MIN || length > IDEMPOTENCY_KEY_MAX) {
    throw appError(
      AppErrorCode.InvalidArgument,
      `idempotency_key 长度必须在 ${IDEMPOTENCY_KEY_MIN}–${IDEMPOTENCY_KEY_MAX} 之间`,
      { details: { field: IDEMPOTENCY_KEY_FIELD } }
    )
  }
  return key
}

export class IdempotencyStore {
  private readonly db: Db
  private readonly clock: Clock
  private readonly ttlSeconds: number

  constructor(db: Db, options: RuntimeOptions & { ttlSeconds?: number } = {}) {
    this.db = db
    this.clock = clockOf(options)
    this.ttlSeconds = options.ttlSeconds ?? IDEMPOTENCY_TTL_SECONDS
  }

  /**
   * Reserves (client, key). The read-check-insert runs in one BEGIN IMMEDIATE
   * transaction so two concurrent calls cannot both execute.
   */
  claim(clientId: string, key: string, tool: string, hash: string): IdempotencyClaim {
    if (clientId === '' || key === '') {
      throw appError(AppErrorCode.InvalidArgument, '幂等记录需要客户端与键', {
        details: { field: IDEMPOTENCY_KEY_FIELD }
      })
    }
    validateIdempotencyKey(key)
    const claim = this.db.transaction((): IdempotencyClaim => {
      const row = this.db
        .prepare(
          `SELECT client_id, key, tool, args_hash, state, result_json, created_at, expires_at
           FROM mcp_idempotency WHERE client_id = ? AND key = ?`
        )
        .get(clientId, key) as IdempotencyRow | undefined
      const now = this.clock.now()

      if (row !== undefined) {
        if (now.getTime() >= new Date(row.expires_at).getTime()) {
          this.db.prepare(`DELETE FROM mcp_idempotency WHERE client_id = ? AND key = ?`).run(clientId, key)
        } else if (row.tool !== tool || row.args_hash !== hash) {
          throw appError(
            AppErrorCode.Conflict,
            '同一 idempotency_key 已用于不同的工具或参数；请为新请求换一个键',
            { details: { key, tool } }
          )
        } else if (row.state === 'completed') {
          return { kind: 'replay', result: jsonObject(row.result_json) ?? {} }
        } else {
          throw appError(AppErrorCode.Conflict, '同一 idempotency_key 的请求仍在执行中', {
            details: { key, tool, state: 'pending' }
          })
        }
      }

      const nowTextValue = nowText(this.clock)
      const expires = new Date(now.getTime() + this.ttlSeconds * 1000)
      this.db
        .prepare(
          `INSERT INTO mcp_idempotency (client_id, key, tool, args_hash, state, created_at, expires_at)
           VALUES (?, ?, ?, ?, 'pending', ?, ?)`
        )
        .run(clientId, key, tool, hash, nowTextValue, formatTimestamp(expires))
      return { kind: 'claimed' }
    })
    return claim.immediate()
  }

  /** Stores the first result for replay. */
  complete(clientId: string, key: string, result: JsonObject): void {
    this.db
      .prepare(
        `UPDATE mcp_idempotency SET state = 'completed', result_json = ?
         WHERE client_id = ? AND key = ?`
      )
      .run(JSON.stringify(result), clientId, key)
  }

  /** Removes an in-flight reservation after a failed call. */
  release(clientId: string, key: string): void {
    this.db
      .prepare(`DELETE FROM mcp_idempotency WHERE client_id = ? AND key = ? AND state = 'pending'`)
      .run(clientId, key)
  }

  /** Startup cleanup: in-flight reservations cannot survive a restart. */
  clearPending(): number {
    return this.db.prepare(`DELETE FROM mcp_idempotency WHERE state = 'pending'`).run().changes
  }

  /** TTL sweep. */
  purgeExpired(): number {
    return this.db
      .prepare(`DELETE FROM mcp_idempotency WHERE expires_at < ?`)
      .run(nowText(this.clock)).changes
  }

  /** Reads a row without claiming it (status/audit). */
  peek(clientId: string, key: string): IdempotencyRow | null {
    const row = this.db
      .prepare(
        `SELECT client_id, key, tool, args_hash, state, result_json, created_at, expires_at
         FROM mcp_idempotency WHERE client_id = ? AND key = ?`
      )
      .get(clientId, key) as IdempotencyRow | undefined
    return row ?? null
  }
}
