/**
 * The Activity contract and its single writer/reader.
 * Activities are append-only; the only exception is the same-day merge of
 * `note.updated` rows, implemented as the partial-unique-index UPSERT.
 * Business code never writes the `activities` table directly.
 *
 * Every write happens inside the caller's transaction so the business row and
 * its event commit atomically. The renderer/index notification is *published
 * after commit*: `emit` queues a transient `entity.changed` carrier and a
 * microtask drains the queue only for activities whose row is actually visible
 * (a rolled-back transaction leaves no row, so nothing is published).
 */
import type Database from 'better-sqlite3'

import { type Clock, SystemClock, formatTimestamp, parseTimestamp } from './clock'
import { inImmediateTransaction } from './db/drizzle'
import { AppErrorCode, appError } from './errors'
import { isValidId, newId } from './ids'
import {
  type Actor,
  type EntityType,
  type EventType,
  isActor,
  isEntityType,
  isEventType
} from './schema'

export type Db = Database.Database

/**
 * Anything that exposes an open connection: a `ProjectHandle`, a `GlobalHandle`
 * or a bare `{ db }`. `emit` relies on the caller having opened the write
 * transaction (`handle.inTx`) so the event and the business write share it.
 */
export interface EventTx {
  readonly db: Db
}

/** The only legal shape for an event's old/new: a JSON object at the top level. */
export type EventObject = Record<string, unknown>

/**
 * One activity to write. A missing `at` means the bus clock; a caller that has
 * already stamped the business row passes the same instant so the payload and
 * the row agree.
 */
export interface ActivityEvent {
  type: EventType
  entityType: EntityType
  entityId: string
  old?: EventObject | null
  new?: EventObject | null
  summary: string
  actor: Actor
  at?: Date
}

/** Input of the merged `note.updated` path. */
export interface NoteUpdatedEvent {
  noteId: string
  summary: string
  actor: Actor
  at?: Date
}

/** The decoded payload column. Extra keys exist only for `note.updated`. */
export interface ActivityPayload {
  entity_type: EntityType
  entity_id: string
  old?: EventObject
  new?: EventObject
  summary: string
  actor: Actor
  save_count?: number
  last_saved_at?: string
}

/** One stored activity, with the projection columns cross-checked. */
export interface Activity {
  id: string
  type: EventType
  entityType: EntityType
  entityId: string
  payload: ActivityPayload
  actor: Actor
  createdAt: Date
  /** The stored UTC timestamp, for exact ordering. */
  createdAtText: string
}

export type BucketKind = 'day' | 'entity_type' | 'event_type' | 'actor'

export interface ActivityQuery {
  since?: Date
  until?: Date
  entityTypes?: readonly EntityType[]
  entityId?: string
  eventTypes?: readonly EventType[]
  actors?: readonly Actor[]
  /** Defaults to 50, capped at 500. */
  limit?: number
  offset?: number
}

export interface AggregateQuery extends ActivityQuery {
  bucket: BucketKind
}

export interface Bucket {
  key: string
  count: number
}

/**
 * What a post-commit subscriber receives: a transient carrier, never a stored
 * row reference. The renderer invalidates the affected query keys from it.
 */
export interface ActivityPublished {
  activityId: string
  eventType: EventType
  entityType: EntityType
  entityId: string
  actor: Actor
  createdAt: string
}

export interface EventBusOptions {
  clock?: Clock
  /**
   * IANA zone defining the natural-day boundary of `note.updated` merging and
   * `day` aggregation (Go: the service `*time.Location`). Defaults to the host
   * zone, which is what the Go build does with `time.Local`.
   */
  timeZone?: string
  /** Post-commit subscriber (renderer invalidation + index hook). */
  publish?: (event: ActivityPublished) => void
  /** Receives subscriber failures; they never break the write path. */
  onPublishError?: (error: unknown, event: ActivityPublished) => void
}

