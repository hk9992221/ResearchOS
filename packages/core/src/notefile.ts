/**
 * Note Markdown body files. The file is the authority for the
 * body; project.db stores the metadata. Writes are atomic: a same-directory
 * temp file, fsync, then rename, so the target is never truncated and a crash
 * leaves the old content plus at most a stale temp file.
 *
 * Frontmatter keys keep a fixed order (`id, kind, title, description, tags,
 * created, updated`); unknown keys survive a rewrite, sorted by name. The
 * database is the metadata authority, so typed fields always come from the row
 * and only unknown keys come from disk.
 */
import { createHash, randomBytes } from 'node:crypto'
import {
  closeSync,
  existsSync,
  fsyncSync,
  mkdirSync,
  openSync,
  readFileSync,
  renameSync,
  rmSync,
  statSync,
  type Stats,
  writeSync
} from 'node:fs'
import * as path from 'node:path'

import { Document, isMap, isScalar, isSeq, Pair, YAMLMap, parseDocument } from 'yaml'

import { type Clock, SystemClock, formatTimestamp, parseTimestamp } from './clock'
import { AppErrorCode, appError } from './errors'
import type { EventBus, EventTx } from './events'
import { isValidId, newId } from './ids'
import type { NoteKind } from './schema'
import { NOTE_KINDS } from './schema'
import type { ProjectHandle } from './workspace/handle'

const FENCE = '---'
const DIR_MODE = 0o755
const FILE_MODE = 0o644
const FILE_STATE_MISSING = 'missing'

/** The YAML frontmatter of a note file. */
export interface NoteFileMeta {
  id?: string
  kind?: string
  title?: string
  description?: string
  tags?: string[]
  created?: Date
  updated?: Date
  /** Unknown keys, kept as YAML nodes so a rewrite never drops them. */
  extras?: YAMLMap
}

export interface NoteFileStat {
  /** Lowercase hex sha256 of the written bytes. */
  hash: string
  mtime: Date
  size: number
}

export interface AtomicWriteOptions {
  /** Test seam: production must keep `renameSync`. */
  rename?: (from: string, to: string) => void
}

/** Lowercase hex sha256, the `notes.file_hash` token. */
export function hashBytes(data: string | Uint8Array): string {
  return createHash('sha256').update(data).digest('hex')
}

/** True when `data` starts with a closed frontmatter block. */
export function hasFrontmatter(data: string): boolean {
  return splitFences(data) !== undefined
}

/**
 * Splits the leading YAML frontmatter from the body. A file without
 * frontmatter, an unterminated block and malformed YAML all yield
 * `{ meta: {}, body: data }`, so a body may legitimately start with `---`.
 */
export function splitNoteFile(data: string): { meta: NoteFileMeta; body: string } {
  const fenced = splitFences(data)
  if (fenced === undefined) return { meta: {}, body: data }
  const { block, body } = fenced
  if (block.trim() === '') return { meta: {}, body }

  const doc = parseDocument(block)
  if (doc.errors.length > 0 || doc.contents === null || !isMap(doc.contents)) {
    return { meta: {}, body: data }
  }
  const meta: NoteFileMeta = {}
  const extras = new YAMLMap()
  for (const pair of doc.contents.items) {
    const key = pair.key
    if (!isScalar(key)) continue
    switch (key.value) {
      case 'id':
        meta.id = scalarText(pair.value)
        break
      case 'kind':
        meta.kind = scalarText(pair.value)
        break
      case 'title':
        meta.title = scalarText(pair.value)
        break
      case 'description':
        meta.description = scalarText(pair.value)
        break
      case 'tags': {
        const tags = stringSequence(pair.value)
        if (tags === undefined) extras.items.push(pair)
        else meta.tags = tags
        break
      }
      case 'created':
        meta.created = timestampValue(pair.value)
        break
      case 'updated':
        meta.updated = timestampValue(pair.value)
        break
      default:
        extras.items.push(pair)
    }
  }
  if (extras.items.length > 0) meta.extras = extras
  return { meta, body }
}

/**
 * Renders frontmatter plus body. Typed keys keep their fixed order and empty
 * optional keys are omitted; unknown keys follow, sorted by name. A meta with
 * nothing to write returns the body unchanged.
 */
