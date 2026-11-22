/**
 * MCP Streamable HTTP host (rewrite plan §3.6).
 *
 * Official SDK only: one `McpServer` + `StreamableHTTPServerTransport` per
 * POST, stateless (`sessionIdGenerator: undefined`), JSON responses
 * (`enableJsonResponse: true`). Bindable to any address; loopback-origin
 * requests are trusted without a token, everything else needs a registry
 * Bearer key. DNS-rebinding protection stays on loopback binds, foreign
 * browser origins are rejected, GET/DELETE answered 405.
 */
import type { Server as HttpServer } from 'node:http'
import type { AddressInfo } from 'node:net'
import { isIP } from 'node:net'

import type { McpAccessMode, McpStatus } from '@researchos/shared'
import { AppErrorCode } from '@researchos/shared'
import { createMcpExpressApp } from '@modelcontextprotocol/sdk/server/express.js'
import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js'
import { requireBearerAuth } from '@modelcontextprotocol/sdk/server/auth/middleware/bearerAuth.js'
import { InvalidTokenError } from '@modelcontextprotocol/sdk/server/auth/errors.js'
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js'
import { SUPPORTED_PROTOCOL_VERSIONS, type CallToolResult } from '@modelcontextprotocol/sdk/types.js'
import { z } from 'zod'

import { appError } from '../../errors'
import { SystemClock, formatTimestamp, type Clock } from '../../clock'
import type { ConfirmService } from '../../confirm'
import type { EventBus } from '../../events'
import type { WorkspaceManager } from '../../workspace'
import { buildCatalog, type Catalog } from './catalog'
import type { Clients } from './clients'
import type { Confirmations } from './confirmations'
import { ToolHost } from './host'
import { IdempotencyStore } from './idempotency'
import type { Jobs } from './jobs'
import { RateLimiter } from './limit'
import { isMutating, toolsForMode } from './policy'
import { IDEMPOTENCY_KEY_FIELD } from './tools'
import { createCoreTools, type ServerStatusValues } from './tools/core'
import { createMiscTools } from './tools/misc'
import type { AccessMode, Principal, ToolDefinition, ToolResult } from './types'

export const MCP_HOST = '127.0.0.1'
export const MCP_PORT = 39217
export const MCP_PATH = '/mcp'

export interface McpServerOptions {
  workspace: WorkspaceManager
  clients: Clients
  confirmations: Confirmations
  jobs: Jobs
  idempotency?: IdempotencyStore
  limiter?: RateLimiter
  events: EventBus
  confirm: ConfirmService
  clock?: Clock
  appVersion: string
  host?: string
  port?: number
  path?: string
  /** Trust boundary override: which remote addresses count as this machine. */
  isLoopbackClient?: (remoteAddress: string | undefined) => boolean
  accessMode?: () => AccessMode
  aiAvailable?: () => boolean | Promise<boolean>
  zoteroAvailable?: () => boolean
  modules?: () => string[]
  /** Extra domain tool arrays, aggregated with the core/misc set. */
  domainTools?: ReadonlyArray<{ module: string; tools: readonly ToolDefinition[] }>
  /** Composition root: module runtimes merged into every tool context. */
  servicesFor?: (projectId: string | null) => Record<string, unknown>
  enforceToolCount?: boolean
}

function toPayload(result: ToolResult): CallToolResult {
  return {
    content: result.content.map((item) => ({ type: 'text' as const, text: item.text })),
    structuredContent: result.structuredContent,
    ...(result.isError === true ? { isError: true } : {}),
    ...(result._meta === undefined ? {} : { _meta: result._meta })
  }
}

/** The mutable JSON-RPC body we preprocess before SDK validation. */
interface JsonRpcBody {
  method?: unknown
  params?: {
    name?: unknown
    arguments?: unknown
    _meta?: Record<string, unknown>
  }
}

/**
 * `_meta.idempotencyKey` support (§3.6): the SDK validates `arguments` before
 * any handler runs, so the host copies the key into the declared argument just
 * before validation. If both are present and differ, the value is left alone
 * and the policy layer returns INVALID_ARGUMENT.
 */
