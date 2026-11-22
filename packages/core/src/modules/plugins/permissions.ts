import type { PluginPermission } from '@researchos/shared'
import { isPluginPermission, isPluginWritePermission } from '@researchos/shared'

import { appError, AppErrorCode } from '../../errors'
import { permissionDenied } from './errors'

export const MAX_NETWORK_DOMAINS = 50
export const NETWORK_DOMAIN_PATTERN = /^(?:\*\.)?[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$/

/** Normalizes and validates a network allow-list; throws INVALID_ARGUMENT. */
export function normalizeNetworkDomains(input: readonly string[] | undefined): string[] {
  if (input === undefined) return []
  if (input.length > MAX_NETWORK_DOMAINS) {
    throw appError(AppErrorCode.InvalidArgument, `at most ${MAX_NETWORK_DOMAINS} network domains`, {
      field: 'networkDomains'
    })
  }
  const out: string[] = []
  for (const raw of input) {
    const domain = raw.trim().toLowerCase()
    if (domain === '') {
      throw appError(AppErrorCode.InvalidArgument, 'network domain must not be empty', { field: 'networkDomains' })
    }
    if (domain.includes('/') || domain.includes('://') || domain.includes(':')) {
      throw appError(
        AppErrorCode.InvalidArgument,
        'network domain must be a bare host name, without scheme, port or path',
        { field: 'networkDomains', domain: raw }
      )
    }
    if (!NETWORK_DOMAIN_PATTERN.test(domain)) {
      throw appError(AppErrorCode.InvalidArgument, `invalid network domain ${JSON.stringify(raw)}`, {
        field: 'networkDomains',
        domain: raw
      })
    }
    if (!out.includes(domain)) out.push(domain)
  }
  return out
}

/** Exact host, or `*.example.com` matching one-or-more subdomain labels. */
export function isHostAllowed(host: string, domains: readonly string[]): boolean {
  const target = host.trim().toLowerCase()
  if (target === '') return false
  for (const domain of domains) {
    if (domain === target) return true
    if (domain.startsWith('*.')) {
      const suffix = domain.slice(2)
      if (target.endsWith(`.${suffix}`) && target.length > suffix.length + 1) return true
    }
  }
  return false
}

export function assertPermission(pluginId: string, granted: readonly PluginPermission[], scope: PluginPermission): void {
  if (!granted.includes(scope)) permissionDenied(pluginId, scope)
}

export function missingPermissions(
  declared: readonly PluginPermission[],
  granted: readonly PluginPermission[]
): PluginPermission[] {
  return declared.filter((permission) => !granted.includes(permission))
}

/** Rejects scopes the manifest never declared; keeps the declared order. */
export function normalizeGranted(
  declared: readonly PluginPermission[],
  requested: readonly PluginPermission[]
): PluginPermission[] {
  const out: PluginPermission[] = []
  for (const permission of requested) {
    if (!isPluginPermission(permission)) {
      throw appError(AppErrorCode.InvalidArgument, `unknown permission ${String(permission)}`, {
        field: 'permissions'
      })
    }
    if (!declared.includes(permission)) {
      throw appError(
        AppErrorCode.InvalidArgument,
        `plugin does not declare permission ${permission}`,
        { field: 'permissions', permission }
      )
    }
    if (!out.includes(permission)) out.push(permission)
  }
  return declared.filter((permission) => out.includes(permission))
}

/** Scopes granted now that were not granted before and need a human prompt. */
export function newlyGrantedWriteScopes(
  previous: readonly PluginPermission[],
  next: readonly PluginPermission[]
): PluginPermission[] {
  return next.filter((permission) => isPluginWritePermission(permission) && !previous.includes(permission))
}

/** Contribution scopes: explicit list, else the manifest's whole set. */
export function contributionScopes(
  declared: readonly PluginPermission[],
  explicit: readonly PluginPermission[] | undefined
): PluginPermission[] {
  const scopes = explicit ?? declared
  for (const scope of scopes) {
    if (!declared.includes(scope)) {
      throw appError(
        AppErrorCode.InvalidArgument,
        `contribution requests undeclared permission ${scope}`,
        { field: 'contributes', permission: scope }
      )
    }
  }
  return [...scopes]
}

export function scopesSatisfied(
  required: readonly PluginPermission[],
  granted: readonly PluginPermission[]
): boolean {
  return required.every((scope) => granted.includes(scope))
}