import { createHash, randomUUID } from 'node:crypto'
import { setTimeout as delay } from 'node:timers/promises'

import type {
  AiActivity,
  AiActivityKind,
  AiAvailability,
  AiChatCallRequest,
  AiChatCallResult,
  AiEmbeddingProfile,
  AiEmbeddingResult,
  AiErrorKind,
  AiProfileSummary,
  AiProbeResult,
  AiRole,
  AiStructuredCallResult,
  AiStructuredOptions,
  AiUsage,
  JsonObject
} from '@researchos/shared'

import { formatTimestamp, SystemClock, type Clock } from '../../clock'
import type { Logger } from '../../logger'
import { isLoopbackUrl } from './capabilities'
import { AiError, asAiError, isAiError, sanitize } from './errors'
import type { AdapterFactory } from './protocol/factory'
import type {
  ProtocolAdapter,
  ProtocolChatRequest,
  ProtocolChatResponse,
  ProviderConfig,
  ResponseFormat
} from './protocol/types'
import { roleCapability, type ResolvedRole, type RoleService } from './role-service'
import type { SecretStore } from './secrets'
import { SettingsStore } from '../../settings'
import {
  extractJsonObject,
  isTruncatedFinish,
  jsonConstraintPrompt,
  repairPrompt,
  validateJsonSchema
} from './structured'

export const CHAT_TIMEOUT_MS = 120_000
export const EMBED_TIMEOUT_MS = 60_000
export const DEFAULT_MAX_OUTPUT_TOKENS = 8192
export const DEFAULT_ATTEMPTS = 3

export interface AiGateway {
  /** Resolves `chat_main` (or any chat-capable role) and returns the reply. */
  chat(role: AiRole, request: AiChatCallRequest): Promise<AiChatCallResult>
  /**
   * Chat with JSON-Schema output. Native structured output where the protocol
   * supports it, prompt + Ajv otherwise; one repair round by default.
   */
  chatStructured(
    role: AiRole,
    request: AiChatCallRequest,
    jsonSchema: JsonObject,
    options?: AiStructuredOptions
  ): Promise<AiStructuredCallResult>
  /** Embedding for one explicit profile; never mixes vector spaces. */
  embed(
    profile: AiEmbeddingProfile | string,
    texts: string[],
    options?: { signal?: AbortSignal }
  ): Promise<AiEmbeddingResult>
  /** Real chat probe (never `/models`) that drives availability. */
  probe(role: AiRole): Promise<AiProbeResult>
  availability(): Promise<AiAvailability>
}

export interface AiGatewayOptions {
  roles: RoleService
  secrets: SecretStore
  factory: AdapterFactory
  clock?: Clock
  settings?: SettingsStore
  logger?: Logger
  attempts?: number
  backoffMs?: number[]
  jitter?: number
  chatTimeoutMs?: number
  embedTimeoutMs?: number
  probeTimeoutMs?: number
  probeTtlMs?: number
  /** Heartbeat for the renderer's AI activity indicator. */
  onActivity?: (activity: AiActivity) => void
  sleep?: (milliseconds: number, signal?: AbortSignal) => Promise<void>
  random?: () => number
}

interface CallOutcome {
  response: ProtocolChatResponse
  attempts: number
}

interface ProbeCacheEntry {
  result: AiProbeResult
  atMs: number
}

const RETRYABLE: Readonly<Record<string, true>> = {
  network: true,
  timeout: true,
  rate_limited: true,
  server_error: true
}

export class Gateway implements AiGateway {
  private readonly roles: RoleService
  private readonly secrets: SecretStore
  private readonly factory: AdapterFactory
  private readonly clock: Clock
  private readonly settings: SettingsStore | undefined
  private readonly logger: Logger | undefined
  private readonly attempts: number
  private readonly backoffMs: number[]
  private readonly jitter: number
  private readonly chatTimeoutMs: number
  private readonly embedTimeoutMs: number
  private readonly probeTimeoutMs: number
  private readonly probeTtlMs: number
  private readonly onActivity: ((activity: AiActivity) => void) | undefined
  private readonly sleep: (milliseconds: number, signal?: AbortSignal) => Promise<void>
  private readonly random: () => number
  private readonly probeCache = new Map<AiRole, ProbeCacheEntry>()
  private readonly probeInflight = new Map<AiRole, Promise<AiProbeResult>>()

