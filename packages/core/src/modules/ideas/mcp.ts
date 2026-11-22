/**
 * The 14 Ideas MCP tools of docs/mcp-tools-catalog.json (module=ideas, minus
 * `project_readiness_check`, which belongs to the readiness workstream).
 *
 * Definitions use the shared `modules/mcp` helpers: `writeTool`/`jobTool`
 * inject the required `idempotency_key` (plan §5 defect 5) and the host wraps
 * every shape with `z.strictObject` (additionalProperties:false). Argument
 * names mirror the catalog exactly — `evidence_update` carries no `idea_id`
 * and none of the rejected source fields (defect 10).
 */
import { z } from 'zod'

import type { EvidenceView, Idea, IdeaHistoryEntry, IdeaRelationSet, IdeaSummary } from '@researchos/shared'
import { AppError, AppErrorCode } from '@researchos/shared'

import { IDEA_ERROR, ideaError, invalidInput } from './errors'
import type { IdeasService } from './service'
import type { EvidenceBatchFailure } from './types'
import { confirmTool, jobQueued, jobTool, ok, pending, readTool, stripIdempotencyKey, writeTool } from '../mcp/tools'
import type { HandlerContext, ToolDefinition } from '../mcp/types'
import type { ConfirmationEffect } from '../mcp/executors'
import { Confirmations } from '../mcp/confirmations'
import { argsHash } from '../mcp/idempotency'

/** Services-bag key the composition root uses to hand over the runtime. */
export const IDEAS_RUNTIME_KEY = 'ideas.runtime'

export interface IdeasMcpRuntime {
  service: IdeasService
  /** `server_status.ai_available` for this request; false fails the skill fast. */
  aiAvailable?: boolean
}

function runtimeOf(ctx: HandlerContext): IdeasMcpRuntime {
  const runtime = ctx.services[IDEAS_RUNTIME_KEY]
  if (runtime === undefined) {
    throw new AppError(AppErrorCode.Internal, 'ideas runtime is not wired', { traceId: ctx.traceId })
  }
  return runtime as IdeasMcpRuntime
}

function serviceOf(ctx: HandlerContext): IdeasService {
  return runtimeOf(ctx).service
}

/** Rejects a `project_id` that disagrees with the request's project scope. */
function projectOf(ctx: HandlerContext, projectId: string): string {
  if (ctx.projectId !== null && ctx.projectId !== projectId) {
    throw invalidInput('project_id 与请求上下文不一致')
  }
  return projectId
}

const id = z.uuidv7()
const statusEnum = z.enum([
  'New',
  'Researching',
  'Validating',
  'Supported',
  'PartiallySupported',
  'Inconclusive',
  'Rejected',
  'OnHold'
])
const directionEnum = z.enum(['support', 'oppose'])
const sourceTypeEnum = z.enum(['experiment', 'run', 'note', 'paper'])
const extractionSourceRef = z.strictObject({ type: z.enum(['experiment', 'run', 'note']), id })
const limit = z.number().int().min(1).max(200)
const cursor = z.string().min(1).max(1_024)
const timestamp = z.string().max(64)
const projectId = { project_id: id }
const paging = { limit: limit.optional(), cursor: cursor.optional() }

const listSchema = z.strictObject({
  ...projectId,
  ...paging,
  status: statusEnum.optional(),
  q: z.string().max(200).optional(),
  include_deleted: z.boolean().optional()
})

const getSchema = z.strictObject({ ...projectId, idea_id: id })

const createSchema = z.strictObject({
  ...projectId,
  title: z.string().min(1).max(200),
  description: z.string().optional()
})

const updateSchema = z.strictObject({
  ...projectId,
  idea_id: id,
  title: z.string().min(1).max(200).optional(),
  description: z.string().optional(),
  expected_updated_at: timestamp.optional()
})

const changeStatusSchema = z.strictObject({
  ...projectId,
  idea_id: id,
  status: statusEnum,
  rejection_reason: z.string().max(4_000).optional(),
  note: z.string().max(2_000).optional()
})

const deriveSchema = z.strictObject({
  ...projectId,
  parent_id: id,
  title: z.string().min(1).max(200),
  description: z.string().optional()
})

