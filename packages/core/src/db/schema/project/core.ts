import { sql } from 'drizzle-orm'
import { check, index, integer, primaryKey, sqliteTable, text, uniqueIndex } from 'drizzle-orm/sqlite-core'

/**
 * project.db migrations 0001_core plus the module additions that only add
 * columns/indexes to these tables (0250, 0260, 0270, 0280, 0310, 0400).
 */

export const projectSchemaMigrations = sqliteTable(
  'schema_migrations',
  {
    version: integer('version').primaryKey(),
    name: text('name').notNull(),
    checksum: text('checksum').notNull(),
    appliedAt: text('applied_at').notNull()
  },
  () => [check('schema_migrations_version_positive', sql`version > 0`)]
)

export const notes = sqliteTable(
  'notes',
  {
    id: text('id').primaryKey(),
    title: text('title').notNull().default(''),
    filePath: text('file_path').notNull(),
    kind: text('kind').notNull(),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull(),
    deletedAt: text('deleted_at'),
    fileHash: text('file_hash'),
    fileMtime: text('file_mtime'),
    fileState: text('file_state').notNull().default('ok'),
    paperId: text('paper_id'),
    metaJson: text('meta_json').notNull().default(''),
    description: text('description').notNull().default('')
  },
  (table) => [
    check('notes_kind_valid', sql`kind IN ('scratchpad','paper_note','db_record','overview')`),
    check('notes_file_state_valid', sql`file_state IN ('ok','missing')`),
    uniqueIndex('ux_notes_file_path').on(table.filePath),
    index('idx_notes_kind_deleted').on(table.kind, table.deletedAt),
    index('idx_notes_deleted_at').on(table.deletedAt),
    index('idx_notes_updated_at').on(table.updatedAt),
    index('idx_notes_file_missing').on(table.fileState).where(sql`file_state = 'missing'`),
    index('idx_notes_paper').on(table.paperId).where(sql`paper_id IS NOT NULL`),
    uniqueIndex('ux_notes_paper_note_live')
      .on(table.paperId)
      .where(sql`kind = 'paper_note' AND deleted_at IS NULL AND paper_id IS NOT NULL`),
    uniqueIndex('ux_notes_overview_live')
      .on(table.kind)
      .where(sql`kind = 'overview' AND deleted_at IS NULL`)
  ]
)

export const researchDatabases = sqliteTable(
  'research_databases',
  {
    id: text('id').primaryKey(),
    name: text('name').notNull(),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull(),
    deletedAt: text('deleted_at'),
    description: text('description').notNull().default('')
  },
  (table) => [
    check('research_databases_name_nonempty', sql`length(name) > 0`),
    index('idx_research_databases_deleted').on(table.deletedAt)
  ]
)

export const dbFields = sqliteTable(
  'db_fields',
  {
    id: text('id').primaryKey(),
    databaseId: text('database_id')
      .notNull()
      .references(() => researchDatabases.id, { onDelete: 'cascade' }),
    name: text('name').notNull(),
    type: text('type').notNull(),
    config: text('config').notNull().default('{}'),
    sortOrder: integer('sort_order').notNull().default(0),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull(),
    deletedAt: text('deleted_at'),
    role: text('role').notNull().default('plain')
  },
  (table) => [
    check('db_fields_name_nonempty', sql`length(name) > 0`),
    check('db_fields_role_valid', sql`role IN ('title','plain')`),
    index('idx_db_fields_database').on(table.databaseId, table.sortOrder),
    uniqueIndex('ux_db_fields_title_role')
      .on(table.databaseId)
      .where(sql`role = 'title' AND deleted_at IS NULL`),
    uniqueIndex('ux_db_fields_name')
      .on(table.databaseId, sql`lower(name)`)
      .where(sql`deleted_at IS NULL`)
  ]
)

export const dbRecords = sqliteTable(
  'db_records',
  {
    id: text('id').primaryKey(),
    databaseId: text('database_id')
      .notNull()
      .references(() => researchDatabases.id, { onDelete: 'cascade' }),
    noteId: text('note_id').references(() => notes.id, { onDelete: 'set null' }),
    title: text('title').notNull().default(''),
    data: text('data').notNull().default('{}'),
    sortOrder: integer('sort_order').notNull().default(0),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull(),
    deletedAt: text('deleted_at')
  },
  (table) => [
    index('idx_db_records_database').on(table.databaseId, table.sortOrder),
    index('idx_db_records_deleted').on(table.deletedAt),
    index('idx_db_records_note').on(table.noteId),
    uniqueIndex('ux_db_records_note_live')
      .on(table.noteId)
      .where(sql`note_id IS NOT NULL AND deleted_at IS NULL`)
  ]
)

