import type Database from 'better-sqlite3'

import { formatTimestamp, type Clock, SystemClock } from '../../clock'
import { ConfirmService } from '../../confirm'
import { AppError, AppErrorCode, appError } from '../../errors'
import { EventBus } from '../../events'
import { RelationService } from '../../relations'
import type { Actor, EventType, Relation, EntityType } from '../../schema'
import { inImmediateTransaction } from '../../db/drizzle'
import type { Db } from '../../db/open'
import type { Paths } from '../../paths'
import { TrashService } from '../../trash'
import type { ProjectHandle } from '../../workspace/handle'

/** The slice of one project a notes service needs. */
export interface NotesContext {
  readonly projectId: string
  readonly handle: ProjectHandle
  readonly db: Db
  readonly paths: Paths
  readonly clock: Clock
  /** global.db, used to resolve paper relation targets and link titles. */
  readonly globalDb: Db | null
  readonly events: EventBus
  readonly trash: TrashService
  readonly relations: RelationService
  inTx<T>(fn: () => T): T
}

export interface NotesContextOptions {
  clock?: Clock
  globalDb?: Db | null
  /** Shared bus from the composition root; defaults to a project-local bus. */
  events?: EventBus
}

export function createNotesContext(handle: ProjectHandle, options: NotesContextOptions = {}): NotesContext {
  const clock = options.clock ?? new SystemClock()
  const globalDb = options.globalDb ?? null
  const events = options.events ?? new EventBus({ clock })
  const trash = new TrashService(handle, events, new ConfirmService({ clock }), { clock })
  const relations = new RelationService(handle, events, {
    ...(globalDb === null ? {} : { global: { db: globalDb } }),
    clock
  })
  return {
    projectId: handle.id,
    handle,
    db: handle.db,
    paths: handle.paths,
    clock,
    globalDb,
    events,
    trash,
    relations,
    inTx: (fn) => inImmediateTransaction(handle.db, fn)
  }
}

/** Actor of a service call; empty means the local user. */
export function actorOf(value: Actor | undefined): Actor {
  return value ?? 'user'
}

export function nowStamp(ctx: NotesContext): string {
  return formatTimestamp(ctx.clock.now())
}

/* --------------------------------------------------------------- errors */

export function invalid(message: string, details?: Record<string, unknown>): AppError {
  return appError(AppErrorCode.InvalidArgument, message, { details })
}

export function notFound(message: string, details?: Record<string, unknown>): AppError {
  return appError(AppErrorCode.NotFound, message, { details })
}

export function conflict(message: string, details?: Record<string, unknown>): AppError {
  return appError(AppErrorCode.Conflict, message, { details })
}

export function entityDeleted(type: string, id: string): AppError {
  return appError(AppErrorCode.EntityDeleted, `${type} ${id} is deleted`)
}

export function noteFileMissing(id: string): AppError {
  return appError(AppErrorCode.NoteFileMissing, `note ${id} has no body file`)
}

/** Maps a thrown sqlite UNIQUE violation onto INVALID_ARGUMENT. */
export function isUniqueViolation(error: unknown): boolean {
  return error instanceof Error && error.message.toUpperCase().includes('UNIQUE')
}

/* ----------------------------------------------------------------- JSON */

export type Json = Record<string, unknown>

export function isJsonNull(value: unknown): boolean {
  return value === null
}

export function asObject(value: unknown, label: string): Json {
  if (value === undefined || value === null) return {}
  if (typeof value !== 'object' || Array.isArray(value)) {
    throw invalid(`${label} must be a JSON object`)
  }
  return value as Json
}

export function parseJsonObject(text: string | null | undefined): Json {
  if (text === null || text === undefined || text.trim() === '') return {}
  try {
    const parsed: unknown = JSON.parse(text)
    return parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed) ? (parsed as Json) : {}
  } catch {
    return {}
  }
}

export function encodeJson(value: Json): string {
  return JSON.stringify(value)
}

/* ------------------------------------------------------------ timestamps */

export function requireTimestamp(value: string | null): string {
  return value ?? ''
}

/* -------------------------------------------------------------- cursors */

interface CursorState {
  offset: number
}

export function encodeCursor(offset: number): string {
  return Buffer.from(JSON.stringify({ offset } satisfies CursorState), 'utf8').toString('base64url')
}

export function decodeCursor(cursor: string | undefined): number {
  if (cursor === undefined || cursor === '') return 0
  try {
    const text = Buffer.from(cursor, 'base64url').toString('utf8')
    const parsed: unknown = JSON.parse(text)
    if (parsed !== null && typeof parsed === 'object' && 'offset' in parsed) {
      const { offset } = parsed
      if (typeof offset === 'number' && Number.isSafeInteger(offset) && offset >= 0) return offset
    }
  } catch {
    // fall through
  }
  throw invalid('cursor is not a valid page cursor', { cursor })
}

export function normalizeLimit(limit: number | undefined, def: number, max: number): number {
  if (limit === undefined || limit <= 0) return def
  return Math.min(Math.trunc(limit), max)
}

/* ------------------------------------------------------------- sql util */

export function placeholders(count: number): string {
  return Array.from({ length: count }, () => '?').join(', ')
}

export function changedRows(result: Database.RunResult): number {
  return result.changes
}

/**
 * Optimistic concurrency check used by every versioned update: the caller's
 * token must equal the row's stored `updated_at`. A mismatch is CONFLICT with
 * the current row in the details (defect 6).
 */
export function requireVersionToken(
  expected: string | undefined,
  current: { updatedAt: string; [key: string]: unknown },
  entity: string
): void {
  if (expected === undefined) return
  if (expected !== current.updatedAt) {
    throw conflict(`${entity} was modified by another writer`, {
      expectedUpdatedAt: expected,
      currentUpdatedAt: current.updatedAt,
      current
    })
  }
}

/** Runs a versioned UPDATE and maps zero rows to CONFLICT/NOT_FOUND. */
export function versionedUpdate(
  db: Db,
  sql: string,
  args: ReadonlyArray<unknown>,
  expected: string | undefined,
  current: { updatedAt: string; [key: string]: unknown } | null,
  entity: string
): void {
  if (expected !== undefined && current !== null && expected !== current.updatedAt) {
    throw conflict(`${entity} was modified by another writer`, {
      expectedUpdatedAt: expected,
      currentUpdatedAt: current.updatedAt,
      current
    })
  }
  const result = db.prepare(sql).run(...args)
  if (result.changes === 0) {
    if (current === null) throw notFound(`${entity} not found`)
    throw conflict(`${entity} was modified by another writer`, {
      expectedUpdatedAt: expected,
      currentUpdatedAt: current.updatedAt,
      current
    })
  }
}

/* ------------------------------------------------------------- constants */

export const NOTE_DEFAULT_LIMIT = 100
export const NOTE_MAX_LIMIT = 500
export const RECORD_DEFAULT_LIMIT = 100
export const RECORD_MAX_LIMIT = 500
export const MAX_BODY_BYTES = 5 * 1024 * 1024
export const MAX_ATTACHMENT_BYTES = 20 * 1024 * 1024

export type { EntityType, EventType, Relation }