export function joinNoteFile(meta: NoteFileMeta, body: string): string {
  const map = new YAMLMap()
  const put = (key: string, value: string | undefined): void => {
    if (value === undefined || value === '') return
    map.set(key, value)
  }
  put('id', meta.id)
  put('kind', meta.kind)
  put('title', meta.title)
  put('description', meta.description)
  if (meta.tags !== undefined && meta.tags.length > 0) {
    map.set('tags', [...meta.tags])
  }
  if (meta.created !== undefined && !Number.isNaN(meta.created.getTime())) {
    put('created', formatTimestamp(meta.created))
  }
  if (meta.updated !== undefined && !Number.isNaN(meta.updated.getTime())) {
    put('updated', formatTimestamp(meta.updated))
  }
  if (meta.extras !== undefined && meta.extras.items.length > 0) {
    const extras = [...meta.extras.items].sort((left, right) => {
      const a = keyText(left)
      const b = keyText(right)
      return a < b ? -1 : a > b ? 1 : 0
    })
    for (const pair of extras) map.items.push(pair)
  }
  if (map.items.length === 0) return body

  const doc = new Document(map)
  return `${FENCE}\n${doc.toString({ lineWidth: 0 })}${FENCE}\n${body}`
}

/** `base` with the unknown keys of `disk` preserved; typed fields come from base. */
export function mergeNoteFileMeta(base: NoteFileMeta, disk: NoteFileMeta): NoteFileMeta {
  return disk.extras === undefined ? base : { ...base, extras: disk.extras }
}

/**
 * Writes `data` through a same-directory temp file. The target is replaced by
 * the final rename, so a failure before it leaves the old file intact.
 */
export function writeFileAtomic(
  absolutePath: string,
  data: string | Uint8Array,
  options: AtomicWriteOptions = {}
): NoteFileStat {
  const directory = path.dirname(absolutePath)
  mkdirSync(directory, { recursive: true, mode: DIR_MODE })
  const temp = path.join(
    directory,
    `.${path.basename(absolutePath)}.tmp-${randomBytes(8).toString('hex')}`
  )
  const bytes = typeof data === 'string' ? Buffer.from(data, 'utf8') : data
  const rename = options.rename ?? renameSync

  let fd: number
  try {
    fd = openSync(temp, 'wx', FILE_MODE)
  } catch (error) {
    throw new Error(`notefile: create temp file: ${messageOf(error)}`, { cause: error })
  }
  try {
    let offset = 0
    while (offset < bytes.length) {
      offset += writeSync(fd, bytes, offset, bytes.length - offset)
    }
    fsyncSync(fd)
  } catch (error) {
    closeSync(fd)
    rmSync(temp, { force: true })
    throw new Error(`notefile: write temp file: ${messageOf(error)}`, { cause: error })
  }
  closeSync(fd)

  try {
    rename(temp, absolutePath)
  } catch (error) {
    rmSync(temp, { force: true })
    throw new Error(`notefile: rename into place: ${messageOf(error)}`, { cause: error })
  }
  // Directory fsync is best-effort: Windows refuses it on directories.
  try {
    const dirFd = openSync(directory, 'r')
    try {
      fsyncSync(dirFd)
    } finally {
      closeSync(dirFd)
    }
  } catch {
    // ignored, as in the Go implementation
  }

  let info: Stats
  try {
    info = statSync(absolutePath)
  } catch (error) {
    throw new Error(`notefile: stat written file: ${messageOf(error)}`, { cause: error })
  }
  return { hash: hashBytes(bytes), mtime: info.mtime, size: info.size }
}

export interface CreateNoteRequest {
  kind: NoteKind
  title: string
  description?: string
  body?: string
  actor: 'user' | 'mcp' | 'ai'
}

export interface CreateNoteResult {
  noteId: string
  /** Workspace-relative path (`projects/<pid>/notes/<id>.md`). */
  filePath: string
}

export interface ReadNoteResult {
  body: string
  stat: NoteFileStat
}

/** The note-file service of one project. */
export class NoteFileService {
  private readonly project: ProjectHandle
  private readonly events: EventBus
  private readonly clock: Clock

