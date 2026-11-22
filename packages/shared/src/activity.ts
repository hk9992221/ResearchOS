import type { EntityType, RelationType } from './entities'
import type { EntityId, IsoDateTime, JsonObject } from './types/common'

/**
 * Activity event contract, docs/schema-catalog.md §E. The registry order is the
 * DDL CHECK order; payloads are append-only JSON objects.
 */
export const ACTIVITY_EVENT_TYPES = [
  'entity.created',
  'entity.deleted',
  'entity.restored',
  'entity.updated',
  'entity.purged',
  'entity.relocated',
  'status.changed',
  'relation.added',
  'relation.removed',
  'note.updated',
  'ai.analysis_completed',
  'paper.imported',
  'paper.overwrite_confirmed',
  'paper.metadata_updated',
  'report.generated',
  'project.exported'
] as const

export type ActivityEventType = (typeof ACTIVITY_EVENT_TYPES)[number]
export type ActivityActor = 'user' | 'mcp' | 'ai'

const EVENT_TYPE_LOOKUP: Readonly<Record<string, true>> = Object.fromEntries(
  ACTIVITY_EVENT_TYPES.map((type) => [type, true])
)
const ACTOR_LOOKUP: Readonly<Record<string, true>> = { user: true, mcp: true, ai: true }

export function isActivityEventType(value: string): value is ActivityEventType {
  return EVENT_TYPE_LOOKUP[value] === true
}

export function isActivityActor(value: string): value is ActivityActor {
  return ACTOR_LOOKUP[value] === true
}

export interface ActivityPayload {
  entity_type: EntityType
  entity_id: EntityId
  old?: JsonObject
  new?: JsonObject
  summary: string
  actor: ActivityActor
  /** Whitelisted only for `note.updated`. */
  save_count?: number
  /** Whitelisted only for `note.updated`. */
  last_saved_at?: string
}

/** The five-key tuple carried by relation.added / relation.removed. */
export interface RelationEventTuple {
  src_type: EntityType
  src_id: EntityId
  relation: RelationType
  dst_type: EntityType
  dst_id: EntityId
}

export interface NoteUpdatedPayload extends ActivityPayload {
  save_count: number
  last_saved_at: string
}

export interface ActivityEvent {
  id: EntityId
  eventType: ActivityEventType
  entityType: EntityType
  entityId: EntityId
  payload: ActivityPayload
  actor: ActivityActor
  mergeKey: string | null
  createdAt: IsoDateTime
}