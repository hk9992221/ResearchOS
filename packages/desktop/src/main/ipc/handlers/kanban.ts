import {
  AppError,
  AppErrorCode,
  type ActivityActor,
  type CommandMap,
  type CommandValidator,
  type KanbanTaskSuggestions
} from '@researchos/shared'
import type { KanbanModule } from '@researchos/core/modules/kanban/index'
import { kanbanValidators, type KanbanChannel } from '@researchos/core/validation/kanban'

import type { CommandRouter } from '../router'

export interface KanbanHandlerDeps {
  /** Opens (or returns the cached) project module; archived state is enforced by the services. */
  resolve(projectId: string): KanbanModule
  /** Readiness/AI preview (W3). Absent → `AI_UNAVAILABLE`, never a stub. */
  suggestTasks?(projectId: string, ideaId?: string): Promise<KanbanTaskSuggestions>
  actor?: ActivityActor
}

/**
 * Registers all `kanban.*` IPC commands. The router validates with the zod
 * schema from `@researchos/core/validation/kanban` before dispatch; AppError
 * passes through unchanged (the router serializes it), unexpected errors get
 * a trace id there.
 */
export function registerKanbanHandlers(router: CommandRouter, deps: KanbanHandlerDeps): void {
  const actor: ActivityActor = deps.actor ?? 'user'

  function register<C extends KanbanChannel>(
    channel: C,
    handler: (request: CommandMap[C]['req']) => Promise<CommandMap[C]['res']> | CommandMap[C]['res']
  ): void {
    // The validator map is typed per channel and CommandValidator is the
    // structural subset the router stores; this bridge loses nothing.
    router.registerHandler(
      channel,
      kanbanValidators[channel] as CommandValidator<CommandMap[C]['req']>,
      (request) => handler(request)
    )
  }

  register('kanban.columns.list', (request) => deps.resolve(request.projectId).columns.list())
  register('kanban.columns.create', (request) =>
    deps.resolve(request.projectId).columns.create(actor, request)
  )
  register('kanban.columns.rename', (request) =>
    deps.resolve(request.projectId).columns.rename(actor, request.name, request.newName)
  )
  register('kanban.columns.move', (request) =>
    deps.resolve(request.projectId).columns.move(actor, request.name, request.position)
  )
  register('kanban.columns.setDone', (request) =>
    deps.resolve(request.projectId).columns.setDone(actor, request.name, request.isDone)
  )
  register('kanban.columns.delete', (request) =>
    deps.resolve(request.projectId).columns.delete(actor, request.name, request.migrateTo)
  )

  register('kanban.tasks.list', (request) =>
    deps.resolve(request.projectId).tasks.list(withoutProject(request))
  )
  register('kanban.tasks.get', (request) => deps.resolve(request.projectId).tasks.get(request.id))
  register('kanban.tasks.create', (request) =>
    deps.resolve(request.projectId).tasks.create(actor, request)
  )
  register('kanban.tasks.update', (request) =>
    deps.resolve(request.projectId).tasks.update(actor, request)
  )
  register('kanban.tasks.move', (request) =>
    deps.resolve(request.projectId).tasks.move(actor, request)
  )
  register('kanban.tasks.complete', (request) =>
    deps.resolve(request.projectId).tasks.complete(actor, request.id, request.done)
  )
  register('kanban.tasks.archive', (request) =>
    deps.resolve(request.projectId).tasks.archive(actor, request.id)
  )
  register('kanban.tasks.unarchive', (request) =>
    deps.resolve(request.projectId).tasks.unarchive(actor, request.id)
  )
  register('kanban.tasks.delete', (request) => {
    deps.resolve(request.projectId).tasks.softDelete(actor, request.id)
    return { deleted: true }
  })
  register('kanban.tasks.restore', (request) =>
    deps.resolve(request.projectId).tasks.restore(actor, request.id)
  )

  register('kanban.checklist.list', (request) =>
    deps.resolve(request.projectId).checklist.list(request.taskId)
  )
  register('kanban.checklist.add', (request) =>
    deps.resolve(request.projectId).checklist.add(actor, request.taskId, request.text, request.beforeId)
  )
  register('kanban.checklist.update', (request) =>
    deps.resolve(request.projectId).checklist.update(actor, request.itemId, request.text)
  )
  register('kanban.checklist.setDone', (request) =>
    deps.resolve(request.projectId).checklist.setDone(actor, request.itemId, request.done)
  )
  register('kanban.checklist.remove', (request) => {
    deps.resolve(request.projectId).checklist.remove(actor, request.itemId)
    return { deleted: true }
  })
  register('kanban.checklist.reorder', (request) =>
    deps.resolve(request.projectId).checklist.reorder(actor, request.taskId, request.itemIds)
  )

  register('kanban.labels.list', (request) => deps.resolve(request.projectId).labels.list())

  register('kanban.links.add', (request) =>
    deps.resolve(request.projectId).links.add(actor, {
      taskId: request.taskId,
      relation: request.relation,
      targetType: request.targetType,
      targetId: request.targetId
    })
  )
  register('kanban.links.remove', (request) => {
    deps.resolve(request.projectId).links.remove(actor, {
      taskId: request.taskId,
      relation: request.relation,
      targetType: request.targetType,
      targetId: request.targetId
    })
    return { removed: true }
  })

  register('kanban.views.board', (request) =>
    deps.resolve(request.projectId).views.board({
      priority: request.priority,
      labels: request.labels,
      blocked: request.blocked,
      q: request.q
    })
  )
  register('kanban.views.list', (request) => deps.resolve(request.projectId).tasks.list(withoutProject(request)))
  register('kanban.views.week', (request) =>
    deps.resolve(request.projectId).views.week(request.weekPlan, request.limit, request.cursor)
  )
  register('kanban.views.blocked', (request) =>
    deps.resolve(request.projectId).views.blocked(request.limit, request.cursor)
  )
  register('kanban.views.inbox', (request) =>
    deps.resolve(request.projectId).views.inbox(request.limit, request.cursor)
  )
  register('kanban.views.archive', (request) =>
    deps.resolve(request.projectId).tasks.listArchived({
      q: request.q,
      sort: request.sort,
      limit: request.limit,
      cursor: request.cursor
    })
  )

  register('kanban.suggestTasks', async (request) => {
    if (deps.suggestTasks === undefined) {
      throw new AppError(AppErrorCode.AiUnavailable, 'No chat_main model is configured', {
        reason: 'suggester-missing'
      })
    }
    return deps.suggestTasks(request.projectId, request.ideaId)
  })
}

/** Query objects are bound to a module, so `projectId` is not forwarded. */
function withoutProject<T extends { projectId: string }>(request: T): Omit<T, 'projectId'> {
  const { projectId: _projectId, ...rest } = request
  return rest
}