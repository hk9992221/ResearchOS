import { AppErrorCode, appError } from '../../errors'
import type { SearchEngine } from './engine'
import type { SearchTarget } from './filters'
import { isEntityType, type EntityRef, type EntityType, type JumpTarget, type Locator, type SourceType } from './types'

interface TargetInput {
  entityType: EntityType
  entityId: string
  projectId: string
  source: SourceType
  locator: Locator
}

/** Resolves the navigation target of a search hit; read-only, writes no Activity. */
export async function jumpTargetFor(
  engine: SearchEngine,
  target: SearchTarget,
  input: TargetInput
): Promise<JumpTarget> {
  const resolved: JumpTarget = {
    kind: input.entityType,
    projectId: input.projectId,
    entityId: input.entityId,
    locator: input.locator,
    paperId: '',
    pdfPath: '',
    source: input.source
  }
  if (input.entityType === 'paper') {
    resolved.paperId = input.entityId
    resolved.pdfPath = paperPdfPath(engine, input.entityId)
    return resolved
  }
  if (input.entityType === 'note' && input.source === 'paper_note') {
    const note = target.db.prepare(`SELECT paper_id FROM notes WHERE id = ?`).get(input.entityId) as
      | { paper_id: string | null }
      | undefined
    if (note !== undefined && note.paper_id !== null && note.paper_id !== '') {
      resolved.paperId = note.paper_id
      resolved.pdfPath = paperPdfPath(engine, note.paper_id)
    }
  }
  return resolved
}

/** Absolute path of one paper's local PDF copy; empty when it has none. */
export function paperPdfPath(engine: SearchEngine, paperId: string): string {
  if (paperId === '') return ''
  const row = engine.workspace.global.db
    .prepare(`SELECT file_path FROM papers WHERE id = ?`)
    .get(paperId) as { file_path: string | null } | undefined
  if (row === undefined || row.file_path === null || row.file_path === '') return ''
  try {
    return engine.workspace.paths.abs(row.file_path)
  } catch {
    return ''
  }
}

/** Resolves an arbitrary entity reference plus locator into a jump target. */
export async function resolveJumpTarget(
  engine: SearchEngine,
  ref: EntityRef,
  locator: Locator
): Promise<JumpTarget> {
  if (!isEntityType(ref.entityType)) {
    throw appError(AppErrorCode.InvalidArgument, `unknown entity type ${JSON.stringify(ref.entityType)}`)
  }
  if (ref.entityId === '') throw appError(AppErrorCode.InvalidArgument, 'entity id is required')

  if (ref.entityType === 'paper') {
    const exists = engine.workspace.global.db
      .prepare(`SELECT 1 AS one FROM papers WHERE id = ?`)
      .get(ref.entityId)
    if (exists === undefined) {
      throw appError(AppErrorCode.NotFound, 'paper does not exist', { details: { entityId: ref.entityId } })
    }
    return {
      kind: 'paper',
      projectId: '',
      entityId: ref.entityId,
      locator,
      paperId: ref.entityId,
      pdfPath: paperPdfPath(engine, ref.entityId),
      source: 'paper_metadata'
    }
  }

  if (ref.projectId === '') {
    throw appError(AppErrorCode.InvalidArgument, 'project entities require projectId')
  }
  const db = engine.workspace.project(ref.projectId).db
  let paperId = ''
  if (ref.entityType === 'note') {
    const row = db.prepare(`SELECT paper_id FROM notes WHERE id = ? AND deleted_at IS NULL`).get(ref.entityId) as
      | { paper_id: string | null }
      | undefined
    if (row === undefined) throw targetNotFound('note', ref.entityId)
    paperId = row.paper_id ?? ''
  } else {
    const table =
      ref.entityType === 'db_record' ? 'db_records' : ref.entityType === 'idea' ? 'ideas' : 'experiments'
    const row = db.prepare(`SELECT 1 AS one FROM ${table} WHERE id = ? AND deleted_at IS NULL`).get(ref.entityId)
    if (row === undefined) throw targetNotFound(ref.entityType, ref.entityId)
  }
  return {
    kind: ref.entityType,
    projectId: ref.projectId,
    entityId: ref.entityId,
    locator,
    paperId,
    pdfPath: paperId === '' ? '' : paperPdfPath(engine, paperId),
    source: ''
  }
}

function targetNotFound(entityType: string, entityId: string): Error {
  return appError(AppErrorCode.NotFound, `${entityType} does not exist`, { details: { entityId } })
}

/**
 * Performs the navigation side effect: a PDF opens through the injected
 * opener (the desktop shell may deep-link to a page); everything else needs an
 * in-app navigator. Never writes Activity and never changes data.
 */
export async function openJumpTarget(engine: SearchEngine, target: JumpTarget): Promise<void> {
  if (target.kind === 'paper' || target.kind === 'note') {
    if (target.pdfPath !== '') {
      if (engine.cfg.opener === undefined) {
        throw appError(AppErrorCode.Unsupported, 'no PDF opener is configured', {
          details: { reason: 'open_unsupported', pdfPath: target.pdfPath }
        })
      }
      try {
        await engine.cfg.opener(target.pdfPath)
        return
      } catch (cause) {
        throw appError(AppErrorCode.ExternalUnavailable, 'opening the PDF failed', { cause })
      }
    }
    if (target.kind === 'paper') {
      throw appError(AppErrorCode.Unsupported, 'no PDF copy exists for this paper', {
        details: { reason: 'open_unsupported', entityId: target.entityId }
      })
    }
  }
  if (engine.cfg.navigator !== undefined) {
    try {
      await engine.cfg.navigator(target)
      return
    } catch (cause) {
      throw appError(AppErrorCode.Internal, 'in-app navigation failed', { cause })
    }
  }
  throw appError(
    AppErrorCode.Unsupported,
    `navigation to ${target.kind} is not available in this environment`,
    { details: { reason: 'open_unsupported', locator: target.locator } }
  )
}