  constructor(options: AiGatewayOptions) {
    this.roles = options.roles
    this.secrets = options.secrets
    this.factory = options.factory
    this.clock = options.clock ?? new SystemClock()
    this.settings = options.settings
    this.logger = options.logger
    this.attempts = options.attempts ?? DEFAULT_ATTEMPTS
    this.backoffMs = options.backoffMs ?? [1000, 4000]
    this.jitter = options.jitter ?? 0.2
    this.chatTimeoutMs = options.chatTimeoutMs ?? CHAT_TIMEOUT_MS
    this.embedTimeoutMs = options.embedTimeoutMs ?? EMBED_TIMEOUT_MS
    this.probeTimeoutMs = options.probeTimeoutMs ?? 20_000
    this.probeTtlMs = options.probeTtlMs ?? 300_000
    this.onActivity = options.onActivity
    this.sleep =
      options.sleep ??
      ((milliseconds, signal) =>
        delay(milliseconds, undefined, signal === undefined ? {} : { signal }))
    this.random = options.random ?? Math.random
  }

  async chat(role: AiRole, request: AiChatCallRequest): Promise<AiChatCallResult> {
    if (request.messages.length === 0) {
      throw new AiError('invalid_request', 'chat requires at least one message', {
        details: { field: 'messages' }
      })
    }
    const resolved = this.resolveCapability(role, 'chat')
    const key = await this.keyFor(resolved.provider)
    const adapter = this.factory.create(resolved.provider, key)
    const maxOutputTokens = normalizeBudget(request.maxOutputTokens)
    const outcome = await this.callChat(
      adapter,
      resolved,
      {
        model: resolved.model.modelId,
        messages: request.messages,
        ...(request.temperature === undefined ? {} : { temperature: request.temperature }),
        maxOutputTokens,
        ...(request.onDelta === undefined ? {} : { onDelta: request.onDelta })
      },
      request.signal,
      'chat'
    )
    return {
      ...summaryOf(resolved),
      content: outcome.response.content,
      finishReason: outcome.response.finishReason,
      usage: outcome.response.usage,
      attempts: outcome.attempts,
      streamed: outcome.response.streamed
    }
  }

  async chatStructured(
    role: AiRole,
    request: AiChatCallRequest,
    jsonSchema: JsonObject,
    options: AiStructuredOptions = {}
  ): Promise<AiStructuredCallResult> {
    if (request.messages.length === 0) {
      throw new AiError('invalid_request', 'chat requires at least one message', {
        details: { field: 'messages' }
      })
    }
    if (jsonSchema === undefined || typeof jsonSchema !== 'object' || jsonSchema === null) {
      throw new AiError('invalid_request', 'structured chat requires a JSON schema', {
        details: { field: 'schema' }
      })
    }
    const resolved = this.resolveCapability(role, 'chat')
    const key = await this.keyFor(resolved.provider)
    const adapter = this.factory.create(resolved.provider, key)
    const native = resolved.provider.protocol !== 'anthropic'
    const maxOutputTokens = normalizeBudget(request.maxOutputTokens)
    const messages = [...request.messages]
    const schemaName = options.name === undefined || options.name === '' ? 'response' : options.name
    const format: ResponseFormat | undefined = native
      ? { type: 'json_schema', name: schemaName, jsonSchema }
      : undefined
    if (!native) {
      messages.unshift({ role: 'system', content: jsonConstraintPrompt(schemaName, jsonSchema) })
    }

    const repairs = Math.max(0, options.repairAttempts ?? 1)
    let usage: AiUsage = { inputTokens: 0, outputTokens: 0 }
    let attemptsTotal = 0
    let lastDetail = 'unknown validation failure'

    for (let round = 0; ; round += 1) {
      const callRequest: ProtocolChatRequest = {
        model: resolved.model.modelId,
        messages,
        ...(request.temperature === undefined ? {} : { temperature: request.temperature }),
        maxOutputTokens,
        ...(format === undefined ? {} : { responseFormat: format }),
        ...(request.onDelta === undefined ? {} : { onDelta: request.onDelta })
      }
      const outcome = await this.callChat(adapter, resolved, callRequest, request.signal, 'chat_structured')
      const response = outcome.response
      attemptsTotal += outcome.attempts
      usage = {
        inputTokens: usage.inputTokens + response.usage.inputTokens,
        outputTokens: usage.outputTokens + response.usage.outputTokens
      }

      if (isTruncatedFinish(response.finishReason)) {
        throw new AiError(
          'truncated',
          `model output was truncated at the token budget (finish_reason=${response.finishReason ?? 'length'})`,
          {
            ...this.contextOf(resolved),
            attempts: attemptsTotal,
            details: { finishReason: response.finishReason, maxOutputTokens, repaired: round > 0 }
          }
        )
      }

      let extracted: { value: unknown; json: string }
      try {
        extracted = extractJsonObject(response.content)
        validateJsonSchema(jsonSchema, extracted.value)
        options.validate?.(extracted.value)
      } catch (error) {
        lastDetail = isAiError(error) ? error.message : String(error)
        if (round >= repairs) {
          throw new AiError(
            'output_invalid',
            `AI output did not validate after ${round + 1} attempt(s): ${lastDetail}`,
            { ...this.contextOf(resolved), attempts: attemptsTotal, cause: error }
          )
        }
        messages.push({ role: 'assistant', content: response.content })
        messages.push({ role: 'user', content: repairPrompt(lastDetail) })
        continue
      }

      return {
        ...summaryOf(resolved),
        value: extracted.value,
        json: extracted.json,
        content: response.content,
        finishReason: response.finishReason,
        usage,
        attempts: attemptsTotal,
        repaired: round > 0
      }
    }
  }

