/**
 * Desktop confirmation executors.
 *
 * Approval and execution exist only here, reachable from the main process's IPC
 * handlers — never from an MCP tool. `approveAndExecute` performs the
 * requested → approved → executing → executed|failed CAS, so a double click or
 * a restart cannot execute a destructive action twice.
 *
 * The seven executor names are frozen. `trash_empty` is implemented here on the
 * W1 foundation services; the other six delegate to the owning module service
 * through the injected effects (project/literature/ideas/weekly/kanban/export),
 * so this file stays a dispatcher rather than a second business layer.
 */
import type { JsonObject, McpConfirmation } from '@researchos/shared'
import { AppErrorCode, isAppError } from '@researchos/shared'

import { type Clock, SystemClock, formatTimestamp } from '../../clock'
import type { ConfirmService } from '../../confirm'
import type { EventBus } from '../../events'
import { appError } from '../../errors'
import { TrashService } from '../../trash'
import type { TrashRef } from '../../trash'
import type { WorkspaceManager } from '../../workspace'
import type { Confirmations } from './confirmations'

export const CONFIRMATION_TOOLS = [
  'project_delete',
  'trash_empty',
  'paper_sync_apply',
  'evidence_confirm',
  'report_confirm',
  'column_delete',
  'project_import_confirm'
] as const

export type ConfirmationTool = (typeof CONFIRMATION_TOOLS)[number]

export interface ConfirmationEffectContext {
  confirmation: McpConfirmation
  args: JsonObject
}

/** One destructive effect. Validation remains with the owning service. */
export type ConfirmationEffect = (
  context: ConfirmationEffectContext
) => Promise<JsonObject> | JsonObject

export type ConfirmationEffectMap = Partial<Record<ConfirmationTool, ConfirmationEffect>>

/** The six effects owned by other modules; wired by W17. */
export interface DomainConfirmationEffects {
  projectDelete?: ConfirmationEffect
  paperSyncApply?: ConfirmationEffect
  evidenceConfirm?: ConfirmationEffect
  reportConfirm?: ConfirmationEffect
  columnDelete?: ConfirmationEffect
  projectImportConfirm?: ConfirmationEffect
}

export interface TrashEmptyExecutorDeps {
  workspace: WorkspaceManager
  events: EventBus
  confirm: ConfirmService
  clock?: Clock
}

interface TrashContents {
  refs: TrashRef[]
  counts: Record<string, number>
  oldest: Date | null
}

function trashContents(deps: TrashEmptyExecutorDeps, projectId: string): TrashContents {
  const handle = deps.workspace.project(projectId)
  const trash = new TrashService(handle, deps.events, deps.confirm, {
    clock: deps.clock ?? new SystemClock()
  })
  const refs: TrashRef[] = []
  const counts: Record<string, number> = {}
  let oldest: Date | null = null
  let offset = 0
  for (;;) {
    const items = trash.list({ limit: 500, offset })
    for (const item of items) {
      refs.push({ type: item.entityType, id: item.id })
      counts[item.entityType] = (counts[item.entityType] ?? 0) + 1
      if (oldest === null || item.deletedAt.getTime() < oldest.getTime()) oldest = item.deletedAt
    }
    if (items.length < 500) break
    offset += 500
  }
  return { refs, counts, oldest }
}

function expectedMatches(expected: unknown, actual: Record<string, number>): boolean {
  if (typeof expected !== 'object' || expected === null || Array.isArray(expected)) return false
  const record = expected as Record<string, unknown>
  const keys = Object.keys(record)
  if (keys.length !== Object.keys(actual).length) return false
  return keys.every((key) => typeof record[key] === 'number' && record[key] === actual[key])
}

/**
 * Builds the `trash_empty` effect: recompute the counts, refuse a changed
 * trash, mint the in-memory token for exactly the current refs, and purge as
 * `actor='user'` because a human approved it.
 */
