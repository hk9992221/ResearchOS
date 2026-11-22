/**
 * Activity, relation and trash tools. Every write goes
 * through the W1 foundation services with `actor='mcp'`; the adapter only shapes
 * arguments and projections.
 */
import { z } from 'zod'

import type { JsonObject } from '@researchos/shared'
import { AppErrorCode } from '@researchos/shared'

import { type Clock, formatTimestamp } from '../../../clock'
import type { ConfirmService } from '../../../confirm'
import type { Activity, ActivityQuery, EventBus } from '../../../events'
import { appError } from '../../../errors'
import { isValidId } from '../../../ids'
import type { RelationLink, RelationService } from '../../../relations'
import { isActor, isEntityType, isEventType, isRelation } from '../../../schema'
import type { EntityType, Relation } from '../../../schema'
import { TrashService } from '../../../trash'
import type { TrashItem } from '../../../trash'
import type { WorkspaceManager } from '../../../workspace'
import type { Confirmations } from '../confirmations'
import { argsHash } from '../idempotency'
import { confirmTool, ok, pending, readTool, writeTool } from '../tools'
import type { HandlerContext, ToolDefinition } from '../types'

export interface MiscToolDeps {
  workspace: WorkspaceManager
  events: EventBus
  confirm: ConfirmService
  confirmations: Confirmations
  clock: Clock
}

const RELATION_TYPES = [
  'references',
  'derived_from',
  'validates',
  'deliverable_of',
  'related_to'
] as const

/** Table/column used to project a relation counterpart's title. */
const TITLE_LOOKUP: Readonly<
  Record<string, { table: string; column: string; global?: true; deleted?: string }>
> = {
  project: { table: 'projects', column: 'name', global: true },
  paper: { table: 'papers', column: 'title', global: true },
  note: { table: 'notes', column: 'title', deleted: 'deleted_at' },
  research_database: { table: 'research_databases', column: 'name', deleted: 'deleted_at' },
  db_field: { table: 'db_fields', column: 'name', deleted: 'deleted_at' },
  db_record: { table: 'db_records', column: 'title', deleted: 'deleted_at' },
  idea: { table: 'ideas', column: 'title', deleted: 'deleted_at' },
  evidence: { table: 'evidence', column: 'content', deleted: 'deleted_at' },
  experiment: { table: 'experiments', column: 'title', deleted: 'deleted_at' },
  run: { table: 'runs', column: 'title', deleted: 'deleted_at' },
  task: { table: 'tasks', column: 'title', deleted: 'deleted_at' },
  weekly_report: { table: 'weekly_reports', column: 'title', deleted: 'deleted_at' }
}

function requireKey(projectId: string | null): string {
  if (projectId === null || !isValidId(projectId)) {
    throw appError(AppErrorCode.NotFound, `项目 ${String(projectId)} 不存在`)
  }
  return projectId
}

function parseRfc3339(value: string, field: string): Date {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    throw appError(AppErrorCode.InvalidArgument, `${field} 必须是 RFC3339 时间`, { details: { field } })
  }
  return date
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
    throw appError(AppErrorCode.InvalidArgument, '分页游标无效', { details: { field: 'cursor' } })
  }
  if (!text.startsWith('o:')) {
    throw appError(AppErrorCode.InvalidArgument, '分页游标无效', { details: { field: 'cursor' } })
  }
  const offset = Number(text.slice(2))
  if (!Number.isInteger(offset) || offset < 0) {
    throw appError(AppErrorCode.InvalidArgument, '分页游标无效', { details: { field: 'cursor' } })
  }
  return offset
}

function entityTypeOf(value: string, field: string): EntityType {
  if (!isEntityType(value)) {
    throw appError(AppErrorCode.InvalidArgument, `不支持的实体类型 ${value}`, { details: { field } })
  }
  return value
}

function relationOf(value: string, field: string): Relation {
  if (!isRelation(value)) {
    throw appError(AppErrorCode.InvalidArgument, `不支持的关系类型 ${value}`, { details: { field } })
  }
  return value
}