  async embed(
    profile: AiEmbeddingProfile | string,
    texts: string[],
    options: { signal?: AbortSignal } = {}
  ): Promise<AiEmbeddingResult> {
    const resolved = this.resolveCapability('embedding', 'embedding')
    const currentProfile = embeddingProfileOf(resolved)
    if (typeof profile === 'object') {
      if (profile.providerId !== currentProfile.providerId || profile.modelId !== currentProfile.modelId) {
        throw new AiError(
          'model_unavailable',
          'embedding profile does not match the current embedding assignment',
          { ...this.contextOf(resolved), details: { expected: currentProfile.profileId } }
        )
      }
    } else if (profile !== 'embedding' && profile !== currentProfile.profileId) {
      throw new AiError('model_unavailable', 'embedding profile is stale; reindex is required', {
        ...this.contextOf(resolved),
        details: { expected: currentProfile.profileId, received: profile }
      })
    }
    if (texts.length === 0) {
      return {
        ...currentProfile,
        dim: currentProfile.dim ?? 0,
        vectors: [],
        usage: { inputTokens: 0, outputTokens: 0 }
      }
    }
    const key = await this.keyFor(resolved.provider)
    const adapter = this.factory.create(resolved.provider, key)
    if (adapter.embed === undefined) {
      throw new AiError(
        'unsupported_capability',
        `protocol ${resolved.provider.protocol} does not support embedding`,
        this.contextOf(resolved)
      )
    }
    const activity = this.startActivity('embedding', resolved.role, resolved.model.modelId)
    const start = this.clock.now().getTime()
    let attempts = 0
    let lastError: AiError | null = null
    let vectors: number[][] = []
    let usage: AiUsage = { inputTokens: 0, outputTokens: 0 }
    try {
      for (let attempt = 1; attempt <= this.attempts; attempt += 1) {
        attempts = attempt
        const scope = this.scope(this.embedTimeoutMs, options.signal)
        try {
          const response = await adapter.embed(
            { model: resolved.model.modelId, inputs: texts },
            scope.signal
          )
          vectors = response.vectors
          usage = response.usage
          lastError = null
          break
        } catch (cause) {
          const classified = this.classify(cause, resolved, scope)
          lastError = classified
          if (attempt === this.attempts || !this.isRetryable(classified)) break
          if (!(await this.waitBeforeRetry(attempt, classified, options.signal))) break
        } finally {
          scope.dispose()
        }
      }
      this.logCall('embed', resolved, attempts, start, lastError)
      if (lastError !== null) throw this.annotate(lastError, resolved, attempts)
      if (vectors.length !== texts.length || vectors.some((vector) => !Array.isArray(vector))) {
        throw new AiError('server_error', 'embeddings response did not align with the inputs', {
          ...this.contextOf(resolved),
          attempts
        })
      }
      const dim = vectors[0]?.length ?? 0
      if (dim > 0) this.cacheDimension(currentProfile.profileId, dim)
      const result: AiEmbeddingResult = {
        ...currentProfile,
        dim,
        vectors,
        usage
      }
      return result
    } catch (error) {
      activity.finish(error)
      throw error
    } finally {
      activity.finish()
    }
  }

