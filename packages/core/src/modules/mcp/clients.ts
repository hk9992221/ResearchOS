/**
 * `mcp_client`: bearer-token registry.
 *
 * New tokens are stored as scrypt hashes; the Go build stored bare SHA-256.
 * Legacy rows keep working and are upgraded to scrypt on the first successful
 * authentication, so no user has to re-issue a token. The plaintext token is
 * returned exactly once, by create/rotate.
 */
import { createHash, randomBytes, scryptSync, timingSafeEqual } from 'node:crypto'

import type { McpClient, McpTransport } from '@researchos/shared'
import { AppErrorCode } from '@researchos/shared'

import { type Clock } from '../../clock'
import { appError } from '../../errors'
import { newId } from '../../ids'
import type { Principal } from './types'
import { type Db, type RuntimeOptions, clockOf, nowText } from './store'

const SCRYPT_PREFIX = 'scrypt$1$'
const SCRYPT_KEY_LENGTH = 32
const SCRYPT_OPTIONS = { N: 16_384, r: 8, p: 1, maxmem: 64 * 1024 * 1024 } as const
const LEGACY_SHA256 = /^[0-9a-f]{64}$/
const CLIENT_NAME_MAX = 100

interface ClientRow {
  id: string
  name: string
  token_hash: string
  transport: string
  created_at: string
  last_seen_at: string | null
  enabled: number
}

function toClient(row: ClientRow): McpClient {
  return {
    id: row.id,
    name: row.name,
    transport: row.transport as McpTransport,
    createdAt: row.created_at,
    lastSeenAt: row.last_seen_at,
    enabled: row.enabled === 1
  }
}

/** 32 random bytes, URL-safe: 43 characters, no padding. */
function newToken(): string {
  return randomBytes(32).toString('base64url')
}

function hashScrypt(token: string): string {
  const salt = randomBytes(16)
  const derived = scryptSync(token, salt, SCRYPT_KEY_LENGTH, SCRYPT_OPTIONS)
  return `${SCRYPT_PREFIX}${salt.toString('base64url')}$${derived.toString('base64url')}`
}

function hashLegacy(token: string): string {
  return createHash('sha256').update(token, 'utf8').digest('hex')
}

function verifyScrypt(token: string, stored: string): boolean {
  if (!stored.startsWith(SCRYPT_PREFIX)) return false
  const [saltText, hashText] = stored.slice(SCRYPT_PREFIX.length).split('$')
  if (saltText === undefined || hashText === undefined) return false
  let salt: Buffer
  let expected: Buffer
  try {
    salt = Buffer.from(saltText, 'base64url')
    expected = Buffer.from(hashText, 'base64url')
  } catch {
    return false
  }
  if (expected.length !== SCRYPT_KEY_LENGTH) return false
  const derived = scryptSync(token, salt, SCRYPT_KEY_LENGTH, SCRYPT_OPTIONS)
  return timingSafeEqual(derived, expected)
}

export class Clients {
  private readonly db: Db
  private readonly clock: Clock

  constructor(db: Db, options: RuntimeOptions = {}) {
    this.db = db
    this.clock = clockOf(options)
  }

  /** Registers a client and returns the one-time plaintext token. */
  create(name: string, transport: McpTransport = 'http'): { client: McpClient; token: string } {
    const trimmed = name.trim()
    if (trimmed === '') {
      throw appError(AppErrorCode.InvalidArgument, '客户端名称不能为空', { details: { field: 'name' } })
    }
    if ([...trimmed].length > CLIENT_NAME_MAX) {
      throw appError(AppErrorCode.InvalidArgument, `客户端名称不能超过 ${CLIENT_NAME_MAX} 个字符`, {
        details: { field: 'name' }
      })
    }
    const id = newId(this.clock)
    const token = newToken()
    this.db
      .prepare(
        `INSERT INTO mcp_client (id, name, token_hash, transport, created_at, enabled)
         VALUES (?, ?, ?, 'http', ?, 1)`
      )
      .run(id, trimmed, hashScrypt(token), nowText(this.clock))
    return { client: this.requireClient(id), token }
  }

  /** Replaces the token; the old one stops working immediately. */
  rotate(id: string): string {
    const token = newToken()
    const changed = this.db
      .prepare(`UPDATE mcp_client SET token_hash = ? WHERE id = ?`)
      .run(hashScrypt(token), id).changes
    if (changed === 0) throw clientNotFound(id)
    return token
  }

