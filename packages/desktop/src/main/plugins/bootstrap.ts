import { readFileSync, statSync } from 'node:fs'
import * as vm from 'node:vm'

import type { PluginBridge, PluginRpcMethod, PluginStorageKeyItem } from '@researchos/shared'

/**
 * Per-plugin utility-process bootstrap (plan §3.7).
 *
 * This is a capability boundary, not an OS sandbox: plugin code is local and
 * user-installed. The vm context exposes exactly `{ console, researchos }`;
 * Node globals are absent, and the API object is deeply frozen. Resource
 * escapes (child_process, require) are impossible without host holes.
 */

export const MAX_MAIN_BYTES = 5 * 1024 * 1024
export const MAIN_EXECUTION_TIMEOUT_MS = 5_000
export const MAX_LOG_ENTRIES = 500

export interface PluginSandboxBridge {
  request(method: string, params: unknown): Promise<unknown>
  onEvent(handler: (event: { type: string; payload?: unknown }) => void): () => void
  resize(width: number, height: number): void
}

export interface PluginSandboxOptions {
  pluginId: string
  version: string
  apiVersion: number
  mainPath: string
  bridge: PluginSandboxBridge
  /** Execution timeout for the top-level script; defaults to 5 s. */
  executionTimeoutMs?: number
  /** Forwards console output to the host's ring buffer. */
  onLog?: (level: string, message: string) => void
}

export interface PluginSandbox {
  readonly pluginId: string
  readonly logs: readonly string[]
  /** Runs `main.js` once; a second call throws. */
  run(): void
  dispose(): void
}

function formatArgs(args: unknown[]): string {
  return args
    .map((value) => {
      if (typeof value === 'string') return value
      if (value instanceof Error) return value.stack ?? value.message
      try {
        return JSON.stringify(value)
      } catch {
        return String(value)
      }
    })
    .join(' ')
}

function deepFreeze<T>(value: T): T {
  if (typeof value !== 'object' || value === null || Object.isFrozen(value)) return value
  Object.freeze(value)
  for (const child of Object.values(value as Record<string, unknown>)) deepFreeze(child)
  return value
}

/**
 * Plugin-facing `researchos.storage` facade. It hides the RPC
 * envelopes: get unwraps `found`, set rejects `undefined` before sending so
 * removal is never ambiguous with a stored null.
 */
export interface ResearchOSStorage {
  get<T = unknown>(key: string, options?: { projectId?: string }): Promise<T | undefined>
  set(key: string, value: unknown, options?: { projectId?: string }): Promise<void>
  delete(key: string, options?: { projectId?: string }): Promise<boolean>
  list(options?: {
    prefix?: string
    projectId?: string
    limit?: number
    cursor?: string
  }): Promise<{ keys: PluginStorageKeyItem[]; nextCursor?: string }>
  clear(options?: { projectId?: string }): Promise<number>
}

function createStorageNamespace(bridge: PluginSandboxBridge): ResearchOSStorage {
  const scope = (options?: { projectId?: string }): { projectId?: string } =>
    options !== undefined && options.projectId !== undefined ? { projectId: options.projectId } : {}
  return {
    async get<T>(key: string, options?: { projectId?: string }): Promise<T | undefined> {
      const result = (await bridge.request('storage.get', { key, ...scope(options) })) as {
        found: boolean
        value?: T
      }
      return result.found ? result.value : undefined
    },
    async set(key: string, value: unknown, options?: { projectId?: string }): Promise<void> {
      if (value === undefined) {
        throw new TypeError(
          'researchos.storage.set requires a value; use researchos.storage.delete(key) to remove a key'
        )
      }
      await bridge.request('storage.set', { key, value, ...scope(options) })
    },
    async delete(key: string, options?: { projectId?: string }): Promise<boolean> {
      const result = (await bridge.request('storage.delete', { key, ...scope(options) })) as {
        deleted: boolean
      }
      return result.deleted
    },
    async list(options?: {
      prefix?: string
      projectId?: string
      limit?: number
      cursor?: string
    }): Promise<{ keys: PluginStorageKeyItem[]; nextCursor?: string }> {
      return (await bridge.request('storage.list', options ?? {})) as {
        keys: PluginStorageKeyItem[]
        nextCursor?: string
      }
    },
    async clear(options?: { projectId?: string }): Promise<number> {
      const result = (await bridge.request('storage.clear', scope(options))) as { deletedCount: number }
      return result.deletedCount
    }
  }
}

/** Creates the sandbox and returns a runner; `main.js` is not executed yet. */
export function createPluginSandbox(options: PluginSandboxOptions): PluginSandbox {
  const info = statSync(options.mainPath)
  if (!info.isFile()) throw new Error(`plugin main is not a file: ${options.mainPath}`)
  if (info.size > MAX_MAIN_BYTES) throw new Error(`plugin main exceeds ${MAX_MAIN_BYTES} bytes`)
  const source = readFileSync(options.mainPath, 'utf8')
  const logs: string[] = []
  const sink = (level: string, message: string): void => {
    logs.push(`[${level}] ${message}`)
    if (logs.length > MAX_LOG_ENTRIES) logs.splice(0, logs.length - MAX_LOG_ENTRIES)
    options.onLog?.(level, message)
  }
  const consoleObject = {
    log: (...args: unknown[]) => sink('log', formatArgs(args)),
    info: (...args: unknown[]) => sink('info', formatArgs(args)),
    warn: (...args: unknown[]) => sink('warn', formatArgs(args)),
    error: (...args: unknown[]) => sink('error', formatArgs(args)),
    debug: (...args: unknown[]) => sink('debug', formatArgs(args))
  }
  const researchos = deepFreeze({
    pluginId: options.pluginId,
    version: options.version,
    apiVersion: options.apiVersion,
    storage: createStorageNamespace(options.bridge),
    pluginBridge: deepFreeze({
      request: (method: string, params: unknown) => {
        if (typeof method !== 'string' || method === '') {
          throw new TypeError('request method must be a non-empty string')
        }
        return options.bridge.request(method, params)
      },
      onEvent: (handler: (event: { type: string; payload?: unknown }) => void) => {
        if (typeof handler !== 'function') throw new TypeError('onEvent requires a function')
        return options.bridge.onEvent(handler)
      },
      resize: (width: number, height: number) => {
        if (!Number.isFinite(width) || !Number.isFinite(height)) {
          throw new TypeError('resize requires finite numbers')
        }
        options.bridge.resize(width, height)
      }
    })
  })
  const sandbox: Record<string, unknown> = { console: Object.freeze(consoleObject), researchos }
  const context = vm.createContext(sandbox, { name: `plugin:${options.pluginId}` })
  // The vm context starts without Node globals; delete defensively so a host
  // that injects anything later does not widen this plugin's surface.
  for (const key of ['require', 'module', 'exports', 'process', 'global', 'Buffer']) {
    delete (sandbox as Record<string, unknown>)[key]
  }
  let ran = false
  let disposed = false
  return {
    pluginId: options.pluginId,
    get logs() {
      return [...logs]
    },
    run(): void {
      if (disposed) throw new Error('plugin sandbox has been disposed')
      if (ran) throw new Error('plugin main already executed')
      ran = true
      vm.runInContext(source, context, {
        filename: options.mainPath,
        timeout: options.executionTimeoutMs ?? MAIN_EXECUTION_TIMEOUT_MS,
        displayErrors: true
      })
    },
    dispose(): void {
      disposed = true
    }
  }
}

/** Typed convenience for plugin authors compiling against the ABI. */
export type PluginAuthorBridge = PluginBridge
export type PluginAuthorMethod = PluginRpcMethod