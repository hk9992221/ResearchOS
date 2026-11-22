import type { IsoDateTime, JobState, JsonObject } from './common'
import type { AppErrorShape } from '../errors'

/** `mcp.access_mode`; the host generates `tools/list` from this value. */
export type McpAccessMode = 'read-only' | 'read-write'

export type McpTransport = 'http' | 'stdio'

export interface McpStatus {
  enabled: boolean
  httpEnabled: boolean
  running: boolean
  host: string
  port: number
  path: string
  accessMode: McpAccessMode
  aiAvailable: boolean
  toolsCount: number
  clientCount: number
  pendingConfirmations: number
  startedAt: IsoDateTime | null
  lastError: string | null
  /** Non-internal IPv4 addresses of this machine, for the LAN access view. */
  lanAddresses?: string[]
}

/** The `mcp_confirmation.state` machine of the MCP spec */
export type McpConfirmationState =
  | 'requested'
  | 'approved'
  | 'executing'
  | 'executed'
  | 'failed'
  | 'denied'
  | 'expired'
  | 'cancelled'

export interface McpConfirmation {
  id: string
  tool: string
  title: string
  summary: string
  /** Desktop approval note; empty when the operator left it blank. */
  note: string
  impact: JsonObject | null
  projectId: string | null
  clientId: string
  state: McpConfirmationState
  createdAt: IsoDateTime
  expiresAt: IsoDateTime
  resolvedAt: IsoDateTime | null
  resolvedBy: string | null
  result: JsonObject | null
  errorCode: string | null
  traceId: string | null
}

export interface McpJob {
  id: string
  tool: string
  projectId: string | null
  clientId: string
  state: JobState
  progress: number
  stage: string
  readOnly: boolean
  params: JsonObject | null
  result: JsonObject | null
  error: AppErrorShape | null
  createdAt: IsoDateTime
  startedAt: IsoDateTime | null
  finishedAt: IsoDateTime | null
  traceId: string | null
}

export interface McpClient {
  id: string
  name: string
  /** Legacy rows may still read `stdio`; new clients are always `http`. */
  transport: McpTransport
  createdAt: IsoDateTime
  lastSeenAt: IsoDateTime | null
  enabled: boolean
}

/** The four MCP tool classes, as used by the catalog and `tools/list`. */
export type McpToolCategory = 'R' | 'W' | 'C' | 'J'

export interface McpConnectionReport {
  ipcOk: boolean
  ipcError: string
  httpEnabled: boolean
  httpOk: boolean
  httpError: string
  toolCount: number
  serverName: string
  version: string
}

export interface McpToolInfo {
  name: string
  title: string
  category: McpToolCategory
  description: string
  requiresConfirmation: boolean
  requiresIdempotencyKey: boolean
}