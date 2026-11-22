/**
 * Per-client token buckets.
 *
 * Reads and writes have separate buckets: 300/min burst 30 for `R` and
 * read-only `J`, 60/min burst 10 for `W`/`C`/writing `J`. The status and
 * job/confirmation reads are exempt so polling can never starve a batch import.
 */
import { AppErrorCode } from '@researchos/shared'

import { type Clock } from '../../clock'
import { appError } from '../../errors'
import { clockOf, type RuntimeOptions } from './store'

export type LimitKind = 'read' | 'write'

export interface BucketConfig {
  perMinute: number
  burst: number
}

/** Defect 9: read traffic must never hit the write bucket. */
export const READ_LIMIT: BucketConfig = { perMinute: 300, burst: 30 }
export const WRITE_LIMIT: BucketConfig = { perMinute: 60, burst: 10 }

/** Exempt reads: status plus the poll endpoints clients hit while waiting. */
export const RATE_LIMIT_EXEMPT: Readonly<Record<string, true>> = {
  server_status: true,
  job_get: true,
  job_list: true,
  confirmation_get: true,
  confirmation_list: true
}

export function isRateLimitExempt(toolName: string): boolean {
  return RATE_LIMIT_EXEMPT[toolName] === true
}

interface Bucket {
  tokens: number
  lastMs: number
}

export interface LimitDecision {
  readonly allowed: boolean
  /** Milliseconds until one token is available; 0 when allowed. */
  readonly retryAfterMs: number
}

export class RateLimiter {
  private readonly read: BucketConfig
  private readonly write: BucketConfig
  private readonly clock: Clock
  private readonly buckets = new Map<string, Bucket>()

  constructor(
    options: RuntimeOptions & { read?: BucketConfig; write?: BucketConfig } = {}
  ) {
    this.clock = options.clock ?? clockOf(options)
    this.read = options.read ?? READ_LIMIT
    this.write = options.write ?? WRITE_LIMIT
  }

  /** Consumes one token; returns the retry delay when the bucket is empty. */
  check(clientId: string, kind: LimitKind): LimitDecision {
    const config = kind === 'read' ? this.read : this.write
    const key = `${kind}:${clientId === '' ? 'anonymous' : clientId}`
    const now = this.clock.now().getTime()
    let bucket = this.buckets.get(key)
    if (bucket === undefined) {
      bucket = { tokens: config.burst, lastMs: now }
      this.buckets.set(key, bucket)
    }
    const perSecond = config.perMinute / 60
    const elapsedSeconds = Math.max(0, (now - bucket.lastMs) / 1000)
    if (elapsedSeconds > 0) {
      bucket.tokens = Math.min(config.burst, bucket.tokens + elapsedSeconds * perSecond)
      bucket.lastMs = now
    }
    if (bucket.tokens >= 1) {
      bucket.tokens -= 1
      return { allowed: true, retryAfterMs: 0 }
    }
    const retryAfterMs = Math.ceil(((1 - bucket.tokens) / perSecond) * 1000)
    return { allowed: false, retryAfterMs }
  }

  /** Throws RATE_LIMITED with `retry_after_ms` when the bucket is empty. */
  enforce(clientId: string, toolName: string, kind: LimitKind): void {
    if (isRateLimitExempt(toolName)) return
    const decision = this.check(clientId, kind)
    if (decision.allowed) return
    throw appError(AppErrorCode.RateLimited, '请求过于频繁，请稍后重试', {
      details: { retry_after_ms: decision.retryAfterMs, tool: toolName, kind }
    })
  }

  /** Drops both buckets of a disconnected client. */
  forget(clientId: string): void {
    this.buckets.delete(`read:${clientId}`)
    this.buckets.delete(`write:${clientId}`)
  }

  /** Test seam: remaining tokens (fractional). */
  tokens(clientId: string, kind: LimitKind): number {
    return this.buckets.get(`${kind}:${clientId === '' ? 'anonymous' : clientId}`)?.tokens ?? 0
  }
}