import { sqliteView, text } from 'drizzle-orm/sqlite-core'

/**
 * project.db 0001_core view: one row per soft-deleted entity. `title` carries
 * the entity's display text (record title, evidence excerpt, run id, report
 * period, …), matching the SQL definition exactly.
 */
export const vTrash = sqliteView('v_trash', {
  entityType: text('entity_type'),
  id: text('id'),
  title: text('title'),
  deletedAt: text('deleted_at')
}).existing()

export type TrashRow = typeof vTrash.$inferSelect