export const ideas = sqliteTable(
  'ideas',
  {
    id: text('id').primaryKey(),
    title: text('title').notNull(),
    description: text('description').notNull().default(''),
    status: text('status').notNull().default('New'),
    rejectionReason: text('rejection_reason').notNull().default(''),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull(),
    deletedAt: text('deleted_at')
  },
  (table) => [
    check('ideas_title_nonempty', sql`length(title) > 0`),
    check(
      'ideas_status_valid',
      sql`status IN ('New','Researching','Validating','Supported','PartiallySupported','Inconclusive','Rejected','OnHold')`
    ),
    index('idx_ideas_status').on(table.status, table.deletedAt),
    index('idx_ideas_deleted').on(table.deletedAt),
    index('idx_ideas_updated').on(table.updatedAt),
    index('idx_ideas_live_status').on(table.status, table.updatedAt).where(sql`deleted_at IS NULL`),
    index('idx_ideas_live_updated').on(table.updatedAt).where(sql`deleted_at IS NULL`),
    index('idx_ideas_trash').on(table.deletedAt).where(sql`deleted_at IS NOT NULL`)
  ]
)

export const evidence = sqliteTable(
  'evidence',
  {
    id: text('id').primaryKey(),
    ideaId: text('idea_id')
      .notNull()
      .references(() => ideas.id, { onDelete: 'cascade' }),
    direction: text('direction').notNull(),
    content: text('content').notNull().default(''),
    sourceType: text('source_type').notNull(),
    sourceId: text('source_id').notNull(),
    aiGenerated: integer('ai_generated').notNull().default(0),
    confirmed: integer('confirmed').notNull().default(0),
    aiOutputId: text('ai_output_id'),
    provenance: text('provenance').notNull().default('{}'),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull(),
    deletedAt: text('deleted_at'),
    contentHash: text('content_hash').notNull().default('')
  },
  (table) => [
    check('evidence_direction_valid', sql`direction IN ('support','oppose')`),
    check('evidence_ai_generated_boolean', sql`ai_generated IN (0,1)`),
    check('evidence_confirmed_boolean', sql`confirmed IN (0,1)`),
    index('idx_evidence_idea').on(table.ideaId, table.direction, table.deletedAt),
    index('idx_evidence_source').on(table.sourceType, table.sourceId),
    index('idx_evidence_ai_output').on(table.aiOutputId).where(sql`ai_output_id IS NOT NULL`),
    index('idx_evidence_live_idea')
      .on(table.ideaId, table.direction, table.confirmed)
      .where(sql`deleted_at IS NULL`),
    index('idx_evidence_live_source')
      .on(table.sourceType, table.sourceId)
      .where(sql`deleted_at IS NULL`),
    index('idx_evidence_dedupe')
      .on(table.ideaId, table.sourceType, table.sourceId, table.contentHash)
      .where(sql`deleted_at IS NULL`)
  ]
)

export const experiments = sqliteTable(
  'experiments',
  {
    id: text('id').primaryKey(),
    title: text('title').notNull(),
    purpose: text('purpose').notNull().default(''),
    dirRef: text('dir_ref'),
    scanRules: text('scan_rules').notNull().default('{}'),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull(),
    deletedAt: text('deleted_at')
  },
  (table) => [
    check('experiments_title_nonempty', sql`length(title) > 0`),
    index('idx_experiments_deleted').on(table.deletedAt),
    index('idx_experiments_title').on(table.title)
  ]
)

export const runs = sqliteTable(
  'runs',
  {
    id: text('id').primaryKey(),
    experimentId: text('experiment_id')
      .notNull()
      .references(() => experiments.id, { onDelete: 'cascade' }),
    dirRef: text('dir_ref'),
    params: text('params').notNull().default('{}'),
    gitInfo: text('git_info').notNull().default('{}'),
    status: text('status').notNull().default('unknown'),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull(),
    deletedAt: text('deleted_at'),
    title: text('title').notNull().default('')
  },
  (table) => [
    index('idx_runs_experiment').on(table.experimentId, table.deletedAt),
    index('idx_runs_status').on(table.status, table.deletedAt)
  ]
)

