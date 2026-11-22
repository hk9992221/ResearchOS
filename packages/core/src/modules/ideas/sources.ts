/**
 * Default source resolver for the extraction skill. It reads
 * experiment/run analysis and note bodies from the project database and the
 * workspace note files; a missing or empty source is a typed failure, never a
 * silent empty string.
 */
import { readFileSync } from 'node:fs'

import type { EvidenceSourceType } from '@researchos/shared'

import { AppErrorCode } from '../../errors'
import { IDEA_ERROR, ideaError } from './errors'
import type { Db } from '../../db/open'
import { runTitle } from './text'
import type { IdeaSourceResolver, IdeasWorkspace, ResolvedIdeaSource } from './types'

interface SourceRow {
  title: string
  purpose: string
  updated_at: string
  deleted_at: string | null
}

function sourceEmpty(type: string, id: string): never {
  throw ideaError(
    IDEA_ERROR.sourceEmpty,
    AppErrorCode.InvalidArgument,
    `source ${type}/${id} has no content`,
    { source_type: type, source_id: id }
  )
}

function latestConfirmedResults(db: Db, targetType: string, targetId: string): string {
  const row = db
    .prepare(
      `SELECT o.content_md AS content
         FROM snapshots s
         JOIN ai_outputs o ON o.id = s.ai_output_id
        WHERE s.target_type = ? AND s.target_id = ?
          AND o.status = 'confirmed' AND o.task_kind = 'experiment_results'
        ORDER BY s.created_at DESC, s.id DESC LIMIT 1`
    )
    .get(targetType, targetId) as { content: string } | undefined
  return row?.content ?? ''
}

export class DatabaseSourceResolver implements IdeaSourceResolver {
  private readonly workspace: IdeasWorkspace

  constructor(workspace: IdeasWorkspace) {
    this.workspace = workspace
  }

  async resolve(
    projectId: string,
    ref: { type: EvidenceSourceType; id: string }
  ): Promise<ResolvedIdeaSource> {
    const handle = this.workspace.project(projectId)
    switch (ref.type) {
      case 'experiment':
        return this.resolveExperiment(handle.db, ref)
      case 'run':
        return this.resolveRun(handle.db, ref)
      case 'note':
        return this.resolveNote(projectId, handle.db, ref)
      default:
        throw ideaError(
          IDEA_ERROR.invalidSource,
          AppErrorCode.InvalidArgument,
          `source type ${JSON.stringify(ref.type)} is not supported by the skill`,
          { source_type: ref.type }
        )
    }
  }

  private resolveExperiment(db: Db, ref: { type: EvidenceSourceType; id: string }): ResolvedIdeaSource {
    const row = db
      .prepare('SELECT title, purpose, updated_at, deleted_at FROM experiments WHERE id = ?')
      .get(ref.id) as SourceRow | undefined
    if (row === undefined) sourceEmpty(ref.type, ref.id)
    const analysis = latestConfirmedResults(db, 'experiment', ref.id)
    const text = analysis.trim() !== '' ? analysis : row.purpose
    if (text.trim() === '') sourceEmpty(ref.type, ref.id)
    return {
      title: row.title,
      text,
      version: row.updated_at,
      truncated: false,
      deleted: row.deleted_at !== null
    }
  }

  private resolveRun(db: Db, ref: { type: EvidenceSourceType; id: string }): ResolvedIdeaSource {
    const row = db
      .prepare(
        `SELECT e.title AS experiment_title, e.purpose AS purpose,
                COALESCE(r.updated_at, '') AS updated_at,
                r.deleted_at AS run_deleted, e.deleted_at AS experiment_deleted
           FROM runs r JOIN experiments e ON e.id = r.experiment_id
          WHERE r.id = ?`
      )
      .get(ref.id) as
      | {
          experiment_title: string
          purpose: string
          updated_at: string
          run_deleted: string | null
          experiment_deleted: string | null
        }
      | undefined
    if (row === undefined) sourceEmpty(ref.type, ref.id)
    const analysis = latestConfirmedResults(db, 'run', ref.id)
    const text = analysis.trim() !== '' ? analysis : row.purpose
    if (text.trim() === '') sourceEmpty(ref.type, ref.id)
    return {
      title: runTitle(row.experiment_title, ref.id),
      text,
      version: row.updated_at,
      truncated: false,
      deleted: row.run_deleted !== null || row.experiment_deleted !== null
    }
  }

  private resolveNote(
    projectId: string,
    db: Db,
    ref: { type: EvidenceSourceType; id: string }
  ): ResolvedIdeaSource {
    const row = db
      .prepare('SELECT title, file_path, file_state, updated_at, deleted_at FROM notes WHERE id = ?')
      .get(ref.id) as
      | { title: string; file_path: string; file_state: string; updated_at: string; deleted_at: string | null }
      | undefined
    if (row === undefined) sourceEmpty(ref.type, ref.id)
    if (row.file_state === 'missing') {
      throw ideaError(
        IDEA_ERROR.sourceUnavailable,
        AppErrorCode.ExternalUnavailable,
        `note ${ref.id} body is missing`,
        { source_type: ref.type, source_id: ref.id }
      )
    }
    const absolute = this.workspace.project(projectId).paths.abs(row.file_path)
    let body: string
    try {
      body = readFileSync(absolute, 'utf8')
    } catch (error) {
      throw ideaError(
        IDEA_ERROR.sourceUnavailable,
        AppErrorCode.ExternalUnavailable,
        `note ${ref.id} body cannot be read`,
        { source_type: ref.type, source_id: ref.id, cause: String(error) }
      )
    }
    if (body.trim() === '') sourceEmpty(ref.type, ref.id)
    return {
      title: row.title,
      text: body,
      version: row.updated_at,
      truncated: false,
      deleted: row.deleted_at !== null
    }
  }
}