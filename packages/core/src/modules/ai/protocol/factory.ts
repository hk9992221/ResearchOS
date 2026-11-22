import type { AiCapability } from '@researchos/shared'

import { protocolCapabilities } from '../capabilities'
import { AiError } from '../errors'
import { createAnthropicAdapter } from './anthropic'
import { createGeminiAdapter } from './gemini'
import { createOpenAiChatAdapter, createOpenAiResponsesAdapter } from './openai'
import type {
  FetchLike,
  ProtocolAdapter,
  ProviderConfig,
  SecretKey,
  UpstreamModel
} from './types'

export interface AdapterFactory {
  create(config: ProviderConfig, apiKey: SecretKey): ProtocolAdapter
}

/** Builds one adapter per provider configuration. */
export class ProtocolFactory implements AdapterFactory {
  private readonly fetchImpl: FetchLike | undefined

  constructor(fetchImpl?: FetchLike) {
    this.fetchImpl = fetchImpl
  }

  create(config: ProviderConfig, apiKey: SecretKey): ProtocolAdapter {
    const deps = {
      config,
      apiKey,
      ...(this.fetchImpl === undefined ? {} : { fetchImpl: this.fetchImpl })
    }
    switch (config.protocol) {
      case 'openai_chat':
        return createOpenAiChatAdapter(deps)
      case 'openai_responses':
        return createOpenAiResponsesAdapter(deps)
      case 'gemini':
        return createGeminiAdapter(deps)
      case 'anthropic':
        return createAnthropicAdapter(deps)
      default:
        throw new AiError('unsupported_capability', `unknown AI protocol: ${config.protocol}`, {
          providerId: config.id,
          providerName: config.name
        })
    }
  }

  /** Low-cost connectivity check used by provider Test and model Sync. */
  async listModels(
    config: ProviderConfig,
    apiKey: SecretKey,
    signal?: AbortSignal
  ): Promise<UpstreamModel[]> {
    return this.create(config, apiKey).listModels(signal)
  }
}

/** Narrows the protocol capability set with upstream metadata (narrow only). */
export function resolveModelCapabilities(
  protocol: ProviderConfig['protocol'],
  upstream: readonly AiCapability[] | undefined
): AiCapability[] {
  const floor = protocolCapabilities(protocol)
  if (upstream === undefined || upstream.length === 0) return floor
  return [...new Set(upstream.filter((capability) => floor.includes(capability)))]
}