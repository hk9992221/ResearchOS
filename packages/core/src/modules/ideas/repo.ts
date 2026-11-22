/**
 * Row mapping and polymorphic target resolution for the Ideas module. All SQL
 * here is read-only projection; writes live in the service so every mutation
 * is visible in one file with its transaction boundary.
 */
import type { Evidence, EvidenceSourceType, Idea } from '@researchos/shared'

import type { Db } from '../../db/open'
import { formatTimestamp, parseTimestamp } from '../../clock'
import {
  IDEA_ERROR,
  evidenceDeleted,
  evidenceNotFound,
  ideaDeleted,
  ideaNotFound,
  invalidInput,
  ideaError
} from './errors'
import { AppErrorCode } from '../../errors'
import { runTitle } from './text'

export interface IdeaRow {
  id: string
  title: string
  description: string
  status: Idea['status']
  rejectionReason: string
  createdAt: Date
  updatedAt: Date
  /** Exact stored text, for optimistic concurrency. */
  updatedAtText: string
  deletedAt: Date | null
  deletedAtText: string | null
}

export interface EvidenceRow {
  id: string
  ideaId: string
  direction: Evidence['direction']
  content: string
  sourceType: EvidenceSourceType
  sourceId: string
  aiGenerated: boolean
  confirmed: boolean
  aiOutputId: string | null
  provenance: Evidence['provenance']
  contentHash: string
  createdAt: Date
  updatedAt: Date
  updatedAtText: string
  deletedAt: Date | null
}

export interface RawIdeaRow {
  id: string
  title: string
  description: string
  status: string
  rejection_reason: string
  created_at: string
  updated_at: string
  deleted_at: string | null
}

export interface RawEvidenceRow {
  id: string
  idea_id: string
  direction: string
  content: string
  source_type: string
  source_id: string
  ai_generated: number
  confirmed: number
  ai_output_id: string | null
  provenance: string
  content_hash: string
  created_at: string
  updated_at: string
  deleted_at: string | null
}

export const IDEA_COLUMNS = 'id, title, description, status, rejection_reason, created_at, updated_at, deleted_at'

export const EVIDENCE_COLUMNS =
  'id, idea_id, direction, content, source_type, source_id, ai_generated, confirmed, ai_output_id, provenance, content_hash, created_at, updated_at, deleted_at'

function parseProvenance(raw: string): Evidence['provenance'] {
  if (raw === '' || raw === '{}' || raw === 'null') return null
  try {
    return JSON.parse(raw) as Record<string, unknown>
  } catch {
    return null
  }
}

export function mapIdeaRow(row: RawIdeaRow): IdeaRow {
  return {
    id: row.id,
    title: row.title,
    description: row.description,
    status: row.status as Idea['status'],
    rejectionReason: row.rejection_reason,
    createdAt: parseTimestamp(row.created_at),
    updatedAt: parseTimestamp(row.updated_at),
    updatedAtText: row.updated_at,
    deletedAt: row.deleted_at === null ? null : parseTimestamp(row.deleted_at),
    deletedAtText: row.deleted_at
  }
}

export function mapEvidenceRow(row: RawEvidenceRow): EvidenceRow {
  return {
    id: row.id,
    ideaId: row.idea_id,
    direction: row.direction as Evidence['direction'],
    content: row.content,
    sourceType: row.source_type as EvidenceSourceType,
    sourceId: row.source_id,
    aiGenerated: row.ai_generated === 1,
    confirmed: row.confirmed === 1,
    aiOutputId: row.ai_output_id,
    provenance: parseProvenance(row.provenance),
    contentHash: row.content_hash,
    createdAt: parseTimestamp(row.created_at),
    updatedAt: parseTimestamp(row.updated_at),
    updatedAtText: row.updated_at,
    deletedAt: row.deleted_at === null ? null : parseTimestamp(row.deleted_at)
  }
}

