import { networkInterfaces } from 'node:os'

import type {
  AppEvent,
  McpAccessMode,
  McpConnectionReport,
  McpStatus,
  McpToolInfo
} from '@researchos/shared'
import { AppErrorCode } from '@researchos/shared'
import { appError } from '@researchos/core/errors'
import type { SettingsStore } from '@researchos/core/settings'
import type { Clients } from '@researchos/core/modules/mcp/clients'
import type { Confirmations } from '@researchos/core/modules/mcp/confirmations'
import { ConfirmationRunner, type ConfirmationEffectMap } from '@researchos/core/modules/mcp/executors'
import type { Jobs } from '@researchos/core/modules/mcp/jobs'
import { parseAccessMode } from '@researchos/core/modules/mcp/policy'
import { MCP_HOST, MCP_PATH, MCP_PORT } from '@researchos/core/modules/mcp/sdk-server'
import type { Principal } from '@researchos/core/modules/mcp/types'
import { mcpValidators } from '@researchos/core/validation/mcp'

import type { CommandRouter } from '../router'

/** Defaults for the `mcp.*` settings keys. */
export const MCP_SETTINGS_DEFAULTS = {
  enabled: true,
  httpEnabled: false,
  accessMode: 'read-write' as McpAccessMode,
  retentionDays: 30
} as const

/** The in-app confirmation center: an empty client id sees every client. */
const LOCAL_PRINCIPAL: Principal = {
  clientId: '',
  clientName: 'desktop',
  transport: 'http'
}

/**
 * Workspace-bound MCP services. `null` before a workspace opens, so every
 * channel that needs the runtime can answer NOT_WORKSPACE honestly instead of
 * surfacing "No handler registered".
 */
export interface McpRuntimeAccess {
  readonly clients: Clients
  readonly confirmations: Confirmations
  readonly jobs: Jobs
  readonly effects: ConfirmationEffectMap
  /** Catalog projection for the *current* access mode. */
  tools(): McpToolInfo[]
  /** Idempotently enable and start the HTTP listener. */
  start(): Promise<void>
  /** Idempotently stop and persist the disabled state. */
  stop(): Promise<void>
  /** Re-read settings and rebind/start/stop the listener. */
  applySettings(): Promise<void>
}

export interface McpHandlerDeps {
  /** Live server status, or null when the workspace runtime is not open. */
  status(): McpStatus | null
  settings(): SettingsStore | null
  runtime(): McpRuntimeAccess | null
  appVersion: string
  /** Renderer event sink for `mcp.status.changed` / `mcp.confirmations.changed`. */
  publish?(event: AppEvent): void
}

function readAccessMode(settings: SettingsStore | null): McpAccessMode {
  if (settings === null) return MCP_SETTINGS_DEFAULTS.accessMode
  try {
    return parseAccessMode(settings.get('mcp.access_mode'))
  } catch {
    return MCP_SETTINGS_DEFAULTS.accessMode
  }
}

/** Non-internal IPv4 addresses of this machine; the LAN access view lists these. */
function listLanAddresses(): string[] {
  const out: string[] = []
  for (const entries of Object.values(networkInterfaces())) {
    for (const entry of entries ?? []) {
      if (entry.family === 'IPv4' && !entry.internal) out.push(entry.address)
    }
  }
  return out
}

/**
 * Honest status for Settings → MCP. Pre-workspace it reports the persisted
 * defaults (or the built-in ones), zero tools and an explanatory `lastError`
 * instead of failing the query and rendering an error card.
 */
