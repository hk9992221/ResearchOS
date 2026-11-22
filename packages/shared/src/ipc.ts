import type { AppErrorShape } from './errors'
import type { AiActivity } from './types/ai'
import type { CommandChannel, CommandMap } from './contracts'
import type { PluginHostEvent, PluginPermission, PluginPromptParams, SilentUpdateInfo } from './plugin-api'
import type { IsoDateTime } from './types/common'
import type { McpStatus } from './types/mcp'

/** Envelope for every renderer → main command response. Never throw raw errors across IPC. */
export type IpcResult<T> =
  | { ok: true; data: T }
  | { ok: false; error: AppErrorShape }

export interface SenderContext {
  windowId: number
  webContentsId: number
  isMainWindow: boolean
}

/**
 * Structural validator contract. Concrete schemas come from
 * `packages/core/src/validation/*` (zod); shared stays dependency-free.
 */
export interface CommandValidator<TRequest> {
  parse(input: unknown): TRequest
}

export type CommandHandler<C extends CommandChannel> = (
  request: CommandMap[C]['req'],
  sender: SenderContext
) => CommandMap[C]['res'] | Promise<CommandMap[C]['res']>

/* --------------------------------------------------------------- events */

export type AppEvent =
  | {
      type: 'entity.changed'
      projectId: string
      entityType: string
      entityId: string
      op: 'created' | 'updated' | 'deleted' | 'restored'
    }
  | ({ type: 'ai.activity' } & AiActivity)
  | { type: 'job.progress'; jobId: string; percent: number; stage: string }
  | {
      type: 'job.done'
      jobId: string
      state: 'succeeded' | 'failed' | 'cancelled'
      error?: AppErrorShape
    }
  | { type: 'mcp.confirmations.changed' }
  | { type: 'mcp.status.changed'; status: McpStatus }
  | { type: 'plugins.changed' }
  /** A background silent update replaced a plugin. */
  | ({ type: 'plugins.updated' } & SilentUpdateInfo)
  /** Host → mounted plugin panel event relay. */
  | { type: 'plugins.event'; pluginId: string; event: PluginHostEvent }
  /** A plugin asked for `ui.prompt` / `ui.confirm`. */
  | {
      type: 'plugins.prompt.request'
      requestId: string
      pluginId: string
      pluginName: string
      kind: 'prompt' | 'confirm'
      params: PluginPromptParams | import('./plugin-api').PluginUiConfirmParams
      permissions: PluginPermission[]
    }
  /** The pending request was withdrawn (plugin disabled or window closed). */
  | { type: 'plugins.prompt.cancel'; requestId: string }
  /** `ui.navigate` relayed to the shell router. */
  | {
      type: 'plugins.navigate'
      pluginId: string
      target: string
      projectId?: string
      entityType?: string
      entityId?: string
    }
  /** `ui.toast` relayed to the shell toast store. */
  | {
      type: 'plugins.toast'
      pluginId: string
      message: string
      level: 'info' | 'success' | 'warning' | 'error'
    }
  | { type: 'theme.system.changed'; dark: boolean }
  | { type: 'prefers.reduced-motion.changed'; reduced: boolean }
  | { type: 'app.flush'; reason: 'close' | 'switch-workspace' }

export type AppEventType = AppEvent['type']
export type AppEventPayload<E extends AppEventType> = Extract<AppEvent, { type: E }>

export const APP_EVENT_CHANNEL_PREFIX = 'researchos:event:'

export function appEventChannel(type: AppEventType): string {
  return `${APP_EVENT_CHANNEL_PREFIX}${type}`
}

/* --------------------------------------------------------------- bridge */

/** Shape exposed as `window.researchos` by the preload script. */
export interface ResearchOsBridge {
  invoke<C extends CommandChannel>(
    channel: C,
    payload: CommandMap[C]['req']
  ): Promise<IpcResult<CommandMap[C]['res']>>

  /** Subscribes and returns an unsubscribe function. */
  on<E extends AppEventType>(event: E, handler: (payload: AppEventPayload<E>) => void): () => void
  off<E extends AppEventType>(event: E, handler: (payload: AppEventPayload<E>) => void): void

  readonly platform: string
  readonly versions: { electron: string; chrome: string }
}

export interface RendererReady {
  at: IsoDateTime
  locale: string
  route: string
}