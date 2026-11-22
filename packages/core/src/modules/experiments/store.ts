import { statSync } from 'node:fs'

import type {
  ComparisonMatrix,
  DirReference,
  ExclusionReason,
  FileKind,
  GitInfo,
  JsonObject,
  Metric,
  MetricSet,
  RunParams,
  RunStatus,
  ScanFileEntry,
  ScanRules,
  SnapshotAiAnalysis,
  SnapshotModelInfo,
  SnapshotSummary,
  SnapshotTargetType
} from '@researchos/shared'
import type Database from 'better-sqlite3'

import { formatTimestamp, type Clock } from '../../clock'
import { isValidId, newId } from '../../ids'
import { notFound } from './errors'
import { defaultScanRules, normalizeScanRules, type EffectiveScanRules } from './rules'

export type Db = Database.Database

export const EXPERIMENT_COLUMNS = 'id, title, purpose, dir_ref, scan_rules, created_at, updated_at, deleted_at'
export const RUN_COLUMNS =
  'id, experiment_id, title, dir_ref, params, git_info, status, created_at, updated_at, deleted_at'
export const SNAPSHOT_COLUMNS =
  'id, target_type, target_id, created_at, file_manifest, git_info, metrics, ai_analysis, model_info'

export interface ExperimentRow {
  id: string
  title: string
  purpose: string
  dirRef: string | null
  scanRules: EffectiveScanRules
  createdAt: string
  updatedAt: string
  deletedAt: string | null
}

export interface RunRow {
  id: string
  experimentId: string
  title: string
  dirRef: string | null
  params: RunParams
  gitInfo: GitInfo | null
  status: RunStatus
  createdAt: string
  updatedAt: string
  deletedAt: string | null
}

export interface SnapshotRow {
  id: string
  targetType: SnapshotTargetType
  targetId: string
  createdAt: string
  fileManifest: ScanFileEntry[]
  gitInfo: GitInfo | null
  metrics: MetricSet
  aiAnalysis: SnapshotAiAnalysis | null
  modelInfo: SnapshotModelInfo | null
}

/* ---------------------------------------------------------- scan rules codec */

interface StoredScanRules {
  v?: number
  include?: string[]
  exclude?: string[]
  max_file_bytes?: number
  max_total_bytes?: number
  max_files?: number
  hash_max_bytes?: number
  follow_symlinks?: boolean
  include_images_in_ai?: boolean
}

const KNOWN_SCAN_RULE_KEYS: Readonly<Record<string, true>> = {
  v: true,
  include: true,
  exclude: true,
  max_file_bytes: true,
  max_total_bytes: true,
  max_files: true,
  hash_max_bytes: true,
  follow_symlinks: true,
  include_images_in_ai: true
}

/** Reads a stored scan_rules payload; damaged JSON falls back to the defaults. */
export function decodeScanRules(raw: string): EffectiveScanRules {
  if (raw.trim() === '' || raw.trim() === 'null') return defaultScanRules()
  let stored: StoredScanRules
  try {
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return defaultScanRules()
    stored = parsed as StoredScanRules
  } catch {
    return defaultScanRules()
  }
  return normalizeScanRules({
    include: Array.isArray(stored.include) ? stored.include : undefined,
    exclude: Array.isArray(stored.exclude) ? stored.exclude : undefined,
    maxFileBytes: stored.max_file_bytes,
    maxTotalBytes: stored.max_total_bytes,
    maxFiles: stored.max_files,
    hashMaxBytes: stored.hash_max_bytes,
    followSymlinks: stored.follow_symlinks,
    includeImagesInAi: stored.include_images_in_ai
  })
}

/** Re-encodes the typed rules next to unknown top-level keys (forward compat). */
export function encodeScanRules(rules: EffectiveScanRules, existing?: string): string {
  const typed: JsonObject = {
    v: 1,
    include: rules.include,
    exclude: rules.exclude,
    max_file_bytes: rules.maxFileBytes,
    max_total_bytes: rules.maxTotalBytes,
    max_files: rules.maxFiles,
    hash_max_bytes: rules.hashMaxBytes,
    follow_symlinks: rules.followSymlinks,
    include_images_in_ai: rules.includeImagesInAi
  }
  if (existing !== undefined && existing.trim() !== '') {
    try {
      const parsed: unknown = JSON.parse(existing)
      if (typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)) {
        for (const [key, value] of Object.entries(parsed)) {
          if (KNOWN_SCAN_RULE_KEYS[key] !== true) typed[key] = value
        }
      }
    } catch {
      // A damaged payload is replaced by the typed rules.
    }
  }
  return JSON.stringify(typed)
}

