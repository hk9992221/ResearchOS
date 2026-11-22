import type { AiProtocol } from '@researchos/shared'

import { AiError, sanitize } from '../errors'
import type { AdapterDependencies, FetchLike, ProviderConfig } from './types'

const ANTHROPIC_VERSION = '2023-05-31'
const MAX_RESPONSE_BYTES = 64 * 1024 * 1024
const MAX_SSE_EVENT_BYTES = 16 * 1024 * 1024

export interface SseEvent {
  name: string
  data: string
}

type HttpMethod = 'GET' | 'POST'

/**
 * One protocol transport: URL join, auth headers, JSON decoding, SSE parsing
 * and upstream error mapping. Adapters never retry — the gateway owns the
 * retry budget.
 */
export class HttpTransport {
  readonly config: ProviderConfig
  private readonly apiKey: string | null
  private readonly fetchImpl: FetchLike

  constructor(deps: AdapterDependencies) {
    this.config = deps.config
    this.apiKey = deps.apiKey === null || deps.apiKey === '' ? null : deps.apiKey
    this.fetchImpl = deps.fetchImpl ?? fetch
  }

  get protocol(): AiProtocol {
    return this.config.protocol
  }

  url(path: string): string {
    return `${this.config.baseUrl.replace(/\/+$/, '')}${path}`
  }

  private headers(acceptJson = true): Record<string, string> {
    const headers: Record<string, string> = {}
    if (acceptJson) headers['Content-Type'] = 'application/json'
    if (this.apiKey !== null) {
      switch (this.config.protocol) {
        case 'openai_chat':
        case 'openai_responses':
          headers.Authorization = `Bearer ${this.apiKey}`
          break
        case 'gemini':
          headers['x-goog-api-key'] = this.apiKey
          break
        case 'anthropic':
          headers['x-api-key'] = this.apiKey
          headers['anthropic-version'] = ANTHROPIC_VERSION
          break
      }
    } else if (this.config.protocol === 'anthropic') {
      headers['anthropic-version'] = ANTHROPIC_VERSION
    }
    return headers
  }

  async json<T>(
    method: HttpMethod,
    path: string,
    body: unknown,
    signal?: AbortSignal
  ): Promise<T> {
    const response = await this.send(method, path, body, signal, true)
    const text = await response.text()
    if (text.length > MAX_RESPONSE_BYTES) {
      throw this.error('server_error', 'response body exceeded the size limit', response.status)
    }
    if (text.trim() === '') return undefined as T
    try {
      return JSON.parse(text) as T
    } catch (cause) {
      throw new AiError('server_error', `decode response: ${sanitize(text.slice(0, 400))}`, {
        ...this.context(),
        statusCode: response.status,
        cause
      })
    }
  }