  constructor(project: ProjectHandle, events: EventBus, clock: Clock = new SystemClock()) {
    this.project = project
    this.events = events
    this.clock = clock
  }

  /**
   * Writes the body first (atomically), then the metadata row and
   * `entity.created` in the caller transaction. A file failure leaves no row;
   * a DB/event failure removes the file again.
   */
  create(tx: EventTx, request: CreateNoteRequest): CreateNoteResult {
    if (!(NOTE_KINDS as readonly string[]).includes(request.kind)) {
      throw appError(AppErrorCode.InvalidArgument, `notefile: invalid note kind ${JSON.stringify(request.kind)}`)
    }
    const noteId = newId(this.clock)
    const filePath = this.project.paths.noteFile(this.project.id, noteId)
    const absolutePath = this.project.paths.abs(filePath)
    const now = this.clock.now()
    const data = joinNoteFile(
      {
        id: noteId,
        kind: request.kind,
        title: request.title,
        description: request.description ?? '',
        created: now,
        updated: now
      },
      request.body ?? ''
    )
    const stat = writeFileAtomic(absolutePath, data)

    const removeFile = (): void => {
      try {
        rmSync(absolutePath, { force: true })
      } catch {
        // best effort, as in the Go implementation
      }
    }
    try {
      tx.db
        .prepare(
          `INSERT INTO notes(id, title, description, file_path, kind, created_at, updated_at, file_hash, file_mtime, file_state)
           VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'ok')`
        )
        .run(
          noteId,
          request.title,
          request.description ?? '',
          filePath,
          request.kind,
          formatTimestamp(now),
          formatTimestamp(now),
          stat.hash,
          formatTimestamp(stat.mtime)
        )
      this.events.emit(tx, {
        type: 'entity.created',
        entityType: 'note',
        entityId: noteId,
        summary: `创建笔记：${request.title}`,
        actor: request.actor,
        at: now
      })
    } catch (error) {
      removeFile()
      throw error
    }
    return { noteId, filePath }
  }

  /** Reads the body and its current stat. A lost file yields `NOTE_FILE_MISSING`. */
  read(noteId: string): ReadNoteResult {
    const row = this.project.db
      .prepare('SELECT file_path, file_state FROM notes WHERE id = ?')
      .get(noteId) as { file_path: string; file_state: string } | undefined
    if (row === undefined) throw appError(AppErrorCode.NotFound, `notefile: read ${noteId}: no such note`)
    if (row.file_state === FILE_STATE_MISSING) {
      throw appError(AppErrorCode.NoteFileMissing, `note ${noteId}`)
    }
    const absolutePath = this.project.paths.abs(row.file_path)
    if (!existsSync(absolutePath)) {
      throw appError(AppErrorCode.NoteFileMissing, `note ${noteId}`)
    }
    const body = readFileSync(absolutePath, 'utf8')
    const info = statSync(absolutePath)
    return { body, stat: { hash: hashBytes(body), mtime: info.mtime, size: info.size } }
  }

  /**
   * Atomically replaces the file (frontmatter included), refreshes the row and
   * emits the merged `note.updated`. A soft-deleted note is refused before the
   * file is touched.
   */
  write(tx: EventTx, noteId: string, data: string, actor: 'user' | 'mcp' | 'ai'): NoteFileStat {
    const row = tx.db
      .prepare('SELECT file_path, deleted_at FROM notes WHERE id = ?')
      .get(noteId) as { file_path: string; deleted_at: string | null } | undefined
    if (row === undefined) throw appError(AppErrorCode.NotFound, `notefile: write ${noteId}: no such note`)
    if (row.deleted_at !== null) throw appError(AppErrorCode.EntityDeleted, `note ${noteId}`)

    const absolutePath = this.project.paths.abs(row.file_path)
    const stat = writeFileAtomic(absolutePath, data)
    const now = this.clock.now()
    const result = tx.db
      .prepare(
        `UPDATE notes SET file_hash = ?, file_mtime = ?, file_state = 'ok', updated_at = ? WHERE id = ?`
      )
      .run(stat.hash, formatTimestamp(stat.mtime), formatTimestamp(now), noteId)
    if (result.changes === 0) {
      throw appError(AppErrorCode.NotFound, `notefile: write ${noteId}: no such note`)
    }
    this.events.emitNoteUpdated(tx, { noteId, summary: '更新笔记', actor, at: now })
    return stat
  }

