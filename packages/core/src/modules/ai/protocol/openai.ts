import { AiError } from '../errors'
import { HttpTransport } from './http'
import type {
  AdapterDependencies,
  ProtocolAdapter,
  ProtocolChatRequest,
  ProtocolChatResponse,
  ProtocolEmbedRequest,
  ProtocolEmbedResponse,
  ProtocolUsage,
  UpstreamModel
} from './types'

interface ChatCompletionChunk {
  id?: string
  model?: string
  choices?: Array<{
    delta?: { content?: string; reasoning_content?: string }
    message?: { content?: string }
    finish_reason?: string | null
  }>
  usage?: { prompt_tokens?: number; completion_tokens?: number }
  error?: { message?: string; type?: string; code?: string }
}

function usageOf(usage: ChatCompletionChunk['usage']): ProtocolUsage {
  return { inputTokens: usage?.prompt_tokens ?? 0, outputTokens: usage?.completion_tokens ?? 0 }
}

function chatBody(request: ProtocolChatRequest, stream: boolean): Record<string, unknown> {
  const body: Record<string, unknown> = {
    model: request.model,
    messages: request.messages.map((message) => ({ role: message.role, content: message.content }))
  }
  if (request.temperature !== undefined) body.temperature = request.temperature
  if (request.maxOutputTokens > 0) body.max_tokens = request.maxOutputTokens
  if (request.responseFormat !== undefined) {
    body.response_format = {
      type: 'json_schema',
      json_schema: {
        name: request.responseFormat.name,
        schema: request.responseFormat.jsonSchema,
        strict: false
      }
    }
  }
  if (stream) body.stream = true
  return body
}

function responsesBody(request: ProtocolChatRequest, stream: boolean): Record<string, unknown> {
  const body: Record<string, unknown> = {
    model: request.model,
    input: request.messages.map((message) => ({ role: message.role, content: message.content }))
  }
  if (request.temperature !== undefined) body.temperature = request.temperature
  if (request.maxOutputTokens > 0) body.max_output_tokens = request.maxOutputTokens
  if (request.responseFormat !== undefined) {
    body.text = {
      format: {
        type: 'json_schema',
        name: request.responseFormat.name,
        schema: request.responseFormat.jsonSchema
      }
    }
  }
  if (stream) body.stream = true
  return body
}

interface ResponsesResult {
  id?: string
  model?: string
  status?: string
  output_text?: string
  incomplete_details?: { reason?: string }
  output?: Array<{
    type?: string
    content?: Array<{ type?: string; text?: string }>
  }>
  usage?: { input_tokens?: number; output_tokens?: number }
  error?: { code?: string; message?: string } | null
}

function responsesText(result: ResponsesResult): string {
  if (typeof result.output_text === 'string' && result.output_text !== '') return result.output_text
  const parts: string[] = []
  for (const item of result.output ?? []) {
    if (item.type !== 'message') continue
    for (const content of item.content ?? []) {
      if (content.type === 'output_text' && typeof content.text === 'string') parts.push(content.text)
    }
  }
  return parts.join('')
}

function responsesFinish(result: ResponsesResult | null, fallback: string | null): string | null {
  if (result === null) return fallback
  if (result.status === 'incomplete') {
    const reason = result.incomplete_details?.reason
    return reason === 'max_output_tokens' || reason === undefined ? 'length' : reason
  }
  return result.status ?? fallback
}

class OpenAiChatAdapter implements ProtocolAdapter {
  readonly protocol = 'openai_chat' as const
  private readonly http: HttpTransport

  constructor(deps: AdapterDependencies) {
    this.http = new HttpTransport(deps)
  }

  async listModels(signal?: AbortSignal): Promise<UpstreamModel[]> {
    const body = await this.http.json<{
      data?: Array<{ id?: string; owned_by?: string }>
    }>('GET', '/models', undefined, signal)
    return (body.data ?? [])
      .filter((model): model is { id: string } => typeof model.id === 'string' && model.id !== '')
      .map((model) => ({
        modelId: model.id,
        displayName: model.id,
        capabilities: ['chat', 'embedding']
      }))
  }

