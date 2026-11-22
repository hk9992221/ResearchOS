/**
 * Composition of the project-level foundation behaviors handed to module
 * services: the activity bus, the recycle bin and the
 * confirmation service, plus one transaction helper that publishes the queued
 * `entity.changed` carriers after the commit.
 *
 * `WorkspaceManager` stays untouched: this factory decorates its
 * `ProjectHandle` instead of widening it.
 */
import { type Clock, SystemClock } from './clock'
import { ConfirmService } from './confirm'
import { type ActivityPublished, EventBus } from './events'
import type { Logger } from './logger'
import { TrashService } from './trash'
import type { ProjectHandle } from './workspace/handle'

export interface WithBehaviorsOptions {
  clock?: Clock
  /** IANA zone of the `note.updated` natural-day boundary. */
  timeZone?: string
  /** Post-commit subscriber: renderer invalidation and the index hook. */
  publish?: (event: ActivityPublished) => void
  onPublishError?: (error: unknown, event: ActivityPublished) => void
  /** Share one token store across projects; defaults to a per-handle instance. */
  confirm?: ConfirmService
  confirmTtlMs?: number
  logger?: Logger
}

export interface ProjectBehaviors {
  readonly projectId: string
  readonly handle: ProjectHandle
  readonly events: EventBus
  readonly confirm: ConfirmService
  readonly trash: TrashService
  /**
   * Runs `fn` in a `BEGIN IMMEDIATE` transaction and publishes committed
   * activities afterwards. Use this instead of `handle.inTx` whenever services
   * emit events.
   */
  inTx<T>(fn: () => T): T
}

export function withBehaviors(
  handle: ProjectHandle,
  options: WithBehaviorsOptions = {}
): ProjectBehaviors {
  const clock: Clock = options.clock ?? new SystemClock()
  const events = new EventBus({
    clock,
    timeZone: options.timeZone,
    publish: options.publish,
    onPublishError: options.onPublishError
  })
  const confirm =
    options.confirm ??
    new ConfirmService({
      clock,
      ...(options.confirmTtlMs === undefined ? {} : { ttlMs: options.confirmTtlMs })
    })
  const trash = new TrashService(handle, events, confirm, { clock, logger: options.logger })

  return {
    projectId: handle.id,
    handle,
    events,
    confirm,
    trash,
    inTx<T>(fn: () => T): T {
      const value = handle.inTx(fn)
      events.flush()
      return value
    }
  }
}