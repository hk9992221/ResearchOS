/**
 * Pure view models for the experiments feature: formatting, rule defaults,
 * parameter validation, error-cause extraction and the snapshot diff. Nothing
 * here calls IPC, so the helpers are unit-testable and the components stay
 * about layout.
 *
 * The rule defaults mirror  (`rules.ts` in core). They are copied
 * rather than imported because the renderer never depends on core.
 */

import type { AppErrorShape, RunStatus, ScanRules } from '@researchos/shared'

import type { Translations } from '../../i18n'
import { routeHref, type RouteKind } from '../../routes'

/* --------------------------------------------------------------- defaults */

export const DEFAULT_SCAN_RULES = {
  include: ['**/*'],
  exclude: [
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
  ],
  maxFileBytes: 512 * 1024,
  maxTotalBytes: 4 * 1024 * 1024,
  maxFiles: 20_000,
  hashMaxBytes: 64 * 1024 * 1024,
  includeImagesInAi: false
} as const

export interface EffectiveRules {
  readonly include: readonly string[]
  readonly exclude: readonly string[]
  readonly maxFileBytes: number
  readonly maxTotalBytes: number
  readonly maxFiles: number
  readonly hashMaxBytes: number
  readonly includeImagesInAi: boolean
}

/** effectiveRules fills the unset fields with the documented defaults. */
export function effectiveRules(rules: ScanRules | undefined): EffectiveRules {
  return {
    include: rules?.include ?? DEFAULT_SCAN_RULES.include,
    exclude: rules?.exclude ?? DEFAULT_SCAN_RULES.exclude,
    maxFileBytes:
      rules?.maxFileBytes !== undefined && rules.maxFileBytes > 0
        ? rules.maxFileBytes
        : DEFAULT_SCAN_RULES.maxFileBytes,
    maxTotalBytes:
      rules?.maxTotalBytes !== undefined && rules.maxTotalBytes > 0
        ? rules.maxTotalBytes
        : DEFAULT_SCAN_RULES.maxTotalBytes,
    maxFiles:
      rules?.maxFiles !== undefined && rules.maxFiles > 0
        ? rules.maxFiles
        : DEFAULT_SCAN_RULES.maxFiles,
    hashMaxBytes:
      rules?.hashMaxBytes !== undefined && rules.hashMaxBytes > 0
        ? rules.hashMaxBytes
        : DEFAULT_SCAN_RULES.hashMaxBytes,
    includeImagesInAi: rules?.includeImagesInAi === true
  }
}

/** parsePatternLines turns one-pattern-per-line text into a trimmed list. */
export function parsePatternLines(text: string): string[] {
  return text
    .split(/\r?\n/u)
    .map((line) => line.trim())
    .filter((line) => line !== '')
}

export interface RulesDraft {
  readonly includeText: string
  readonly excludeText: string
  readonly maxFileBytes: string
  readonly maxTotalBytes: string
  readonly maxFiles: string
  readonly hashMaxBytes: string
  readonly includeImagesInAi: boolean
}

export function draftFromRules(rules: ScanRules | undefined): RulesDraft {
  const effective = effectiveRules(rules)
  return {
    includeText: effective.include.join('\n'),
    excludeText: effective.exclude.join('\n'),
    maxFileBytes: String(effective.maxFileBytes),
    maxTotalBytes: String(effective.maxTotalBytes),
    maxFiles: String(effective.maxFiles),
    hashMaxBytes: String(effective.hashMaxBytes),
    includeImagesInAi: effective.includeImagesInAi
  }
}

export interface RulesParseResult {
  readonly rules?: ScanRules
  readonly error?: 'bad_number'
}

function positiveInt(text: string): number | null {
  if (!/^\d+$/u.test(text.trim())) return null
  const value = Number(text.trim())
  return Number.isSafeInteger(value) && value > 0 ? value : null
}

