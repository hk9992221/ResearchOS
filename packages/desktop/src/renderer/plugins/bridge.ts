import {
  AppErrorCode,
  isAppError,
  isPluginRpcMethod,
  type AppErrorShape,
  type PluginEvent,
  type PluginFrameTheme,
  type PluginHostEvent
} from '@researchos/shared'

/**
 * Host side of the sandboxed iframe bridge (plan §3.7). `renderer.js` never
 * reaches the host DOM: it receives a dedicated MessagePort, its requests are
 * validated here and permission-checked in the main process, and it can only
 * ask the host to resize itself.
 */

const MAX_PARAMS_BYTES = 256 * 1024
const MAX_DIMENSION = 10_000

export interface FrameRequest {
  kind: 'request'
  requestId: string
  method: string
  params: unknown
}

export interface FrameResize {
  kind: 'resize'
  width: number
  height: number
}

export type FrameMessage = FrameRequest | FrameResize

/** Parses one message arriving from the iframe's MessagePort. */
export function parseFrameMessage(value: unknown): FrameMessage {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new TypeError('plugin bridge message must be an object')
  }
  const message = value as Record<string, unknown>
  const type = message['type']
  if (type === 'resize') {
    const width = message['width']
    const height = message['height']
    if (
      typeof width !== 'number' ||
      typeof height !== 'number' ||
      !Number.isFinite(width) ||
      !Number.isFinite(height) ||
      width < 0 ||
      height < 0 ||
      width > MAX_DIMENSION ||
      height > MAX_DIMENSION
    ) {
      throw new TypeError('plugin resize dimensions are invalid')
    }
    return { kind: 'resize', width: Math.round(width), height: Math.round(height) }
  }
  if (type !== 'request') {
    throw new TypeError(`unknown plugin bridge message type: ${String(type)}`)
  }
  const requestId = message['requestId']
  if (typeof requestId !== 'string' || requestId === '' || requestId.length > 128) {
    throw new TypeError('plugin request id is invalid')
  }
  const method = message['method']
  if (!isPluginRpcMethod(method)) {
    throw new TypeError(`unknown plugin RPC method: ${String(method)}`)
  }
  const params = message['params']
  if (params !== undefined) {
    if (typeof params !== 'object' || params === null || Array.isArray(params)) {
      throw new TypeError('plugin request params must be an object')
    }
    let serialized: string
    try {
      serialized = JSON.stringify(params)
    } catch {
      throw new TypeError('plugin request params are not JSON-serializable')
    }
    if (serialized.length > MAX_PARAMS_BYTES) throw new TypeError('plugin request params are too large')
  }
  return { kind: 'request', requestId, method, params }
}

export type PluginBridgeInvoker = (method: string, params: unknown) => Promise<unknown>

export interface FrameRequestResult {
  response: Record<string, unknown>
  error?: unknown
}

/** Dispatches a validated frame request; never throws at the port boundary. */
export async function answerFrameRequest(
  request: FrameRequest,
  invoke: PluginBridgeInvoker
): Promise<FrameRequestResult> {
  try {
    const result = await invoke(request.method, request.params)
    return {
      response: { type: 'response', requestId: request.requestId, ok: true, result }
    }
  } catch (error) {
    return {
      response: {
        type: 'response',
        requestId: request.requestId,
        ok: false,
        error: errorShape(error)
      },
      error
    }
  }
}

function errorShape(error: unknown): AppErrorShape {
  if (isAppError(error)) return error.toShape()
  return {
    code: AppErrorCode.Internal,
    message: error instanceof Error ? error.message : String(error)
  }
}

/** Reads the live design-token variables; read-only values only. */
export function readThemeTokens(doc: Document): Record<string, string> {
  const tokens: Record<string, string> = {}
  try {
    const style = doc.defaultView?.getComputedStyle(doc.documentElement)
    if (style === null || style === undefined) return tokens
    for (let index = 0; index < style.length && index < 200; index += 1) {
      const property = style.item(index)
      if (!property.startsWith('--')) continue
      const value = style.getPropertyValue(property).trim()
      if (value !== '') tokens[property] = value
    }
  } catch {
    // A document without a window (tests) simply gets an empty token set.
  }
  return tokens
}

export interface PluginFrameBridgeOptions {
  pluginId: string
  invoke: PluginBridgeInvoker
  locale?: string
  theme?: PluginFrameTheme
  onResize?(width: number, height: number): void
  onError?(error: unknown): void
}

export interface PluginFrameBridge {
  attach(frame: HTMLIFrameElement): void
  sendEvent(event: PluginHostEvent | PluginEvent): void
  dispose(): void
}

export function createPluginFrameBridge(options: PluginFrameBridgeOptions): PluginFrameBridge {
  let channel: MessageChannel | undefined
  let attached: HTMLIFrameElement | undefined

  const dispose = (): void => {
    if (channel !== undefined) {
      try {
        channel.port1.close()
      } catch {
        // Already closed.
      }
      channel = undefined
    }
    attached = undefined
  }

  return {
    attach(frame) {
      dispose()
      const target = frame.contentWindow
      if (target === null) throw new Error('plugin iframe has no contentWindow')
      const created = new MessageChannel()
      channel = created
      attached = frame
      created.port1.onmessage = (event: MessageEvent) => {
        let message: FrameMessage
        try {
          message = parseFrameMessage(event.data)
        } catch (error) {
          options.onError?.(error)
          return
        }
        if (message.kind === 'resize') {
          options.onResize?.(message.width, message.height)
          return
        }
        void answerFrameRequest(message, options.invoke).then(({ response, error }) => {
          if (error !== undefined) options.onError?.(error)
          if (channel !== undefined) channel.port1.postMessage(response)
        })
      }
      const theme =
        options.theme ??
        ({
          mode: 'light',
          tokens: readThemeTokens(frame.ownerDocument)
        } satisfies PluginFrameTheme)
      target.postMessage(
        {
          type: 'plugin:init',
          pluginId: options.pluginId,
          locale: options.locale ?? 'en',
          theme
        },
        '*',
        [created.port2]
      )
    },
    sendEvent(event) {
      if (channel === undefined) return
      channel.port1.postMessage({ type: 'event', event })
    },
    dispose
  }
}

/** Renderer-side URL for the host-controlled iframe document. */
export function pluginFrameUrl(pluginId: string, entry: string): string {
  return `plugin://${encodeURIComponent(pluginId)}/__host__/iframe.html?entry=${encodeURIComponent(entry)}`
}