import * as path from 'node:path'

import type { PluginContributions, PluginManifest, PluginPermission } from '@researchos/shared'
import { PLUGIN_API_VERSION, PLUGIN_PERMISSIONS, isPluginPermission } from '@researchos/shared'
import { z } from 'zod'

import { invalidManifest } from './errors'

/** Hard limits; a manifest is a small declarative document, never a payload. */
export const MAX_MANIFEST_BYTES = 64 * 1024
export const MAX_CONTRIBUTIONS = 200
export const MAX_MAIN_BYTES = 5 * 1024 * 1024
export const MAX_PLUGIN_FILES = 5_000
export const MAX_PLUGIN_BYTES = 64 * 1024 * 1024

export const MANIFEST_FILE = 'manifest.json'

/**
 * Plugin ids are reverse-DNS-ish but must also be usable as a folder name and
 * as the Weekly Review skill namespace. Dots are accepted because the plan's
 * example uses `com.example.lab-helper`; the declarative importer surfaces the
 * Weekly pattern as a warning if a host rejects it.
 */
export const PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,127}$/
export const PLUGIN_VERSION_PATTERN = /^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/
const ENTRY_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._/-]*$/
const CONTRIBUTION_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/
const TITLE_MAX = 200

const permissionSchema = z.custom<PluginPermission>(
  (value) => typeof value === 'string' && isPluginPermission(value),
  { message: 'unknown permission scope' }
)

const contributionId = z.string().regex(CONTRIBUTION_ID_PATTERN, 'must be a stable id')
const contributionTitle = z.string().trim().min(1).max(TITLE_MAX)
const permissionList = z.array(permissionSchema).max(PLUGIN_PERMISSIONS.length)

const commandSchema = z
  .object({
    id: contributionId,
    title: contributionTitle,
    permissions: permissionList.optional()
  })
  .strict()

const sidebarSchema = z
  .object({
    id: contributionId,
    title: contributionTitle,
    icon: z.string().trim().max(64).optional(),
    entry: z.string().regex(ENTRY_PATTERN, 'must be a safe entry name').max(200),
    scope: z.enum(['workspace', 'project']).optional(),
    permissions: permissionList.optional()
  })
  .strict()

const inspectorSchema = z
  .object({
    id: contributionId,
    title: contributionTitle,
    entry: z.string().regex(ENTRY_PATTERN, 'must be a safe entry name').max(200),
    permissions: permissionList.optional()
  })
  .strict()

const editorSchema = z
  .object({
    id: contributionId,
    title: contributionTitle,
    command: contributionId,
    permissions: permissionList.optional()
  })
  .strict()

const contributionsSchema = z
  .object({
    commands: z.array(commandSchema).max(MAX_CONTRIBUTIONS).optional(),
    sidebar: z.array(sidebarSchema).max(MAX_CONTRIBUTIONS).optional(),
    inspector: z.array(inspectorSchema).max(MAX_CONTRIBUTIONS).optional(),
    editor: z.array(editorSchema).max(MAX_CONTRIBUTIONS).optional()
  })
  .strict()

const manifestSchema = z
  .object({
    id: z.string().regex(PLUGIN_ID_PATTERN, 'must be a safe plugin id'),
    name: z.string().trim().min(1).max(120),
    version: z.string().regex(PLUGIN_VERSION_PATTERN, 'must be a semantic version'),
    apiVersion: z.number().int(),
    description: z.string().trim().max(2_000).optional(),
    main: z.string().trim().min(1).max(300),
    renderer: z.string().trim().min(1).max(300).optional(),
    permissions: permissionList,
    contributes: contributionsSchema.optional()
  })
  .strict()

export interface ParsedManifest {
  manifest: PluginManifest
  contributions: PluginContributions
}

/** Parses one manifest document; JSON syntax and schema errors are INVALID_ARGUMENT. */
export function parseManifestDocument(text: string, origin = MANIFEST_FILE): PluginManifest {
  if (Buffer.byteLength(text, 'utf8') > MAX_MANIFEST_BYTES) {
    invalidManifest(`manifest exceeds ${MAX_MANIFEST_BYTES} bytes`, { file: origin })
  }
  let raw: unknown
  try {
    raw = JSON.parse(text) as unknown
  } catch (error) {
    invalidManifest(`manifest is not valid JSON: ${error instanceof Error ? error.message : String(error)}`, {
      file: origin
    })
  }
  const result = manifestSchema.safeParse(raw)
  if (!result.success) {
    const issues = result.error.issues.map((issue) => ({
      path: issue.path.join('.'),
      message: issue.message
    }))
    invalidManifest(`manifest failed validation (${issues[0]?.path || 'root'}: ${issues[0]?.message ?? 'invalid'})`, {
      file: origin,
      issues
    })
  }
  const manifest = result.data as PluginManifest
  validateManifestSemantics(manifest)
  return manifest
}

