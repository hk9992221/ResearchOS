/**
 * Literature Overview: one live `kind='overview'` note per
 * project, created on first save. The Markdown body is owned by the notes
 * service; `ros://` references are resolved read-only for the UI.
 */
import type {
  LiteratureOverview,
  OverviewComparisonRow,
  ReferenceResolution
} from '@researchos/shared'
import { EMPTY_PAPER_NOTE_FIELDS } from '@researchos/shared'

import type { Actor } from '../../../schema'
import { appError, AppErrorCode } from '../../../errors'
import { isValidId } from '../../../ids'
import type { LiteratureContext } from '../context'
import { paperMetadataOf } from '../types'
import { decodeFields, findPaperNoteRow, NOTE_COLUMNS, noteToDto, type NoteRowLike } from './papernote'
import { getPaperRow } from './query'

const OVERVIEW_TITLE = '文献综述'
const ROS_REF = /ros:\/\/(note|paper|idea)\/([0-9a-f-]{36})/g

function overviewRow(ctx: LiteratureContext, projectId: string): NoteRowLike | null {
  const row = ctx.workspace
    .project(projectId)
    .db.prepare(
      `SELECT ${NOTE_COLUMNS} FROM notes
        WHERE kind = 'overview' AND deleted_at IS NULL
        ORDER BY created_at, id LIMIT 1`
    )
    .get() as NoteRowLike | undefined
  return row ?? null
}

export function resolveReferences(
  ctx: LiteratureContext,
  projectId: string,
  markdown: string
): ReferenceResolution[] {
  const matches = [...markdown.matchAll(ROS_REF)]
  if (matches.length === 0) return []
  const handle = ctx.workspace.project(projectId)
  const out: ReferenceResolution[] = []
  for (const match of matches) {
    const raw = match[0] ?? ''
    const entityType = match[1] ?? ''
    const entityId = match[2] ?? ''
    let title = ''
    let exists = isValidId(entityId)
    if (exists && entityType === 'note') {
      const row = handle.db.prepare(`SELECT title FROM notes WHERE id = ?`).get(entityId) as
        | { title: string }
        | undefined
      title = row?.title ?? ''
      exists = row !== undefined
    } else if (exists && entityType === 'idea') {
      const row = handle.db.prepare(`SELECT title FROM ideas WHERE id = ?`).get(entityId) as
        | { title: string }
        | undefined
      title = row?.title ?? ''
      exists = row !== undefined
    } else if (exists && entityType === 'paper') {
      try {
        title = paperMetadataOf(getPaperRow(ctx, entityId)).title
      } catch {
        exists = false
      }
    }
    out.push({ raw, entityType, entityId, title, exists })
  }
  return out
}

export function getOverview(ctx: LiteratureContext, projectId: string): LiteratureOverview | null {
  ctx.requireProject(projectId)
  const row = overviewRow(ctx, projectId)
  if (row === null) return null
  let markdown = ''
  let bodyHash: string | null = row.file_hash
  try {
    const body = ctx.notesFor(projectId).loadBody(row.id)
    markdown = body.markdown
    bodyHash = body.hash === '' ? bodyHash : body.hash
  } catch {
    // A missing body is an empty overview; the next save recreates the file.
  }
  const refs = resolveReferences(ctx, projectId, markdown)
  return {
    note: noteToDto(row),
    markdown,
    bodyHash,
    referencedNoteIds: refs
      .filter((ref) => ref.entityType === 'note' && ref.exists)
      .map((ref) => ref.entityId)
  }
}

export function saveOverview(
  ctx: LiteratureContext,
  projectId: string,
  markdown: string,
  baseHash: string | undefined,
  actor: Actor
): { fileHash: string; savedAt: string; overview: LiteratureOverview } {
  ctx.requireActive(projectId)
  const row = overviewRow(ctx, projectId)
  let saved: { fileHash: string; savedAt: string }
  if (row === null) {
    if (baseHash !== undefined && baseHash !== '') {
      throw appError(AppErrorCode.Conflict, '文献综述尚未创建，基准哈希已过期', {
        details: { disk_hash: '' }
      })
    }
    const created = ctx.notesFor(projectId).createNote({
      kind: 'overview',
      title: OVERVIEW_TITLE,
      bodyMd: markdown,
      actor
    })
    const body = ctx.notesFor(projectId).loadBody(created.id)
    saved = { fileHash: body.hash, savedAt: created.updatedAt }
  } else {
    saved = ctx.notesFor(projectId).saveBody({
      noteId: row.id,
      markdown,
      baseHash: baseHash ?? row.file_hash ?? '',
      actor
    })
  }
  const overview = getOverview(ctx, projectId)
  if (overview === null) {
    throw appError(AppErrorCode.Internal, '文献综述保存后读取失败')
  }
  return { fileHash: saved.fileHash, savedAt: saved.savedAt, overview }
}

export function comparison(
  ctx: LiteratureContext,
  projectId: string,
  paperIds?: readonly string[]
): OverviewComparisonRow[] {
  ctx.requireProject(projectId)
  const linked = ctx.workspace
    .project(projectId)
    .db.prepare(`SELECT paper_id FROM project_papers WHERE project_id = ? ORDER BY linked_at, paper_id`)
    .all(projectId) as Array<{ paper_id: string }>
  const wanted = paperIds === undefined || paperIds.length === 0 ? null : new Set(paperIds)
  const out: OverviewComparisonRow[] = []
  for (const link of linked) {
    if (wanted !== null && !wanted.has(link.paper_id)) continue
    let paper
    try {
      paper = getPaperRow(ctx, link.paper_id)
    } catch {
      continue
    }
    const meta = paperMetadataOf(paper)
    const note = findPaperNoteRow(ctx, projectId, link.paper_id)
    out.push({
      paperId: link.paper_id,
      title: meta.title,
      year: meta.year === 0 ? null : meta.year,
      venue: meta.publicationTitle,
      authors: meta.authors,
      sourceState: paper.sourceState,
      noteId: note !== null && note.deleted_at === null ? note.id : null,
      fields: note === null ? { ...EMPTY_PAPER_NOTE_FIELDS } : decodeFields(note.meta_json)
    })
  }
  return out
}