const MAX_SUMMARY_RUNES = 200
const DEFAULT_LIMIT = 50
const MAX_LIMIT = 500
const MERGE_KEY_PREFIX = 'note.updated:'
const RELATION_TUPLE_KEYS = ['src_type', 'src_id', 'relation', 'dst_type', 'dst_id'] as const
const ID_BATCH = 500

function shapeError(message: string): never {
  throw appError(AppErrorCode.BadPayloadShape, message)
}

function isPlainObject(value: unknown): value is EventObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/**
 * Decodes raw JSON into an old/new object. Arrays, scalars, null and trailing
 * data are rejected with `BAD_PAYLOAD_SHAPE`.
 */
export function parsePayloadObject(raw: string): EventObject {
  const trimmed = raw.trim()
  if (trimmed === '' || trimmed.startsWith('{') === false) {
    shapeError('old/new must be a JSON object')
  }
  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed)
  } catch (error) {
    shapeError(`old/new: ${error instanceof Error ? error.message : String(error)}`)
  }
  if (!isPlainObject(parsed)) shapeError('old/new must be a JSON object')
  return parsed
}

function truncateSummary(summary: string): string {
  const runes = Array.from(summary)
  return runes.length <= MAX_SUMMARY_RUNES ? summary : runes.slice(0, MAX_SUMMARY_RUNES).join('')
}

function normalizedLimit(limit: number | undefined): number {
  if (limit === undefined || limit <= 0) return DEFAULT_LIMIT
  return limit > MAX_LIMIT ? MAX_LIMIT : Math.trunc(limit)
}

function normalizedOffset(offset: number | undefined): number {
  return offset === undefined || offset < 0 ? 0 : Math.trunc(offset)
}

function placeholders(count: number): string {
  return new Array(count).fill('?').join(', ')
}

/** The `save_count`/`last_saved_at` extras are whitelisted for `note.updated` only. */
function validateShape(event: ActivityEvent): void {
  switch (event.type) {
    case 'relation.added':
      if (event.new == null) shapeError('relation.added requires new with the 5-key tuple')
      validateRelationObject(event.new)
      if (event.old != null) validateRelationObject(event.old)
      return
    case 'relation.removed':
      if (event.old == null) shapeError('relation.removed requires old with the 5-key tuple')
      validateRelationObject(event.old)
      if (event.new != null) validateRelationObject(event.new)
      return
    case 'entity.updated':
      if (event.old == null && event.new == null) shapeError('entity.updated requires old or new')
      return
    case 'entity.relocated':
      if (event.old == null || event.new == null) shapeError('entity.relocated requires old and new')
      if (!hasKey(event.old, 'path') || !hasKey(event.new, 'path')) {
        shapeError('entity.relocated old and new must carry path')
      }
      return
    case 'entity.purged':
      if (event.old != null || event.new != null) shapeError('entity.purged must not carry old or new')
      return
    case 'status.changed': {
      if (event.old == null && event.new == null) shapeError('status.changed requires old or new')
      // A task side carries {column|blocked|archived}; every other
      // status-bearing entity carries {status}.
      if (event.entityType === 'task') {
        const oldOK = event.old != null && taskStatusShape(event.old)
        const newOK = event.new != null && taskStatusShape(event.new)
        if (!oldOK && !newOK) {
          shapeError('status.changed for task must carry column, blocked or archived')
        }
        return
      }
      if (event.old != null && !hasKey(event.old, 'status')) {
        shapeError('status.changed old must carry status')
      }
      if (event.new != null && !hasKey(event.new, 'status')) {
        shapeError('status.changed new must carry status')
      }
      return
    }
    default:
      return
  }
}

function validateRelationObject(object: EventObject): void {
  const keys = Object.keys(object)
  if (keys.length !== RELATION_TUPLE_KEYS.length) {
    shapeError(
      `relation payload must contain exactly ${RELATION_TUPLE_KEYS.length} keys, got ${keys.length}`
    )
  }
  for (const key of RELATION_TUPLE_KEYS) {
    const value = object[key]
    if (value === undefined) shapeError(`relation payload is missing ${JSON.stringify(key)}`)
    if (typeof value !== 'string') {
      shapeError(`relation payload ${JSON.stringify(key)} must be a string`)
    }
  }
}

