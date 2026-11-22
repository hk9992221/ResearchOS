import { readdirSync, realpathSync, statSync } from 'node:fs'
import * as path from 'node:path'

import type {
  AnalysisKind,
  ComparisonMatrix,
  ComparisonTable,
  ComparisonTableOptions,
  DirReference,
  ExperimentDetail,
  ExperimentSummary,
  ExperimentView,
  GitInfo,
  IdeaRef,
  JsonObject,
  MetricSet,
  RunDetail,
  RunParams,
  RunStatus,
  RunSummary,
  RunView,
  ScanFileEntry,
  ScanMetadata,
  ScanPlan,
  ScanResult,
  ScanRules,
  SnapshotAiAnalysis,
  SnapshotDetail,
  SnapshotSummary,
  TargetRef
} from '@researchos/shared'
import { AppError, AppErrorCode, isAppError } from '@researchos/shared'

import { formatTimestamp, type Clock, SystemClock } from '../../clock'
import { ConfirmService } from '../../confirm'
import { EventBus } from '../../events'
import { isValidId, newId } from '../../ids'
import type { Logger } from '../../logger'
import { RelationService } from '../../relations'
import type { Actor } from '../../schema'
import { TrashService } from '../../trash'
import type { ProjectHandle } from '../../workspace/handle'
import type { WorkspaceManager } from '../../workspace/index'
import {
  generateAnalysis,
  modelInfoOf,
  requireAiPort,
  type AnalysisKind as AiAnalysisKind,
  type ExperimentsAiPort
} from './ai'
import { buildContext, type ContextBundle } from './context'
import { dirGuard, dirUnavailable, invalidInput, notFound, SnapshotPersistError } from './errors'
import { gitDiff, inspectGit, type GitDiff, type GitDiffOptions } from './git'
import { buildComparisonMatrix, formatComparisonTable, runDisplayName, type MatrixRunInput } from './matrix'
import { extractMetadata } from './metadata'
import { createMetricRegistry, extractMetrics, walkJsonLeaves, type MetricRegistry } from './metrics'
import {
  defaultScanRules,
  isZeroScanRules,
  mergeScanRules,
  normalizeScanRules,
  RuleSet,
  validateScanRules,
  withMandatoryExclusions,
  type EffectiveScanRules
} from './rules'
import { scanDirectory } from './scan'
import {
  appendSnapshot,
  decodeMetricSet,
  decodeRunParams,
  dirRefId,
  encodeGitInfo,
  encodeRunParams,
  encodeScanRules,
  loadDirRefs,
  loadExperiment,
  loadRun,
  loadSnapshot,
  probeDirRefs,
  registerDirRef,
  scanRulesToJson,
  snapshotSummaries,
  type DirRefRow,
  type ExperimentRow,
  type RunRow,
  type SnapshotRow
} from './store'

const MAX_TITLE_LENGTH = 200
const MAX_RUN_TITLE_LENGTH = 200
const DEFAULT_LIST_LIMIT = 100
const MAX_LIST_LIMIT = 500
const ANALYSIS_SNAPSHOT_LIST_LIMIT = 10

export interface ExperimentsIndexHook {
  onEntityChanged(event: {
    experimentId: string
    eventType: 'entity.created' | 'entity.updated' | 'entity.deleted' | 'entity.restored' | 'ai.analysis_completed'
    entityId: string
    summary: string
  }): void
}

export interface ExperimentsServiceOptions {
  workspace: WorkspaceManager
  projectId: string
  clock?: Clock
  logger?: Logger
  /** W3 `modules/ai/index.ts` gateway; absent means AI_UNAVAILABLE. */
  ai?: ExperimentsAiPort
  /** Post-commit search-index hook. */
  indexer?: ExperimentsIndexHook
  /** Plugin rule hook: pass a registry with extra extractors. */
  metrics?: MetricRegistry
}

export interface ExperimentListFilter {
  q?: string
  includeDeleted?: boolean
  limit?: number
  offset?: number
}

export interface CreateExperimentInput {
  title: string
  purpose?: string
  dirPath?: string
  rules?: ScanRules
  actor?: Actor
}

export interface UpdateExperimentInput {
  title?: string
  purpose?: string
  dirPath?: string | null
  rules?: ScanRules
  expectedUpdatedAt?: string
  actor?: Actor
}

export interface CreateRunInput {
  experimentId: string
  title: string
  dirPath?: string | null
  params?: RunParams
  status?: RunStatus
  actor?: Actor
}

export interface UpdateRunInput {
  title?: string
  dirPath?: string | null
  params?: RunParams
  status?: RunStatus
  expectedUpdatedAt?: string
  actor?: Actor
}

export interface CompareQuery {
  experimentId?: string
  runIds?: string[]
  baselineId?: string
  diffParams?: boolean
  directions?: Record<string, 'higher' | 'lower'>
}

export interface AnalysisOptions {
  extraInstructions?: string
  maxContextBytes?: number
  signal?: AbortSignal
}

/**
 * The service layer of one project (rewrite plan §4.6). It is the
 * single entry point shared by IPC and MCP: every write runs in one immediate
 * transaction with its Activity row, every update re-reads the stored row, and
 * AI failures keep their typed cause (defects 1, 4, 10).
 */
export class ExperimentsService {
  private readonly workspace: WorkspaceManager
  readonly projectId: string
  private readonly clockSource: Clock
  private readonly logger: Logger | undefined
  private readonly ai: ExperimentsAiPort | undefined
  private readonly indexer: ExperimentsIndexHook | undefined
  private readonly metrics: MetricRegistry
  private readonly events: EventBus
  private readonly confirm: ConfirmService
  private readonly pending = new Map<string, SnapshotRow>()

  constructor(options: ExperimentsServiceOptions) {
    this.workspace = options.workspace
    this.projectId = options.projectId
    this.clockSource = options.clock ?? new SystemClock()
    this.logger = options.logger
    this.ai = options.ai
    this.indexer = options.indexer
    this.metrics = options.metrics ?? createMetricRegistry()
    this.events = new EventBus({ clock: this.clockSource })
    this.confirm = new ConfirmService({ clock: this.clockSource })
  }

  private handle(): ProjectHandle {
    return this.workspace.project(this.projectId)
  }

