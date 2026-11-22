import type {
  AiOutput,
  AiOutputSourceRef,
  AiOutputStatus,
  AiProtocol,
  AiUsage,
  JsonObject
} from '@researchos/shared'
import { AppErrorCode, isEntityType } from '@researchos/shared'

import { formatTimestamp, SystemClock, type Clock } from '../../clock'
import type { Db } from '../../db/open'
import { appError } from '../../errors'
import { isValidId, newId } from '../../ids'
import type { Logger } from '../../logger'
import { isAiProtocol } from './capabilities'

/** Code-side task registry; no prompts table. */
const TASK_KINDS = new Set([
  'evidence_extraction',
  'experiment_outline',
  'experiment_results',
  'weekly_report',
  'document_understanding',
  'task_suggestions',
  'other'
])

export function registerTaskKinds(...kinds: string[]): void {
  for (const kind of kinds) TASK_KINDS.add(kind)
}

export function isValidTaskKind(kind: string): boolean {
  return TASK_KINDS.has(kind)
}

export type OutputActor = 'user' | 'mcp' | 'ai'

export interface AiOutputEntityRef {
  type: string
  id: string
}

export interface AiOutputProvenance {
  providerId: string
  providerName: string
  protocol: AiProtocol
  modelId: string
  promptVersion: string
  sourceRefs?: AiOutputSourceRef[]
  parameters?: JsonObject
}

export interface CreateDraftInput {
  projectId: string
  taskKind: string
  entity?: AiOutputEntityRef | null
  contentMd: string
  provenance: AiOutputProvenance
  usage?: AiUsage | null
}

export interface AiOutputQuery {
  entityType?: string
  entityId?: string
  taskKind?: string
  status?: AiOutputStatus
  limit?: number
  offset?: number
}

export interface AnalysisEvent {
  type: 'ai.analysis_completed'
  actor: 'ai'
  entityType: string
  entityId: string
  summary: string
  payload: { taskKind: string; providerId: string; modelId: string }
}

export interface AiOutputServiceOptions {
  clock?: Clock
  /** Bound project id; drafts must carry the same id. */
  projectId?: string
  /** Throws while the project is archived (AI-service rules 5–6). */
  readOnly?: () => void
  /** Emits `ai.analysis_completed`; failures are diagnostics, never rollbacks. */
  emitAnalysis?: (event: AnalysisEvent) => void
  logger?: Logger
}

export interface AiOutputService {
  createDraft(input: CreateDraftInput): AiOutput
  get(outputId: string): AiOutput
  list(query?: AiOutputQuery): { items: AiOutput[]; total: number }
  listByEntity(entityType: string, entityId: string): AiOutput[]
  updateContent(outputId: string, contentMd: string, actor: OutputActor): AiOutput
  confirm(outputId: string, actor?: OutputActor): AiOutput
  /** Confirm inside the caller's project transaction (nested savepoint). */
  confirmTx(outputId: string, actor?: OutputActor): AiOutput
  discard(outputId: string): { discarded: boolean; at: string }
}

interface OutputRow {
  id: string
  task_kind: string
  entity_type: string | null
  entity_id: string | null
  status: AiOutputStatus
  content_md: string
  provider_id: string
  provider_name: string
  protocol: string
  model_id: string
  prompt_version: string
  source_refs: string
  parameters: string
  usage: string | null
  human_edited: number
  created_at: string
  confirmed_at: string | null
}

const OUTPUT_COLUMNS = `id, task_kind, entity_type, entity_id, status, content_md,
  provider_id, provider_name, protocol, model_id, prompt_version, source_refs,
  parameters, usage, human_edited, created_at, confirmed_at`

export class SqliteAiOutputService implements AiOutputService {
  private readonly db: Db
  private readonly clock: Clock
  private readonly projectId: string | undefined
  private readonly options: AiOutputServiceOptions

  constructor(db: Db, options: AiOutputServiceOptions = {}) {
    this.db = db
    this.clock = options.clock ?? new SystemClock()
    this.projectId = options.projectId
    this.options = options
  }

