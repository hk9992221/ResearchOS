import { z } from 'zod'

import {
  AppError,
  AppErrorCode,
  type JsonObject,
  type KanbanTask,
  type KanbanTaskSuggestion,
  type TaskDetail,
  type TaskPage,
  type TaskSummary
} from '@researchos/shared'

import { KanbanModule } from './module'
import type { KanbanEventWriter, KanbanRelations, KanbanTrash, TaskSuggester } from './deps'
import type { ChecklistItem, TaskColumnInfo } from '@researchos/shared'
import { Confirmations } from '../mcp/confirmations'
import { argsHash } from '../mcp/idempotency'
import { ok, pending, confirmTool, readTool, writeTool } from '../mcp/tools'
import type { ConfirmationEffect } from '../mcp/executors'
import type { HandlerContext, ToolDefinition } from '../mcp/types'

export interface KanbanToolDeps {
  /** Resolves the project module; `ctx.project` carries the open handle. */
  moduleFor(projectId: string, ctx: HandlerContext): KanbanModule
  /** AI readiness implementation (W3). Absent → `AI_UNAVAILABLE`, never a stub. */
  suggester?: TaskSuggester
}

/* ------------------------------------------------------------- helpers */

const id = z.string().min(1)
const projectId = z.string().min(1)
const priority = z.enum(['high', 'medium', 'low', 'none'])
const columnKind = z.enum(['backlog', 'todo', 'in_progress', 'review', 'done', 'custom'])

const MCP_ACTOR = 'mcp' as const

function moduleOf(deps: KanbanToolDeps, ctx: HandlerContext, project: string): KanbanModule {
  return deps.moduleFor(project, ctx)
}

function objectWithMethods(value: unknown, methods: readonly string[]): value is Record<string, unknown> {
  if (value === null || typeof value !== 'object') return false
  return methods.every((method) => typeof (value as Record<string, unknown>)[method] === 'function')
}

function projectStatusOf(ctx: HandlerContext, projectId: string): 'active' | 'archived' {
  const row = ctx.global.db.prepare('SELECT status FROM projects WHERE id = ?').get(projectId) as
    | { status: string }
    | undefined
  return row?.status === 'archived' ? 'archived' : 'active'
}

/**
 * Builds the module from the request context (open project handle + the
 * foundation services the host injects). This is what makes `kanbanTools`
 * usable without W17-specific wiring; it also honours an explicit
 * `ctx.services.kanban` module when integration provides one.
 */
export function kanbanModuleFromContext(ctx: HandlerContext): KanbanModule {
  const injected: unknown = ctx.services['kanban']
  if (injected !== null && typeof injected === 'object' && 'tasks' in injected && 'columns' in injected) {
    // The host injected a project-scoped module; structural check above.
    return injected as KanbanModule
  }
  if (ctx.project === null) {
    throw new AppError(AppErrorCode.NotFound, 'kanban tools require a project_id', { field: 'project_id' })
  }
  const events: unknown = ctx.services['events']
  if (!objectWithMethods(events, ['emit'])) {
    throw new AppError(AppErrorCode.Internal, 'kanban: event writer is not available', { port: 'events' })
  }
  const trash: unknown = ctx.services['trash']
  const relations: unknown = ctx.services['relations']
  return new KanbanModule({
    db: ctx.project.db,
    projectId: ctx.project.id,
    projectStatus: (projectId) => projectStatusOf(ctx, projectId),
    events: events as unknown as KanbanEventWriter,
    ...(objectWithMethods(trash, ['softDelete', 'restore'])
      ? { trash: trash as unknown as KanbanTrash }
      : {}),
    ...(objectWithMethods(relations, ['link', 'unlink', 'resolve', 'to'])
      ? { relations: relations as unknown as KanbanRelations }
      : {}),
    clock: ctx.clock
  })
}

/** `kanbanTools` resolves the project module from the request context. */
function kanbanFromServices(ctx: HandlerContext): KanbanModule {
  return kanbanModuleFromContext(ctx)
}

function suggesterOf(deps: KanbanToolDeps, ctx: HandlerContext): TaskSuggester | undefined {
  if (deps.suggester !== undefined) return deps.suggester
  const value: unknown = ctx.services['suggester']
  if (value === null || typeof value !== 'object' || !('available' in value) || !('suggest' in value)) return undefined
  return value as TaskSuggester
}

