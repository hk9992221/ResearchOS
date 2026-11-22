import type { ActivityActor, ActivityEventType, EntityType, Idea, Task } from '@researchos/shared'

import type { Db } from '../../db/open'

/**
 * Collaborators of the project/dashboard/overview module. They are structural
 * ports, not imports: the foundation behaviour (events, confirmations) and the
 * sibling services (notes, kanban, ideas) land from other workstreams, so each
 * is injected by the composition root and can never be reached around.
 */

/** One Activity row to append. */
export interface ActivityEventInput {
  type: ActivityEventType
  entityType: EntityType
  entityId: string
  summary: string
  actor: ActivityActor
  old?: Record<string, unknown>
  new?: Record<string, unknown>
}

/**
 * Foundation EventBus surface used here (`events.EventBus.emit(tx, event)`).
 * It writes inside the caller's transaction and validates event/payload rules.
 */
export interface ActivityEmitter {
  emit(db: Db, event: ActivityEventInput): void
}

export type ConfirmationKind = 'delete_project'

/**
 * Foundation confirmation service: one-time tokens with a TTL
 * and an exact scope set. The desktop issues the token at the final delete
 * step; `consume` throws on an unknown/expired/mismatched token.
 */
export interface ConfirmationPort {
  issue(kind: ConfirmationKind, scope: readonly string[]): string
  consume(token: string, kind: ConfirmationKind, scope: readonly string[]): void
}

/** Note creation used by the quick-note action. */
export interface NoteCreatorPort {
  createScratchpad(input: {
    projectId: string
    title: string
    bodyMd: string
    actor: ActivityActor
  }): { id: string }
}

/** Task completion used by the Overview quick action. */
export interface TaskCompleterPort {
  completeTask(input: { projectId: string; taskId: string; actor: ActivityActor }): void
}

/** Task creation used by the quick-add and derive actions. */
export interface TaskCreatorPort {
  createTask(input: {
    projectId: string
    title: string
    column?: string
    weekPlan?: string
  }): Task
}

/** Idea creation used by the quick-add action. */
export interface IdeaCreatorPort {
  createIdea(input: { projectId: string; title: string }): Idea
}

/** Reports a blocker code while a permanent delete is being prepared. */
export type BusyProbe = (projectId: string) => string | null