export function prepareArguments(body: unknown, catalog: Catalog): void {
  const message = body as JsonRpcBody
  if (message === null || typeof message !== 'object') return
  if (message.method !== 'tools/call') return
  const params = message.params
  if (params === undefined || typeof params !== 'object') return
  if (typeof params.name !== 'string') return
  const definition = catalog.byName.get(params.name)
  if (definition === undefined || !isMutating(definition)) return

  const metaKey = params._meta?.['idempotencyKey']
  if (typeof metaKey !== 'string') return
  if (params.arguments === undefined || params.arguments === null) params.arguments = {}
  if (typeof params.arguments !== 'object' || Array.isArray(params.arguments)) return
  const args = params.arguments as Record<string, unknown>
  if (args[IDEMPOTENCY_KEY_FIELD] === undefined) args[IDEMPOTENCY_KEY_FIELD] = metaKey
}

/**
 * The slice of Express this file uses. Named here because `express` is a
 * transitive SDK dependency and its types are not part of our public surface.
 */
interface HttpRequestLike {
  headers: { origin?: string }
  body: unknown
  auth?: unknown
  socket?: { remoteAddress?: string }
}
interface HttpResponseLike {
  headersSent: boolean
  on(event: 'close', listener: () => void): void
  status(code: number): { json(body: unknown): void }
}
interface ExpressAppLike {
  use(handler: (request: HttpRequestLike, response: HttpResponseLike, next: () => void) => void): void
  use(path: string, handler: unknown): void
  post(path: string, handler: (req: HttpRequestLike, res: HttpResponseLike) => Promise<void>): void
  get(path: string, handler: unknown): void
  delete(path: string, handler: unknown): void
  listen(port: number, host: string, callback: () => void): HttpServer
}

type RequestHandlerLike = (request: HttpRequestLike, response: HttpResponseLike, next: () => void) => void

export class McpHttpServer {
  readonly catalog: Catalog
  readonly host: ToolHost
  readonly workspace: WorkspaceManager
  private readonly options: McpServerOptions
  private readonly clock: Clock
  private readonly accessMode: () => AccessMode
  private readonly loopbackClient: (remoteAddress: string | undefined) => boolean
  private readonly limiter: RateLimiter
  private readonly idempotency: IdempotencyStore
  private readonly app: ExpressAppLike
  private http: HttpServer | undefined
  private startedAt: string | null = null
  private lastError: string | null = null
  /** Latest resolved AI availability, for the synchronous IPC status. */
  private lastAiAvailable = false

  constructor(options: McpServerOptions) {
    this.options = options
    this.workspace = options.workspace
    this.clock = options.clock ?? new SystemClock()
    this.accessMode = options.accessMode ?? (() => 'read-write')
    this.loopbackClient = options.isLoopbackClient ?? isLoopbackAddress
    this.limiter = options.limiter ?? new RateLimiter({ clock: this.clock })
    this.idempotency =
      options.idempotency ?? new IdempotencyStore(options.workspace.global.db, { clock: this.clock })

    const core = createCoreTools({
      status: () => this.statusValues(),
      confirmations: options.confirmations,
      jobs: options.jobs
    })
    const misc = createMiscTools({
      workspace: options.workspace,
      events: options.events,
      confirm: options.confirm,
      confirmations: options.confirmations,
      clock: this.clock
    })
    this.catalog = buildCatalog(
      [
        { module: 'core', tools: core },
        { module: 'misc', tools: misc },
        ...(options.domainTools ?? [])
      ],
      { enforceCount: options.enforceToolCount === true }
    )

    this.host = new ToolHost({
      tools: this.catalog.tools,
      accessMode: this.accessMode,
      confirmations: options.confirmations,
      jobs: options.jobs,
      idempotency: this.idempotency,
      limiter: this.limiter,
      workspace: options.workspace,
      events: options.events,
      confirm: options.confirm,
      clock: this.clock,
      ...(options.servicesFor === undefined ? {} : { servicesFor: options.servicesFor })
    })

    const host = options.host ?? MCP_HOST
    if (host !== 'localhost' && isIP(host) === 0) {
      throw appError(AppErrorCode.InvalidArgument, 'MCP 监听地址必须是有效的 IP 或 localhost', {
        details: { host }
      })
    }
    const port = options.port ?? MCP_PORT
    if (!Number.isInteger(port) || port < 0 || port > 65535) {
      throw appError(AppErrorCode.InvalidArgument, 'MCP 端口必须在 0–65535 之间', {
        details: { port }
      })
    }
    this.app = createMcpExpressApp({ host }) as unknown as ExpressAppLike
    this.installRoutes()
  }