export const snapshots = sqliteTable(
  'snapshots',
  {
    id: text('id').primaryKey(),
    targetType: text('target_type').notNull(),
    targetId: text('target_id').notNull(),
    fileManifest: text('file_manifest').notNull().default('[]'),
    gitInfo: text('git_info').notNull().default('{}'),
    metrics: text('metrics').notNull().default('{}'),
    aiAnalysis: text('ai_analysis'),
    aiOutputId: text('ai_output_id'),
    modelInfo: text('model_info').notNull().default('{}'),
    createdAt: text('created_at').notNull()
  },
  (table) => [
    check('snapshots_target_type_valid', sql`target_type IN ('experiment','run')`),
    index('idx_snapshots_target').on(table.targetType, table.targetId, table.createdAt),
    index('idx_snapshots_ai_output').on(table.aiOutputId).where(sql`ai_output_id IS NOT NULL`)
  ]
)

export const tasks = sqliteTable(
  'tasks',
  {
    id: text('id').primaryKey(),
    title: text('title').notNull(),
    column: text('column').notNull(),
    priority: text('priority'),
    deadline: text('deadline'),
    blocked: integer('blocked').notNull().default(0),
    labels: text('labels').notNull().default('[]'),
    weekPlan: text('week_plan'),
    sortOrder: integer('sort_order').notNull().default(0),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull(),
    deletedAt: text('deleted_at'),
    parentTaskId: text('parent_task_id'),
    descriptionMd: text('description_md').notNull().default(''),
    completedAt: text('completed_at'),
    archivedAt: text('archived_at')
  },
  (table) => [
    check('tasks_title_nonempty', sql`length(title) > 0`),
    check('tasks_blocked_boolean', sql`blocked IN (0,1)`),
    index('idx_tasks_column').on(table.column, table.sortOrder),
    index('idx_tasks_week').on(table.weekPlan, table.deletedAt),
    index('idx_tasks_blocked').on(table.blocked, table.deletedAt),
    index('idx_tasks_deadline').on(table.deadline),
    index('idx_tasks_deleted').on(table.deletedAt),
    index('idx_tasks_parent').on(table.parentTaskId).where(sql`parent_task_id IS NOT NULL`),
    index('idx_tasks_archived').on(table.archivedAt).where(sql`archived_at IS NOT NULL`)
  ]
)

export const weeklyReports = sqliteTable(
  'weekly_reports',
  {
    id: text('id').primaryKey(),
    periodStart: text('period_start').notNull(),
    periodEnd: text('period_end').notNull(),
    contentMd: text('content_md').notNull().default(''),
    status: text('status').notNull().default('draft'),
    meta: text('meta').notNull().default('{}'),
    confirmedAt: text('confirmed_at'),
    aiOutputId: text('ai_output_id'),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull(),
    deletedAt: text('deleted_at')
  },
  (table) => [
    check('weekly_reports_status_valid', sql`status IN ('draft','confirmed')`),
    index('idx_weekly_reports_period').on(table.periodStart, table.periodEnd),
    index('idx_weekly_reports_status').on(table.status, table.deletedAt),
    index('idx_weekly_reports_ai_output').on(table.aiOutputId).where(sql`ai_output_id IS NOT NULL`)
  ]
)

export const activities = sqliteTable(
  'activities',
  {
    id: text('id').primaryKey(),
    eventType: text('event_type').notNull(),
    entityType: text('entity_type').notNull(),
    entityId: text('entity_id').notNull(),
    payload: text('payload').notNull(),
    actor: text('actor').notNull(),
    mergeKey: text('merge_key'),
    createdAt: text('created_at').notNull()
  },
  (table) => [
    check(
      'activities_event_type_valid',
      sql`event_type IN ('entity.created','entity.deleted','entity.restored','status.changed','relation.added','relation.removed','note.updated','ai.analysis_completed','paper.imported','paper.overwrite_confirmed','report.generated','project.exported','entity.updated','entity.purged','entity.relocated','paper.metadata_updated')`
    ),
    check('activities_actor_valid', sql`actor IN ('user','mcp','ai')`),
    index('idx_activities_created').on(table.createdAt),
    index('idx_activities_entity').on(table.entityType, table.entityId, table.createdAt),
    index('idx_activities_type').on(table.eventType, table.createdAt),
    index('idx_activities_actor').on(table.actor, table.createdAt),
    uniqueIndex('ux_activities_merge_key').on(table.mergeKey).where(sql`merge_key IS NOT NULL`)
  ]
)

