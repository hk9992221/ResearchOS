import type { Db } from '../db/open'

/**
 * Storage primitives of the unified index. Extraction,
 * chunking and orchestration live in `modules/search`; this layer only writes,
 * replaces, deletes and queries docs/chunks/vectors.
 *
 * Table shapes are fixed by the copied 0110 migrations:
 *   project: index_docs / index_chunks / index_chunks_fts / chunk_embeddings
 *   global:  paper_index_docs / paper_chunks / paper_fts / paper_chunk_vectors
 */
export type IndexPartition = 'project' | 'global'

export type IndexDb = Db

export interface IndexDocRow {
  projectId: string
  docId: string
  source: string
  entityType: string
  entityId: string
  title: string
  contentHash: string
}

/** `locatorJson` is opaque pass-through data (AC-08); the store never parses it. */
export interface IndexChunkRow {
  chunkId: string
  docId: string
  projectId: string
  ordinal: number
  text: string
  contentHash: string
  locatorJson: string
}

export interface ChunkVector {
  chunkId: string
  vector: readonly number[]
}

/** One retrieval hit; `score` is higher-is-better (keyword: -bm25, vector: cosine). */
export interface ChunkHit {
  chunkId: string
  docId: string
  projectId: string
  text: string
  score: number
  matchedTerms: string[]
  locatorJson: string
}

/** Restricts or excludes documents before top-k truncation. */
export interface IndexFilter {
  docIds?: readonly string[]
  excludeDocIds?: readonly string[]
}

export interface IndexTables {
  docs: string
  chunks: string
  fts: string
  vectors: string
  hasProject: boolean
}

export function tablesFor(partition: IndexPartition): IndexTables {
  if (partition === 'global') {
    return {
      docs: 'paper_index_docs',
      chunks: 'paper_chunks',
      fts: 'paper_fts',
      vectors: 'paper_chunk_vectors',
      hasProject: false
    }
  }
  return {
    docs: 'index_docs',
    chunks: 'index_chunks',
    fts: 'index_chunks_fts',
    vectors: 'chunk_embeddings',
    hasProject: true
  }
}

export function placeholders(count: number): string {
  return new Array(count).fill('?').join(',')
}

/** Validates the doc/chunk shape shared by both indexes. */
export function validateReplace(doc: IndexDocRow, chunks: readonly IndexChunkRow[]): void {
  if (doc.docId === '') throw new Error('index-store: docId must not be empty')
  const seen = new Set<string>()
  for (const chunk of chunks) {
    if (chunk.docId !== doc.docId) throw new Error('index-store: chunk does not belong to the document')
    if (chunk.chunkId === '' || seen.has(chunk.chunkId)) {
      throw new Error('index-store: duplicate chunk id in replaceDoc')
    }
    seen.add(chunk.chunkId)
  }
}