import type { Db } from '../db/open'
import {
  type ChunkHit,
  type ChunkVector,
  type IndexChunkRow,
  type IndexDocRow,
  type IndexFilter,
  type IndexPartition,
  placeholders,
  tablesFor,
  type IndexTables,
  validateReplace
} from './types'

function encodeVector(vector: readonly number[]): Buffer {
  const buffer = Buffer.allocUnsafe(vector.length * 4)
  for (let index = 0; index < vector.length; index += 1) {
    buffer.writeFloatLE(vector[index] ?? 0, index * 4)
  }
  return buffer
}

/**
 * Exact cosine search over the existing blob tables, isolated per embedding
 * profile. Uses `sqlite-vec`'s `vec_distance_cosine` when the extension is
 * loaded; without it vector recall is unavailable and the caller degrades to
 * keyword-only. No `vec0` table is created.
 */
export class VectorIndex {
  readonly partition: IndexPartition
  readonly vectorAvailable: boolean
  private readonly db: Db
  private readonly tables: IndexTables

  constructor(db: Db, partition: IndexPartition, options: { vectorAvailable?: boolean } = {}) {
    this.db = db
    this.partition = partition
    this.tables = tablesFor(partition)
    this.vectorAvailable = options.vectorAvailable ?? true
  }

  /**
   * Writes every vector of one document under `profileId`; other profiles are
   * untouched. The whole batch is validated before any write, so a successful
   * call stores every vector or none.
   */
  replaceDoc(
    profileId: string,
    doc: IndexDocRow,
    chunks: readonly IndexChunkRow[],
    vectors: readonly ChunkVector[]
  ): void {
    if (profileId === '') throw new Error('index-store: profile id must not be empty')
    validateReplace(doc, chunks)
    if (vectors.length !== chunks.length) {
      throw new Error('index-store: vectors must cover exactly the document chunks')
    }
    const chunkIds = new Set(chunks.map((chunk) => chunk.chunkId))
    const byChunk = new Map<string, readonly number[]>()
    let dim = -1
    for (const entry of vectors) {
      if (!chunkIds.has(entry.chunkId)) {
        throw new Error('index-store: vector does not belong to the document chunks')
      }
      if (byChunk.has(entry.chunkId)) throw new Error('index-store: duplicate vector chunk id')
      if (entry.vector.length === 0) throw new Error('index-store: vector dimensions are inconsistent')
      if (dim < 0) dim = entry.vector.length
      else if (entry.vector.length !== dim) {
        throw new Error('index-store: vector dimensions are inconsistent')
      }
      byChunk.set(entry.chunkId, entry.vector)
    }
    if (chunks.length > 0) {
      const storedDim = this.db
        .prepare(`SELECT dim FROM ${this.tables.vectors} WHERE profile_id = ? LIMIT 1`)
        .pluck()
        .get(profileId) as number | undefined
      if (storedDim !== undefined && storedDim !== dim) {
        throw new Error(`index-store: profile has dim ${storedDim}, batch has dim ${dim}`)
      }
    }

    const run = this.db.transaction(() => {
      this.ensureStored(doc, chunks)
      this.db
        .prepare(`DELETE FROM ${this.tables.vectors} WHERE profile_id = ? AND doc_id = ?`)
        .run(profileId, doc.docId)
      const insert = this.db.prepare(
        `INSERT INTO ${this.tables.vectors} (profile_id, doc_id, chunk_id, dim, vector) VALUES (?, ?, ?, ?, ?)`
      )
      for (const chunk of chunks) {
        const vector = byChunk.get(chunk.chunkId)
        if (vector === undefined) throw new Error('index-store: vectors must cover exactly the document chunks')
        insert.run(profileId, doc.docId, chunk.chunkId, dim, encodeVector(vector))
      }
    })
    run.immediate()
  }

