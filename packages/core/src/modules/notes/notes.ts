import { rmSync } from 'node:fs'

import { NOTE_KINDS, type Note, type NoteKind } from '@researchos/shared'
import { formatTimestamp } from '../../clock'
import { AppErrorCode } from '../../errors'
import { appError } from '../../errors'
import { newId } from '../../ids'
import type { Actor } from '../../schema'
import { emitEvent, emitNoteUpdated, restoreEntity, softDelete } from './activity'
import { joinNoteFile, mergeNoteFileMeta, splitNoteFile } from './notefile'
import {
  createNoteFile,
  hashBytes,
  loadNoteRow,
  markNoteFileState,
  noteFileMeta,
  noteTagNames,
  readNoteFile,
  requireNoteRow,
  rewriteNoteMeta,
  writeNoteFile,
  type NoteRow
} from './notefile'
import {
  addTags,
  tagNamesFor
} from './tags'
import {
  conflict,
  entityDeleted,
  invalid,
  isUniqueViolation,
  notFound,
  nowStamp,
  parseJsonObject,
  type Json,
  type NotesContext
} from './support'

export interface NoteListQuery {
  kind?: NoteKind | string
  q?: string
  tags?: string[]
  limit?: number
  offset?: number
  includeDeleted?: boolean
}

export interface NoteCreateInput {
  kind?: NoteKind | string
  title?: string
  description?: string
  bodyMd?: string
  tags?: string[]
  paperId?: string | null
  actor?: Actor
}

export interface NoteMetaPatch {
  title?: string
  description?: string
}

export interface NoteSaveResult {
  noteId: string
  fileHash: string
  fileMtime: string
  savedAt: string
}

export interface NoteBody {
  noteId: string
  markdown: string
  hash: string
  state: 'ok' | 'missing'
}

export function isNoteKind(value: string): value is NoteKind {
  return (NOTE_KINDS as readonly string[]).includes(value)
}

function requireKind(value: string | undefined): NoteKind {
  const kind = value ?? 'scratchpad'
  if (!isNoteKind(kind)) throw invalid(`invalid note kind ${JSON.stringify(kind)}`, { field: 'kind' })
  return kind
}

function validateTitle(value: string, field = 'title'): string {
  const trimmed = value.trim()
  if (trimmed === '') throw invalid('title must not be empty', { field })
  if (Array.from(trimmed).length > 200) throw invalid('title must be at most 200 characters', { field })
  return trimmed
}

export function noteToDto(row: NoteRow, tags: string[] = []): Note {
  return {
    id: row.id,
    title: row.title,
    filePath: row.filePath,
    kind: row.kind as NoteKind,
    description: row.description,
    paperId: row.paperId,
    metaJson: row.metaJson === '' ? null : parseJsonObject(row.metaJson),
    tags,
    fileHash: row.fileHash,
    fileMtime: row.fileMtime,
    fileState: row.fileState as 'ok' | 'missing',
    createdAt: row.createdAt,
    updatedAt: row.updatedAt,
    deletedAt: row.deletedAt
  }
}

export function getNote(ctx: NotesContext, noteId: string): Note {
  const row = loadNoteRow(ctx, noteId)
  if (row === null) throw notFound(`note ${noteId} not found`)
  return noteToDto(row, tagNamesFor(ctx, 'note', noteId))
}

export function listNotes(ctx: NotesContext, query: NoteListQuery): {
  items: Note[]
  total: number
  limit: number
  offset: number
} {
  const where: string[] = []
  const args: unknown[] = []
  if (query.kind !== undefined && query.kind !== '') {
    if (!isNoteKind(query.kind)) throw invalid(`invalid note kind ${JSON.stringify(query.kind)}`, { field: 'kind' })
    where.push('kind = ?')
    args.push(query.kind)
  }
  const titleLike = query.q?.trim()
  if (titleLike !== undefined && titleLike !== '') {
    where.push('instr(lower(title), lower(?)) > 0')
    args.push(titleLike)
  }
  if (query.includeDeleted !== true) where.push('deleted_at IS NULL')
  const tagValues = (query.tags ?? []).filter((tag) => tag !== '')
  if (tagValues.length > 0) {
    const clauses: string[] = []
    for (const value of tagValues) {
      clauses.push(
        `EXISTS (SELECT 1 FROM entity_tags et JOIN tags t ON t.id = et.tag_id
                  WHERE et.entity_type = 'note' AND et.entity_id = notes.id
                    AND (et.tag_id = ? OR lower(t.name) = lower(?)))`
      )
      args.push(value, value)
    }
    where.push(`(${clauses.join(' OR ')})`)
  }
  const clause = where.length === 0 ? '' : ` WHERE ${where.join(' AND ')}`
  const totalRow = ctx.db.prepare(`SELECT count(*) AS n FROM notes${clause}`).get(...args) as { n: number }
  const limit = query.limit === undefined || query.limit <= 0 ? 100 : Math.min(Math.trunc(query.limit), 500)
  const offset = query.offset === undefined || query.offset < 0 ? 0 : Math.trunc(query.offset)
  const rows = ctx.db
    .prepare(
      `SELECT id, title, description, file_path, kind, created_at, updated_at, deleted_at,
              file_hash, file_mtime, file_state, paper_id, meta_json
         FROM notes${clause}
        ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`
    )
    .all(...args, limit, offset) as Array<Record<string, unknown>>
  const items = rows.map((row) =>
    noteToDto({
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
      fileState: String(row.file_state),
      paperId: row.paper_id === null ? null : String(row.paper_id),
      metaJson: String(row.meta_json ?? '')
    }, tagNamesFor(ctx, 'note', String(row.id)))
  )
  return { items, total: totalRow.n, limit, offset }
}

