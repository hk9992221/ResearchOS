import { createPluginSandbox } from './bootstrap'

/**
 * Utility-process entry for one plugin (plan §3.7). The parent hands the child
 * a dedicated MessagePort; all host API calls flow over it and every request is
 * permission-checked on the main side. The plugin sees only the sandboxed
 * `researchos` facade.
 */

interface MessageEventLike {
  data?: unknown
  ports?: PortLike[]
}

interface PortLike {
  on(event: 'message', listener: (event: MessageEventLike) => void): void
  postMessage(message: unknown): void
  start?(): void
  close?(): void
}

interface ParentPortLike {
  on(event: 'message', listener: (event: MessageEventLike) => void): void
  postMessage(message: unknown): void
}

interface InitMessage {
  type: 'init'
  pluginId: string
  mainPath: string
  version: string
  apiVersion: number
}

const RPC_TIMEOUT_MS = 15_000

const parent = (process as unknown as { parentPort?: ParentPortLike }).parentPort
if (parent === undefined) throw new Error('plugin child must run inside an Electron utility process')
const parentPort: ParentPortLike = parent

let disposed = false

function unwrapMessage(event: unknown): Record<string, unknown> | undefined {
  if (typeof event !== 'object' || event === null) return undefined
  const data = (event as MessageEventLike).data ?? event
  if (typeof data !== 'object' || data === null) return undefined
  return data as Record<string, unknown>
}

function createPortBridge(pluginId: string, port: PortLike): {
  request(method: string, params: unknown): Promise<unknown>
  onEvent(handler: (event: { type: string; payload?: unknown }) => void): () => void
  resize(width: number, height: number): void
} {
  interface Pending {
    resolve(value: unknown): void
    reject(error: Error): void
    timer: ReturnType<typeof setTimeout>
  }
  const pending = new Map<string, Pending>()
  const listeners = new Set<(event: { type: string; payload?: unknown }) => void>()
  let sequence = 0

  port.on('message', (event) => {
    const message = unwrapMessage(event)
    if (message === undefined) return
    if (message['type'] === 'event') {
      const payload = message['event']
      if (typeof payload !== 'object' || payload === null) return
      const value = payload as { type?: unknown; payload?: unknown }
      if (typeof value.type !== 'string') return
      for (const listener of listeners) listener({ type: value.type, payload: value.payload })
      return
    }
    if (message['type'] !== 'response' && message['type'] !== undefined) return
    const requestId = message['requestId']
    if (typeof requestId !== 'string') return
    const entry = pending.get(requestId)
    if (entry === undefined) return
    pending.delete(requestId)
    clearTimeout(entry.timer)
    if (message['ok'] === true) {
      entry.resolve(message['result'])
      return
    }
    const shape = message['error']
    const text =
      typeof shape === 'object' && shape !== null && typeof (shape as { message?: unknown }).message === 'string'
        ? (shape as { message: string }).message
        : 'plugin RPC failed'
    entry.reject(new Error(text))
  })
  port.start?.()

  return {
    request(method, params) {
      if (disposed) return Promise.reject(new Error('plugin is shutting down'))
      sequence += 1
      const requestId = `${pluginId}-${sequence}`
      return new Promise<unknown>((resolve, reject) => {
        const timer = setTimeout(() => {
          pending.delete(requestId)
          reject(new Error(`plugin RPC timed out after ${RPC_TIMEOUT_MS} ms: ${method}`))
        }, RPC_TIMEOUT_MS)
        timer.unref?.()
        pending.set(requestId, { resolve, reject, timer })
        port.postMessage({ type: 'request', requestId, method, params })
      })
    },
    onEvent(handler) {
      listeners.add(handler)
      return () => listeners.delete(handler)
    },
    resize(width, height) {
      port.postMessage({ type: 'resize', width, height })
    }
  }
}

async function startPlugin(message: InitMessage, port: PortLike): Promise<void> {
  const bridge = createPortBridge(message.pluginId, port)
  const sandbox = createPluginSandbox({
    pluginId: message.pluginId,
    version: message.version,
    apiVersion: message.apiVersion,
    mainPath: message.mainPath,
    bridge: {
      request: (method, params) => bridge.request(method, params),
      onEvent: (handler) => bridge.onEvent(handler),
      resize: (width, height) => bridge.resize(width, height)
    },
    onLog: (level, text) => parentPort.postMessage({ type: 'log', level, message: text })
  })
  sandbox.run()
  parentPort.postMessage({ type: 'ready', pluginId: message.pluginId })
}

parentPort.on('message', (event) => {
  const message = unwrapMessage(event)
  if (message === undefined || message['type'] !== 'init') return
  const port = event.ports?.[0]
  if (port === undefined) {
    parentPort.postMessage({ type: 'error', error: 'plugin init is missing its MessagePort' })
    process.exitCode = 1
    return
  }
  const init = message as unknown as InitMessage
  void startPlugin(init, port).catch((error: unknown) => {
    parentPort.postMessage({
      type: 'error',
      error: error instanceof Error ? error.message : String(error)
    })
    disposed = true
    process.exitCode = 1
    port.close?.()
  })
})