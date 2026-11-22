/**
 * `mcp_confirmation`: the desktop-only approval queue.
 *
 * Binding is (tool, args_hash, project_id, client_id). MCP can list/get/cancel;
 * only the desktop can resolve/execute. State changes are single UPDATE ... WHERE
 * CAS statements so a double approval cannot execute twice.
 */
import type {
  McpConfirmation,
  McpConfirmationState,
  JsonObject
} from '@researchos/shared'
import { AppErrorCode } from '@researchos/shared'

import { appError } from '../../errors'
import { newId } from '../../ids'
import { formatTimestamp, type Clock } from '../../clock'
import type { Principal } from './types'
import {
  type Db,
  type RuntimeOptions,
  clampLimit,
  clockOf,
  confirmationTtlSeconds,
  jsonObject,
  jsonText,
  nullText,
  nowText
} from './store'

export interface ConfirmationRequest {
  tool: string
  args: JsonObject
  argsHash: string
  title: string
  summary: string
  impact?: JsonObject
  projectId?: string | null
  traceId?: string | null
  /** Overrides `mcp.confirmation_ttl_seconds` for a single request. */
  ttlSeconds?: number
}

export interface ConfirmationFilter {
  state?: McpConfirmationState
  projectId?: string
  limit?: number
}

/** Desktop approval input; `by` is the UI actor label recorded on the row. */
export interface ConfirmationResolution {
  approve: boolean
  by?: string
  note?: string
}

interface ConfirmationRow {
  id: string
  tool: string
  args_json: string
  args_hash: string
  title: string
  summary: string
  note: string
  impact_json: string | null
  project_id: string | null
  client_id: string
  state: string
  created_at: string
  expires_at: string
  resolved_at: string | null
  resolved_by: string | null
  result_json: string | null
  error_code: string | null
  trace_id: string | null
}

const COLUMNS = `id, tool, args_json, args_hash, title, summary, note, impact_json,
  project_id, client_id, state, created_at, expires_at, resolved_at, resolved_by,
  result_json, error_code, trace_id`

function toConfirmation(row: ConfirmationRow): McpConfirmation {
  return {
    id: row.id,
    tool: row.tool,
    title: row.title,
    summary: row.summary,
    note: row.note,
    impact: jsonObject(row.impact_json),
    projectId: row.project_id,
    clientId: row.client_id,
    state: row.state as McpConfirmationState,
    createdAt: row.created_at,
    expiresAt: row.expires_at,
    resolvedAt: row.resolved_at,
    resolvedBy: row.resolved_by,
    result: jsonObject(row.result_json),
    errorCode: row.error_code,
    traceId: row.trace_id
  }
}

export class Confirmations {
  private readonly db: Db
  private readonly clock: Clock
  private readonly ttl: number

  constructor(db: Db, options: RuntimeOptions & { ttlSeconds?: number } = {}) {
    this.db = db
    this.clock = clockOf(options)
    this.ttl = confirmationTtlSeconds(options.ttlSeconds)
  }

  /** Enqueues a pending request bound to the calling client. */
  enqueue(principal: Principal, request: ConfirmationRequest): McpConfirmation {
    if (principal.clientId === '') {
      throw appError(AppErrorCode.InvalidArgument, '缺少客户端标识')
    }
    const id = newId(this.clock)
    const now = this.clock.now()
    const ttl = confirmationTtlSeconds(request.ttlSeconds ?? this.ttl)
    const expiresAt = new Date(now.getTime() + ttl * 1000)
    this.db
      .prepare(
        `INSERT INTO mcp_confirmation (id, tool, args_json, args_hash, title, summary, impact_json,
           project_id, client_id, state, created_at, expires_at, trace_id)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'requested', ?, ?, ?)`
      )
      .run(
        id,
        request.tool,
        JSON.stringify(request.args ?? {}),
        request.argsHash,
        request.title === '' ? request.tool : request.title,
        request.summary,
        jsonText(request.impact),
        nullText(request.projectId),
        principal.clientId,
        nowText(this.clock),
        formatTimestamp(expiresAt),
        nullText(request.traceId)
      )
    return this.requireRow(id)
  }

  /** MCP get: own requests only; expiry is applied first. */
  get(principal: Principal, id: string): McpConfirmation {
    const row = this.row(id)
    if (principal.clientId !== '' && row.client_id !== principal.clientId) {
      throw notFound(id)
    }
    const now = this.clock.now().getTime()
    if (row.state === 'requested' && now >= new Date(row.expires_at).getTime()) {
      this.expire(id)
      return this.requireRow(id)
    }
    return toConfirmation(row)
  }

