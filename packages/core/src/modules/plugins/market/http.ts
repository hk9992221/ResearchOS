import { PLUGIN_MARKET_SETTING_KEYS, PLUGIN_MARKET_TIMEOUT_MS } from '@researchos/shared'

import { AppError, AppErrorCode, appError } from '../../../errors'
import { SettingsStore } from '../../../settings'
import { assertRemoteUrl } from './registry'

/**
 * HTTP for the market layer. One narrow seam: the caller states
 * a byte cap, the transport either returns a body inside that cap or fails.
 * Timeouts, size caps and the GitHub mirror fallback live here so every
 * caller — index document, README, release asset — gets the same rules.
 */

export interface MarketRequest {
  url: string
  /** Extra request headers (etag conditionals, Accept). */
  headers?: Record<string, string>
  maxBytes: number
  timeoutMs?: number
}

export interface MarketRawResponse {
  status: number
  /** Lower-cased response headers. */
  headers: Record<string, string>
  body: Buffer
}

export type MarketTransport = (request: MarketRequest) => Promise<MarketRawResponse>

/** Statuses worth retrying through a mirror; others are the answer. */
const RETRYABLE_STATUS = new Set([403, 408, 425, 429, 500, 502, 503, 504])

function headerOf(headers: Record<string, string>, name: string): string {
  return headers[name.toLowerCase()] ?? ''
}

function contentLengthOf(headers: Record<string, string>): number | null {
  const raw = headerOf(headers, 'content-length')
  if (raw === '') return null
  const value = Number(raw)
  return Number.isFinite(value) && value >= 0 ? value : null
}

/**
 * The default transport: Node `fetch` with a hard timeout, redirect following
 * and an aborting stream cap. A `Content-Length` above the cap is rejected
 * before a single byte is read.
 */
export const fetchMarketTransport: MarketTransport = async (request) => {
  const timeoutMs = request.timeoutMs ?? PLUGIN_MARKET_TIMEOUT_MS
  const signal = AbortSignal.timeout(timeoutMs)
  let response: Response
  try {
    response = await fetch(request.url, {
      method: 'GET',
      headers: request.headers,
      redirect: 'follow',
      signal
    })
  } catch (error) {
    throw appError(AppErrorCode.ExternalUnavailable, `market request failed: ${request.url}`, {
      reason: 'market_network',
      cause: error instanceof Error ? error.message : String(error)
    })
  }
  // A redirect can leave https; the final URL is checked, not just the request.
  assertRemoteUrl(response.url === '' ? request.url : response.url, 'url')

  const declared = contentLengthOf(normalizeHeaders(response.headers))
  if (declared !== null && declared > request.maxBytes) {
    throw appError(AppErrorCode.LimitExceeded, `remote resource exceeds ${String(request.maxBytes)} bytes`, {
      reason: 'market_size',
      declared,
      maxBytes: request.maxBytes
    })
  }

  const chunks: Buffer[] = []
  let received = 0
  if (response.body !== null) {
    const reader = response.body.getReader()
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      if (value === undefined) continue
      received += value.byteLength
      if (received > request.maxBytes) {
        await reader.cancel().catch(() => undefined)
        throw appError(AppErrorCode.LimitExceeded, `remote resource exceeds ${String(request.maxBytes)} bytes`, {
          reason: 'market_size',
          received,
          maxBytes: request.maxBytes
        })
      }
      chunks.push(Buffer.from(value))
    }
  }
  return {
    status: response.status,
    headers: normalizeHeaders(response.headers),
    body: Buffer.concat(chunks, received)
  }
}

function normalizeHeaders(headers: Headers): Record<string, string> {
  const out: Record<string, string> = {}
  headers.forEach((value, key) => {
    out[key.toLowerCase()] = value
  })
  return out
}

export interface MarketHttpOptions {
  transport?: MarketTransport
  /** Workspace settings; required unless `mirrorPrefix` is supplied. */
  settings?: SettingsStore
  timeoutMs?: number
  /** Reads the mirror prefix directly, for callers without settings. */
  mirrorPrefix?: () => string
  logger?: { warn(message: string, fields?: Record<string, unknown>): void }
}

export interface FetchedResource {
  /** True when the conditional request proved the cached copy current. */
  notModified: boolean
  body: Buffer
  etag: string
  /** The URL that finally served the bytes (mirror or origin). */
  url: string
}

/**
 * Resolves mirror prefixes and retries once through them. The mirror is read
 * from settings on every request: a network change should not need a restart.
 */
