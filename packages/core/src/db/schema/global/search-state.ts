import { sql } from 'drizzle-orm'
import { check, index, integer, primaryKey, sqliteTable, text } from 'drizzle-orm/sqlite-core'

/** global.db 0300_search_index_state: paper source registry, parse state, dirty queue, jobs. */
export const paperIndexSources = sqliteTable(
  'paper_index_sources',
  {
    source: text('source').primaryKey(),
    enabled: integer('enabled').notNull().default(1),
    schemaVersion: integer('schema_version').notNull().default(1),
    parserVersion: text('parser_version').notNull().default(''),
    updatedAt: text('updated_at').notNull()
  },
  (table) => [check('paper_index_sources_enabled_boolean', sql`enabled IN (0,1)`)]
)

export const paperParseState = sqliteTable('paper_parse_state', {
  paperId: text('paper_id').primaryKey(),
  fileHash: text('file_hash').notNull().default(''),
  parserVersion: text('parser_version').notNull(),
  pageCount: integer('page_count').notNull().default(0),
  parsedAt: text('parsed_at').notNull()
})

export const paperIndexDirty = sqliteTable(
  'paper_index_dirty',
  {
    projectId: text('project_id').notNull().default(''),
    entityType: text('entity_type').notNull(),
    entityId: text('entity_id').notNull(),
    op: text('op').notNull(),
    attempts: integer('attempts').notNull().default(0),
    runAfter: text('run_after').notNull(),
    lastError: text('last_error'),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull()
  },
  (table) => [
    primaryKey({ columns: [table.projectId, table.entityType, table.entityId] }),
    check('paper_index_dirty_op_valid', sql`op IN ('upsert','delete','restore')`),
    index('idx_paper_index_dirty_due').on(table.runAfter)
  ]
)

export const paperIndexJobs = sqliteTable(
  'paper_index_jobs',
  {
    jobId: text('job_id').primaryKey(),
    scopeKind: text('scope_kind').notNull(),
    scopeId: text('scope_id').notNull().default(''),
    reparsePdf: integer('reparse_pdf').notNull().default(0),
    rebuildVector: integer('rebuild_vector').notNull().default(0),
    status: text('status').notNull().default('pending'),
    total: integer('total').notNull().default(0),
    processed: integer('processed').notNull().default(0),
    failed: integer('failed').notNull().default(0),
    lastError: text('last_error'),
    cancelRequested: integer('cancel_requested').notNull().default(0),
    createdAt: text('created_at').notNull(),
    startedAt: text('started_at'),
    finishedAt: text('finished_at')
  },
  (table) => [
    check('paper_index_jobs_scope_valid', sql`scope_kind IN ('project','global')`),
    check('paper_index_jobs_reparse_boolean', sql`reparse_pdf IN (0,1)`),
    check('paper_index_jobs_rebuild_boolean', sql`rebuild_vector IN (0,1)`),
    check('paper_index_jobs_status_valid', sql`status IN ('pending','running','done','failed','canceled')`),
    check('paper_index_jobs_cancel_boolean', sql`cancel_requested IN (0,1)`),
    index('idx_paper_index_jobs_status').on(table.status, table.createdAt)
  ]
)