function hasKey(object: EventObject, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(object, key)
}

/** `{column…}` for column transitions, `{blocked}` or `{archived}` markers. */
function taskStatusShape(object: EventObject): boolean {
  return hasKey(object, 'column') || hasKey(object, 'blocked') || hasKey(object, 'archived')
}

function serializePayload(payload: ActivityPayload): string {
  try {
    return JSON.stringify(payload)
  } catch (error) {
    shapeError(`payload is not serializable: ${error instanceof Error ? error.message : String(error)}`)
  }
}

const INSERT_ACTIVITY = `INSERT INTO activities(id, event_type, entity_type, entity_id, payload, actor, merge_key, created_at)
VALUES (?, ?, ?, ?, ?, ?, NULL, ?)`

/**
 * The §5.6.2 statement, verbatim. The conflict target repeats the partial-index
 * predicate. `created_at` stays at the first save of the day; the payload's
 * save_count/last_saved_at/actor/summary move forward.
 */
const NOTE_UPDATED_UPSERT = `INSERT INTO activities(id, event_type, entity_type, entity_id, payload, actor, merge_key, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(merge_key) WHERE merge_key IS NOT NULL DO UPDATE SET
  payload = json_set(activities.payload,
                     '$.save_count',    json_extract(activities.payload, '$.save_count') + 1,
                     '$.last_saved_at', excluded.created_at,
                     '$.actor',         excluded.actor,
                     '$.summary',       json_extract(excluded.payload, '$.summary'))`

interface PendingPublish {
  db: Db
  published: ActivityPublished
}

/**
 * The only entry point for Activity writes and the read side of the activity
 * feed. One instance per process is fine: the bus is stateless per database and
 * keys internal state by connection.
 */
export class EventBus {
  private readonly clock: Clock
  private readonly timeZone: string
  private readonly publish: ((event: ActivityPublished) => void) | undefined
  private readonly onPublishError: ((error: unknown, event: ActivityPublished) => void) | undefined
  private pending: PendingPublish[] = []
  private flushScheduled = false

  constructor(options: EventBusOptions = {}) {
    this.clock = options.clock ?? new SystemClock()
    this.timeZone = options.timeZone ?? resolvedTimeZone()
    this.publish = options.publish
    this.onPublishError = options.onPublishError
    // Validate the zone once so a typo fails at wiring time, not on first save.
    getDayFormatter(this.timeZone)
  }

  /**
   * Validates `event` and writes it inside the caller's transaction. Returns
   * the new activity id. `note.updated` must go through `emitNoteUpdated`.
   */
  emit(tx: EventTx, event: ActivityEvent): string {
    const prepared = this.prepare(event)
    tx.db
      .prepare(INSERT_ACTIVITY)
      .run(
        prepared.id,
        prepared.type,
        prepared.entityType,
        prepared.entityId,
        prepared.payload,
        prepared.actor,
        prepared.createdAtText
      )
    this.schedule(
      tx.db,
      prepared.id,
      prepared.type,
      prepared.entityType,
      prepared.entityId,
      prepared.actor,
      prepared.createdAtText
    )
    return prepared.id
  }

  /**
   * Writes an event in its own transaction, for events that accompany no
   * business write. When called inside an outer transaction it nests as a
   * savepoint and publication waits for the outer commit.
   */
  emitStandalone(tx: EventTx, event: ActivityEvent): string {
    const id = inImmediateTransaction(tx.db, () => this.emit(tx, event))
    if (!tx.db.inTransaction) this.flush()
    return id
  }

