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

const BATCH_EMBED_LIMIT = 100

interface GeminiCandidate {
  content?: { parts?: Array<{ text?: string }> }
  finishReason?: string
}

interface GeminiResponse {
  candidates?: GeminiCandidate[]
  usageMetadata?: {
    promptTokenCount?: number
    candidatesTokenCount?: number
  }
  responseId?: string
  error?: { message?: string; status?: string; code?: number }
}

function geminiText(response: GeminiResponse): string {
  const parts = response.candidates?.[0]?.content?.parts ?? []
  return parts.map((part) => part.text ?? '').join('')
}

function geminiUsage(response: GeminiResponse | null): ProtocolUsage {
  return {
    inputTokens: response?.usageMetadata?.promptTokenCount ?? 0,
    outputTokens: response?.usageMetadata?.candidatesTokenCount ?? 0
  }
}

/** Gemini accepts only a JSON-Schema subset; unknown keywords cause a 400. */
function geminiSchema(schema: Record<string, unknown>): Record<string, unknown> {
  const visit = (value: unknown): unknown => {
    if (Array.isArray(value)) return value.map(visit)
    if (value === null || typeof value !== 'object') return value
    const out: Record<string, unknown> = {}
    for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
      if (key === '$schema' || key === '$id' || key === 'additionalProperties') continue
      out[key] = visit(child)
    }
    return out
  }
  return visit(schema) as Record<string, unknown>
}

function generationConfig(request: ProtocolChatRequest): Record<string, unknown> {
  const config: Record<string, unknown> = {}
  if (request.temperature !== undefined) config.temperature = request.temperature
  if (request.maxOutputTokens > 0) config.maxOutputTokens = request.maxOutputTokens
  if (request.responseFormat !== undefined) {
    config.responseMimeType = 'application/json'
    config.responseSchema = geminiSchema(request.responseFormat.jsonSchema)
  }
  return config
}

export class GeminiAdapter implements ProtocolAdapter {
  readonly protocol = 'gemini' as const
  private readonly http: HttpTransport

  constructor(deps: AdapterDependencies) {
    this.http = new HttpTransport(deps)
  }

