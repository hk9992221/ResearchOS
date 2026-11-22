/**
 * Registers and probes external references — experiment directories and Git
 * repositories — without ever writing to them. Probing is
 * `stat`-based: no repository object is read and no git command is executed.
 */
import { existsSync, statSync, type Stats } from 'node:fs'
import * as path from 'node:path'

import { type Clock, SystemClock, formatTimestamp, parseTimestamp } from './clock'
import { AppErrorCode, appError } from './errors'
import type { EventBus, EventTx } from './events'
import { isValidId, newId } from './ids'
import type { EntityType } from './schema'
import type { ProjectHandle } from './workspace/handle'

export const FIELD_DIR_REF = 'dir_ref'
export const FIELD_GIT_REPO = 'git_repo'
export type RefField = typeof FIELD_DIR_REF | typeof FIELD_GIT_REPO

export const KIND_DIRECTORY = 'directory'
export const KIND_FILE = 'file'
export const KIND_GIT_REPOSITORY = 'git_repository'
export type RefKind = typeof KIND_DIRECTORY | typeof KIND_FILE | typeof KIND_GIT_REPOSITORY

export const REF_STATUS_OK = 'ok'
export const REF_STATUS_BROKEN = 'broken'
export type RefStatusValue = typeof REF_STATUS_OK | typeof REF_STATUS_BROKEN

export interface ExternalRef {
  id: string
  /** `experiment` or `run`. */
  ownerType: EntityType
  ownerId: string
  field: RefField
  kind: RefKind
  /** Absolute path. */
  path: string
  status: RefStatusValue
  lastCheckedAt?: Date
}

/** A probe result: the stored row plus the live check. */
export interface RefProbe extends ExternalRef {
  exists: boolean
  reason: string
}

export interface RefInput {
  ownerType: EntityType
  ownerId: string
  field: RefField
  kind: RefKind
  path: string
}

export interface RelocateRequest {
  refId: string
  newPath: string
}

const SELECT_COLUMNS =
  'SELECT id, owner_type, owner_id, field, ref_kind, path, status, last_checked_at FROM external_refs'

interface RefRow {
  id: string
  owner_type: EntityType
  owner_id: string
  field: RefField
  ref_kind: RefKind
  path: string
  status: RefStatusValue
  last_checked_at: string | null
}

const OWNING_TABLE: Readonly<Partial<Record<EntityType, string>>> = {
  experiment: 'experiments',
  run: 'runs'
}

export class ExternalRefService {
  private readonly project: ProjectHandle
  private readonly events: EventBus
  private readonly clock: Clock

  constructor(project: ProjectHandle, events: EventBus, clock: Clock = new SystemClock()) {
    this.project = project
    this.events = events
    this.clock = clock
  }

  /**
   * Creates or updates the `(owner_type, owner_id, field)` registration. The
   * path must already exist and match the kind, else `REF_NOT_FOUND`. The
   * caller composes this call with the owning-column write in the same tx.
   */
  register(tx: EventTx, input: RefInput): string {
    this.validateOwner(input.ownerType, input.ownerId)
    if (input.field !== FIELD_DIR_REF && input.field !== FIELD_GIT_REPO) {
      throw appError(AppErrorCode.InvalidArgument, `externalrefs: invalid field ${JSON.stringify(input.field)}`)
    }
    if (!isRefKind(input.kind)) {
      throw appError(AppErrorCode.InvalidArgument, `externalrefs: invalid ref kind ${JSON.stringify(input.kind)}`)
    }
    if (!path.isAbsolute(input.path)) {
      throw appError(AppErrorCode.InvalidArgument, `externalrefs: path ${JSON.stringify(input.path)} must be absolute`)
    }
    const probe = probePath(input.kind, input.path)
    if (!probe.exists) {
      throw appError(AppErrorCode.RefNotFound, `${input.path} (${probe.reason})`)
    }
    const now = formatTimestamp(this.clock.now())

    const existing = tx.db
      .prepare('SELECT id FROM external_refs WHERE owner_type = ? AND owner_id = ? AND field = ?')
      .get(input.ownerType, input.ownerId, input.field) as { id: string } | undefined
    if (existing === undefined) {
      const id = newId(this.clock)
      tx.db
        .prepare(
          `INSERT INTO external_refs(id, owner_type, owner_id, field, ref_kind, path, status, last_checked_at, created_at, updated_at)
           VALUES (?, ?, ?, ?, ?, ?, 'ok', ?, ?, ?)`
        )
        .run(id, input.ownerType, input.ownerId, input.field, input.kind, input.path, now, now, now)
      return id
    }
    tx.db
      .prepare(
        `UPDATE external_refs SET ref_kind = ?, path = ?, status = 'ok', last_checked_at = ?, updated_at = ?
         WHERE id = ?`
      )
      .run(input.kind, input.path, now, now, existing.id)
    return existing.id
  }

  /** Probes the given references read-only; an empty list means all. */
  check(refIds: readonly string[] = []): RefProbe[] {
    return this.load(refIds).map((ref) => ({ ...ref, ...probePath(ref.kind, ref.path) }))
  }

  /** Probes and records `status`/`last_checked_at` in one transaction. */
  checkAndRecord(refIds: readonly string[] = []): RefProbe[] {
    const probes = this.check(refIds)
    const now = formatTimestamp(this.clock.now())
    this.project.inTx(() => {
      const statement = this.project.db.prepare(
        'UPDATE external_refs SET status = ?, last_checked_at = ?, updated_at = ? WHERE id = ?'
      )
      for (const probe of probes) {
        statement.run(probe.exists ? REF_STATUS_OK : REF_STATUS_BROKEN, now, now, probe.id)
      }
    })
    return probes
  }

