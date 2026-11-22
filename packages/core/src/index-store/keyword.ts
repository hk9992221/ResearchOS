import { formatTimestamp } from '../clock'
import type { Db } from '../db/open'
import {
  type ChunkHit,
  type IndexChunkRow,
  type IndexDocRow,
  type IndexFilter,
  type IndexPartition,
  placeholders,
  tablesFor,
  type IndexTables,
  validateReplace
} from './types'

/** Splits on non-alphanumerics and keeps terms long enough for trigram FTS. */
export function ftsTokens(text: string): string[] {
  const fields = text.split(/[^\p{L}\p{N}]+/u)
  const seen = new Set<string>()
  const out: string[] = []
  for (const field of fields) {
    if ([...field].length < 3) continue
    const lower = field.toLowerCase()
    if (seen.has(lower)) continue
    seen.add(lower)
    out.push(lower)
  }
  return out
}

function filterSql(filter: IndexFilter): string {
  let sql = ''
  if (filter.docIds !== undefined && filter.docIds.length > 0) {
    sql += ` AND d.doc_id IN (${placeholders(filter.docIds.length)})`
  }
  if (filter.excludeDocIds !== undefined && filter.excludeDocIds.length > 0) {
    sql += ` AND d.doc_id NOT IN (${placeholders(filter.excludeDocIds.length)})`
  }
  return sql
}

function filterArgs(filter: IndexFilter): string[] {
  return [...(filter.docIds ?? []), ...(filter.excludeDocIds ?? [])]
}

/**
 * FTS5 keyword index with the trigram tokenizer (O-02). Queries whose every
 * term is shorter than three runes fall back to a LIKE substring scan, so
 * two-character CJK terms still retrieve.
 */
export class KeywordIndex {
  readonly partition: IndexPartition
  private readonly db: Db
  private readonly tables: IndexTables

  constructor(db: Db, partition: IndexPartition) {
    this.db = db
    this.partition = partition
    this.tables = tablesFor(partition)
  }

  /**
   * Atomically replaces the doc row, its chunks and FTS rows. The doc row is
   * upserted, never deleted, so metadata columns survive a keyword
   * re-projection. Vectors under every profile are untouched.
   */
  replaceDoc(doc: IndexDocRow, chunks: readonly IndexChunkRow[]): void {
    validateReplace(doc, chunks)
    const tables = this.tables
    const now = formatTimestamp(Date.now())
    const run = this.db.transaction(() => {
      if (tables.hasProject) {
        this.db
          .prepare(
            `INSERT INTO ${tables.docs}
               (doc_id, project_id, source, entity_type, entity_id, title, content_hash, updated_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?)
             ON CONFLICT(doc_id) DO UPDATE SET
               project_id = excluded.project_id,
               source = excluded.source,
               entity_type = excluded.entity_type,
               entity_id = excluded.entity_id,
               title = excluded.title,
               content_hash = excluded.content_hash,
               updated_at = excluded.updated_at`
          )
          .run(doc.docId, doc.projectId, doc.source, doc.entityType, doc.entityId, doc.title, doc.contentHash, now)
      } else {
        this.db
          .prepare(
            `INSERT INTO ${tables.docs}
               (doc_id, source, entity_type, entity_id, title, content_hash, updated_at)
             VALUES (?, ?, ?, ?, ?, ?, ?)
             ON CONFLICT(doc_id) DO UPDATE SET
               source = excluded.source,
               entity_type = excluded.entity_type,
               entity_id = excluded.entity_id,
               title = excluded.title,
               content_hash = excluded.content_hash,
               updated_at = excluded.updated_at`
          )
          .run(doc.docId, doc.source, doc.entityType, doc.entityId, doc.title, doc.contentHash, now)
      }
      this.purgeChunks(doc.docId)
      const insertChunk = this.db.prepare(
        `INSERT INTO ${tables.chunks} (chunk_id, doc_id, ordinal, text, locator_json, content_hash)
         VALUES (?, ?, ?, ?, ?, ?)`
      )
      const chunkRowid = this.db.prepare(`SELECT rowid FROM ${tables.chunks} WHERE chunk_id = ?`)
      const insertFts = this.db.prepare(`INSERT INTO ${tables.fts} (rowid, chunk_id, body) VALUES (?, ?, ?)`)
      for (const chunk of chunks) {
        insertChunk.run(
          chunk.chunkId,
          chunk.docId,
          chunk.ordinal,
          chunk.text,
          chunk.locatorJson ?? '',
          chunk.contentHash
        )
        const row = chunkRowid.get(chunk.chunkId) as { rowid: number }
        insertFts.run(row.rowid, chunk.chunkId, chunk.text)
      }
    })
    run.immediate()
  }