  private now(): string {
    return formatTimestamp(this.clockSource.now())
  }

  private assertWritable(): void {
    const row = this.workspace.global.db
      .prepare('SELECT status FROM projects WHERE id = ?')
      .get(this.projectId) as { status: string } | undefined
    if (row === undefined) throw notFound(`项目不存在：${this.projectId}`)
    if (row.status === 'archived') {
      throw new AppError(AppErrorCode.ArchivedReadOnly, '项目已归档，不能写入', {
        project_id: this.projectId
      })
    }
  }

  private notifyIndex(
    experimentId: string,
    eventType: 'entity.created' | 'entity.updated' | 'entity.deleted' | 'entity.restored' | 'ai.analysis_completed',
    summary: string
  ): void {
    if (this.indexer === undefined) return
    try {
      this.indexer.onEntityChanged({ experimentId, eventType, entityId: experimentId, summary })
    } catch (error) {
      this.logger?.warn('experiments: index hook failed', {
        experiment_id: experimentId,
        event: eventType,
        error: error instanceof Error ? error.message : String(error)
      })
    }
  }

  private trashService(handle: ProjectHandle): TrashService {
    return new TrashService(handle, this.events, this.confirm, { clock: this.clockSource })
  }

  private relationsService(handle: ProjectHandle): RelationService {
    return new RelationService(handle, this.events, {
      clock: this.clockSource,
      global: { db: this.workspace.global.db }
    })
  }

  /* --------------------------------------------------------------- experiments */

  listExperiments(filter: ExperimentListFilter = {}): ExperimentSummary[] {
    const handle = this.handle()
    let sql = `SELECT e.id, e.title, e.purpose, e.dir_ref, e.updated_at, e.deleted_at,
        (SELECT COUNT(*) FROM runs r WHERE r.experiment_id = e.id AND r.deleted_at IS NULL) AS run_count,
        (SELECT r.status FROM runs r WHERE r.experiment_id = e.id AND r.deleted_at IS NULL
           ORDER BY r.created_at DESC, r.id DESC LIMIT 1) AS last_run_status,
        (SELECT MAX(s.created_at) FROM snapshots s
           WHERE (s.target_type = 'experiment' AND s.target_id = e.id)
              OR (s.target_type = 'run' AND s.target_id IN
                    (SELECT r2.id FROM runs r2 WHERE r2.experiment_id = e.id))) AS last_analyzed_at,
        (SELECT COUNT(*) FROM entity_relations er
           WHERE er.src_type = 'experiment' AND er.src_id = e.id AND er.relation = 'validates') AS linked_ideas,
        (SELECT x.status FROM external_refs x
           WHERE x.owner_type = 'experiment' AND x.owner_id = e.id AND x.field = 'dir_ref') AS dir_status
      FROM experiments e WHERE 1 = 1`
    const args: unknown[] = []
    if (filter.includeDeleted !== true) sql += ' AND e.deleted_at IS NULL'
    const q = filter.q?.trim() ?? ''
    if (q !== '') {
      sql += " AND e.title LIKE ? ESCAPE '\\'"
      args.push(`%${q.replaceAll('\\', '\\\\').replaceAll('%', '\\%').replaceAll('_', '\\_')}%`)
    }
    const limit =
      filter.limit === undefined || filter.limit <= 0
        ? DEFAULT_LIST_LIMIT
        : Math.min(filter.limit, MAX_LIST_LIMIT)
    const offset = filter.offset !== undefined && filter.offset > 0 ? filter.offset : 0
    sql += ' ORDER BY e.updated_at DESC, e.id DESC LIMIT ? OFFSET ?'
    args.push(limit, offset)
    const rows = handle.db.prepare(sql).all(...args) as Array<{
      id: string
      title: string
      purpose: string
      dir_ref: string | null
      updated_at: string
      deleted_at: string | null
      run_count: number
      last_run_status: string | null
      last_analyzed_at: string | null
      linked_ideas: number
      dir_status: string | null
    }>
    return rows.map((row) => ({
      id: row.id,
      title: row.title,
      purpose: row.purpose,
      dirRef: row.dir_ref,
      dirStatus: row.dir_status === 'ok' ? 'ok' : row.dir_status === 'broken' ? 'broken' : null,
      dirReason: '',
      runCount: row.run_count,
      lastRunStatus:
        row.last_run_status === 'running' ||
        row.last_run_status === 'finished' ||
        row.last_run_status === 'failed'
          ? row.last_run_status
          : 'unknown',
      lastAnalyzedAt: row.last_analyzed_at,
      linkedIdeas: row.linked_ideas,
      updatedAt: row.updated_at,
      deletedAt: row.deleted_at
    }))
  }

  getExperiment(id: string): ExperimentDetail {
    const handle = this.handle()
    const experiment = loadExperiment(handle.db, id, false)
    const dirRefs = loadDirRefs(handle.db, 'experiment', id).map((ref) => ({
      refId: ref.refId,
      ownerType: 'experiment' as const,
      ownerId: ref.ownerId,
      path: ref.path,
      status: ref.status,
      exists: ref.status === 'ok',
      reason: '',
      lastCheckedAt: ref.lastCheckedAt
    }))
    return {
      experiment: this.toExperimentView(experiment),
      dirRefs,
      runs: this.listRuns(id),
      recentSnapshots: this.listSnapshots(
        { kind: 'experiment', id },
        { limit: ANALYSIS_SNAPSHOT_LIST_LIMIT }
      ),
      linkedIdeas: this.listLinkedIdeas(handle, id)
    }
  }

  private listLinkedIdeas(handle: ProjectHandle, experimentId: string): IdeaRef[] {
    const rows = handle.db
      .prepare(
        `SELECT i.id, i.title, i.deleted_at FROM entity_relations er
           JOIN ideas i ON i.id = er.dst_id
          WHERE er.src_type = 'experiment' AND er.src_id = ?
            AND er.relation = 'validates' AND er.dst_type = 'idea'
          ORDER BY er.created_at, er.id`
      )
      .all(experimentId) as Array<{ id: string; title: string; deleted_at: string | null }>
    return rows.map((row) => ({ id: row.id, title: row.title, targetDeleted: row.deleted_at !== null }))
  }