  async probe(role: AiRole): Promise<AiProbeResult> {
    const result = await this.runProbe(role)
    this.probeCache.set(role, { result, atMs: this.clock.now().getTime() })
    return result
  }

  async availability(): Promise<AiAvailability> {
    const missing: AiRole[] = []
    const reasons: string[] = []
    let chatProfile: AiProfileSummary | null = null
    let embedding: AiEmbeddingProfile | null = null
    let chat = false

    const chatResolved = this.tryResolve('chat_main')
    if (chatResolved.error !== null) {
      missing.push('chat_main')
      reasons.push(`chat_main: ${chatResolved.error.message}`)
    } else {
      chatProfile = summaryOf(chatResolved.resolved)
      const key = await this.tryKey(chatResolved.resolved.provider)
      if (key.error !== null) {
        missing.push('chat_main')
        reasons.push(`chat_main: ${key.error.message}`)
      } else {
        const probe = await this.ensureProbe('chat_main')
        if (probe.ok) {
          chat = true
        } else {
          missing.push('chat_main')
          reasons.push(`chat_main probe: ${probe.message ?? probe.errorKind ?? 'failed'}`)
        }
      }
    }

    const embedResolved = this.tryResolve('embedding')
    if (embedResolved.error !== null) {
      missing.push('embedding')
      reasons.push(`embedding: ${embedResolved.error.message}`)
    } else {
      const profile = embeddingProfileOf(embedResolved.resolved)
      const cachedDim = this.cachedDimension(profile.profileId)
      embedding = { ...profile, dim: cachedDim }
      const key = await this.tryKey(embedResolved.resolved.provider)
      if (key.error !== null) {
        missing.push('embedding')
        reasons.push(`embedding: ${key.error.message}`)
      }
    }

    missing.push('rerank')
    reasons.push('rerank: no rerank protocol is registered')

    const probeEntry = this.probeCache.get('chat_main')
    return {
      available: chat,
      chat,
      embedding: embedding !== null && !missing.includes('embedding'),
      rerank: false,
      rerankReason: 'rerank_unavailable',
      missingRoles: [...new Set(missing)],
      reason: reasons.length === 0 ? null : reasons[0]!,
      chatProfile,
      embeddingProfile: embedding,
      probe:
        probeEntry === undefined
          ? null
          : { ok: probeEntry.result.ok, checkedAt: probeEntry.result.checkedAt }
    }
  }

  /* ------------------------------------------------------------ internals */

  private resolveCapability(role: AiRole, capability: 'chat' | 'embedding'): ResolvedRole {
    if (!isRole(role)) {
      throw new AiError('invalid_request', `unknown AI role: ${String(role)}`, {
        details: { field: 'role' }
      })
    }
    const resolved = this.roles.resolve(role)
    if (roleCapability(role) !== capability || resolved.capability !== capability) {
      throw new AiError(
        'unsupported_capability',
        `role ${role} does not serve ${capability}`,
        this.contextOf(resolved)
      )
    }
    return resolved
  }

  private tryResolve(role: AiRole): { resolved: ResolvedRole; error: null } | { resolved: null; error: AiError } {
    try {
      return { resolved: this.roles.resolve(role), error: null }
    } catch (cause) {
      return { resolved: null, error: asAiError(cause, 'role_unassigned') }
    }
  }

  private async keyFor(provider: ProviderConfig): Promise<string | null> {
    if (provider.apiKeyRef === '') {
      if (!isLoopbackUrl(provider.baseUrl)) {
        throw new AiError('secret_unavailable', 'provider has no API key', {
          providerId: provider.id,
          providerName: provider.name,
          protocol: provider.protocol
        })
      }
      return null
    }
    let key: string | null
    try {
      key = await this.secrets.get(provider.apiKeyRef)
    } catch (cause) {
      throw new AiError('secret_unavailable', 'provider key is unreadable', {
        providerId: provider.id,
        providerName: provider.name,
        protocol: provider.protocol,
        cause
      })
    }
    if (key === null) {
      throw new AiError('secret_unavailable', 'provider key is missing', {
        providerId: provider.id,
        providerName: provider.name,
        protocol: provider.protocol
      })
    }
    return key
  }

