/**
 * Typed IPC client.
 *
 * The preload script exposes one bridge, `window.researchos`, whose `invoke`
 * always resolves to the `IpcResult` envelope. This module turns that envelope
 * into the command's response type on success and a reconstructed `AppError`
 * on failure, so an error code survives the crossing and the UI can branch on
 * it. Raw transport rejections are wrapped the same way: a renderer never sees
 * an untyped throw from a command.
 *
 * The bridge is resolved on every call, not cached: tests and the dev browser
 * install it after import, and a missing bridge must surface as an explicit
 * error the shell can render, never as a silent empty state.
 */

import {
  AppError,
  AppErrorCode,
  isAppErrorCode,
  type AppEventPayload,
  type AppEventType,
  type CommandChannel,
  type CommandMap,
  type IpcResult,
  type ResearchOsBridge
} from '@researchos/shared'

const BRIDGE_UNAVAILABLE_MESSAGE =
  'The Electron IPC bridge is unavailable. Run the renderer inside the desktop app.'

export interface BridgeVersions {
  readonly electron: string
  readonly chrome: string
}

/** getBridge returns the preload bridge, or null outside Electron (dev/tests). */
export function getBridge(): ResearchOsBridge | null {
  if (typeof window === 'undefined') return null
  return window.researchos ?? null
}

export function hasBridge(): boolean {
  return getBridge() !== null
}

/** isBridgeUnavailable separates "not running in Electron" from a command error. */
export function isBridgeUnavailable(error: unknown): boolean {
  return (
    error instanceof AppError &&
    error.code === AppErrorCode.Internal &&
    error.details?.['bridgeUnavailable'] === true
  )
}

function bridgeUnavailable(channel: string): AppError {
  return new AppError(AppErrorCode.Internal, BRIDGE_UNAVAILABLE_MESSAGE, {
    channel,
    bridgeUnavailable: true
  })
}

function unwrap<C extends CommandChannel>(
  channel: C,
  result: IpcResult<CommandMap[C]['res']>
): CommandMap[C]['res'] {
  if (result.ok) return result.data
  const shape = result.error
  // The envelope is typed, but the runtime value crosses a process boundary:
  // an unknown code becomes INTERNAL instead of leaking as a fake typed code.
  const code = isAppErrorCode(shape.code) ? shape.code : AppErrorCode.Internal
  throw new AppError(code, shape.message, shape.details)
}

/**
 * invokePending reaches a command whose contract has not landed in
 * `@researchos/shared` yet (the recycle bin, for example). The envelope and
 * error handling are identical to `invoke`; only the channel/response types
 * are the local feature's. Delete the call sites when the shared contract
 * arrives — this is the one place allowed to name an unlisted channel.
 */
export async function invokePending<T>(channel: string, request?: unknown): Promise<T> {
  const bridge = getBridge()
  if (bridge === null) throw bridgeUnavailable(channel)
  const rawInvoke = bridge.invoke as (
    name: string,
    payload: unknown
  ) => Promise<IpcResult<unknown>>
  try {
    const result = await rawInvoke(channel, request)
    if (result.ok) return result.data as T
    const shape = result.error
    const code = isAppErrorCode(shape.code) ? shape.code : AppErrorCode.Internal
    throw new AppError(code, shape.message, shape.details)
  } catch (cause) {
    if (cause instanceof AppError) throw cause
    const message = cause instanceof Error ? cause.message : String(cause)
    throw new AppError(AppErrorCode.Internal, message, { channel }, { cause })
  }
}

/** Channels whose request is void may be invoked without a payload. */
type InvokeArgs<C extends CommandChannel> = CommandMap[C]['req'] extends void
  ? [] | [request?: CommandMap[C]['req']]
  : [request: CommandMap[C]['req']]

export interface IpcClient {
  invoke<C extends CommandChannel>(
    channel: C,
    ...args: InvokeArgs<C>
  ): Promise<CommandMap[C]['res']>
  on<E extends AppEventType>(
    event: E,
    handler: (payload: AppEventPayload<E>) => void
  ): () => void
  off<E extends AppEventType>(event: E, handler: (payload: AppEventPayload<E>) => void): void
  readonly platform: string | null
  readonly versions: BridgeVersions | null
  readonly available: boolean
}

export const ipc: IpcClient = {
  async invoke<C extends CommandChannel>(
    channel: C,
    ...args: InvokeArgs<C>
  ): Promise<CommandMap[C]['res']> {
    const request = args[0] as CommandMap[C]['req']
    const bridge = getBridge()
    if (bridge === null) throw bridgeUnavailable(channel)
    try {
      return unwrap(channel, await bridge.invoke(channel, request))
    } catch (cause) {
      if (cause instanceof AppError) throw cause
      const message = cause instanceof Error ? cause.message : String(cause)
      throw new AppError(AppErrorCode.Internal, message, { channel }, { cause })
    }
  },

  on<E extends AppEventType>(
    event: E,
    handler: (payload: AppEventPayload<E>) => void
  ): () => void {
    const bridge = getBridge()
    // Subscriptions are attached only when there is a host; the workspace
    // status query is what reports a missing bridge to the user.
    return bridge === null ? () => {} : bridge.on(event, handler)
  },

  off<E extends AppEventType>(event: E, handler: (payload: AppEventPayload<E>) => void): void {
    getBridge()?.off(event, handler)
  },

  get platform(): string | null {
    return getBridge()?.platform ?? null
  },

  get versions(): BridgeVersions | null {
    return getBridge()?.versions ?? null
  },

  get available(): boolean {
    return getBridge() !== null
  }
}