/* ------------------------------------------------------------ projections */

export function taskView(task: KanbanTask): Record<string, unknown> {
  return {
    task_id: task.id,
    title: task.title,
    column: task.column,
    parent_task_id: task.parentTaskId,
    description_md: task.descriptionMd,
    priority: task.priority,
    deadline: task.deadline,
    blocked: task.blocked,
    labels: task.labels,
    week_plan: task.weekPlan,
    sort_order: task.sortOrder,
    completed_at: task.completedAt,
    archived_at: task.archivedAt,
    deleted_at: task.deletedAt,
    created_at: task.createdAt,
    updated_at: task.updatedAt
  }
}

export function taskSummaryView(task: TaskSummary): Record<string, unknown> {
  return {
    task_id: task.id,
    title: task.title,
    column: task.column,
    parent_task_id: task.parentTaskId,
    priority: task.priority,
    deadline: task.deadline,
    blocked: task.blocked,
    labels: task.labels,
    week_plan: task.weekPlan,
    subtask_done: task.subtaskDone,
    subtask_total: task.subtaskTotal,
    checklist_done: task.checklistDone,
    checklist_total: task.checklistTotal,
    link_count: task.linkCount,
    completed_at: task.completedAt,
    archived_at: task.archivedAt,
    updated_at: task.updatedAt
  }
}

export function columnView(column: TaskColumnInfo): Record<string, unknown> {
  return { name: column.name, kind: column.kind, is_done: column.isDone, position: column.position }
}

export function checklistView(item: ChecklistItem): Record<string, unknown> {
  return {
    item_id: item.id,
    task_id: item.taskId,
    text: item.text,
    done: item.done,
    sort_order: item.sortOrder
  }
}

export function taskDetailView(detail: TaskDetail): Record<string, unknown> {
  return {
    ...taskView(detail),
    link_count: detail.links.length,
    column_detail: columnView(detail.columnInfo),
    subtasks: detail.subtasks.map(taskSummaryView),
    checklist: detail.checklist.map(checklistView),
    links: detail.links.map((link) => ({
      relation_id: link.relationId,
      relation: link.relation,
      dst_type: link.dstType,
      dst_id: link.dstId,
      dst_title: link.dstTitle,
      target_state: link.targetState
    }))
  }
}

function pageOf(page: TaskPage): Record<string, unknown> {
  return {
    items: page.items.map(taskSummaryView),
    next_cursor: page.nextCursor,
    total: page.total
  }
}

function suggestionView(suggestion: KanbanTaskSuggestion): Record<string, unknown> {
  return {
    temp_id: suggestion.tempId,
    title: suggestion.title,
    description: suggestion.description,
    priority: suggestion.priority,
    checklist: suggestion.checklist,
    target_entity: suggestion.targetEntity,
    source_rule: suggestion.sourceRule
  }
}

/* -------------------------------------------------------------- schemas */