/** parseRulesDraft validates the editor; `bad_number` is the only failure. */
export function parseRulesDraft(draft: RulesDraft): RulesParseResult {
  const maxFileBytes = positiveInt(draft.maxFileBytes)
  const maxTotalBytes = positiveInt(draft.maxTotalBytes)
  const maxFiles = positiveInt(draft.maxFiles)
  const hashMaxBytes = positiveInt(draft.hashMaxBytes)
  if (
    maxFileBytes === null ||
    maxTotalBytes === null ||
    maxFiles === null ||
    hashMaxBytes === null
  ) {
    return { error: 'bad_number' }
  }
  return {
    rules: {
      include: parsePatternLines(draft.includeText),
      // `.git` is forced by the service; keeping it visible in the editor is
      // the honest rule, not a disabled checkbox that pretends otherwise.
      exclude: parsePatternLines(draft.excludeText),
      maxFileBytes,
      maxTotalBytes,
      maxFiles,
      hashMaxBytes,
      includeImagesInAi: draft.includeImagesInAi
    }
  }
}

/* -------------------------------------------------------------- run params */

export interface ParamRow {
  readonly key: string
  readonly value: string
}

export interface ParamParseResult {
  readonly params?: Record<string, { value: string }>
  /** `key_required` / `duplicate_key` — both block the save. */
  readonly error?: 'key_required' | 'duplicate_key'
}

/** paramsToRows keeps insertion-independent order (sorted keys, like the Go UI). */
export function paramsToRows(params: Record<string, { value: string }> | undefined): ParamRow[] {
  const rows = Object.entries(params ?? {})
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, value]) => ({ key, value: value.value }))
  return rows.length === 0 ? [{ key: '', value: '' }] : rows
}

/**
 * parseParamRows rejects an empty key that carries a value and any duplicate
 * key. The service cannot accept an empty key (zod min(1)), and a duplicate
 * would silently overwrite a value in the map.
 */
export function parseParamRows(rows: readonly ParamRow[]): ParamParseResult {
  const params: Record<string, { value: string }> = {}
  const seen = new Set<string>()
  for (const row of rows) {
    const key = row.key.trim()
    if (key === '') {
      if (row.value.trim() !== '') return { error: 'key_required' }
      continue
    }
    if (seen.has(key)) return { error: 'duplicate_key' }
    seen.add(key)
    params[key] = { value: row.value }
  }
  return { params }
}

/* ------------------------------------------------------------------- misc */

export type StatusTone = 'neutral' | 'success' | 'warning' | 'danger' | 'info'

const STATUS_TONE: Readonly<Record<RunStatus, StatusTone>> = {
  unknown: 'neutral',
  running: 'info',
  finished: 'success',
  failed: 'danger'
}

export function runStatusTone(status: RunStatus): StatusTone {
  return STATUS_TONE[status] ?? 'neutral'
}

export function dirTone(status: 'ok' | 'broken' | 'unbound'): StatusTone {
  if (status === 'ok') return 'success'
  if (status === 'broken') return 'danger'
  return 'neutral'
}

export function runStatusKey(status: RunStatus): string {
  switch (status) {
    case 'running':
      return 'pages.experiments.run_running'
    case 'finished':
      return 'pages.experiments.run_finished'
    case 'failed':
      return 'pages.experiments.run_failed'
    default:
      return 'pages.experiments.run_unknown'
  }
}

export function formatTime(iso: string | null | undefined): string {
  if (iso === null || iso === undefined || iso === '') return '—'
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return date.toLocaleString()
}

export type GitState = 'ok' | 'metadata_only' | 'absent'

/**
 * gitState separates "repository read completely", "metadata only, the CLI
 * failed" and "no repository". The UI must not paint the middle one green.
 */
export function gitState(info: { detected: boolean; available: boolean } | null | undefined): GitState {
  if (info === null || info === undefined || !info.detected) return 'absent'
  return info.available ? 'ok' : 'metadata_only'
}

export function formatPercent(pct: number | null | undefined, digits = 2): string {
  if (pct === null || pct === undefined || !Number.isFinite(pct)) return '—'
  const sign = pct >= 0 ? '+' : ''
  return `${sign}${pct.toFixed(digits)}%`
}

export function formatMetricValue(value: number | null | undefined, precision = 4): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return '—'
  return value.toFixed(precision)
}