  get express(): ExpressAppLike {
    return this.app
  }

  get running(): boolean {
    return this.http !== undefined
  }

  get port(): number {
    const address = this.http?.address()
    return address === null || address === undefined || typeof address === 'string'
      ? (this.options.port ?? MCP_PORT)
      : (address as AddressInfo).port
  }

  /** Tool names visible in the current mode, for Settings → MCP. */
  visibleTools(): readonly ToolDefinition[] {
    return toolsForMode(this.catalog.tools, this.accessMode())
  }

  async statusValues(): Promise<ServerStatusValues> {
    const available = await this.options.aiAvailable?.()
    this.lastAiAvailable = available ?? false
    return {
      app_version: this.options.appVersion,
      mcp_versions: [...SUPPORTED_PROTOCOL_VERSIONS],
      access_mode: this.accessMode(),
      workspace_root: this.workspace.root,
      modules: this.options.modules?.() ?? ['core', 'misc'],
      ai_available: this.lastAiAvailable,
      zotero_available: this.options.zoteroAvailable?.() ?? false,
      transports: ['http'],
      tool_count: this.visibleTools().length
    }
  }

  /** Server status for IPC (no secrets). */
  status(): McpStatus {
    return {
      enabled: true,
      httpEnabled: this.running,
      running: this.running,
      host: this.options.host ?? MCP_HOST,
      port: this.port,
      path: this.options.path ?? MCP_PATH,
      accessMode: this.accessMode() as McpAccessMode,
      aiAvailable: this.lastAiAvailable,
      toolsCount: this.visibleTools().length,
      clientCount: this.options.clients.count(),
      pendingConfirmations: this.options.confirmations.pendingCount(),
      startedAt: this.startedAt,
      lastError: this.lastError
    }
  }

  /**
   * Builds a per-request SDK server with exactly the current mode's tools.
   * Conformance tests connect it to an in-memory transport.
   */
  createSdkServer(principal: Principal): McpServer {
    const server = new McpServer({ name: 'researchos', version: this.options.appVersion })
    for (const definition of toolsForMode(this.catalog.tools, this.accessMode())) {
      server.registerTool(
        definition.name,
        {
          title: definition.title,
          description: definition.description,
          inputSchema: z.strictObject(definition.inputSchema),
          ...(definition.outputSchema === undefined
            ? {}
            : { outputSchema: z.strictObject(definition.outputSchema) }),
          annotations: definition.annotations
        },
        async (args, extra) => {
          const result = await this.host.call(principal, {
            name: definition.name,
            args: args as Record<string, unknown>,
            ...(extra._meta === undefined ? {} : { meta: extra._meta }),
            signal: extra.signal
          })
          return toPayload(result)
        }
      )
    }
    return server
  }

  async start(): Promise<void> {
    if (this.http !== undefined) return
    const host = this.options.host ?? MCP_HOST
    const port = this.options.port ?? MCP_PORT
    const server = await new Promise<HttpServer>((resolve, reject) => {
      const listening = this.app.listen(port, host, () => resolve(listening))
      listening.once('error', reject)
    })
    this.http = server
    this.startedAt = formatTimestamp(this.clock.now())
    this.lastError = null
  }

  async stop(): Promise<void> {
    const server = this.http
    if (server === undefined) return
    this.http = undefined
    await new Promise<void>((resolve, reject) => {
      server.close((error) => (error === undefined ? resolve() : reject(error)))
    })
    this.startedAt = null
  }

