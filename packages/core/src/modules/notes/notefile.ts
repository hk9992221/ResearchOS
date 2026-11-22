import { readFileSync, rmSync, statSync } from 'node:fs'

import { formatTimestamp, parseTimestamp } from '../../clock'
import {
  NoteFileService,
  hashBytes,
  joinNoteFile,
  mergeNoteFileMeta,
  splitNoteFile,
  writeFileAtomic,
  type NoteFileMeta,
  type NoteFileStat
} from '../../notefile'
import type { Actor, NoteKind } from '../../schema'
import { NOTE_KINDS } from '../../schema'
import { entityDeleted, invalid, notFound, noteFileMissing, type NotesContext } from './support'

/**
 * Adapter over the foundation `NoteFileService` (W1-B). Body writes, atomic
 * replace, frontmatter rendering and `note.updated` emission stay in the
 * foundation; this module only knows the notes row and metadata projections.
 */
export interface FileStat {
  hash: string
  mtime: string
  size: number
}

export interface CreateNoteFileRequest {
  kind: string
  title: string
  description?: string
  body: string
  actor?: Actor
  /** Tag names written into the frontmatter on create (optional). */
  tags?: string[]
}

export interface CreatedNoteFile {
  noteId: string
  filePath: string
  stat: FileStat
  createdAt: string
}

export function noteFileService(ctx: NotesContext): NoteFileService {
  return new NoteFileService(ctx.handle, ctx.events, ctx.clock)
}

export function toFileStat(stat: NoteFileStat): FileStat {
  return { hash: stat.hash, mtime: formatTimestamp(stat.mtime), size: stat.size }
}

export { hashBytes, joinNoteFile, mergeNoteFileMeta, splitNoteFile }
export const writeAtomicFile = writeFileAtomic

interface NoteRow {
  id: string
  title: string
  description: string
  filePath: string
  kind: string
  createdAt: string
  updatedAt: string
  deletedAt: string | null
  fileHash: string | null
  fileMtime: string | null
  fileState: string
  paperId: string | null
  metaJson: string
}

export function loadNoteRow(ctx: NotesContext, noteId: string): NoteRow | null {
  const row = ctx.db
    .prepare(
      `SELECT id, title, description, file_path, kind, created_at, updated_at, deleted_at,
              file_hash, file_mtime, file_state, paper_id, meta_json
         FROM notes WHERE id = ?`
    )
    .get(noteId) as Record<string, unknown> | undefined
  if (row === undefined) return null
  return {
    id: String(row.id),
    title: String(row.title ?? ''),
    description: String(row.description ?? ''),
    filePath: String(row.file_path),
    kind: String(row.kind),
    createdAt: String(row.created_at),
    updatedAt: String(row.updated_at),
    deletedAt: row.deleted_at === null ? null : String(row.deleted_at),
    fileHash: row.file_hash === null ? null : String(row.file_hash),
    fileMtime: row.file_mtime === null ? null : String(row.file_mtime),
    fileState: String(row.file_state ?? 'ok'),
    paperId: row.paper_id === null ? null : String(row.paper_id),
    metaJson: String(row.meta_json ?? '')
  }
}

export function noteTagNames(ctx: NotesContext, noteId: string): string[] {
  const rows = ctx.db
    .prepare(
      `SELECT t.name FROM entity_tags et
         JOIN tags t ON t.id = et.tag_id
        WHERE et.entity_type = 'note' AND et.entity_id = ? AND t.deleted_at IS NULL
        ORDER BY t.name`
    )
    .all(noteId) as Array<{ name: string }>
  return rows.map((row) => row.name)
}

export function noteFileMeta(ctx: NotesContext, row: NoteRow): NoteFileMeta {
  return {
    id: row.id,
    kind: row.kind,
    title: row.title,
    description: row.description,
    tags: noteTagNames(ctx, row.id),
    created: parseTimestamp(row.createdAt),
    updated: parseTimestamp(row.updatedAt)
  }
}

/**
 * Writes the Markdown file and inserts the notes row in the caller's
 * transaction through the foundation service. A database failure removes the
 * file again, so a failed create leaves neither a row nor a body behind.
 */
export function createNoteFile(ctx: NotesContext, request: CreateNoteFileRequest): CreatedNoteFile {
  const kind = request.kind as NoteKind
  if (!(NOTE_KINDS as readonly string[]).includes(kind)) {
    throw invalid(`invalid note kind ${JSON.stringify(request.kind)}`, { field: 'kind' })
  }
  const created = noteFileService(ctx).create(
    { db: ctx.db },
    {
      kind,
      title: request.title,
      description: request.description,
      body: request.body,
      actor: request.actor ?? 'user'
    }
  )
  const row = loadNoteRow(ctx, created.noteId)
  return {
    noteId: created.noteId,
    filePath: created.filePath,
    stat: {
      hash: row?.fileHash ?? '',
      mtime: row?.fileMtime ?? '',
      size: Buffer.byteLength(request.body, 'utf8')
    },
    createdAt: row?.createdAt ?? formatTimestamp(ctx.clock.now())
  }
}

export interface ReadNoteFile {
  data: string
  stat: FileStat
}

/** Reads the raw body file (frontmatter included) and its current hash. */
export function readNoteFile(ctx: NotesContext, noteId: string, filePath: string): ReadNoteFile | null {
  const absolute = ctx.paths.abs(filePath)
  let data: Buffer
  try {
    data = readFileSync(absolute)
  } catch (error) {
    if (isMissingFile(error)) return null
    throw error
  }
  const info = statSync(absolute)
  return {
    data: data.toString('utf8'),
    stat: {
      hash: hashBytes(data),
      mtime: formatTimestamp(new Date(info.mtimeMs)),
      size: info.size
    }
  }
}

function isMissingFile(error: unknown): boolean {
  return error instanceof Error && 'code' in error && error.code === 'ENOENT'
}

export function requireNoteRow(ctx: NotesContext, noteId: string): NoteRow {
  const row = loadNoteRow(ctx, noteId)
  if (row === null) throw notFound(`note ${noteId} not found`)
  if (row.deletedAt !== null) throw entityDeleted('note', noteId)
  return row
}

/** Atomically replaces a live note body through the foundation service. */
export function writeNoteFile(
  ctx: NotesContext,
  noteId: string,
  data: string,
  actor: Actor
): FileStat {
  const row = loadNoteRow(ctx, noteId)
  if (row === null) throw noteFileMissing(noteId)
  if (row.deletedAt !== null) throw entityDeleted('note', noteId)
  const stat = noteFileService(ctx).write({ db: ctx.db }, noteId, data, actor)
  return toFileStat(stat)
}

/** Projects row metadata onto frontmatter; no event and no updated_at move. */
export function rewriteNoteMeta(ctx: NotesContext, noteId: string): void {
  noteFileService(ctx).rewriteMeta({ db: ctx.db }, noteId)
}

export function markNoteFileState(ctx: NotesContext, noteId: string, state: 'ok' | 'missing'): void {
  ctx.db.prepare(`UPDATE notes SET file_state = ? WHERE id = ? AND file_state <> ?`).run(state, noteId, state)
}

export type { NoteRow, NoteFileMeta }