/**
 * Paper Notes. One live `kind='paper_note'` row per
 * `(project, paper)`; the unique partial index enforces it. The body file is
 * owned by the notes service (W7) through its port — this module only stores
 * the structured `meta_json` fields and the `note --references--> paper`
 * relation.
 */
import type { Note, PaperNoteFields, PaperNoteView } from '@researchos/shared'
import { EMPTY_PAPER_NOTE_FIELDS } from '@researchos/shared'

import type { Actor } from '../../../schema'
import { appError, AppErrorCode } from '../../../errors'
import type { LiteratureContext } from '../context'
import type { LiteratureNote } from '../deps'
import { getPaperRow } from './query'

export interface NoteRowLike {
  id: string
  title: string
  file_path: string
  kind: string
  description: string
  paper_id: string | null
  meta_json: string
  file_hash: string | null
  file_mtime: string | null
  file_state: string
  created_at: string
  updated_at: string
  deleted_at: string | null
}

export const NOTE_COLUMNS = `id, title, file_path, kind, description, paper_id, meta_json,
  file_hash, file_mtime, file_state, created_at, updated_at, deleted_at`

export function noteToDto(row: NoteRowLike): Note {
  let metaJson: Record<string, unknown> | null = null
  if (row.meta_json.trim() !== '') {
    try {
      const value: unknown = JSON.parse(row.meta_json)
      if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
        // Parsed JSON object; key access needs the index signature TS loses here.
        const object: Record<string, unknown> = value as Record<string, unknown>
        metaJson = object
      }
    } catch {
      metaJson = null
    }
  }
  return {
    id: row.id,
    title: row.title,
    filePath: row.file_path,
    kind: row.kind as Note['kind'],
    description: row.description,
    paperId: row.paper_id,
    metaJson,
    fileHash: row.file_hash,
    fileMtime: row.file_mtime,
    fileState: row.file_state === 'missing' ? 'missing' : 'ok',
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    deletedAt: row.deleted_at
  }
}

export function decodeFields(raw: string): PaperNoteFields {
  if (raw.trim() === '') return { ...EMPTY_PAPER_NOTE_FIELDS }
  let value: unknown
  try {
    value = JSON.parse(raw)
  } catch {
    return { ...EMPTY_PAPER_NOTE_FIELDS }
  }
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return { ...EMPTY_PAPER_NOTE_FIELDS }
  }
  const wrapper = value as Record<string, unknown>
  const nested = wrapper['paper_note']
  const candidate =
    nested !== null && typeof nested === 'object' && !Array.isArray(nested)
      ? (nested as Record<string, unknown>)
      : wrapper
  const field = (key: string): string =>
    typeof candidate[key] === 'string' ? (candidate[key] as string) : ''
  return {
    summary: field('summary'),
    problem: field('problem'),
    method: field('method'),
    contribution: field('contribution'),
    limitations: field('limitations'),
    understanding: field('understanding')
  }
}

export function encodeFields(fields: PaperNoteFields): string {
  return JSON.stringify({
    paper_note: {
      summary: fields.summary,
      problem: fields.problem,
      method: fields.method,
      contribution: fields.contribution,
      limitations: fields.limitations,
      understanding: fields.understanding
    }
  })
}

export function findPaperNoteRow(
  ctx: LiteratureContext,
  projectId: string,
  paperId: string
): NoteRowLike | null {
  const row = ctx.workspace
    .project(projectId)
    .db.prepare(
      `SELECT ${NOTE_COLUMNS} FROM notes
        WHERE kind = 'paper_note' AND paper_id = ?
        ORDER BY created_at, id LIMIT 1`
    )
    .get(paperId) as NoteRowLike | undefined
  return row ?? null
}

export function findPaperNoteRowById(
  ctx: LiteratureContext,
  projectId: string,
  noteId: string
): NoteRowLike | null {
  const row = ctx.workspace
    .project(projectId)
    .db.prepare(`SELECT ${NOTE_COLUMNS} FROM notes WHERE id = ? AND kind = 'paper_note'`)
    .get(noteId) as NoteRowLike | undefined
  return row ?? null
}

function projection(
  ctx: LiteratureContext,
  projectId: string,
  note: LiteratureNote
): PaperNoteView {
  let bodyMd = ''
  let bodyHash: string | null = note.fileHash
  try {
    const body = ctx.notesFor(projectId).loadBody(note.id)
    bodyMd = body.markdown
    bodyHash = body.hash === '' ? bodyHash : body.hash
  } catch {
    // A missing body file leaves the fields readable; the note row is still
    // returned so the editor can recreate the file on the next save.
  }
  const row = ctx.workspace
    .project(projectId)
    .db.prepare(`SELECT ${NOTE_COLUMNS} FROM notes WHERE id = ?`)
    .get(note.id) as NoteRowLike | undefined
  return {
    note:
      row === undefined
        ? {
            id: note.id,
            title: note.title,
            filePath: note.filePath,
            kind: 'paper_note',
            description: note.description,
            paperId: note.paperId,
            metaJson: null,
            fileHash: note.fileHash,
            fileMtime: note.fileMtime,
            fileState: note.fileState,
            createdAt: note.createdAt,
            updatedAt: note.updatedAt,
            deletedAt: note.deletedAt
          }
        : noteToDto(row),
    fields: decodeFields(row?.meta_json ?? ''),
    bodyMd,
    bodyHash
  }
}

export function getPaperNote(
  ctx: LiteratureContext,
  projectId: string,
  paperId: string
): PaperNoteView | null {
  ctx.requireProject(projectId)
  const row = findPaperNoteRow(ctx, projectId, paperId)
  if (row === null) return null
  const note = ctx.notesFor(projectId).getNote(row.id)
  return projection(ctx, projectId, note)
}