  /** Probes every reference of one owner. */
  checkOwner(ownerType: EntityType, ownerId: string): RefProbe[] {
    const rows = this.project.db
      .prepare(`${SELECT_COLUMNS} WHERE owner_type = ? AND owner_id = ? ORDER BY created_at, id`)
      .all(ownerType, ownerId) as RefRow[]
    return rows.map((row) => {
      const ref = decode(row)
      return { ...ref, ...probePath(ref.kind, ref.path) }
    })
  }

  /**
   * Validates the new path and, in one transaction, updates the owning column
   * (`dir_ref` only; `git_repo` has no baseline column), the reference row and
   * emits `entity.relocated`. History is never cleared.
   */
  relocate(request: RelocateRequest, actor: 'user' | 'mcp' | 'ai'): RefProbe {
    const ref = this.byId(request.refId)
    if (ref === undefined) {
      throw appError(AppErrorCode.RefNotFound, `reference ${request.refId}`)
    }
    if (!path.isAbsolute(request.newPath)) {
      throw appError(AppErrorCode.InvalidArgument, `externalrefs: path ${JSON.stringify(request.newPath)} must be absolute`)
    }
    const probe = probePath(ref.kind, request.newPath)
    if (!probe.exists) {
      throw appError(AppErrorCode.RefNotFound, `${request.newPath} (${probe.reason})`)
    }

    const now = formatTimestamp(this.clock.now())
    this.project.inTx(() => {
      if (ref.field === FIELD_DIR_REF) {
        const table = OWNING_TABLE[ref.ownerType]
        if (table === undefined) {
          throw appError(AppErrorCode.BadEntityType, JSON.stringify(ref.ownerType))
        }
        const result = this.project.db
          .prepare(`UPDATE ${table} SET dir_ref = ?, updated_at = ? WHERE id = ?`)
          .run(request.newPath, now, ref.ownerId)
        if (result.changes === 0) {
          throw appError(
            AppErrorCode.NotFound,
            `externalrefs: owning ${ref.ownerType} ${ref.ownerId} not found`
          )
        }
      }
      this.project.db
        .prepare(
          `UPDATE external_refs SET path = ?, status = 'ok', last_checked_at = ?, updated_at = ? WHERE id = ?`
        )
        .run(request.newPath, now, now, request.refId)
      this.events.emit(this.project, {
        type: 'entity.relocated',
        entityType: ref.ownerType,
        entityId: ref.ownerId,
        old: { path: ref.path },
        new: { path: request.newPath },
        summary: '重新定位',
        actor,
        at: this.clock.now()
      })
    })

    return {
      ...ref,
      path: request.newPath,
      status: REF_STATUS_OK,
      lastCheckedAt: this.clock.now(),
      exists: true,
      reason: ''
    }
  }

  private load(refIds: readonly string[]): ExternalRef[] {
    if (refIds.length === 0) {
      const rows = this.project.db
        .prepare(`${SELECT_COLUMNS} ORDER BY created_at, id`)
        .all() as RefRow[]
      return rows.map(decode)
    }
    const rows = this.project.db
      .prepare(
        `${SELECT_COLUMNS} WHERE id IN (${refIds.map(() => '?').join(', ')}) ORDER BY created_at, id`
      )
      .all(...refIds) as RefRow[]
    return rows.map(decode)
  }

  private byId(id: string): ExternalRef | undefined {
    const row = this.project.db.prepare(`${SELECT_COLUMNS} WHERE id = ?`).get(id) as
      | RefRow
      | undefined
    return row === undefined ? undefined : decode(row)
  }

  private validateOwner(ownerType: EntityType, ownerId: string): void {
    if (ownerType !== 'experiment' && ownerType !== 'run') {
      throw appError(
        AppErrorCode.BadEntityType,
        `external references belong to experiment or run, got ${JSON.stringify(ownerType)}`
      )
    }
    if (!isValidId(ownerId)) throw appError(AppErrorCode.BadEntityId, JSON.stringify(ownerId))
  }
}

function decode(row: RefRow): ExternalRef {
  const ref: ExternalRef = {
    id: row.id,
    ownerType: row.owner_type,
    ownerId: row.owner_id,
    field: row.field,
    kind: row.ref_kind,
    path: row.path,
    status: row.status
  }
  if (row.last_checked_at !== null) ref.lastCheckedAt = parseTimestamp(row.last_checked_at)
  return ref
}

function isRefKind(value: unknown): value is RefKind {
  return value === KIND_DIRECTORY || value === KIND_FILE || value === KIND_GIT_REPOSITORY
}

/** The §5.12 probe rules, `stat` only. */
function probePath(kind: RefKind, target: string): { exists: boolean; reason: string } {
  let info: Stats
  try {
    info = statSync(target)
  } catch (error) {
    const code = (error as NodeJS.ErrnoException).code
    if (code === 'ENOENT') return { exists: false, reason: '不存在' }
    if (code === 'EACCES' || code === 'EPERM') return { exists: false, reason: '权限拒绝' }
    return { exists: false, reason: error instanceof Error ? error.message : String(error) }
  }
  switch (kind) {
    case KIND_DIRECTORY:
      return info.isDirectory() ? { exists: true, reason: '' } : { exists: false, reason: '不是目录' }
    case KIND_FILE:
      return info.isDirectory() ? { exists: false, reason: '不是文件' } : { exists: true, reason: '' }
    case KIND_GIT_REPOSITORY:
      if (!info.isDirectory()) return { exists: false, reason: '不是目录' }
      // A `.git` directory or a `.git` file (worktrees) both qualify.
      return existsSync(path.join(target, '.git'))
        ? { exists: true, reason: '' }
        : { exists: false, reason: '不是 git 仓库' }
  }
}