export function scanRulesToJson(rules: EffectiveScanRules): ScanRules {
  return {
    include: [...rules.include],
    exclude: [...rules.exclude],
    maxFileBytes: rules.maxFileBytes,
    maxTotalBytes: rules.maxTotalBytes,
    maxFiles: rules.maxFiles,
    hashMaxBytes: rules.hashMaxBytes,
    followSymlinks: rules.followSymlinks,
    includeImagesInAi: rules.includeImagesInAi
  }
}

/* -------------------------------------------------------------- row loading */

export function loadExperiment(db: Db, id: string, includeDeleted: boolean): ExperimentRow {
  if (!isValidId(id)) throw notFound(`实验不存在：${id}`)
  let sql = `SELECT ${EXPERIMENT_COLUMNS} FROM experiments WHERE id = ?`
  if (!includeDeleted) sql += ' AND deleted_at IS NULL'
  const row = db.prepare(sql).get(id) as
    | {
        id: string
        title: string
        purpose: string
        dir_ref: string | null
        scan_rules: string
        created_at: string
        updated_at: string
        deleted_at: string | null
      }
    | undefined
  if (row === undefined) throw notFound(`实验不存在：${id}`)
  return {
    id: row.id,
    title: row.title,
    purpose: row.purpose,
    dirRef: row.dir_ref,
    scanRules: decodeScanRules(row.scan_rules),
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    deletedAt: row.deleted_at
  }
}

export function loadRun(db: Db, id: string, includeDeleted: boolean): RunRow {
  if (!isValidId(id)) throw notFound(`运行不存在：${id}`)
  let sql = `SELECT ${RUN_COLUMNS} FROM runs WHERE id = ?`
  if (!includeDeleted) sql += ' AND deleted_at IS NULL'
  const row = db.prepare(sql).get(id) as
    | {
        id: string
        experiment_id: string
        title: string
        dir_ref: string | null
        params: string
        git_info: string
        status: string
        created_at: string
        updated_at: string
        deleted_at: string | null
      }
    | undefined
  if (row === undefined) throw notFound(`运行不存在：${id}`)
  return {
    id: row.id,
    experimentId: row.experiment_id,
    title: row.title,
    dirRef: row.dir_ref,
    params: decodeRunParams(row.params),
    gitInfo: decodeGitInfo(row.git_info),
    status: isRunStatus(row.status) ? row.status : 'unknown',
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    deletedAt: row.deleted_at
  }
}

export function isRunStatus(value: string): value is RunStatus {
  return value === 'unknown' || value === 'running' || value === 'finished' || value === 'failed'
}

export function decodeRunParams(raw: string): RunParams {
  if (raw.trim() === '' || raw.trim() === 'null') return {}
  try {
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return {}
    const out: RunParams = {}
    for (const [key, value] of Object.entries(parsed)) {
      if (typeof value === 'object' && value !== null && !Array.isArray(value)) {
        const candidate = value as { value?: unknown; source?: unknown; confirmed?: unknown }
        out[key] = {
          value: typeof candidate.value === 'string' ? candidate.value : String(candidate.value ?? ''),
          ...(candidate.source === undefined ? {} : { source: candidate.source as RunParams[string]['source'] }),
          ...(typeof candidate.confirmed === 'boolean' ? { confirmed: candidate.confirmed } : {})
        }
      } else if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
        out[key] = { value: String(value) }
      }
    }
    return out
  } catch {
    return {}
  }
}

export function encodeRunParams(params: RunParams): string {
  return JSON.stringify(params)
}

/* --------------------------------------------------------------- git codec */

interface StoredGitInfo {
  v?: number
  detected?: boolean
  repo_root?: string
  branch?: string
  commit?: string
  commit_short?: string
  commit_time?: string
  dirty?: boolean
  changed_files?: number
  untracked_files?: number
  captured_at?: string
  source?: string
  truncated?: boolean
}

