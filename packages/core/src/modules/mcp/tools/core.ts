/**
 * The always-available runtime tools: server status
 * and the job/confirmation handles. There is deliberately no confirmation
 * approve tool — approval is desktop-only.
 */
import { z } from 'zod'

import type { JsonObject, McpConfirmation, McpJob } from '@researchos/shared'

import type { Confirmations } from '../confirmations'
import type { Jobs } from '../jobs'
import { ok, readTool, writeTool } from '../tools'
import type { ToolDefinition } from '../types'

export interface ServerStatusValues {
  app_version: string
  mcp_versions: string[]
  access_mode: string
  workspace_root: string
  modules: string[]
  ai_available: boolean
  zotero_available: boolean
  transports: string[]
  tool_count: number
}

export interface CoreToolDeps {
  status: () => Promise<ServerStatusValues>
  confirmations: Confirmations
  jobs: Jobs
}

const JOB_STATES = ['queued', 'running', 'succeeded', 'failed', 'cancelled'] as const
const CONFIRMATION_STATES = [
  'requested',
  'approved',
  'executing',
  'executed',
  'failed',
  'denied',
  'expired',
  'cancelled'
] as const

function optional<T>(key: string, value: T | null | undefined): Record<string, T> {
  return value === null || value === undefined ? {} : { [key]: value }
}

export function confirmationView(confirmation: McpConfirmation): Record<string, unknown> {
  return {
    confirmation_id: confirmation.id,
    tool: confirmation.tool,
    title: confirmation.title,
    summary: confirmation.summary,
    note: confirmation.note,
    state: confirmation.state,
    client_id: confirmation.clientId,
    created_at: confirmation.createdAt,
    expires_at: confirmation.expiresAt,
    resolved_at: confirmation.resolvedAt,
    resolved_by: confirmation.resolvedBy,
    ...optional('project_id', confirmation.projectId),
    ...optional('impact', confirmation.impact),
    ...optional('result', confirmation.result),
    ...optional('error_code', confirmation.errorCode)
  }
}

export function jobView(job: McpJob, detail: boolean): Record<string, unknown> {
  return {
    job_id: job.id,
    tool: job.tool,
    state: job.state,
    progress: job.progress,
    stage: job.stage,
    read_only: job.readOnly,
    created_at: job.createdAt,
    started_at: job.startedAt,
    finished_at: job.finishedAt,
    ...optional('project_id', job.projectId),
    ...(detail ? optional('result', job.result) : {}),
    ...(detail ? optional('error', job.error) : {})
  }
}

export function createCoreTools(deps: CoreToolDeps): ToolDefinition[] {
  return [
    readTool({
      name: 'server_status',
      title: '服务器状态',
      description:
        '返回 ResearchOS MCP 服务器的版本、协议版本、访问模式、工作区路径、已就绪模块与 AI/Zotero 可用性。',
      inputSchema: {},
      handler: async () => ok((await deps.status()) as unknown as Record<string, unknown>)
    }),

    readTool({
      name: 'confirmation_list',
      title: '确认请求列表',
      description:
        '列出当前客户端提交的确认请求（可按状态、项目过滤），按创建时间倒序。MCP 只能查看与取消，不能批准。',
      inputSchema: {
        state: z.enum(CONFIRMATION_STATES).optional().describe('确认状态'),
        project_id: z.string().optional().describe('项目 ID'),
        limit: z.number().int().min(1).max(200).optional().describe('返回条数，默认 50，上限 200')
      },
      handler: (ctx, args) => {
        const list = deps.confirmations.list(ctx.principal, {
          ...(args.state === undefined ? {} : { state: args.state }),
          ...(args.project_id === undefined ? {} : { projectId: args.project_id }),
          ...(args.limit === undefined ? {} : { limit: args.limit })
        })
        return ok({ confirmations: list.map(confirmationView) })
      }
    }),

    readTool({
      name: 'confirmation_get',
      title: '确认请求详情',
      description: '返回一个确认请求的状态、影响摘要、结果或错误。该工具只读；MCP 不存在批准路径。',
      inputSchema: {
        confirmation_id: z.string().describe('确认 ID')
      },
      handler: (ctx, args) => ok(confirmationView(deps.confirmations.get(ctx.principal, args.confirmation_id)))
    }),

    writeTool({
      name: 'confirmation_cancel',
      title: '取消确认请求',
      description:
        '取消本客户端发起、仍处于 requested 状态的确认请求。已处理或属于其他客户端的请求返回冲突。',
      inputSchema: {
        confirmation_id: z.string().describe('确认 ID')
      },
      handler: (ctx, args) => {
        const cancelled = deps.confirmations.cancelByClient(ctx.principal, args.confirmation_id)
        return ok({ confirmation_id: cancelled.id, state: 'cancelled' })
      }
    }),

    readTool({
      name: 'job_list',
      title: '后台作业列表',
      description: '列出当前客户端提交的后台作业（可按项目、状态过滤），按创建时间倒序。',
      inputSchema: {
        project_id: z.string().optional().describe('项目 ID'),
        state: z.enum(JOB_STATES).optional().describe('作业状态'),
        limit: z.number().int().min(1).max(200).optional().describe('返回条数，默认 50，上限 200')
      },
      handler: (ctx, args) => {
        const list = deps.jobs.list(ctx.principal, {
          ...(args.project_id === undefined ? {} : { projectId: args.project_id }),
          ...(args.state === undefined ? {} : { state: args.state }),
          ...(args.limit === undefined ? {} : { limit: args.limit })
        })
        return ok({ jobs: list.map((job) => jobView(job, false)) })
      }
    }),

    readTool({
      name: 'job_get',
      title: '后台作业详情',
      description: '返回后台作业的状态、进度、阶段、结果或错误。',
      inputSchema: {
        job_id: z.string().describe('作业 ID')
      },
      handler: (ctx, args) => ok(jobView(deps.jobs.get(ctx.principal, args.job_id), true))
    }),

    writeTool({
      name: 'job_cancel',
      title: '取消后台作业',
      description:
        '请求取消一个排队或运行中的后台作业。取消是尽力而为：已提交的文件写入不可回滚，但不会留下半成品业务实体。',
      inputSchema: {
        job_id: z.string().describe('作业 ID')
      },
      handler: (ctx, args) => {
        deps.jobs.cancel(ctx.principal, args.job_id)
        return ok({ job_id: args.job_id, state: 'cancelling' })
      }
    })
  ]
}

/** The job view used by IPC handlers as well (shared projection). */
export type { JsonObject }