  createDraft(input: CreateDraftInput): AiOutput {
    this.assertWritable()
    this.validateDraft(input)
    const id = newId(this.clock)
    const now = formatTimestamp(this.clock.now())
    const provenance = input.provenance
    const entity = input.entity ?? null
    this.db
      .prepare(
        `INSERT INTO ai_outputs
           (id, task_kind, entity_type, entity_id, status, content_md, provider_id,
            provider_name, protocol, model_id, prompt_version, source_refs, parameters,
            usage, human_edited, created_at)
         VALUES (?, ?, ?, ?, 'draft', ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`
      )
      .run(
        id,
        input.taskKind,
        entity?.type ?? null,
        entity?.id ?? null,
        input.contentMd,
        provenance.providerId,
        provenance.providerName,
        provenance.protocol,
        provenance.modelId,
        provenance.promptVersion,
        JSON.stringify(provenance.sourceRefs ?? []),
        JSON.stringify(provenance.parameters ?? {}),
        input.usage === undefined || input.usage === null ? null : JSON.stringify(input.usage),
        now
      )
    const output = this.get(id)
    if (entity !== null) {
      try {
        this.options.emitAnalysis?.({
          type: 'ai.analysis_completed',
          actor: 'ai',
          entityType: entity.type,
          entityId: entity.id,
          summary: `${input.taskKind} via ${provenance.providerName}/${provenance.modelId}`,
          payload: {
            taskKind: input.taskKind,
            providerId: provenance.providerId,
            modelId: provenance.modelId
          }
        })
      } catch (cause) {
        this.options.logger?.warn('ai.analysis_completed emit failed', {
          outputId: id,
          error: cause instanceof Error ? cause.message : String(cause)
        })
      }
    }
    return output
  }

  get(outputId: string): AiOutput {
    const row = this.db
      .prepare(`SELECT ${OUTPUT_COLUMNS} FROM ai_outputs WHERE id = ?`)
      .get(outputId) as OutputRow | undefined
    if (row === undefined) {
      throw appError(AppErrorCode.NotFound, `AI output not found: ${outputId}`, { outputId })
    }
    return toOutput(row, this.projectId)
  }

