import { sql } from 'drizzle-orm'
import { check, index, integer, sqliteTable, text } from 'drizzle-orm/sqlite-core'

/** project.db 0200_kanban: column configuration and checklist items. */
export const taskColumns = sqliteTable(
  'task_columns',
  {
    name: text('name').primaryKey(),
    kind: text('kind').notNull(),
    isDone: integer('is_done').notNull().default(0),
    position: integer('position').notNull(),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull()
  },
  (table) => [
    check('task_columns_is_done_boolean', sql`is_done IN (0,1)`),
    index('idx_task_columns_position').on(table.position)
  ]
)

export const taskChecklistItems = sqliteTable(
  'task_checklist_items',
  {
    id: text('id').primaryKey(),
    taskId: text('task_id').notNull(),
    text: text('text').notNull(),
    done: integer('done').notNull().default(0),
    sortOrder: integer('sort_order').notNull().default(0),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull()
  },
  (table) => [
    check('task_checklist_items_text_nonempty', sql`length(text) > 0`),
    check('task_checklist_items_done_boolean', sql`done IN (0,1)`),
    index('idx_task_checklist').on(table.taskId, table.sortOrder)
  ]
)