/** Adds cross-field rules zod cannot express cleanly. */
export function validateManifestSemantics(manifest: PluginManifest): void {
  if (manifest.apiVersion !== PLUGIN_API_VERSION) {
    invalidManifest(`unsupported apiVersion ${manifest.apiVersion}`, {
      apiVersion: manifest.apiVersion,
      supported: PLUGIN_API_VERSION
    })
  }
  if (new Set(manifest.permissions).size !== manifest.permissions.length) {
    invalidManifest('permissions must be unique')
  }
  const files = [manifest.main, manifest.renderer].filter((value): value is string => value !== undefined)
  for (const file of files) {
    assertSafeRelativeFile(file, 'entry')
  }
  for (const permission of manifest.permissions) {
    if (!isPluginPermission(permission)) invalidManifest(`unknown permission ${String(permission)}`)
  }
  validateContributionIds(manifest.contributes)
}

function assertSafeRelativeFile(file: string, field: string): void {
  if (file.includes('\\')) invalidManifest(`${field} must use forward slashes`, { field, file })
  if (path.posix.isAbsolute(file) || /^[A-Za-z]:/.test(file)) {
    invalidManifest(`${field} must be relative`, { field, file })
  }
  const segments = file.split('/')
  if (segments.some((segment) => segment === '' || segment === '.' || segment === '..')) {
    invalidManifest(`${field} must not contain traversal or empty segments`, { field, file })
  }
  const extension = path.posix.extname(file).toLowerCase()
  if (extension !== '.js' && extension !== '.mjs' && extension !== '.cjs') {
    invalidManifest(`${field} must be a JavaScript file`, { field, file })
  }
}

function validateContributionIds(contributions: PluginContributions | undefined): void {
  if (contributions === undefined) return
  const groups: Array<[string, Array<{ id: string }> | undefined]> = [
    ['commands', contributions.commands],
    ['sidebar', contributions.sidebar],
    ['inspector', contributions.inspector],
    ['editor', contributions.editor]
  ]
  const seen = new Set<string>()
  let total = 0
  for (const [group, entries] of groups) {
    for (const entry of entries ?? []) {
      total += 1
      if (seen.has(entry.id)) invalidManifest(`duplicate contribution id ${entry.id}`, { id: entry.id, group })
      seen.add(entry.id)
    }
  }
  if (total > MAX_CONTRIBUTIONS) invalidManifest(`at most ${MAX_CONTRIBUTIONS} contributions are allowed`)
  if (contributions.editor !== undefined) {
    const commandIds = new Set((contributions.commands ?? []).map((command) => command.id))
    for (const editor of contributions.editor) {
      if (!commandIds.has(editor.command)) {
        invalidManifest(`editor contribution ${editor.id} names unknown command ${editor.command}`, {
          id: editor.id,
          command: editor.command
        })
      }
    }
  }
  for (const [group, entries] of [
    ['sidebar', contributions.sidebar],
    ['inspector', contributions.inspector]
  ] as const) {
    for (const entry of entries ?? []) {
      const segments = entry.entry.split('/')
      if (segments.some((segment) => segment === '' || segment === '.' || segment === '..')) {
        invalidManifest(`${group} entry must not contain traversal segments`, {
          id: entry.id,
          entry: entry.entry
        })
      }
    }
  }
}

/** Normalized contributions (always four arrays, never undefined). */
export function normalizedContributions(manifest: PluginManifest): PluginContributions {
  return {
    commands: [...(manifest.contributes?.commands ?? [])],
    sidebar: [...(manifest.contributes?.sidebar ?? [])],
    inspector: [...(manifest.contributes?.inspector ?? [])],
    editor: [...(manifest.contributes?.editor ?? [])]
  }
}

export function isSafePluginId(value: string): boolean {
  return PLUGIN_ID_PATTERN.test(value)
}