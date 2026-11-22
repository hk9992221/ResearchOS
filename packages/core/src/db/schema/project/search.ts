import { sql } from 'drizzle-orm'
import { blob, check, index, integer, primaryKey, sqliteTable, text } from 'drizzle-orm/sqlite-core'

/** project.db 0110_ai_index_project: project document/chunk/FTS/vector storage. */
export const indexDocs = sqliteTable(
  'index_docs',
  {
    docId: text('doc_id').primaryKey(),
    projectId: text('project_id').notNull(),
    source: text('source').notNull(),
    entityType: text('entity_type').notNull(),
    entityId: text('entity_id').notNull(),
    title: text('title').notNull().default(''),
    lang: text('lang').notNull().default(''),
    contentHash: text('content_hash').notNull().default(''),
    updatedAt: text('updated_at').notNull(),
    schemaVersion: integer('schema_version').notNull().default(1),
    parserVersion: text('parser_version').notNull().default(''),
    embeddingModelId: text('embedding_model_id').notNull().default(''),
    embeddingDim: integer('embedding_dim').notNull().default(0),
    indexStatus: text('index_status').notNull().default(''),
    indexedAt: text('indexed_at'),
    error: text('error')
  },
  (table) => [
    index('idx_index_docs_entity').on(table.entityType, table.entityId),
    index('idx_index_docs_project').on(table.projectId)
  ]
)

export const indexChunks = sqliteTable(
  'index_chunks',
  {
    chunkId: text('chunk_id').primaryKey(),
    docId: text('doc_id')
      .notNull()
      .references(() => indexDocs.docId, { onDelete: 'cascade' }),
    ordinal: integer('ordinal').notNull(),
    text: text('text').notNull(),
    locatorJson: text('locator_json').notNull().default(''),
    contentHash: text('content_hash').notNull().default('')
  },
  (table) => [index('idx_index_chunks_doc').on(table.docId, table.ordinal)]
)

/** FTS5 virtual table (`chunk_id UNINDEXED`, `body`, trigram tokenizer). */
export const INDEX_CHUNKS_FTS = {
  name: 'index_chunks_fts',
  columns: ['chunk_id', 'body'],
  unindexed: ['chunk_id'],
  tokenize: 'trigram'
} as const

export const chunkEmbeddings = sqliteTable(
  'chunk_embeddings',
  {
    profileId: text('profile_id').notNull(),
    docId: text('doc_id').notNull(),
    chunkId: text('chunk_id').notNull(),
    dim: integer('dim').notNull(),
    vector: blob('vector', { mode: 'buffer' }).notNull()
  },
  (table) => [
    primaryKey({ columns: [table.profileId, table.chunkId] }),
    check('chunk_embeddings_dim_positive', sql`dim > 0`),
    index('idx_chunk_embeddings_chunk').on(table.chunkId),
    index('idx_chunk_embeddings_doc').on(table.docId)
  ]
)

export type IndexDocRow = typeof indexDocs.$inferSelect
export type IndexChunkRow = typeof indexChunks.$inferSelect