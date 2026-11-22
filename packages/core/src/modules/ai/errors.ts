import { AppError, AppErrorCode } from '@researchos/shared'
import type { AiErrorKind, AiProtocol } from '@researchos/shared'

export interface AiErrorContext {
  providerId?: string
  providerName?: string
  protocol?: AiProtocol
  modelId?: string
  statusCode?: number
  retryAfterMs?: number
  attempts?: number
  cause?: unknown
  details?: Record<string, unknown>
}

const CODE_FOR_KIND: Readonly<Record<AiErrorKind, AppErrorCode>> = {
  auth: AppErrorCode.AiUnavailable,
  rate_limited: AppErrorCode.RateLimited,
  timeout: AppErrorCode.ExternalUnavailable,
  network: AppErrorCode.ExternalUnavailable,
  invalid_request: AppErrorCode.InvalidArgument,
  model_not_found: AppErrorCode.AiUnavailable,
  unsupported_capability: AppErrorCode.Unsupported,
  context_too_long: AppErrorCode.InvalidArgument,
  server_error: AppErrorCode.ExternalUnavailable,
  canceled: AppErrorCode.Interrupted,
  role_unassigned: AppErrorCode.AiUnavailable,
  provider_disabled: AppErrorCode.AiUnavailable,
  secret_unavailable: AppErrorCode.SecretUnavailable,
  model_unavailable: AppErrorCode.AiUnavailable,
  persist_failed: AppErrorCode.Internal,
  truncated: AppErrorCode.AiOutputTruncated,
  output_invalid: AppErrorCode.AiOutputInvalid
}

/**
 * The single typed AI failure. The message is always sanitized;
 * provider/model/status/attempts travel in `details` for UI and logs.
 */
export class AiError extends AppError {
  readonly kind: AiErrorKind
  readonly providerId: string | null
  readonly providerName: string | null
  readonly protocol: AiProtocol | null
  readonly modelId: string | null
  readonly statusCode: number | null
  readonly retryAfterMs: number | null
  readonly attempts: number

  constructor(kind: AiErrorKind, message: string, context: AiErrorContext = {}) {
    const details: Record<string, unknown> = {
      kind,
      ...(context.providerId === undefined ? {} : { providerId: context.providerId }),
      ...(context.providerName === undefined ? {} : { providerName: context.providerName }),
      ...(context.protocol === undefined ? {} : { protocol: context.protocol }),
      ...(context.modelId === undefined ? {} : { modelId: context.modelId }),
      ...(context.statusCode === undefined ? {} : { statusCode: context.statusCode }),
      ...(context.retryAfterMs === undefined ? {} : { retryAfterMs: context.retryAfterMs }),
      ...(context.attempts === undefined ? {} : { attempts: context.attempts }),
      ...context.details
    }
    super(CODE_FOR_KIND[kind], sanitize(message), details, { cause: context.cause })
    this.name = 'AiError'
    this.kind = kind
    this.providerId = context.providerId ?? null
    this.providerName = context.providerName ?? null
    this.protocol = context.protocol ?? null
    this.modelId = context.modelId ?? null
    this.statusCode = context.statusCode ?? null
    this.retryAfterMs = context.retryAfterMs ?? null
    this.attempts = context.attempts ?? 0
  }

  withAttempts(attempts: number): AiError {
    return new AiError(this.kind, this.message, {
      ...this.context(),
      attempts,
      cause: this.cause
    })
  }

  context(): AiErrorContext {
    return {
      providerId: this.providerId ?? undefined,
      providerName: this.providerName ?? undefined,
      protocol: this.protocol ?? undefined,
      modelId: this.modelId ?? undefined,
      statusCode: this.statusCode ?? undefined,
      retryAfterMs: this.retryAfterMs ?? undefined,
      attempts: this.attempts
    }
  }
}

export function isAiError(value: unknown): value is AiError {
  return value instanceof AiError
}

/** Wraps any thrown value into an AiError without losing the cause. */
export function asAiError(value: unknown, kind: AiErrorKind = 'network'): AiError {
  if (isAiError(value)) return value
  const message = value instanceof Error ? value.message : String(value)
  return new AiError(kind, message, { cause: value })
}

const BEARER = /\bbearer\s+[A-Za-z0-9._~+/=-]+/gi
const SK_KEY = /\bsk-[A-Za-z0-9_-]{4,}/g
const KEY_VALUE =
  /\b(api[_-]?key|authorization|x[-_]api[-_]key|x[-_]goog[-_]api[-_]key)\s*[=:]\s*"?[^",\s}]+/gi

/** Removes key-like material and bounds the message. */
export function sanitize(message: string, secrets: readonly string[] = []): string {
  let text = message
  for (const secret of secrets) {
    if (secret !== '') text = text.split(secret).join('***')
  }
  text = text.replace(BEARER, 'Bearer ***').replace(SK_KEY, 'sk-***').replace(KEY_VALUE, '$1=***')
  text = text.replace(/\s+/g, ' ').trim()
  const chars = [...text]
  if (chars.length <= 200) return text
  return `${chars.slice(0, 197).join('')}...`
}