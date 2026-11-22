import type { AiCapability, AiProtocol } from '@researchos/shared'

export interface ProviderConfig {
  id: string
  name: string
  protocol: AiProtocol
  baseUrl: string
  apiKeyRef: string
}

export type SecretKey = string | null

export interface UpstreamModel {
  modelId: string
  displayName: string
  capabilities: AiCapability[]
  raw?: unknown
}

export interface ProtocolChatMessage {
  role: 'system' | 'user' | 'assistant'
  content: string
}

/** Structured-output instruction handed to a protocol mapping. */
export interface ResponseFormat {
  type: 'json_schema'
  name: string
  jsonSchema: Record<string, unknown>
}

export interface ProtocolChatRequest {
  model: string
  messages: ProtocolChatMessage[]
  temperature?: number
  maxOutputTokens: number
  responseFormat?: ResponseFormat
  /** When set the adapter requests SSE and emits assistant-visible deltas. */
  onDelta?: (delta: string) => void
}

export interface ProtocolUsage {
  inputTokens: number
  outputTokens: number
}

export interface ProtocolChatResponse {
  content: string
  model: string
  usage: ProtocolUsage
  finishReason: string | null
  requestId: string | null
  streamed: boolean
}

export interface ProtocolEmbedRequest {
  model: string
  inputs: string[]
}

export interface ProtocolEmbedResponse {
  vectors: number[][]
  model: string
  usage: ProtocolUsage
}

export interface ProtocolAdapter {
  readonly protocol: AiProtocol
  listModels(signal?: AbortSignal): Promise<UpstreamModel[]>
  chat(request: ProtocolChatRequest, signal?: AbortSignal): Promise<ProtocolChatResponse>
  /** Absent for anthropic: the capability matrix has no embedding there. */
  embed?(request: ProtocolEmbedRequest, signal?: AbortSignal): Promise<ProtocolEmbedResponse>
}

export type FetchLike = typeof fetch

export interface AdapterDependencies {
  config: ProviderConfig
  apiKey: SecretKey
  fetchImpl?: FetchLike
}