  /** Removes the doc, its chunks and FTS rows; vectors cascade. Idempotent. */
  deleteDoc(projectId: string, docId: string): void {
    if (docId === '') throw new Error('index-store: docId must not be empty')
    const tables = this.tables
    if (tables.hasProject) {
      const exists = this.db
        .prepare(`SELECT doc_id FROM ${tables.docs} WHERE doc_id = ? AND project_id = ?`)
        .get(docId, projectId) as { doc_id: string } | undefined
      if (exists === undefined) return
    }
    const run = this.db.transaction(() => {
      this.purgeChunks(docId)
      this.db.prepare(`DELETE FROM ${tables.docs} WHERE doc_id = ?`).run(docId)
    })
    run.immediate()
  }

  private purgeChunks(docId: string): void {
    const tables = this.tables
    this.db
      .prepare(
        `DELETE FROM ${tables.fts}
         WHERE chunk_id IN (SELECT chunk_id FROM ${tables.chunks} WHERE doc_id = ?)`
      )
      .run(docId)
    this.db.prepare(`DELETE FROM ${tables.chunks} WHERE doc_id = ?`).run(docId)
  }

  /**
   * Returns hits for one partition only. `text` is the retrieval text; quoted
   * phrases are enforced by the caller, because trigram FTS is OR-based here.
   */
  query(projectId: string, text: string, topK: number, filter: IndexFilter = {}): ChunkHit[] {
    const trimmed = text.trim()
    if (trimmed === '') return []
    const limit = topK > 0 ? topK : 50
    const tokens = ftsTokens(trimmed)
    return tokens.length > 0
      ? this.queryFts(projectId, tokens, limit, filter)
      : this.queryLike(projectId, trimmed, limit, filter)
  }

  private queryFts(projectId: string, tokens: readonly string[], topK: number, filter: IndexFilter): ChunkHit[] {
    const tables = this.tables
    const match = tokens.map((token) => `"${token.replaceAll('"', '""')}"`).join(' OR ')
    const projectSelect = tables.hasProject ? 'd.project_id' : "''"
    const projectCondition = tables.hasProject ? ' AND d.project_id = ?' : ''
    const sql = `SELECT c.chunk_id, c.doc_id, ${projectSelect} AS project_id, c.text,
                        c.locator_json, -bm25(${tables.fts}) AS score
                 FROM ${tables.fts} f
                 JOIN ${tables.chunks} c ON c.rowid = f.rowid
                 JOIN ${tables.docs} d ON d.doc_id = c.doc_id
                 WHERE ${tables.fts} MATCH ?${projectCondition}${filterSql(filter)}
                 ORDER BY bm25(${tables.fts}), c.doc_id, c.ordinal
                 LIMIT ?`
    const args: Array<string | number> = [match]
    if (tables.hasProject) args.push(projectId)
    args.push(...filterArgs(filter), topK)
    return this.collect(sql, args, tokens)
  }

  private queryLike(projectId: string, text: string, topK: number, filter: IndexFilter): ChunkHit[] {
    const tables = this.tables
    const projectSelect = tables.hasProject ? 'd.project_id' : "''"
    const projectCondition = tables.hasProject ? ' AND d.project_id = ?' : ''
    const sql = `SELECT c.chunk_id, c.doc_id, ${projectSelect} AS project_id, c.text,
                        c.locator_json, 0.0 AS score
                 FROM ${tables.chunks} c
                 JOIN ${tables.docs} d ON d.doc_id = c.doc_id
                 WHERE c.text LIKE ? ESCAPE '\\'${projectCondition}${filterSql(filter)}
                 ORDER BY c.doc_id, c.ordinal
                 LIMIT ?`
    const escaped = text.replaceAll('\\', '\\\\').replaceAll('%', '\\%').replaceAll('_', '\\_')
    const args: Array<string | number> = [`%${escaped}%`]
    if (tables.hasProject) args.push(projectId)
    args.push(...filterArgs(filter), topK)
    return this.collect(sql, args, [text.toLowerCase()])
  }

  private collect(sql: string, args: readonly (string | number)[], terms: readonly string[]): ChunkHit[] {
    const rows = this.db.prepare(sql).all(...args) as Array<{
      chunk_id: string
      doc_id: string
      project_id: string
      text: string
      locator_json: string
      score: number
    }>
    return rows.map((row) => ({
      chunkId: row.chunk_id,
      docId: row.doc_id,
      projectId: row.project_id,
      text: row.text,
      score: row.score,
      matchedTerms: [...terms],
      locatorJson: row.locator_json ?? ''
    }))
  }
}
