/**
 * Test support for the renderer: a fake `window.researchos`, a bridge that
 * can emit app events, and the standard provider render.
 *
 * Not imported by the application bundle; tests use it to exercise the real
 * IPC client, query wiring and shell against a controllable host instead of
 * mocking the client itself.
 */

import {
  AppError,
  AppErrorCode,
  type AppErrorShape,
  type AppEventPayload,
  type AppEventType,
  type AppPreferences,
  type IpcResult,
  type McpStatus,
  type Project,
  type ResearchOsBridge,
  type WorkspaceStatus
} from '@researchos/shared'

export interface BridgeCall {
  readonly channel: string
  readonly request: unknown
}

export interface FakeBridgeOptions {
  readonly workspace?: Partial<WorkspaceStatus>
  readonly projects?: readonly Project[]
  /** Return this envelope failure for project.list (error-state tests). */
  readonly projectsError?: AppErrorShape
  readonly preferences?: Partial<AppPreferences>
  readonly mcpStatus?: Partial<McpStatus>
  /**
   * Per-channel overrides. Throw an `AppError` to produce a typed failure
   * envelope; the fake bridge never throws across "IPC".
   */
  readonly handlers?: Readonly<Record<string, (request: unknown) => unknown | Promise<unknown>>>
}

export interface FakeBridge {
  readonly calls: readonly BridgeCall[]
  /** Deliver an app event to every renderer subscription of that type. */
  emit<E extends AppEventType>(event: E, payload: AppEventPayload<E>): void
  uninstall(): void
}

const DEFAULT_PREFERENCES: AppPreferences = {
  locale: 'en',
  theme: 'light',
  weekStartsOn: 1,
  timezone: 'UTC'
}

const DEFAULT_MCP_STATUS: McpStatus = {
  enabled: false,
  httpEnabled: false,
  running: false,
  host: '127.0.0.1',
  port: 39217,
  path: '/mcp',
  accessMode: 'read-only',
  aiAvailable: false,
  toolsCount: 0,
  clientCount: 0,
  pendingConfirmations: 0,
  startedAt: null,
  lastError: null
}

export function makeProject(id: string, name: string): Project {
  return {
    id,
    name,
    summary: '',
    goals: '',
    focusMd: '',
    status: 'active',
    archivedAt: null,
    createdAt: '2026-01-01T00:00:00.000Z',
    updatedAt: '2026-01-01T00:00:00.000Z'
  }
}

/** installBridge puts a controllable host on `window.researchos`. */
export function installBridge(options: FakeBridgeOptions = {}): FakeBridge {
  const calls: BridgeCall[] = []
  const listeners = new Map<AppEventType, Set<(payload: unknown) => void>>()
  const workspace: WorkspaceStatus = {
    root: 'F:/Workspace',
    name: 'Workspace',
    initialized: true,
    markerVersion: 1,
    schemaVersion: 1,
    open: true,
    projectCount: options.projects?.length ?? 0,
    warnings: [],
    ...options.workspace
  }
  const preferences: AppPreferences = { ...DEFAULT_PREFERENCES, ...options.preferences }
  const mcpStatus: McpStatus = { ...DEFAULT_MCP_STATUS, ...options.mcpStatus }

  const respond = async (channel: string, request: unknown): Promise<IpcResult<unknown>> => {
    const custom = options.handlers?.[channel]
    if (custom !== undefined) {
      try {
        return { ok: true, data: await custom(request) }
      } catch (error) {
        if (error instanceof AppError) return { ok: false, error: error.toShape() }
        return {
          ok: false,
          error: {
            code: AppErrorCode.Internal,
            message: error instanceof Error ? error.message : String(error)
          }
        }
      }
    }
    switch (channel) {
      case 'workspace.status':
        return { ok: true, data: workspace }
      case 'project.list':
        if (options.projectsError !== undefined) {
          return { ok: false, error: options.projectsError }
        }
        return { ok: true, data: options.projects ?? [] }
      case 'project.get': {
        const id =
          typeof request === 'object' && request !== null && 'id' in request
            ? request.id
            : undefined
        const project = (options.projects ?? []).find((candidate) => candidate.id === id)
        if (project === undefined) {
          return {
            ok: false,
            error: { code: AppErrorCode.NotFound, message: `project not found: ${String(id)}` }
          }
        }
        return { ok: true, data: project }
      }
      case 'app.preferences.get':
        return { ok: true, data: preferences }
      case 'app.preferences.set': {
        const patch =
          typeof request === 'object' && request !== null && 'patch' in request
            ? request.patch
            : {}
        if (typeof patch !== 'object' || patch === null) return { ok: true, data: preferences }
        // The renderer sends Partial<AppPreferences>; this fake host only echoes
        // it, so the structural read is enough at this boundary.
        const typedPatch = patch as Partial<AppPreferences>
        return { ok: true, data: { ...preferences, ...typedPatch } }
      }
      case 'mcp.status':
        return { ok: true, data: mcpStatus }
      default:
        return {
          ok: false,
          error: {
            code: AppErrorCode.Internal,
            message: `unexpected channel in test: ${channel}`
          }
        }
    }
  }

  const bridge: ResearchOsBridge = {
    invoke: (async (channel: string, request: unknown) => {
      calls.push({ channel, request })
      return respond(channel, request)
    }) as ResearchOsBridge['invoke'],

    on<E extends AppEventType>(
      event: E,
      handler: (payload: AppEventPayload<E>) => void
    ): () => void {
      const set = listeners.get(event) ?? new Set<(payload: unknown) => void>()
      listeners.set(event, set)
      const wrapped = handler as (payload: unknown) => void
      set.add(wrapped)
      return () => set.delete(wrapped)
    },

    off<E extends AppEventType>(event: E, handler: (payload: AppEventPayload<E>) => void): void {
      listeners.get(event)?.delete(handler as (payload: unknown) => void)
    },

    platform: 'win32',
    versions: { electron: '45.3.4', chrome: '124.0.0' }
  }

  window.researchos = bridge

  return {
    calls,
    emit: (event, payload) => {
      for (const handler of listeners.get(event) ?? []) handler(payload)
    },
    uninstall: () => {
      if (window.researchos === bridge) delete window.researchos
    }
  }
}