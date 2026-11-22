import type { EntityId, IsoDateTime } from '../types/common'
import type {
  AiCapability,
  AiChatCallRequest,
  AiChatCallResult,
  AiChatMessage,
  AiEmbeddingResult,
  AiOutput,
  AiOutputStatus,
  AiProbeResult,
  AiProtocol,
  AiRole,
  AiStructuredCallResult,
  AiStructuredOptions,
  AiAvailability,
  AssignResult,
  CreateProviderInput,
  ModelInfo,
  ModelSyncResult,
  ProviderTestResult,
  ProviderView,
  RoleAssignment,
  RoleView,
  SecretStatus,
  UpdateProviderInput
} from '../types/ai'

export interface AiOutputQuery {
  projectId: EntityId
  entityType?: string
  entityId?: EntityId
  taskKind?: string
  status?: AiOutputStatus
  limit?: number
  offset?: number
}

/** IPC-safe chat call: callbacks and signals never cross the bridge. */
export interface AiChatCommandRequest {
  role?: AiRole
  messages: AiChatMessage[]
  temperature?: number
  maxOutputTokens?: number
}

/**
 * AI command surface. Every write names its project explicitly: `ai_outputs`
 * rows live in `projects/<id>/project.db`.
 */
export interface AiCommands {
  'ai.providers.list': { req: void; res: ProviderView[] }
  'ai.providers.create': { req: { provider: CreateProviderInput }; res: ProviderView }
  'ai.providers.update': { req: { providerId: string; patch: UpdateProviderInput }; res: ProviderView }
  'ai.providers.delete': { req: { providerId: string }; res: { deleted: boolean } }
  'ai.providers.test': { req: { providerId: string }; res: ProviderTestResult }

  'ai.models.list': { req: { providerId: string; capability?: AiCapability; includeUnavailable?: boolean }; res: ModelInfo[] }
  'ai.models.refresh': { req: { providerId: string }; res: ModelSyncResult }
  'ai.models.addManual': { req: { providerId: string; modelId: string; displayName?: string }; res: void }
  'ai.models.remove': { req: { providerId: string; modelId: string }; res: void }

  'ai.roles.list': { req: void; res: RoleView[] }
  'ai.roles.set': { req: { role: AiRole; providerId: string; modelId: string }; res: AssignResult }
  'ai.roles.clear': { req: { role: AiRole }; res: void }

  'ai.secrets.set': { req: { providerId: string; apiKey: string }; res: SecretStatus }
  'ai.secrets.delete': { req: { providerId: string }; res: SecretStatus }
  'ai.secrets.status': { req: { providerId: string }; res: SecretStatus }

  'ai.chat': { req: AiChatCommandRequest; res: AiChatCallResult }
  'ai.probe': { req: { role?: AiRole }; res: AiProbeResult }
  'ai.availability': { req: { refresh?: boolean } | void; res: AiAvailability }

  'ai.outputs.list': { req: AiOutputQuery; res: { items: AiOutput[]; total: number } }
  'ai.outputs.get': { req: { projectId: EntityId; outputId: string }; res: AiOutput }
  'ai.outputs.confirm': { req: { projectId: EntityId; outputId: string; contentMd?: string }; res: AiOutput }
  'ai.outputs.update': { req: { projectId: EntityId; outputId: string; contentMd: string }; res: AiOutput }
  'ai.outputs.discard': { req: { projectId: EntityId; outputId: string }; res: { discarded: boolean; at: IsoDateTime } }
}

export type {
  AiCapability,
  AiChatCallRequest,
  AiChatCallResult,
  AiChatMessage,
  AiEmbeddingResult,
  AiOutput,
  AiProtocol,
  AiRole,
  AiStructuredCallResult,
  AiStructuredOptions,
  AiAvailability,
  AssignResult,
  CreateProviderInput,
  ModelInfo,
  ModelSyncResult,
  ProviderTestResult,
  ProviderView,
  RoleAssignment,
  RoleView,
  SecretStatus,
  UpdateProviderInput
}