export function toIdea(row: IdeaRow): Idea {
  return {
    id: row.id,
    title: row.title,
    description: row.description,
    status: row.status,
    rejectionReason: row.rejectionReason === '' ? null : row.rejectionReason,
    createdAt: formatTimestamp(row.createdAt),
    updatedAt: formatTimestamp(row.updatedAt),
    deletedAt: row.deletedAt === null ? null : formatTimestamp(row.deletedAt)
  }
}

export function toEvidence(row: EvidenceRow): Evidence {
  return {
    id: row.id,
    ideaId: row.ideaId,
    direction: row.direction,
    content: row.content,
    sourceType: row.sourceType,
    sourceId: row.sourceId,
    aiGenerated: row.aiGenerated,
    confirmed: row.confirmed,
    aiOutputId: row.aiOutputId,
    provenance: row.provenance,
    contentHash: row.contentHash,
    createdAt: formatTimestamp(row.createdAt),
    updatedAt: formatTimestamp(row.updatedAt),
    deletedAt: row.deletedAt === null ? null : formatTimestamp(row.deletedAt)
  }
}

export function readIdea(db: Db, id: string, includeDeleted: boolean): IdeaRow | undefined {
  const sql =
    `SELECT ${IDEA_COLUMNS} FROM ideas WHERE id = ?` + (includeDeleted ? '' : ' AND deleted_at IS NULL')
  const row = db.prepare(sql).get(id) as RawIdeaRow | undefined
  return row === undefined ? undefined : mapIdeaRow(row)
}

/**
 * Reads one Idea or throws. A soft-deleted row yields `idea.deleted` (not
 * `idea.not_found`) unless `includeDeleted` is set — AC-20.
 */
export function requireIdea(db: Db, id: string, includeDeleted = false): IdeaRow {
  const row = readIdea(db, id, true)
  if (row === undefined) throw ideaNotFound(`idea ${id} not found`, { idea_id: id })
  if (row.deletedAt !== null && !includeDeleted) throw ideaDeleted(`idea ${id} is deleted`, { idea_id: id })
  return row
}

export function readEvidence(db: Db, id: string): EvidenceRow | undefined {
  const row = db.prepare(`SELECT ${EVIDENCE_COLUMNS} FROM evidence WHERE id = ?`).get(id) as
    | RawEvidenceRow
    | undefined
  return row === undefined ? undefined : mapEvidenceRow(row)
}

export function requireEvidence(db: Db, id: string, includeDeleted = false): EvidenceRow {
  const row = readEvidence(db, id)
  if (row === undefined) throw evidenceNotFound(`evidence ${id} not found`, { evidence_id: id })
  if (row.deletedAt !== null && !includeDeleted) {
    throw evidenceDeleted(`evidence ${id} is deleted`, { evidence_id: id })
  }
  return row
}

export interface EvidenceCounts {
  support: number
  oppose: number
  drafts: number
}

export function evidenceCounts(db: Db, ideaId: string): EvidenceCounts {
  const row = db
    .prepare(
      `SELECT
         COALESCE(sum(CASE WHEN direction = 'support' AND confirmed = 1 AND deleted_at IS NULL THEN 1 ELSE 0 END), 0) AS support,
         COALESCE(sum(CASE WHEN direction = 'oppose'  AND confirmed = 1 AND deleted_at IS NULL THEN 1 ELSE 0 END), 0) AS oppose,
         COALESCE(sum(CASE WHEN ai_generated = 1 AND confirmed = 0 AND deleted_at IS NULL THEN 1 ELSE 0 END), 0) AS drafts
       FROM evidence WHERE idea_id = ?`
    )
    .get(ideaId) as { support: number; oppose: number; drafts: number } | undefined
  return { support: row?.support ?? 0, oppose: row?.oppose ?? 0, drafts: row?.drafts ?? 0 }
}

/* ------------------------------------------------------- target resolution */

export interface TargetInfo {
  type: string
  id: string
  title: string
  deleted: boolean
  broken: boolean
}

function paperTitle(globalDb: Db, paperId: string): { title: string; found: boolean } {
  const row = globalDb
    .prepare("SELECT COALESCE(json_extract(metadata, '$.title'), '') AS title FROM papers WHERE id = ?")
    .get(paperId) as { title: string } | undefined
  return row === undefined ? { title: '', found: false } : { title: row.title, found: true }
}