  /**
   * Projects the notes row onto the frontmatter: metadata and tags come from
   * the database, the Markdown body and unknown keys from disk. Emits no event
   * and does not touch `updated_at`. A missing file is left as is.
   */
  rewriteMeta(tx: EventTx, noteId: string): void {
    const row = tx.db
      .prepare(
        'SELECT file_path, kind, title, description, created_at, updated_at FROM notes WHERE id = ?'
      )
      .get(noteId) as
      | {
          file_path: string
          kind: string
          title: string
          description: string
          created_at: string
          updated_at: string
        }
      | undefined
    if (row === undefined) throw appError(AppErrorCode.NotFound, `notefile: rewrite meta ${noteId}`)

    const absolutePath = this.project.paths.abs(row.file_path)
    if (!existsSync(absolutePath)) return
    const data = readFileSync(absolutePath, 'utf8')
    const disk = splitNoteFile(data)
    const tags = this.noteTags(tx, noteId)
    const meta = mergeNoteFileMeta(
      {
        id: noteId,
        kind: row.kind,
        title: row.title,
        description: row.description,
        tags,
        created: parseTimestamp(row.created_at),
        updated: parseTimestamp(row.updated_at)
      },
      disk.meta
    )
    const stat = writeFileAtomic(absolutePath, joinNoteFile(meta, disk.body))
    tx.db
      .prepare(
        `UPDATE notes SET file_hash = ?, file_mtime = ?, file_state = 'ok' WHERE id = ?`
      )
      .run(stat.hash, formatTimestamp(stat.mtime), noteId)
  }

  /** The absolute attachments directory of a note, created when needed. */
  attachmentsDir(noteId: string): string {
    if (!isValidId(noteId)) throw appError(AppErrorCode.BadEntityId, JSON.stringify(noteId))
    const absolutePath = this.project.paths.abs(
      this.project.paths.attachmentsDir(this.project.id, noteId)
    )
    mkdirSync(absolutePath, { recursive: true, mode: DIR_MODE })
    return absolutePath
  }

  private noteTags(tx: EventTx, noteId: string): string[] {
    const rows = tx.db
      .prepare(
        `SELECT t.name FROM entity_tags et JOIN tags t ON t.id = et.tag_id
         WHERE et.entity_type = 'note' AND et.entity_id = ? AND t.deleted_at IS NULL
         ORDER BY t.name`
      )
      .all(noteId) as Array<{ name: string }>
    return rows.map((row) => row.name)
  }
}

interface Fenced {
  block: string
  body: string
}

function splitFences(data: string): Fenced | undefined {
  const firstBreak = data.indexOf('\n')
  if (firstBreak < 0) return undefined
  if (data.slice(0, firstBreak).replace(/\r$/, '') !== FENCE) return undefined

  let cursor = firstBreak + 1
  while (cursor <= data.length) {
    const next = data.indexOf('\n', cursor)
    const lineEnd = next < 0 ? data.length : next
    const line = data.slice(cursor, lineEnd).replace(/\r$/, '')
    if (line === FENCE) {
      return {
        block: data.slice(firstBreak + 1, cursor),
        body: next < 0 ? '' : data.slice(next + 1)
      }
    }
    if (next < 0) return undefined
    cursor = next + 1
  }
  return undefined
}

function scalarText(node: unknown): string | undefined {
  return isScalar(node) && typeof node.value === 'string' ? node.value : undefined
}

function keyText(pair: Pair): string {
  return isScalar(pair.key) ? String(pair.key.value) : ''
}

function stringSequence(node: unknown): string[] | undefined {
  if (!isSeq(node)) return undefined
  const values: string[] = []
  for (const item of node.items) {
    if (!isScalar(item) || typeof item.value !== 'string') return undefined
    values.push(item.value)
  }
  return values
}

function timestampValue(node: unknown): Date | undefined {
  if (!isScalar(node)) return undefined
  const text = typeof node.value === 'string' ? node.value.trim() : ''
  try {
    return parseTimestamp(text)
  } catch {
    return undefined // the typed field is rewritten from the row anyway
  }
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}