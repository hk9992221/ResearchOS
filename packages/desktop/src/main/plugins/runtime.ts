import { readFileSync } from 'node:fs'
import * as path from 'node:path'

import { MessageChannelMain, utilityProcess } from 'electron'

import type { PluginRpcMethod, SilentUpdateInfo } from '@researchos/shared'

import type {
  MarketTransport,
  PluginAiHost,
  PluginFetch,
  PluginHostOperations,
  PluginPromptPresenter,
  PluginRpcHandler,
  PluginRuleSink,
  PluginServiceOptions,
  PluginSkillSink
} from '@researchos/core/modules/plugins/index'
import {
  PluginBlobStore,
  PluginFilesService,
  PluginMarketService,
  PluginService,
  PluginUpdateScheduler
} from '@researchos/core/modules/plugins/index'
import { SettingsStore } from '@researchos/core/settings'
import type { WorkspaceManager } from '@researchos/core/workspace/index'

import type { HostChildProcess, HostMessagePort } from './host'
import { PluginUtilityHost } from './host'
import { PluginHotReloader } from './hot-reload'
import type { ProtocolRegistrar } from './protocol'
import { registerPluginProtocol } from './protocol'
import { PluginWatchService } from './watch-service'
import pluginChildPath from './plugin-child?modulePath'

/**
 * Composition root for the desktop half of the plugin subsystem. The main
 * process wires this once; renderer handlers only see `PluginService`.
 *
 * Plugin code is trusted-but-permission-limited: the utility process is a
 * capability boundary, not an OS sandbox.
 */

export interface PluginRuntimeBundleOptions {
  workspace: Pick<WorkspaceManager, 'root' | 'paths' | 'global'>
  skills?: PluginSkillSink
  rules?: PluginRuleSink
  rpcHandlers?: Partial<Record<PluginRpcMethod, PluginRpcHandler>>
  fetchImpl?: PluginFetch
  clipboard?: PluginServiceOptions['clipboard']
  notifications?: PluginServiceOptions['notifications']
  editor?: PluginServiceOptions['editor']
  /** Test seam for the market's HTTP layer. */
  marketTransport?: MarketTransport
  onChange?(info?: unknown): void
  /** Pushes the "已更新至 vX.Y.Z" toast payload. */
  onSilentUpdate?(update: SilentUpdateInfo): void
  onLog?(pluginId: string, level: string, message: string): void
  /** Electron's `protocol` module, when the app is ready. */
  protocol?: ProtocolRegistrar
  /** Native directory picker; the core persists the grant. */
  pickDirectory?: PluginHostOperations['pickDirectory']
  /** Shell reveal/open and renderer navigation/toast. */
  host?: Omit<PluginHostOperations, 'pickDirectory'>
  /** Desktop AI gateway bridge. */
  ai?: PluginAiHost
  /** Renderer dialog presenter for ui.prompt/ui.confirm. */
  promptPresenter?: PluginPromptPresenter
  /** IPC reply sink paired with `promptPresenter`. */
  promptResponder?: { respond(requestId: string, answer: { submitted?: boolean; confirmed?: boolean; value?: string }): boolean }
  /** Relays host events to mounted plugin panels. */
  publishEvent?(pluginId: string, event: { type: string; payload?: unknown }): void
}

export interface PluginRuntimeBundle {
  service: PluginService
  /** Registry index, downloads and the update state machine. */
  market: PluginMarketService
  /** Startup-delayed and periodic background update checks. */
  updates: PluginUpdateScheduler
  host: PluginUtilityHost
  hotReload: PluginHotReloader
  /** Sets the renderer-backed prompt reply sink for the IPC handler. */
  promptResponder: { respond(requestId: string, answer: { submitted?: boolean; confirmed?: boolean; value?: string }): boolean } | undefined
  shutdown(): Promise<void>
}

/** Pass to `protocol.registerSchemesAsPrivileged` before `app.whenReady()`. */
export function pluginSchemePrivileges(): Array<{
  scheme: string
  privileges: { standard: true; secure: true; supportFetchAPI: true; stream: true; corsEnabled: true }
}> {
  return [
    {
      scheme: 'plugin',
      // corsEnabled: sandboxed panel frames fetch module scripts cross-origin.
      privileges: { standard: true, secure: true, supportFetchAPI: true, stream: true, corsEnabled: true }
    }
  ]
}

