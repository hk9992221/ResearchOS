import { sql } from 'drizzle-orm'
import { check, index, integer, sqliteTable, text } from 'drizzle-orm/sqlite-core'

import { evidence, notes } from './core'

/**
 * project.db 0500_paper_annotations (rewrite addition): page-precise PDF
 * annotations. Wired into `db/schema/index.ts` by the integration owner.
 */
export const paperAnnotations = sqliteTable(
  'paper_annotations',
  {
    id: text('id').primaryKey(),
    projectId: text('project_id').notNull(),
    paperId: text('paper_id').notNull(),
    noteId: text('note_id').references(() => notes.id, { onDelete: 'set null' }),
    evidenceId: text('evidence_id').references(() => evidence.id, { onDelete: 'set null' }),
    pageNo: integer('page_no').notNull(),
    rects: text('rects').notNull().default('[]'),
    quote: text('quote').notNull().default(''),
    color: text('color'),
    locator: text('locator'),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull()
  },
  (table) => [
    check('paper_annotations_page_positive', sql`page_no > 0`),
    check('paper_annotations_rects_json', sql`json_valid(rects)`),
    check('paper_annotations_locator_json', sql`locator IS NULL OR json_valid(locator)`),
    index('idx_paper_annotations_paper').on(table.paperId),
    index('idx_paper_annotations_project_paper').on(table.projectId, table.paperId, table.pageNo),
    index('idx_paper_annotations_note')
      .on(table.noteId)
      .where(sql`note_id IS NOT NULL`)
  ]
)