/**
 * AI module public port. Other modules import only these names:
 *
 *   const ai = createAiModule({ globalDb })
 *   await ai.gateway.chat('chat_main', { messages })
 *   await ai.gateway.chatStructured('chat_main', { messages }, { jsonSchema })
 *   await ai.gateway.embed(profile, texts)
 *   await ai.gateway.probe('chat_main')
 *   await ai.gateway.availability()
 *
 * Providers/models/roles/secrets are Settings-UI services; ai_outputs are
 * project-scoped and constructed with `createAiOutputService(projectDb, …)`.
 */
import type { AiActivity } from '@researchos/shared'

import type { Clock } from '../../clock'
import type { Db } from '../../db/open'
import type { Logger } from '../../logger'
import type { SettingsStore } from '../../settings'
import { ProtocolFactory } from './protocol/factory'
import type { FetchLike } from './protocol/types'
import { Gateway, type AiGateway, type AiGatewayOptions } from './gateway'
import { SqliteModelService, type ModelService } from './model-service'
import {
  SqliteAiOutputService,
  type AiOutputService,
  type AiOutputServiceOptions
} from './output-service'
import {
  SqliteProviderService,
  type ModelLister,
  type ProviderService
} from './provider-service'
import { SqliteRoleService, type RoleService } from './role-service'
import {
  KeyringSecretStore,
  SecretServiceImpl,
  type SecretService,
  type SecretStore
} from './secrets'

export type { AiGateway } from './gateway'
export {
  CHAT_TIMEOUT_MS,
  DEFAULT_ATTEMPTS,
  DEFAULT_MAX_OUTPUT_TOKENS,
  EMBED_TIMEOUT_MS,
  embeddingProfileId
} from './gateway'
export type {
  AiActivity,
  AiAvailability,
  AiCapability,
  AiChatCallRequest,
  AiChatCallResult,
  AiChatMessage,
  AiEmbeddingProfile,
  AiEmbeddingResult,
  AiErrorKind,
  AiOutput,
  AiProbeResult,
  AiProtocol,
  AiRole,
  AiStructuredCallResult,
  AiStructuredOptions,
  AiUsage,
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
} from '@researchos/shared'
export { AiError, isAiError } from './errors'
export {
  AI_PROTOCOLS,
  DEFAULT_BASE_URLS,
  isLoopbackUrl,
  normalizeBaseUrl,
  protocolCapabilities,
  protocolSupports
} from './capabilities'
export { AI_ROLES, isAiRole, roleCapability, type ResolvedRole, type RoleService } from './role-service'
export type { ModelService } from './model-service'
export type { ProviderService } from './provider-service'
export type { SecretService, SecretStore } from './secrets'
export { MemorySecretStore, providerKeyRef } from './secrets'
export {
  isValidTaskKind,
  registerTaskKinds,
  type AiOutputEntityRef,
  type AiOutputProvenance,
  type AiOutputQuery,
  type AiOutputService,
  type AnalysisEvent,
  type CreateDraftInput,
  type OutputActor
} from './output-service'
export type { ProtocolAdapter } from './protocol/types'

export interface AiModuleDependencies {
  globalDb: Db
  clock?: Clock
  secrets?: SecretStore
  fetchImpl?: FetchLike
  logger?: Logger
  settings?: SettingsStore
  /** Defaults to the protocol factory (upstream `/models` listings). */
  lister?: ModelLister
  /** Renderer heartbeat for every chat/structured/embedding call. */
  onActivity?: (activity: AiActivity) => void
  gateway?: Partial<
    Pick<
      AiGatewayOptions,
      'attempts' | 'backoffMs' | 'jitter' | 'chatTimeoutMs' | 'embedTimeoutMs' | 'probeTimeoutMs' | 'probeTtlMs' | 'sleep' | 'random'
    >
  >
}

export interface AiModule {
  providers: ProviderService
  models: ModelService
  roles: RoleService
  secrets: SecretService
  gateway: AiGateway
  /** The underlying store, for tests and integration wiring only. */
  secretStore: SecretStore
}

export function createAiModule(dependencies: AiModuleDependencies): AiModule {
  const clock = dependencies.clock
  const secretStore = dependencies.secrets ?? new KeyringSecretStore()
  const factory = new ProtocolFactory(dependencies.fetchImpl)
  const lister = dependencies.lister ?? factory
  const roles = new SqliteRoleService(dependencies.globalDb, clock)
  const providers = new SqliteProviderService(dependencies.globalDb, {
    ...(clock === undefined ? {} : { clock }),
    secrets: secretStore,
    lister
  })
  const models = new SqliteModelService(dependencies.globalDb, {
    ...(clock === undefined ? {} : { clock }),
    secrets: secretStore,
    lister
  })
  const gateway = new Gateway({
    roles,
    secrets: secretStore,
    factory,
    ...(clock === undefined ? {} : { clock }),
    ...(dependencies.settings === undefined ? {} : { settings: dependencies.settings }),
    ...(dependencies.logger === undefined ? {} : { logger: dependencies.logger }),
    ...(dependencies.onActivity === undefined ? {} : { onActivity: dependencies.onActivity }),
    ...dependencies.gateway
  })
  const secrets = new SecretServiceImpl(secretStore)
  return { providers, models, roles, secrets, gateway, secretStore }
}

export function createAiOutputService(
  projectDb: Db,
  options: AiOutputServiceOptions = {}
): AiOutputService {
  return new SqliteAiOutputService(projectDb, options)
}