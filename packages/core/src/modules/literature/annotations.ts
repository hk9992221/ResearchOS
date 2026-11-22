/**
 * PDF annotations (new in the rewrite, plan §4.3). `paper_annotations` stores a
 * page-precise jump target: page number, normalized rects, quote, color and
 * optional links to a Paper Note / Idea Evidence. Annotation bodies are not
 * note bodies; no Markdown is involved.
 */
import type { Locator, PaperAnnotation, Rect } from '@researchos/shared'

import type { EntityType } from '../../schema'
import { appError, AppErrorCode } from '../../errors'
import { isValidId, newId } from '../../ids'
import type { LiteratureContext } from './context'
import { getPaperRow } from './library/query'

interface AnnotationRow {
  id: string
  paper_id: string
  project_id: string
  note_id: string | null
  evidence_id: string | null
  page_no: number
  rects: string
  quote: string
  color: string | null
  locator: string | null
  created_at: string
  updated_at: string
}

const COLUMNS = `id, paper_id, project_id, note_id, evidence_id, page_no, rects, quote,
  color, locator, created_at, updated_at`

export interface AnnotationPatch {
  quote?: string
  color?: string | null
  noteId?: string | null
  evidenceId?: string | null
}

function parseRects(raw: string): Rect[] {
  try {
    const value: unknown = JSON.parse(raw)
    if (!Array.isArray(value)) return []
    const out: Rect[] = []
    for (const entry of value) {
      if (entry === null || typeof entry !== 'object') continue
      const object: Record<string, unknown> = entry as Record<string, unknown>
      const x = object['x']
      const y = object['y']
      const width = object['width']
      const height = object['height']
      if (
        typeof x === 'number' &&
        typeof y === 'number' &&
        typeof width === 'number' &&
        typeof height === 'number'
      ) {
        out.push({ x, y, width, height })
      }
    }
    return out
  } catch {
    return []
  }
}

function parseLocator(raw: string | null): Locator | null {
  if (raw === null || raw.trim() === '') return null
  try {
    const value: unknown = JSON.parse(raw)
    if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
    // Persisted locator JSON; the columns are written by this service only.
    const locator: Locator = value as Locator
    return locator
  } catch {
    return null
  }
}