  async listModels(signal?: AbortSignal): Promise<UpstreamModel[]> {
    const models: UpstreamModel[] = []
    let pageToken = ''
    for (;;) {
      const query = pageToken === '' ? '?pageSize=1000' : `?pageSize=1000&pageToken=${encodeURIComponent(pageToken)}`
      const body = await this.http.json<{
        models?: Array<{
          name?: string
          displayName?: string
          supportedGenerationMethods?: string[]
        }>
        nextPageToken?: string
      }>('GET', `/models${query}`, undefined, signal)
      for (const model of body.models ?? []) {
        const modelId = (model.name ?? '').replace(/^models\//, '')
        if (modelId === '') continue
        const capabilities = geminiCapabilities(model.supportedGenerationMethods)
        models.push({
          modelId,
          displayName: model.displayName ?? modelId,
          capabilities
        })
      }
      if (body.nextPageToken === undefined || body.nextPageToken === '') return models
      pageToken = body.nextPageToken
    }
  }

  async chat(request: ProtocolChatRequest, signal?: AbortSignal): Promise<ProtocolChatResponse> {
    const system: string[] = []
    const contents: Array<{ role: string; parts: Array<{ text: string }> }> = []
    for (const message of request.messages) {
      if (message.role === 'system') {
        system.push(message.content)
      } else {
        contents.push({
          role: message.role === 'assistant' ? 'model' : 'user',
          parts: [{ text: message.content }]
        })
      }
    }
    const body: Record<string, unknown> = { contents }
    if (system.length > 0) {
      body.systemInstruction = { role: 'user', parts: [{ text: system.join('\n\n') }] }
    }
    const config = generationConfig(request)
    if (Object.keys(config).length > 0) body.generationConfig = config
    const model = encodeURIComponent(request.model)

    if (request.onDelta === undefined) {
      const response = await this.http.json<GeminiResponse>(
        'POST',
        `/models/${model}:generateContent`,
        body,
        signal
      )
      if (response.error !== undefined && response.error !== null) {
        throw this.http.error('server_error', response.error.message ?? 'gemini error')
      }
      if ((response.candidates ?? []).length === 0) {
        throw this.http.error('server_error', 'gemini response carried no candidates')
      }
      return {
        content: geminiText(response),
        model: request.model,
        usage: geminiUsage(response),
        finishReason: response.candidates?.[0]?.finishReason ?? null,
        requestId: response.responseId ?? null,
        streamed: false
      }
    }

    let content = ''
    let finishReason: string | null = null
    let usage: ProtocolUsage = { inputTokens: 0, outputTokens: 0 }
    let requestId: string | null = null
    let sawCandidate = false
    let terminal = false
    await this.http.sse(
      `/models/${model}:streamGenerateContent?alt=sse`,
      body,
      (event) => {
        const trimmed = event.data.trim()
        if (trimmed === '' || trimmed === '[DONE]') return
        let chunk: GeminiResponse
        try {
          chunk = JSON.parse(trimmed) as GeminiResponse
        } catch {
          throw this.http.error('server_error', `decode stream chunk: ${trimmed.slice(0, 200)}`)
        }
        if (chunk.error !== undefined && chunk.error !== null) {
          throw this.http.error('server_error', chunk.error.message ?? 'gemini stream error')
        }
        if (chunk.responseId !== undefined && chunk.responseId !== '') requestId = chunk.responseId
        if (chunk.usageMetadata !== undefined) usage = geminiUsage(chunk)
        const candidate = chunk.candidates?.[0]
        if (candidate === undefined) return
        sawCandidate = true
        if (candidate.finishReason !== undefined && candidate.finishReason !== '') {
          finishReason = candidate.finishReason
          terminal = true
        }
        const text = (candidate.content?.parts ?? []).map((part) => part.text ?? '').join('')
        if (text !== '') {
          content += text
          request.onDelta?.(text)
        }
      },
      signal
    )
    if (!sawCandidate) throw this.http.error('server_error', 'gemini stream carried no candidates')
    if (!terminal) throw this.http.error('server_error', 'gemini stream ended without a finish event')
    return { content, model: request.model, usage, finishReason, requestId, streamed: true }
  }

  async embed(request: ProtocolEmbedRequest, signal?: AbortSignal): Promise<ProtocolEmbedResponse> {
    const vectors: number[][] = []
    const model = encodeURIComponent(request.model)
    for (let start = 0; start < request.inputs.length; start += BATCH_EMBED_LIMIT) {
      const batch = request.inputs.slice(start, start + BATCH_EMBED_LIMIT)
      const body = {
        requests: batch.map((text) => ({
          model: `models/${request.model}`,
          content: { parts: [{ text }] }
        }))
      }
      const response = await this.http.json<{
        embeddings?: Array<{ values?: number[] }>
      }>('POST', `/models/${model}:batchEmbedContents`, body, signal)
      const embeddings = response.embeddings ?? []
      if (embeddings.length !== batch.length) {
        throw this.http.error(
          'server_error',
          `batchEmbedContents returned ${embeddings.length} vectors for ${batch.length} inputs`
        )
      }
      for (const entry of embeddings) vectors.push(entry.values ?? [])
    }
    return {
      vectors,
      model: request.model,
      usage: { inputTokens: 0, outputTokens: 0 }
    }
  }
}

function geminiCapabilities(methods: string[] | undefined): Array<'chat' | 'embedding'> {
  if (methods === undefined || methods.length === 0) return ['chat', 'embedding']
  const capabilities: Array<'chat' | 'embedding'> = []
  if (methods.some((method) => method === 'generateContent' || method === 'streamGenerateContent')) {
    capabilities.push('chat')
  }
  if (
    methods.some(
      (method) =>
        method === 'embedContent' || method === 'batchEmbedContents' || method === 'embed'
    )
  ) {
    capabilities.push('embedding')
  }
  return capabilities
}

export function createGeminiAdapter(deps: AdapterDependencies): ProtocolAdapter {
  return new GeminiAdapter(deps)
}