  async chat(request: ProtocolChatRequest, signal?: AbortSignal): Promise<ProtocolChatResponse> {
    if (request.onDelta === undefined) {
      const body = await this.http.json<ChatCompletionChunk>(
        'POST',
        '/chat/completions',
        chatBody(request, false),
        signal
      )
      if (body.error !== undefined && body.error !== null) {
        throw this.http.error('server_error', errorText(body.error))
      }
      const choice = body.choices?.[0]
      if (choice === undefined) {
        throw this.http.error('server_error', 'chat response carried no choices')
      }
      return {
        content: choice.message?.content ?? '',
        model: body.model ?? request.model,
        usage: usageOf(body.usage),
        finishReason: choice.finish_reason ?? null,
        requestId: body.id ?? null,
        streamed: false
      }
    }

    let content = ''
    let model = request.model
    let requestId: string | null = null
    let finishReason: string | null = null
    let usage: ProtocolUsage = { inputTokens: 0, outputTokens: 0 }
    let sawChoice = false
    let terminal = false

    await this.http.sse(
      '/chat/completions',
      chatBody(request, true),
      (event) => {
        if (event.data.trim() === '[DONE]') {
          terminal = true
          return
        }
        let chunk: ChatCompletionChunk
        try {
          chunk = JSON.parse(event.data) as ChatCompletionChunk
        } catch {
          throw this.http.error('server_error', `decode stream chunk: ${event.data.slice(0, 200)}`)
        }
        if (chunk.error !== undefined && chunk.error !== null) {
          throw this.http.error('server_error', errorText(chunk.error))
        }
        if (typeof chunk.id === 'string') requestId = chunk.id
        if (typeof chunk.model === 'string' && chunk.model !== '') model = chunk.model
        if (chunk.usage !== undefined) usage = usageOf(chunk.usage)
        if (chunk.choices === undefined || chunk.choices.length === 0) return
        sawChoice = true
        const choice = chunk.choices[0]!
        if (typeof choice.finish_reason === 'string' && choice.finish_reason !== '') {
          finishReason = choice.finish_reason
          terminal = true
        }
        const delta = choice.delta?.content
        if (typeof delta === 'string' && delta !== '') {
          content += delta
          request.onDelta?.(delta)
        }
      },
      signal
    )
    if (!sawChoice) throw this.http.error('server_error', 'chat stream carried no choices')
    if (!terminal) throw this.http.error('server_error', 'chat stream ended without a finish event')
    return {
      content,
      model,
      usage,
      finishReason,
      requestId,
      streamed: true
    }
  }

  async embed(request: ProtocolEmbedRequest, signal?: AbortSignal): Promise<ProtocolEmbedResponse> {
    return embedOpenAi(this.http, request, signal)
  }
}

class OpenAiResponsesAdapter implements ProtocolAdapter {
  readonly protocol = 'openai_responses' as const
  private readonly http: HttpTransport

  constructor(deps: AdapterDependencies) {
    this.http = new HttpTransport(deps)
  }

  async listModels(signal?: AbortSignal): Promise<UpstreamModel[]> {
    const body = await this.http.json<{ data?: Array<{ id?: string }> }>(
      'GET',
      '/models',
      undefined,
      signal
    )
    return (body.data ?? [])
      .filter((model): model is { id: string } => typeof model.id === 'string' && model.id !== '')
      .map((model) => ({
        modelId: model.id,
        displayName: model.id,
        capabilities: ['chat', 'embedding']
      }))
  }

