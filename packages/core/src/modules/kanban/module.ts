import { AppErrorCode, AppError, type ActivityActor, type KanbanTask, type TaskColumnInfo } from '@researchos/shared'

import { SystemClock, formatTimestamp, type Clock } from '../../clock'
import { newId } from '../../ids'
import { NullLogger, type Logger } from '../../logger'
import type { KanbanDeps, Db, KanbanEvent, ProjectStatus } from './deps'
import { ChecklistService } from './checklist'
import { ColumnService } from './columns'
import { LabelService } from './labels'
import { LinkService } from './links'
import { TaskService } from './tasks'
import { ViewService } from './views'
import { DEFAULT_COLUMNS, LIMITS } from './types'
import { assertTaskId, conflict, notFound } from './validate'
import { getTaskRow, mapTask } from './repo'

/** One task_columns snapshot for a status.changed payload. */
export function columnState(column: TaskColumnInfo): Record<string, unknown> {
  return { column: column.name, column_name: column.name, is_done: column.isDone }
}

export function quoteTitle(title: string): string {
  return `「${title}」`
}

/**
 * Module bound to one project database. The desktop UI, the overview
 * `TaskCompleter` port and the MCP adapter all call these services, so
 * validation and Activity writes live in exactly one place.
 *
 * Every write runs inside one `BEGIN IMMEDIATE` transaction; events are
 * written through `deps.events` in that transaction, never after commit.
 */
export class KanbanModule {
  readonly deps: KanbanDeps
  readonly clock: Clock
  readonly logger: Logger
  readonly tasks: TaskService
  readonly columns: ColumnService
  readonly checklist: ChecklistService
  readonly labels: LabelService
  readonly links: LinkService
  readonly views: ViewService

  private readonly idFactory: () => string

  constructor(deps: KanbanDeps) {
    this.deps = deps
    this.clock = deps.clock ?? new SystemClock()
    this.logger = deps.logger ?? new NullLogger()
    this.idFactory = deps.generateId ?? (() => newId(this.clock))
    this.tasks = new TaskService(this)
    this.columns = new ColumnService(this)
    this.checklist = new ChecklistService(this)
    this.labels = new LabelService(this)
    this.links = new LinkService(this)
    this.views = new ViewService(this)
  }

  get db(): Db {
    return this.deps.db
  }

  get projectId(): string {
    return this.deps.projectId
  }

  newId(): string {
    return this.idFactory()
  }

  now(): string {
    return formatTimestamp(this.clock.now())
  }

  /** `BEGIN IMMEDIATE` transaction: the single writer per project DB. */
  tx<T>(fn: (tx: Db) => T): T {
    const wrapped = this.deps.db.transaction(() => fn(this.deps.db))
    // better-sqlite3 types `immediate(...params)` from the transaction
    // callback's parameters; the real call takes none (single connection).
    const immediate = wrapped as unknown as { immediate(): T }
    return immediate.immediate()
  }

  /** Transaction that first applies the one-time default column seed. */
  seededTx<T>(fn: (tx: Db) => T): T {
    return this.tx((tx) => {
      this.ensureDefaultColumns(tx)
      return fn(tx)
    })
  }

  /** Archived projects are read-only (plan §4.7, MCP archived gate). */
  assertWritable(): void {
    const status: ProjectStatus = this.deps.projectStatus(this.projectId)
    if (status === 'archived') {
      throw new AppError(AppErrorCode.ArchivedReadOnly, 'Project is archived and read-only', {
        projectId: this.projectId
      })
    }
  }

  /**
   * Seeds Backlog/Todo/In Progress/Review/Done once. The check is “table is
   * empty”, so a user rename/reorder/deletion of a default column is never
   * undone and a second call inserts nothing.
   */
  private ensureDefaultColumns(tx: Db): void {
    const row = tx.prepare('SELECT count(*) AS n FROM task_columns').get() as { n: number }
    if (row.n > 0) return
    const at = this.now()
    const insert = tx.prepare(
      'INSERT INTO task_columns(name, kind, is_done, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)'
    )
    DEFAULT_COLUMNS.forEach((column, index) => {
      insert.run(column.name, column.kind, column.isDone ? 1 : 0, (index + 1) * LIMITS.sortStep, at, at)
    })
  }

  /**
   * Loads a task for a write: alive, unarchived, and with an unarchived
   * parent. Archived tasks accept only unarchive/restore/delete.
   */
  loadGateTask(db: Db, id: string): KanbanTask {
    assertTaskId(id)
    const row = getTaskRow(db, id)
    if (row === undefined || row.deleted_at !== null) {
      throw notFound(`task ${id} not found`, { taskId: id })
    }
    const task = mapTask(row)
    if (task.archivedAt !== null) {
      throw conflict(`task ${id} is archived; unarchive it first`, { taskId: id, reason: 'task-archived' })
    }
    if (task.parentTaskId !== null) {
      const parent = getTaskRow(db, task.parentTaskId)
      if (parent !== undefined && parent.archived_at !== null) {
        throw conflict(`parent task ${task.parentTaskId} is archived`, {
          taskId: id,
          parentTaskId: task.parentTaskId,
          reason: 'parent-archived'
        })
      }
    }
    return task
  }

  /** Fresh read of one task, used by every write response (no stale echoes). */
  readTask(db: Db, id: string): KanbanTask {
    const row = getTaskRow(db, id)
    if (row === undefined) throw notFound(`task ${id} not found`, { taskId: id })
    return mapTask(row)
  }

  actor(actor: ActivityActor | undefined): ActivityActor {
    return actor ?? 'user'
  }

  /** Emits one Activity row inside the caller transaction. */
  emit(tx: Db, event: KanbanEvent): void {
    this.deps.events.emit({ db: tx }, event)
  }

  /** `entity.created` for a new task (title, column, optional parent). */
  createdEvent(actor: ActivityActor | undefined, task: KanbanTask): KanbanEvent {
    const payload: Record<string, unknown> = { title: task.title, column: task.column }
    if (task.parentTaskId !== null) payload.parent_task_id = task.parentTaskId
    return {
      type: 'entity.created',
      entityType: 'task',
      entityId: task.id,
      new: payload,
      summary: `创建任务${quoteTitle(task.title)}`,
      actor: this.actor(actor)
    }
  }
}