export function createPluginRuntimeBundle(options: PluginRuntimeBundleOptions): PluginRuntimeBundle {
  const pluginsRoot = path.join(options.workspace.root, 'plugins')
  let service!: PluginService

  const hotReload = new PluginHotReloader({
    onReload: async (pluginId) => {
      await service.reload(pluginId)
      armWatcher(pluginId)
    },
    onError: (pluginId, error) => {
      options.onLog?.(
        pluginId,
        'error',
        `hot reload failed: ${error instanceof Error ? error.message : String(error)}`
      )
    }
  })

  const host = new PluginUtilityHost({
    childModulePath: pluginChildPath,
    fork: (modulePath, args, forkOptions) =>
      utilityProcess.fork(modulePath, args, forkOptions as never) as unknown as HostChildProcess,
    createChannel: () => {
      const channel = new MessageChannelMain()
      return {
        port1: channel.port1 as unknown as HostMessagePort,
        port2: channel.port2 as unknown as HostMessagePort
      }
    },
    invoke: (pluginId, method, params) => service.call(pluginId, method, params),
    onCrash: (pluginId, reason) => {
      void service.disable(pluginId, reason).catch((error: unknown) => {
        options.onLog?.(
          pluginId,
          'error',
          `crash isolation failed: ${error instanceof Error ? error.message : String(error)}`
        )
      })
    },
    onLog: (pluginId, level, message) => options.onLog?.(pluginId, level, message)
  })

  const files = new PluginFilesService({ global: options.workspace.global })
  const blobs = new PluginBlobStore({ pluginsRoot, files })
  const watch = new PluginWatchService({
    emit: (pluginId, event) => {
      host.emitEvent(pluginId, event)
      try {
        options.publishEvent?.(pluginId, event)
      } catch {
        // A missing renderer must not break file tracking.
      }
    }
  })
  const hostOperations: PluginHostOperations = {
    ...(options.pickDirectory === undefined ? {} : { pickDirectory: options.pickDirectory }),
    ...options.host
  }

  service = new PluginService({
    workspace: options.workspace,
    runtime: host,
    skills: options.skills,
    rules: options.rules,
    rpcHandlers: options.rpcHandlers,
    fetchImpl: options.fetchImpl,
    clipboard: options.clipboard,
    notifications: options.notifications,
    editor: options.editor,
    files,
    blobs,
    watch,
    host: hostOperations,
    ...(options.ai === undefined ? {} : { ai: options.ai }),
    ...(options.promptPresenter === undefined ? {} : { promptPresenter: options.promptPresenter }),
    pluginName: (pluginId) => {
      try {
        return service.get(pluginId).name
      } catch {
        return pluginId
      }
    },
    onChange: (info) => {
      rearmAll()
      options.onChange?.(info)
    }
  })

  const market = new PluginMarketService({
    workspace: options.workspace,
    service,
    settings: new SettingsStore(options.workspace.global.db, 'workspace'),
    transport: options.marketTransport,
    onSilentUpdate: (update) => options.onSilentUpdate?.(update)
  })
  const updates = new PluginUpdateScheduler({ market })
  updates.start()

  if (options.protocol !== undefined) {
    registerPluginProtocol(options.protocol, { pluginsRoot })
  }

  function readManifestShell(pluginId: string): { main: string; renderer?: string } | null {
    try {
      const raw = JSON.parse(
        readFileSync(path.join(pluginsRoot, pluginId, 'manifest.json'), 'utf8')
      ) as { main?: unknown; renderer?: unknown }
      if (typeof raw.main !== 'string') return null
      return {
        main: raw.main,
        ...(typeof raw.renderer === 'string' ? { renderer: raw.renderer } : {})
      }
    } catch {
      return null
    }
  }

  function armWatcher(pluginId: string): void {
    const info = service.list().find((candidate) => candidate.id === pluginId)
    if (info === undefined || !info.enabled) {
      hotReload.unwatch(pluginId)
      return
    }
    const manifest = readManifestShell(pluginId)
    if (manifest === null) {
      hotReload.unwatch(pluginId)
      return
    }
    hotReload.watchPlugin(pluginId, path.join(pluginsRoot, pluginId), manifest)
  }

  function rearmAll(): void {
    const enabled = new Set(service.list().filter((info) => info.enabled).map((info) => info.id))
    for (const pluginId of enabled) armWatcher(pluginId)
    for (const pluginId of service.list().map((info) => info.id)) {
      if (!enabled.has(pluginId)) hotReload.unwatch(pluginId)
    }
  }

  return {
    service,
    market,
    updates,
    host,
    hotReload,
    promptResponder: options.promptResponder,
    async shutdown(): Promise<void> {
      updates.stop()
      hotReload.close()
      watch.close()
      await service.shutdown()
    }
  }
}