  createExperiment(input: CreateExperimentInput): ExperimentView {
    const title = input.title.trim()
    if (title === '') throw invalidInput('实验标题不能为空')
    if ([...title].length > MAX_TITLE_LENGTH) throw invalidInput('实验标题最多 200 个字符')
    let rules = input.rules === undefined ? defaultScanRules() : normalizeScanRules(input.rules)
    validateScanRules(rules)
    const dirPath =
      input.dirPath === undefined || input.dirPath.trim() === '' ? undefined : this.resolveDir(input.dirPath)
    this.assertWritable()
    const handle = this.handle()
    const actor = input.actor ?? 'user'
    const now = this.now()
    const id = newId(this.clockSource)
    handle.inTx(() => {
      handle.db
        .prepare(
          `INSERT INTO experiments(id, title, purpose, dir_ref, scan_rules, created_at, updated_at)
           VALUES (?, ?, ?, ?, ?, ?, ?)`
        )
        .run(id, title, input.purpose ?? '', dirPath ?? null, encodeScanRules(rules), now, now)
      if (dirPath !== undefined) {
        registerDirRef(handle.db, this.clockSource, 'experiment', id, dirPath)
      }
      this.events.emit(handle, {
        type: 'entity.created',
        entityType: 'experiment',
        entityId: id,
        new: { title },
        summary: `创建实验：${title}`,
        actor
      })
    })
    const created = loadExperiment(handle.db, id, false)
    this.notifyIndex(id, 'entity.created', `创建实验：${created.title}`)
    return this.toExperimentView(created)
  }

  updateExperiment(id: string, input: UpdateExperimentInput): ExperimentView {
    const hasTitle = input.title !== undefined
    const hasPurpose = input.purpose !== undefined
    const hasRules = input.rules !== undefined
    const hasDir = input.dirPath !== undefined
    if (!hasTitle && !hasPurpose && !hasRules && !hasDir) {
      throw invalidInput('至少提供 title、purpose、scan_rules 或 dir_ref')
    }
    const title = input.title?.trim()
    if (hasTitle && (title === undefined || title === '')) throw invalidInput('实验标题不能为空')
    if (title !== undefined && [...title].length > MAX_TITLE_LENGTH) {
      throw invalidInput('实验标题最多 200 个字符')
    }
    const rules = hasRules && input.rules !== undefined ? normalizeScanRules(input.rules) : undefined
    if (rules !== undefined) validateScanRules(rules)
    const dirPath =
      input.dirPath === undefined || input.dirPath === null || input.dirPath.trim() === ''
        ? input.dirPath === null
          ? null
          : undefined
        : this.resolveDir(input.dirPath)
    this.assertWritable()
    const handle = this.handle()
    handle.inTx(() => {
      const current = loadExperiment(handle.db, id, false)
      if (input.expectedUpdatedAt !== undefined && current.updatedAt !== input.expectedUpdatedAt) {
        throw new AppError(AppErrorCode.Conflict, '实验已被修改，请刷新后重试', {
          current_updated_at: current.updatedAt,
          expected_updated_at: input.expectedUpdatedAt
        })
      }
      const now = this.now()
      if (title !== undefined) {
        handle.db.prepare('UPDATE experiments SET title = ? WHERE id = ?').run(title, id)
      }
      if (input.purpose !== undefined) {
        handle.db.prepare('UPDATE experiments SET purpose = ? WHERE id = ?').run(input.purpose, id)
      }
      if (rules !== undefined) {
        const stored = handle.db
          .prepare('SELECT scan_rules FROM experiments WHERE id = ?')
          .get(id) as { scan_rules: string } | undefined
        handle.db
          .prepare('UPDATE experiments SET scan_rules = ? WHERE id = ?')
          .run(encodeScanRules(rules, stored?.scan_rules), id)
      }
      if (dirPath !== undefined) {
        if (dirPath === null) {
          handle.db.prepare('UPDATE experiments SET dir_ref = NULL WHERE id = ?').run(id)
          handle.db
            .prepare("DELETE FROM external_refs WHERE owner_type = 'experiment' AND owner_id = ? AND field = 'dir_ref'")
            .run(id)
        } else {
          const previous = current.dirRef
          handle.db.prepare('UPDATE experiments SET dir_ref = ? WHERE id = ?').run(dirPath, id)
          registerDirRef(handle.db, this.clockSource, 'experiment', id, dirPath)
          if (previous !== null && previous !== dirPath) {
            this.events.emit(handle, {
              type: 'entity.relocated',
              entityType: 'experiment',
              entityId: id,
              old: { path: previous },
              new: { path: dirPath },
              summary: '重新绑定实验目录',
              actor: input.actor ?? 'user'
            })
          }
        }
      }
      handle.db.prepare('UPDATE experiments SET updated_at = ? WHERE id = ?').run(now, id)
    })
    const fresh = loadExperiment(handle.db, id, false)
    this.notifyIndex(id, 'entity.updated', `更新实验：${fresh.title}`)
    // Defect 10: the response is the freshly read row, scan_rules included.
    return this.toExperimentView(fresh)
  }

  setScanRules(id: string, input: ScanRules): void {
    const rules = withMandatoryExclusions(normalizeScanRules(input))
    validateScanRules(rules)
    this.assertWritable()
    const handle = this.handle()
    handle.inTx(() => {
      const stored = handle.db
        .prepare('SELECT scan_rules FROM experiments WHERE id = ? AND deleted_at IS NULL')
        .get(id) as { scan_rules: string } | undefined
      if (stored === undefined) throw notFound(`实验不存在：${id}`)
      handle.db
        .prepare('UPDATE experiments SET scan_rules = ?, updated_at = ? WHERE id = ?')
        .run(encodeScanRules(rules, stored.scan_rules), this.now(), id)
    })
  }

  deleteExperiment(id: string, actor: Actor = 'user'): void {
    this.assertWritable()
    const handle = this.handle()
    loadExperiment(handle.db, id, true)
    handle.inTx(() => {
      this.trashService(handle).softDelete(handle, { type: 'experiment', id }, actor)
    })
    this.notifyIndex(id, 'entity.deleted', '删除实验')
  }