function counterpartTitle(
  ctx: HandlerContext,
  type: EntityType,
  id: string
): { title: string; deleted: boolean; missing: boolean } {
  const lookup = TITLE_LOOKUP[type]
  if (lookup === undefined) return { title: '', deleted: false, missing: true }
  const db = lookup.global === true ? ctx.global.db : ctx.project?.db
  if (db === undefined) return { title: '', deleted: false, missing: true }
  const deletedSelect = lookup.deleted === undefined ? '' : `, ${lookup.deleted} AS deleted`
  try {
    const row = db
      .prepare(`SELECT ${lookup.column} AS title${deletedSelect} FROM ${lookup.table} WHERE id = ?`)
      .get(id) as { title: string | null; deleted?: string | null } | undefined
    if (row === undefined) return { title: '', deleted: false, missing: true }
    return {
      title: (row.title ?? '').slice(0, 200),
      deleted: lookup.deleted === undefined ? false : row.deleted !== null && row.deleted !== undefined,
      missing: false
    }
  } catch {
    return { title: '', deleted: false, missing: true }
  }
}

function linkView(ctx: HandlerContext, link: RelationLink, counterpart: 'src' | 'dst') {
  const type = counterpart === 'src' ? link.srcType : link.dstType
  const id = counterpart === 'src' ? link.srcId : link.dstId
  const info = counterpartTitle(ctx, type, id)
  return {
    src_type: link.srcType,
    src_id: link.srcId,
    relation: link.relation,
    dst_type: link.dstType,
    dst_id: link.dstId,
    title: info.title,
    deleted: info.deleted,
    missing: info.missing
  }
}

function relationArgs(args: Record<string, unknown>, withRelationId: boolean): RelationLink {
  const read = (field: string): string => {
    const value = args[field]
    if (typeof value !== 'string' || value === '') {
      throw appError(AppErrorCode.InvalidArgument, `${field} 必填`, { details: { field } })
    }
    return value
  }
  if (withRelationId) read('relation_id')
  return {
    srcType: entityTypeOf(read('src_type'), 'src_type'),
    srcId: read('src_id'),
    relation: relationOf(read('relation'), 'relation'),
    dstType: entityTypeOf(read('dst_type'), 'dst_type'),
    dstId: read('dst_id')
  }
}

function trashCounts(ctx: HandlerContext): { counts: Record<string, number>; oldest: Date | null } {
  requireKey(ctx.projectId)
  const trash = ctx.services['trash'] as TrashService
  const counts: Record<string, number> = {}
  let oldest: Date | null = null
  let offset = 0
  for (;;) {
    const items = trash.list({ limit: 500, offset })
    for (const item of items) {
      counts[item.entityType] = (counts[item.entityType] ?? 0) + 1
      if (oldest === null || item.deletedAt.getTime() < oldest.getTime()) oldest = item.deletedAt
    }
    if (items.length < 500) break
    offset += 500
  }
  return { counts, oldest }
}

function sameCounts(expected: Record<string, unknown>, actual: Record<string, number>): boolean {
  const expectedKeys = Object.keys(expected)
  if (expectedKeys.length !== Object.keys(actual).length) return false
  return expectedKeys.every((key) => {
    const value = expected[key]
    return typeof value === 'number' && Number.isInteger(value) && actual[key] === value
  })
}