  /** Removes every profile's vectors for the document. Idempotent. */
  deleteDoc(docId: string): void {
    if (docId === '') throw new Error('index-store: docId must not be empty')
    this.db.prepare(`DELETE FROM ${this.tables.vectors} WHERE doc_id = ?`).run(docId)
  }

  /**
   * The document and exactly its chunk rows must already exist: keyword writes
   * own the doc/chunk projection and always run first.
   */
  private ensureStored(doc: IndexDocRow, chunks: readonly IndexChunkRow[]): void {
    if (this.tables.hasProject) {
      const exists = this.db
        .prepare(`SELECT 1 AS one FROM ${this.tables.docs} WHERE doc_id = ? AND project_id = ?`)
        .get(doc.docId, doc.projectId)
      if (exists === undefined) {
        throw new Error(`index-store: document ${doc.docId} is not keyword-indexed yet`)
      }
    } else {
      const exists = this.db.prepare(`SELECT 1 AS one FROM ${this.tables.docs} WHERE doc_id = ?`).get(doc.docId)
      if (exists === undefined) {
        throw new Error(`index-store: document ${doc.docId} is not keyword-indexed yet`)
      }
    }
    const stored = new Set(
      this.db
        .prepare(`SELECT chunk_id FROM ${this.tables.chunks} WHERE doc_id = ?`)
        .pluck()
        .all(doc.docId) as string[]
    )
    for (const chunk of chunks) {
      if (!stored.delete(chunk.chunkId)) {
        throw new Error(`index-store: chunk ${chunk.chunkId} is not stored for document ${doc.docId}`)
      }
    }
    if (stored.size > 0) {
      throw new Error(`index-store: vectors cover only part of document ${doc.docId} chunks`)
    }
  }

  /** Searches exactly one profile; a mismatched stored dim is excluded. */
  query(
    profileId: string,
    projectId: string,
    vector: readonly number[],
    topK: number,
    filter: IndexFilter = {}
  ): ChunkHit[] {
    if (profileId === '') throw new Error('index-store: profile id must not be empty')
    if (!this.vectorAvailable || vector.length === 0) return []
    const tables = this.tables
    const limit = topK > 0 ? topK : 50
    const projectSelect = tables.hasProject ? 'd.project_id' : "''"
    const projectCondition = tables.hasProject ? ' AND d.project_id = ?' : ''
    let filterSql = ''
    if (filter.docIds !== undefined && filter.docIds.length > 0) {
      filterSql += ` AND d.doc_id IN (${placeholders(filter.docIds.length)})`
    }
    if (filter.excludeDocIds !== undefined && filter.excludeDocIds.length > 0) {
      filterSql += ` AND d.doc_id NOT IN (${placeholders(filter.excludeDocIds.length)})`
    }
    const sql = `SELECT c.chunk_id, c.doc_id, ${projectSelect} AS project_id, c.text, c.locator_json,
                        vec_distance_cosine(v.vector, ?) AS distance
                 FROM ${tables.vectors} v
                 JOIN ${tables.chunks} c ON c.chunk_id = v.chunk_id
                 JOIN ${tables.docs} d ON d.doc_id = c.doc_id
                 WHERE v.profile_id = ? AND v.dim = ?${projectCondition}${filterSql}
                 ORDER BY distance ASC, c.doc_id, c.chunk_id
                 LIMIT ?`
    const args: Array<string | number | Buffer> = [encodeVector(vector), profileId, vector.length]
    if (tables.hasProject) args.push(projectId)
    args.push(...(filter.docIds ?? []), ...(filter.excludeDocIds ?? []), limit)
    const rows = this.db.prepare(sql).all(...args) as Array<{
      chunk_id: string
      doc_id: string
      project_id: string
      text: string
      locator_json: string
      distance: number
    }>
    return rows.map((row) => ({
      chunkId: row.chunk_id,
      docId: row.doc_id,
      projectId: row.project_id,
      text: row.text,
      score: 1 - row.distance,
      matchedTerms: [],
      locatorJson: row.locator_json ?? ''
    }))
  }
}
