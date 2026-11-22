import * as nodePath from 'node:path'

import {
  app,
  BrowserWindow,
  dialog,
  ipcMain,
  nativeTheme,
  protocol,
  shell,
  systemPreferences,
  type IpcMainInvokeEvent
} from 'electron'

import {
  AppError,
  AppErrorCode,
  appEventChannel,
  COMMAND_CHANNELS,
  type AppEventPayload,
  type AppEventType,
  type PluginHostEvent,
  type PluginPermission,
  type SenderContext
} from '@researchos/shared'

import { FlushCoordinator } from './flush'
import { AppIntegration } from './wire/app'
import { registerAppHandlers } from './ipc/handlers/app'
import { CommandRouter } from './ipc/router'
import { installApplicationMenu } from './menu'
import { installRosAssetProtocol, rosAssetSchemePrivileges } from './protocol/ros-asset'
import { createPluginRuntimeBundle, pluginSchemePrivileges, type PluginRuntimeBundle } from './plugins/runtime'
import { PluginPromptHost } from './plugins/prompt-host'
import { createPluginDomainHandlers } from './plugins/domain-handlers'
import { runNativeSmoke } from './native-smoke'
import { createMainWindow, resolveAppIcon } from './window'

// Privileged scheme registration must happen before app.whenReady().
protocol.registerSchemesAsPrivileged([...pluginSchemePrivileges(), ...rosAssetSchemePrivileges()])

const flushCoordinator = new FlushCoordinator()

let mainWindow: BrowserWindow | null = null
let integration: AppIntegration | null = null
let pluginBundle: PluginRuntimeBundle | null = null
let quitRequested = false
let shutdownInFlight = false

const router = new CommandRouter({
  logger: {
    error: (message, fields) => console.error(message, fields)
  }
})

function senderContext(event: IpcMainInvokeEvent): SenderContext {
  const window = BrowserWindow.fromWebContents(event.sender)
  return {
    windowId: window?.id ?? -1,
    webContentsId: event.sender.id,
    isMainWindow: mainWindow !== null && event.sender === mainWindow.webContents
  }
}

function sendAppEvent<E extends AppEventType>(window: BrowserWindow, event: AppEventPayload<E>): void {
  if (window.isDestroyed()) return
  window.webContents.send(appEventChannel(event.type), event)
}

function sendSystemPreferences(window: BrowserWindow): void {
  sendAppEvent(window, { type: 'theme.system.changed', dark: nativeTheme.shouldUseDarkColors })
  sendAppEvent(window, {
    type: 'prefers.reduced-motion.changed',
    reduced: systemPreferences.getAnimationSettings().prefersReducedMotion
  })
}

function registerDispatcher(): void {
  // Deduplicate: ipcMain.handle throws on a repeated channel and would abort
  // registration for every channel after it.
  for (const channel of new Set(COMMAND_CHANNELS)) {
    ipcMain.handle(channel, (event, payload: unknown) => router.invoke(channel, payload, senderContext(event)))
  }
}

function attachWindowLifecycle(window: BrowserWindow): void {
  window.webContents.on('did-finish-load', () => sendSystemPreferences(window))

  window.on('close', (event) => {
    if (quitRequested) return
    event.preventDefault()
    if (shutdownInFlight) return
    shutdownInFlight = true

    // Register the pending flush before notifying the renderer so a fast ack
    // cannot race past the handshake.
    const flush = flushCoordinator.request('close')
    sendAppEvent(window, { type: 'app.flush', reason: 'close' })

    void flush.then((result) => {
      if (result === 'timeout') console.warn('renderer flush timed out; closing window')
      quitRequested = true
      app.quit()
    })
  })
}

async function createIntegration(router: CommandRouter): Promise<void> {
  integration = await AppIntegration.create({
    appVersion: app.getVersion(),
    publish: (event) => {
      if (mainWindow !== null) sendAppEvent(mainWindow, event)
    }
  })
  integration.registerHandlers(router)

  integration.setWorkspaceOpenHook((manager) => mountPlugins(manager))
}