  restoreExperiment(id: string, actor: Actor = 'user'): void {
    this.assertWritable()
    const handle = this.handle()
    loadExperiment(handle.db, id, true)
    handle.inTx(() => {
      this.trashService(handle).restore(handle, { type: 'experiment', id }, actor)
    })
    this.notifyIndex(id, 'entity.restored', '恢复实验')
  }

  bindDirectory(experimentId: string, dirPath: string, rules?: ScanRules): DirReference {
    const dir = this.resolveDir(dirPath)
    const resolved =
      rules === undefined || isZeroScanRules(rules) ? defaultScanRules() : normalizeScanRules(rules)
    validateScanRules(resolved)
    this.assertWritable()
    const handle = this.handle()
    let refId = ''
    handle.inTx(() => {
      loadExperiment(handle.db, experimentId, false)
      handle.db
        .prepare('UPDATE experiments SET dir_ref = ?, scan_rules = ?, updated_at = ? WHERE id = ?')
        .run(dir, encodeScanRules(resolved), this.now(), experimentId)
      refId = registerDirRef(handle.db, this.clockSource, 'experiment', experimentId, dir)
    })
    const refs = this.probeRefs(handle, loadDirRefs(handle.db, 'experiment', experimentId).filter((ref) => ref.refId === refId))
    const first = refs[0]
    if (first === undefined) throw notFound(`实验目录引用不存在：${experimentId}`)
    return first
  }

  relocateDirectory(experimentId: string, dirPath: string, actor: Actor = 'user'): DirReference {
    const dir = this.resolveDir(dirPath)
    this.assertWritable()
    const handle = this.handle()
    const refId = dirRefId(handle.db, 'experiment', experimentId)
    if (refId === undefined) throw dirUnavailable(dir, '实验尚未绑定目录')
    handle.inTx(() => {
      const current = loadExperiment(handle.db, experimentId, false)
      const previous = current.dirRef ?? ''
      handle.db
        .prepare('UPDATE experiments SET dir_ref = ?, updated_at = ? WHERE id = ?')
        .run(dir, this.now(), experimentId)
      registerDirRef(handle.db, this.clockSource, 'experiment', experimentId, dir)
      this.events.emit(handle, {
        type: 'entity.relocated',
        entityType: 'experiment',
        entityId: experimentId,
        old: { path: previous },
        new: { path: dir },
        summary: '重新绑定实验目录',
        actor
      })
    })
    const refs = this.probeRefs(handle, loadDirRefs(handle.db, 'experiment', experimentId))
    const first = refs[0]
    if (first === undefined) throw notFound(`实验目录引用不存在：${experimentId}`)
    return first
  }

  verifyDirectory(experimentId: string): DirReference[] {
    const handle = this.handle()
    loadExperiment(handle.db, experimentId, false)
    const refs = loadDirRefs(handle.db, 'experiment', experimentId)
    for (const run of this.listRuns(experimentId)) {
      refs.push(...loadDirRefs(handle.db, 'run', run.id))
    }
    return this.probeRefs(handle, refs)
  }

  private probeRefs(handle: ProjectHandle, refs: DirRefRow[]): DirReference[] {
    return probeDirRefs(handle.db, this.clockSource, refs)
  }

  listDirCandidates(experimentId: string): Array<{ path: string; fileCount: number }> {
    const resolved = this.resolveTarget({ kind: 'experiment', id: experimentId })
    const ruleSet = new RuleSet(resolved.rules)
    const entries = readdirSync(resolved.root, { withFileTypes: true })
      .filter((entry) => entry.isDirectory())
      .map((entry) => entry.name)
      .sort()
    const out: Array<{ path: string; fileCount: number }> = []
    for (const name of entries) {
      if (name === '.git' || name === '.hg' || name === '.svn') continue
      if (ruleSet.excluded(`${name}/`, true).excluded) continue
      let count = 0
      const walk = (directory: string, relative: string): void => {
        if (count >= 200) return
        for (const entry of readdirSync(directory, { withFileTypes: true })) {
          if (count >= 200) return
          const child = `${relative}/${entry.name}`
          if (entry.isDirectory()) {
            if (entry.name === '.git') continue
            if (ruleSet.excluded(`${child}/`, true).excluded) continue
            walk(path.join(directory, entry.name), child)
            continue
          }
          if (entry.isFile()) count += 1
        }
      }
      try {
        walk(path.join(resolved.root, name), name)
      } catch {
        count = 0
      }
      out.push({ path: path.join(resolved.root, name), fileCount: count })
    }
    return out
  }

  /* ---------------------------------------------------------------- scanning */

  scanPlan(target: TargetRef, override?: ScanRules, signal?: AbortSignal): ScanPlan {
    const resolved = this.resolveTarget(target, override)
    return scanDirectory({
      target,
      root: resolved.root,
      rules: resolved.rules,
      clock: this.clockSource,
      ...(signal === undefined ? {} : { signal })
    })
  }

  async scan(target: TargetRef, override?: ScanRules, signal?: AbortSignal): Promise<ScanResult> {
    const plan = this.scanPlan(target, override, signal)
    const base: ScanResult = {
      plan,
      metadata: emptyMetadata(),
      configs: [],
      gitInfo: null,
      warnings: []
    }
    const extracted = await extractMetadata(base)
    const result: ScanResult = {
      ...base,
      metadata: extracted.metadata,
      configs: extracted.metadata.configs,
      warnings: [...extracted.warnings]
    }
    try {
      const info = inspectGit(plan.root, this.clockSource)
      if (info.detected) result.gitInfo = info
    } catch (error) {
      result.warnings.push({
        code: 'git_unavailable',
        path: plan.root,
        message: error instanceof Error ? error.message : String(error)
      })
    }
    return result
  }

  async extractMetrics(target: TargetRef, override?: ScanRules, signal?: AbortSignal): Promise<MetricSet> {
    const result = await this.scan(target, override, signal)
    return extractMetrics(result, this.metrics)
  }

  gitInfo(target: TargetRef): GitInfo {
    const resolved = this.resolveTarget(target)
    return inspectGit(resolved.root, this.clockSource)
  }

  gitDiff(target: TargetRef, options: GitDiffOptions = {}): GitDiff {
    const resolved = this.resolveTarget(target)
    return gitDiff(resolved.root, options)
  }