  private async tryKey(provider: ProviderConfig): Promise<{ error: AiError | null }> {
    try {
      await this.keyFor(provider)
      return { error: null }
    } catch (cause) {
      return { error: asAiError(cause, 'secret_unavailable') }
    }
  }

  private async callChat(
    adapter: ProtocolAdapter,
    resolved: ResolvedRole,
    request: ProtocolChatRequest,
    outerSignal: AbortSignal | undefined,
    operation: string
  ): Promise<CallOutcome> {
    const activity = this.startActivity(
      operation === 'chat_structured' ? 'structured' : 'chat',
      resolved.role,
      resolved.model.modelId
    )
    const start = this.clock.now().getTime()
    let lastError: AiError | null = null
    let attempts = 0
    try {
      for (let attempt = 1; attempt <= this.attempts; attempt += 1) {
        attempts = attempt
        const scope = this.scope(this.chatTimeoutMs, outerSignal)
        let emitted = false
        try {
          const response = await adapter.chat(
            {
              ...request,
              ...(request.onDelta === undefined
                ? {}
                : {
                    onDelta: (delta: string) => {
                      emitted = true
                      scope.touch()
                      request.onDelta?.(delta)
                    }
                  })
            },
            scope.signal
          )
          this.logCall(operation, resolved, attempt, start, null)
          return { response, attempts: attempt }
        } catch (cause) {
          const classified = this.classify(cause, resolved, scope)
          lastError = classified
          // A stream that already delivered text must not be replayed.
          if (emitted) {
            const partial = this.annotate(
              new AiError('server_error', `stream broke after partial output: ${classified.message}`, {
                ...this.contextOf(resolved),
                cause
              }),
              resolved,
              attempt
            )
            this.logCall(operation, resolved, attempt, start, partial)
            throw partial
          }
          if (attempt === this.attempts || !this.isRetryable(classified)) break
          if (!(await this.waitBeforeRetry(attempt, classified, outerSignal))) break
        } finally {
          scope.dispose()
        }
      }
      const final = this.annotate(lastError ?? new AiError('network', 'chat failed'), resolved, attempts)
      this.logCall(operation, resolved, attempts, start, final)
      throw final
    } catch (error) {
      activity.finish(error)
      throw error
    } finally {
      activity.finish()
    }
  }

  /** Emits `started`, a one-second `tick`, then `finished`/`failed`. */
  private startActivity(
    kind: AiActivityKind,
    role: string,
    model: string | null
  ): { finish(error?: unknown): void } {
    if (this.onActivity === undefined) return { finish: () => undefined }
    const callId = randomUUID()
    const startedAt = this.clock.now()
    const startedMs = Date.now()
    let done = false
    const emit = (phase: AiActivity['phase'], error?: unknown): void => {
      try {
        this.onActivity?.({
          callId,
          phase,
          kind,
          role,
          model,
          startedAt: startedAt.toISOString(),
          elapsedMs: Math.max(0, Date.now() - startedMs),
          error:
            error === null || error === undefined
              ? null
              : error instanceof Error
                ? error.message
                : String(error)
        })
      } catch {
        // The UI indicator must never break the AI call it describes.
      }
    }
    emit('started')
    const timer = setInterval(() => emit('tick'), 1000)
    timer.unref?.()
    return {
      finish: (error?: unknown) => {
        if (done) return
        done = true
        clearInterval(timer)
        emit(error === null || error === undefined ? 'finished' : 'failed', error)
      }
    }
  }

