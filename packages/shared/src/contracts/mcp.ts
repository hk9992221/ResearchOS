import type {
  McpAccessMode,
  McpConnectionReport,
  McpClient,
  McpConfirmation,
  McpConfirmationState,
  McpJob,
  McpStatus,
  McpToolInfo
} from '../types/mcp'
import type { JobState } from '../types/common'

export interface McpClientCreateInput {
  name: string
  enabled?: boolean
}

export interface ConfirmationDecision {
  id: string
  note?: string
}

export interface McpSettingsUpdateInput {
  enabled?: boolean
  httpEnabled?: boolean
  host?: string
  port?: number
  accessMode?: McpAccessMode
}

export interface McpConfirmationFilter {
  state?: McpConfirmationState
  projectId?: string
  limit?: number
}

/**
 * IPC commands (desktop only). `mcp.confirmations.approve|reject` are the
 * desktop approval path; MCP itself has list/get/cancel only.
 */
export interface McpCommands {
  'mcp.status': { req: void; res: McpStatus }
  'mcp.probe': { req: void; res: McpConnectionReport }
  'mcp.retention.get': { req: void; res: { days: number } }
  'mcp.retention.set': { req: { days: number }; res: { days: number } }
  'mcp.start': { req: void; res: McpStatus }
  'mcp.stop': { req: void; res: McpStatus }
  'mcp.settings.update': { req: McpSettingsUpdateInput; res: McpStatus }

  'mcp.clients.list': { req: void; res: McpClient[] }
  'mcp.clients.create': { req: McpClientCreateInput; res: { client: McpClient; token: string } }
  'mcp.clients.update': { req: { clientId: string; name?: string; enabled?: boolean }; res: McpClient }
  'mcp.clients.delete': { req: { clientId: string }; res: { deleted: boolean } }

  'mcp.confirmations.list': { req: McpConfirmationFilter; res: McpConfirmation[] }
  'mcp.confirmations.get': { req: { id: string }; res: McpConfirmation }
  'mcp.confirmations.approve': { req: ConfirmationDecision; res: McpConfirmation }
  'mcp.confirmations.reject': { req: ConfirmationDecision; res: McpConfirmation }
  'mcp.confirmations.cancel': { req: { id: string }; res: McpConfirmation }

  'mcp.jobs.list': { req: { state?: JobState; projectId?: string }; res: McpJob[] }
  'mcp.jobs.get': { req: { id: string }; res: McpJob }
  'mcp.jobs.cancel': { req: { id: string }; res: McpJob }

  'mcp.tools.list': { req: void; res: McpToolInfo[] }
}