/** Rebuilds the plugin runtime whenever a workspace is opened or replaced. */
function mountPlugins(manager: import('@researchos/core/workspace/index').WorkspaceManager | null): void {
  if (pluginBundle !== null) {
    void pluginBundle.shutdown()
    pluginBundle = null
  }
  if (manager === null || integration === null) return
  const app = integration
  const promptHost = new PluginPromptHost({
    publishRequest: (request) => app.publishPluginPrompt(request),
    publishCancel: (requestId) => app.publishPluginPromptCancel(requestId),
    permissionsFor: (pluginId): PluginPermission[] => {
      try {
        return pluginBundle?.service.get(pluginId).permissions ?? []
      } catch {
        return []
      }
    },
    nameFor: (pluginId) => {
      try {
        return pluginBundle?.service.get(pluginId).name ?? pluginId
      } catch {
        return pluginId
      }
    }
  })
  try {
    try {
      protocol.unhandle('plugin')
    } catch {
      // no handler yet (first mount) or a previous handle is already gone
    }
    pluginBundle = createPluginRuntimeBundle({
      workspace: manager,
      protocol,
      skills: app.pluginSkillSink(),
      rules: { registerExtractor: (extractor) => app.registerPluginMetricExtractor(extractor) },
      rpcHandlers: createPluginDomainHandlers({
        notes: (projectId) => app.notes(projectId),
        literature: () => app.literature(),
        ideas: () => app.ideas(),
        experiments: (projectId) => app.experiments(projectId),
        kanban: (projectId) => app.kanban(projectId),
        behavior: (projectId) => app.behavior(projectId),
        projects: () => app.projectService(),
        globalDb: () => manager.global.db
      }),
      onChange: () => app.publishPluginsChanged(),
      onSilentUpdate: (update) => app.publishPluginsUpdated(update),
      onLog: (pluginId, level, message) => console.log(`[plugin:${pluginId}] ${level}: ${message}`),
      pickDirectory: async (_pluginId, params) => {
        const window = BrowserWindow.getFocusedWindow() ?? mainWindow
        const result = window === null
          ? await dialog.showOpenDialog({
              properties: ['openDirectory'],
              ...(params.title === undefined ? {} : { title: params.title })
            })
          : await dialog.showOpenDialog(window, {
              properties: ['openDirectory'],
              ...(params.title === undefined ? {} : { title: params.title })
            })
        const dirPath = result.canceled ? undefined : result.filePaths[0]
        if (dirPath === undefined) return { cancelled: true }
        return { cancelled: false, dirPath, displayName: nodePath.basename(dirPath) }
      },
      host: {
        revealInFolder: (_pluginId, absolutePath) => {
          shell.showItemInFolder(absolutePath)
          return true
        },
        openPath: async (_pluginId, absolutePath) => (await shell.openPath(absolutePath)) === '',
        navigate: (pluginId, params) => {
          if (mainWindow === null) return false
          sendAppEvent(mainWindow, {
            type: 'plugins.navigate',
            pluginId,
            target: params.target,
            ...(params.projectId === undefined ? {} : { projectId: params.projectId }),
            ...(params.entityType === undefined ? {} : { entityType: params.entityType }),
            ...(params.entityId === undefined ? {} : { entityId: params.entityId })
          })
          return true
        },
        toast: (pluginId, params) => {
          if (mainWindow === null) return
          sendAppEvent(mainWindow, {
            type: 'plugins.toast',
            pluginId,
            message: params.message,
            level: params.level ?? 'info'
          })
        },
        context: () => app.pluginHostContext()
      },
      ai: {
        summarize: async (_pluginId, params) => {
          const gateway = app.aiGateway()
          if (gateway === null) {
            throw new AppError(AppErrorCode.AiUnavailable, 'no AI provider is configured')
          }
          try {
            const result = await gateway.chat('chat_main', {
              messages: [
                {
                  role: 'system',
                  content:
                    params.instruction ??
                    'Summarize the user text concisely. Reply with the summary only.'
                },
                { role: 'user', content: params.text }
              ],
              ...(params.maxOutputTokens === undefined
                ? {}
                : { maxOutputTokens: params.maxOutputTokens })
            })
            return { summary: result.content, model: result.modelId }
          } catch (error) {
            throw new AppError(
              AppErrorCode.AiUnavailable,
              error instanceof Error ? error.message : String(error)
            )
          }
        },
        complete: async (_pluginId, params) => {
          const gateway = app.aiGateway()
          if (gateway === null) {
            throw new AppError(AppErrorCode.AiUnavailable, 'no AI provider is configured')
          }
          try {
            const result = await gateway.chat('chat_main', {
              messages: [
                {
                  role: 'system',
                  content:
                    (params.system ?? '') +
                    (params.jsonMode === true
                      ? `${params.system === undefined ? '' : '\n'}Return a single valid JSON value with no markdown fence.`
                      : '')
                },
                { role: 'user', content: params.prompt }
              ],
              ...(params.maxOutputTokens === undefined
                ? {}
                : { maxOutputTokens: params.maxOutputTokens })
            })
            return { text: result.content, model: result.modelId }
          } catch (error) {
            throw new AppError(
              AppErrorCode.AiUnavailable,
              error instanceof Error ? error.message : String(error)
            )
          }
        }
      },
      promptPresenter: promptHost.presenter,
      promptResponder: promptHost,
      publishEvent: (pluginId: string, event: PluginHostEvent) => app.publishPluginEvent(pluginId, event)
    })
    app.attachPluginRuntime(pluginBundle)
  } catch (error) {
    console.error('plugin runtime mount failed', error)
  }
}

function openMainWindow(): BrowserWindow {
  const window = createMainWindow()
  mainWindow = window
  window.on('closed', () => {
    if (mainWindow === window) mainWindow = null
  })
  attachWindowLifecycle(window)
  return window
}

function bootstrap(): void {
  registerAppHandlers(router, {
    getMainWindow: () => mainWindow,
    flush: flushCoordinator,
    runNativeSmoke: () => runNativeSmoke(':memory:'),
    ...(integration === null ? {} : { flushAll: () => integration!.flushAll() })
  })
  registerDispatcher()
  installApplicationMenu(!app.isPackaged)

  installRosAssetProtocol(() => integration?.workspaceRoot ?? null)
  mainWindow = openMainWindow()

  if (process.platform === 'darwin') {
    const icon = resolveAppIcon()
    if (icon !== undefined) app.dock?.setIcon(icon)
  }

  nativeTheme.on('updated', () => {
    if (mainWindow !== null) sendSystemPreferences(mainWindow)
  })

  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) mainWindow = openMainWindow()
  })
}

const hasSingleInstanceLock = app.requestSingleInstanceLock()

if (!hasSingleInstanceLock) {
  app.quit()
} else {
  app.on('second-instance', () => {
    if (mainWindow === null) {
      if (app.isReady()) mainWindow = openMainWindow()
      return
    }
    if (mainWindow.isMinimized()) mainWindow.restore()
    mainWindow.show()
    mainWindow.focus()
  })

  void app.whenReady().then(async () => {
    await createIntegration(router)
    bootstrap()
  })

  app.on('before-quit', () => {
  if (pluginBundle !== null) void pluginBundle.shutdown()
})

app.on('window-all-closed', () => {
    if (process.platform !== 'darwin') app.quit()
  })
}