function toAnnotation(row: AnnotationRow): PaperAnnotation {
  return {
    id: row.id,
    paperId: row.paper_id,
    projectId: row.project_id,
    noteId: row.note_id,
    evidenceId: row.evidence_id,
    pageNo: row.page_no,
    rects: parseRects(row.rects),
    quote: row.quote,
    color: row.color,
    locator: parseLocator(row.locator),
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function requireAnnotation(ctx: LiteratureContext, projectId: string, id: string): AnnotationRow {
  const row = ctx.workspace
    .project(projectId)
    .db.prepare(`SELECT ${COLUMNS} FROM paper_annotations WHERE id = ? AND project_id = ?`)
    .get(id, projectId) as AnnotationRow | undefined
  if (row === undefined) {
    throw appError(AppErrorCode.NotFound, `批注 ${id} 不存在`, { details: { annotation_id: id } })
  }
  return row
}

function validateLink(ctx: LiteratureContext, projectId: string, entityType: EntityType, id: string): void {
  const db = ctx.workspace.project(projectId).db
  if (entityType === 'note') {
    const row = db.prepare(`SELECT deleted_at FROM notes WHERE id = ?`).get(id) as
      | { deleted_at: string | null }
      | undefined
    if (row === undefined) {
      throw appError(AppErrorCode.NotFound, `笔记 ${id} 不存在`, { details: { note_id: id } })
    }
    if (row.deleted_at !== null) {
      throw appError(AppErrorCode.EntityDeleted, `笔记 ${id} 已删除`, { details: { note_id: id } })
    }
    return
  }
  const row = db.prepare(`SELECT deleted_at FROM evidence WHERE id = ?`).get(id) as
    | { deleted_at: string | null }
    | undefined
  if (row === undefined) {
    throw appError(AppErrorCode.NotFound, `证据 ${id} 不存在`, { details: { evidence_id: id } })
  }
  if (row.deleted_at !== null) {
    throw appError(AppErrorCode.EntityDeleted, `证据 ${id} 已删除`, { details: { evidence_id: id } })
  }
}

export interface CreateAnnotationInput {
  pageNo: number
  rects: Rect[]
  quote: string
  color?: string | null
  noteId?: string | null
  evidenceId?: string | null
  locator?: Locator | null
}

export function createAnnotation(
  ctx: LiteratureContext,
  projectId: string,
  paperId: string,
  input: CreateAnnotationInput
): PaperAnnotation {
  ctx.requireActive(projectId)
  getPaperRow(ctx, paperId)
  const link = ctx.workspace
    .project(projectId)
    .db.prepare(`SELECT 1 AS one FROM project_papers WHERE project_id = ? AND paper_id = ?`)
    .get(projectId, paperId) as { one: number } | undefined
  if (link === undefined) {
    throw appError(AppErrorCode.Conflict, '论文尚未关联到该项目', { details: { paper_id: paperId } })
  }
  if (!Number.isSafeInteger(input.pageNo) || input.pageNo < 1) {
    throw appError(AppErrorCode.InvalidArgument, 'page_no 必须是从 1 开始的整数', {
      details: { field: 'page_no' }
    })
  }
  if (input.quote.length > 20_000) {
    throw appError(AppErrorCode.LimitExceeded, '引用文本过长（上限 20000 字符）')
  }
  if (input.noteId !== undefined && input.noteId !== null) {
    validateLink(ctx, projectId, 'note', input.noteId)
  }
  if (input.evidenceId !== undefined && input.evidenceId !== null) {
    validateLink(ctx, projectId, 'evidence', input.evidenceId)
  }
  const id = newId(ctx.clock)
  const now = ctx.now()
  const locator: Locator = { ...(input.locator ?? {}), pageNo: input.pageNo, rects: input.rects }
  const handle = ctx.workspace.project(projectId)
  ctx.inTx(handle, () => {
    handle.db
      .prepare(
        `INSERT INTO paper_annotations(id, paper_id, project_id, note_id, evidence_id, page_no,
           rects, quote, color, locator, created_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
      )
      .run(
        id,
        paperId,
        projectId,
        input.noteId ?? null,
        input.evidenceId ?? null,
        input.pageNo,
        JSON.stringify(input.rects),
        input.quote,
        input.color ?? null,
        JSON.stringify(locator),
        now,
        now
      )
  })
  return toAnnotation(requireAnnotation(ctx, projectId, id))
}

export function listAnnotations(
  ctx: LiteratureContext,
  projectId: string,
  paperId: string
): PaperAnnotation[] {
  ctx.requireProject(projectId)
  const rows = ctx.workspace
    .project(projectId)
    .db.prepare(
      `SELECT ${COLUMNS} FROM paper_annotations
        WHERE project_id = ? AND paper_id = ?
        ORDER BY page_no, created_at, id`
    )
    .all(projectId, paperId) as AnnotationRow[]
  return rows.map(toAnnotation)
}

export function updateAnnotation(
  ctx: LiteratureContext,
  projectId: string,
  annotationId: string,
  patch: AnnotationPatch
): PaperAnnotation {
  ctx.requireActive(projectId)
  const current = requireAnnotation(ctx, projectId, annotationId)
  if (patch.noteId !== undefined && patch.noteId !== null) {
    validateLink(ctx, projectId, 'note', patch.noteId)
  }
  if (patch.evidenceId !== undefined && patch.evidenceId !== null) {
    validateLink(ctx, projectId, 'evidence', patch.evidenceId)
  }
  if (patch.quote !== undefined && patch.quote.length > 20_000) {
    throw appError(AppErrorCode.LimitExceeded, '引用文本过长（上限 20000 字符）')
  }
  const handle = ctx.workspace.project(projectId)
  ctx.inTx(handle, () => {
    handle.db
      .prepare(
        `UPDATE paper_annotations
            SET quote = ?, color = ?, note_id = ?, evidence_id = ?, updated_at = ?
          WHERE id = ? AND project_id = ?`
      )
      .run(
        patch.quote ?? current.quote,
        patch.color === undefined ? current.color : patch.color,
        patch.noteId === undefined ? current.note_id : patch.noteId,
        patch.evidenceId === undefined ? current.evidence_id : patch.evidenceId,
        ctx.now(),
        annotationId,
        projectId
      )
  })
  return toAnnotation(requireAnnotation(ctx, projectId, annotationId))
}

export function deleteAnnotation(
  ctx: LiteratureContext,
  projectId: string,
  annotationId: string
): { deleted: boolean } {
  ctx.requireActive(projectId)
  requireAnnotation(ctx, projectId, annotationId)
  const handle = ctx.workspace.project(projectId)
  const changed = ctx.inTx(handle, () =>
    handle.db
      .prepare(`DELETE FROM paper_annotations WHERE id = ? AND project_id = ?`)
      .run(annotationId, projectId)
  ).changes
  return { deleted: changed > 0 }
}