function paperTitle(ctx: LiteratureContext, paperId: string): string {
  const paper = getPaperRow(ctx, paperId)
  try {
    const parsed: unknown = JSON.parse(paper.metadata)
    if (parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed)) {
      // Parsed metadata object; read one field rather than widening the type.
      const object: Record<string, unknown> = parsed as Record<string, unknown>
      const title = object['title']
      if (typeof title === 'string') return title.trim()
    }
  } catch {
    // fall through
  }
  return ''
}

export function createPaperNote(
  ctx: LiteratureContext,
  projectId: string,
  paperId: string,
  actor: Actor
): PaperNoteView {
  ctx.requireActive(projectId)
  getPaperRow(ctx, paperId)
  const link = ctx.workspace
    .project(projectId)
    .db.prepare(`SELECT 1 AS one FROM project_papers WHERE project_id = ? AND paper_id = ?`)
    .get(projectId, paperId) as { one: number } | undefined
  if (link === undefined) {
    throw appError(AppErrorCode.Conflict, '论文尚未关联到该项目，无法创建笔记', {
      details: { paper_id: paperId }
    })
  }
  const existing = findPaperNoteRow(ctx, projectId, paperId)
  if (existing !== null) {
    if (existing.deleted_at !== null) {
      throw appError(AppErrorCode.EntityDeleted, '该论文的笔记已删除', {
        details: { note_id: existing.id }
      })
    }
    throw appError(AppErrorCode.Conflict, '该论文已有 Paper Note', {
      details: { note_id: existing.id }
    })
  }
  const title = paperTitle(ctx, paperId)
  const noteTitle = title === '' ? '文献笔记' : `《${title}》笔记`
  const created = ctx.notesFor(projectId).createNote({ kind: 'paper_note', title: noteTitle, paperId, actor })
  const handle = ctx.workspace.project(projectId)
  try {
    ctx.inTx(handle, () => {
      handle.db
        .prepare(`UPDATE notes SET paper_id = ?, meta_json = ?, updated_at = ? WHERE id = ?`)
        .run(paperId, encodeFields(EMPTY_PAPER_NOTE_FIELDS), ctx.now(), created.id)
      ctx.relations(handle).link(
        handle,
        {
          srcType: 'note',
          srcId: created.id,
          relation: 'references',
          dstType: 'paper',
          dstId: paperId
        },
        actor
      )
    })
  } catch (error) {
    try {
      ctx.notesFor(projectId).deleteNote(created.id, actor)
    } catch {
      // The dangling note is visible in the notes list and can be deleted there.
    }
    throw error
  }
  const note = ctx.notesFor(projectId).getNote(created.id)
  return projection(ctx, projectId, note)
}

export interface PaperNotePatch {
  title?: string
  fields?: PaperNoteFields
}

export function updatePaperNote(
  ctx: LiteratureContext,
  projectId: string,
  noteId: string,
  patch: PaperNotePatch,
  expectedUpdatedAt: string | undefined,
  actor: Actor
): PaperNoteView {
  ctx.requireActive(projectId)
  const row = findPaperNoteRowById(ctx, projectId, noteId)
  if (row === null) {
    throw appError(AppErrorCode.NotFound, `Paper Note ${noteId} 不存在`, {
      details: { note_id: noteId }
    })
  }
  if (row.deleted_at !== null) {
    throw appError(AppErrorCode.EntityDeleted, '该论文笔记已删除', { details: { note_id: noteId } })
  }
  if (expectedUpdatedAt !== undefined && expectedUpdatedAt !== row.updated_at) {
    throw appError(AppErrorCode.Conflict, '笔记已被其他写入修改', {
      details: { expectedUpdatedAt, currentUpdatedAt: row.updated_at }
    })
  }
  if (patch.title !== undefined) {
    ctx.notesFor(projectId).updateTitle(noteId, patch.title, actor)
  }
  if (patch.fields !== undefined) {
    const handle = ctx.workspace.project(projectId)
    const meta = encodeFields(patch.fields)
    ctx.inTx(handle, () => {
      const changed = handle.db
        .prepare(
          `UPDATE notes SET meta_json = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`
        )
        .run(meta, ctx.now(), noteId).changes
      if (changed === 0) {
        throw appError(AppErrorCode.EntityDeleted, '该论文笔记已删除', { details: { note_id: noteId } })
      }
      ctx.events.emitNoteUpdated(handle, {
        noteId,
        summary: '更新文献笔记字段',
        actor,
        at: ctx.clock.now()
      })
    })
  }
  const note = ctx.notesFor(projectId).getNote(noteId)
  return projection(ctx, projectId, note)
}

export function savePaperNoteBody(
  ctx: LiteratureContext,
  projectId: string,
  noteId: string,
  markdown: string,
  baseHash: string,
  actor: Actor
): { fileHash: string; savedAt: string; note: PaperNoteView | null } {
  ctx.requireActive(projectId)
  const row = findPaperNoteRowById(ctx, projectId, noteId)
  if (row === null) {
    throw appError(AppErrorCode.NotFound, `Paper Note ${noteId} 不存在`, {
      details: { note_id: noteId }
    })
  }
  if (row.deleted_at !== null) {
    throw appError(AppErrorCode.EntityDeleted, '该论文笔记已删除', { details: { note_id: noteId } })
  }
  const saved = ctx.notesFor(projectId).saveBody({ noteId, markdown, baseHash, actor })
  const paperId = row.paper_id
  return {
    fileHash: saved.fileHash,
    savedAt: saved.savedAt,
    note: paperId === null ? null : getPaperNote(ctx, projectId, paperId)
  }
}