export function createMiscTools(deps: MiscToolDeps): ToolDefinition[] {
  const relationsOf = (ctx: HandlerContext): RelationService =>
    ctx.services['relations'] as RelationService
  const trashOf = (ctx: HandlerContext): TrashService => ctx.services['trash'] as TrashService

  return [
    readTool({
      name: 'activity_list',
      title: '活动列表',
      description:
        '读取项目 Activity（只读，按时间倒序）。MCP 与 AI 的写入分别以 actor=mcp / ai 记录。',
      inputSchema: {
        project_id: z.string().describe('项目 ID'),
        since: z.string().optional().describe('起始时间 RFC3339（含）'),
        until: z.string().optional().describe('结束时间 RFC3339（含）'),
        event_types: z.array(z.string()).optional().describe('事件类型'),
        actor: z.enum(['user', 'mcp', 'ai']).optional().describe('操作者'),
        entity_type: z.string().optional().describe('实体类型'),
        entity_id: z.string().optional().describe('实体 ID'),
        limit: z.number().int().min(1).max(200).optional().describe('返回条数，默认 50，上限 200'),
        cursor: z.string().optional().describe('上一页返回的 next_cursor')
      },
      handler: (ctx, args) => {
        requireKey(ctx.projectId)
        const offset = decodeCursor(args.cursor)
        const limit = args.limit ?? 50
        const query: ActivityQuery = { limit, offset }
        if (args.since !== undefined) query.since = parseRfc3339(args.since, 'since')
        if (args.until !== undefined) query.until = parseRfc3339(args.until, 'until')
        if (args.event_types !== undefined) {
          query.eventTypes = args.event_types.map((value) => {
            if (!isEventType(value)) {
              throw appError(AppErrorCode.InvalidArgument, `未知事件类型 ${value}`, {
                details: { field: 'event_types' }
              })
            }
            return value
          })
        }
        if (args.actor !== undefined) {
          if (!isActor(args.actor)) {
            throw appError(AppErrorCode.InvalidArgument, `未知操作者 ${args.actor}`, {
              details: { field: 'actor' }
            })
          }
          query.actors = [args.actor]
        }
        if (args.entity_type !== undefined && args.entity_type !== '') {
          query.entityTypes = [entityTypeOf(args.entity_type, 'entity_type')]
        }
        if (args.entity_id !== undefined) query.entityId = args.entity_id

        const handle = ctx.project!
        const items = deps.events.list(handle, query)
        const total = deps.events.count(handle, query)
        const result: Record<string, unknown> = { items: items.map(activityView) }
        if (offset + limit < total) result.next_cursor = encodeCursor(offset + limit)
        return ok(result)
      }
    }),

    readTool({
      name: 'relation_list',
      title: '关系列表',
      description: '列出一个实体的关系（按来源或目标）。',
      inputSchema: {
        project_id: z.string().describe('项目 ID'),
        src_type: z.string().optional().describe('来源实体类型'),
        src_id: z.string().optional().describe('来源实体 ID'),
        dst_type: z.string().optional().describe('目标实体类型'),
        dst_id: z.string().optional().describe('目标实体 ID'),
        relation: z.enum(RELATION_TYPES).optional().describe('关系类型')
      },
      handler: (ctx, args) => {
        requireKey(ctx.projectId)
        const service = relationsOf(ctx)
        const relation = args.relation === undefined ? undefined : relationOf(args.relation, 'relation')
        let links: RelationLink[]
        let counterpart: 'src' | 'dst'
        if (args.src_id !== undefined && args.src_id !== '') {
          const type = entityTypeOf(args.src_type ?? '', 'src_type')
          links = service.from(type, args.src_id, relation)
          counterpart = 'dst'
        } else if (args.dst_id !== undefined && args.dst_id !== '') {
          const type = entityTypeOf(args.dst_type ?? '', 'dst_type')
          links = service.to(type, args.dst_id, relation)
          counterpart = 'src'
        } else {
          throw appError(AppErrorCode.InvalidArgument, '必须提供 src_id 或 dst_id')
        }
        return ok({ relations: links.map((link) => linkView(ctx, link, counterpart)) })
      }
    }),

    writeTool({
      name: 'relation_add',
      title: '添加关系',
      description: '在两个实体间添加关系。仅接受注册表内的关系类型；跨项目关系被拒绝。',
      inputSchema: {
        project_id: z.string().describe('项目 ID'),
        src_type: z.string().describe('来源实体类型'),
        src_id: z.string().describe('来源实体 ID'),
        relation: z.enum(RELATION_TYPES).describe('关系类型'),
        dst_type: z.string().describe('目标实体类型'),
        dst_id: z.string().describe('目标实体 ID')
      },
      handler: (ctx, args) => {
        requireKey(ctx.projectId)
        const link = relationArgs(args as unknown as Record<string, unknown>, false)
        const service = relationsOf(ctx)
        const id = ctx.project!.inTx(() => service.link(ctx.project!, link, 'mcp'))
        return ok({ added: true, relation_id: id })
      }
    }),

    writeTool({
      name: 'relation_remove',
      title: '移除关系',
      description:
        '移除两个实体间的关系。服务层没有按关系 ID 删除的入口，因此除 relation_id 外必须同时提供关系三元组。',
      inputSchema: {
        project_id: z.string().describe('项目 ID'),
        relation_id: z.string().describe('关系 ID（用于审计回显）'),
        src_type: z.string().describe('来源实体类型'),
        src_id: z.string().describe('来源实体 ID'),
        relation: z.enum(RELATION_TYPES).describe('关系类型'),
        dst_type: z.string().describe('目标实体类型'),
        dst_id: z.string().describe('目标实体 ID')
      },
      handler: (ctx, args) => {
        requireKey(ctx.projectId)
        const link = relationArgs(args as unknown as Record<string, unknown>, true)
        const service = relationsOf(ctx)
        ctx.project!.inTx(() => service.unlink(ctx.project!, link, 'mcp'))
        return ok({ removed: true })
      }
    }),

    readTool({
      name: 'trash_list',
      title: '回收站列表',
      description: '列出回收站内容。省略 project_id 时聚合所有项目。',
      inputSchema: {
        project_id: z.string().optional().describe('项目 ID（可选）')
      },
      handler: (ctx) => {
        const items: Array<Record<string, unknown>> = []
        if (ctx.projectId !== null && ctx.projectId !== '') {
          const trash = trashOf(ctx)
          collectTrash(ctx.projectId, trash, items)
          return ok({ items })
        }
        const ids = deps.workspace.global.db
          .prepare('SELECT id FROM projects ORDER BY id')
          .pluck()
          .all() as string[]
        for (const id of ids) {
          let handle
          try {
            handle = deps.workspace.project(id)
          } catch {
            continue
          }
          const trash = new TrashService(handle, deps.events, deps.confirm, { clock: deps.clock })
          collectTrash(id, trash, items)
        }
        return ok({ items })
      }
    }),

    writeTool({
      name: 'trash_restore',
      title: '从回收站恢复',
      description: '恢复一个软删除实体。归档项目拒绝恢复，需先解归档。',
      inputSchema: {
        project_id: z.string().describe('项目 ID'),
        entity_type: z.string().describe('实体类型'),
        entity_id: z.string().describe('实体 ID')
      },
      handler: (ctx, args) => {
        requireKey(ctx.projectId)
        const type = entityTypeOf(args.entity_type, 'entity_type')
        const trash = trashOf(ctx)
        ctx.project!.inTx(() => trash.restore(ctx.project!, { type, id: args.entity_id }, 'mcp'))
        return ok({ restored: true })
      }
    }),

    readTool({
      name: 'trash_empty_preview',
      title: '清空回收站预览',
      description: '返回回收站按类型计数与最早删除时间。清空前必须用该结果作为 expected_counts。',
      inputSchema: {
        project_id: z.string().describe('项目 ID')
      },
      handler: (ctx) => {
        const { counts, oldest } = trashCounts(ctx)
        return ok({
          counts,
          oldest_deleted_at: oldest === null ? null : formatTimestamp(oldest)
        })
      }
    }),

    confirmTool({
      name: 'trash_empty',
      title: '清空回收站',
      description:
        '请求永久清空项目回收站。必须携带预览得到的 expected_counts；执行前重算，不一致则冲突。实际清空需在应用内人工批准。',
      inputSchema: {
        project_id: z.string().describe('项目 ID'),
        expected_counts: z.record(z.string(), z.number().int()).describe('预览得到的 {entity_type: count}'),
        dry_run: z.boolean().optional().describe('只返回预览，不入队')
      },
      handler: (ctx, args) => {
        requireKey(ctx.projectId)
        const { counts, oldest } = trashCounts(ctx)
        if (!sameCounts(args.expected_counts, counts)) {
          throw appError(AppErrorCode.Conflict, '回收站内容已变化，请重新预览后再确认', {
            details: { actual: counts }
          })
        }
        const oldestText = oldest === null ? null : formatTimestamp(oldest)
        if (args.dry_run === true) {
          return ok({ status: 'dry_run', counts, oldest_deleted_at: oldestText })
        }
        const total = Object.values(counts).reduce((sum, value) => sum + value, 0)
        const impact = { counts, oldest_deleted_at: oldestText }
        const created = deps.confirmations.enqueue(ctx.principal, {
          tool: 'trash_empty',
          args: args as unknown as JsonObject,
          argsHash: argsHash(args as unknown as JsonObject),
          title: '清空回收站',
          summary: `永久删除回收站中的 ${total} 个实体；此操作不可撤销。`,
          impact,
          projectId: ctx.projectId,
          traceId: ctx.traceId
        })
        return pending({
          confirmation_id: created.id,
          expires_at: created.expiresAt,
          title: created.title,
          summary: created.summary,
          impact,
          project_id: ctx.projectId
        })
      }
    })
  ]
}

function activityView(activity: Activity): Record<string, unknown> {
  return {
    activity_id: activity.id,
    event_type: activity.type,
    entity_type: activity.entityType,
    entity_id: activity.entityId,
    payload: activity.payload,
    actor: activity.actor,
    created_at: activity.createdAtText
  }
}

function collectTrash(
  projectId: string,
  trash: TrashService,
  out: Array<Record<string, unknown>>
): void {
  let offset = 0
  for (;;) {
    const items = trash.list({ limit: 500, offset })
    for (const item of items) out.push(trashView(projectId, item))
    if (items.length < 500) break
    offset += 500
  }
}

function trashView(projectId: string, item: TrashItem): Record<string, unknown> {
  return {
    project_id: projectId,
    entity_type: item.entityType,
    entity_id: item.id,
    title: item.title,
    deleted_at: formatTimestamp(item.deletedAt)
  }
}