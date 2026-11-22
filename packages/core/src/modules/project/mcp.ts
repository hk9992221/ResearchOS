import { z } from 'zod'

import type {
  Project,
  ProvenanceLineageNode,
  ProvenanceNode,
  ProvenanceReport
} from '@researchos/shared'
import { AppErrorCode } from '@researchos/shared'

import { appError } from '../../errors'
import type { ContextGraphService } from '../contextgraph/index'
import type { DashboardService } from '../dashboard/index'
import type { OverviewService } from '../overview/index'
import type { ProvenanceService } from '../provenance/index'
import type { ReadinessService } from '../readiness/index'
import type { ProjectService } from './service'
import { argsHash } from '../mcp/idempotency'
import { ok, pending, readTool, writeTool, confirmTool } from '../mcp/tools'
import type { HandlerContext, Principal, ToolDefinition } from '../mcp/types'

/**
 * The nine project-domain MCP tools:
 * project_list/get/overview/provenance/create/update/archive/unarchive/delete.
 * Export and import tools belong to the export workstream.
 *
 * Every mutating tool declares `idempotency_key` through `writeTool`/
 * `confirmTool` (defect 5). Handlers throw typed AppErrors; the host maps them
 * with `tools.fail`, never through a catch-all INTERNAL.
 */

interface ProjectToolServices {
  project?: ProjectService
  overview?: OverviewService
  provenance?: ProvenanceService
  dashboard?: DashboardService
  context?: ContextGraphService
  readiness?: ReadinessService
  confirmations?: ConfirmationQueue
}

interface ConfirmationQueue {
  enqueue(
    principal: Principal,
    request: {
      tool: string
      args: Record<string, unknown>
      argsHash: string
      title: string
      summary: string
      impact?: Record<string, unknown>
      projectId?: string | null
      traceId?: string | null
    }
  ): { id: string; expiresAt: string }
}

function servicesOf(ctx: HandlerContext): ProjectToolServices {
  return ctx.services as ProjectToolServices
}

function projectService(ctx: HandlerContext): ProjectService {
  const service = servicesOf(ctx).project
  if (service === undefined) throw missingService('project')
  return service
}

function overviewService(ctx: HandlerContext): OverviewService {
  const service = servicesOf(ctx).overview
  if (service === undefined) throw missingService('overview')
  return service
}

function provenanceService(ctx: HandlerContext): ProvenanceService {
  const service = servicesOf(ctx).provenance
  if (service === undefined) throw missingService('provenance')
  return service
}

function confirmationQueue(ctx: HandlerContext): ConfirmationQueue {
  const service = servicesOf(ctx).confirmations
  if (service === undefined) throw missingService('confirmations')
  return service
}

function missingService(name: string) {
  return appError(AppErrorCode.Internal, `项目模块未就绪：${name}`, { details: { service: name } })
}

/* ------------------------------------------------------------- projections */

function projectView(project: Project): Record<string, unknown> {
  return {
    id: project.id,
    name: project.name,
    summary: project.summary,
    goals: project.goals,
    focus_md: project.focusMd,
    status: project.status,
    created_at: project.createdAt,
    updated_at: project.updatedAt,
    archived_at: project.archivedAt
  }
}

function statsView(stats: {
  notes: number
  ideasActive: number
  experiments: number
  tasksOpen: number
  tasksBlocked: number
  papers: number
  trashed: number
  dirBytes: number
  lastActivityAt: string | null
}): Record<string, unknown> {
  return {
    notes: stats.notes,
    ideas_active: stats.ideasActive,
    experiments: stats.experiments,
    tasks_open: stats.tasksOpen,
    tasks_blocked: stats.tasksBlocked,
    papers: stats.papers,
    trashed: stats.trashed,
    dir_bytes: stats.dirBytes,
    last_activity_at: stats.lastActivityAt
  }
}

function encodeCursor(offset: number): string {
  return Buffer.from(`o:${offset}`, 'utf8').toString('base64url')
}

function decodeCursor(cursor: string | undefined): number {
  if (cursor === undefined || cursor.trim() === '') return 0
  let text: string
  try {
    text = Buffer.from(cursor, 'base64url').toString('utf8')
  } catch {
    throw invalidCursor()
  }
  if (!text.startsWith('o:')) throw invalidCursor()
  const offset = Number(text.slice(2))
  if (!Number.isInteger(offset) || offset < 0) throw invalidCursor()
  return offset
}

