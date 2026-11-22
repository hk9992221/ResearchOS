import type { EntityId } from '../types/common'

/** Recycle bin: `deleted_at` rows surfaced by the `v_trash` view. */
export const TRASH_ENTITY_TYPES = [
  'note',
  'paper',
  'idea',
  'experiment',
  'run',
  'task',
  'weekly_report'
] as const
export type TrashEntityType = (typeof TRASH_ENTITY_TYPES)[number]

export interface TrashEntry {
  type: TrashEntityType
  id: EntityId
  title: string
  deletedAt: string
  fileState: 'ok' | 'missing'
}

export interface TrashRef {
  type: TrashEntityType
  id: EntityId
}

/** First confirmation tier: exact impact plus the one-time token. */
export interface TrashPlan {
  token: string
  count: number
  warnings: string[]
}

export interface TrashCommands {
  'trash.list': { req: { projectId: string }; res: TrashEntry[] }
  'trash.restore': { req: { projectId: string; items: TrashRef[] }; res: { restored: number } }
  'trash.purge.plan': { req: { projectId: string; items: TrashRef[] }; res: TrashPlan }
  'trash.purge.confirm': { req: { token: string }; res: { purged: number } }
  'trash.empty.plan': { req: { projectId: string }; res: TrashPlan }
  'trash.empty.confirm': { req: { token: string }; res: { emptied: number } }
}