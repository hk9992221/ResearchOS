import { sql } from 'drizzle-orm'
import { check, index, integer, sqliteTable, text, uniqueIndex } from 'drizzle-orm/sqlite-core'

/**
 * global.db migration 0001: schema_migrations, projects, papers, providers,
 * role_assignments, settings. `providers` also carries the test columns added by
 * 0100_ai_provider_models.
 */

export const globalSchemaMigrations = sqliteTable(
  'schema_migrations',
  {
    version: integer('version').primaryKey(),
    name: text('name').notNull(),
    checksum: text('checksum').notNull(),
    appliedAt: text('applied_at').notNull()
  },
  () => [check('schema_migrations_version_positive', sql`version > 0`)]
)

export const projects = sqliteTable(
  'projects',
  {
    id: text('id').primaryKey(),
    name: text('name').notNull(),
    summary: text('summary').notNull().default(''),
    goals: text('goals').notNull().default(''),
    focusMd: text('focus_md').notNull().default(''),
    status: text('status').notNull().default('active'),
    archivedAt: text('archived_at'),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull()
  },
  (table) => [
    check('projects_name_nonempty', sql`length(name) > 0`),
    check('projects_status_valid', sql`status IN ('active','archived')`),
    index('idx_projects_status').on(table.status),
    index('idx_projects_archived_at').on(table.archivedAt),
    index('idx_projects_created_at').on(table.createdAt),
    index('idx_projects_name').on(table.name)
  ]
)

export const papers = sqliteTable(
  'papers',
  {
    id: text('id').primaryKey(),
    zoteroLibraryId: text('zotero_library_id').notNull().default('0'),
    zoteroKey: text('zotero_key').notNull(),
    attachmentKey: text('attachment_key'),
    metadata: text('metadata').notNull().default('{}'),
    filePath: text('file_path'),
    fileHash: text('file_hash'),
    fileSize: integer('file_size'),
    fileState: text('file_state').notNull().default('ok'),
    sourceState: text('source_state').notNull().default('ok'),
    lastSyncedAt: text('last_synced_at'),
    importedAt: text('imported_at').notNull(),
    updatedAt: text('updated_at').notNull()
  },
  (table) => [
    check('papers_file_state_valid', sql`file_state IN ('ok','missing')`),
    check('papers_source_state_valid', sql`source_state IN ('ok','missing','unreadable')`),
    uniqueIndex('ux_papers_zotero_identity').on(table.zoteroLibraryId, table.zoteroKey),
    index('idx_papers_zotero_key').on(table.zoteroKey),
    index('idx_papers_imported_at').on(table.importedAt),
    index('idx_papers_file_hash').on(table.fileHash),
    index('idx_papers_file_missing').on(table.fileState).where(sql`file_state = 'missing'`)
  ]
)

export const providers = sqliteTable(
  'providers',
  {
    id: text('id').primaryKey(),
    name: text('name').notNull(),
    protocol: text('protocol').notNull(),
    baseUrl: text('base_url').notNull(),
    apiKeyRef: text('api_key_ref').notNull(),
    enabled: integer('enabled').notNull().default(1),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull(),
    lastTestedAt: text('last_tested_at'),
    lastTestOk: integer('last_test_ok'),
    lastTestLatencyMs: integer('last_test_latency_ms'),
    lastTestModelCount: integer('last_test_model_count'),
    lastTestError: text('last_test_error'),
    modelsFetchedAt: text('models_fetched_at'),
    modelsFetchError: text('models_fetch_error')
  },
  (table) => [
    check(
      'providers_protocol_valid',
      sql`protocol IN ('openai_chat','openai_responses','gemini','anthropic')`
    ),
    check('providers_name_nonempty', sql`length(name) > 0`),
    check('providers_enabled_boolean', sql`enabled IN (0,1)`),
    check('providers_last_test_ok_boolean', sql`last_test_ok IN (0,1)`),
    check('providers_last_test_latency_nonnegative', sql`last_test_latency_ms >= 0`),
    check('providers_last_test_model_count_nonnegative', sql`last_test_model_count >= 0`),
    index('idx_providers_enabled').on(table.enabled)
  ]
)

export const roleAssignments = sqliteTable(
  'role_assignments',
  {
    role: text('role').primaryKey(),
    providerId: text('provider_id')
      .notNull()
      .references(() => providers.id, { onDelete: 'restrict' }),
    modelId: text('model_id').notNull(),
    updatedAt: text('updated_at').notNull()
  },
  (table) => [index('idx_role_assignments_provider').on(table.providerId)]
)

export const settings = sqliteTable('settings', {
  key: text('key').primaryKey(),
  value: text('value').notNull(),
  updatedAt: text('updated_at').notNull()
})

export type ProjectRow = typeof projects.$inferSelect
export type NewProjectRow = typeof projects.$inferInsert
export type PaperRow = typeof papers.$inferSelect
export type ProviderRow = typeof providers.$inferSelect