  private resolveTarget(
    target: TargetRef,
    override?: ScanRules
  ): { root: string; rules: EffectiveScanRules; experimentId: string } {
    const handle = this.handle()
    let root: string
    let rules: EffectiveScanRules
    let experimentId: string
    if (target.kind === 'experiment') {
      const experiment = loadExperiment(handle.db, target.id, false)
      if (experiment.dirRef === null || experiment.dirRef.trim() === '') {
        throw dirUnavailable('', `实验 ${experiment.id} 未绑定目录`)
      }
      root = experiment.dirRef
      rules = experiment.scanRules
      experimentId = experiment.id
    } else if (target.kind === 'run') {
      const run = loadRun(handle.db, target.id, false)
      const experiment = loadExperiment(handle.db, run.experimentId, false)
      rules = experiment.scanRules
      experimentId = experiment.id
      if (run.dirRef !== null && run.dirRef.trim() !== '') root = run.dirRef
      else if (experiment.dirRef !== null && experiment.dirRef.trim() !== '') root = experiment.dirRef
      else throw dirUnavailable('', `运行 ${run.id} 未绑定目录`)
    } else {
      throw invalidInput(`无效的扫描目标：${String(target.kind)}`)
    }
    try {
      if (!statSync(root).isDirectory()) throw new Error('not a directory')
    } catch (error) {
      throw dirUnavailable(root, error instanceof Error ? error.message : String(error))
    }
    const merged = mergeScanRules(rules, override)
    validateScanRules(merged)
    return { root, rules: merged, experimentId }
  }

  /** Resolves the effective rules without scanning (used by preview/context). */
  rulesFor(target: TargetRef, override?: ScanRules): EffectiveScanRules {
    return this.resolveTarget(target, override).rules
  }

  /* -------------------------------------------------------------------- runs */

  listRuns(experimentId: string, includeDeleted = false): RunSummary[] {
    const handle = this.handle()
    loadExperiment(handle.db, experimentId, true)
    let sql = `SELECT r.id, r.title, r.dir_ref, r.params, r.status, r.deleted_at,
                 (SELECT MAX(s.created_at) FROM snapshots s
                    WHERE s.target_type = 'run' AND s.target_id = r.id) AS last_analyzed_at
            FROM runs r WHERE r.experiment_id = ?`
    if (!includeDeleted) sql += ' AND r.deleted_at IS NULL'
    sql += ' ORDER BY r.created_at, r.id'
    const rows = handle.db.prepare(sql).all(experimentId) as Array<{
      id: string
      title: string
      dir_ref: string | null
      params: string
      status: string
      deleted_at: string | null
      last_analyzed_at: string | null
    }>
    return rows.map((row) => {
      const params = decodeRunParams(row.params)
      const status = normalizeStatus(row.status)
      return {
        id: row.id,
        title: row.title,
        displayName: runDisplayName({ id: row.id, title: row.title, dirRef: row.dir_ref, params }),
        dirRef: row.dir_ref,
        params,
        paramCount: Object.keys(params).length,
        status,
        lastAnalyzedAt: row.last_analyzed_at,
        deletedAt: row.deleted_at
      }
    })
  }

  getRun(runId: string): RunDetail {
    const handle = this.handle()
    const run = loadRun(handle.db, runId, false)
    return {
      ...this.toRunView(run),
      dirRefs: loadDirRefs(handle.db, 'run', runId).map((ref) => ({
        refId: ref.refId,
        ownerType: 'run' as const,
        ownerId: ref.ownerId,
        path: ref.path,
        status: ref.status,
        exists: ref.status === 'ok',
        reason: '',
        lastCheckedAt: ref.lastCheckedAt
      }))
    }
  }