export function decodeGitInfo(raw: string): GitInfo | null {
  if (raw.trim() === '' || raw.trim() === 'null') return null
  let stored: StoredGitInfo
  try {
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return null
    stored = parsed as StoredGitInfo
  } catch {
    return null
  }
  if (stored.detected !== true) return null
  return {
    detected: true,
    available: true,
    repoRoot: stored.repo_root ?? '',
    branch: stored.branch === undefined || stored.branch === '' ? null : stored.branch,
    commit: stored.commit === undefined || stored.commit === '' ? null : stored.commit,
    commitShort: stored.commit_short === undefined || stored.commit_short === '' ? null : stored.commit_short,
    commitTime: stored.commit_time === undefined || stored.commit_time === '' ? null : stored.commit_time,
    dirty: stored.dirty === true,
    changedFiles: stored.changed_files ?? 0,
    untrackedFiles: stored.untracked_files ?? 0,
    capturedAt: stored.captured_at ?? formatTimestamp(),
    source: stored.source === 'runtime_metadata' ? 'runtime_metadata' : 'read_at_analysis',
    truncated: stored.truncated === true,
    error: null
  }
}

export function encodeGitInfo(info: GitInfo | null): string {
  if (info === null || !info.detected) return JSON.stringify({ v: 1 })
  return JSON.stringify({
    v: 1,
    detected: true,
    repo_root: info.repoRoot,
    branch: info.branch ?? '',
    commit: info.commit ?? '',
    commit_short: info.commitShort ?? '',
    commit_time: info.commitTime ?? '',
    dirty: info.dirty,
    changed_files: info.changedFiles,
    untracked_files: info.untrackedFiles,
    captured_at: info.capturedAt,
    source: info.source,
    truncated: info.truncated
  })
}

/* ----------------------------------------------------------- metrics codec */

const FILE_KIND_VALUES: Readonly<Record<string, true>> = {
  code: true,
  readme: true,
  config: true,
  data: true,
  metrics: true,
  log: true,
  image: true,
  notebook: true,
  binary: true,
  other: true
}

export function decodeFileManifest(raw: string): ScanFileEntry[] {
  if (raw.trim() === '' || raw.trim() === 'null') return []
  try {
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.flatMap((item): ScanFileEntry[] => {
      if (typeof item !== 'object' || item === null || Array.isArray(item)) return []
      const row = item as Record<string, unknown>
      const kind = typeof row['kind'] === 'string' && FILE_KIND_VALUES[row['kind']] === true ? (row['kind'] as FileKind) : 'other'
      return [
        {
          path: typeof row['path'] === 'string' ? row['path'] : '',
          kind,
          size: typeof row['size'] === 'number' ? row['size'] : 0,
          mtime: typeof row['mtime'] === 'string' ? row['mtime'] : '',
          sha256: typeof row['sha256'] === 'string' ? row['sha256'] : null,
          hashSkipped:
            row['hash_skipped'] === 'too_large' || row['hash_skipped'] === 'binary'
              ? row['hash_skipped']
              : null,
          contentEligible: row['content_eligible'] === true,
          exclusionReason:
            typeof row['exclusion_reason'] === 'string'
              ? (row['exclusion_reason'] as ExclusionReason)
              : null,
          sampled: row['sampled'] === true
        }
      ]
    })
  } catch {
    return []
  }
}

export function encodeFileManifest(entries: ScanFileEntry[]): string {
  return JSON.stringify(
    entries.map((entry) => ({
      path: entry.path,
      kind: entry.kind,
      size: entry.size,
      mtime: entry.mtime,
      sha256: entry.sha256,
      hash_skipped: entry.hashSkipped,
      content_eligible: entry.contentEligible,
      exclusion_reason: entry.exclusionReason,
      sampled: entry.sampled
    }))
  )
}

