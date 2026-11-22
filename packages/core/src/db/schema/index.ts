import type { SQLiteTable } from 'drizzle-orm/sqlite-core'

import type { MigrationKind } from '../bands'

export * from './global/core'
export * from './global/ai'
export * from './global/search'
export * from './global/search-state'
export * from './global/mcp'
export * from './global/plugins'

export * from './project/core'
export * from './project/kanban'
export * from './project/ai'
export * from './project/search'
export * from './project/search-state'
export * from './project/views'
export * from './project/annotations'

import { globalSchemaMigrations, papers, projects, providers, roleAssignments, settings } from './global/core'
import { providerModels } from './global/ai'
import { paperChunkVectors, paperChunks, paperIndexDocs, PAPER_FTS } from './global/search'
import { paperIndexDirty, paperIndexJobs, paperIndexSources, paperParseState } from './global/search-state'
import { mcpClient, mcpConfirmation, mcpIdempotency, mcpJob } from './global/mcp'
import { pluginKv, plugins, pluginRegistries } from './global/plugins'

import {
  activities,
  dbFields,
  dbRecords,
  entityRelations,
  entityTags,
  evidence,
  experiments,
  externalRefs,
  ideas,
  notes,
  projectPapers,
  projectSchemaMigrations,
  projectSettings,
  researchDatabases,
  runs,
  snapshots,
  tags,
  tasks,
  weeklyReports
} from './project/core'
import { taskChecklistItems, taskColumns } from './project/kanban'
import { aiOutputs } from './project/ai'
import { chunkEmbeddings, indexChunks, indexDocs, INDEX_CHUNKS_FTS } from './project/search'
import { indexDirty, indexJobs, indexSources } from './project/search-state'
import { vTrash } from './project/views'
import { paperAnnotations } from './project/annotations'

/** Every regular table in global.db, in migration order. */
export const GLOBAL_TABLES: readonly SQLiteTable[] = [
  globalSchemaMigrations,
  projects,
  papers,
  providers,
  roleAssignments,
  settings,
  providerModels,
  paperIndexDocs,
  paperChunks,
  paperChunkVectors,
  paperIndexSources,
  paperParseState,
  paperIndexDirty,
  paperIndexJobs,
  mcpConfirmation,
  mcpJob,
  mcpClient,
  mcpIdempotency,
  plugins,
  pluginRegistries,
  pluginKv
]

/** Every regular table in project.db, in migration order. */
export const PROJECT_TABLES: readonly SQLiteTable[] = [
  projectSchemaMigrations,
  notes,
  researchDatabases,
  dbFields,
  dbRecords,
  ideas,
  evidence,
  experiments,
  runs,
  snapshots,
  tasks,
  weeklyReports,
  activities,
  entityRelations,
  externalRefs,
  projectPapers,
  projectSettings,
  taskColumns,
  taskChecklistItems,
  aiOutputs,
  indexDocs,
  indexChunks,
  chunkEmbeddings,
  indexSources,
  indexDirty,
  indexJobs,
  tags,
  entityTags,
  paperAnnotations
]

/** FTS5 virtual tables have no Drizzle builder; this is their checked shape. */
export interface VirtualTableShape {
  readonly kind: MigrationKind
  readonly name: string
  readonly columns: readonly string[]
  readonly unindexed: readonly string[]
  readonly tokenize: string
}

export const VIRTUAL_TABLES: readonly VirtualTableShape[] = [
  { kind: 'global', ...PAPER_FTS },
  { kind: 'project', ...INDEX_CHUNKS_FTS }
]

export const VIEWS = [
  { kind: 'project' as const, name: 'v_trash', columns: ['entity_type', 'id', 'title', 'deleted_at'], table: vTrash }
]

export const TABLES_BY_KIND: Readonly<Record<MigrationKind, readonly SQLiteTable[]>> = {
  global: GLOBAL_TABLES,
  project: PROJECT_TABLES
}