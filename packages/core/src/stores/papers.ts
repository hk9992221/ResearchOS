/**
 * Minimal store over global.db.papers. It never writes Activity
 * events: `paper.*` events belong to the literature module and are written into
 * the project database.
 *
 * `metadata` stays the raw JSON text of the Zotero item, exactly as the Go
 * `json.RawMessage` does, so a round-trip never reorders or reformats keys.
 */
import { type Clock, SystemClock, formatTimestamp, parseTimestamp } from '../clock'
import { AppErrorCode, appError } from '../errors'
import type { EventTx } from '../events'
import { newId } from '../ids'
import { type PaperFileState, type PaperSourceState, PAPER_FILE_STATES } from '../schema'

export interface Paper {
  id: string
  zoteroLibraryId: string
  zoteroKey: string
  attachmentKey?: string
  /** Raw JSON text (`{}` when absent). */
  metadata: string
  filePath?: string
  fileHash?: string
  fileSize?: number
  fileState: PaperFileState
  sourceState: PaperSourceState
  lastSyncedAt?: Date
  importedAt: Date
  updatedAt: Date
}

/** Fields an upsert inserts; identity is `(zoteroLibraryId, zoteroKey)`. */
export interface PaperInput {
  zoteroLibraryId?: string
  zoteroKey: string
  attachmentKey?: string
  metadata?: string
  filePath?: string
  fileHash?: string
  fileSize?: number
  fileState?: PaperFileState
  sourceState?: PaperSourceState
}

/** Zero-value filters mean "no filter". `titleLike` matches the Zotero title. */
export interface PaperQuery {
  zoteroLibraryId?: string
  zoteroKey?: string
  titleLike?: string
  fileState?: PaperFileState
  importedFrom?: Date
  importedTo?: Date
  limit?: number
  offset?: number
}

const SELECT_COLUMNS = `SELECT id, zotero_library_id, zotero_key, attachment_key, metadata,
	file_path, file_hash, file_size, file_state, source_state, last_synced_at,
	imported_at, updated_at`

interface PaperRow {
  id: string
  zotero_library_id: string
  zotero_key: string
  attachment_key: string | null
  metadata: string
  file_path: string | null
  file_hash: string | null
  file_size: number | null
  file_state: PaperFileState
  source_state: PaperSourceState
  last_synced_at: string | null
  imported_at: string
  updated_at: string
}

export class PaperStore {
  private readonly db: EventTx['db']
  private readonly clock: Clock

  constructor(source: EventTx, clock: Clock = new SystemClock()) {
    this.db = source.db
    this.clock = clock
  }

  /**
   * De-duplicates by `(zotero_library_id, zotero_key)`. An existing row is
   * returned unchanged — in particular `file_path`/`file_hash` are never
   * overwritten; PDF replacement goes through the literature module's hash
   * comparison and user confirmation.
   */
  upsert(tx: EventTx, paper: PaperInput): { id: string; created: boolean } {
    if (paper.zoteroKey === '') {
      throw appError(AppErrorCode.InvalidArgument, 'papers: upsert: empty zotero key')
    }
    const libraryId = paper.zoteroLibraryId === undefined || paper.zoteroLibraryId === '' ? '0' : paper.zoteroLibraryId
    const metadata = paper.metadata === undefined || paper.metadata === '' ? '{}' : paper.metadata
    if (!isValidJson(metadata)) {
      throw appError(AppErrorCode.InvalidArgument, 'papers: upsert: metadata is not valid JSON')
    }
    const id = newId(this.clock)
    const now = formatTimestamp(this.clock.now())
    const result = tx.db
      .prepare(
        `INSERT INTO papers(id, zotero_library_id, zotero_key, attachment_key, metadata,
                           file_path, file_hash, file_size, file_state, source_state,
                           last_synced_at, imported_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)
         ON CONFLICT(zotero_library_id, zotero_key) DO NOTHING`
      )
      .run(
        id,
        libraryId,
        paper.zoteroKey,
        paper.attachmentKey ?? null,
        metadata,
        paper.filePath ?? null,
        paper.fileHash ?? null,
        paper.fileSize === undefined || paper.fileSize === 0 ? null : paper.fileSize,
        paper.fileState ?? 'ok',
        paper.sourceState ?? 'ok',
        now,
        now
      )
    if (result.changes === 1) return { id, created: true }

    const existing = tx.db
      .prepare('SELECT id FROM papers WHERE zotero_library_id = ? AND zotero_key = ?')
      .get(libraryId, paper.zoteroKey) as { id: string } | undefined
    if (existing === undefined) {
      throw appError(AppErrorCode.Conflict, `papers: upsert: identity (${libraryId}, ${paper.zoteroKey}) vanished`)
    }
    return { id: existing.id, created: false }
  }