export function decodeMetricSet(raw: string): MetricSet {
  if (raw.trim() === '' || raw.trim() === 'null') return { v: 1, metrics: [], unparsed: [] }
  try {
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      return { v: 1, metrics: [], unparsed: [] }
    }
    const row = parsed as { v?: number; metrics?: unknown; unparsed?: unknown }
    const metrics: Metric[] = Array.isArray(row.metrics)
      ? row.metrics.flatMap((item): Metric[] => {
          if (typeof item !== 'object' || item === null || Array.isArray(item)) return []
          const metric = item as Record<string, unknown>
          const source = metric['source']
          const sourceObject =
            typeof source === 'object' && source !== null && !Array.isArray(source)
              ? (source as Record<string, unknown>)
              : {}
          return [
            {
              key: typeof metric['key'] === 'string' ? metric['key'] : '',
              label: typeof metric['label'] === 'string' ? metric['label'] : '',
              value: typeof metric['value'] === 'number' ? metric['value'] : null,
              raw: typeof metric['raw'] === 'string' ? metric['raw'] : '',
              unit: typeof metric['unit'] === 'string' ? metric['unit'] : null,
              ...(typeof metric['context'] === 'object' && metric['context'] !== null
                ? { context: metric['context'] as JsonObject }
                : {}),
              source: {
                path: typeof sourceObject['path'] === 'string' ? sourceObject['path'] : '',
                pointer: typeof sourceObject['pointer'] === 'string' ? sourceObject['pointer'] : null,
                line: typeof sourceObject['line'] === 'number' ? sourceObject['line'] : null,
                sha256: typeof sourceObject['sha256'] === 'string' ? sourceObject['sha256'] : null
              },
              extractor: typeof metric['extractor'] === 'string' ? metric['extractor'] : ''
            }
          ]
        })
      : []
    const unparsed = Array.isArray(row.unparsed)
      ? row.unparsed.flatMap((item) => {
          if (typeof item !== 'object' || item === null || Array.isArray(item)) return []
          const entry = item as Record<string, unknown>
          return [
            {
              path: typeof entry['path'] === 'string' ? entry['path'] : '',
              reason: typeof entry['reason'] === 'string' ? entry['reason'] : ''
            }
          ]
        })
      : []
    return { v: typeof row.v === 'number' ? row.v : 1, metrics, unparsed }
  } catch {
    return { v: 1, metrics: [], unparsed: [] }
  }
}

export function encodeMetricSet(metrics: MetricSet): string {
  return JSON.stringify(metrics)
}

/* ---------------------------------------------------------------- snapshots */

export function loadSnapshot(db: Db, id: string): SnapshotRow {
  if (!isValidId(id)) throw notFound(`快照不存在：${id}`)
  const row = db.prepare(`SELECT ${SNAPSHOT_COLUMNS} FROM snapshots WHERE id = ?`).get(id) as
    | {
        id: string
        target_type: string
        target_id: string
        created_at: string
        file_manifest: string
        git_info: string
        metrics: string
        ai_analysis: string | null
        model_info: string
      }
    | undefined
  if (row === undefined) throw notFound(`快照不存在：${id}`)
  return {
    id: row.id,
    targetType: row.target_type === 'run' ? 'run' : 'experiment',
    targetId: row.target_id,
    createdAt: row.created_at,
    fileManifest: decodeFileManifest(row.file_manifest),
    gitInfo: decodeGitInfo(row.git_info),
    metrics: decodeMetricSet(row.metrics),
    aiAnalysis: decodeAiAnalysis(row.ai_analysis),
    modelInfo: decodeModelInfo(row.model_info)
  }
}

interface StoredAiAnalysis {
  v?: number
  kind?: string
  ai_output_id?: string
  prompt_version?: string
  content_md?: string
  content_sha256?: string
  sections?: unknown
  unverified_claims?: unknown
  config?: unknown
  warnings?: unknown
}

