/**
 * Keyword and vector storage primitives consumed by `modules/search`
 * Extraction, chunking and orchestration deliberately live
 * outside this folder.
 */
export { KeywordIndex, ftsTokens } from './keyword'
export { VectorIndex } from './vector'
export { tablesFor, type ChunkHit, type ChunkVector, type IndexChunkRow, type IndexDocRow, type IndexFilter, type IndexPartition, type IndexTables } from './types'
