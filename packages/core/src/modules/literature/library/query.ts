/**
 * Paper queries and project associations. Global rows
 * live in global.db; the project link lives in `project_papers`. A missing
 * local copy never drops the paper from a listing: it is reported as broken.
 */
import type { PaperSourceState, PaperView, ProjectPaper } from '@researchos/shared'
import { appError, AppErrorCode } from '../../../errors'
import { isValidId } from '../../../ids'
import type { LiteratureContext } from '../context'
import { paperMetadataOf, type PaperRow } from '../types'
import { hashFile } from './files'

export interface ProjectPaperQuery {
  projectId: string
  q?: string
  fileState?: 'ok' | 'missing'
  sourceState?: PaperSourceState
  hasNote?: boolean
  limit?: number
  offset?: number
}

const PAPER_COLUMNS = `id, zotero_library_id, zotero_key, attachment_key, metadata, file_path,
  file_hash, file_size, file_state, source_state, last_synced_at, imported_at, updated_at`

function paperRow(row: Record<string, unknown>): PaperRow {
  return {
    id: String(row['id']),
    zoteroLibraryId: String(row['zotero_library_id']),
    zoteroKey: String(row['zotero_key']),
    attachmentKey: row['attachment_key'] === null ? null : String(row['attachment_key']),
    metadata: String(row['metadata']),
    filePath: row['file_path'] === null ? null : String(row['file_path']),
    fileHash: row['file_hash'] === null ? null : String(row['file_hash']),
    fileSize: row['file_size'] === null ? null : Number(row['file_size']),
    fileState: row['file_state'] === 'missing' ? 'missing' : 'ok',
    sourceState:
      row['source_state'] === 'missing'
        ? 'missing'
        : row['source_state'] === 'unreadable'
          ? 'unreadable'
          : 'ok',
    lastSyncedAt: row['last_synced_at'] === null ? null : String(row['last_synced_at']),
    importedAt: String(row['imported_at']),
    updatedAt: String(row['updated_at'])
  }
}

export function getPaperRow(ctx: LiteratureContext, paperId: string): PaperRow {
  if (!isValidId(paperId)) {
    throw appError(AppErrorCode.InvalidArgument, `无效的文献 ID ${JSON.stringify(paperId)}`, {
      details: { field: 'paper_id' }
    })
  }
  const row = ctx
    .global()
    .db.prepare(`SELECT ${PAPER_COLUMNS} FROM papers WHERE id = ?`)
    .get(paperId) as Record<string, unknown> | undefined
  if (row === undefined) {
    throw appError(AppErrorCode.NotFound, `文献 ${paperId} 不存在`, {
      details: { paper_id: paperId }
    })
  }
  return paperRow(row)
}

export function findPaperByZotero(
  ctx: LiteratureContext,
  libraryId: string,
  key: string
): PaperRow | null {
  const row = ctx
    .global()
    .db.prepare(
      `SELECT ${PAPER_COLUMNS} FROM papers WHERE zotero_library_id = ? AND zotero_key = ?`
    )
    .get(libraryId === '' ? '0' : libraryId, key) as Record<string, unknown> | undefined
  return row === undefined ? null : paperRow(row)
}

export function noteIdsByPaper(
  ctx: LiteratureContext,
  projectId: string,
  paperIds: readonly string[]
): Map<string, string> {
  const out = new Map<string, string>()
  if (paperIds.length === 0) return out
  const db = ctx.workspace.project(projectId).db
  const chunk = 400
  for (let start = 0; start < paperIds.length; start += chunk) {
    const ids = paperIds.slice(start, start + chunk)
    const marks = ids.map(() => '?').join(',')
    const rows = db
      .prepare(
        `SELECT r.dst_id AS paper_id, n.id AS note_id
           FROM entity_relations r
           JOIN notes n ON n.id = r.src_id
          WHERE r.src_type = 'note' AND r.dst_type = 'paper' AND r.relation = 'references'
            AND n.kind = 'paper_note' AND n.deleted_at IS NULL
            AND r.dst_id IN (${marks})
          ORDER BY r.dst_id, n.created_at, n.id`
      )
      .all(...ids) as Array<{ paper_id: string; note_id: string }>
    for (const row of rows) {
      if (!out.has(row.paper_id)) out.set(row.paper_id, row.note_id)
    }
  }
  return out
}

