import { AppErrorCode, isAppError, type AppErrorShape } from '@researchos/shared'

import type { PluginRuntime, PluginRuntimeStart } from '@researchos/core/modules/plugins/index'
import { PluginRpcRouter } from '@researchos/core/modules/plugins/index'

/**
 * Electron utility-process host (plan §3.7): one process per plugin, a
 * dedicated MessagePort per plugin, 15 s call timeouts, crash isolation and a
 * log ring buffer. The Electron dependency is injected (`fork`/`createChannel`)
 * so the lifecycle is testable in plain Node.
 */

export const PLUGIN_STARTUP_TIMEOUT_MS = 15_000
export const PLUGIN_MEMORY_MB = 128
export const MAX_LOG_LINES = 500

export interface HostMessageEvent {
  data?: unknown
  ports?: HostMessagePort[]
}

export interface HostMessagePort {
  on(event: 'message', listener: (event: HostMessageEvent) => void): void
  postMessage(message: unknown, transfer?: HostMessagePort[]): void
  start?(): void
  close?(): void
}

export interface HostChildProcess {
  on(event: 'message', listener: (message: unknown) => void): void
  on(event: 'exit', listener: (code: number) => void): void
  on(event: 'error', listener: (...args: unknown[]) => void): void
  postMessage(message: unknown, transfer?: HostMessagePort[]): void
  kill(): boolean
  stdout?: { on(event: 'data', listener: (chunk: unknown) => void): void }
  stderr?: { on(event: 'data', listener: (chunk: unknown) => void): void }
}

export interface PluginHostForkOptions {
  execArgv: string[]
  stdio: 'pipe'
  env: NodeJS.ProcessEnv
}

export interface PluginHostDeps {
  childModulePath: string
  fork(modulePath: string, args: string[], options: PluginHostForkOptions): HostChildProcess
  createChannel(): { port1: HostMessagePort; port2: HostMessagePort }
  /** Permission-checked dispatcher, normally `PluginService.call`. */
  invoke(pluginId: string, method: string, params: unknown): Promise<unknown>
  onCrash?(pluginId: string, reason: string): void
  onLog?(pluginId: string, level: string, message: string): void
  startupTimeoutMs?: number
}

interface RunningPlugin {
  child: HostChildProcess
  port: HostMessagePort
  logs: string[]
  ready: boolean
  stopping: boolean
  resolveReady(): void
  rejectReady(error: Error): void
  readyPromise: Promise<void>
}

export class PluginUtilityHost implements PluginRuntime {
  private readonly deps: PluginHostDeps
  private readonly running = new Map<string, RunningPlugin>()
  private readonly startupTimeoutMs: number

  constructor(deps: PluginHostDeps) {
    this.deps = deps
    this.startupTimeoutMs =
      deps.startupTimeoutMs !== undefined && deps.startupTimeoutMs > 0
        ? deps.startupTimeoutMs
        : PLUGIN_STARTUP_TIMEOUT_MS
  }

  async start(plugin: PluginRuntimeStart): Promise<void> {
    if (this.running.has(plugin.pluginId)) await this.stop(plugin.pluginId, 'restart')
    const channel = this.deps.createChannel()
    const child = this.deps.fork(this.deps.childModulePath, [plugin.pluginId], {
      execArgv: [`--max-old-space-size=${PLUGIN_MEMORY_MB}`],
      stdio: 'pipe',
      env: process.env
    })
    let resolveReady!: () => void
    let rejectReady!: (error: Error) => void
    const readyPromise = new Promise<void>((resolve, reject) => {
      resolveReady = resolve
      rejectReady = reject
    })
    const state: RunningPlugin = {
      child,
      port: channel.port1,
      logs: [],
      ready: false,
      stopping: false,
      resolveReady,
      rejectReady,
      readyPromise
    }
    this.running.set(plugin.pluginId, state)
    this.captureStream(plugin.pluginId, state, child.stdout)
    this.captureStream(plugin.pluginId, state, child.stderr)

    child.on('message', (message) => this.onChildMessage(plugin.pluginId, state, message))
    child.on('exit', (code) => this.onExit(plugin.pluginId, state, code))
    child.on('error', (...args: unknown[]) => {
      const reason = args.length === 0 ? 'plugin process error' : `plugin process error: ${String(args[0])}`
      this.appendLog(plugin.pluginId, state, 'error', reason)
      if (!state.ready) state.rejectReady(new Error(reason))
      this.handleCrash(plugin.pluginId, state, reason)
    })

    channel.port1.on('message', (event) => {
      void this.onPortMessage(plugin.pluginId, state, event)
    })
    channel.port1.start?.()

    const init = {
      type: 'init',
      pluginId: plugin.pluginId,
      mainPath: plugin.mainPath,
      version: plugin.version,
      apiVersion: 1
    }
    child.postMessage(init, [channel.port2])

    try {
      await withDeadline(state.readyPromise, this.startupTimeoutMs, () => {
        const reason = `plugin ${plugin.pluginId} did not start within ${this.startupTimeoutMs} ms`
        state.rejectReady(new Error(reason))
        return new Error(reason)
      })
    } catch (error) {
      await this.stop(plugin.pluginId, 'start-failed')
      throw error
    }
  }