export const entityRelations = sqliteTable(
  'entity_relations',
  {
    id: text('id').primaryKey(),
    srcType: text('src_type').notNull(),
    srcId: text('src_id').notNull(),
    relation: text('relation').notNull(),
    dstType: text('dst_type').notNull(),
    dstId: text('dst_id').notNull(),
    createdAt: text('created_at').notNull()
  },
  (table) => [
    check(
      'entity_relations_relation_valid',
      sql`relation IN ('references','derived_from','validates','deliverable_of','related_to')`
    ),
    uniqueIndex('ux_entity_relations_tuple').on(
      table.srcType,
      table.srcId,
      table.relation,
      table.dstType,
      table.dstId
    ),
    index('idx_entity_relations_src').on(table.srcType, table.srcId),
    index('idx_entity_relations_dst').on(table.dstType, table.dstId),
    index('idx_entity_relations_relation').on(table.relation)
  ]
)

export const externalRefs = sqliteTable(
  'external_refs',
  {
    id: text('id').primaryKey(),
    ownerType: text('owner_type').notNull(),
    ownerId: text('owner_id').notNull(),
    field: text('field').notNull(),
    refKind: text('ref_kind').notNull(),
    path: text('path').notNull(),
    status: text('status').notNull().default('ok'),
    lastCheckedAt: text('last_checked_at'),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull()
  },
  (table) => [
    check('external_refs_kind_valid', sql`ref_kind IN ('directory','file','git_repository')`),
    check('external_refs_status_valid', sql`status IN ('ok','broken')`),
    uniqueIndex('ux_external_refs_owner').on(table.ownerType, table.ownerId, table.field),
    index('idx_external_refs_status').on(table.status)
  ]
)

export const projectPapers = sqliteTable(
  'project_papers',
  {
    projectId: text('project_id').notNull(),
    paperId: text('paper_id').notNull(),
    linkedAt: text('linked_at').notNull(),
    linkedBy: text('linked_by').notNull()
  },
  (table) => [
    primaryKey({ columns: [table.projectId, table.paperId] }),
    check('project_papers_linked_by_valid', sql`linked_by IN ('user','mcp')`),
    index('idx_project_papers_paper').on(table.paperId)
  ]
)

export const projectSettings = sqliteTable('project_settings', {
  key: text('key').primaryKey(),
  value: text('value').notNull(),
  updatedAt: text('updated_at').notNull()
})

export const tags = sqliteTable(
  'tags',
  {
    id: text('id').primaryKey(),
    name: text('name').notNull(),
    color: text('color').notNull().default(''),
    createdAt: text('created_at').notNull(),
    updatedAt: text('updated_at').notNull(),
    deletedAt: text('deleted_at')
  },
  (table) => [
    check('tags_name_nonempty', sql`length(name) > 0`),
    uniqueIndex('ux_tags_name')
      .on(sql`lower(name)`)
      .where(sql`deleted_at IS NULL`),
    index('idx_tags_deleted').on(table.deletedAt)
  ]
)

export const entityTags = sqliteTable(
  'entity_tags',
  {
    tagId: text('tag_id')
      .notNull()
      .references(() => tags.id, { onDelete: 'cascade' }),
    entityType: text('entity_type').notNull(),
    entityId: text('entity_id').notNull(),
    createdAt: text('created_at').notNull()
  },
  (table) => [
    primaryKey({ columns: [table.tagId, table.entityType, table.entityId] }),
    index('idx_entity_tags_entity').on(table.entityType, table.entityId)
  ]
)

export type NoteRow = typeof notes.$inferSelect
export type IdeaRow = typeof ideas.$inferSelect
export type TaskRow = typeof tasks.$inferSelect