  list(query: AiOutputQuery = {}): { items: AiOutput[]; total: number } {
    const where: string[] = []
    const params: unknown[] = []
    if (query.entityType !== undefined) {
      where.push('entity_type = ?')
      params.push(query.entityType)
    }
    if (query.entityId !== undefined) {
      where.push('entity_id = ?')
      params.push(query.entityId)
    }
    if (query.taskKind !== undefined) {
      where.push('task_kind = ?')
      params.push(query.taskKind)
    }
    if (query.status !== undefined) {
      where.push('status = ?')
      params.push(query.status)
    }
    const clause = where.length === 0 ? '' : `WHERE ${where.join(' AND ')}`
    const limit = Math.min(Math.max(query.limit ?? 50, 1), 200)
    const offset = Math.max(query.offset ?? 0, 0)
    const rows = this.db
      .prepare(
        `SELECT ${OUTPUT_COLUMNS} FROM ai_outputs ${clause}
         ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
      )
      .all(...params, limit, offset) as OutputRow[]
    const totalRow = this.db
      .prepare(`SELECT COUNT(*) AS count FROM ai_outputs ${clause}`)
      .get(...params) as { count: number }
    return {
      items: rows.map((row) => toOutput(row, this.projectId)),
      total: totalRow.count
    }
  }

  listByEntity(entityType: string, entityId: string): AiOutput[] {
    return this.list({ entityType, entityId, limit: 200 }).items
  }

  updateContent(outputId: string, contentMd: string, actor: OutputActor): AiOutput {
    this.assertWritable()
    if (actor !== 'user' && actor !== 'mcp' && actor !== 'ai') {
      throw appError(AppErrorCode.InvalidArgument, `unknown actor: ${String(actor)}`, {
        field: 'actor'
      })
    }
    const output = this.get(outputId)
    if (output.status === 'confirmed' && actor !== 'user') {
      throw appError(
        AppErrorCode.Conflict,
        'confirmed AI output is immutable for machine actors',
        { reason: 'confirmed_immutable_for_machine', outputId }
      )
    }
    this.db
      .prepare(`UPDATE ai_outputs SET content_md = ?, human_edited = MAX(human_edited, ?) WHERE id = ?`)
      .run(contentMd, actor === 'user' ? 1 : 0, outputId)
    return this.get(outputId)
  }

  confirm(outputId: string, actor: OutputActor = 'user'): AiOutput {
    this.assertWritable()
    if (this.db.inTransaction) return this.confirmTx(outputId, actor)
    return this.db.transaction(() => this.confirmTx(outputId, actor)).immediate()
  }

  confirmTx(outputId: string, actor: OutputActor = 'user'): AiOutput {
    this.assertWritable()
    if (actor !== 'user') {
      throw appError(
        AppErrorCode.UserConfirmationRequired,
        'only the user can confirm an AI output',
        { reason: 'confirmation_requires_user', outputId }
      )
    }
    const output = this.get(outputId)
    if (output.status === 'confirmed') return output
    const now = formatTimestamp(this.clock.now())
    this.db
      .prepare(
        `UPDATE ai_outputs SET status = 'confirmed', confirmed_at = ?
         WHERE id = ? AND status = 'draft'`
      )
      .run(now, outputId)
    return this.get(outputId)
  }

  discard(outputId: string): { discarded: boolean; at: string } {
    this.assertWritable()
    const output = this.get(outputId)
    if (output.status === 'confirmed') {
      throw appError(AppErrorCode.Conflict, 'confirmed AI output cannot be discarded', {
        reason: 'confirmed_immutable',
        outputId
      })
    }
    this.db.prepare(`DELETE FROM ai_outputs WHERE id = ? AND status = 'draft'`).run(outputId)
    return { discarded: true, at: formatTimestamp(this.clock.now()) }
  }

  private assertWritable(): void {
    this.options.readOnly?.()
  }

  private validateDraft(input: CreateDraftInput): void {
    if (!isValidTaskKind(input.taskKind)) {
      throw appError(AppErrorCode.InvalidArgument, `unregistered task_kind: ${input.taskKind}`, {
        field: 'taskKind'
      })
    }
    const provenance = input.provenance
    if (provenance.promptVersion === undefined || provenance.promptVersion === '') {
      throw appError(AppErrorCode.InvalidArgument, 'prompt_version is required', {
        field: 'promptVersion'
      })
    }
    if (!isAiProtocol(provenance.protocol)) {
      throw appError(AppErrorCode.InvalidArgument, 'invalid provenance protocol', {
        field: 'protocol'
      })
    }
    if (provenance.providerId === '' || provenance.modelId === '') {
      throw appError(AppErrorCode.InvalidArgument, 'provenance requires provider and model ids', {
        field: 'provenance'
      })
    }
    if (input.projectId === '') {
      throw appError(AppErrorCode.InvalidArgument, 'project id is required', { field: 'projectId' })
    }
    if (this.projectId !== undefined && input.projectId !== this.projectId) {
      throw appError(AppErrorCode.InvalidArgument, 'draft project does not match the bound project', {
        field: 'projectId'
      })
    }
    const entity = input.entity ?? null
    if (entity !== null) {
      if (!isEntityType(entity.type) || !isValidId(entity.id)) {
        throw appError(
          AppErrorCode.InvalidArgument,
          `entity binding must reference a registered entity type and a UUIDv7: ${entity.type}/${entity.id}`,
          { field: 'entity' }
        )
      }
    }
  }
}

function toOutput(row: OutputRow, projectId: string | undefined): AiOutput {
  const sourceRefs = JSON.parse(row.source_refs) as AiOutputSourceRef[]
  let usage: JsonObject | null = null
  if (row.usage !== null && row.usage !== '') {
    usage = JSON.parse(row.usage) as JsonObject
  }
  return {
    id: row.id,
    projectId: projectId ?? null,
    taskKind: row.task_kind,
    entityType: row.entity_type,
    entityId: row.entity_id,
    status: row.status,
    contentMd: row.content_md,
    providerId: row.provider_id,
    providerName: row.provider_name,
    protocol: row.protocol,
    modelId: row.model_id,
    promptVersion: row.prompt_version,
    sourceRefs,
    parameters: JSON.parse(row.parameters) as JsonObject,
    usage,
    humanEdited: row.human_edited === 1,
    createdAt: row.created_at,
    confirmedAt: row.confirmed_at
  }
}
