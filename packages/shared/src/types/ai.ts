import type { EntityId, IsoDateTime, JsonObject } from './common'

export type AiProtocol = 'openai_chat' | 'openai_responses' | 'gemini' | 'anthropic'
export type AiRole = 'chat_main' | 'embedding' | 'rerank'
export type AiCapability = 'chat' | 'embedding' | 'rerank'

/**
 * Stable gateway failure classification. `truncated` and
 * `output_invalid` are the structured-generation classes that used to be
 * swallowed by the Go job layer.
 */
export type AiErrorKind =
  | 'auth'
  | 'rate_limited'
  | 'timeout'
  | 'network'
  | 'invalid_request'
  | 'model_not_found'
  | 'unsupported_capability'
  | 'context_too_long'
  | 'server_error'
  | 'canceled'
  | 'role_unassigned'
  | 'provider_disabled'
  | 'secret_unavailable'
  | 'model_unavailable'
  | 'persist_failed'
  | 'truncated'
  | 'output_invalid'

/** Token accounting as reported upstream; zero when the provider omits it. */
export interface AiUsage {
  inputTokens: number
  outputTokens: number
}

/* ------------------------------------------------------------- providers */

export interface ProviderView {
  id: string
  name: string
  protocol: AiProtocol
  baseUrl: string
  hasKey: boolean
  enabled: boolean
  lastTestOk: boolean | null
  lastTestedAt: IsoDateTime | null
  lastTestLatencyMs: number | null
  lastTestModelCount: number | null
  lastTestError: string | null
  modelsFetchedAt: IsoDateTime | null
  modelsError: string | null
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
}

export interface CreateProviderInput {
  name: string
  protocol: AiProtocol
  baseUrl: string
  /** Written to the OS key store; never persisted in SQLite. */
  apiKey?: string
  enabled?: boolean
}

export interface UpdateProviderInput {
  name?: string
  protocol?: AiProtocol
  baseUrl?: string
  /** Replace the stored key. */
  apiKey?: string
  /** Delete the stored key; mutually exclusive with `apiKey`. */
  clearKey?: boolean
  enabled?: boolean
}

export interface ProviderTestResult {
  ok: boolean
  latencyMs: number
  modelCount: number
  errorKind: AiErrorKind | null
  error: string | null
  testedAt: IsoDateTime
}

export interface ModelInfo {
  providerId: string
  modelId: string
  displayName: string
  capabilities: AiCapability[]
  source: 'upstream' | 'manual'
  available: boolean
  lastSeenAt: IsoDateTime | null
}

export interface ModelSyncResult {
  providerId: string
  added: number
  markedUnavailable: number
  total: number
  fetchedAt: IsoDateTime
  warning: string | null
}

/* ----------------------------------------------------------------- roles */

export interface RoleAssignment {
  role: AiRole
  providerId: string
  modelId: string
  updatedAt: IsoDateTime
}

export type RoleProblem =
  | 'role_unassigned'
  | 'provider_disabled'
  | 'model_unavailable'
  | 'capability_missing'

export interface RoleView {
  role: AiRole
  requiredCapability: AiCapability
  assignment: RoleAssignment | null
  providerName: string | null
  resolved: boolean
  problem: RoleProblem | null
}

export interface AssignResult {
  assignment: RoleAssignment
  /** The semantic index must be rebuilt (embedding assignment changed). */
  reindexRequired: boolean
}

/* -------------------------------------------------------------- gateway */

export interface AiProfileSummary {
  providerId: string
  providerName: string
  protocol: AiProtocol
  modelId: string
}

export interface AiEmbeddingProfile extends AiProfileSummary {
  /** Opaque, stable vector-space key derived from provider + model. */
  profileId: string
  /** Last observed vector length; null until a vector was produced. */
  dim: number | null
}

export interface AiChatMessage {
  role: 'system' | 'user' | 'assistant'
  content: string
}

