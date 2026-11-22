import * as path from 'node:path'

import type { ExclusionReason } from '@researchos/shared'

import { invalidInput } from './errors'

/** Threshold defaults of the experiments spec (calibratable, not requirements). */
export const DEFAULT_MAX_FILE_BYTES = 512 * 1024
export const DEFAULT_MAX_TOTAL_BYTES = 4 * 1024 * 1024
export const DEFAULT_MAX_FILES = 20_000
export const DEFAULT_HASH_MAX_BYTES = 64 * 1024 * 1024

export interface EffectiveScanRules {
  v: number
  include: string[]
  exclude: string[]
  maxFileBytes: number
  maxTotalBytes: number
  maxFiles: number
  hashMaxBytes: number
  followSymlinks: boolean
  includeImagesInAi: boolean
}

export interface ScanRulesInput {
  include?: string[]
  exclude?: string[]
  maxFileBytes?: number
  maxTotalBytes?: number
  maxFiles?: number
  hashMaxBytes?: number
  followSymlinks?: boolean
  includeImagesInAi?: boolean
}

export function defaultExclusions(): string[] {
  return [
    '.git/',
    '.hg/',
    '.svn/',
    'node_modules/',
    '.venv/',
    'venv/',
    '__pycache__/',
    '.mypy_cache/',
    '.pytest_cache/',
    '.ipynb_checkpoints/',
    'wandb/',
    'mlruns/',
    '.cache/',
    'checkpoints/'
  ]
}

export function defaultScanRules(): EffectiveScanRules {
  return {
    v: 1,
    include: ['**/*'],
    exclude: defaultExclusions(),
    maxFileBytes: DEFAULT_MAX_FILE_BYTES,
    maxTotalBytes: DEFAULT_MAX_TOTAL_BYTES,
    maxFiles: DEFAULT_MAX_FILES,
    hashMaxBytes: DEFAULT_HASH_MAX_BYTES,
    followSymlinks: false,
    includeImagesInAi: false
  }
}

/** Fills every unset field with the §3.2 default; explicit values win. */
export function normalizeScanRules(input?: ScanRulesInput | null): EffectiveScanRules {
  const defaults = defaultScanRules()
  if (input === undefined || input === null) return defaults
  return {
    v: 1,
    include: input.include === undefined ? defaults.include : [...input.include],
    exclude: input.exclude === undefined ? defaults.exclude : [...input.exclude],
    maxFileBytes:
      input.maxFileBytes !== undefined && input.maxFileBytes > 0 ? input.maxFileBytes : defaults.maxFileBytes,
    maxTotalBytes:
      input.maxTotalBytes !== undefined && input.maxTotalBytes > 0
        ? input.maxTotalBytes
        : defaults.maxTotalBytes,
    maxFiles: input.maxFiles !== undefined && input.maxFiles > 0 ? input.maxFiles : defaults.maxFiles,
    hashMaxBytes:
      input.hashMaxBytes !== undefined && input.hashMaxBytes > 0 ? input.hashMaxBytes : defaults.hashMaxBytes,
    followSymlinks: input.followSymlinks ?? defaults.followSymlinks,
    includeImagesInAi: input.includeImagesInAi ?? defaults.includeImagesInAi
  }
}

/** Overlays only the supplied fields of `override` on `base`. */
export function mergeScanRules(base: EffectiveScanRules, override?: ScanRulesInput | null): EffectiveScanRules {
  if (override === undefined || override === null) return { ...base }
  const out: EffectiveScanRules = {
    ...base,
    include: [...base.include],
    exclude: [...base.exclude]
  }
  if (override.include !== undefined) out.include = [...override.include]
  if (override.exclude !== undefined) out.exclude = [...override.exclude]
  if (override.maxFileBytes !== undefined && override.maxFileBytes > 0) out.maxFileBytes = override.maxFileBytes
  if (override.maxTotalBytes !== undefined && override.maxTotalBytes > 0) out.maxTotalBytes = override.maxTotalBytes
  if (override.maxFiles !== undefined && override.maxFiles > 0) out.maxFiles = override.maxFiles
  if (override.hashMaxBytes !== undefined && override.hashMaxBytes > 0) out.hashMaxBytes = override.hashMaxBytes
  out.followSymlinks = override.followSymlinks ?? base.followSymlinks
  out.includeImagesInAi = override.includeImagesInAi ?? base.includeImagesInAi
  return out
}