const deleteSchema = z.strictObject({ ...projectId, idea_id: id })

const evidenceListSchema = z.strictObject({
  ...projectId,
  ...paging,
  idea_id: id.optional(),
  confirmed: z.boolean().optional(),
  direction: directionEnum.optional(),
  include_deleted: z.boolean().optional()
})

const evidenceCreateSchema = z.strictObject({
  ...projectId,
  idea_id: id,
  direction: directionEnum,
  content: z.string().min(1).max(8_000),
  source_type: sourceTypeEnum.optional(),
  source_id: id.optional()
})

const evidenceUpdateSchema = z.strictObject({
  ...projectId,
  evidence_id: id,
  direction: directionEnum.optional(),
  content: z.string().min(1).max(8_000).optional(),
  expected_updated_at: timestamp.optional()
})

const evidenceDiscardSchema = z.strictObject({ ...projectId, idea_id: id, evidence_id: id })

const evidenceDeleteSchema = z.strictObject({ ...projectId, evidence_id: id })

const evidenceExtractSchema = z.strictObject({
  ...projectId,
  idea_id: id,
  source_refs: z.array(extractionSourceRef).max(8).optional()
})

const evidenceConfirmSchema = z.strictObject({
  ...projectId,
  idea_id: id,
  evidence_ids: z.array(id).min(1)
})

/* ------------------------------------------------------------- projections */

function ideaView(idea: Idea): Record<string, unknown> {
  return {
    idea_id: idea.id,
    title: idea.title,
    status: idea.status,
    description_md: idea.description,
    rejection_reason: idea.rejectionReason,
    created_at: idea.createdAt,
    updated_at: idea.updatedAt,
    deleted_at: idea.deletedAt
  }
}

function ideaSummaryView(summary: IdeaSummary): Record<string, unknown> {
  return {
    ...ideaView(summary),
    support_count: summary.supportCount,
    oppose_count: summary.opposeCount,
    draft_count: summary.draftCount,
    linked_papers: summary.linkedPapers,
    linked_notes: summary.linkedNotes,
    linked_experiments: summary.linkedExperiments,
    derived_from_id: summary.derivedFromId,
    derived_from_title: summary.derivedFromTitle
  }
}

function evidenceOf(view: EvidenceView) {
  return {
    evidence_id: view.id,
    idea_id: view.ideaId,
    direction: view.direction,
    content: view.content,
    ai_generated: view.aiGenerated,
    confirmed: view.confirmed,
    source: { type: view.source.type, id: view.source.id },
    source_title: view.sourceTitle,
    source_broken: view.sourceBroken,
    source_deleted: view.sourceDeleted,
    content_hash: view.contentHash,
    provenance: view.provenance,
    created_at: view.createdAt,
    updated_at: view.updatedAt
  }
}

function historyView(entry: IdeaHistoryEntry): Record<string, unknown> {
  return {
    at: entry.at,
    kind: entry.kind,
    event_type: entry.eventType,
    summary: entry.summary,
    actor: entry.actor,
    old: entry.old,
    new: entry.new,
    ref: entry.ref,
    synthesized: entry.synthesized
  }
}

function relationsView(relations: IdeaRelationSet): Record<string, unknown> {
  return {
    papers: relations.papers,
    notes: relations.notes,
    experiments: relations.experiments,
    derived_from: relations.derivedFrom,
    derived: relations.derived
  }
}

/** Maps the first refused batch item to the tool's typed domain error. */
function throwBatchFailure(failure: EvidenceBatchFailure): never {
  const code =
    failure.code === IDEA_ERROR.evidenceConfirmed || failure.code === IDEA_ERROR.evidenceDirectionLocked
      ? AppErrorCode.Conflict
      : failure.code === IDEA_ERROR.evidenceNotFound
        ? AppErrorCode.NotFound
        : AppErrorCode.InvalidArgument
  throw ideaError(failure.code, code, failure.message, { evidence_id: failure.id })
}