/** Protocol-neutral chat call. `onDelta`/`signal` stay in-process. */
export interface AiChatCallRequest {
  messages: AiChatMessage[]
  temperature?: number
  /** Defaults to 8192; no hard 4000 ceiling. */
  maxOutputTokens?: number
  /** Receives assistant-visible text as it arrives (SSE when supported). */
  onDelta?: (delta: string) => void
  signal?: AbortSignal
}

export interface AiChatCallResult extends AiProfileSummary {
  content: string
  finishReason: string | null
  usage: AiUsage
  attempts: number
  streamed: boolean
}

export interface AiStructuredOptions {
  /** Schema name sent to native structured-output APIs; default "response". */
  name?: string
  /** Optional strict module validator; Ajv validates the JSON Schema regardless. */
  validate?: (value: unknown) => void
  /** Repair rounds after the first invalid reply; default 1, 0 disables. */
  repairAttempts?: number
}

export interface AiStructuredCallResult extends AiProfileSummary {
  /** Parsed and schema-validated JSON. */
  value: unknown
  /** Validated JSON text as returned by the model (what a draft should store). */
  json: string
  /** Raw model reply. */
  content: string
  finishReason: string | null
  usage: AiUsage
  attempts: number
  repaired: boolean
}

export interface AiEmbeddingResult {
  profileId: string
  providerId: string
  modelId: string
  dim: number
  /** Aligned with the input order. */
  vectors: number[][]
  usage: AiUsage
}

export interface AiProbeResult {
  role: AiRole
  ok: boolean
  checkedAt: IsoDateTime
  latencyMs: number
  providerId: string | null
  providerName: string | null
  protocol: AiProtocol | null
  modelId: string | null
  errorKind: AiErrorKind | null
  message: string | null
}

export interface AiAvailability {
  /** Chat generation is usable: role + enabled provider + key/keyless + probe. */
  available: boolean
  chat: boolean
  embedding: boolean
  rerank: boolean
  /** Present when rerank degrades: no protocol implements it. */
  rerankReason: 'rerank_unavailable' | null
  missingRoles: AiRole[]
  reason: string | null
  chatProfile: AiProfileSummary | null
  embeddingProfile: AiEmbeddingProfile | null
  probe: { ok: boolean; checkedAt: IsoDateTime } | null
}

/* -------------------------------------------------------------- secrets */

export interface SecretStatus {
  ref: string
  available: boolean
  store: 'keyring' | 'unavailable'
}

/* -------------------------------------------------------------- outputs */

export type AiOutputStatus = 'draft' | 'confirmed'

export interface AiOutputSourceRef {
  entityType: string
  entityId: EntityId
  locator?: string
  title?: string
}

export interface AiOutput {
  id: EntityId
  projectId: EntityId | null
  taskKind: string
  entityType: string | null
  entityId: EntityId | null
  status: AiOutputStatus
  contentMd: string
  providerId: string | null
  providerName: string | null
  protocol: string | null
  modelId: string | null
  promptVersion: string | null
  sourceRefs: AiOutputSourceRef[] | null
  parameters: JsonObject | null
  usage: JsonObject | null
  humanEdited: boolean
  createdAt: IsoDateTime
  confirmedAt: IsoDateTime | null
}
/* ------------------------------------------------------------- activity */

/**
 * Main-process AI call heartbeat. `started` opens a call, `tick` arrives once
 * a second while it runs, and `finished`/`failed` close it. A gap of ticks is
 * the signal that the call stopped progressing (renderer still alive), while a
 * growing elapsed time is the signal that the model is slow.
 */
export type AiActivityPhase = 'started' | 'tick' | 'finished' | 'failed'

export type AiActivityKind = 'chat' | 'structured' | 'embedding'

export interface AiActivity {
  callId: string
  phase: AiActivityPhase
  kind: AiActivityKind
  /** Role name, e.g. `chat_main` or `chat_fast`. */
  role: string
  model: string | null
  startedAt: IsoDateTime
  elapsedMs: number
  error: string | null
}
