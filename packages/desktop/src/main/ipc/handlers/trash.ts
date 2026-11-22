import {
  AppError,
  AppErrorCode,
  TRASH_ENTITY_TYPES,
  type TrashEntry,
  type TrashPlan,
  type TrashRef
} from '@researchos/shared'
import type { ProjectBehaviors } from '@researchos/core/behaviors'
import { trashValidators } from '@researchos/core/validation/trash'
import type { TrashRef as CoreTrashRef } from '@researchos/core/trash'

import type { CommandRouter } from '../router'

export interface TrashHandlerDeps {
  behavior(projectId: string): ProjectBehaviors
}

interface PendingPlan {
  projectId: string
  refs: CoreTrashRef[]
  kind: 'purge' | 'empty'
}

const TOKEN_TTL_MS = 5 * 60 * 1000

function toCoreRef(ref: TrashRef): CoreTrashRef {
  return { type: ref.type, id: ref.id }
}

/** Two-tier confirmation: plan mints a scoped token, confirm consumes it. */
export function registerTrashHandlers(router: CommandRouter, deps: TrashHandlerDeps): void {
  const plans = new Map<string, PendingPlan & { expiresAt: number }>()

  const prune = (): void => {
    const now = Date.now()
    for (const [token, plan] of plans) if (plan.expiresAt <= now) plans.delete(token)
  }

  router.registerHandler('trash.list', trashValidators['trash.list'], (request): TrashEntry[] =>
    deps
      .behavior(request.projectId)
      .trash.list({ limit: 500 })
      .filter((item) => (TRASH_ENTITY_TYPES as readonly string[]).includes(item.entityType))
      .map((item) => ({
        type: item.entityType as TrashEntry['type'],
        id: item.id,
        title: item.title,
        deletedAt: item.deletedAt.toISOString(),
        fileState: 'ok'
      }))
  )

  router.registerHandler('trash.restore', trashValidators['trash.restore'], (request) => {
    const behavior = deps.behavior(request.projectId)
    const restored = behavior.inTx(() => {
      for (const item of request.items) behavior.trash.restore({ db: behavior.handle.db }, toCoreRef(item), 'user')
      return request.items.length
    })
    return { restored }
  })

  router.registerHandler('trash.purge.plan', trashValidators['trash.purge.plan'], (request): TrashPlan => {
    const behavior = deps.behavior(request.projectId)
    const current = behavior.trash.list({ limit: 500 })
    const wanted = new Set(request.items.map((item) => `${item.type}:${item.id}`))
    const refs = current
      .filter((item) => wanted.has(`${item.entityType}:${item.id}`))
      .map((item) => ({ type: item.entityType, id: item.id }) as CoreTrashRef)
    if (refs.length === 0) {
      throw new AppError(AppErrorCode.NotFound, '回收站中没有匹配的条目')
    }
    prune()
    const token = behavior.confirm.issue({
      kind: 'purge_entity',
      projectId: request.projectId,
      entityTypes: refs.map((ref) => ref.type),
      entityIds: refs.map((ref) => ref.id)
    })
    plans.set(token, { projectId: request.projectId, refs, kind: 'purge', expiresAt: Date.now() + TOKEN_TTL_MS })
    const warnings = refs.length < request.items.length ? ['部分条目已不在回收站中，已按当前内容生成计划'] : []
    return { token, count: refs.length, warnings }
  })

  router.registerHandler('trash.purge.confirm', trashValidators['trash.purge.confirm'], (request) => {
    prune()
    const plan = plans.get(request.token)
    if (plan === undefined || plan.kind !== 'purge') {
      throw new AppError(AppErrorCode.UserConfirmationRequired, '确认令牌无效或已过期')
    }
    plans.delete(request.token)
    const result = deps.behavior(plan.projectId).trash.purge({
      refs: plan.refs,
      confirm: request.token,
      actor: 'user'
    })
    return { purged: result.purged }
  })

  router.registerHandler('trash.empty.plan', trashValidators['trash.empty.plan'], (request): TrashPlan => {
    const behavior = deps.behavior(request.projectId)
    const items = behavior.trash.list({ limit: 500 })
    const refs = items.map((item) => ({ type: item.entityType, id: item.id }) as CoreTrashRef)
    prune()
    const token = behavior.confirm.issue({
      kind: 'empty_trash',
      projectId: request.projectId,
      entityTypes: refs.map((ref) => ref.type),
      entityIds: refs.map((ref) => ref.id)
    })
    plans.set(token, { projectId: request.projectId, refs, kind: 'empty', expiresAt: Date.now() + TOKEN_TTL_MS })
    return { token, count: refs.length, warnings: [] }
  })

  router.registerHandler('trash.empty.confirm', trashValidators['trash.empty.confirm'], (request) => {
    prune()
    const plan = plans.get(request.token)
    if (plan === undefined || plan.kind !== 'empty') {
      throw new AppError(AppErrorCode.UserConfirmationRequired, '确认令牌无效或已过期')
    }
    plans.delete(request.token)
    const result = deps.behavior(plan.projectId).trash.empty({ confirm: request.token, actor: 'user' })
    return { emptied: result.purged }
  })
}