  async chat(request: ProtocolChatRequest, signal?: AbortSignal): Promise<ProtocolChatResponse> {
    if (request.onDelta === undefined) {
      const result = await this.http.json<ResponsesResult>(
        'POST',
        '/responses',
        responsesBody(request, false),
        signal
      )
      if (result.error !== undefined && result.error !== null) {
        throw this.http.error('server_error', errorText(result.error))
      }
      return {
        content: responsesText(result),
        model: result.model ?? request.model,
        usage: {
          inputTokens: result.usage?.input_tokens ?? 0,
          outputTokens: result.usage?.output_tokens ?? 0
        },
        finishReason: responsesFinish(result, null),
        requestId: result.id ?? null,
        streamed: false
      }
    }

    let content = ''
    let final: ResponsesResult | null = null
    let terminal = false

    await this.http.sse(
      '/responses',
      responsesBody(request, true),
      (event) => {
        const trimmed = event.data.trim()
        if (trimmed === '' || trimmed === '[DONE]') return
        let payload: {
          type?: string
          delta?: string
          code?: string
          message?: string
          response?: ResponsesResult
        }
        try {
          payload = JSON.parse(trimmed) as typeof payload
        } catch {
          throw this.http.error('server_error', `decode stream event: ${trimmed.slice(0, 200)}`)
        }
        const name = event.name !== '' ? event.name : (payload.type ?? '')
        switch (name) {
          case 'response.output_text.delta':
            if (typeof payload.delta === 'string' && payload.delta !== '') {
              content += payload.delta
              request.onDelta?.(payload.delta)
            }
            break
          case 'response.completed':
          case 'response.incomplete': {
            if (payload.response === undefined) {
              throw this.http.error('server_error', `${name} carried no response object`)
            }
            final = payload.response
            terminal = true
            break
          }
          case 'response.failed':
            if (payload.response?.error !== undefined && payload.response.error !== null) {
              throw this.http.error('server_error', errorText(payload.response.error))
            }
            throw this.http.error('server_error', 'response failed without an error object')
          case 'error':
            throw this.http.error(
              'server_error',
              [payload.code, payload.message].filter((part) => typeof part === 'string').join(': ')
            )
          default:
            break
        }
      },
      signal
    )
    if (!terminal) {
      throw this.http.error('server_error', 'response stream ended without a terminal event')
    }
    const settled = final as ResponsesResult | null
    if (settled !== null && settled.error !== undefined && settled.error !== null) {
      throw this.http.error('server_error', errorText(settled.error))
    }
    const text = settled !== null && responsesText(settled) !== '' ? responsesText(settled) : content
    return {
      content: text,
      model: settled?.model ?? request.model,
      usage: {
        inputTokens: settled?.usage?.input_tokens ?? 0,
        outputTokens: settled?.usage?.output_tokens ?? 0
      },
      finishReason: responsesFinish(settled, null),
      requestId: settled?.id ?? null,
      streamed: true
    }
  }

  async embed(request: ProtocolEmbedRequest, signal?: AbortSignal): Promise<ProtocolEmbedResponse> {
    return embedOpenAi(this.http, request, signal)
  }
}

function errorText(error: { message?: string; type?: string; code?: string }): string {
  return [error.code, error.type, error.message]
    .filter((part): part is string => typeof part === 'string' && part !== '')
    .join(': ')
}

async function embedOpenAi(
  http: HttpTransport,
  request: ProtocolEmbedRequest,
  signal?: AbortSignal
): Promise<ProtocolEmbedResponse> {
  const body = await http.json<{
    model?: string
    data?: Array<{ index?: number; embedding?: number[] }>
    usage?: { prompt_tokens?: number }
  }>('POST', '/embeddings', { model: request.model, input: request.inputs }, signal)
  const vectors: number[][] = new Array(request.inputs.length)
  for (const entry of body.data ?? []) {
    const index = entry.index ?? 0
    if (index >= 0 && index < vectors.length && Array.isArray(entry.embedding)) {
      vectors[index] = entry.embedding
    }
  }
  if (vectors.some((vector) => vector === undefined)) {
    throw http.error('server_error', 'embeddings response missing an input slot')
  }
  return {
    vectors,
    model: body.model ?? request.model,
    usage: { inputTokens: body.usage?.prompt_tokens ?? 0, outputTokens: 0 }
  }
}

export function createOpenAiChatAdapter(deps: AdapterDependencies): ProtocolAdapter {
  return new OpenAiChatAdapter(deps)
}

export function createOpenAiResponsesAdapter(deps: AdapterDependencies): ProtocolAdapter {
  return new OpenAiResponsesAdapter(deps)
}