export function paperView(
  ctx: LiteratureContext,
  row: PaperRow,
  options: { projectId?: string; linkedAt?: string | null; noteId?: string | null } = {}
): PaperView {
  const meta = paperMetadataOf(row)
  const broken = ctx.files.missing(row.id)
  return {
    id: row.id,
    zoteroLibraryId: row.zoteroLibraryId,
    zoteroKey: row.zoteroKey,
    attachmentKey: row.attachmentKey,
    metadata: (() => {
      try {
        const value: unknown = JSON.parse(row.metadata)
        return value !== null && typeof value === 'object' && !Array.isArray(value)
          ? (value as Record<string, unknown>)
          : {}
      } catch {
        return {}
      }
    })(),
    filePath: row.filePath,
    fileHash: row.fileHash,
    fileSize: row.fileSize,
    fileState: broken ? 'missing' : row.fileState,
    sourceState: row.sourceState,
    lastSyncedAt: row.lastSyncedAt,
    importedAt: row.importedAt,
    updatedAt: row.updatedAt,
    title: meta.title,
    authors: meta.authors,
    year: meta.year === 0 ? null : meta.year,
    doi: meta.doi === '' ? null : meta.doi,
    noteId: options.noteId ?? null,
    linkedAt: options.linkedAt ?? null,
    broken
  }
}

export function projectPapers(
  ctx: LiteratureContext,
  query: ProjectPaperQuery
): { items: ProjectPaper[]; total: number; limit: number; offset: number } {
  ctx.requireProject(query.projectId)
  const limit = Math.min(Math.max(1, Math.trunc(query.limit ?? 50)), 200)
  const offset = Math.max(0, Math.trunc(query.offset ?? 0))
  const handle = ctx.workspace.project(query.projectId)
  const links = handle.db
    .prepare(
      `SELECT paper_id, linked_at FROM project_papers
        WHERE project_id = ? ORDER BY linked_at DESC, paper_id`
    )
    .all(query.projectId) as Array<{ paper_id: string; linked_at: string }>
  const notes = noteIdsByPaper(
    ctx,
    query.projectId,
    links.map((link) => link.paper_id)
  )
  const term = query.q?.trim().toLowerCase() ?? ''
  const candidates: ProjectPaper[] = []
  for (const link of links) {
    let row: PaperRow
    try {
      row = getPaperRow(ctx, link.paper_id)
    } catch {
      continue
    }
    const view = paperView(ctx, row, {
      projectId: query.projectId,
      linkedAt: link.linked_at,
      noteId: notes.get(row.id) ?? null
    })
    if (term !== '' && !row.metadata.toLowerCase().includes(term)) continue
    if (query.fileState !== undefined && view.fileState !== query.fileState) continue
    if (query.sourceState !== undefined && row.sourceState !== query.sourceState) continue
    if (query.hasNote !== undefined && (view.noteId !== null) !== query.hasNote) continue
    candidates.push({
      ...view,
      noteId: notes.get(row.id) ?? null,
      linkedAt: link.linked_at
    })
  }
  return {
    items: candidates.slice(offset, offset + limit),
    total: candidates.length,
    limit,
    offset
  }
}

export function getPaper(
  ctx: LiteratureContext,
  paperId: string,
  projectId?: string
): PaperView {
  const row = getPaperRow(ctx, paperId)
  let linkedAt: string | null = null
  let noteId: string | null = null
  if (projectId !== undefined) {
    ctx.requireProject(projectId)
    const link = ctx
      .workspace.project(projectId)
      .db.prepare(`SELECT linked_at FROM project_papers WHERE project_id = ? AND paper_id = ?`)
      .get(projectId, paperId) as { linked_at: string } | undefined
    linkedAt = link?.linked_at ?? null
    noteId = noteIdsByPaper(ctx, projectId, [paperId]).get(paperId) ?? null
  }
  return paperView(ctx, row, {
    ...(projectId === undefined ? {} : { projectId }),
    linkedAt,
    noteId
  })
}