export function createNote(ctx: NotesContext, input: NoteCreateInput): Note {
  const kind = requireKind(input.kind)
  const title = input.title === undefined ? '' : validateTitle(input.title)
  const body = input.bodyMd ?? ''
  if (Buffer.byteLength(body, 'utf8') > 5 * 1024 * 1024) {
    throw invalid('note body exceeds the 5 MiB limit', { field: 'bodyMd' })
  }
  return ctx.inTx(() => {
    const created = createNoteFile(ctx, {
      kind,
      title,
      description: input.description ?? '',
      body,
      actor: input.actor,
      tags: input.tags
    })
    try {
      if (input.paperId !== undefined && input.paperId !== null) {
        ctx.db.prepare(`UPDATE notes SET paper_id = ? WHERE id = ?`).run(input.paperId, created.noteId)
      }
      if (input.tags !== undefined && input.tags.length > 0) {
        addTags(ctx, 'note', created.noteId, input.tags, input.actor)
      }
      return getNote(ctx, created.noteId)
    } catch (error) {
      rmSync(ctx.paths.abs(created.filePath), { force: true })
      if (isUniqueViolation(error)) {
        throw conflict('a live note of this kind already exists for the target', { kind })
      }
      throw error
    }
  })
}

export function updateNoteMeta(
  ctx: NotesContext,
  noteId: string,
  patch: NoteMetaPatch,
  expectedUpdatedAt: string | undefined,
  actor: Actor = 'user'
): Note {
  if (patch.title === undefined && patch.description === undefined) {
    throw invalid('patch must contain title or description')
  }
  return ctx.inTx(() => {
    const row = requireNoteRow(ctx, noteId)
    const title = patch.title === undefined ? row.title : validateTitle(patch.title)
    const description = patch.description === undefined ? row.description : patch.description
    if (title === row.title && description === row.description) return getNote(ctx, noteId)
    if (expectedUpdatedAt !== undefined && expectedUpdatedAt !== row.updatedAt) {
      throw conflict('note was modified by another writer', {
        expectedUpdatedAt,
        currentUpdatedAt: row.updatedAt,
        current: noteToDto(row)
      })
    }
    const sql =
      expectedUpdatedAt === undefined
        ? `UPDATE notes SET title = ?, description = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`
        : `UPDATE notes SET title = ?, description = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL AND updated_at = ?`
    const args: unknown[] = [title, description, nowStamp(ctx), noteId]
    if (expectedUpdatedAt !== undefined) args.push(expectedUpdatedAt)
    const result = ctx.db.prepare(sql).run(...args)
    if (result.changes === 0) {
      const current = loadNoteRow(ctx, noteId)
      throw conflict('note was modified by another writer', {
        expectedUpdatedAt,
        currentUpdatedAt: current?.updatedAt ?? null
      })
    }
    rewriteNoteMeta(ctx, noteId)
    emitNoteUpdated(ctx, { noteId, summary: '更新笔记元数据', actor, at: ctx.clock.now() })
    return getNote(ctx, noteId)
  })
}

export function loadNoteBody(ctx: NotesContext, noteId: string): NoteBody {
  const row = requireNoteRow(ctx, noteId)
  const file = readNoteFile(ctx, noteId, row.filePath)
  if (file === null) {
    if (row.fileState !== 'missing') markNoteFileState(ctx, noteId, 'missing')
    return { noteId, markdown: '', hash: '', state: 'missing' }
  }
  return { noteId, markdown: splitNoteFile(file.data).body, hash: file.stat.hash, state: 'ok' }
}

/**
 * Optimistic body save. The client token is the sha256 of the whole file. A
 * stale token is CONFLICT with the current on-disk hash and writes nothing.
 */
export function saveNoteBody(
  ctx: NotesContext,
  noteId: string,
  markdown: string,
  baseHash: string,
  actor: Actor = 'user'
): NoteSaveResult {
  if (Buffer.byteLength(markdown, 'utf8') > 5 * 1024 * 1024) {
    throw appError(AppErrorCode.LimitExceeded, 'note body exceeds the 5 MiB limit', {
      details: { limitBytes: 5 * 1024 * 1024 }
    })
  }
  return ctx.inTx(() => {
    const row = requireNoteRow(ctx, noteId)
    const file = readNoteFile(ctx, noteId, row.filePath)
    const diskHash = file === null ? '' : file.stat.hash
    const disk = splitNoteFile(file?.data ?? '')
    const meta = mergeNoteFileMeta(noteFileMeta(ctx, row), disk.meta)
    const submitted = joinNoteFile(meta, markdown)
    const submittedHash = hashBytes(submitted)
    if (diskHash !== baseHash && submittedHash !== diskHash) {
      throw conflict('note body changed on disk', {
        expectedHash: baseHash,
        diskHash,
        disk_hash: diskHash,
        currentHash: diskHash,
        expected_hash: baseHash
      })
    }
    const stat = writeNoteFile(ctx, noteId, submitted, actor)
    const after = loadNoteRow(ctx, noteId)
    return {
      noteId,
      fileHash: stat.hash,
      fileMtime: stat.mtime,
      savedAt: after?.updatedAt ?? nowStamp(ctx)
    }
  })
}