export function createTrashEmptyExecutor(deps: TrashEmptyExecutorDeps): ConfirmationEffect {
  return ({ args }) => {
    const projectId = typeof args['project_id'] === 'string' ? args['project_id'] : ''
    if (projectId === '') {
      throw appError(AppErrorCode.InvalidArgument, 'trash_empty: 缺少 project_id')
    }
    const contents = trashContents(deps, projectId)
    if (!expectedMatches(args['expected_counts'], contents.counts)) {
      throw appError(AppErrorCode.Conflict, '回收站内容已变化，请重新预览后再确认', {
        details: { actual: contents.counts }
      })
    }
    const token = deps.confirm.issue({
      kind: 'empty_trash',
      projectId,
      entityTypes: contents.refs.map((ref) => ref.type),
      entityIds: contents.refs.map((ref) => ref.id)
    })
    const handle = deps.workspace.project(projectId)
    const trash = new TrashService(handle, deps.events, deps.confirm, {
      clock: deps.clock ?? new SystemClock()
    })
    const result = trash.empty({ confirm: token, actor: 'user' })
    return {
      purged: result.purged,
      removed_files: result.removedFiles,
      removed_dirs: result.removedDirs,
      cleared_links: result.clearedLinks,
      purged_events: result.purgedEvents,
      warnings: result.warnings,
      counts: contents.counts,
      oldest_deleted_at: contents.oldest === null ? null : formatTimestamp(contents.oldest)
    }
  }
}

/** Assembles the seven-name registry from the trash effect plus domain ones. */
export function createConfirmationEffects(
  trashEmpty: ConfirmationEffect,
  domain: DomainConfirmationEffects = {}
): ConfirmationEffectMap {
  const effects: ConfirmationEffectMap = { trash_empty: trashEmpty }
  if (domain.projectDelete !== undefined) effects.project_delete = domain.projectDelete
  if (domain.paperSyncApply !== undefined) effects.paper_sync_apply = domain.paperSyncApply
  if (domain.evidenceConfirm !== undefined) effects.evidence_confirm = domain.evidenceConfirm
  if (domain.reportConfirm !== undefined) effects.report_confirm = domain.reportConfirm
  if (domain.columnDelete !== undefined) effects.column_delete = domain.columnDelete
  if (domain.projectImportConfirm !== undefined) {
    effects.project_import_confirm = domain.projectImportConfirm
  }
  return effects
}

export class ConfirmationRunner {
  private readonly confirmations: Confirmations
  private readonly effects: ConfirmationEffectMap

  constructor(confirmations: Confirmations, effects: ConfirmationEffectMap) {
    this.confirmations = confirmations
    this.effects = effects
  }

  /** Approve without executing (the desktop may defer execution). */
  approve(id: string, by = 'user', note = ''): McpConfirmation {
    return this.confirmations.resolve(id, { approve: true, by, note })
  }

  reject(id: string, by = 'user', note = ''): McpConfirmation {
    return this.confirmations.resolve(id, { approve: false, by, note })
  }

  /**
   * Approve and execute in one call. An effect failure is recorded on the row
   * as `failed` with the domain error code; it never rolls the approval back.
   */
  async approveAndExecute(id: string, by = 'user', note = ''): Promise<McpConfirmation> {
    this.confirmations.resolve(id, { approve: true, by, note })
    const claimed = this.confirmations.claimExecution(id)
    const effect = this.effects[claimed.tool as ConfirmationTool]
    if (effect === undefined) {
      return this.confirmations.finish(id, null, AppErrorCode.CapabilityInvalid)
    }
    try {
      const result = await effect({ confirmation: claimed, args: this.confirmations.argsOf(id) })
      return this.confirmations.finish(id, result, null)
    } catch (error) {
      const code = isAppError(error) ? error.code : AppErrorCode.Internal
      return this.confirmations.finish(id, null, code)
    }
  }
}