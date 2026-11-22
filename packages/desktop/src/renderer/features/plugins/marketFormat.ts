import type { PluginPermission, PluginSourceType } from '@researchos/shared'
import { isPluginWritePermission } from '@researchos/shared'

/** A grant dialog is required only for write scopes and the network scope. */
export function requiresGrant(permissions: readonly PluginPermission[]): boolean {
  return permissions.some(isPluginWritePermission) || permissions.includes('network')
}

/** Checksums are shown shortened; the full value stays in the detail payload. */
export function shortSha(value: string): string {
  return value.slice(0, 12)
}

/** Release assets are sized in MiB with one decimal. */
export function mebibytes(bytes: number): string {
  return (bytes / 1024 / 1024).toFixed(1)
}

/** Short calendar form for registry fetch and release timestamps. */
export function shortDate(value: string | null): string {
  return value === null ? '—' : value.slice(0, 10)
}

export function provenanceKey(source: PluginSourceType): string {
  if (source === 'registry') return 'pages.plugins.source_official'
  if (source === 'github') return 'pages.plugins.source_github'
  return 'pages.plugins.source_local'
}