export function validateScanRules(rules: EffectiveScanRules): void {
  for (const pattern of [...rules.include, ...rules.exclude]) {
    if (pattern.trim() === '') throw invalidInput('扫描规则中的 glob 不能为空')
    compileGlob(pattern)
  }
  if (
    rules.maxFileBytes <= 0 ||
    rules.maxTotalBytes <= 0 ||
    rules.maxFiles <= 0 ||
    rules.hashMaxBytes <= 0
  ) {
    throw invalidInput('扫描阈值必须为正数', { reason: 'scan_thresholds' })
  }
  if (rules.maxTotalBytes < rules.maxFileBytes) {
    throw invalidInput('max_total_bytes 必须大于等于 max_file_bytes', { reason: 'scan_thresholds' })
  }
}

/** True when the stored rules payload carries no meaningful field. */
export function isZeroScanRules(input: ScanRulesInput): boolean {
  return (
    input.include === undefined &&
    input.exclude === undefined &&
    input.maxFileBytes === undefined &&
    input.maxTotalBytes === undefined &&
    input.maxFiles === undefined &&
    input.hashMaxBytes === undefined &&
    input.followSymlinks === undefined &&
    input.includeImagesInAi === undefined
  )
}

/* -------------------------------------------------------------- glob compile */

export interface CompiledGlob {
  readonly pattern: string
  readonly re: RegExp
  readonly dirOnly: boolean
  readonly baseOnly: boolean
}

/**
 * Compiles one gitignore-style pattern: no negation, `**` spans zero or more
 * segments, `*` and `?` stay inside a segment, `[...]` classes are supported,
 * a trailing `/` means directories only, and a pattern with no `/` matches a
 * basename at any depth.
 */
