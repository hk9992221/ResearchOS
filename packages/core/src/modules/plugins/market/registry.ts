import type { PluginPermission, RegistryDocument, RegistryPluginEntry } from '@researchos/shared'
import {
  PLUGIN_API_VERSION,
  PLUGIN_MARKET_SCHEMA_VERSION,
  isPluginPermission
} from '@researchos/shared'

import { AppErrorCode, appError } from '../../../errors'
import { PLUGIN_ID_PATTERN, isSafePluginId } from '../manifest'
import { NETWORK_DOMAIN_PATTERN } from '../permissions'
import { isSemver } from './semver'

/**
 * `registry.json` validation. The index is third-party input,
 * so every field that feeds a decision — id, version, checksum, permissions,
 * domains, download URL — is checked before the document is cached. Entries
 * that fail are dropped and reported: one broken plugin must not make the
 * whole registry unreadable.
 */

const MAX_REGISTRY_PLUGINS = 2000
const MAX_TAGS_PER_PLUGIN = 16
const MAX_NAME_LENGTH = 120
const MAX_AUTHOR_LENGTH = 120
const MAX_DESCRIPTION_LENGTH = 500
const REPO_PATTERN = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/
const SHA256_PATTERN = /^[0-9a-f]{64}$/
const ISO_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,3})?(?:Z|[+-]\d{2}:\d{2})$/

export interface ParsedRegistry {
  document: RegistryDocument
  /** Human-readable reasons for every entry that was dropped. */
  warnings: string[]
}

function badDocument(message: string, details: Record<string, unknown> = {}): never {
  throw appError(AppErrorCode.InvalidArgument, `registry: ${message}`, {
    reason: 'registry_document',
    ...details
  })
}

function stringField(value: unknown, field: string): string {
  if (typeof value !== 'string' || value.trim() === '') {
    badDocument(`plugin entry is missing ${field}`, { field })
  }
  return value
}

function boundedText(value: unknown, field: string, max: number, fallback: string): string {
  const text = typeof value === 'string' ? value.trim() : fallback
  if (text.length > max) badDocument(`${field} exceeds ${String(max)} characters`, { field })
  return text
}

function optionalString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

/** Remote sources must be https; only a local test server may be plain http. */
export function assertRemoteUrl(raw: string, field: string): string {
  let url: URL
  try {
    url = new URL(raw)
  } catch {
    badDocument(`${field} is not an absolute URL`, { field, value: raw })
  }
  if (url.protocol === 'http:') {
    if (url.hostname !== 'localhost' && url.hostname !== '127.0.0.1' && url.hostname !== '[::1]') {
      badDocument(`${field} must use https`, { field, value: raw })
    }
    return url.toString()
  }
  if (url.protocol !== 'https:') {
    badDocument(`${field} must use https`, { field, value: raw })
  }
  return url.toString()
}

function entryId(value: unknown): string {
  const id = stringField(value, 'id')
  if (!isSafePluginId(id) || !PLUGIN_ID_PATTERN.test(id)) {
    badDocument(`plugin id ${JSON.stringify(id)} is not a safe plugin id`, { field: 'id' })
  }
  return id
}

function permissionsOf(value: unknown, id: string): { permissions: PluginPermission[]; warning: string | null } {
  if (!Array.isArray(value)) badDocument(`plugin ${id} permissions must be an array`, { field: 'permissions' })
  const permissions: PluginPermission[] = []
  for (const item of value) {
    if (typeof item !== 'string') {
      return { permissions: [], warning: `plugin ${id}: permission ${JSON.stringify(item)} is not a string` }
    }
    if (!isPluginPermission(item)) {
      // An unknown scope means the registry is newer than this host: refuse the
      // entry rather than install a plugin whose requests we cannot show.
      return { permissions: [], warning: `plugin ${id}: unknown permission ${item}` }
    }
    if (!permissions.includes(item)) permissions.push(item)
  }
  return { permissions, warning: null }
}

function networkDomainsOf(value: unknown, id: string): string[] {
  if (value === undefined) return []
  if (!Array.isArray(value)) badDocument(`plugin ${id} networkDomains must be an array`, { field: 'networkDomains' })
  const domains: string[] = []
  for (const item of value) {
    if (typeof item !== 'string' || !NETWORK_DOMAIN_PATTERN.test(item.toLowerCase())) {
      badDocument(`plugin ${id} has an invalid network domain`, { field: 'networkDomains', value: item })
    }
    const domain = item.toLowerCase()
    if (!domains.includes(domain)) domains.push(domain)
  }
  return domains
}