export function deleteNote(ctx: NotesContext, noteId: string, actor: Actor = 'user'): { deleted: boolean } {
  return ctx.inTx(() => {
    const row = loadNoteRow(ctx, noteId)
    if (row === null) throw notFound(`note ${noteId} not found`)
    softDelete(ctx, 'note', noteId, actor)
    return { deleted: true }
  })
}

export function restoreNote(ctx: NotesContext, noteId: string, actor: Actor = 'user'): Note {
  return ctx.inTx(() => {
    const row = loadNoteRow(ctx, noteId)
    if (row === null) throw notFound(`note ${noteId} not found`)
    restoreEntity(ctx, 'note', noteId, actor)
    const file = readNoteFile(ctx, noteId, row.filePath)
    if (file === null) markNoteFileState(ctx, noteId, 'missing')
    else markNoteFileState(ctx, noteId, 'ok')
    return getNote(ctx, noteId)
  })
}

export function noteTagNamesOf(ctx: NotesContext, noteId: string): string[] {
  return noteTagNames(ctx, noteId)
}

export function noteOutboundTagNames(ctx: NotesContext, noteId: string): string[] {
  return tagNamesFor(ctx, 'note', noteId)
}

/* ----------------------------------------------------- promote / detach */

export interface PromotedRecord {
  id: string
  databaseId: string
  noteId: string | null
  title: string
  data: Json
  sortOrder: number
  createdAt: string
  updatedAt: string
  deletedAt: string | null
}

export function promoteNote(
  ctx: NotesContext,
  noteId: string,
  databaseId: string,
  actor: Actor = 'user'
): PromotedRecord {
  return ctx.inTx(() => {
    const note = requireNoteRow(ctx, noteId)
    const database = ctx.db.prepare(`SELECT deleted_at FROM research_databases WHERE id = ?`).get(databaseId) as
      | { deleted_at: string | null }
      | undefined
    if (database === undefined) throw notFound(`database ${databaseId} not found`)
    if (database.deleted_at !== null) throw entityDeleted('research_database', databaseId)
    const existing = ctx.db
      .prepare(`SELECT id FROM db_records WHERE note_id = ? AND deleted_at IS NULL`)
      .get(noteId) as { id: string } | undefined
    if (existing !== undefined) {
      throw invalid(`note ${noteId} is already the page of record ${existing.id}`, { field: 'noteId' })
    }
    const titleField = ctx.db
      .prepare(`SELECT id FROM db_fields WHERE database_id = ? AND role = 'title' AND deleted_at IS NULL`)
      .get(databaseId) as { id: string } | undefined
    if (titleField === undefined) throw invalid(`database ${databaseId} has no title field`, { field: 'databaseId' })
    const positionRow = ctx.db
      .prepare(`SELECT COALESCE(MAX(sort_order), -1) + 1 AS n FROM db_records WHERE database_id = ?`)
      .get(databaseId) as { n: number }
    const values: Json = { [titleField.id]: note.title }
    const now = nowStamp(ctx)
    const recordId = newId(ctx.clock)
    ctx.db
      .prepare(
        `INSERT INTO db_records(id, database_id, note_id, title, data, sort_order, created_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
      )
      .run(recordId, databaseId, noteId, note.title, JSON.stringify(values), positionRow.n, now, now)
    emitEvent(ctx, {
      type: 'entity.created',
      entityType: 'db_record',
      entityId: recordId,
      summary: `加入数据库：${note.title}`,
      actor,
      at: ctx.clock.now()
    })
    return {
      id: recordId,
      databaseId,
      noteId,
      title: note.title,
      data: values,
      sortOrder: positionRow.n,
      createdAt: now,
      updatedAt: now,
      deletedAt: null
    }
  })
}

export function detachRecord(
  ctx: NotesContext,
  recordId: string,
  deleteNoteToo: boolean,
  actor: Actor = 'user'
): { deleted: boolean } {
  return ctx.inTx(() => {
    const record = ctx.db.prepare(`SELECT note_id FROM db_records WHERE id = ?`).get(recordId) as
      | { note_id: string | null }
      | undefined
    if (record === undefined) throw notFound(`record ${recordId} not found`)
    softDelete(ctx, 'db_record', recordId, actor)
    if (deleteNoteToo && record.note_id !== null) softDelete(ctx, 'note', record.note_id, actor)
    return { deleted: true }
  })
}