export function compileGlob(input: string): CompiledGlob {
  let raw = input.trim()
  if (raw === '') throw invalidInput('扫描规则中的 glob 不能为空')
  if (raw.startsWith('!')) {
    throw invalidInput(`不支持取反的 glob：${input}`, { reason: 'negated_glob', pattern: input })
  }
  let dirOnly = false
  if (raw.endsWith('/')) {
    dirOnly = true
    raw = raw.slice(0, -1)
  }
  const anchored = raw.startsWith('/')
  raw = raw.replace(/^\//, '')
  const baseOnly = !anchored && !raw.includes('/')
  const segments = raw.split('/')
  let body = '^'
  if (baseOnly) body += '(?:.*/)?'
  let wroteSegment = false
  let skipSeparator = false
  for (let index = 0; index < segments.length; index += 1) {
    const segment = segments[index]
    if (segment === undefined) continue
    if (segment === '') throw invalidInput(`glob 含空路径段：${input}`, { pattern: input })
    if (segment === '**') {
      body += index === segments.length - 1 ? '.*' : '(?:[^/]+/)*'
      skipSeparator = index !== segments.length - 1
      continue
    }
    if (wroteSegment && !skipSeparator) body += '/'
    body += translateSegment(segment, input)
    wroteSegment = true
    skipSeparator = false
  }
  body += '$'
  let re: RegExp
  try {
    re = new RegExp(body)
  } catch (error) {
    throw invalidInput(`glob 无法编译：${input}`, { pattern: input, cause: String(error) })
  }
  return { pattern: input, re, dirOnly, baseOnly }
}

function translateSegment(segment: string, pattern: string): string {
  let out = ''
  for (let index = 0; index < segment.length; index += 1) {
    const char = segment[index]
    if (char === undefined) continue
    if (char === '*') {
      out += '[^/]*'
      continue
    }
    if (char === '?') {
      out += '[^/]'
      continue
    }
    if (char === '[') {
      const end = segment.indexOf(']', index)
      if (end < 0) throw invalidInput(`glob 字符类未闭合：${pattern}`, { pattern })
      let characterClass = segment.slice(index, end + 1)
      if (characterClass.startsWith('[!')) characterClass = `[^${characterClass.slice(2)}`
      out += characterClass
      index = end
      continue
    }
    out += char.replace(/[\\^$.*+?()[\]{}|]/g, '\\$&')
  }
  return out
}

function matchesGlob(glob: CompiledGlob, rel: string, isDir: boolean): boolean {
  if (glob.dirOnly && !isDir) return false
  let target = rel.startsWith('/') ? rel.slice(1) : rel
  if (isDir) {
    if (glob.dirOnly) {
      if (target.endsWith('/')) target = target.slice(0, -1)
    } else if (!target.endsWith('/')) {
      target += '/'
    }
  }
  if (glob.baseOnly) {
    const name = target.endsWith('/') ? target.slice(0, -1) : target
    return glob.re.test(name)
  }
  return glob.re.test(target)
}

/** Canonical scan-relative path: forward slashes, no leading separator. */
export function normalizeRelPath(rel: string): string {
  const slashed = rel.replaceAll('\\', '/')
  return path.posix.normalize(`/${slashed}`).replace(/^\/+/, '')
}

const CACHE_DIRS: Readonly<Record<string, true>> = {
  '.git': true,
  '.hg': true,
  '.svn': true,
  '.venv': true,
  venv: true,
  __pycache__: true,
  '.mypy_cache': true,
  '.pytest_cache': true,
  '.ipynb_checkpoints': true,
  wandb: true,
  mlruns: true,
  '.cache': true,
  checkpoints: true,
  node_modules: true
}

export function exclusionReasonFor(pattern: string): ExclusionReason {
  const cleaned = pattern.replace(/^\//, '').replace(/\/$/, '')
  return CACHE_DIRS[cleaned] === true ? 'cache' : 'user_exclude'
}

export interface RuleDecision {
  excluded: boolean
  reason?: ExclusionReason
}

/** The compiled rule set of one scan. Excludes always win over includes. */
export class RuleSet {
  readonly rules: EffectiveScanRules
  private readonly include: CompiledGlob[]
  private readonly exclude: CompiledGlob[]

  constructor(rules: EffectiveScanRules) {
    validateScanRules(rules)
    this.rules = rules
    this.include = rules.include.map((pattern) => compileGlob(pattern))
    this.exclude = rules.exclude.map((pattern) => compileGlob(pattern))
  }

  excluded(input: string, isDir: boolean): RuleDecision {
    const rel = normalizeRelPath(input)
    if (rel === '' || rel === '.') return { excluded: false }
    if (isDir) {
      for (const glob of this.exclude) {
        if (matchesGlob(glob, rel, true)) {
          return { excluded: true, reason: exclusionReasonFor(glob.pattern) }
        }
      }
      return { excluded: false }
    }
    const segments = rel.split('/')
    for (let index = 1; index < segments.length; index += 1) {
      const dir = segments.slice(0, index).join('/')
      for (const glob of this.exclude) {
        if (matchesGlob(glob, dir, true)) {
          return { excluded: true, reason: exclusionReasonFor(glob.pattern) }
        }
      }
    }
    for (const glob of this.exclude) {
      if (matchesGlob(glob, rel, false)) {
        return { excluded: true, reason: exclusionReasonFor(glob.pattern) }
      }
    }
    if (this.include.length > 0) {
      for (const glob of this.include) {
        if (matchesGlob(glob, rel, false)) return { excluded: false }
      }
      return { excluded: true, reason: 'user_exclude' }
    }
    return { excluded: false }
  }
}

/** Adds the mandatory `.git/` exclusion if the caller dropped it. */
export function withMandatoryExclusions(rules: EffectiveScanRules): EffectiveScanRules {
  const hasGit = rules.exclude.some((pattern) => pattern.trim().toLowerCase() === '.git/')
  if (hasGit) return rules
  return { ...rules, exclude: [...rules.exclude, '.git/'] }
}

export function forcedExcludedDir(name: string): boolean {
  return name === '.git' || name === '.hg' || name === '.svn'
}