export function mcpStatusFrom(snapshot: McpStatus | null, settings: SettingsStore | null): McpStatus {
  const enabled = settings?.getBoolean('mcp.enabled', MCP_SETTINGS_DEFAULTS.enabled) ?? MCP_SETTINGS_DEFAULTS.enabled
  const httpEnabled =
    settings?.getBoolean('mcp.http.enabled', MCP_SETTINGS_DEFAULTS.httpEnabled) ??
    MCP_SETTINGS_DEFAULTS.httpEnabled
  const port = settings?.getNumber('mcp.port', MCP_PORT) ?? MCP_PORT
  const host = settings?.get('mcp.http.host') ?? MCP_HOST
  const accessMode = readAccessMode(settings)
  if (snapshot === null) {
    return {
      enabled,
      httpEnabled,
      running: false,
      host,
      port,
      path: MCP_PATH,
      accessMode,
      aiAvailable: false,
      toolsCount: 0,
      clientCount: 0,
      pendingConfirmations: 0,
      startedAt: null,
      lastError: '工作区未打开，MCP 服务不可用',
      lanAddresses: listLanAddresses()
    }
  }
  return {
    ...snapshot,
    enabled,
    httpEnabled,
    host: snapshot.running ? snapshot.host : host,
    port: snapshot.running ? snapshot.port : port,
    accessMode,
    lanAddresses: listLanAddresses()
  }
}

function requireSettings(deps: McpHandlerDeps): SettingsStore {
  const settings = deps.settings()
  if (settings === null) {
    throw appError(AppErrorCode.NotWorkspace, '工作区未打开，MCP 设置不可用')
  }
  return settings
}

function requireRuntime(deps: McpHandlerDeps): McpRuntimeAccess {
  const runtime = deps.runtime()
  if (runtime === null) {
    throw appError(AppErrorCode.NotWorkspace, '工作区未打开，MCP 服务不可用')
  }
  return runtime
}