  private scope(
    timeoutMs: number,
    outer: AbortSignal | undefined
  ): {
    signal: AbortSignal
    timedOut: () => boolean
    touch: () => void
    dispose: () => void
  } {
    const controller = new AbortController()
    let timedOut = false
    let timer: NodeJS.Timeout | undefined
    const arm = (): void => {
      clearTimeout(timer)
      timer = setTimeout(() => {
        timedOut = true
        controller.abort()
      }, timeoutMs)
    }
    arm()
    const onAbort = (): void => controller.abort()
    if (outer !== undefined) {
      if (outer.aborted) controller.abort()
      else outer.addEventListener('abort', onAbort, { once: true })
    }
    return {
      signal: controller.signal,
      timedOut: () => timedOut,
      touch: arm,
      dispose: () => {
        clearTimeout(timer)
        outer?.removeEventListener('abort', onAbort)
      }
    }
  }

  private classify(
    cause: unknown,
    resolved: ResolvedRole,
    scope: { timedOut: () => boolean }
  ): AiError {
    if (scope.timedOut()) {
      return new AiError('timeout', 'request timed out', {
        ...this.contextOf(resolved),
        cause
      })
    }
    const error = asAiError(cause)
    if (isAiError(cause)) return error
    return this.annotate(error, resolved, 0)
  }

  private isRetryable(error: AiError): boolean {
    return RETRYABLE[error.kind] === true
  }

  /** Returns false when the caller must retry manually (Retry-After > 30 s). */
  private async waitBeforeRetry(
    attempt: number,
    error: AiError,
    outerSignal: AbortSignal | undefined
  ): Promise<boolean> {
    let delay: number
    if (error.retryAfterMs !== null && error.retryAfterMs > 0) {
      if (error.retryAfterMs > 30_000) return false
      delay = error.retryAfterMs
    } else {
      const base = this.backoffMs[Math.min(attempt - 1, this.backoffMs.length - 1)] ?? 1000
      const spread = base * this.jitter
      delay = Math.max(0, Math.round(base + (this.random() * 2 - 1) * spread))
    }
    try {
      await this.sleep(delay, outerSignal)
      return true
    } catch (cause) {
      throw new AiError('canceled', 'canceled while waiting to retry', {
        ...(error.providerId === null ? {} : { providerId: error.providerId }),
        ...(error.providerName === null ? {} : { providerName: error.providerName }),
        ...(error.protocol === null ? {} : { protocol: error.protocol }),
        ...(error.modelId === null ? {} : { modelId: error.modelId }),
        cause
      })
    }
  }

  private async ensureProbe(role: AiRole): Promise<AiProbeResult> {
    const cached = this.probeCache.get(role)
    const now = this.clock.now().getTime()
    if (cached !== undefined && now - cached.atMs <= this.probeTtlMs) return cached.result
    const inflight = this.probeInflight.get(role)
    if (inflight !== undefined) return inflight
    const promise = this.runProbe(role)
      .then((result) => {
        this.probeCache.set(role, { result, atMs: this.clock.now().getTime() })
        return result
      })
      .finally(() => {
        this.probeInflight.delete(role)
      })
    this.probeInflight.set(role, promise)
    return promise
  }

  private async runProbe(role: AiRole): Promise<AiProbeResult> {
    const started = this.clock.now().getTime()
    const checkedAt = formatTimestamp(this.clock.now())
    const failed = (kind: AiErrorKind, message: string, context: Partial<AiProbeResult> = {}): AiProbeResult => ({
      role,
      ok: false,
      checkedAt,
      latencyMs: Math.max(0, this.clock.now().getTime() - started),
      providerId: context.providerId ?? null,
      providerName: context.providerName ?? null,
      protocol: context.protocol ?? null,
      modelId: context.modelId ?? null,
      errorKind: kind,
      message: sanitize(message)
    })
    let resolved: ResolvedRole
    try {
      resolved = this.roles.resolve(role)
    } catch (cause) {
      const error = asAiError(cause, 'role_unassigned')
      return failed(error.kind, error.message)
    }
    if (roleCapability(role) !== 'chat') {
      return failed('unsupported_capability', `role ${role} does not serve chat`, summaryOf(resolved))
    }
    const summary = summaryOf(resolved)
    let key: string | null
    try {
      key = await this.keyFor(resolved.provider)
    } catch (cause) {
      const error = asAiError(cause, 'secret_unavailable')
      return failed(error.kind, error.message, summary)
    }
    const adapter = this.factory.create(resolved.provider, key)
    const scope = this.scope(this.probeTimeoutMs, undefined)
    try {
      await adapter.chat(
        {
          model: resolved.model.modelId,
          messages: [{ role: 'user', content: 'Reply with the single word OK.' }],
          temperature: 0,
          maxOutputTokens: 8
        },
        scope.signal
      )
      return {
        role,
        ok: true,
        checkedAt,
        latencyMs: Math.max(0, this.clock.now().getTime() - started),
        ...summary,
        errorKind: null,
        message: null
      }
    } catch (cause) {
      const error = this.classify(cause, resolved, scope)
      return failed(error.kind, error.message, summary)
    } finally {
      scope.dispose()
    }
  }