  get(id: string): Paper {
    const row = this.db.prepare(`${SELECT_COLUMNS} FROM papers WHERE id = ?`).get(id) as
      | PaperRow
      | undefined
    if (row === undefined) throw appError(AppErrorCode.NotFound, `papers: get ${id}: no such row`)
    return decode(row)
  }

  byZotero(libraryId: string, zoteroKey: string): Paper | undefined {
    const row = this.db
      .prepare(`${SELECT_COLUMNS} FROM papers WHERE zotero_library_id = ? AND zotero_key = ?`)
      .get(libraryId, zoteroKey) as PaperRow | undefined
    return row === undefined ? undefined : decode(row)
  }

  /** Matching papers, newest import first. */
  list(query: PaperQuery = {}): Paper[] {
    const where: string[] = []
    const args: unknown[] = []
    if (query.zoteroLibraryId !== undefined) {
      where.push('zotero_library_id = ?')
      args.push(query.zoteroLibraryId)
    }
    if (query.zoteroKey !== undefined) {
      where.push('zotero_key = ?')
      args.push(query.zoteroKey)
    }
    if (query.titleLike !== undefined) {
      where.push(`json_extract(metadata, '$.title') LIKE ?`)
      args.push(`%${query.titleLike}%`)
    }
    if (query.fileState !== undefined) {
      where.push('file_state = ?')
      args.push(query.fileState)
    }
    if (query.importedFrom !== undefined) {
      where.push('imported_at >= ?')
      args.push(formatTimestamp(query.importedFrom))
    }
    if (query.importedTo !== undefined) {
      where.push('imported_at <= ?')
      args.push(formatTimestamp(query.importedTo))
    }

    let sql = `${SELECT_COLUMNS} FROM papers`
    if (where.length > 0) sql += ` WHERE ${where.join(' AND ')}`
    sql += ' ORDER BY imported_at DESC, id'
    const limit = query.limit ?? 0
    const offset = query.offset ?? 0
    if (limit > 0) {
      sql += ' LIMIT ?'
      args.push(limit)
      if (offset > 0) {
        sql += ' OFFSET ?'
        args.push(offset)
      }
    } else if (offset > 0) {
      sql += ' LIMIT -1 OFFSET ?'
      args.push(offset)
    }
    const rows = this.db.prepare(sql).all(...args) as PaperRow[]
    return rows.map(decode)
  }

  /** Records the local PDF copy metadata. */
  updateFile(
    tx: EventTx,
    id: string,
    relPath: string,
    hash: string,
    size: number,
    state: PaperFileState
  ): void {
    if (!(PAPER_FILE_STATES as readonly string[]).includes(state)) {
      throw appError(AppErrorCode.InvalidArgument, `papers: update file ${id}: invalid file state ${JSON.stringify(state)}`)
    }
    const result = tx.db
      .prepare(
        'UPDATE papers SET file_path = ?, file_hash = ?, file_size = ?, file_state = ?, updated_at = ? WHERE id = ?'
      )
      .run(
        relPath === '' ? null : relPath,
        hash === '' ? null : hash,
        size === 0 ? null : size,
        state,
        formatTimestamp(this.clock.now()),
        id
      )
    if (result.changes === 0) {
      throw appError(AppErrorCode.NotFound, `papers: update file ${id}: no such row`)
    }
  }

  /** Removes a paper row; only the copy GC calls this. */
  delete(tx: EventTx, id: string): void {
    const result = tx.db.prepare('DELETE FROM papers WHERE id = ?').run(id)
    if (result.changes === 0) {
      throw appError(AppErrorCode.NotFound, `papers: delete ${id}: no such row`)
    }
  }
}

function decode(row: PaperRow): Paper {
  const paper: Paper = {
    id: row.id,
    zoteroLibraryId: row.zotero_library_id,
    zoteroKey: row.zotero_key,
    metadata: row.metadata,
    fileState: row.file_state,
    sourceState: row.source_state,
    importedAt: parseTimestamp(row.imported_at),
    updatedAt: parseTimestamp(row.updated_at)
  }
  if (row.attachment_key !== null) paper.attachmentKey = row.attachment_key
  if (row.file_path !== null) paper.filePath = row.file_path
  if (row.file_hash !== null) paper.fileHash = row.file_hash
  if (row.file_size !== null) paper.fileSize = row.file_size
  if (row.last_synced_at !== null) paper.lastSyncedAt = parseTimestamp(row.last_synced_at)
  return paper
}

function isValidJson(text: string): boolean {
  try {
    JSON.parse(text)
    return true
  } catch {
    return false
  }
}