function decodeAiAnalysis(raw: string | null): SnapshotAiAnalysis | null {
  if (raw === null || raw.trim() === '' || raw.trim() === 'null') return null
  let stored: StoredAiAnalysis
  try {
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return null
    stored = parsed as StoredAiAnalysis
  } catch {
    return null
  }
  const sections = Array.isArray(stored.sections)
    ? stored.sections.flatMap((item) => {
        if (typeof item !== 'object' || item === null || Array.isArray(item)) return []
        const section = item as Record<string, unknown>
        const sources = Array.isArray(section['sources'])
          ? section['sources'].flatMap((source) => {
              if (typeof source !== 'object' || source === null || Array.isArray(source)) return []
              const ref = source as Record<string, unknown>
              return [
                {
                  path: typeof ref['path'] === 'string' ? ref['path'] : '',
                  sha256: typeof ref['sha256'] === 'string' ? ref['sha256'] : undefined,
                  metricKeys: Array.isArray(ref['metric_keys'])
                    ? ref['metric_keys'].filter((key): key is string => typeof key === 'string')
                    : undefined
                }
              ]
            })
          : []
        return [
          {
            key: typeof section['key'] === 'string' ? section['key'] : '',
            title: typeof section['title'] === 'string' ? section['title'] : '',
            textMd: typeof section['text_md'] === 'string' ? section['text_md'] : '',
            sources,
            unconfirmed: section['unconfirmed'] === true,
            unconfirmedReason:
              typeof section['unconfirmed_reason'] === 'string' ? section['unconfirmed_reason'] : null
          }
        ]
      })
    : []
  const claims = Array.isArray(stored.unverified_claims)
    ? stored.unverified_claims.flatMap((item) => {
        if (typeof item !== 'object' || item === null || Array.isArray(item)) return []
        const claim = item as Record<string, unknown>
        return [
          {
            section: typeof claim['section'] === 'string' ? claim['section'] : '',
            text: typeof claim['text'] === 'string' ? claim['text'] : '',
            reason: typeof claim['reason'] === 'string' ? claim['reason'] : ''
          }
        ]
      })
    : []
  const config =
    typeof stored.config === 'object' && stored.config !== null && !Array.isArray(stored.config)
      ? (stored.config as Record<string, unknown>)
      : {}
  const runParams =
    typeof config['run_params'] === 'object' && config['run_params'] !== null
      ? (config['run_params'] as RunParams)
      : {}
  const storedComparison = config['comparison']
  // The comparison block is written by this module, so its shape is trusted.
  const comparison: ComparisonMatrix | null =
    typeof storedComparison === 'object' &&
    storedComparison !== null &&
    !Array.isArray(storedComparison)
      ? (storedComparison as ComparisonMatrix)
      : null
  return {
    v: typeof stored.v === 'number' ? stored.v : 1,
    kind: stored.kind === 'results' ? 'results' : 'outline',
    aiOutputId: typeof stored.ai_output_id === 'string' ? stored.ai_output_id : '',
    promptVersion: typeof stored.prompt_version === 'string' ? stored.prompt_version : '',
    contentMd: typeof stored.content_md === 'string' ? stored.content_md : '',
    contentSha256: typeof stored.content_sha256 === 'string' ? stored.content_sha256 : '',
    sections,
    unverifiedClaims: claims,
    config: {
      scanRules: (config['scan_rules'] as JsonObject | undefined) ?? {},
      runParams,
      extracted: Array.isArray(config['extracted'])
        ? (config['extracted'] as SnapshotAiAnalysis['config']['extracted'])
        : [],
      comparison
    },
    warnings: Array.isArray(stored.warnings)
      ? stored.warnings.filter((warning): warning is string => typeof warning === 'string')
      : []
  }
}

function decodeModelInfo(raw: string): SnapshotModelInfo | null {
  if (raw.trim() === '' || raw.trim() === 'null') return null
  try {
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return null
    const stored = parsed as Record<string, unknown>
    if (stored['provider_id'] === undefined && stored['model_id'] === undefined) return null
    return {
      v: typeof stored['v'] === 'number' ? stored['v'] : 1,
      providerId: typeof stored['provider_id'] === 'string' ? stored['provider_id'] : '',
      providerName: typeof stored['provider_name'] === 'string' ? stored['provider_name'] : '',
      protocol: typeof stored['protocol'] === 'string' ? stored['protocol'] : '',
      modelId: typeof stored['model_id'] === 'string' ? stored['model_id'] : '',
      promptVersion: typeof stored['prompt_version'] === 'string' ? stored['prompt_version'] : '',
      parameters:
        typeof stored['parameters'] === 'object' && stored['parameters'] !== null
          ? (stored['parameters'] as JsonObject)
          : {},
      sourceRefs: Array.isArray(stored['source_refs'])
        ? (stored['source_refs'] as SnapshotModelInfo['sourceRefs'])
        : [],
      generatedAt: typeof stored['generated_at'] === 'string' ? stored['generated_at'] : ''
    }
  } catch {
    return null
  }
}