  private cachedDimension(profileId: string): number | null {
    if (this.settings === undefined) return null
    try {
      const dim = this.settings.getNumber(`ai.embedding_dim.${profileId}`, 0)
      return dim > 0 ? dim : null
    } catch {
      return null
    }
  }

  private cacheDimension(profileId: string, dim: number): void {
    if (this.settings === undefined || dim <= 0) return
    try {
      this.settings.set(`ai.embedding_dim.${profileId}`, String(dim))
    } catch (cause) {
      this.logger?.warn('ai embedding dimension cache failed', { profileId, error: String(cause) })
    }
  }

  private logCall(
    operation: string,
    resolved: ResolvedRole,
    attempts: number,
    startedAt: number,
    error: AiError | null
  ): void {
    if (this.logger === undefined) return
    const fields: Record<string, unknown> = {
      operation,
      providerId: resolved.provider.id,
      providerName: resolved.provider.name,
      protocol: resolved.provider.protocol,
      model: resolved.model.modelId,
      attempts,
      elapsedMs: this.clock.now().getTime() - startedAt,
      ok: error === null
    }
    if (error !== null) {
      fields.errorKind = error.kind
      if (error.statusCode !== null) fields.statusCode = error.statusCode
    }
    this.logger.info('ai call', fields)
  }

  private annotate(error: AiError, resolved: ResolvedRole, attempts: number): AiError {
    return new AiError(error.kind, error.message, {
      providerId: error.providerId ?? resolved.provider.id,
      providerName: error.providerName ?? resolved.provider.name,
      protocol: error.protocol ?? resolved.provider.protocol,
      modelId: error.modelId ?? resolved.model.modelId,
      ...(error.statusCode === null ? {} : { statusCode: error.statusCode }),
      ...(error.retryAfterMs === null ? {} : { retryAfterMs: error.retryAfterMs }),
      attempts,
      cause: error.cause
    })
  }

  private contextOf(resolved: ResolvedRole): {
    providerId: string
    providerName: string
    protocol: ResolvedRole['provider']['protocol']
    modelId: string
  } {
    return {
      providerId: resolved.provider.id,
      providerName: resolved.provider.name,
      protocol: resolved.provider.protocol,
      modelId: resolved.model.modelId
    }
  }
}

/* ---------------------------------------------------------------- helpers */

function isRole(value: unknown): value is AiRole {
  return value === 'chat_main' || value === 'embedding' || value === 'rerank'
}

function normalizeBudget(value: number | undefined): number {
  if (value === undefined) return DEFAULT_MAX_OUTPUT_TOKENS
  if (!Number.isFinite(value) || value < 0) {
    throw new AiError('invalid_request', 'maxOutputTokens must be a non-negative number', {
      details: { field: 'maxOutputTokens' }
    })
  }
  return value === 0 ? DEFAULT_MAX_OUTPUT_TOKENS : Math.floor(value)
}

function summaryOf(resolved: ResolvedRole): AiProfileSummary {
  return {
    providerId: resolved.provider.id,
    providerName: resolved.provider.name,
    protocol: resolved.provider.protocol,
    modelId: resolved.model.modelId
  }
}

/** Opaque, stable vector-space key (must not be parsed back). */
export function embeddingProfileId(providerId: string, modelId: string): string {
  return createHash('sha256')
    .update(`researchos/embedding-profile/v1|${providerId}|${modelId}`)
    .digest('hex')
}

function embeddingProfileOf(resolved: ResolvedRole): AiEmbeddingProfile {
  const profileId = embeddingProfileId(resolved.provider.id, resolved.model.modelId)
  return {
    ...summaryOf(resolved),
    profileId,
    dim: null
  }
}