function invalidCursor() {
  return appError(AppErrorCode.InvalidArgument, '分页游标无效', { details: { field: 'cursor' } })
}

function provenanceNodeView(node: ProvenanceNode): Record<string, unknown> {
  return {
    node_id: node.id,
    at: node.at,
    kind: node.kind,
    entity: node.entity,
    title: node.title,
    summary: node.summary,
    badge: node.badge,
    routine: node.routine,
    linked_nodes: node.linkedNodes
  }
}

function lineageNodeView(node: ProvenanceLineageNode): Record<string, unknown> {
  return {
    idea_id: node.id,
    title: node.title,
    status: node.status,
    rejection_reason: node.rejectionReason,
    support_count: node.supportCount,
    oppose_count: node.opposeCount,
    papers: node.papers.map((ref) => ({ id: ref.id, title: ref.title })),
    notes: node.notes.map((ref) => ({ id: ref.id, title: ref.title })),
    experiments: node.experiments.map((ref) => ({ id: ref.id, title: ref.title, badge: ref.badge })),
    children: node.children.map(lineageNodeView)
  }
}

function deleteImpact(plan: {
  projectName: string
  entityCounts: Record<string, number>
  trashedCount: number
  dirBytes: number
  sharedPaperIds: string[]
  exclusivePaperIds: string[]
  paperImpactUnknown: boolean
  lastExportedAt: string | null
  exportRecommended: boolean
}): Record<string, unknown> {
  return {
    project_name: plan.projectName,
    entity_counts: plan.entityCounts,
    trashed: plan.trashedCount,
    dir_bytes: plan.dirBytes,
    papers_shared: plan.sharedPaperIds.length,
    papers_orphaned: plan.exclusivePaperIds.length,
    paper_impact_known: !plan.paperImpactUnknown,
    last_exported_at: plan.lastExportedAt,
    export_recommended: plan.exportRecommended
  }
}

function parseArgTime(raw: string | undefined, field: string): string | undefined {
  if (raw === undefined || raw === '') return undefined
  const parsed = Date.parse(raw)
  if (!Number.isFinite(parsed)) {
    throw appError(AppErrorCode.InvalidArgument, `参数 ${field} 必须是 RFC3339 时间`, {
      details: { field }
    })
  }
  return raw
}

/* ------------------------------------------------------------------- tools */

const projectIdField = z.string().min(1).max(64).describe('项目 ID')