function parseEntry(value: unknown): { entry: RegistryPluginEntry | null; warning: string | null } {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    badDocument('registry plugin entry must be an object')
  }
  const raw = value as Record<string, unknown>
  const id = entryId(raw['id'])
  const scopes = permissionsOf(raw['permissions'] ?? [], id)
  if (scopes.warning !== null) return { entry: null, warning: scopes.warning }

  const latestVersion = stringField(raw['latestVersion'], 'latestVersion')
  if (!isSemver(latestVersion)) {
    badDocument(`plugin ${id} latestVersion ${JSON.stringify(latestVersion)} is not semver`, {
      field: 'latestVersion'
    })
  }
  const minApiVersion = raw['minApiVersion']
  if (typeof minApiVersion !== 'number' || !Number.isInteger(minApiVersion) || minApiVersion < 1) {
    badDocument(`plugin ${id} minApiVersion must be a positive integer`, { field: 'minApiVersion' })
  }
  const sha256 = optionalString(raw['sha256']).toLowerCase()
  if (!SHA256_PATTERN.test(sha256)) {
    badDocument(`plugin ${id} sha256 must be 64 hex characters`, { field: 'sha256' })
  }
  const repo = stringField(raw['repo'], 'repo')
  if (!REPO_PATTERN.test(repo)) {
    badDocument(`plugin ${id} repo ${JSON.stringify(repo)} is not owner/name`, { field: 'repo' })
  }
  const downloadUrl = assertRemoteUrl(stringField(raw['downloadUrl'], 'downloadUrl'), 'downloadUrl')
  const readmeUrl = optionalString(raw['readmeUrl'])
  const updatedAt = optionalString(raw['updatedAt'])

  const tags: string[] = []
  if (raw['tags'] !== undefined) {
    if (!Array.isArray(raw['tags'])) badDocument(`plugin ${id} tags must be an array`, { field: 'tags' })
    for (const tag of raw['tags'] as unknown[]) {
      if (typeof tag !== 'string' || tag.trim() === '') {
        badDocument(`plugin ${id} has a non-string tag`, { field: 'tags' })
      }
      const normalized = (tag as string).trim().toLowerCase()
      if (!tags.includes(normalized)) tags.push(normalized)
    }
    if (tags.length > MAX_TAGS_PER_PLUGIN) {
      badDocument(`plugin ${id} declares more than ${String(MAX_TAGS_PER_PLUGIN)} tags`, { field: 'tags' })
    }
  }

  return {
    entry: {
      id,
      name: boundedText(raw['name'], `plugin ${id} name`, MAX_NAME_LENGTH, id),
      author: boundedText(raw['author'], `plugin ${id} author`, MAX_AUTHOR_LENGTH, ''),
      description: boundedText(raw['description'], `plugin ${id} description`, MAX_DESCRIPTION_LENGTH, ''),
      tags,
      repo,
      latestVersion,
      minApiVersion: minApiVersion as number,
      downloadUrl,
      sha256,
      permissions: scopes.permissions,
      networkDomains: networkDomainsOf(raw['networkDomains'], id),
      readmeUrl: readmeUrl === '' ? '' : assertRemoteUrl(readmeUrl, 'readmeUrl'),
      verified: raw['verified'] === true,
      updatedAt: updatedAt === '' ? null : assertIso(updatedAt, id)
    },
    warning: null
  }
}

function assertIso(value: string, id: string): string {
  if (!ISO_PATTERN.test(value)) {
    badDocument(`plugin ${id} updatedAt is not an ISO timestamp`, { field: 'updatedAt' })
  }
  return value
}

/**
 * Validates a cached or freshly fetched index document. A document that claims
 * a newer `schemaVersion` than this host understands is refused outright: the
 * fields it adds are exactly the ones that gate installs.
 */
export function parseRegistryDocument(raw: unknown): ParsedRegistry {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) {
    badDocument('registry document must be an object')
  }
  const document = raw as Record<string, unknown>
  const schemaVersion = document['schemaVersion']
  if (schemaVersion !== PLUGIN_MARKET_SCHEMA_VERSION) {
    badDocument(`unsupported schemaVersion ${String(schemaVersion)}`, {
      field: 'schemaVersion',
      supported: PLUGIN_MARKET_SCHEMA_VERSION
    })
  }
  if (!Array.isArray(document['plugins'])) {
    badDocument('registry document needs a plugins array', { field: 'plugins' })
  }
  const entries = document['plugins'] as unknown[]
  if (entries.length > MAX_REGISTRY_PLUGINS) {
    badDocument(`registry lists more than ${String(MAX_REGISTRY_PLUGINS)} plugins`)
  }
  const updatedAt = optionalString(document['updatedAt'])
  const plugins: RegistryPluginEntry[] = []
  const warnings: string[] = []
  for (const item of entries) {
    const parsed = parseEntry(item)
    if (parsed.entry === null) {
      if (parsed.warning !== null) warnings.push(parsed.warning)
      continue
    }
    if (plugins.some((seen) => seen.id === parsed.entry!.id)) {
      warnings.push(`duplicate id ${parsed.entry.id}`)
      continue
    }
    plugins.push(parsed.entry)
  }
  return {
    document: {
      schemaVersion: PLUGIN_MARKET_SCHEMA_VERSION,
      updatedAt: updatedAt === '' ? null : updatedAt,
      plugins
    },
    warnings
  }
}

/** Entries this host cannot run at all, reported separately from parse noise. */
export function isCompatible(entry: RegistryPluginEntry): boolean {
  return entry.minApiVersion <= PLUGIN_API_VERSION
}