const listSchema = z.strictObject({
  project_id: projectId,
  column_id: z.string().optional(),
  blocked: z.boolean().optional(),
  deadline_before: z.string().optional(),
  week_plan: z.string().optional(),
  q: z.string().optional(),
  include_archived: z.boolean().optional(),
  include_deleted: z.boolean().optional(),
  limit: z.number().int().min(1).max(200).optional(),
  cursor: z.string().optional()
})
const projectSchema = z.strictObject({ project_id: projectId })
const taskRefSchema = z.strictObject({ project_id: projectId, task_id: id })
const taskCreateSchema = z.strictObject({
  project_id: projectId,
  title: z.string().min(1).max(200),
  description_md: z.string().optional(),
  column_id: z.string().optional(),
  parent_id: z.string().optional(),
  priority: priority.optional(),
  deadline: z.string().optional(),
  labels: z.array(z.string().min(1).max(32)).optional()
})
const taskUpdateSchema = z.strictObject({
  project_id: projectId,
  task_id: id,
  expected_updated_at: z.string().optional(),
  title: z.string().min(1).max(200).optional(),
  description_md: z.string().optional(),
  priority: priority.optional(),
  deadline: z.string().optional(),
  blocked: z.boolean().optional(),
  labels: z.array(z.string().min(1).max(32)).optional(),
  column_id: z.string().optional()
})
const taskMoveSchema = z.strictObject({
  project_id: projectId,
  task_id: id,
  column_id: z.string().min(1),
  before_id: z.string().optional(),
  after_id: z.string().optional()
})
const columnCreateSchema = z.strictObject({
  project_id: projectId,
  name: z.string().min(1).max(40),
  kind: columnKind.optional(),
  is_done: z.boolean().optional(),
  after: z.string().optional()
})
const columnRenameSchema = z.strictObject({
  project_id: projectId,
  old_name: z.string().min(1),
  new_name: z.string().min(1).max(40)
})
const columnMoveSchema = z.strictObject({
  project_id: projectId,
  name: z.string().min(1),
  to_position: z.number().int().min(0)
})
const columnSetDoneSchema = z.strictObject({
  project_id: projectId,
  name: z.string().min(1),
  done: z.boolean()
})
const columnDeleteSchema = z.strictObject({
  project_id: projectId,
  name: z.string().min(1),
  migrate_to: z.string().min(1)
})
const checklistAddSchema = z.strictObject({
  project_id: projectId,
  task_id: id,
  text: z.string().min(1).max(500),
  before_id: z.string().optional()
})
const checklistUpdateSchema = z.strictObject({
  project_id: projectId,
  item_id: id,
  text: z.string().min(1).max(500)
})
const checklistSetDoneSchema = z.strictObject({
  project_id: projectId,
  item_id: id,
  done: z.boolean()
})
const checklistRemoveSchema = z.strictObject({ project_id: projectId, item_id: id })
const checklistReorderSchema = z.strictObject({
  project_id: projectId,
  task_id: id,
  ordered_ids: z.array(id)
})
const suggestSchema = z.strictObject({ project_id: projectId, idea_id: z.string().optional() })

/* -------------------------------------------------------------- factory */

/**
 * The 21 MCP tools (plan §1.4). `readTool`/`writeTool`/`confirmTool`
 * inject the required `idempotency_key` on W/C schemas (defect 5) and build
 * strict input objects; handlers never map a domain AppError to INTERNAL —
 * the host's single `fail()` does that.
 */