export class MarketHttp {
  private readonly transport: MarketTransport
  private readonly settings: SettingsStore | undefined
  private readonly timeoutMs: number
  private readonly mirrorOf: () => string
  private readonly logger: MarketHttpOptions['logger']

  constructor(options: MarketHttpOptions) {
    this.transport = options.transport ?? fetchMarketTransport
    this.settings = options.settings
    this.timeoutMs = options.timeoutMs ?? PLUGIN_MARKET_TIMEOUT_MS
    this.mirrorOf =
      options.mirrorPrefix ??
      (() => this.settings?.get(PLUGIN_MARKET_SETTING_KEYS.mirrorPrefix)?.trim() ?? '')
    this.logger = options.logger
  }

  /** GitHub hosts only; other origins must not be proxied by a third party. */
  private mirrored(url: string, prefix: string): string {
    let parsed: URL
    try {
      parsed = new URL(url)
    } catch {
      return ''
    }
    if (parsed.hostname !== 'github.com' && parsed.hostname !== 'raw.githubusercontent.com') return ''
    const base = prefix.endsWith('/') ? prefix : `${prefix}/`
    try {
      const proxied = new URL(base + parsed.toString())
      // A mirror that is not https (or a local test server) is never used.
      if (proxied.protocol !== 'https:') {
        const loopback =
          proxied.protocol === 'http:' &&
          (proxied.hostname === 'localhost' || proxied.hostname === '127.0.0.1')
        if (!loopback) return ''
      }
      return proxied.toString()
    } catch {
      return ''
    }
  }

  /**
   * GETs `url`, retrying through the configured mirror when the origin is
   * unreachable or throttled. `notModified` comes back as a result, not an
   * error, because 304 is the healthy steady state for a cached index.
   */
  async get(
    url: string,
    init: { maxBytes: number; etag?: string; accept?: string }
  ): Promise<FetchedResource> {
    assertRemoteUrl(url, 'url')
    const prefix = this.mirrorOf()
    const candidates: string[] = [url]
    if (prefix !== '') {
      const mirror = this.mirrored(url, prefix)
      if (mirror !== '' && mirror !== url) candidates.push(mirror)
    }
    const headers: Record<string, string> = { accept: init.accept ?? '*/*' }
    if (init.etag !== undefined && init.etag !== '') headers['if-none-match'] = init.etag

    let lastError: unknown = null
    for (let index = 0; index < candidates.length; index += 1) {
      const candidate = candidates[index]!
      // Only a non-final candidate may fall through to the mirror.
      const canRetry = index < candidates.length - 1
      let response: MarketRawResponse
      try {
        response = await this.transport({
          url: candidate,
          headers,
          maxBytes: init.maxBytes,
          timeoutMs: this.timeoutMs
        })
      } catch (error) {
        if (!canRetry) throw error
        this.logger?.warn('market origin request failed, trying mirror', {
          url: candidate,
          error: error instanceof Error ? error.message : String(error)
        })
        lastError = error
        continue
      }

      if (response.status === 304) {
        return { notModified: true, body: Buffer.alloc(0), etag: init.etag ?? '', url: candidate }
      }
      if (response.status >= 400) {
        const error = statusError(response.status, candidate)
        if (canRetry && RETRYABLE_STATUS.has(response.status)) {
          this.logger?.warn('market origin failed, trying mirror', {
            url: candidate,
            status: response.status
          })
          lastError = error
          continue
        }
        throw error
      }
      return {
        notModified: false,
        body: response.body,
        etag: headerOf(response.headers, 'etag'),
        url: candidate
      }
    }
    throw lastError ?? appError(AppErrorCode.ExternalUnavailable, 'market request failed', {
      reason: 'market_network'
    })
  }

  async text(
    url: string,
    init: { maxBytes: number; etag?: string }
  ): Promise<FetchedResource> {
    return this.get(url, { ...init, accept: 'text/plain, text/markdown, application/json;q=0.9' })
  }

  async bytes(url: string, init: { maxBytes: number }): Promise<FetchedResource> {
    return this.get(url, { ...init, accept: 'application/zip, application/octet-stream' })
  }
}

function statusError(status: number, url: string): AppError {
  const code =
    status === 404 || status === 410
      ? AppErrorCode.NotFound
      : status === 413
        ? AppErrorCode.LimitExceeded
        : AppErrorCode.ExternalUnavailable
  return appError(code, `market resource unavailable (${String(status)}): ${url}`, {
    reason: 'market_status',
    status
  })
}
