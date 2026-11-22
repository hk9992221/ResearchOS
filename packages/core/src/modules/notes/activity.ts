import type { Actor, EntityType, EventType, Relation } from '../../schema'
import { entityDeleted, notFound, type NotesContext } from './support'

export interface ActivityEventInput {
  type: EventType
  entityType: EntityType
  entityId: string
  old?: Record<string, unknown>
  new?: Record<string, unknown>
  summary: string
  actor: Actor
  at: Date
}

export interface NoteUpdatedInput {
  noteId: string
  summary: string
  actor: Actor
  at: Date
}

/** Writes one activity row inside the caller transaction (foundation writer). */
export function emitEvent(ctx: NotesContext, event: ActivityEventInput): void {
  ctx.events.emit(
    { db: ctx.db },
    {
      type: event.type,
      entityType: event.entityType,
      entityId: event.entityId,
      ...(event.old === undefined ? {} : { old: event.old }),
      ...(event.new === undefined ? {} : { new: event.new }),
      summary: event.summary,
      actor: event.actor,
      at: event.at
    }
  )
}

/** The merged same-day note.updated path lives in the foundation EventBus. */
export function emitNoteUpdated(ctx: NotesContext, input: NoteUpdatedInput): void {
  ctx.events.emitNoteUpdated(
    { db: ctx.db },
    { noteId: input.noteId, summary: input.summary, actor: input.actor, at: input.at }
  )
}

/** Soft delete through the foundation trash service (idempotent, no event). */
export function softDelete(ctx: NotesContext, entityType: EntityType, entityId: string, actor: Actor): void {
  ctx.trash.softDelete({ db: ctx.db }, { type: entityType, id: entityId }, actor)
}

/** Restore through the foundation trash service (idempotent, no event). */
export function restoreEntity(ctx: NotesContext, entityType: EntityType, entityId: string, actor: Actor): void {
  ctx.trash.restore({ db: ctx.db }, { type: entityType, id: entityId }, actor)
}

export interface RelationEdge {
  srcType: EntityType
  srcId: string
  relation: Relation
  dstType: EntityType
  dstId: string
}

/** Inserts one relation triple through the foundation validation service. */
export function linkRelation(ctx: NotesContext, edge: RelationEdge, actor: Actor): void {
  ctx.relations.link({ db: ctx.db }, edge, actor)
}

/** Deletes one relation triple; a missing triple is a silent no-op. */
export function unlinkRelation(ctx: NotesContext, edge: RelationEdge, actor: Actor): void {
  ctx.relations.unlink({ db: ctx.db }, edge, actor)
}

export function requireLiveRow(ctx: NotesContext, table: string, id: string, label: string): void {
  const row: unknown = ctx.db.prepare(`SELECT deleted_at FROM ${table} WHERE id = ?`).get(id)
  if (row === null || typeof row !== 'object' || !('deleted_at' in row)) {
    throw notFound(`${label} ${id} not found`)
  }
  const { deleted_at } = row
  if (deleted_at !== null && deleted_at !== undefined) throw entityDeleted(label, id)
}