  /**
   * Writes or merges the `note.updated` row for the local day of `at`. This is
   * the only way to write that type.
   */
  emitNoteUpdated(tx: EventTx, event: NoteUpdatedEvent): string {
    if (!isValidId(event.noteId)) {
      throw appError(AppErrorCode.BadEntityId, `${JSON.stringify(event.noteId)}`)
    }
    if (!isActor(event.actor)) throw appError(AppErrorCode.BadActor, `${JSON.stringify(event.actor)}`)
    const at = event.at ?? this.clock.now()
    const atText = formatTimestamp(at)
    const id = newId(this.clock)
    const payload: ActivityPayload = {
      entity_type: 'note',
      entity_id: event.noteId,
      summary: truncateSummary(event.summary),
      actor: event.actor,
      save_count: 1,
      last_saved_at: atText
    }
    const mergeKey = `${MERGE_KEY_PREFIX}${event.noteId}:${this.dayKey(at)}`
    tx.db
      .prepare(NOTE_UPDATED_UPSERT)
      .run(id, 'note.updated', 'note', event.noteId, serializePayload(payload), event.actor, mergeKey, atText)
    this.schedule(tx.db, id, 'note.updated', 'note', event.noteId, event.actor, atText)
    return id
  }

  /** Activities matching `query`, newest first. */
  list(tx: EventTx, query: ActivityQuery = {}): Activity[] {
    const { where, args } = this.where(query)
    const rows = tx.db
      .prepare(
        `SELECT id, event_type, entity_type, entity_id, payload, actor, created_at
         FROM activities${where} ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
      )
      .all(
        ...args,
        normalizedLimit(query.limit),
        normalizedOffset(query.offset)
      ) as ActivityRow[]
    return rows.map((row) => this.decodeActivity(row))
  }

  /** Number of matching activities; limit and offset are ignored. */
  count(tx: EventTx, query: ActivityQuery = {}): number {
    const { where, args } = this.where(query)
    const row = tx.db
      .prepare(`SELECT count(*) AS n FROM activities${where}`)
      .get(...args) as { n: number } | undefined
    return row?.n ?? 0
  }

  /** Groups the full filtered set (limit and offset are ignored). */
  aggregate(tx: EventTx, query: AggregateQuery): Bucket[] {
    const { where, args } = this.where(query)
    switch (query.bucket) {
      case 'day':
        return this.aggregateDay(tx, where, args)
      case 'entity_type':
      case 'event_type':
      case 'actor': {
        const rows = tx.db
          .prepare(
            `SELECT ${query.bucket} AS key, count(*) AS count FROM activities${where}
             GROUP BY ${query.bucket} ORDER BY count(*) DESC, ${query.bucket} ASC`
          )
          .all(...args) as Array<{ key: string; count: number }>
        return rows.map((row) => ({ key: row.key, count: row.count }))
      }
      default:
        throw appError(
          AppErrorCode.InvalidArgument,
          `events: unknown aggregate bucket ${JSON.stringify(String(query.bucket))}`
        )
    }
  }

  /**
   * Publishes every queued activity whose row survived its transaction. Called
   * automatically on the microtask queue and by `emitStandalone`; safe to call
   * at any time (for example on application shutdown).
   */
  flush(): void {
    if (this.pending.length === 0) return
    const ready: PendingPublish[] = []
    const waiting: PendingPublish[] = []
    for (const item of this.pending) {
      if (item.db.inTransaction) waiting.push(item)
      else ready.push(item)
    }
    this.pending = waiting
    if (this.pending.length > 0) this.scheduleFlush()
    if (ready.length === 0) return

    const groups = new Map<Db, PendingPublish[]>()
    for (const item of ready) {
      const group = groups.get(item.db)
      if (group === undefined) groups.set(item.db, [item])
      else group.push(item)
    }
    for (const [db, items] of groups) {
      let visible: Set<string>
      try {
        visible = existingActivityIds(
          db,
          items.map((item) => item.published.activityId)
        )
      } catch (error) {
        // A closed connection must not resurrect events: drop and report.
        for (const item of items) this.reportPublishError(error, item.published)
        continue
      }
      for (const item of items) {
        if (!visible.has(item.published.activityId)) continue
        if (this.publish === undefined) continue
        try {
          this.publish(item.published)
        } catch (error) {
          this.reportPublishError(error, item.published)
        }
      }
    }
  }

  private prepare(event: ActivityEvent): PreparedEvent {
    if (event.type === 'note.updated') {
      throw appError(
        AppErrorCode.WrongEmitPath,
        'note.updated must be emitted with emitNoteUpdated'
      )
    }
    if (!isEventType(event.type)) {
      throw appError(AppErrorCode.BadEventType, JSON.stringify(event.type))
    }
    if (!isActor(event.actor)) throw appError(AppErrorCode.BadActor, JSON.stringify(event.actor))
    if (!isEntityType(event.entityType)) {
      throw appError(AppErrorCode.BadEntityType, JSON.stringify(event.entityType))
    }
    if (!isValidId(event.entityId)) {
      throw appError(AppErrorCode.BadEntityId, JSON.stringify(event.entityId))
    }
    if (event.old != null && !isPlainObject(event.old)) shapeError('old must be a JSON object')
    if (event.new != null && !isPlainObject(event.new)) shapeError('new must be a JSON object')
    validateShape(event)

    const at = event.at ?? this.clock.now()
    const payload: ActivityPayload = {
      entity_type: event.entityType,
      entity_id: event.entityId,
      summary: truncateSummary(event.summary),
      actor: event.actor
    }
    if (event.old != null) payload.old = event.old
    if (event.new != null) payload.new = event.new

    return {
      id: newId(this.clock),
      type: event.type,
      entityType: event.entityType,
      entityId: event.entityId,
      payload: serializePayload(payload),
      actor: event.actor,
      createdAtText: formatTimestamp(at)
    }
  }

  private schedule(
    db: Db,
    id: string,
    type: EventType,
    entityType: EntityType,
    entityId: string,
    actor: Actor,
    createdAt: string
  ): void {
    if (this.publish === undefined) return
    this.pending.push({
      db,
      published: {
        activityId: id,
        eventType: type,
        entityType,
        entityId,
        actor,
        createdAt
      }
    })
    this.scheduleFlush()
  }

  private scheduleFlush(): void {
    if (this.flushScheduled) return
    this.flushScheduled = true
    queueMicrotask(() => {
      this.flushScheduled = false
      this.flush()
    })
  }

  private reportPublishError(error: unknown, event: ActivityPublished): void {
    this.onPublishError?.(error, event)
  }

  private aggregateDay(tx: EventTx, where: string, args: unknown[]): Bucket[] {
    const rows = tx.db.prepare(`SELECT created_at FROM activities${where}`).all(...args) as Array<{
      created_at: string
    }>
    const counts = new Map<string, number>()
    for (const row of rows) {
      const key = this.dayKey(parseTimestamp(row.created_at))
      counts.set(key, (counts.get(key) ?? 0) + 1)
    }
    return [...counts.keys()].sort().map((key) => ({ key, count: counts.get(key) ?? 0 }))
  }

  private dayKey(at: Date): string {
    const parts = getDayFormatter(this.timeZone).formatToParts(at)
    const pick = (type: Intl.DateTimeFormatPartTypes): string =>
      parts.find((part) => part.type === type)?.value ?? ''
    return `${pick('year').padStart(4, '0')}-${pick('month')}-${pick('day')}`
  }

  private where(query: ActivityQuery): { where: string; args: unknown[] } {
    const clauses: string[] = []
    const args: unknown[] = []
    if (query.since !== undefined) {
      clauses.push('created_at >= ?')
      args.push(formatTimestamp(query.since))
    }
    if (query.until !== undefined) {
      clauses.push('created_at <= ?')
      args.push(formatTimestamp(query.until))
    }
    if (query.entityId !== undefined && query.entityId !== '') {
      clauses.push('entity_id = ?')
      args.push(query.entityId)
    }
    if (query.entityTypes !== undefined && query.entityTypes.length > 0) {
      clauses.push(`entity_type IN (${placeholders(query.entityTypes.length)})`)
      args.push(...query.entityTypes)
    }
    if (query.eventTypes !== undefined && query.eventTypes.length > 0) {
      clauses.push(`event_type IN (${placeholders(query.eventTypes.length)})`)
      args.push(...query.eventTypes)
    }
    if (query.actors !== undefined && query.actors.length > 0) {
      clauses.push(`actor IN (${placeholders(query.actors.length)})`)
      args.push(...query.actors)
    }
    return clauses.length === 0 ? { where: '', args } : { where: ` WHERE ${clauses.join(' AND ')}`, args }
  }

  private decodeActivity(row: ActivityRow): Activity {
    let payload: ActivityPayload
    try {
      payload = JSON.parse(row.payload) as ActivityPayload
    } catch (error) {
      throw appError(
        AppErrorCode.BadPayloadShape,
        `events: activity ${row.id} payload: ${error instanceof Error ? error.message : String(error)}`
      )
    }
    if (!isPlainObject(payload)) {
      throw appError(AppErrorCode.BadPayloadShape, `events: activity ${row.id} payload is not an object`)
    }
    if (payload.entity_type !== row.entity_type || payload.entity_id !== row.entity_id) {
      throw appError(
        AppErrorCode.BadPayloadShape,
        `events: activity ${row.id} payload/column mismatch: payload=(${payload.entity_type},${payload.entity_id}) columns=(${row.entity_type},${row.entity_id})`
      )
    }
    // note.updated merge advances payload.actor but not the actor column, so
    // Only merged rows may diverge (the read-path whitelist of the shared-contract spec).
    if (payload.actor !== row.actor && !(row.event_type === 'note.updated' && (payload.save_count ?? 0) > 1)) {
      throw appError(
        AppErrorCode.BadPayloadShape,
        `events: activity ${row.id} payload/column actor mismatch: payload=${payload.actor} column=${row.actor}`
      )
    }
    return {
      id: row.id,
      type: row.event_type,
      entityType: row.entity_type,
      entityId: row.entity_id,
      payload,
      actor: row.actor,
      createdAt: parseTimestamp(row.created_at),
      createdAtText: row.created_at
    }
  }
}

interface PreparedEvent {
  id: string
  type: EventType
  entityType: EntityType
  entityId: string
  payload: string
  actor: Actor
  createdAtText: string
}

interface ActivityRow {
  id: string
  event_type: EventType
  entity_type: EntityType
  entity_id: string
  payload: string
  actor: Actor
  created_at: string
}

function existingActivityIds(db: Db, ids: readonly string[]): Set<string> {
  const found = new Set<string>()
  for (let start = 0; start < ids.length; start += ID_BATCH) {
    const chunk = ids.slice(start, start + ID_BATCH)
    const rows = db
      .prepare(`SELECT id FROM activities WHERE id IN (${placeholders(chunk.length)})`)
      .all(...chunk) as Array<{ id: string }>
    for (const row of rows) found.add(row.id)
  }
  return found
}

const DAY_FORMATTERS = new Map<string, Intl.DateTimeFormat>()

function getDayFormatter(timeZone: string): Intl.DateTimeFormat {
  const cached = DAY_FORMATTERS.get(timeZone)
  if (cached !== undefined) return cached
  const formatter = new Intl.DateTimeFormat('en-US', {
    timeZone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    calendar: 'gregory',
    numberingSystem: 'latn'
  })
  DAY_FORMATTERS.set(timeZone, formatter)
  return formatter
}

function resolvedTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

/** Convenience constructor bound to nothing; the bus is stateless per db. */
export function createEventBus(options: EventBusOptions = {}): EventBus {
  return new EventBus(options)
}