  /**
   * MCP list: expires stale rows, then filters by client. An empty client id is
   * the in-app confirmation center and sees every client.
   */
  list(principal: Principal, filter: ConfirmationFilter = {}): McpConfirmation[] {
    this.expireDue()
    const limit = clampLimit(filter.limit)
    let sql = `SELECT ${COLUMNS} FROM mcp_confirmation WHERE 1 = 1`
    const params: Array<string | number> = []
    if (filter.state !== undefined) {
      sql += ' AND state = ?'
      params.push(filter.state)
    }
    if (filter.projectId !== undefined && filter.projectId !== '') {
      sql += ' AND project_id = ?'
      params.push(filter.projectId)
    }
    if (principal.clientId !== '') {
      sql += ' AND client_id = ?'
      params.push(principal.clientId)
    }
    sql += ' ORDER BY created_at DESC, id DESC LIMIT ?'
    params.push(limit)
    const rows = this.db.prepare(sql).all(...params) as ConfirmationRow[]
    return rows.map(toConfirmation)
  }

  /**
   * MCP cancel: only the owning client and only from `requested`. A processed
   * request is a CONFLICT; another client's request is NOT_FOUND so existence
   * never leaks.
   */
  cancelByClient(principal: Principal, id: string): McpConfirmation {
    if (principal.clientId === '') {
      throw appError(AppErrorCode.InvalidArgument, '缺少客户端标识')
    }
    const changed = this.db
      .prepare(
        `UPDATE mcp_confirmation SET state = 'cancelled', resolved_at = ?, resolved_by = 'client'
         WHERE id = ? AND client_id = ? AND state = 'requested'`
      )
      .run(nowText(this.clock), id, principal.clientId).changes
    if (changed === 1) return this.requireRow(id)
    const row = this.row(id)
    if (row.client_id !== principal.clientId) throw notFound(id)
    throw appError(AppErrorCode.Conflict, `确认 ${id} 当前状态为 ${row.state}，不能取消`, {
      details: { confirmation_id: id, state: row.state }
    })
  }

  /**
   * Desktop cancel: requested → cancelled regardless of the owning client.
   * A settled request is a CONFLICT; the in-app confirmation center is the
   * only actor allowed to cancel another client's request.
   */
  cancel(id: string, by = 'user'): McpConfirmation {
    const changed = this.db
      .prepare(
        `UPDATE mcp_confirmation SET state = 'cancelled', resolved_at = ?, resolved_by = ?
         WHERE id = ? AND state = 'requested'`
      )
      .run(nowText(this.clock), by, id).changes
    if (changed === 1) return this.requireRow(id)
    const row = this.row(id)
    throw appError(AppErrorCode.Conflict, `确认 ${id} 当前状态为 ${row.state}，不能取消`, {
      details: { confirmation_id: id, state: row.state }
    })
  }

  /**
   * Desktop approve/deny: requested → approved|denied in one CAS. A request
   * past its TTL is expired first and then reported as CONFLICT.
   */
  resolve(id: string, resolution: ConfirmationResolution): McpConfirmation {
    const target = resolution.approve ? 'approved' : 'denied'
    const by = resolution.by === undefined || resolution.by === '' ? 'user' : resolution.by
    const note = resolution.note?.trim() ?? ''
    const changed = this.db
      .prepare(
        `UPDATE mcp_confirmation SET state = ?, resolved_at = ?, resolved_by = ?, note = ?
         WHERE id = ? AND state = 'requested' AND expires_at > ?`
      )
      .run(target, nowText(this.clock), by, note, id, nowText(this.clock)).changes
    if (changed === 1) return this.requireRow(id)
    const row = this.row(id)
    if (row.state === 'requested') {
      this.expire(id)
      throw appError(AppErrorCode.Conflict, `确认 ${id} 已过期`, {
        details: { confirmation_id: id }
      })
    }
    throw appError(AppErrorCode.Conflict, `确认 ${id} 当前状态为 ${row.state}，不能再次处理`, {
      details: { confirmation_id: id, state: row.state }
    })
  }

  /** approved → executing, so an approved request executes exactly once. */
  claimExecution(id: string): McpConfirmation {
    const changed = this.db
      .prepare(
        `UPDATE mcp_confirmation SET state = 'executing', resolved_at = ?, resolved_by = 'user'
         WHERE id = ? AND state = 'approved'`
      )
      .run(nowText(this.clock), id).changes
    if (changed === 1) return this.requireRow(id)
    const row = this.row(id)
    throw appError(AppErrorCode.Conflict, `确认 ${id} 当前状态为 ${row.state}，不能再次执行`, {
      details: { confirmation_id: id, state: row.state }
    })
  }