  /**
   * POSTs JSON and dispatches every server-sent event. A non-2xx answer is
   * mapped before reading; a stream that ends without a terminal event leaves
   * that decision to the adapter.
   */
  async sse(
    path: string,
    body: unknown,
    onEvent: (event: SseEvent) => void,
    signal?: AbortSignal
  ): Promise<void> {
    const response = await this.send('POST', path, body, signal, false)
    if (response.body === null) {
      throw new AiError('server_error', 'stream response carried no body', {
        ...this.context(),
        statusCode: response.status
      })
    }
    const reader = response.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''
    let name = ''
    let data = ''
    let dataBytes = 0

    const dispatch = (): void => {
      if (data === '') {
        name = ''
        return
      }
      const payload = data
      const eventName = name
      name = ''
      data = ''
      dataBytes = 0
      onEvent({ name: eventName, data: payload })
    }

    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      for (;;) {
        const newline = buffer.indexOf('\n')
        if (newline < 0) break
        let line = buffer.slice(0, newline)
        buffer = buffer.slice(newline + 1)
        if (line.endsWith('\r')) line = line.slice(0, -1)
        if (line === '') {
          dispatch()
          continue
        }
        if (line.startsWith(':')) continue // keepalive comment
        const colon = line.indexOf(':')
        const field = colon < 0 ? line : line.slice(0, colon)
        let fieldValue = colon < 0 ? '' : line.slice(colon + 1)
        if (fieldValue.startsWith(' ')) fieldValue = fieldValue.slice(1)
        if (field === 'event') {
          name = fieldValue
        } else if (field === 'data') {
          dataBytes += fieldValue.length
          if (dataBytes > MAX_SSE_EVENT_BYTES) {
            throw new AiError('server_error', 'stream event exceeded the size limit', this.context())
          }
          data = data === '' ? fieldValue : `${data}\n${fieldValue}`
        }
      }
    }
    buffer += decoder.decode()
    if (buffer.trim() !== '') {
      const line = buffer.replace(/\r$/, '')
      if (line.startsWith('data:')) {
        const value = line.slice(5).replace(/^ /, '')
        data = data === '' ? value : `${data}\n${value}`
      }
    }
    dispatch()
  }

  unsupported(capability: string): AiError {
    return new AiError(
      'unsupported_capability',
      `protocol ${this.config.protocol} does not support ${capability}`,
      this.context()
    )
  }

  error(kind: AiError['kind'], message: string, statusCode?: number, cause?: unknown): AiError {
    return new AiError(kind, message, {
      ...this.context(),
      ...(statusCode === undefined ? {} : { statusCode }),
      cause
    })
  }

  context(): {
    providerId: string
    providerName: string
    protocol: AiProtocol
  } {
    return {
      providerId: this.config.id,
      providerName: this.config.name,
      protocol: this.config.protocol
    }
  }

  private async send(
    method: HttpMethod,
    path: string,
    body: unknown,
    signal: AbortSignal | undefined,
    jsonResponse: boolean
  ): Promise<Response> {
    const headers = this.headers(jsonResponse)
    if (body === undefined) delete headers['Content-Type']
    if (!jsonResponse) headers.Accept = 'text/event-stream'
    let response: Response
    try {
      response = await this.fetchImpl(this.url(path), {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        signal
      })
    } catch (cause) {
      throw this.transportError(cause, signal)
    }
    if (!response.ok) {
      const text = await response.text().catch(() => '')
      throw this.mapHttp(response.status, response.headers.get('retry-after'), text)
    }
    return response
  }

  private transportError(cause: unknown, signal: AbortSignal | undefined): AiError {
    const name = cause instanceof Error ? cause.name : ''
    const message = cause instanceof Error ? cause.message : String(cause)
    if (name === 'TimeoutError' || /timeout/i.test(message)) {
      return this.error('timeout', message, undefined, cause)
    }
    if (signal?.aborted === true || name === 'AbortError') {
      return this.error('canceled', 'canceled by caller', undefined, cause)
    }
    return this.error('network', message, undefined, cause)
  }

  private mapHttp(status: number, retryAfterHeader: string | null, text: string): AiError {
    const message = sanitize(extractUpstreamMessage(text), this.apiKey === null ? [] : [this.apiKey])
    let kind: AiError['kind']
    switch (true) {
      case status === 401 || status === 403:
        kind = 'auth'
        break
      case status === 408:
        kind = 'timeout'
        break
      case status === 429:
        kind = 'rate_limited'
        break
      case status === 404:
        kind = 'model_not_found'
        break
      case status >= 500:
        kind = 'server_error'
        break
      default:
        kind = 'invalid_request'
    }
    if (kind === 'invalid_request' && looksContextTooLong(message)) {
      kind = 'context_too_long'
    }
    const retryAfterMs = parseRetryAfter(retryAfterHeader)
    return new AiError(kind, message, {
      ...this.context(),
      statusCode: status,
      ...(retryAfterMs === null ? {} : { retryAfterMs })
    })
  }
}

function parseRetryAfter(value: string | null): number | null {
  if (value === null || value.trim() === '') return null
  const seconds = Number(value)
  if (Number.isFinite(seconds) && seconds >= 0) return Math.round(seconds * 1000)
  const date = Date.parse(value)
  if (Number.isNaN(date)) return null
  const delta = date - Date.now()
  return delta > 0 ? delta : null
}

function looksContextTooLong(message: string): boolean {
  const lower = message.toLowerCase()
  return [
    'context_length_exceeded',
    'maximum context length',
    'prompt is too long',
    'exceeds the model',
    'token limit',
    'exceeds context window',
    'input token count'
  ].some((probe) => lower.includes(probe))
}

function extractUpstreamMessage(text: string): string {
  const trimmed = text.trim()
  if (trimmed === '') return 'empty response body'
  try {
    const parsed = JSON.parse(trimmed) as {
      error?: { message?: unknown; type?: unknown; code?: unknown }
      message?: unknown
    }
    const error = parsed.error
    if (error !== undefined && error !== null && typeof error === 'object') {
      const parts: string[] = []
      if (typeof error.message === 'string') parts.push(error.message)
      if (typeof error.type === 'string') parts.push(error.type)
      if (parts.length > 0) return sanitize(parts.join(': '))
    }
    if (typeof parsed.message === 'string') return sanitize(parsed.message)
  } catch {
    // fall through to the raw fragment
  }
  return sanitize(trimmed.slice(0, 400))
}