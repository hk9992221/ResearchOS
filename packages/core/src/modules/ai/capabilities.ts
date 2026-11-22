import { AppErrorCode } from '@researchos/shared'
import type { AiCapability, AiProtocol } from '@researchos/shared'

import { appError } from '../../errors'

/** The four frozen upstream protocols. */
export const AI_PROTOCOLS = ['openai_chat', 'openai_responses', 'gemini', 'anthropic'] as const

export const DEFAULT_BASE_URLS: Readonly<Record<AiProtocol, string>> = {
  openai_chat: 'https://api.openai.com/v1',
  openai_responses: 'https://api.openai.com/v1',
  gemini: 'https://generativelanguage.googleapis.com/v1beta',
  anthropic: 'https://api.anthropic.com/v1'
}

const CAPABILITIES: Readonly<Record<AiProtocol, readonly AiCapability[]>> = {
  openai_chat: ['chat', 'embedding'],
  openai_responses: ['chat', 'embedding'],
  gemini: ['chat', 'embedding'],
  anthropic: ['chat']
}

export function isAiProtocol(value: unknown): value is AiProtocol {
  return typeof value === 'string' && (AI_PROTOCOLS as readonly string[]).includes(value)
}

/** The single capability source shared by validation, UI filtering and roles. */
export function protocolCapabilities(protocol: AiProtocol): AiCapability[] {
  return [...CAPABILITIES[protocol]]
}

export function protocolSupports(protocol: AiProtocol, capability: AiCapability): boolean {
  return CAPABILITIES[protocol].includes(capability)
}

export function assertProtocol(value: unknown): AiProtocol {
  if (!isAiProtocol(value)) {
    throw appError(AppErrorCode.InvalidArgument, `unknown AI protocol: ${JSON.stringify(value)}`, {
      field: 'protocol'
    })
  }
  return value
}

export function assertCapability(protocol: AiProtocol, capability: AiCapability): void {
  if (!protocolSupports(protocol, capability)) {
    throw appError(
      AppErrorCode.Unsupported,
      `protocol ${protocol} does not support ${capability}`,
      { protocol, capability, kind: 'unsupported_capability' }
    )
  }
}

const LOOPBACK_HOSTS: Readonly<Record<string, true>> = {
  localhost: true,
  '127.0.0.1': true,
  '::1': true,
  '[::1]': true
}

/** True for a keyless local service. */
export function isLoopbackUrl(baseUrl: string): boolean {
  try {
    const url = new URL(baseUrl)
    return LOOPBACK_HOSTS[url.hostname] === true
  } catch {
    return false
  }
}

/**
 * Base-URL rules: http/https only, no userinfo, no query/fragment,
 * no trailing slash, no whitespace. Path (`/v1`) is allowed and meaningful.
 */
export function normalizeBaseUrl(raw: string): string {
  const value = raw.trim()
  if (value === '') {
    throw appError(AppErrorCode.InvalidArgument, 'base URL is required', { field: 'baseUrl' })
  }
  if (/\s/.test(value)) {
    throw appError(AppErrorCode.InvalidArgument, 'base URL must not contain whitespace', {
      field: 'baseUrl'
    })
  }
  let url: URL
  try {
    url = new URL(value)
  } catch {
    throw appError(AppErrorCode.InvalidArgument, 'base URL is not a valid URL', { field: 'baseUrl' })
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw appError(AppErrorCode.InvalidArgument, 'base URL must use http or https', {
      field: 'baseUrl',
      protocol: url.protocol
    })
  }
  if (url.username !== '' || url.password !== '') {
    throw appError(AppErrorCode.InvalidArgument, 'base URL must not contain credentials', {
      field: 'baseUrl'
    })
  }
  if (url.search !== '' || url.hash !== '') {
    throw appError(AppErrorCode.InvalidArgument, 'base URL must not contain a query or fragment', {
      field: 'baseUrl'
    })
  }
  return `${url.origin}${url.pathname}`.replace(/\/+$/, '')
}

/** A provider may be enabled only with a readable key or a keyless loopback base. */
export function assertEnablePolicy(baseUrl: string, hasKey: boolean): void {
  if (hasKey) return
  if (isLoopbackUrl(baseUrl)) return
  throw appError(
    AppErrorCode.InvalidArgument,
    'enabling a remote provider requires an API key',
    { field: 'enabled', reason: 'api_key_required' }
  )
}