  /** Records the executor outcome; `errorCode` marks failure. */
  finish(id: string, result: JsonObject | null, errorCode: string | null): McpConfirmation {
    const target = errorCode === null || errorCode === '' ? 'executed' : 'failed'
    const changed = this.db
      .prepare(
        `UPDATE mcp_confirmation SET state = ?, resolved_at = ?, resolved_by = 'user',
           result_json = ?, error_code = ?
         WHERE id = ? AND state IN ('approved','executing')`
      )
      .run(target, nowText(this.clock), jsonText(result), nullText(errorCode), id).changes
    if (changed === 1) return this.requireRow(id)
    const row = this.row(id)
    throw appError(AppErrorCode.Conflict, `确认 ${id} 当前状态为 ${row.state}，不能记录结果`, {
      details: { confirmation_id: id, state: row.state }
    })
  }

  /** Expires one requested confirmation immediately. */
  expire(id: string): void {
    this.db
      .prepare(
        `UPDATE mcp_confirmation SET state = 'expired', resolved_at = ?, resolved_by = 'system'
         WHERE id = ? AND state = 'requested'`
      )
      .run(nowText(this.clock), id)
  }

  /** Bulk expiry applied before every list/get. */
  expireDue(): number {
    return this.db
      .prepare(
        `UPDATE mcp_confirmation SET state = 'expired', resolved_at = ?, resolved_by = 'system'
         WHERE state = 'requested' AND expires_at <= ?`
      )
      .run(nowText(this.clock), nowText(this.clock)).changes
  }

  /**
   * Startup recovery: expired requests expire; approved but
   * unexecuted requests return to requested; an interrupted execution fails
   * with INTERRUPTED instead of silently disappearing.
   */
  recoverAtStartup(): { expired: number; reverted: number; interrupted: number } {
    const now = nowText(this.clock)
    const expired = this.db
      .prepare(
        `UPDATE mcp_confirmation SET state = 'expired', resolved_at = ?, resolved_by = 'system'
         WHERE state = 'requested' AND expires_at <= ?`
      )
      .run(now, now).changes
    const reverted = this.db
      .prepare(
        `UPDATE mcp_confirmation SET state = 'requested', resolved_at = NULL, resolved_by = NULL
         WHERE state = 'approved' AND expires_at > ?`
      )
      .run(now).changes
    this.db
      .prepare(
        `UPDATE mcp_confirmation SET state = 'expired', resolved_at = ?, resolved_by = 'system'
         WHERE state = 'approved' AND expires_at <= ?`
      )
      .run(now, now)
    const interrupted = this.db
      .prepare(
        `UPDATE mcp_confirmation SET state = 'failed', resolved_at = ?, resolved_by = 'system',
           error_code = 'INTERRUPTED'
         WHERE state = 'executing'`
      )
      .run(now).changes
    return { expired, reverted, interrupted }
  }

  /** Retention sweep; only settled rows older than `before` are removed. */
  purge(before: string): number {
    return this.db
      .prepare(
        `DELETE FROM mcp_confirmation
         WHERE state IN ('executed','failed','denied','expired','cancelled') AND created_at < ?`
      )
      .run(before).changes
  }

  /** Parsed arguments recorded at enqueue time, for the desktop executor. */
  argsOf(id: string): JsonObject {
    const row = this.row(id)
    return jsonObject(row.args_json) ?? {}
  }

  /** Count of pending requests, for `server_status`/the settings badge. */
  pendingCount(): number {
    this.expireDue()
    const row = this.db
      .prepare(`SELECT COUNT(*) AS n FROM mcp_confirmation WHERE state = 'requested'`)
      .get() as { n: number }
    return row.n
  }

  private row(id: string): ConfirmationRow {
    const row = this.db.prepare(`SELECT ${COLUMNS} FROM mcp_confirmation WHERE id = ?`).get(id) as
      | ConfirmationRow
      | undefined
    if (row === undefined) throw notFound(id)
    return row
  }

  private requireRow(id: string): McpConfirmation {
    return toConfirmation(this.row(id))
  }
}

function notFound(id: string) {
  return appError(AppErrorCode.NotFound, `确认记录 ${id} 不存在`, { details: { confirmation_id: id } })
}