  revoke(id: string): void {
    const changed = this.db.prepare(`DELETE FROM mcp_client WHERE id = ?`).run(id).changes
    if (changed === 0) throw clientNotFound(id)
  }

  update(id: string, patch: { name?: string; enabled?: boolean }): McpClient {
    if (patch.name !== undefined) {
      const trimmed = patch.name.trim()
      if (trimmed === '') {
        throw appError(AppErrorCode.InvalidArgument, '客户端名称不能为空', { details: { field: 'name' } })
      }
      this.db.prepare(`UPDATE mcp_client SET name = ? WHERE id = ?`).run(trimmed, id)
    }
    if (patch.enabled !== undefined) {
      this.db.prepare(`UPDATE mcp_client SET enabled = ? WHERE id = ?`).run(patch.enabled ? 1 : 0, id)
    }
    return this.requireClient(id)
  }

  setEnabled(id: string, enabled: boolean): void {
    const changed = this.db
      .prepare(`UPDATE mcp_client SET enabled = ? WHERE id = ?`)
      .run(enabled ? 1 : 0, id).changes
    if (changed === 0) throw clientNotFound(id)
  }

  list(): McpClient[] {
    const rows = this.db
      .prepare(
        `SELECT id, name, token_hash, transport, created_at, last_seen_at, enabled
         FROM mcp_client ORDER BY created_at DESC, id DESC`
      )
      .all() as ClientRow[]
    return rows.map(toClient)
  }

  get(id: string): McpClient {
    return toClient(this.requireRow(id))
  }

  /** Enabled-client count, for `server_status`. */
  count(): number {
    const row = this.db.prepare(`SELECT COUNT(*) AS n FROM mcp_client`).get() as { n: number }
    return row.n
  }

  /**
   * Resolves a bearer token. Unknown, disabled and malformed all return null so
   * the HTTP layer can answer a uniform 401. Successful legacy uses upgrade the
   * stored hash to scrypt.
   */
  authenticate(token: string): Principal | null {
    if (token === '') return null
    const legacyHash = hashLegacy(token)
    let row = this.db
      .prepare(
        `SELECT id, name, token_hash, transport, created_at, last_seen_at, enabled
         FROM mcp_client WHERE token_hash = ?`
      )
      .get(legacyHash) as ClientRow | undefined
    let legacy = row !== undefined

    if (row === undefined) {
      const candidates = this.db
        .prepare(
          `SELECT id, name, token_hash, transport, created_at, last_seen_at, enabled
           FROM mcp_client WHERE token_hash LIKE 'scrypt$%'`
        )
        .all() as ClientRow[]
      for (const candidate of candidates) {
        if (verifyScrypt(token, candidate.token_hash)) {
          row = candidate
          legacy = false
          break
        }
      }
    }
    if (row === undefined) return null
    if (row.enabled !== 1) return null

    const now = nowText(this.clock)
    if (legacy) {
      this.db
        .prepare(`UPDATE mcp_client SET token_hash = ?, last_seen_at = ? WHERE id = ?`)
        .run(hashScrypt(token), now, row.id)
    } else {
      this.db.prepare(`UPDATE mcp_client SET last_seen_at = ? WHERE id = ?`).run(now, row.id)
    }
    return { clientId: row.id, clientName: row.name, transport: row.transport as McpTransport }
  }

  /** True when `token` belongs to the client (test/audit helper). */
  verify(id: string, token: string): boolean {
    const row = this.requireRow(id)
    if (LEGACY_SHA256.test(row.token_hash)) return row.token_hash === hashLegacy(token)
    return verifyScrypt(token, row.token_hash)
  }

  private requireRow(id: string): ClientRow {
    const row = this.db
      .prepare(
        `SELECT id, name, token_hash, transport, created_at, last_seen_at, enabled
         FROM mcp_client WHERE id = ?`
      )
      .get(id) as ClientRow | undefined
    if (row === undefined) throw clientNotFound(id)
    return row
  }

  private requireClient(id: string): McpClient {
    return toClient(this.requireRow(id))
  }
}

function clientNotFound(id: string) {
  return appError(AppErrorCode.NotFound, `客户端 ${id} 不存在`, { details: { client_id: id } })
}