  async stop(pluginId: string, reason?: string): Promise<void> {
    const state = this.running.get(pluginId)
    if (state === undefined) return
    state.stopping = true
    this.running.delete(pluginId)
    try {
      state.port.close?.()
    } catch {
      // Closing an already-disconnected port is harmless.
    }
    try {
      state.child.kill()
    } catch {
      // The child may already have exited.
    }
    if (reason !== undefined) this.appendLog(pluginId, state, 'info', `stopping: ${reason}`)
  }

  isRunning(pluginId: string): boolean {
    return this.running.get(pluginId)?.ready === true
  }

  /** Pushes one host event to the plugin's `main.js` listener set. */
  emitEvent(pluginId: string, event: { type: string; payload?: unknown }): boolean {
    const state = this.running.get(pluginId)
    if (state === undefined) return false
    try {
      state.port.postMessage({ type: 'event', event })
      return true
    } catch {
      return false
    }
  }

  /** Ring buffer snapshot; newest last. */
  logs(pluginId: string): string[] {
    return [...(this.running.get(pluginId)?.logs ?? [])]
  }

  private onChildMessage(pluginId: string, state: RunningPlugin, message: unknown): void {
    if (typeof message !== 'object' || message === null) return
    const data = message as Record<string, unknown>
    switch (data['type']) {
      case 'ready':
        state.ready = true
        state.resolveReady()
        return
      case 'log':
        this.appendLog(
          pluginId,
          state,
          typeof data['level'] === 'string' ? data['level'] : 'log',
          typeof data['message'] === 'string' ? data['message'] : ''
        )
        return
      case 'error': {
        const reason = typeof data['error'] === 'string' ? data['error'] : 'plugin process failed'
        this.appendLog(pluginId, state, 'error', reason)
        if (!state.ready) state.rejectReady(new Error(reason))
        this.handleCrash(pluginId, state, reason)
        return
      }
      default:
        return
    }
  }

  private onExit(pluginId: string, state: RunningPlugin, code: number): void {
    if (state.stopping) return
    const reason = `plugin process exited with code ${code}`
    this.appendLog(pluginId, state, 'error', reason)
    if (!state.ready) state.rejectReady(new Error(reason))
    this.handleCrash(pluginId, state, reason)
  }

  private handleCrash(pluginId: string, state: RunningPlugin, reason: string): void {
    if (state.stopping) return
    state.stopping = true
    this.running.delete(pluginId)
    try {
      state.port.close?.()
    } catch {
      // Already gone.
    }
    try {
      state.child.kill()
    } catch {
      // Already gone.
    }
    try {
      this.deps.onCrash?.(pluginId, reason)
    } catch {
      // A crash callback must never take down the main process.
    }
  }

  private async onPortMessage(pluginId: string, state: RunningPlugin, event: HostMessageEvent): Promise<void> {
    const message = event.data ?? event
    let requestId: string | undefined
    try {
      const request = PluginRpcRouter.validateRequest(message)
      requestId = request.requestId
      const result = await this.deps.invoke(pluginId, request.method, request.params)
      state.port.postMessage({ type: 'response', requestId, ok: true, result })
    } catch (error) {
      const responseRequestId =
        requestId ??
        (typeof message === 'object' && message !== null && typeof (message as { requestId?: unknown }).requestId === 'string'
          ? (message as { requestId: string }).requestId
          : undefined)
      if (responseRequestId === undefined) {
        this.appendLog(pluginId, state, 'warn', `invalid RPC message: ${describe(error)}`)
        return
      }
      state.port.postMessage({
        type: 'response',
        requestId: responseRequestId,
        ok: false,
        error: toErrorShape(error)
      })
    }
  }

  private captureStream(
    pluginId: string,
    state: RunningPlugin,
    stream: { on(event: 'data', listener: (chunk: unknown) => void): void } | undefined
  ): void {
    stream?.on('data', (chunk) => {
      this.appendLog(pluginId, state, 'stdout', String(chunk).trimEnd())
    })
  }

  private appendLog(pluginId: string, state: RunningPlugin, level: string, message: string): void {
    state.logs.push(`[${level}] ${message}`)
    if (state.logs.length > MAX_LOG_LINES) state.logs.splice(0, state.logs.length - MAX_LOG_LINES)
    try {
      this.deps.onLog?.(pluginId, level, message)
    } catch {
      // Observing logs must never break the host.
    }
  }
}

function toErrorShape(error: unknown): AppErrorShape {
  if (isAppError(error)) return error.toShape()
  return {
    code: AppErrorCode.Internal,
    message: error instanceof Error ? error.message : String(error)
  }
}

function describe(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function withDeadline<T>(promise: Promise<T>, ms: number, onTimeout: () => Error): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(onTimeout()), ms)
    timer.unref?.()
    promise.then(
      (value) => {
        clearTimeout(timer)
        resolve(value)
      },
      (error: unknown) => {
        clearTimeout(timer)
        reject(error)
      }
    )
  })
}