const SOURCE_TYPES: readonly EvidenceSourceType[] = ['experiment', 'run', 'note', 'paper']

export function isEvidenceSourceType(value: unknown): value is EvidenceSourceType {
  return typeof value === 'string' && (SOURCE_TYPES as readonly string[]).includes(value)
}

/**
 * Resolves one polymorphic target. A missing target never throws: `broken`
 * reports it so read paths can render “target deleted” instead of hiding rows.
 */
export function resolveTarget(
  db: Db,
  globalDb: Db,
  projectId: string,
  type: string,
  id: string
): TargetInfo {
  const info: TargetInfo = { type, id, title: '', deleted: false, broken: false }
  switch (type) {
    case 'paper': {
      const paper = paperTitle(globalDb, id)
      if (!paper.found) {
        info.broken = true
        return info
      }
      const linked = db
        .prepare('SELECT count(*) AS n FROM project_papers WHERE project_id = ? AND paper_id = ?')
        .get(projectId, id) as { n: number } | undefined
      info.title = paper.title
      info.broken = (linked?.n ?? 0) === 0
      return info
    }
    case 'note': {
      const row = db.prepare('SELECT title, deleted_at FROM notes WHERE id = ?').get(id) as
        | { title: string; deleted_at: string | null }
        | undefined
      if (row === undefined) {
        info.broken = true
        return info
      }
      info.title = row.title
      info.deleted = row.deleted_at !== null
      return info
    }
    case 'experiment': {
      const row = db.prepare('SELECT title, deleted_at FROM experiments WHERE id = ?').get(id) as
        | { title: string; deleted_at: string | null }
        | undefined
      if (row === undefined) {
        info.broken = true
        return info
      }
      info.title = row.title
      info.deleted = row.deleted_at !== null
      return info
    }
    case 'run': {
      const row = db
        .prepare(
          `SELECT e.title AS experiment_title, r.deleted_at AS run_deleted, e.deleted_at AS experiment_deleted
             FROM runs r JOIN experiments e ON e.id = r.experiment_id
            WHERE r.id = ?`
        )
        .get(id) as
        | { experiment_title: string; run_deleted: string | null; experiment_deleted: string | null }
        | undefined
      if (row === undefined) {
        info.broken = true
        return info
      }
      info.title = runTitle(row.experiment_title, id)
      info.deleted = row.run_deleted !== null || row.experiment_deleted !== null
      return info
    }
    case 'idea': {
      const row = db.prepare('SELECT title, deleted_at FROM ideas WHERE id = ?').get(id) as
        | { title: string; deleted_at: string | null }
        | undefined
      if (row === undefined) {
        info.broken = true
        return info
      }
      info.title = row.title
      info.deleted = row.deleted_at !== null
      return info
    }
    default:
      throw invalidInput(`source type ${JSON.stringify(type)} is not supported`, { source_type: type })
  }
}

/** Requires a live, project-local evidence target and returns its title. */
export function validateEvidenceSource(
  db: Db,
  globalDb: Db,
  projectId: string,
  type: string,
  id: string
): string {
  if (!isEvidenceSourceType(type)) {
    throw ideaError(
      IDEA_ERROR.invalidSource,
      AppErrorCode.InvalidArgument,
      `source type ${JSON.stringify(type)} is not registered`,
      { source_type: type }
    )
  }
  const info = resolveTarget(db, globalDb, projectId, type, id)
  if (info.broken) {
    throw ideaError(
      IDEA_ERROR.invalidSource,
      AppErrorCode.InvalidArgument,
      `source ${type} ${id} does not exist or is not linked to the project`,
      { source_type: type, source_id: id }
    )
  }
  if (info.deleted) {
    throw ideaError(
      IDEA_ERROR.invalidSource,
      AppErrorCode.InvalidArgument,
      `source ${type} ${id} is deleted`,
      { source_type: type, source_id: id }
    )
  }
  return info.title
}