  private installRoutes(): void {
    const path = this.options.path ?? MCP_PATH
    const verifier = {
      verifyAccessToken: async (token: string) => {
        const principal = this.options.clients.authenticate(token)
        if (principal === null) throw new InvalidTokenError('Invalid token')
        return {
          token,
          clientId: principal.clientId,
          scopes: ['mcp'],
          // Registry tokens have no expiry; the SDK middleware still requires
          // a numeric value, so report the far future.
          expiresAt: 253_402_300_799,
          extra: { principal: principal as unknown as Record<string, unknown> }
        }
      }
    }

    this.app.use((request, response, next) => {
      const origin = request.headers.origin
      if (origin !== undefined && !isLoopbackOrigin(origin)) {
        response.status(403).json({ error: 'origin_not_allowed' })
        return
      }
      next()
    })
    const bearerAuth: RequestHandlerLike = requireBearerAuth({ verifier }) as unknown as RequestHandlerLike
    const loopbackGate: RequestHandlerLike = (request, response, next) => {
      // Trust boundary: a request that arrived over a loopback socket comes
      // from this machine, so any (or no) key is accepted as the local
      // principal. Remote sockets still need a registered Bearer token.
      if (this.loopbackClient(request.socket?.remoteAddress)) {
        request.auth = LOOPBACK_AUTH
        next()
        return
      }
      bearerAuth(request, response, next)
    }
    this.app.use(path, loopbackGate)

    this.app.post(path, async (request, response) => {
      const principal = principalOf(request.auth)
      if (principal === null) {
        response.status(401).json({ error: 'unauthorized' })
        return
      }
      const body = request.body as unknown
      prepareArguments(body, this.catalog)
      const server = this.createSdkServer(principal)
      const transport = new StreamableHTTPServerTransport({
        sessionIdGenerator: undefined,
        enableJsonResponse: true
      })
      response.on('close', () => {
        void transport.close()
        void server.close()
      })
      try {
        await server.connect(transport)
        await transport.handleRequest(request as never, response as never, body)
      } catch (error) {
        this.lastError = error instanceof Error ? error.message : String(error)
        if (!response.headersSent) {
          response.status(500).json({
            jsonrpc: '2.0',
            error: { code: -32603, message: 'Internal error' },
            id: null
          })
        }
      }
    })

    const methodNotAllowed = (_request: HttpRequestLike, response: HttpResponseLike) => {
      response.status(405).json({
        jsonrpc: '2.0',
        error: {
          code: -32000,
          message: 'Method not allowed: stateless Streamable HTTP accepts POST only'
        },
        id: null
      })
    }
    this.app.get(path, methodNotAllowed)
    this.app.delete(path, methodNotAllowed)
  }
}

/** Far-future epoch: registry tokens and the local principal never expire. */
const FAR_FUTURE_EPOCH = 253_402_300_799

/**
 * Auth payload installed for loopback-origin requests: a synthetic local
 * principal, mirroring the shape `requireBearerAuth` would have produced.
 */
const LOOPBACK_AUTH = {
  clientId: 'local',
  scopes: ['mcp'],
  expiresAt: FAR_FUTURE_EPOCH,
  extra: {
    principal: { clientId: 'local', clientName: 'local', transport: 'http' } satisfies Principal
  }
}

/** True when a socket address is the local machine (127/8, ::1, v4-mapped). */
export function isLoopbackAddress(address: string | undefined): boolean {
  if (address === undefined) return false
  const mapped = address.startsWith('::ffff:') ? address.slice('::ffff:'.length) : address
  if (mapped.startsWith('127.')) return true
  return mapped === '::1' || mapped.toLowerCase() === 'localhost'
}

function principalOf(auth: unknown): Principal | null {
  if (auth === null || typeof auth !== 'object') return null
  const extra = (auth as { extra?: Record<string, unknown> }).extra
  const principal = extra?.['principal']
  if (principal === null || principal === undefined || typeof principal !== 'object') return null
  const value = principal as Partial<Principal>
  if (typeof value.clientId !== 'string' || typeof value.clientName !== 'string') return null
  return {
    clientId: value.clientId,
    clientName: value.clientName,
    transport: value.transport === 'stdio' ? 'stdio' : 'http'
  }
}

function isLoopbackOrigin(origin: string): boolean {
  try {
    const url = new URL(origin)
    return url.hostname === '127.0.0.1' || url.hostname === 'localhost' || url.hostname === '::1'
  } catch {
    return false
  }
}