export function shortSha(sha: string | null | undefined): string {
  if (sha === null || sha === undefined || sha === '') return '—'
  return sha.slice(0, 7)
}

/* ------------------------------------------------------------ error causes */

const KNOWN_CAUSE_CODES = [
  'AI_OUTPUT_TRUNCATED',
  'AI_OUTPUT_INVALID',
  'AI_UNAVAILABLE',
  'SNAPSHOT_PERSIST'
] as const

export type KnownCauseCode = (typeof KNOWN_CAUSE_CODES)[number]

export interface JobFailure {
  readonly code: string | null
  readonly message: string | null
  /** A known cause found in the job error's details chain, if any. */
  readonly cause: KnownCauseCode | null
}

function asShape(value: unknown): AppErrorShape | null {
  if (typeof value !== 'object' || value === null) return null
  const candidate = value as { code?: unknown; message?: unknown; details?: unknown }
  if (typeof candidate.code !== 'string') return null
  return candidate as AppErrorShape
}

/**
 * readJobFailure reads the typed error of a failed job. The job runner keeps
 * the domain error under `details.cause`, so a bare `INTERNAL` wrapper must not
 * hide `AI_OUTPUT_TRUNCATED` from the user.
 */
export function readJobFailure(error: AppErrorShape | undefined): JobFailure {
  if (error === undefined) return { code: null, message: null, cause: null }
  let current: AppErrorShape | null = error
  let cause: KnownCauseCode | null = null
  const seen = new Set<unknown>()
  while (current !== null && !seen.has(current)) {
    seen.add(current)
    const code = current.code
    if ((KNOWN_CAUSE_CODES as readonly string[]).includes(code)) {
      cause = code as KnownCauseCode
      break
    }
    current = asShape(current.details?.['cause'])
  }
  return { code: error.code, message: error.message, cause }
}

/* --------------------------------------------------------- ros references */

export interface RosTarget {
  readonly type: string
  readonly id: string
}

/** parseRosTarget reads `ros://<type>/<uuid>`; anything else is not a reference. */
export function parseRosTarget(href: string): RosTarget | null {
  const match = /^ros:\/\/([a-z_]+)\/([0-9a-fA-F-]{36})$/u.exec(href)
  if (match === null) return null
  return { type: match[1] ?? '', id: match[2] ?? '' }
}

const ROS_ROUTE: Readonly<Record<string, RouteKind>> = {
  note: 'note',
  paper: 'paper',
  idea: 'idea',
  experiment: 'experiment'
}

/** rosHrefFor resolves a reference to a route; null means "no route here". */
export function rosHrefFor(target: RosTarget, projectId: string): string | null {
  const kind = ROS_ROUTE[target.type]
  if (kind === undefined) return null
  return routeHref(kind, { projectId, entityId: target.id }) ?? null
}

/* ------------------------------------------------------- snapshot compare */

/** changedMetricKeys lists the union of numeric keys whose value differs. */
export function changedMetricKeys(
  left: Record<string, number | null>,
  right: Record<string, number | null>
): string[] {
  return [...new Set([...Object.keys(left), ...Object.keys(right)])]
    .filter((key) => (left[key] ?? null) !== (right[key] ?? null))
    .sort()
}

export function metricValueMap(
  metrics: ReadonlyArray<{ key: string; value: number | null }>
): Record<string, number | null> {
  const map: Record<string, number | null> = {}
  for (const metric of metrics) map[metric.key] = metric.value
  return map
}

/** rulesSummaryText is the one-line rule recap used by the overview card. */
export function rulesSummaryText(rules: ScanRules | undefined, t: Translations): string {
  const effective = effectiveRules(rules)
  return t('pages.experiments.rules_summary', {
    include: effective.include.join(', ') || '—',
    exclude: String(effective.exclude.length),
    file: effective.maxFileBytes,
    total: effective.maxTotalBytes,
    files: effective.maxFiles,
    images: t(
      effective.includeImagesInAi ? 'pages.experiments.value_yes' : 'pages.experiments.value_no'
    )
  })
}