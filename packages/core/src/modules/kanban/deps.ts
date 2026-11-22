import type Database from 'better-sqlite3'

import type { ActivityActor, EntityType, KanbanTaskSuggestions } from '@researchos/shared'

import type { Clock } from '../../clock'
import type { EventTx } from '../../events'
import type { Logger } from '../../logger'
import type { RelationLink, TargetState } from '../../relations'
import type { Actor, Relation } from '../../schema'
import type { TrashRef } from '../../trash'

export type Db = Database.Database

/** Transaction handle: better-sqlite3 reuses the Database type inside a tx. */
export type Tx = Database.Database

/**
 * Activity row this module is allowed to emit: `entity.created` and
 * `status.changed` only. `entity.deleted`/`entity.restored` belong to the
 * trash service, `relation.*` to the relations service.
 */
export interface KanbanEvent {
  type: 'entity.created' | 'status.changed'
  entityType: 'task'
  entityId: string
  old?: Record<string, unknown>
  new?: Record<string, unknown>
  summary: string
  actor: Actor
}

/**
 * Structural view of the foundation event writer (W1-B `./events`). `emit`
 * writes the `activities` row in the caller transaction.
 */
export interface KanbanEventWriter {
  emit(tx: EventTx, event: KanbanEvent): void
}

/**
 * Structural view of the foundation trash service (W1-B `./trash`). The
 * service owns `entity.deleted`/`entity.restored`; kanban never writes those.
 * `TrashService` satisfies this interface as-is.
 */
export interface KanbanTrash {
  softDelete(tx: EventTx, ref: TrashRef, actor: Actor): void
  restore(tx: EventTx, ref: TrashRef, actor: Actor): void
}

/**
 * Structural view of foundation relations (W1-B `./relations`): validation,
 * direction rules, idempotency and `relation.added`/`relation.removed` events
 * live there. `RelationService` satisfies this interface as-is.
 */
export interface KanbanRelations {
  link(tx: EventTx, link: RelationLink, actor: Actor): string
  unlink(tx: EventTx, link: RelationLink, actor: Actor): void
  resolve(dstType: EntityType, dstId: string): TargetState
  to(dstType: EntityType, dstId: string, relation?: Relation): RelationLink[]
}

export type ProjectStatus = 'active' | 'archived'

/**
 * AI task suggestions. The suggester is a read-only
 * collaborator: this module only relays the preview and never writes tasks.
 * W3 owns the readiness/AI implementation; kanban does not stub it — the
 * handler reports `AI_UNAVAILABLE` when nothing is wired.
 */
export interface TaskSuggester {
  available(): boolean
  suggest(projectId: string, options: { ideaId?: string }): Promise<KanbanTaskSuggestions>
}

export interface KanbanDeps {
  /** project.db handle. */
  db: Db
  projectId: string
  /** Archived-project write gate (global.db `projects.status`). */
  projectStatus(projectId: string): ProjectStatus
  /** Required: all Activity writes go through the foundation writer. */
  events: KanbanEventWriter
  /** Optional until a task is deleted/restored; those calls report a config error. */
  trash?: KanbanTrash
  /** Optional until links are used; link operations report a config error. */
  relations?: KanbanRelations
  clock?: Clock
  /** UUIDv7 generator; defaults to the foundation `newId`. */
  generateId?: () => string
  logger?: Logger
}

/** Overview port; kept structural so integration can adapt it. */
export interface TaskCompleter {
  completeTask(projectId: string, taskId: string, actor: ActivityActor): Promise<void>
}

/**
 * Builds the overview adapter from a project → module resolver (the workspace
 * `Open(projectId)` call in integration).
 */
export function createTaskCompleter(resolve: (projectId: string) => KanbanModuleLike): TaskCompleter {
  return {
    async completeTask(projectId, taskId, actor) {
      const module = resolve(projectId)
      module.tasks.complete(actor, taskId, true)
    }
  }
}

/** The subset the completer needs, to avoid an import cycle. */
export interface KanbanModuleLike {
  tasks: { complete(actor: ActivityActor, id: string, done: boolean): unknown }
}