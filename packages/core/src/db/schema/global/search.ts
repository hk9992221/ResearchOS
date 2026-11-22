import { sql } from 'drizzle-orm'
import { blob, check, index, integer, primaryKey, sqliteTable, text } from 'drizzle-orm/sqlite-core'

/** global.db 0110_ai_index_paper: paper document/chunk/FTS/vector storage. */
export const paperIndexDocs = sqliteTable(
  'paper_index_docs',
  {
    docId: text('doc_id').primaryKey(),
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
  (table) => [index('idx_paper_index_docs_entity').on(table.entityType, table.entityId)]
)

export const paperChunks = sqliteTable(
  'paper_chunks',
  {
    chunkId: text('chunk_id').primaryKey(),
    docId: text('doc_id')
      .notNull()
      .references(() => paperIndexDocs.docId, { onDelete: 'cascade' }),
    ordinal: integer('ordinal').notNull(),
    text: text('text').notNull(),
    locatorJson: text('locator_json').notNull().default(''),
    contentHash: text('content_hash').notNull().default('')
  },
  (table) => [index('idx_paper_chunks_doc').on(table.docId, table.ordinal)]
)

/**
 * FTS5 virtual table (`chunk_id UNINDEXED`, `body`, trigram tokenizer). Drizzle
 * has no virtual-table builder, so the shape lives here as data and the parity
 * script checks it against sqlite_master. Queries use raw SQL.
 */
export const PAPER_FTS = {
  name: 'paper_fts',
  columns: ['chunk_id', 'body'],
  unindexed: ['chunk_id'],
  tokenize: 'trigram'
} as const

export const paperChunkVectors = sqliteTable(
  'paper_chunk_vectors',
  {
    profileId: text('profile_id').notNull(),
    docId: text('doc_id').notNull(),
    chunkId: text('chunk_id').notNull(),
    dim: integer('dim').notNull(),
    vector: blob('vector', { mode: 'buffer' }).notNull()
  },
  (table) => [
    primaryKey({ columns: [table.profileId, table.chunkId] }),
    check('paper_chunk_vectors_dim_positive', sql`dim > 0`),
    index('idx_paper_chunk_vectors_chunk').on(table.chunkId),
    index('idx_paper_chunk_vectors_doc').on(table.docId)
  ]
)

export type PaperIndexDocRow = typeof paperIndexDocs.$inferSelect
export type PaperChunkRow = typeof paperChunks.$inferSelect