  createRun(input: CreateRunInput): RunView {
    const title = input.title.trim()
    if (title === '') throw invalidInput('运行标题不能为空')
    if ([...title].length > MAX_RUN_TITLE_LENGTH) throw invalidInput('运行标题最多 200 个字符')
    this.assertWritable()
    const handle = this.handle()
    const experiment = loadExperiment(handle.db, input.experimentId, false)
    const dirPath =
      input.dirPath === undefined || input.dirPath === null || input.dirPath.trim() === ''
        ? null
        : this.resolveDir(input.dirPath)
    const params = normalizeRunParams(input.params)
    const now = this.now()
    const id = newId(this.clockSource)
    let gitInfo: GitInfo | null = null
    if (dirPath !== null) {
      try {
        gitInfo = inspectGit(dirPath, this.clockSource)
      } catch {
        gitInfo = null
      }
    }
    const status = input.status ?? 'unknown'
    handle.inTx(() => {
      handle.db
        .prepare(
          `INSERT INTO runs(id, experiment_id, title, dir_ref, params, git_info, status, created_at, updated_at)
           VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
        )
        .run(
          id,
          experiment.id,
          title,
          dirPath,
          encodeRunParams(params),
          encodeGitInfo(gitInfo),
          status,
          now,
          now
        )
      if (dirPath !== null) registerDirRef(handle.db, this.clockSource, 'run', id, dirPath)
      this.events.emit(handle, {
        type: 'entity.created',
        entityType: 'run',
        entityId: id,
        new: { experiment_id: experiment.id, title },
        summary: `创建运行：${title}`,
        actor: input.actor ?? 'user'
      })
      this.events.emit(handle, {
        type: 'status.changed',
        entityType: 'run',
        entityId: id,
        new: { status },
        summary: `创建运行：${title}`,
        actor: input.actor ?? 'user'
      })
    })
    return this.toRunView(loadRun(handle.db, id, false))
  }

  updateRun(runId: string, input: UpdateRunInput): RunDetail {
    this.assertWritable()
    const handle = this.handle()
    const current = loadRun(handle.db, runId, false)
    if (input.expectedUpdatedAt !== undefined && current.updatedAt !== input.expectedUpdatedAt) {
      throw new AppError(AppErrorCode.Conflict, '运行已被修改，请刷新后重试', {
        current_updated_at: current.updatedAt,
        expected_updated_at: input.expectedUpdatedAt
      })
    }
    const title = input.title?.trim()
    if (title !== undefined) {
      if (title === '') throw invalidInput('运行标题不能为空')
      if ([...title].length > MAX_RUN_TITLE_LENGTH) throw invalidInput('运行标题最多 200 个字符')
    }
    const status = input.status
    if (status !== undefined && !isStatus(status)) throw invalidInput(`无效的运行状态：${String(status)}`)
    const params = input.params === undefined ? undefined : normalizeRunParams(input.params)
    const dirPath =
      input.dirPath === undefined || input.dirPath === null || input.dirPath.trim() === ''
        ? input.dirPath === null
          ? null
          : undefined
        : this.resolveDir(input.dirPath)
    handle.inTx(() => {
      const now = this.now()
      if (title !== undefined) {
        handle.db.prepare('UPDATE runs SET title = ?, updated_at = ? WHERE id = ?').run(title, now, runId)
      }
      if (params !== undefined) {
        handle.db
          .prepare('UPDATE runs SET params = ?, updated_at = ? WHERE id = ?')
          .run(encodeRunParams(params), now, runId)
      }
      if (status !== undefined && status !== current.status) {
        handle.db.prepare('UPDATE runs SET status = ?, updated_at = ? WHERE id = ?').run(status, now, runId)
        this.events.emit(handle, {
          type: 'status.changed',
          entityType: 'run',
          entityId: runId,
          old: { status: current.status },
          new: { status },
          summary: `运行「${current.title || runId.slice(0, 8)}」：${current.status} → ${status}`,
          actor: input.actor ?? 'user'
        })
      }
      if (dirPath !== undefined) {
        if (dirPath === null) {
          handle.db.prepare('UPDATE runs SET dir_ref = NULL, updated_at = ? WHERE id = ?').run(now, runId)
          handle.db
            .prepare("DELETE FROM external_refs WHERE owner_type = 'run' AND owner_id = ? AND field = 'dir_ref'")
            .run(runId)
        } else {
          handle.db.prepare('UPDATE runs SET dir_ref = ?, updated_at = ? WHERE id = ?').run(dirPath, now, runId)
          registerDirRef(handle.db, this.clockSource, 'run', runId, dirPath)
        }
      }
    })
    return this.getRun(runId)
  }

  deleteRun(runId: string, actor: Actor = 'user'): void {
    this.assertWritable()
    const handle = this.handle()
    loadRun(handle.db, runId, true)
    handle.inTx(() => {
      this.trashService(handle).softDelete(handle, { type: 'run', id: runId }, actor)
    })
  }

  restoreRun(runId: string, actor: Actor = 'user'): void {
    this.assertWritable()
    const handle = this.handle()
    loadRun(handle.db, runId, true)
    handle.inTx(() => {
      this.trashService(handle).restore(handle, { type: 'run', id: runId }, actor)
    })
  }

  /* --------------------------------------------------------------- snapshots */

  listSnapshots(
    target: TargetRef,
    filter: { kind?: AnalysisKind; limit?: number; offset?: number } = {}
  ): SnapshotSummary[] {
    const handle = this.handle()
    if (!isValidId(target.id)) throw invalidInput(`无效的快照目标：${target.id}`)
    const limit =
      filter.limit === undefined || filter.limit <= 0 ? DEFAULT_LIST_LIMIT : Math.min(filter.limit, MAX_LIST_LIMIT)
    const offset = filter.offset !== undefined && filter.offset > 0 ? filter.offset : 0
    let sql = `SELECT id, target_type, target_id, created_at, file_manifest, git_info, metrics, ai_analysis, model_info
                 FROM snapshots WHERE target_type = ? AND target_id = ?`
    const args: unknown[] = [target.kind, target.id]
    if (filter.kind !== undefined) {
      sql += " AND CASE WHEN json_valid(ai_analysis) THEN json_extract(ai_analysis, '$.kind') END = ?"
      args.push(filter.kind)
    }
    sql += ' ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?'
    args.push(limit, offset)
    const ids = handle.db.prepare(sql).all(...args) as Array<{ id: string }>
    const snapshots = ids.map((row) => loadSnapshot(handle.db, row.id))
    return snapshotSummaries(handle.db, snapshots)
  }

  getSnapshot(snapshotId: string): SnapshotDetail {
    const handle = this.handle()
    return this.toSnapshotDetail(loadSnapshot(handle.db, snapshotId))
  }

  /**
   * Appends one snapshot for the renderer's "save evidence" action. There is no
   * update path: the table is append-only.
   */
  createSnapshot(input: {
    target: TargetRef
    metrics?: JsonObject
    aiAnalysis?: string
    aiOutputId?: string
    kind?: AnalysisKind
  }): SnapshotDetail {
    this.assertWritable()
    const resolved = this.resolveTarget(input.target)
    const plan = this.scanPlan(input.target)
    const git = inspectGitSafely(resolved.root, this.clockSource)
    const metrics = input.metrics === undefined ? emptyMetrics() : metricSetFromJson(input.metrics)
    const analysis: SnapshotAiAnalysis | null =
      input.aiAnalysis === undefined && input.kind === undefined
        ? null
        : {
            v: 1,
            kind: input.kind ?? 'outline',
            aiOutputId: input.aiOutputId ?? '',
            promptVersion: '',
            contentMd: input.aiAnalysis ?? '',
            contentSha256: '',
            sections: [],
            unverifiedClaims: [],
            config: { scanRules: {}, runParams: {}, extracted: [], comparison: null },
            warnings: []
          }
    const row: SnapshotRow = {
      id: newId(this.clockSource),
      targetType: input.target.kind,
      targetId: input.target.id,
      createdAt: this.now(),
      fileManifest: plan.entries,
      gitInfo: git,
      metrics,
      aiAnalysis: analysis,
      modelInfo: null
    }
    this.persistSnapshot(row)
    return this.toSnapshotDetail(row)
  }

  private persistSnapshot(row: SnapshotRow): void {
    const handle = this.handle()
    handle.inTx(() => {
      appendSnapshot(handle.db, row)
    })
  }

  /* -------------------------------------------------------------- comparison */

  compareMatrix(query: CompareQuery): ComparisonMatrix {
    const handle = this.handle()
    const runs: MatrixRunInput[] = []
    if (query.runIds !== undefined && query.runIds.length > 0) {
      const seen = new Set<string>()
      for (const id of query.runIds) {
        if (id === '' || seen.has(id)) continue
        seen.add(id)
        runs.push(this.matrixRun(handle, loadRun(handle.db, id, false)))
      }
    } else {
      const experimentId = query.experimentId
      if (experimentId === undefined || experimentId === '') {
        throw invalidInput('experiment_id 与 run_ids 至少提供一个')
      }
      loadExperiment(handle.db, experimentId, false)
      for (const summary of this.listRuns(experimentId)) {
        runs.push(this.matrixRun(handle, loadRun(handle.db, summary.id, false)))
      }
    }
    return buildComparisonMatrix(runs, {
      ...(query.experimentId === undefined ? {} : { experimentId: query.experimentId }),
      ...(query.baselineId === undefined ? {} : { baselineId: query.baselineId }),
      ...(query.diffParams === undefined ? {} : { diffParams: query.diffParams }),
      ...(query.directions === undefined ? {} : { directions: query.directions })
    })
  }

  private matrixRun(handle: ProjectHandle, run: RunRow): MatrixRunInput {
    return {
      id: run.id,
      title: run.title,
      dirRef: run.dirRef,
      params: run.params,
      gitShortSha: run.gitInfo?.commitShort ?? '',
      dirty: run.gitInfo?.dirty ?? false,
      status: run.status,
      metrics: this.latestResultsMetrics(handle, run.id)
    }
  }

  private latestResultsMetrics(handle: ProjectHandle, runId: string): MetricSet | null {
    const row = handle.db
      .prepare(
        `SELECT metrics FROM snapshots
          WHERE target_type = 'run' AND target_id = ?
            AND CASE WHEN json_valid(ai_analysis) THEN json_extract(ai_analysis, '$.kind') END = 'results'
          ORDER BY created_at DESC, id DESC LIMIT 1`
      )
      .get(runId) as { metrics: string } | undefined
    return row === undefined ? null : decodeMetricSet(row.metrics)
  }

  compareTable(query: CompareQuery, options: ComparisonTableOptions = {}): ComparisonTable {
    const matrix = this.compareMatrix(query)
    return {
      format: options.format ?? 'latex_booktabs',
      table: formatComparisonTable(matrix, options),
      baselineId: matrix.baselineId,
      runs: matrix.runs,
      paramRows: matrix.paramRows,
      metricRows: matrix.metricRows
    }
  }

  /* --------------------------------------------------------------------- AI */

  async generateOutline(experimentId: string, options: AnalysisOptions = {}): Promise<SnapshotDetail> {
    const port = requireAiPort(this.ai)
    return this.generate(port, 'outline', { kind: 'experiment', id: experimentId }, options)
  }

  async generateResults(target: TargetRef, options: AnalysisOptions = {}): Promise<SnapshotDetail> {
    const port = requireAiPort(this.ai)
    return this.generate(port, 'results', target, options)
  }

  /** The exact bundle a generation would send; never calls the model. */
  async previewContext(target: TargetRef, options: AnalysisOptions = {}): Promise<ContextBundle> {
    const resolved = this.resolveTarget(target)
    const scan = await this.scan(target)
    const metrics = extractMetrics(scan, this.metrics)
    return buildContext(scan, metrics, {
      maxBytes: options.maxContextBytes ?? resolved.rules.maxTotalBytes
    })
  }

  private async generate(
    port: ExperimentsAiPort,
    kind: AiAnalysisKind,
    target: TargetRef,
    options: AnalysisOptions
  ): Promise<SnapshotDetail> {
    this.assertWritable()
    const handle = this.handle()
    const resolved = this.resolveTarget(target)
    const experimentId =
      target.kind === 'experiment' ? target.id : loadRun(handle.db, target.id, false).experimentId
    const scan = await this.scan(target, undefined, options.signal)
    const metrics = kind === 'results' ? extractMetrics(scan, this.metrics) : null
    const comparison =
      kind === 'results'
        ? this.compareMatrix({ experimentId })
        : null
    const runParams =
      target.kind === 'run' ? loadRun(handle.db, target.id, false).params : null
    const bundle = buildContext(scan, metrics, {
      maxBytes: options.maxContextBytes ?? resolved.rules.maxTotalBytes
    })
    if (bundle.entries.length === 0) {
      throw invalidInput('没有可用于分析的内容', { reason: 'no_content' })
    }
    const manifest = scan.plan.entries.map((entry) => ({ path: entry.path, sha256: entry.sha256 }))
    try {
      const generated = await generateAnalysis(
        port,
        {
          projectId: this.projectId,
          kind,
          scanRules: resolved.rules,
          extracted: scan.configs,
          bundle,
          metrics,
          comparison,
          runParams,
          manifest
        },
        {
          ...(options.extraInstructions === undefined ? {} : { extraInstructions: options.extraInstructions }),
          ...(options.signal === undefined ? {} : { signal: options.signal })
        }
      )
      const drift = detectDrift(resolved.root, scan.plan.entries)
      generated.analysis.warnings.push(...drift)
      const row: SnapshotRow = {
        id: newId(this.clockSource),
        targetType: target.kind,
        targetId: target.id,
        createdAt: this.now(),
        fileManifest: scan.plan.entries,
        gitInfo: scan.gitInfo,
        metrics: generated.metrics,
        aiAnalysis: generated.analysis,
        modelInfo: modelInfoOf(generated.output)
      }
      try {
        this.persistSnapshot(row)
      } catch (error) {
        this.pending.set(generated.analysis.aiOutputId, row)
        throw new SnapshotPersistError(generated.analysis.aiOutputId, error)
      }
      try {
        handle.inTx(() => {
          this.events.emit(handle, {
            type: 'ai.analysis_completed',
            entityType: 'experiment',
            entityId: experimentId,
            new: { snapshot_id: row.id, kind },
            summary: '实验分析已生成',
            actor: 'ai'
          })
        })
      } catch (error) {
        // The snapshot is committed; a missing audit row is logged, not fatal.
        this.logger?.warn('experiments: ai.analysis_completed event failed', {
          experiment_id: experimentId,
          snapshot_id: row.id,
          error: error instanceof Error ? error.message : String(error)
        })
      }
      this.notifyIndex(experimentId, 'ai.analysis_completed', '实验分析已生成')
      return this.toSnapshotDetail(row)
    } catch (error) {
      if (isAppError(error)) throw error
      throw new AppError(
        AppErrorCode.AiUnavailable,
        error instanceof Error ? error.message : String(error),
        { reason: 'ai_generation_failed' }
      )
    }
  }

  retrySaveSnapshot(aiOutputId: string): SnapshotDetail {
    const pending = this.pending.get(aiOutputId)
    if (pending === undefined) throw notFound(`没有待保存的快照：${aiOutputId}`)
    const row: SnapshotRow = {
      ...pending,
      id: newId(this.clockSource),
      createdAt: this.now()
    }
    try {
      this.persistSnapshot(row)
    } catch (error) {
      throw new SnapshotPersistError(aiOutputId, error)
    }
    this.pending.delete(aiOutputId)
    return this.toSnapshotDetail(row)
  }

  /* ------------------------------------------------------------------ helpers */

  private resolveDir(input: string): string {
    const trimmed = input.trim()
    if (trimmed === '') throw invalidInput('目录路径不能为空')
    if (!path.isAbsolute(trimmed)) throw invalidInput(`目录必须是绝对路径：${trimmed}`)
    const cleaned = path.resolve(trimmed)
    let info
    try {
      info = statSync(cleaned)
    } catch (error) {
      throw dirUnavailable(cleaned, error instanceof Error ? error.message : String(error))
    }
    if (!info.isDirectory()) throw dirUnavailable(cleaned, 'not a directory')
    try {
      readdirSync(cleaned)
    } catch (error) {
      throw dirUnavailable(cleaned, error instanceof Error ? error.message : String(error))
    }
    if (this.workspace.paths.isInternal(cleaned)) {
      throw dirGuard(cleaned, `${cleaned} 位于工作区内，不能作为实验目录`)
    }
    try {
      const real = realpathSync(cleaned)
      if (this.workspace.paths.isInternal(real)) {
        throw dirGuard(cleaned, `${cleaned} 解析后位于工作区内，不能作为实验目录`)
      }
    } catch (error) {
      if (error instanceof AppError) throw error
    }
    return cleaned
  }

  private toExperimentView(row: ExperimentRow): ExperimentView {
    return {
      id: row.id,
      title: row.title,
      purpose: row.purpose,
      dirRef: row.dirRef,
      scanRules: scanRulesToJson(row.scanRules),
      createdAt: row.createdAt,
      updatedAt: row.updatedAt,
      deletedAt: row.deletedAt
    }
  }

  private toRunView(row: RunRow): RunView {
    return {
      id: row.id,
      experimentId: row.experimentId,
      title: row.title,
      dirRef: row.dirRef,
      params: row.params,
      gitInfo: row.gitInfo,
      status: row.status,
      createdAt: row.createdAt,
      updatedAt: row.updatedAt,
      deletedAt: row.deletedAt
    }
  }

  private toSnapshotDetail(row: SnapshotRow): SnapshotDetail {
    return {
      id: row.id,
      targetType: row.targetType,
      targetId: row.targetId,
      createdAt: row.createdAt,
      fileManifest: row.fileManifest,
      gitInfo: row.gitInfo,
      metrics: row.metrics,
      aiAnalysis: row.aiAnalysis,
      modelInfo: row.modelInfo
    }
  }
}

function normalizeStatus(value: string): RunStatus {
  return value === 'running' || value === 'finished' || value === 'failed' ? value : 'unknown'
}

function isStatus(value: string): value is RunStatus {
  return value === 'unknown' || value === 'running' || value === 'finished' || value === 'failed'
}

function normalizeRunParams(params: RunParams | undefined): RunParams {
  const out: RunParams = {}
  for (const [rawKey, value] of Object.entries(params ?? {})) {
    const key = rawKey.trim()
    if (key === '') throw invalidInput('参数名不能为空')
    const source = value.source ?? undefined
    if (source !== undefined && source !== null) {
      if (source.path.trim() === '') throw invalidInput(`参数 ${key} 的来源缺少路径`)
      if (source.pointer !== undefined && source.pointer !== null && source.pointer.trim() === '') {
        throw invalidInput(`参数 ${key} 的来源指针为空`)
      }
    }
    out[key] = {
      value: value.value,
      ...(source === undefined || source === null ? {} : { source }),
      ...(value.confirmed === undefined ? {} : { confirmed: value.confirmed })
    }
  }
  return out
}

function inspectGitSafely(root: string, clock: Clock): GitInfo | null {
  try {
    const info = inspectGit(root, clock)
    return info.detected ? info : null
  } catch {
    return null
  }
}

function emptyMetrics(): MetricSet {
  return { v: 1, metrics: [], unparsed: [] }
}

function metricSetFromJson(value: JsonObject): MetricSet {
  const metrics: MetricSet['metrics'] = []
  walkJsonLeaves(value, '', (pointer, leaf) => {
    if (typeof leaf !== 'number' || !Number.isFinite(leaf)) return
    const key = pointer.replace(/^\//, '') || '(root)'
    metrics.push({
      key,
      label: key,
      value: leaf,
      raw: String(leaf),
      unit: null,
      source: { path: '', pointer, line: null, sha256: null },
      extractor: 'snapshot_input.v1'
    })
  })
  metrics.sort((left, right) => (left.key < right.key ? -1 : left.key > right.key ? 1 : 0))
  return { v: 1, metrics, unparsed: [] }
}

function emptyMetadata(): ScanMetadata {
  return {
    totalFiles: 0,
    totalBytes: 0,
    kindCounts: {},
    configs: [],
    csvs: [],
    readmes: [],
    dependencies: [],
    entryPoints: [],
    languages: {},
    logs: [],
    warnings: []
  }
}

function detectDrift(root: string, manifest: ScanFileEntry[]): string[] {
  const warnings: string[] = []
  for (const entry of manifest) {
    try {
      const info = statSync(path.join(root, ...entry.path.split('/')))
      if (info.isDirectory() || info.size !== entry.size || formatTimestamp(info.mtime) !== entry.mtime) {
        warnings.push(`files_changed_during_analysis:${entry.path}`)
      }
    } catch {
      warnings.push(`file_disappeared_during_analysis:${entry.path}`)
    }
  }
  return warnings
}