export function createKanbanTools(deps: KanbanToolDeps): ToolDefinition[] {
  return [
    readTool({
      name: 'task_list',
      title: '任务列表',
      description: '列出项目任务，可按列、阻塞、截止日期、周计划、关键字过滤。',
      inputSchema: listSchema.shape,
      handler(ctx, args) {
        const input = listSchema.parse(args)
        const page = moduleOf(deps, ctx, input.project_id).tasks.list({
          column: input.column_id,
          blocked: input.blocked,
          deadlineTo: input.deadline_before,
          weekPlan: input.week_plan,
          q: input.q,
          includeArchived: input.include_archived,
          includeDeleted: input.include_deleted,
          limit: input.limit,
          cursor: input.cursor
        })
        return ok(pageOf(page))
      }
    }),

    readTool({
      name: 'task_get',
      title: '任务详情',
      description: '返回任务、子任务、检查项、关系链接与所在列。',
      inputSchema: taskRefSchema.shape,
      handler(ctx, args) {
        const input = taskRefSchema.parse(args)
        return ok(taskDetailView(moduleOf(deps, ctx, input.project_id).tasks.get(input.task_id)))
      }
    }),

    writeTool({
      name: 'task_create',
      title: '创建任务',
      description: '创建任务。省略 column_id/parent_id 即为快速捕获（Inbox）。',
      inputSchema: taskCreateSchema.shape,
      handler(ctx, args) {
        const input = taskCreateSchema.parse(args)
        const task = moduleOf(deps, ctx, input.project_id).tasks.create(MCP_ACTOR, {
          projectId: input.project_id,
          title: input.title,
          column: input.column_id,
          parentTaskId: input.parent_id,
          descriptionMd: input.description_md,
          priority: input.priority,
          deadline: input.deadline ?? null,
          labels: input.labels
        })
        return ok({ task: taskView(task) })
      }
    }),

    writeTool({
      name: 'task_update',
      title: '更新任务',
      description: '部分更新任务。提供 expected_updated_at 时执行乐观并发检查；不一致返回 CONFLICT。',
      inputSchema: taskUpdateSchema.shape,
      handler(ctx, args) {
        const input = taskUpdateSchema.parse(args)
        const task = moduleOf(deps, ctx, input.project_id).tasks.update(MCP_ACTOR, {
          projectId: input.project_id,
          id: input.task_id,
          expectedUpdatedAt: input.expected_updated_at,
          patch: {
            title: input.title,
            descriptionMd: input.description_md,
            priority: input.priority,
            deadline: input.deadline === undefined ? undefined : input.deadline === '' ? null : input.deadline,
            blocked: input.blocked,
            labels: input.labels,
            column: input.column_id
          }
        })
        return ok({ task: taskView(task) })
      }
    }),

    writeTool({
      name: 'task_move',
      title: '移动任务',
      description: '把任务移动到目标列并可指定前后锚点。仅列变化时发 status.changed。',
      inputSchema: taskMoveSchema.shape,
      handler(ctx, args) {
        const input = taskMoveSchema.parse(args)
        const task = moduleOf(deps, ctx, input.project_id).tasks.move(MCP_ACTOR, {
          projectId: input.project_id,
          id: input.task_id,
          column: input.column_id,
          beforeId: input.before_id,
          afterId: input.after_id
        })
        return ok({ task: taskView(task) })
      }
    }),

    writeTool({
      name: 'task_archive',
      title: '归档任务',
      description: '归档任务（不级联子任务，不产生 Activity）。',
      inputSchema: taskRefSchema.shape,
      handler(ctx, args) {
        const input = taskRefSchema.parse(args)
        return ok({ task: taskView(moduleOf(deps, ctx, input.project_id).tasks.archive(MCP_ACTOR, input.task_id)) })
      }
    }),

    writeTool({
      name: 'task_unarchive',
      title: '解除任务归档',
      description: '解除任务归档。',
      inputSchema: taskRefSchema.shape,
      handler(ctx, args) {
        const input = taskRefSchema.parse(args)
        return ok({ task: taskView(moduleOf(deps, ctx, input.project_id).tasks.unarchive(MCP_ACTOR, input.task_id)) })
      }
    }),

    writeTool({
      name: 'task_delete',
      title: '删除任务',
      description: '软删除任务，级联其未删除的子任务。',
      inputSchema: taskRefSchema.shape,
      annotations: { destructiveHint: true },
      handler(ctx, args) {
        const input = taskRefSchema.parse(args)
        moduleOf(deps, ctx, input.project_id).tasks.softDelete(MCP_ACTOR, input.task_id)
        return ok({ deleted: true })
      }
    }),

    readTool({
      name: 'column_list',
      title: '看板列列表',
      description: '列出看板列（名称、类型、是否为完成列、位置）。',
      inputSchema: projectSchema.shape,
      handler(ctx, args) {
        const input = projectSchema.parse(args)
        return ok({ columns: moduleOf(deps, ctx, input.project_id).columns.list().map(columnView) })
      }
    }),

    writeTool({
      name: 'column_create',
      title: '新建看板列',
      description: '新建看板列，可指定插入位置与是否为完成列。',
      inputSchema: columnCreateSchema.shape,
      handler(ctx, args) {
        const input = columnCreateSchema.parse(args)
        const column = moduleOf(deps, ctx, input.project_id).columns.create(MCP_ACTOR, {
          projectId: input.project_id,
          name: input.name,
          kind: input.kind,
          isDone: input.is_done,
          after: input.after
        })
        return ok({ column: columnView(column) })
      }
    }),

    writeTool({
      name: 'column_rename',
      title: '重命名看板列',
      description: '重命名看板列；该列全部任务（含已删除）的列值同步更新。',
      inputSchema: columnRenameSchema.shape,
      handler(ctx, args) {
        const input = columnRenameSchema.parse(args)
        return ok({
          column: columnView(
            moduleOf(deps, ctx, input.project_id).columns.rename(MCP_ACTOR, input.old_name, input.new_name)
          )
        })
      }
    }),

    writeTool({
      name: 'column_move',
      title: '移动看板列',
      description: '调整列顺序（0 起始的目标位置）。',
      inputSchema: columnMoveSchema.shape,
      handler(ctx, args) {
        const input = columnMoveSchema.parse(args)
        return ok({
          columns: moduleOf(deps, ctx, input.project_id)
            .columns.move(MCP_ACTOR, input.name, input.to_position)
            .map(columnView)
        })
      }
    }),

    writeTool({
      name: 'column_set_done',
      title: '设置完成列',
      description: '标记或取消列的完成语义；不能移除最后一个完成列。',
      inputSchema: columnSetDoneSchema.shape,
      handler(ctx, args) {
        const input = columnSetDoneSchema.parse(args)
        return ok({
          column: columnView(
            moduleOf(deps, ctx, input.project_id).columns.setDone(MCP_ACTOR, input.name, input.done)
          )
        })
      }
    }),

    confirmTool({
      name: 'column_delete',
      title: '删除看板列',
      description: '请求删除看板列。任务必须迁移到 migrate_to 指定列；实际删除需在应用内人工批准。',
      inputSchema: columnDeleteSchema.shape,
      handler(ctx, args) {
        const input = columnDeleteSchema.parse(args)
        if (input.name.trim() === input.migrate_to.trim()) {
          throw new AppError(AppErrorCode.InvalidArgument, 'migrate_to 不能与要删除的列相同', {
            field: 'migrate_to'
          })
        }
        const module = moduleOf(deps, ctx, input.project_id)
        const page = module.tasks.list({ column: input.name, includeDeleted: true, limit: 200 })
        const impact: JsonObject = {
          column: input.name,
          migrate_to: input.migrate_to,
          task_count: page.total,
          count_at_least: false
        }
        const raw: unknown = ctx.services['confirmations']
        const confirmations = raw instanceof Confirmations ? raw : undefined
        if (confirmations === undefined) {
          throw new AppError(AppErrorCode.Internal, 'kanban: confirmation service is not configured', {
            port: 'confirmations'
          })
        }
        const summary = `删除列「${input.name}」并把 ${page.total} 个任务迁移到「${input.migrate_to}」。`
        const request: JsonObject = { project_id: input.project_id, name: input.name, migrate_to: input.migrate_to }
        const confirmation = confirmations.enqueue(ctx.principal, {
          tool: 'column_delete',
          args: request,
          argsHash: argsHash(request),
          title: '删除看板列',
          summary,
          impact,
          projectId: input.project_id,
          traceId: ctx.traceId
        })
        return pending({
          confirmation_id: confirmation.id,
          expires_at: confirmation.expiresAt,
          title: '删除看板列',
          summary,
          impact
        })
      }
    }),

    readTool({
      name: 'checklist_list',
      title: '检查项列表',
      description: '列出任务的检查项。',
      inputSchema: taskRefSchema.shape,
      handler(ctx, args) {
        const input = taskRefSchema.parse(args)
        return ok({
          items: moduleOf(deps, ctx, input.project_id).checklist.list(input.task_id).map(checklistView)
        })
      }
    }),

    writeTool({
      name: 'checklist_add',
      title: '添加检查项',
      description: '为任务添加检查项（不改变父任务 updated_at，不产生 Activity）。',
      inputSchema: checklistAddSchema.shape,
      handler(ctx, args) {
        const input = checklistAddSchema.parse(args)
        return ok({
          item: checklistView(
            moduleOf(deps, ctx, input.project_id).checklist.add(MCP_ACTOR, input.task_id, input.text, input.before_id)
          )
        })
      }
    }),

    writeTool({
      name: 'checklist_update',
      title: '修改检查项',
      description: '修改检查项文本。',
      inputSchema: checklistUpdateSchema.shape,
      handler(ctx, args) {
        const input = checklistUpdateSchema.parse(args)
        return ok({
          item: checklistView(
            moduleOf(deps, ctx, input.project_id).checklist.update(MCP_ACTOR, input.item_id, input.text)
          )
        })
      }
    }),

    writeTool({
      name: 'checklist_set_done',
      title: '勾选检查项',
      description: '设置检查项完成状态。',
      inputSchema: checklistSetDoneSchema.shape,
      handler(ctx, args) {
        const input = checklistSetDoneSchema.parse(args)
        return ok({
          item: checklistView(
            moduleOf(deps, ctx, input.project_id).checklist.setDone(MCP_ACTOR, input.item_id, input.done)
          )
        })
      }
    }),

    writeTool({
      name: 'checklist_remove',
      title: '删除检查项',
      description: '物理删除检查项。',
      inputSchema: checklistRemoveSchema.shape,
      annotations: { destructiveHint: true },
      handler(ctx, args) {
        const input = checklistRemoveSchema.parse(args)
        moduleOf(deps, ctx, input.project_id).checklist.remove(MCP_ACTOR, input.item_id)
        return ok({ removed: true })
      }
    }),

    writeTool({
      name: 'checklist_reorder',
      title: '重排检查项',
      description: '按给定顺序重排任务的检查项（必须覆盖全部检查项）。',
      inputSchema: checklistReorderSchema.shape,
      handler(ctx, args) {
        const input = checklistReorderSchema.parse(args)
        return ok({
          items: moduleOf(deps, ctx, input.project_id)
            .checklist.reorder(MCP_ACTOR, input.task_id, input.ordered_ids)
            .map(checklistView)
        })
      }
    }),

    readTool({
      name: 'kanban_suggest_tasks',
      title: 'AI 任务建议',
      description:
        '根据确定性完备度缺口、Idea 描述、实验 Limitations 与笔记 TODO 生成补充任务建议。只返回预览，绝不写入 tasks 表；客户端确认后用 kanban_task_create 创建。需要已配置 chat_main。',
      inputSchema: suggestSchema.shape,
      async handler(ctx, args) {
        const input = suggestSchema.parse(args)
        const suggester = suggesterOf(deps, ctx)
        if (suggester === undefined || !suggester.available()) {
          throw new AppError(AppErrorCode.AiUnavailable, '未配置 chat_main 模型，无法生成 AI 建议', {
            reason: 'chat_main-missing'
          })
        }
        const suggestions = await suggester.suggest(input.project_id, { ideaId: input.idea_id })
        return ok({
          output_id: suggestions.outputId,
          suggestions: suggestions.suggestions.map(suggestionView),
          gaps: suggestions.gaps,
          note: suggestions.note
        })
      }
    })
  ]
}