/** Settings → MCP: the full settings/confirmation/job surface. */
export function registerMcpHandlers(router: CommandRouter, deps: McpHandlerDeps): void {
  const status = (): McpStatus => mcpStatusFrom(deps.status(), deps.settings())
  const publishStatus = (): void => {
    deps.publish?.({ type: 'mcp.status.changed', status: status() })
  }
  const publishConfirmations = (): void => {
    deps.publish?.({ type: 'mcp.confirmations.changed' })
  }

  router.registerHandler('mcp.status', mcpValidators['mcp.status'], () => status())

  router.registerHandler('mcp.probe', mcpValidators['mcp.probe'], (): McpConnectionReport => {
    const current = status()
    return {
      ipcOk: true,
      ipcError: '',
      httpEnabled: current.httpEnabled,
      httpOk: current.running,
      httpError: current.running
        ? ''
        : (current.lastError ??
          (current.httpEnabled ? 'MCP 服务未启动' : 'MCP HTTP 服务未启用')),
      toolCount: current.toolsCount,
      serverName: 'ResearchOS',
      version: deps.appVersion
    }
  })

  router.registerHandler('mcp.retention.get', mcpValidators['mcp.retention.get'], () => ({
    days:
      deps.settings()?.getNumber('mcp.retention_days', MCP_SETTINGS_DEFAULTS.retentionDays) ??
      MCP_SETTINGS_DEFAULTS.retentionDays
  }))

  router.registerHandler('mcp.retention.set', mcpValidators['mcp.retention.set'], (request) => {
    requireSettings(deps).set('mcp.retention_days', String(request.days))
    return { days: request.days }
  })

  router.registerHandler('mcp.start', mcpValidators['mcp.start'], async () => {
    await requireRuntime(deps).start()
    publishStatus()
    return status()
  })

  router.registerHandler('mcp.stop', mcpValidators['mcp.stop'], async () => {
    await requireRuntime(deps).stop()
    publishStatus()
    return status()
  })

  router.registerHandler('mcp.settings.update', mcpValidators['mcp.settings.update'], async (request) => {
    const settings = requireSettings(deps)
    const runtime = requireRuntime(deps)
    if (request.enabled !== undefined) settings.set('mcp.enabled', request.enabled ? '1' : '0')
    if (request.httpEnabled !== undefined) settings.set('mcp.http.enabled', request.httpEnabled ? '1' : '0')
    if (request.host !== undefined) settings.set('mcp.http.host', request.host)
    if (request.port !== undefined) settings.set('mcp.port', String(request.port))
    if (request.accessMode !== undefined) settings.set('mcp.access_mode', request.accessMode)
    await runtime.applySettings()
    publishStatus()
    return status()
  })

  router.registerHandler('mcp.clients.list', mcpValidators['mcp.clients.list'], () =>
    requireRuntime(deps).clients.list()
  )

  router.registerHandler('mcp.clients.create', mcpValidators['mcp.clients.create'], (request) => {
    const { clients } = requireRuntime(deps)
    const created = clients.create(request.name)
    const client =
      request.enabled === false ? clients.update(created.client.id, { enabled: false }) : created.client
    publishStatus()
    return { client, token: created.token }
  })

  router.registerHandler('mcp.clients.update', mcpValidators['mcp.clients.update'], (request) => {
    const client = requireRuntime(deps).clients.update(request.clientId, {
      ...(request.name === undefined ? {} : { name: request.name }),
      ...(request.enabled === undefined ? {} : { enabled: request.enabled })
    })
    publishStatus()
    return client
  })

  router.registerHandler('mcp.clients.delete', mcpValidators['mcp.clients.delete'], (request): { deleted: boolean } => {
    requireRuntime(deps).clients.revoke(request.clientId)
    publishStatus()
    return { deleted: true }
  })

  router.registerHandler('mcp.confirmations.list', mcpValidators['mcp.confirmations.list'], (request) =>
    requireRuntime(deps).confirmations.list(LOCAL_PRINCIPAL, {
      ...(request.state === undefined ? {} : { state: request.state }),
      ...(request.projectId === undefined ? {} : { projectId: request.projectId }),
      ...(request.limit === undefined ? {} : { limit: request.limit })
    })
  )

  router.registerHandler('mcp.confirmations.get', mcpValidators['mcp.confirmations.get'], (request) =>
    requireRuntime(deps).confirmations.get(LOCAL_PRINCIPAL, request.id)
  )

  router.registerHandler('mcp.confirmations.approve', mcpValidators['mcp.confirmations.approve'], async (request) => {
    const runtime = requireRuntime(deps)
    const runner = new ConfirmationRunner(runtime.confirmations, runtime.effects)
    const confirmation = await runner.approveAndExecute(request.id, 'user', request.note ?? '')
    publishConfirmations()
    publishStatus()
    return confirmation
  })

  router.registerHandler('mcp.confirmations.reject', mcpValidators['mcp.confirmations.reject'], (request) => {
    const runtime = requireRuntime(deps)
    const confirmation = new ConfirmationRunner(runtime.confirmations, runtime.effects).reject(
      request.id,
      'user',
      request.note ?? ''
    )
    publishConfirmations()
    publishStatus()
    return confirmation
  })

  router.registerHandler('mcp.confirmations.cancel', mcpValidators['mcp.confirmations.cancel'], (request) => {
    const confirmation = requireRuntime(deps).confirmations.cancel(request.id, 'user')
    publishConfirmations()
    publishStatus()
    return confirmation
  })

  router.registerHandler('mcp.jobs.list', mcpValidators['mcp.jobs.list'], (request) =>
    requireRuntime(deps).jobs.list(LOCAL_PRINCIPAL, {
      ...(request.state === undefined ? {} : { state: request.state }),
      ...(request.projectId === undefined ? {} : { projectId: request.projectId })
    })
  )

  router.registerHandler('mcp.jobs.get', mcpValidators['mcp.jobs.get'], (request) =>
    requireRuntime(deps).jobs.get(LOCAL_PRINCIPAL, request.id)
  )

  router.registerHandler('mcp.jobs.cancel', mcpValidators['mcp.jobs.cancel'], (request) =>
    requireRuntime(deps).jobs.cancel(LOCAL_PRINCIPAL, request.id)
  )

  router.registerHandler('mcp.tools.list', mcpValidators['mcp.tools.list'], () => requireRuntime(deps).tools())
}