export const projectTools: ToolDefinition[] = [
  readTool({
    name: 'project_list',
    title: '项目列表',
    description: '列出工作区项目，可按状态与关键词过滤。归档项目仅在 status=archived 或显式包含时返回。',
    inputSchema: {
      status: z.enum(['active', 'archived']).optional().describe('项目状态'),
      q: z.string().max(200).optional().describe('名称/摘要关键字'),
      limit: z.number().int().min(1).max(200).optional().describe('返回条数，默认 50，上限 200'),
      cursor: z.string().max(512).optional().describe('上一页返回的 next_cursor')
    },
    handler: (ctx, args) => {
      const service = projectService(ctx)
      const limit = args.limit === undefined || args.limit <= 0 ? 50 : Math.min(args.limit, 200)
      const offset = decodeCursor(args.cursor)
      const all = service
        .list({
          includeArchived: args.status === 'archived',
          ...(args.q === undefined ? {} : { query: args.q })
        })
        .filter((project) => {
          if (args.status === 'active') return project.status === 'active'
          if (args.status === 'archived') return project.status === 'archived'
          return project.status === 'active'
        })
      const window = all.slice(offset, offset + limit)
      const items = window.map(projectView)
      const next = offset + limit < all.length ? encodeCursor(offset + limit) : undefined
      return ok(next === undefined ? { items } : { items, next_cursor: next })
    }
  }),

  readTool({
    name: 'project_get',
    title: '项目详情',
    description: '返回一个项目的元数据、实体计数与数据目录状态。',
    inputSchema: { project_id: projectIdField },
    handler: (ctx, args) => {
      const service = projectService(ctx)
      const project = service.get(args.project_id)
      const stats = service.stats(args.project_id)
      let dirStatus: 'ok' | 'missing' = 'ok'
      try {
        service.projectDb(args.project_id)
      } catch {
        dirStatus = 'missing'
      }
      return ok({ project: projectView(project), counts: statsView(stats), dir_status: dirStatus })
    }
  }),

  readTool({
    name: 'project_overview',
    title: '项目总览',
    description: '返回项目总览聚合（焦点、活跃 Idea、近期实验、我的任务、近期笔记、文献、阻塞任务、周进度）。',
    inputSchema: { project_id: projectIdField },
    handler: (ctx, args) => {
      const snapshot = overviewService(ctx).load(args.project_id)
      return ok({ ...snapshot })
    }
  }),

  writeTool({
    name: 'project_create',
    title: '创建项目',
    description: '创建一个新项目。创建后可在应用内继续编辑焦点与设置。',
    inputSchema: {
      name: z.string().min(1).max(200).describe('项目名称'),
      summary: z.string().max(5000).optional().describe('项目摘要'),
      goals: z.string().max(100_000).optional().describe('项目目标')
    },
    handler: (ctx, args) => {
      const created = projectService(ctx).create(
        {
          name: args.name,
          ...(args.summary === undefined ? {} : { summary: args.summary }),
          ...(args.goals === undefined ? {} : { goals: args.goals })
        },
        'mcp'
      )
      return ok({ project: projectView(created) })
    }
  }),

  writeTool({
    name: 'project_update',
    title: '更新项目',
    description: '更新项目名称、摘要、目标或焦点。重命名不会改变项目目录名。',
    inputSchema: {
      project_id: projectIdField,
      expected_updated_at: z.string().max(64).optional().describe('并发令牌（可选）'),
      name: z.string().min(1).max(200).optional().describe('项目名称'),
      summary: z.string().max(5000).optional().describe('项目摘要'),
      goals: z.string().max(100_000).optional().describe('项目目标'),
      focus_md: z.string().max(20_000).optional().describe('当前焦点 Markdown')
    },
    handler: (ctx, args) => {
      const patch = {
        ...(args.name === undefined ? {} : { name: args.name }),
        ...(args.summary === undefined ? {} : { summary: args.summary }),
        ...(args.goals === undefined ? {} : { goals: args.goals }),
        ...(args.focus_md === undefined ? {} : { focusMd: args.focus_md })
      }
      if (Object.keys(patch).length === 0) {
        throw appError(AppErrorCode.InvalidArgument, '至少提供一个要更新的字段', {
          details: { field: 'patch' }
        })
      }
      const updated = projectService(ctx).update({
        id: args.project_id,
        patch,
        ...(args.expected_updated_at === undefined ? {} : { expectedUpdatedAt: args.expected_updated_at }),
        actor: 'mcp'
      })
      return ok({ project: projectView(updated) })
    }
  }),

  writeTool({
    name: 'project_archive',
    title: '归档项目',
    description: '归档项目：项目变为只读，数据与目录保留，可随时解除归档。',
    inputSchema: { project_id: projectIdField },
    handler: (ctx, args) => {
      const archived = projectService(ctx).archive(args.project_id, 'mcp')
      return ok({ project: projectView(archived) })
    }
  }),

  writeTool({
    name: 'project_unarchive',
    title: '解除归档',
    description: '解除项目归档，恢复读写。',
    inputSchema: { project_id: projectIdField },
    handler: (ctx, args) => {
      const active = projectService(ctx).unarchive(args.project_id, 'mcp')
      return ok({ project: projectView(active) })
    }
  }),

  confirmTool({
    name: 'project_delete',
    title: '永久删除项目',
    description:
      '请求永久删除项目及其数据。工具只计算影响并入队，必须在 ResearchOS 应用内人工批准；dry_run=true 只返回影响预览。',
    inputSchema: {
      project_id: projectIdField,
      dry_run: z.boolean().optional().describe('只返回影响预览，不入队')
    },
    handler: (ctx, args) => {
      const service = projectService(ctx)
      const plan = service.prepareDelete(args.project_id, { issueToken: false })
      const impact = deleteImpact(plan)
      if (args.dry_run === true) {
        return ok({ status: 'dry_run', project_id: args.project_id, impact })
      }
      const summary = `永久删除项目《${plan.projectName}》及其目录与数据库；共享文献保留，独占文献副本清理。`
      const record = confirmationQueue(ctx).enqueue(ctx.principal, {
        tool: 'project_delete',
        args: { project_id: args.project_id },
        argsHash: argsHash({ project_id: args.project_id }),
        title: '永久删除项目',
        summary,
        impact,
        projectId: args.project_id,
        traceId: ctx.traceId
      })
      return pending({
        project_id: args.project_id,
        confirmation_id: record.id,
        expires_at: record.expiresAt,
        title: '永久删除项目',
        summary,
        impact
      })
    }
  }),

  readTool({
    name: 'project_provenance',
    title: '科研脉络',
    description:
      '回放项目的科研脉络：Idea 演化森林与里程碑时间轴，可按时间、聚焦想法与核心里程碑过滤；format=markdown 时返回可直接写入论文的叙述摘要。只读。',
    inputSchema: {
      project_id: projectIdField,
      from: z.string().max(64).optional().describe('起始时间 RFC3339（含）'),
      until: z.string().max(64).optional().describe('结束时间 RFC3339（含）'),
      focus_idea_id: z.string().max(64).optional().describe('聚焦的 Idea ID：仅回放其前因后果链路'),
      milestones: z.boolean().optional().describe('仅保留核心里程碑，过滤日常编辑'),
      format: z.enum(['json', 'markdown']).optional().describe('输出格式')
    },
    handler: (ctx, args) => {
      const service = provenanceService(ctx)
      const from = parseArgTime(args.from, 'from')
      const until = parseArgTime(args.until, 'until')
      const report: ProvenanceReport = service.load({
        projectId: args.project_id,
        ...(from === undefined ? {} : { from }),
        ...(until === undefined ? {} : { to: until }),
        ...(args.focus_idea_id === undefined ? {} : { focusIdeaId: args.focus_idea_id }),
        ...(args.milestones === undefined ? {} : { milestones: args.milestones })
      })
      if (args.format === 'markdown') {
        return ok(
          { project_id: report.projectId, node_count: report.nodeCount },
          service.markdown(report)
        )
      }
      const structured: Record<string, unknown> = {
        project_id: report.projectId,
        project_name: report.projectName,
        generated_at: report.generatedAt,
        node_count: report.nodeCount,
        nodes: report.nodes.map(provenanceNodeView),
        lineage: { roots: report.lineage.roots.map(lineageNodeView) }
      }
      if (report.focusIdeaId !== null) {
        structured.focused_lineage = { roots: report.focusedLineage.roots.map(lineageNodeView) }
      }
      return ok(structured)
    }
  }),
  readTool({
    name: 'entity_context',
    title: '实体上下文',
    description: '聚合一个实体的反向链接、关联想法与证据、验证实验、文献与任务；可选返回 1~2 跳关系图。只读。',
    inputSchema: {
      project_id: z.string().min(1).max(64).describe('项目 ID'),
      entity_type: z
        .enum(['note', 'paper', 'idea', 'experiment', 'run', 'task', 'db_record'])
        .describe('实体类型'),
      entity_id: z.string().min(1).max(64).describe('实体 ID'),
      include_graph: z.boolean().optional().describe('同时返回局部关系图'),
      depth: z.number().int().min(1).max(2).optional().describe('关系图半径（1 或 2），默认 1')
    },
    handler: (ctx, args) => {
      const service = servicesOf(ctx).context
      if (service === undefined) throw missingService('context')
      const projectId = String(args.project_id)
      const target = { type: String(args.entity_type), id: String(args.entity_id) }
      const entity = service.load(projectId, target)
      const structured: Record<string, unknown> = {
        project_id: projectId,
        entity_type: target.type,
        entity_id: target.id,
        context: entity
      }
      if (args.include_graph === true) {
        structured.graph = service.graph(projectId, target, args.depth === 2 ? 2 : 1)
      }
      return ok(structured)
    }
  }),
  readTool({
    name: 'project_readiness_check',
    title: '完备度检查',
    description: '对验证中/已支持/部分支持的 Idea 运行 R-01…R-06 确定性完备度规则，返回缺口、建议任务标题与每个 Idea 的通过数。纯离线，不调用 AI，不写任务。',
    inputSchema: {
      project_id: z.string().min(1).max(64).describe('项目 ID'),
      idea_id: z.string().min(1).max(64).optional().describe('可选：只检查一个 Idea')
    },
    handler: (ctx, args) => {
      const service = servicesOf(ctx).readiness
      if (service === undefined) throw missingService('readiness')
      const report = service.check(String(args.project_id), args.idea_id === undefined ? undefined : String(args.idea_id))
      return ok(report as unknown as Record<string, unknown>)
    }
  })
]