/**
 * Ready-to-aggregate array: the MCP host puts the project-scoped module in
 * `ctx.services.kanban`, so the catalog needs no extra wiring.
 */
export const kanbanTools: ToolDefinition[] = createKanbanTools({
  moduleFor: (_projectId, ctx) => kanbanFromServices(ctx)
})

/**
 * Desktop executor for `column_delete` (W4 `DomainConfirmationEffects`):
 * approval runs the same structural migration the UI uses and returns the
 * moved-task count as the confirmation result.
 */
export function createColumnDeleteEffect(resolve: (projectId: string) => KanbanModule): ConfirmationEffect {
  return async ({ args }) => {
    const projectIdValue = args['project_id']
    const name = args['name']
    const migrateTo = args['migrate_to']
    if (typeof projectIdValue !== 'string' || typeof name !== 'string' || typeof migrateTo !== 'string') {
      throw new AppError(AppErrorCode.InvalidArgument, 'column_delete confirmation args are incomplete', {
        field: 'project_id|name|migrate_to'
      })
    }
    const result = resolve(projectIdValue).columns.delete('user', name, migrateTo)
    return { deleted: result.deleted, moved_tasks: result.movedTasks }
  }
}

/** Stable registration order and names (catalog snapshot test). */
export const KANBAN_TOOL_NAMES = [
  'task_list',
  'task_get',
  'task_create',
  'task_update',
  'task_move',
  'task_archive',
  'task_unarchive',
  'task_delete',
  'column_list',
  'column_create',
  'column_rename',
  'column_move',
  'column_set_done',
  'column_delete',
  'checklist_list',
  'checklist_add',
  'checklist_update',
  'checklist_set_done',
  'checklist_remove',
  'checklist_reorder',
  'kanban_suggest_tasks'
] as const