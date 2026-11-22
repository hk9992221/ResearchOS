import type { AiProtocol } from '@researchos/shared'

export const AI_PROTOCOLS: readonly AiProtocol[] = [
  'openai_chat',
  'openai_responses',
  'anthropic',
  'gemini'
]

export function isAiProtocol(value: string): value is AiProtocol {
  return (AI_PROTOCOLS as readonly string[]).includes(value)
}

export interface ProviderFormValues {
  readonly name: string
  readonly protocol: AiProtocol
  readonly baseUrl: string
  readonly apiKey: string
}

export interface ProviderFormErrors {
  readonly name?: string
  readonly protocol?: string
  readonly baseUrl?: string
}

/**
 * Validates the provider form. The returned strings are catalog keys, so the
 * page localizes them and the test can assert the exact rule that fired.
 */
export function validateProviderForm(values: ProviderFormValues): ProviderFormErrors {
  const errors: { name?: string; protocol?: string; baseUrl?: string } = {}
  if (values.name.trim() === '') errors.name = 'pages.settings.ai.error.name'
  if (!AI_PROTOCOLS.includes(values.protocol)) errors.protocol = 'pages.settings.ai.error.protocol'

  try {
    const url = new URL(values.baseUrl.trim())
    if (url.protocol !== 'http:' && url.protocol !== 'https:') {
      errors.baseUrl = 'pages.settings.ai.error.base_url'
    }
    if (url.username !== '' || url.password !== '') {
      errors.baseUrl = 'pages.settings.ai.error.base_url'
    }
  } catch {
    errors.baseUrl = 'pages.settings.ai.error.base_url'
  }
  return errors
}

export function hasErrors(errors: ProviderFormErrors): boolean {
  return Object.values(errors).some((message) => message !== undefined)
}