export function linkPaper(
  ctx: LiteratureContext,
  projectId: string,
  paperId: string,
  actor: 'user' | 'mcp' | 'ai'
): PaperView {
  ctx.requireActive(projectId)
  const row = getPaperRow(ctx, paperId)
  const handle = ctx.workspace.project(projectId)
  ctx.inTx(handle, () => {
    handle.db
      .prepare(
        `INSERT INTO project_papers(project_id, paper_id, linked_at, linked_by)
         VALUES (?, ?, ?, ?) ON CONFLICT(project_id, paper_id) DO NOTHING`
      )
      .run(projectId, paperId, ctx.now(), actor === 'mcp' ? 'mcp' : 'user')
  })
  const link = handle.db
    .prepare(`SELECT linked_at FROM project_papers WHERE project_id = ? AND paper_id = ?`)
    .get(projectId, paperId) as { linked_at: string } | undefined
  return paperView(ctx, row, {
    projectId,
    linkedAt: link?.linked_at ?? null,
    noteId: noteIdsByPaper(ctx, projectId, [paperId]).get(paperId) ?? null
  })
}

export function unlinkPaper(
  ctx: LiteratureContext,
  projectId: string,
  paperId: string
): { unlinked: boolean; detachedNoteIds: string[] } {
  ctx.requireActive(projectId)
  getPaperRow(ctx, paperId)
  const handle = ctx.workspace.project(projectId)
  const noteIds = [...noteIdsByPaper(ctx, projectId, [paperId]).values()]
  const removed = ctx.inTx(handle, () =>
    handle.db
      .prepare(`DELETE FROM project_papers WHERE project_id = ? AND paper_id = ?`)
      .run(projectId, paperId)
  ).changes
  return { unlinked: removed > 0, detachedNoteIds: noteIds }
}

export function reconcilePaper(ctx: LiteratureContext, paperId: string) {
  const row = getPaperRow(ctx, paperId)
  let fileExists = false
  let actualHash: string | null = null
  let hashMatches = false
  try {
    const target = ctx.files.stat(paperId)
    fileExists = true
    actualHash = hashFile(target.absolutePath)
    hashMatches = row.fileHash !== null && actualHash === row.fileHash
  } catch {
    fileExists = false
  }
  return {
    paperId,
    fileExists,
    hashMatches,
    storedHash: row.fileHash,
    actualHash,
    sourceState: row.sourceState
  }
}

export function listPaperProjects(
  ctx: LiteratureContext,
  paperId: string
): Array<{ projectId: string; name: string; status: string }> {
  getPaperRow(ctx, paperId)
  const out: Array<{ projectId: string; name: string; status: string }> = []
  const projects = ctx
    .global()
    .db.prepare(`SELECT id, name, status FROM projects ORDER BY lower(name), id`)
    .all() as Array<{ id: string; name: string; status: string }>
  for (const project of projects) {
    try {
      const link = ctx.workspace
        .project(project.id)
        .db.prepare(`SELECT 1 AS one FROM project_papers WHERE project_id = ? AND paper_id = ?`)
        .get(project.id, paperId) as { one: number } | undefined
      if (link !== undefined) {
        out.push({ projectId: project.id, name: project.name, status: project.status })
      }
    } catch {
      continue
    }
  }
  return out
}

/** Global listing used by MCP `paper_list` when no project is given. */
export function listGlobalPapers(
  ctx: LiteratureContext,
  query: { q?: string; importedAfter?: string; limit?: number; cursor?: string | null }
): { items: PaperView[]; nextCursor: string | null } {
  const limit = Math.min(Math.max(1, Math.trunc(query.limit ?? 50)), 200)
  const offset = parseCursor(query.cursor ?? null)
  const conditions: string[] = []
  const args: unknown[] = []
  if (query.q !== undefined && query.q.trim() !== '') {
    conditions.push('instr(lower(metadata), lower(?)) > 0')
    args.push(query.q.trim())
  }
  if (query.importedAfter !== undefined && query.importedAfter !== '') {
    conditions.push('imported_at >= ?')
    args.push(query.importedAfter)
  }
  const where = conditions.length === 0 ? '' : ` WHERE ${conditions.join(' AND ')}`
  const rows = ctx
    .global()
    .db.prepare(
      `SELECT ${PAPER_COLUMNS} FROM papers${where} ORDER BY imported_at DESC, id LIMIT ? OFFSET ?`
    )
    .all(...args, limit + 1, offset) as Array<Record<string, unknown>>
  const hasMore = rows.length > limit
  const page = hasMore ? rows.slice(0, limit) : rows
  return {
    items: page.map((raw) => paperView(ctx, paperRow(raw))),
    nextCursor: hasMore ? encodeCursor(offset + limit) : null
  }
}

function parseCursor(cursor: string | null): number {
  if (cursor === null || cursor === '') return 0
  const parsed = Number.parseInt(cursor, 10)
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : 0
}

function encodeCursor(offset: number): string {
  return String(offset)
}