export function appendSnapshot(db: Db, snapshot: SnapshotRow): void {
  if (!isValidId(snapshot.id)) throw new Error(`experiments: invalid snapshot id ${snapshot.id}`)
  const aiAnalysis = snapshot.aiAnalysis === null ? null : JSON.stringify(toStoredAiAnalysis(snapshot.aiAnalysis))
  db.prepare(
    `INSERT INTO snapshots(id, target_type, target_id, file_manifest, git_info, metrics, ai_analysis, ai_output_id, model_info, created_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
  ).run(
    snapshot.id,
    snapshot.targetType,
    snapshot.targetId,
    encodeFileManifest(snapshot.fileManifest),
    encodeGitInfo(snapshot.gitInfo),
    encodeMetricSet(snapshot.metrics),
    aiAnalysis,
    snapshot.aiAnalysis?.aiOutputId !== undefined && snapshot.aiAnalysis.aiOutputId !== ''
      ? snapshot.aiAnalysis.aiOutputId
      : null,
    snapshot.modelInfo === null ? JSON.stringify({ v: 1 }) : JSON.stringify(toStoredModelInfo(snapshot.modelInfo)),
    snapshot.createdAt
  )
}

function toStoredAiAnalysis(analysis: SnapshotAiAnalysis): Record<string, unknown> {
  return {
    v: 1,
    kind: analysis.kind,
    ai_output_id: analysis.aiOutputId,
    prompt_version: analysis.promptVersion,
    content_md: analysis.contentMd,
    content_sha256: analysis.contentSha256,
    sections: analysis.sections.map((section) => ({
      key: section.key,
      title: section.title,
      text_md: section.textMd,
      sources: section.sources.map((source) => ({
        path: source.path,
        ...(source.sha256 === undefined ? {} : { sha256: source.sha256 }),
        ...(source.metricKeys === undefined ? {} : { metric_keys: source.metricKeys })
      })),
      unconfirmed: section.unconfirmed,
      unconfirmed_reason: section.unconfirmedReason
    })),
    unverified_claims: analysis.unverifiedClaims,
    config: {
      scan_rules: analysis.config.scanRules,
      run_params: analysis.config.runParams,
      extracted: analysis.config.extracted,
      ...(analysis.config.comparison === null || analysis.config.comparison === undefined
        ? {}
        : { comparison: analysis.config.comparison })
    },
    warnings: analysis.warnings
  }
}

function toStoredModelInfo(info: SnapshotModelInfo): Record<string, unknown> {
  return {
    v: 1,
    provider_id: info.providerId,
    provider_name: info.providerName,
    protocol: info.protocol,
    model_id: info.modelId,
    prompt_version: info.promptVersion,
    parameters: info.parameters,
    source_refs: info.sourceRefs,
    generated_at: info.generatedAt
  }
}

export function snapshotSummaries(db: Db, snapshots: SnapshotRow[]): SnapshotSummary[] {
  return snapshots.map((snapshot) => {
    const ai = snapshot.aiAnalysis
    const aiOutput = ai === null ? undefined : aiOutputState(db, ai.aiOutputId)
    return {
      id: snapshot.id,
      targetType: snapshot.targetType,
      targetId: snapshot.targetId,
      kind: ai === null ? null : ai.kind,
      createdAt: snapshot.createdAt,
      providerName: snapshot.modelInfo?.providerName ?? '',
      modelId: snapshot.modelInfo?.modelId ?? '',
      gitCommit: snapshot.gitInfo?.commitShort ?? '',
      metricCount: snapshot.metrics.metrics.length,
      warnings: ai?.warnings.length ?? 0,
      aiStatus: aiOutput?.status ?? '',
      humanEdited: aiOutput?.humanEdited ?? false,
      confirmedAt: aiOutput?.confirmedAt ?? null
    }
  })
}

function aiOutputState(
  db: Db,
  aiOutputId: string
): { status: string; humanEdited: boolean; confirmedAt: string | null } | undefined {
  if (aiOutputId === '') return undefined
  const table = db
    .prepare("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'ai_outputs'")
    .get() as { name: string } | undefined
  if (table === undefined) return undefined
  const row = db
    .prepare('SELECT status, human_edited, confirmed_at FROM ai_outputs WHERE id = ?')
    .get(aiOutputId) as { status: string; human_edited: number; confirmed_at: string | null } | undefined
  if (row === undefined) return undefined
  return { status: row.status, humanEdited: row.human_edited !== 0, confirmedAt: row.confirmed_at }
}

/* ------------------------------------------------------------ external refs */

export interface DirRefRow {
  refId: string
  ownerType: 'experiment' | 'run'
  ownerId: string
  path: string
  status: 'ok' | 'broken'
  lastCheckedAt: string | null
}

export function loadDirRefs(db: Db, ownerType: 'experiment' | 'run', ownerId: string): DirRefRow[] {
  const rows = db
    .prepare(
      `SELECT id, owner_type, owner_id, path, status, last_checked_at
         FROM external_refs WHERE owner_type = ? AND owner_id = ? AND field = 'dir_ref'
         ORDER BY created_at, id`
    )
    .all(ownerType, ownerId) as Array<{
    id: string
    owner_type: string
    owner_id: string
    path: string
    status: string
    last_checked_at: string | null
  }>
  return rows.map((row) => ({
    refId: row.id,
    ownerType: row.owner_type === 'run' ? 'run' : 'experiment',
    ownerId: row.owner_id,
    path: row.path,
    status: row.status === 'broken' ? 'broken' : 'ok',
    lastCheckedAt: row.last_checked_at
  }))
}

/** Registers (or updates) one owner's directory reference. */
export function registerDirRef(
  db: Db,
  clock: Clock,
  ownerType: 'experiment' | 'run',
  ownerId: string,
  dirPath: string
): string {
  const now = formatTimestamp(clock.now())
  const existing = db
    .prepare("SELECT id FROM external_refs WHERE owner_type = ? AND owner_id = ? AND field = 'dir_ref'")
    .get(ownerType, ownerId) as { id: string } | undefined
  if (existing !== undefined) {
    db.prepare(
      'UPDATE external_refs SET path = ?, status = ?, last_checked_at = ?, updated_at = ? WHERE id = ?'
    ).run(dirPath, 'ok', now, now, existing.id)
    return existing.id
  }
  const id = newId(clock)
  db.prepare(
    `INSERT INTO external_refs(id, owner_type, owner_id, field, ref_kind, path, status, last_checked_at, created_at, updated_at)
     VALUES (?, ?, ?, 'dir_ref', 'directory', ?, 'ok', ?, ?, ?)`
  ).run(id, ownerType, ownerId, dirPath, now, now, now)
  return id
}

/** Probes the given reference rows and records the fresh status. */
export function probeDirRefs(db: Db, clock: Clock, refs: DirRefRow[]): DirReference[] {
  const now = formatTimestamp(clock.now())
  const out: DirReference[] = []
  for (const ref of refs) {
    let exists = false
    let reason = ''
    try {
      exists = statSync(ref.path).isDirectory()
      if (!exists) reason = 'not_a_directory'
    } catch (error) {
      reason = (error as NodeJS.ErrnoException).code ?? 'unreadable'
    }
    const status = exists ? 'ok' : 'broken'
    db.prepare('UPDATE external_refs SET status = ?, last_checked_at = ?, updated_at = ? WHERE id = ?').run(
      status,
      now,
      now,
      ref.refId
    )
    out.push({
      refId: ref.refId,
      ownerType: ref.ownerType,
      ownerId: ref.ownerId,
      path: ref.path,
      status,
      exists,
      reason,
      lastCheckedAt: now
    })
  }
  return out
}

export function toDirReference(ref: DirRefRow): DirReference {
  return {
    refId: ref.refId,
    ownerType: ref.ownerType,
    ownerId: ref.ownerId,
    path: ref.path,
    status: ref.status,
    exists: ref.status === 'ok',
    reason: '',
    lastCheckedAt: ref.lastCheckedAt
  }
}

export function dirRefId(db: Db, ownerType: 'experiment' | 'run', ownerId: string): string | undefined {
  const row = db
    .prepare("SELECT id FROM external_refs WHERE owner_type = ? AND owner_id = ? AND field = 'dir_ref'")
    .get(ownerType, ownerId) as { id: string } | undefined
  return row?.id
}
