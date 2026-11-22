import { HttpTransport } from './http'
import type {
  AdapterDependencies,
  ProtocolAdapter,
  ProtocolChatRequest,
  ProtocolChatResponse,
  ProtocolUsage,
  UpstreamModel
} from './types'

interface AnthropicMessage {
  id?: string
  model?: string
  content?: Array<{ type?: string; text?: string }>
  stop_reason?: string | null
  usage?: { input_tokens?: number; output_tokens?: number }
  error?: { type?: string; message?: string }
}

function textOf(message: AnthropicMessage): string {
  return (message.content ?? [])
    .filter((block) => block.type === 'text')
    .map((block) => block.text ?? '')
    .join('')
}

function usageOf(usage: AnthropicMessage['usage']): ProtocolUsage {
  return { inputTokens: usage?.input_tokens ?? 0, outputTokens: usage?.output_tokens ?? 0 }
}

export class AnthropicAdapter implements ProtocolAdapter {
  readonly protocol = 'anthropic' as const
  private readonly http: HttpTransport

  constructor(deps: AdapterDependencies) {
    this.http = new HttpTransport(deps)
  }

  async listModels(signal?: AbortSignal): Promise<UpstreamModel[]> {
    const models: UpstreamModel[] = []
    let after = ''
    for (;;) {
      const query = after === '' ? '?limit=100' : `?limit=100&after=${encodeURIComponent(after)}`
      const body = await this.http.json<{
        data?: Array<{ id?: string; display_name?: string }>
        has_more?: boolean
        last_id?: string
      }>('GET', `/models${query}`, undefined, signal)
      for (const model of body.data ?? []) {
        if (typeof model.id !== 'string' || model.id === '') continue
        models.push({
          modelId: model.id,
          displayName: model.display_name ?? model.id,
          capabilities: ['chat']
        })
      }
      if (body.has_more !== true || body.last_id === undefined || body.last_id === '') {
        return models
      }
      after = body.last_id
    }
  }

  async chat(request: ProtocolChatRequest, signal?: AbortSignal): Promise<ProtocolChatResponse> {
    const system: string[] = []
    const messages: Array<{ role: 'user' | 'assistant'; content: string }> = []
    for (const message of request.messages) {
      if (message.role === 'system') system.push(message.content)
      else messages.push({ role: message.role, content: message.content })
    }
    const body: Record<string, unknown> = {
      model: request.model,
      messages,
      // Messages API requires max_tokens; the gateway always sets a budget.
      max_tokens: request.maxOutputTokens > 0 ? request.maxOutputTokens : 8192
    }
    if (system.length > 0) body.system = system.join('\n\n')
    if (request.temperature !== undefined) body.temperature = request.temperature

    if (request.onDelta === undefined) {
      const response = await this.http.json<AnthropicMessage>('POST', '/messages', body, signal)
      if (response.error !== undefined && response.error !== null) {
        throw this.http.error('server_error', response.error.message ?? 'anthropic error')
      }
      return {
        content: textOf(response),
        model: response.model ?? request.model,
        usage: usageOf(response.usage),
        finishReason: response.stop_reason ?? null,
        requestId: response.id ?? null,
        streamed: false
      }
    }

    let content = ''
    let model = request.model
    let requestId: string | null = null
    let finishReason: string | null = null
    let usage: ProtocolUsage = { inputTokens: 0, outputTokens: 0 }
    let terminal = false
    await this.http.sse(
      '/messages',
      { ...body, stream: true },
      (event) => {
        const trimmed = event.data.trim()
        if (trimmed === '' || trimmed === '[DONE]') return
        let payload: {
          type?: string
          message?: AnthropicMessage
          delta?: { type?: string; text?: string; stop_reason?: string }
          usage?: { input_tokens?: number; output_tokens?: number }
          error?: { type?: string; message?: string }
        }
        try {
          payload = JSON.parse(trimmed) as typeof payload
        } catch {
          throw this.http.error('server_error', `decode stream event: ${trimmed.slice(0, 200)}`)
        }
        const name = event.name !== '' ? event.name : (payload.type ?? '')
        switch (name) {
          case 'message_start': {
            if (payload.message !== undefined) {
              if (typeof payload.message.id === 'string') requestId = payload.message.id
              if (typeof payload.message.model === 'string' && payload.message.model !== '') {
                model = payload.message.model
              }
              usage = { ...usage, inputTokens: payload.message.usage?.input_tokens ?? 0 }
            }
            break
          }
          case 'content_block_delta': {
            const delta = payload.delta?.text
            if (typeof delta === 'string' && delta !== '') {
              content += delta
              request.onDelta?.(delta)
            }
            break
          }
          case 'message_delta': {
            if (typeof payload.delta?.stop_reason === 'string') {
              finishReason = payload.delta.stop_reason
            }
            if (payload.usage?.output_tokens !== undefined) {
              usage = { ...usage, outputTokens: payload.usage.output_tokens }
            }
            break
          }
          case 'message_stop':
            terminal = true
            break
          case 'error':
            throw this.http.error(
              'server_error',
              [payload.error?.type, payload.error?.message].filter(Boolean).join(': ')
            )
          default:
            break
        }
      },
      signal
    )
    if (!terminal) {
      throw this.http.error('server_error', 'anthropic stream ended without message_stop')
    }
    return { content, model, usage, finishReason, requestId, streamed: true }
  }
}

export function createAnthropicAdapter(deps: AdapterDependencies): ProtocolAdapter {
  return new AnthropicAdapter(deps)
}