/** The 14 catalog tools for the `ideas` module. */
export const ideasTools: readonly ToolDefinition[] = [
  readTool({
    name: 'idea_list',
    title: 'Idea 列表',
    description: '列出项目 Idea，可按状态、关键字过滤。',
    inputSchema: listSchema.shape,
    handler(ctx, args) {
      const input = listSchema.parse(stripIdempotencyKey(args))
      const project = projectOf(ctx, input.project_id)
      const page = serviceOf(ctx).listIdeas(project, {
        statuses: input.status === undefined ? undefined : [input.status],
        text: input.q,
        includeDeleted: input.include_deleted,
        limit: input.limit,
        cursor: input.cursor
      })
      return ok({
        items: page.items.map(ideaSummaryView),
        next_cursor: page.nextCursor
      })
    }
  }),

  readTool({
    name: 'idea_get',
    title: 'Idea 详情',
    description: '返回 Idea、证据、关系与近期历史。',
    inputSchema: getSchema.shape,
    handler(ctx, args) {
      const input = getSchema.parse(stripIdempotencyKey(args))
      const project = projectOf(ctx, input.project_id)
      const service = serviceOf(ctx)
      const detail = service.getIdea(project, input.idea_id)
      const evidence = service.listEvidence(project, { ideaId: input.idea_id, limit: 200 })
      return ok({
        idea: { ...ideaView(detail), evidence: detail.evidence },
        summary: detail.evidence,
        evidence: evidence.items.map((view) => evidenceOf(view)),
        relations: relationsView(detail.relations),
        history: detail.history.map(historyView)
      })
    }
  }),

  writeTool({
    name: 'idea_create',
    title: '创建 Idea',
    description: '创建 Idea，初始状态恒为 New。',
    inputSchema: createSchema.shape,
    handler(ctx, args) {
      const input = createSchema.parse(stripIdempotencyKey(args))
      const project = projectOf(ctx, input.project_id)
      const idea = serviceOf(ctx).createIdea(project, {
        title: input.title,
        description: input.description,
        actor: 'mcp'
      })
      return ok({ idea: ideaView(idea) })
    }
  }),

  writeTool({
    name: 'idea_update',
    title: '更新 Idea',
    description: '更新 Idea 标题或描述。提供 expected_updated_at 时做乐观并发检查。',
    inputSchema: updateSchema.shape,
    handler(ctx, args) {
      const input = updateSchema.parse(stripIdempotencyKey(args))
      const project = projectOf(ctx, input.project_id)
      if (input.title === undefined && input.description === undefined) {
        throw invalidInput('provide title or description to update')
      }
      const idea = serviceOf(ctx).updateIdea(
        project,
        input.idea_id,
        { title: input.title, description: input.description },
        { actor: 'mcp', expectedUpdatedAt: input.expected_updated_at }
      )
      return ok({ idea: ideaView(idea) })
    }
  }),

  writeTool({
    name: 'idea_change_status',
    title: '变更 Idea 状态',
    description: '变更 Idea 状态；Rejected 必须提供 rejection_reason。',
    inputSchema: changeStatusSchema.shape,
    handler(ctx, args) {
      const input = changeStatusSchema.parse(stripIdempotencyKey(args))
      const project = projectOf(ctx, input.project_id)
      const idea = serviceOf(ctx).changeStatus(project, input.idea_id, {
        status: input.status,
        rejectionReason: input.rejection_reason,
        note: input.note,
        actor: 'mcp'
      })
      return ok({ idea: ideaView(idea) })
    }
  }),

  writeTool({
    name: 'idea_derive',
    title: '衍生 Idea',
    description: '从父 Idea 衍生新 Idea，并写入 derived_from 关系；初始状态恒为 New。',
    inputSchema: deriveSchema.shape,
    handler(ctx, args) {
      const input = deriveSchema.parse(stripIdempotencyKey(args))
      const project = projectOf(ctx, input.project_id)
      const idea = serviceOf(ctx).deriveIdea(
        project,
        input.parent_id,
        { title: input.title, description: input.description },
        'mcp'
      )
      return ok({ idea: ideaView(idea) })
    }
  }),

  writeTool({
    name: 'idea_delete',
    title: '删除 Idea',
    description: '软删除 Idea（进入回收站）。',
    inputSchema: deleteSchema.shape,
    annotations: { destructiveHint: true },
    handler(ctx, args) {
      const input = deleteSchema.parse(stripIdempotencyKey(args))
      const project = projectOf(ctx, input.project_id)
      serviceOf(ctx).deleteIdea(project, input.idea_id, 'mcp')
      return ok({ deleted: true })
    }
  }),

  readTool({
    name: 'evidence_list',
    title: '证据列表',
    description: '列出 Idea 证据，可按方向与确认状态过滤。',
    inputSchema: evidenceListSchema.shape,
    handler(ctx, args) {
      const input = evidenceListSchema.parse(stripIdempotencyKey(args))
      const project = projectOf(ctx, input.project_id)
      const service = serviceOf(ctx)
      const page = service.listEvidence(project, {
        ideaId: input.idea_id,
        direction: input.direction,
        confirmed: input.confirmed,
        includeDeleted: input.include_deleted,
        limit: input.limit,
        cursor: input.cursor
      })
      return ok({ items: page.items.map((view) => evidenceOf(view)), next_cursor: page.nextCursor })
    }
  }),

  writeTool({
    name: 'evidence_create',
    title: '新增证据',
    description: '为 Idea 新增人工证据（ai_generated=false, confirmed=true）。',
    inputSchema: evidenceCreateSchema.shape,
    handler(ctx, args) {
      const input = evidenceCreateSchema.parse(stripIdempotencyKey(args))
      const project = projectOf(ctx, input.project_id)
      if (input.source_type === undefined || input.source_id === undefined) {
        throw invalidInput('证据必须提供 source_type 与 source_id')
      }
      const evidence = serviceOf(ctx).addEvidence(project, {
        ideaId: input.idea_id,
        direction: input.direction,
        content: input.content,
        source: { type: input.source_type, id: input.source_id },
        actor: 'mcp'
      })
      return ok({ evidence: evidenceOf(evidence) })
    }
  }),

  writeTool({
    name: 'evidence_update',
    title: '更新证据',
    description: '更新草稿证据的方向或内容；已确认证据不可改方向。',
    inputSchema: evidenceUpdateSchema.shape,
    handler(ctx, args) {
      const input = evidenceUpdateSchema.parse(stripIdempotencyKey(args))
      const project = projectOf(ctx, input.project_id)
      if (input.direction === undefined && input.content === undefined) {
        throw invalidInput('provide direction or content to update')
      }
      const evidence = serviceOf(ctx).updateEvidence(project, {
        evidenceId: input.evidence_id,
        direction: input.direction,
        content: input.content,
        actor: 'mcp',
        expectedUpdatedAt: input.expected_updated_at
      })
      return ok({ evidence: evidenceOf(evidence) })
    }
  }),

  writeTool({
    name: 'evidence_discard',
    title: '丢弃草稿证据',
    description: '丢弃 AI 草稿证据（仅草稿，软删除）。',
    inputSchema: evidenceDiscardSchema.shape,
    handler(ctx, args) {
      const input = evidenceDiscardSchema.parse(stripIdempotencyKey(args))
      const project = projectOf(ctx, input.project_id)
      const result = serviceOf(ctx).discardEvidence(project, input.idea_id, [input.evidence_id], 'mcp')
      const failure = result.failed[0]
      if (failure !== undefined) throwBatchFailure(failure)
      return ok({ discarded: true })
    }
  }),

  writeTool({
    name: 'evidence_delete',
    title: '删除证据',
    description: '软删除已确认证据。草稿请使用 evidence_discard。',
    inputSchema: evidenceDeleteSchema.shape,
    annotations: { destructiveHint: true },
    handler(ctx, args) {
      const input = evidenceDeleteSchema.parse(stripIdempotencyKey(args))
      const project = projectOf(ctx, input.project_id)
      const service = serviceOf(ctx)
      const evidence = service.evidenceById(project, input.evidence_id)
      if (!evidence.confirmed) {
        throw invalidInput('draft evidence must be discarded, not deleted', {
          reason: IDEA_ERROR.evidenceNotAIDraft,
          evidence_id: input.evidence_id
        })
      }
      service.deleteEvidence(project, input.evidence_id, 'mcp')
      return ok({ deleted: true })
    }
  }),

  jobTool({
    name: 'evidence_extract',
    title: 'AI 提取证据草稿',
    description: '从实验/运行/笔记来源提取证据草稿（AI 产出，actor=ai，不自动确认）。返回 job_id。',
    readOnly: false,
    inputSchema: evidenceExtractSchema.shape,
    async handler(ctx, args) {
      const input = evidenceExtractSchema.parse(stripIdempotencyKey(args))
      const project = projectOf(ctx, input.project_id)
      if (input.source_refs === undefined || input.source_refs.length === 0) {
        throw invalidInput('source_refs must contain 1..8 entries')
      }
      const runtime = runtimeOf(ctx)
      if (runtime.aiAvailable === false) {
        throw new AppError(AppErrorCode.AiUnavailable, 'AI 服务未配置，无法提取证据草稿', {
          reason: IDEA_ERROR.aiDisabled
        })
      }
      const { jobId } = await runtime.service.enqueueExtraction(project, {
        ideaId: input.idea_id,
        sourceRefs: input.source_refs,
        actor: 'mcp',
        signal: ctx.signal
      })
      return jobQueued({ job_id: jobId, poll_after_ms: 1_000 })
    }
  }),

  confirmTool({
    name: 'evidence_confirm',
    title: '确认证据',
    description: '请求把 AI 草稿证据转为已确认。工具只入队，必须在应用内人工批准后以 actor=user 逐条执行。',
    inputSchema: evidenceConfirmSchema.shape,
    handler(ctx, args) {
      const input = evidenceConfirmSchema.parse(stripIdempotencyKey(args))
      const project = projectOf(ctx, input.project_id)
      const service = serviceOf(ctx)
      const drafts = input.evidence_ids.map((evidenceId) => {
        const evidence = service.evidenceById(project, evidenceId)
        if (evidence.ideaId !== input.idea_id) {
          throw ideaError(
            IDEA_ERROR.evidenceNotFound,
            AppErrorCode.NotFound,
            `evidence ${evidenceId} does not belong to idea ${input.idea_id}`,
            { evidence_id: evidenceId, idea_id: input.idea_id }
          )
        }
        if (!evidence.aiGenerated || evidence.confirmed) {
          throw invalidInput(`evidence ${evidenceId} is not an unconfirmed AI draft`, {
            evidence_id: evidenceId
          })
        }
        return evidenceOf(evidence)
      })
      const raw: unknown = ctx.services['confirmations']
      const confirmations = raw instanceof Confirmations ? raw : undefined
      if (confirmations === undefined) {
        throw new AppError(AppErrorCode.Internal, 'ideas: confirmation service is not configured', {
          port: 'confirmations',
          traceId: ctx.traceId
        })
      }
      const request: Record<string, unknown> = {
        project_id: project,
        idea_id: input.idea_id,
        evidence_ids: [...input.evidence_ids]
      }
      const summary = `把 ${input.evidence_ids.length} 条证据草稿标记为已确认。`
      const impact = { idea_id: input.idea_id, drafts }
      const confirmation = confirmations.enqueue(ctx.principal, {
        tool: 'evidence_confirm',
        args: request,
        argsHash: argsHash(request),
        title: '确认证据',
        summary,
        impact,
        projectId: project,
        traceId: ctx.traceId
      })
      return pending({
        confirmation_id: confirmation.id,
        expires_at: confirmation.expiresAt,
        title: '确认证据',
        summary,
        impact
      })
    }
  })
]

/**
 * Desktop confirmation effect for `evidence_confirm`; W17 wires it into
 * `DomainConfirmationEffects.evidenceConfirm`. The executor runs with
 * actor=user, one event per draft.
 */
export function createEvidenceConfirmEffect(service: IdeasService): ConfirmationEffect {
  return async (context) => {
    const projectIdValue = context.args['project_id']
    const ideaId = context.args['idea_id']
    const evidenceIds = context.args['evidence_ids']
    if (typeof projectIdValue !== 'string' || typeof ideaId !== 'string' || !Array.isArray(evidenceIds)) {
      throw invalidInput('evidence_confirm args are incomplete')
    }
    return service.executeEvidenceConfirm(
      projectIdValue,
      ideaId,
      evidenceIds.filter((value): value is string => typeof value === 'string')
    )
  }
}