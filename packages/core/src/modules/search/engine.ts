import { createHash } from 'node:crypto'

import { AppErrorCode, appError } from '../../errors'
import type { Clock } from '../../clock'
import { formatTimestamp } from '../../clock'
import type { Db } from '../../db/open'
import { newId } from '../../ids'
import { KeywordIndex, tablesFor, VectorIndex, type ChunkHit, type IndexPartition } from '../../index-store'
import type { Logger } from '../../logger'
import type { WorkspaceManager } from '../../workspace'
import { Extractor, type ExtractOptions } from './extract'
import { PdfJsParser } from './pdf'
import { runSearch } from './search'
import { openJumpTarget, resolveJumpTarget } from './target'
import {
  type Change,
  type ChangeKind,
  type EmbeddingPort,
  type EmbeddingProfile,
  type EntityType,
  type IndexDoc,
  type IndexJob,
  type IndexNotifier,
  type IndexStatus,
  isEntityType,
  PARSER_VERSION_MARKDOWN,
  PARSER_VERSION_PDF,
  type PdfParser,
  type ReindexRequest,
  type RerankPort,
  SCHEMA_VERSION,
  type Scope,
  type SearchRequest,
  type SearchResponse,
  validateScope,
  type JumpTarget,
  type EntityRef
} from './types'

export interface SearchHooks {
  docIndexed?: (scopeKey: string, docId: string, status: string) => void
  jobFinished?: (scopeKey: string, jobId: string, state: string) => void
}

export interface SearchEngineOptions {
  clock?: Clock
  logger?: Logger
  embedding?: EmbeddingPort
  rerank?: RerankPort
  parser?: PdfParser
  vectorAvailable?: boolean
  fts5TrigramAvailable?: boolean
  debounceMs?: number
  keywordTopK?: number
  vectorTopK?: number
  rerankTopN?: number
  rrfK?: number
  keywordWeight?: number
  vectorWeight?: number
  rerankTimeoutMs?: number
  projectTimeoutMs?: number
  globalBudgetMs?: number
  maxConcurrency?: number
  maxAttempts?: number
  backoffMs?: readonly number[]
  embedBatchSize?: number
  snippetRunes?: number
  limit?: number
  maxLimit?: number
  /** Per-project override; a non-positive value deterministically times out. */
  projectBudget?: (projectId: string) => number
  hooks?: SearchHooks
  /** Opens/handles navigable targets for `openTarget` (desktop concern). */
  opener?: (target: string) => Promise<void> | void
  navigator?: (target: JumpTarget) => Promise<void> | void
}

export interface EngineConfig {
  clock: Clock
  logger?: Logger
  embedding?: EmbeddingPort
  rerank?: RerankPort
  debounceMs: number
  keywordTopK: number
  vectorTopK: number
  rerankTopN: number
  rrfK: number
  keywordWeight: number
  vectorWeight: number
  rerankTimeoutMs: number
  projectTimeoutMs: number
  globalBudgetMs: number
  maxConcurrency: number
  maxAttempts: number
  backoffMs: number[]
  embedBatchSize: number
  snippetRunes: number
  limit: number
  maxLimit: number
  projectBudget?: (projectId: string) => number
  hooks?: SearchHooks
  opener?: (target: string) => Promise<void> | void
  navigator?: (target: JumpTarget) => Promise<void> | void
}

export interface ScopeContext {
  key: string
  db: Db
  partition: IndexPartition
  projectId: string
  tables: { docs: string; chunks: string; fts: string; vectors: string }
  dirty: string
  jobs: string
  sources: string
}

interface DocState {
  status: string
  contentHash: string
  parserVersion: string
  embeddingModel: string
  embeddingDim: number
}

interface DirtyRow {
  project_id: string
  entity_type: string
  entity_id: string
  op: string
  attempts: number
  run_after: string
  last_error: string | null
  created_at: string
}

export const INDEX_STATUS = {
  pending: 'pending',
  ok: 'ok',
  vectorPending: 'vector_pending',
  failed: 'failed',
  noText: 'no_text',
  deleted: 'deleted',
  stale: 'stale',
  fileStale: 'index_stale'
} as const

/**
 * One search engine serves every scope of one workspace. It is the single
 * writer of each index database: dirty-queue processing and reindex jobs are
 * serialized per scope.
 */
export class SearchEngine {
  readonly workspace: WorkspaceManager
  readonly cfg: EngineConfig
  readonly vectorAvailable: boolean
  readonly fts5TrigramAvailable: boolean
  readonly extractor: Extractor
  private readonly runners = new Map<string, ScopeRunner>()
  private closed = false

  constructor(workspace: WorkspaceManager, options: SearchEngineOptions = {}) {
    this.workspace = workspace
    this.vectorAvailable = options.vectorAvailable ?? workspace.vectorAvailable
    this.fts5TrigramAvailable = options.fts5TrigramAvailable ?? workspace.fts5TrigramAvailable
    const clock = options.clock ?? workspace.clock
    this.cfg = {
      clock,
      logger: options.logger,
      embedding: options.embedding,
      rerank: options.rerank,
      debounceMs: options.debounceMs ?? 1500,
      keywordTopK: options.keywordTopK ?? 50,
      vectorTopK: options.vectorTopK ?? 50,
      rerankTopN: options.rerankTopN ?? 40,
      rrfK: options.rrfK ?? 60,
      keywordWeight: options.keywordWeight ?? 1,
      vectorWeight: options.vectorWeight ?? 1,
      rerankTimeoutMs: options.rerankTimeoutMs ?? 800,
      projectTimeoutMs: options.projectTimeoutMs ?? 2000,
      globalBudgetMs: options.globalBudgetMs ?? 2500,
      maxConcurrency: options.maxConcurrency ?? 8,
      maxAttempts: options.maxAttempts ?? 3,
      backoffMs: [...(options.backoffMs ?? [1000, 4000, 16000])],
      embedBatchSize: options.embedBatchSize ?? 32,
      snippetRunes: options.snippetRunes ?? 200,
      limit: options.limit ?? 20,
      maxLimit: options.maxLimit ?? 100,
      projectBudget: options.projectBudget,
      hooks: options.hooks,
      opener: options.opener,
      navigator: options.navigator
    }
    this.extractor = new Extractor(workspace, clock, options.parser ?? new PdfJsParser())
  }

  now(): Date {
    return this.cfg.clock.now()
  }

  nowIso(): string {
    return formatTimestamp(this.now())
  }

  /** Rerank capability of this engine: no port means keyword/vector only. */
  rerankStatus(): 'available' | 'unavailable' {
    return this.cfg.rerank === undefined ? 'unavailable' : 'available'
  }

  /** Runs the hybrid retrieval pipeline of the search spec */
  query(request: SearchRequest): Promise<SearchResponse> {
    return runSearch(this, request)
  }

  /** Resolves an entity reference plus locator into a jump target. */
  resolveTarget(ref: EntityRef, locator: JumpTarget['locator']): Promise<JumpTarget> {
    return resolveJumpTarget(this, ref, locator)
  }

  /** Opens a resolved target (PDF copy or in-app navigation). */
  openTarget(target: JumpTarget): Promise<void> {
    return openJumpTarget(this, target)
  }

  /* ---------------------------------------------------------------- scope */

  projectIds(): string[] {
    const ids = this.workspace.global.db.prepare(`SELECT id FROM projects ORDER BY id`).pluck().all()
    return ids.map(String)
  }

  scopeContext(scope: Scope): ScopeContext {
    validateScope(scope)
    if (scope.kind === 'global') {
      const { docs, chunks, fts, vectors } = tablesFor('global')
      const workspace = this.workspace
      return {
        key: 'global',
        get db() {
          return workspace.global.db
        },
        partition: 'global',
        projectId: '',
        tables: { docs, chunks, fts, vectors },
        dirty: 'paper_index_dirty',
        jobs: 'paper_index_jobs',
        sources: 'paper_index_sources'
      }
    }
    const projectId = scope.projectId ?? ''
    const status = this.workspace.global.db
      .prepare(`SELECT status FROM projects WHERE id = ?`)
      .get(projectId) as { status: string } | undefined
    if (status === undefined) {
      throw appError(AppErrorCode.NotFound, `project ${projectId} does not exist`, {
        details: { projectId }
      })
    }
    const { docs, chunks, fts, vectors } = tablesFor('project')
    const workspace = this.workspace
    return {
      key: projectId,
      get db() {
        return workspace.project(projectId).db
      },
      partition: 'project',
      projectId,
      tables: { docs, chunks, fts, vectors },
      dirty: 'index_dirty',
      jobs: 'index_jobs',
      sources: 'index_sources'
    }
  }

  private runnerFor(context: ScopeContext): ScopeRunner {
    if (this.closed) throw appError(AppErrorCode.Unsupported, 'search engine is closed')
    let runner = this.runners.get(context.key)
    if (runner === undefined) {
      runner = new ScopeRunner(this, context)
      this.runners.set(context.key, runner)
    }
    return runner
  }

  /* -------------------------------------------------------------- startup */

  /**
   * Syncs the source version registry, marks version-mismatched documents
   * stale, enqueues them, and arms the workers so a crash-resumed dirty queue
   * is processed.
   */
  async start(): Promise<void> {
    this.applySettings()
    const issues: Error[] = []
    const scopes: Scope[] = [{ kind: 'global' }, ...this.projectIds().map((id) => ({ kind: 'project' as const, projectId: id }))]
    for (const scope of scopes) {
      try {
        const context = this.scopeContext(scope)
        this.syncSourceRegistry(context)
        await this.reconcile(context)
        this.runnerFor(context).wake()
      } catch (error) {
        issues.push(error instanceof Error ? error : new Error(String(error)))
      }
    }
    if (issues.length > 0) throw new AggregateError(issues, 'search: startup reconciliation had failures')
  }

  private syncSourceRegistry(context: ScopeContext): void {
    const sources = context.db
      .prepare(`SELECT source FROM ${context.sources} ORDER BY source`)
      .pluck()
      .all() as string[]
    const upsert = context.db.prepare(
      `UPDATE ${context.sources} SET schema_version = ?, parser_version = ?, updated_at = ? WHERE source = ?`
    )
    for (const source of sources) {
      upsert.run(
        SCHEMA_VERSION,
        source === 'paper_fulltext' ? PARSER_VERSION_PDF : PARSER_VERSION_MARKDOWN,
        this.nowIso(),
        source
      )
    }
  }

  /** Marks schema/parser/profile mismatches stale and queues a re-upsert. */
  private async reconcile(context: ScopeContext): Promise<void> {
    const expectedModel = await this.expectedEmbeddingModel()
    const rows = context.db
      .prepare(
        `SELECT doc_id, source, entity_type, entity_id, index_status, schema_version,
                parser_version, embedding_model_id
         FROM ${context.tables.docs} WHERE index_status IN (?, ?, ?)`
      )
      .all(INDEX_STATUS.ok, INDEX_STATUS.vectorPending, INDEX_STATUS.noText) as Array<{
      doc_id: string
      source: string
      entity_type: string
      entity_id: string
      index_status: string
      schema_version: number
      parser_version: string
      embedding_model_id: string
    }>
    for (const row of rows) {
      const expectedParser = row.source === 'paper_fulltext' ? PARSER_VERSION_PDF : PARSER_VERSION_MARKDOWN
      let stale = row.schema_version !== SCHEMA_VERSION || row.parser_version !== expectedParser
      if (
        !stale &&
        expectedModel !== '' &&
        row.index_status === INDEX_STATUS.ok &&
        row.embedding_model_id !== '' &&
        row.embedding_model_id !== expectedModel
      ) {
        stale = true
      }
      // A previous run without sqlite-vec left documents keyword-only. Once a
      // profile and the extension are available, rebuild their vectors.
      if (
        !stale &&
        this.vectorAvailable &&
        expectedModel !== '' &&
        row.index_status === INDEX_STATUS.vectorPending
      ) {
        stale = true
      }
      if (!stale) continue
      context.db
        .prepare(`UPDATE ${context.tables.docs} SET index_status = ? WHERE doc_id = ?`)
        .run(INDEX_STATUS.stale, row.doc_id)
      if (isEntityType(row.entity_type)) {
        this.upsertDirty(context, {
          projectId: context.projectId,
          entityType: row.entity_type,
          entityId: row.entity_id,
          kind: 'upsert'
        }, 0)
      }
    }
  }

  private async expectedEmbeddingModel(): Promise<string> {
    if (this.cfg.embedding === undefined) return ''
    try {
      return (await this.cfg.embedding.currentProfile())?.id ?? ''
    } catch {
      return ''
    }
  }

  private applySettings(): void {
    const db = this.workspace.global.db
    const read = (key: string): number | undefined => {
      const row = db.prepare(`SELECT value FROM settings WHERE key = ?`).get(key) as { value: string } | undefined
      if (row === undefined) return undefined
      const value = Number(row.value)
      return Number.isFinite(value) ? value : undefined
    }
    const settings: Array<[string, (value: number) => void, number, number, boolean]> = [
      ['search.debounce_ms', (value) => (this.cfg.debounceMs = value), 0, 60000, true],
      ['search.keyword_topk', (value) => (this.cfg.keywordTopK = value), 1, 500, false],
      ['search.vector_topk', (value) => (this.cfg.vectorTopK = value), 1, 500, false],
      ['search.rerank_topn', (value) => (this.cfg.rerankTopN = value), 1, 200, false],
      ['search.project_timeout_ms', (value) => (this.cfg.projectTimeoutMs = value), 1, 60000, false],
      ['search.snippet_runes', (value) => (this.cfg.snippetRunes = value), 50, 1000, false]
    ]
    for (const [key, apply, min, max, allowZero] of settings) {
      const value = read(key)
      if (value === undefined) continue
      if (value < min || value > max) continue
      if (!allowZero && value === 0) continue
      apply(value)
    }
  }

  /* ---------------------------------------------------------------- notify */

  async notify(change: Change): Promise<void> {
    if (!isEntityType(change.entityType)) {
      throw appError(AppErrorCode.InvalidArgument, `entity type ${JSON.stringify(change.entityType)} is not indexed`)
    }
    if (change.entityId === '') {
      throw appError(AppErrorCode.InvalidArgument, 'index change requires an entityId')
    }
    if (change.entityType === 'paper' && change.projectId !== '') {
      throw appError(AppErrorCode.InvalidArgument, 'paper changes live in the global index and take no projectId')
    }
    const kind: ChangeKind = change.kind ?? 'upsert'
    if (change.projectId === '' && change.entityType !== 'paper') {
      throw appError(AppErrorCode.InvalidArgument, `${change.entityType} changes require a projectId`)
    }
    if (kind !== 'upsert' && kind !== 'delete' && kind !== 'restore') {
      throw appError(AppErrorCode.InvalidArgument, `unknown index change ${JSON.stringify(kind)}`)
    }
    if (change.at === undefined) change.at = this.nowIso()
    const scope: Scope =
      change.projectId === ''
        ? { kind: 'global' }
        : { kind: 'project', projectId: change.projectId }
    const context = this.scopeContext(scope)
    this.upsertDirty(context, { ...change, kind }, this.cfg.debounceMs)
    this.runnerFor(context).wake()
  }

  private upsertDirty(context: ScopeContext, change: Change, delayMs: number): void {
    const now = this.now()
    const runAfter = formatTimestamp(now.getTime() + delayMs)
    context.db
      .prepare(
        `INSERT INTO ${context.dirty}
           (project_id, entity_type, entity_id, op, attempts, run_after, last_error, created_at, updated_at)
         VALUES (?, ?, ?, ?, 0, ?, NULL, ?, ?)
         ON CONFLICT(project_id, entity_type, entity_id) DO UPDATE SET
           op = excluded.op, attempts = 0, run_after = excluded.run_after,
           last_error = NULL, updated_at = excluded.updated_at`
      )
      .run(
        change.projectId,
        change.entityType,
        change.entityId,
        change.kind ?? 'upsert',
        runAfter,
        formatTimestamp(now),
        formatTimestamp(now)
      )
  }

  /** Processes every queued document of a scope now (flush/exit). */
  async flush(scope?: Scope): Promise<void> {
    const scopes: Scope[] =
      scope === undefined
        ? [{ kind: 'global' }, ...this.projectIds().map((id) => ({ kind: 'project' as const, projectId: id }))]
        : [scope]
    const issues: Error[] = []
    for (const item of scopes) {
      try {
        const context = this.scopeContext(item)
        await this.runnerFor(context).processDue(true)
      } catch (error) {
        issues.push(error instanceof Error ? error : new Error(String(error)))
      }
    }
    if (issues.length > 0) throw new AggregateError(issues, 'search: flush had failures')
  }

  /* --------------------------------------------------------------- indexing */

  readDocState(context: ScopeContext, docId: string): { state: DocState; exists: boolean } {
    const row = context.db
      .prepare(
        `SELECT index_status, content_hash, parser_version, embedding_model_id, embedding_dim
         FROM ${context.tables.docs} WHERE doc_id = ?`
      )
      .get(docId) as
      | {
          index_status: string
          content_hash: string
          parser_version: string
          embedding_model_id: string
          embedding_dim: number
        }
      | undefined
    if (row === undefined) {
      return {
        exists: false,
        state: { status: '', contentHash: '', parserVersion: '', embeddingModel: '', embeddingDim: 0 }
      }
    }
    return {
      exists: true,
      state: {
        status: row.index_status,
        contentHash: row.content_hash,
        parserVersion: row.parser_version,
        embeddingModel: row.embedding_model_id,
        embeddingDim: row.embedding_dim
      }
    }
  }

  async profile(): Promise<EmbeddingProfile | null> {
    if (this.cfg.embedding === undefined) return null
    try {
      const profile = await this.cfg.embedding.currentProfile()
      return profile !== null && profile.id !== '' ? profile : null
    } catch {
      return null
    }
  }

  private canSkip(state: DocState, doc: IndexDoc, profileId: string): boolean {
    if (state.contentHash !== doc.contentHash || state.parserVersion !== doc.parserVersion) return false
    switch (state.status) {
      case INDEX_STATUS.ok:
        return !(profileId !== '' && doc.chunks.length > 0 && state.embeddingModel !== profileId)
      case INDEX_STATUS.noText:
        return true
      case INDEX_STATUS.vectorPending:
        return profileId === ''
      default:
        return false
    }
  }

  async applyDoc(context: ScopeContext, doc: IndexDoc, options: { force?: boolean } = {}): Promise<void> {
    const profile = await this.profile()
    const previous = this.readDocState(context, doc.docId)
    const effectiveProfile = this.vectorAvailable ? profile?.id ?? '' : ''
    if (options.force !== true && previous.exists && this.canSkip(previous.state, doc, effectiveProfile)) return

    const storeDoc = {
      projectId: doc.projectId,
      docId: doc.docId,
      source: doc.source,
      entityType: doc.entityType,
      entityId: doc.entityId,
      title: doc.title,
      contentHash: doc.contentHash
    }
    const storeChunks = doc.chunks.map((chunk) => ({
      chunkId: chunk.chunkId,
      docId: chunk.docId,
      projectId: doc.projectId,
      ordinal: chunk.ordinal,
      text: chunk.text,
      contentHash: chunk.contentHash,
      locatorJson: JSON.stringify(chunk.locator)
    }))
    try {
      new KeywordIndex(context.db, context.partition).replaceDoc(storeDoc, storeChunks)
    } catch (cause) {
      throw appError(AppErrorCode.Internal, `keyword index write failed for ${doc.docId}`, { cause })
    }

    let status: string = INDEX_STATUS.ok
    let errorText: string | null = null
    let modelId = ''
    let dim = 0
    if (doc.noText === true) {
      status = INDEX_STATUS.noText
    } else if (doc.chunks.length > 0) {
      if (profile === null || !this.vectorAvailable) {
        // Keyword-only is an expected degradation, not a document error.
        status = INDEX_STATUS.vectorPending
      } else {
        modelId = profile.id
        try {
          dim = await this.writeVectors(context, doc, profile.id)
        } catch (error) {
          status = INDEX_STATUS.vectorPending
          errorText = error instanceof Error ? error.message : String(error)
          modelId = ''
        }
      }
    }
    this.updateDocMeta(context, doc, status, modelId, dim, errorText)
    this.cfg.hooks?.docIndexed?.(context.projectId, doc.docId, status)
  }

  private async writeVectors(context: ScopeContext, doc: IndexDoc, profileId: string): Promise<number> {
    if (!this.vectorAvailable || this.cfg.embedding === undefined) return 0
    const vectors: Array<{ chunkId: string; vector: readonly number[] }> = []
    for (let start = 0; start < doc.chunks.length; start += this.cfg.embedBatchSize) {
      const batch = doc.chunks.slice(start, start + this.cfg.embedBatchSize)
      const embedded = await this.cfg.embedding.embed(batch.map((chunk) => chunk.text))
      if (embedded.length !== batch.length) {
        throw new Error(`embedding returned ${embedded.length} vectors for ${batch.length} chunks`)
      }
      for (const [index, vector] of embedded.entries()) {
        const chunk = batch[index]
        if (chunk !== undefined) vectors.push({ chunkId: chunk.chunkId, vector })
      }
    }
    const storeDoc = {
      projectId: doc.projectId,
      docId: doc.docId,
      source: doc.source,
      entityType: doc.entityType,
      entityId: doc.entityId,
      title: doc.title,
      contentHash: doc.contentHash
    }
    const storeChunks = doc.chunks.map((chunk) => ({
      chunkId: chunk.chunkId,
      docId: chunk.docId,
      projectId: doc.projectId,
      ordinal: chunk.ordinal,
      text: chunk.text,
      contentHash: chunk.contentHash,
      locatorJson: JSON.stringify(chunk.locator)
    }))
    new VectorIndex(context.db, context.partition, { vectorAvailable: this.vectorAvailable }).replaceDoc(
      profileId,
      storeDoc,
      storeChunks,
      vectors
    )
    return vectors[0]?.vector.length ?? 0
  }

  private updateDocMeta(
    context: ScopeContext,
    doc: IndexDoc,
    status: string,
    modelId: string,
    dim: number,
    errorText: string | null
  ): void {
    const projectCondition = context.partition === 'project' ? ', project_id = ?' : ''
    const args: Array<string | number | null> = [
      doc.source,
      doc.entityType,
      doc.entityId,
      doc.title,
      doc.lang,
      status,
      doc.parserVersion,
      modelId,
      dim,
      this.nowIso(),
      errorText,
      formatTimestamp(doc.updatedAt)
    ]
    if (context.partition === 'project') args.push(doc.projectId)
    args.push(doc.docId)
    context.db
      .prepare(
        `UPDATE ${context.tables.docs} SET source = ?, entity_type = ?, entity_id = ?, title = ?, lang = ?,
           index_status = ?, parser_version = ?, embedding_model_id = ?, embedding_dim = ?,
           indexed_at = ?, error = ?, updated_at = ?${projectCondition}
         WHERE doc_id = ?`
      )
      .run(...args)
  }

  markDeleted(context: ScopeContext, entityType: EntityType, entityId: string): void {
    const projectCondition = context.partition === 'project' ? ' AND project_id = ?' : ''
    const args: string[] = [INDEX_STATUS.deleted, this.nowIso(), entityType, entityId]
    if (context.partition === 'project') args.push(context.projectId)
    context.db
      .prepare(
        `UPDATE ${context.tables.docs} SET index_status = ?, indexed_at = ?, error = NULL
         WHERE entity_type = ? AND entity_id = ?${projectCondition}`
      )
      .run(...args)
  }

  markProblem(context: ScopeContext, docId: string, cause: Error): void {
    context.db
      .prepare(`UPDATE ${context.tables.docs} SET index_status = ?, error = ? WHERE doc_id = ?`)
      .run(INDEX_STATUS.fileStale, cause.message, docId)
  }

  markFailed(context: ScopeContext, entityType: EntityType, entityId: string, cause: Error): void {
    const projectCondition = context.partition === 'project' ? ' AND project_id = ?' : ''
    const args: string[] = [INDEX_STATUS.failed, cause.message, entityType, entityId]
    if (context.partition === 'project') args.push(context.projectId)
    context.db
      .prepare(
        `UPDATE ${context.tables.docs} SET index_status = ?, error = ?
         WHERE entity_type = ? AND entity_id = ?${projectCondition}`
      )
      .run(...args)
  }

  /** Extracts and writes every document of one entity; absent entity → deleted. */
  async indexEntity(context: ScopeContext, change: Change): Promise<void> {
    const result = await this.extractor.entityDocs(
      context.projectId,
      change.entityType,
      change.entityId,
      {}
    )
    for (const problem of result.problems) this.markProblem(context, problem.docId, problem.error)
    if (result.docs.length === 0 && result.problems.length === 0) {
      this.markDeleted(context, change.entityType, change.entityId)
      return
    }
    for (const doc of result.docs) await this.applyDoc(context, doc)
  }

  /* ------------------------------------------------------------------ jobs */

  async reindex(request: ReindexRequest): Promise<string> {
    const context = this.scopeContext(request.scope)
    if (this.closed) throw appError(AppErrorCode.Unsupported, 'search engine is closed')
    const jobId = newId(this.cfg.clock)
    context.db
      .prepare(
        `INSERT INTO ${context.jobs}
           (job_id, scope_kind, scope_id, reparse_pdf, rebuild_vector, status, created_at)
         VALUES (?, ?, ?, ?, ?, 'pending', ?)`
      )
      .run(
        jobId,
        request.scope.kind,
        request.scope.projectId ?? '',
        request.reparsePdf === true ? 1 : 0,
        request.rebuildVector === true ? 1 : 0,
        this.nowIso()
      )
    void this.runJob(context, jobId, request).catch((error: unknown) => {
      this.cfg.logger?.error('search: reindex job crashed', { jobId, error: String(error) })
    })
    return jobId
  }

  private async runJob(context: ScopeContext, jobId: string, request: ReindexRequest): Promise<void> {
    const runner = this.runnerFor(context)
    try {
      this.updateJob(context, jobId, 'running', 0, 0, 0, null)
      const options: ExtractOptions = { reparsePdf: request.reparsePdf === true }
      const extracted =
        context.partition === 'global'
          ? await this.extractor.allPaperDocs(options)
          : await this.extractor.allProjectDocs(context.projectId, options)
      const total = extracted.docs.length + extracted.problems.length
      this.updateJob(context, jobId, 'running', total, 0, 0, null)
      let processed = 0
      let failed = 0
      const seen = new Set<string>()
      for (const doc of extracted.docs) {
        if (this.jobCanceled(context, jobId)) {
          this.finishJob(context, jobId, 'canceled', processed, failed, null)
          return
        }
        seen.add(doc.docId)
        try {
          await runner.serialize(() =>
            this.applyDoc(context, doc, { force: request.reparsePdf === true || request.rebuildVector === true })
          )
        } catch (error) {
          failed += 1
          this.cfg.logger?.warn('search: rebuild document failed', { docId: doc.docId, error: String(error) })
        }
        processed += 1
        this.updateProgress(context, jobId, processed, failed)
      }
      for (const problem of extracted.problems) {
        if (this.jobCanceled(context, jobId)) {
          this.finishJob(context, jobId, 'canceled', processed, failed, null)
          return
        }
        await runner.serialize(() => this.markProblem(context, problem.docId, problem.error))
        seen.add(problem.docId)
        processed += 1
        this.updateProgress(context, jobId, processed, failed)
      }
      await runner.serialize(() => this.markVanished(context, seen))
      this.finishJob(context, jobId, 'done', processed, failed, null)
    } catch (error) {
      const cause = error instanceof Error ? error : new Error(String(error))
      this.finishJob(context, jobId, 'failed', 0, 0, cause)
    }
  }

  private markVanished(context: ScopeContext, seen: Set<string>): void {
    const ids = context.db
      .prepare(`SELECT doc_id FROM ${context.tables.docs} WHERE index_status <> ?`)
      .pluck()
      .all(INDEX_STATUS.deleted) as string[]
    const update = context.db.prepare(
      `UPDATE ${context.tables.docs} SET index_status = ?, error = NULL WHERE doc_id = ?`
    )
    for (const id of ids.map(String)) {
      if (!seen.has(id)) update.run(INDEX_STATUS.deleted, id)
    }
  }

  private jobCanceled(context: ScopeContext, jobId: string): boolean {
    const row = context.db
      .prepare(`SELECT cancel_requested FROM ${context.jobs} WHERE job_id = ?`)
      .get(jobId) as { cancel_requested: number } | undefined
    return row === undefined || row.cancel_requested !== 0
  }

  async cancelJob(jobId: string): Promise<void> {
    if (jobId === '') throw appError(AppErrorCode.InvalidArgument, 'job id is required')
    const scopes: Scope[] = [
      { kind: 'global' },
      ...this.projectIds().map((id) => ({ kind: 'project' as const, projectId: id }))
    ]
    for (const scope of scopes) {
      let context: ScopeContext
      try {
        context = this.scopeContext(scope)
      } catch (error) {
        this.cfg.logger?.warn('search: scope unavailable while cancelling a job', {
          scope: scope.kind === 'global' ? 'global' : scope.projectId,
          error: String(error)
        })
        continue
      }
      const result = context.db
        .prepare(
          `UPDATE ${context.jobs} SET cancel_requested = 1,
             status = CASE WHEN status = 'pending' THEN 'canceled' ELSE status END,
             finished_at = CASE WHEN status = 'pending' THEN ? ELSE finished_at END
           WHERE job_id = ?`
        )
        .run(this.nowIso(), jobId)
      if (result.changes > 0) {
        this.runners.get(context.key)?.wake()
        return
      }
    }
    throw appError(AppErrorCode.NotFound, `index job ${jobId} does not exist`, { details: { jobId } })
  }

  private updateJob(
    context: ScopeContext,
    jobId: string,
    status: string,
    total: number,
    processed: number,
    failed: number,
    errorText: string | null
  ): void {
    context.db
      .prepare(
        `UPDATE ${context.jobs} SET status = ?, total = ?, processed = ?, failed = ?, last_error = ?,
           started_at = COALESCE(started_at, ?) WHERE job_id = ?`
      )
      .run(status, total, processed, failed, errorText, status === 'running' ? this.nowIso() : null, jobId)
  }

  private updateProgress(context: ScopeContext, jobId: string, processed: number, failed: number): void {
    context.db
      .prepare(`UPDATE ${context.jobs} SET processed = ?, failed = ? WHERE job_id = ?`)
      .run(processed, failed, jobId)
  }

  private finishJob(
    context: ScopeContext,
    jobId: string,
    status: string,
    processed: number,
    failed: number,
    cause: Error | null
  ): void {
    context.db
      .prepare(
        `UPDATE ${context.jobs} SET status = ?, processed = ?, failed = ?, last_error = ?, finished_at = ?
         WHERE job_id = ?`
      )
      .run(status, processed, failed, cause?.message ?? null, this.nowIso(), jobId)
    this.cfg.hooks?.jobFinished?.(context.key, jobId, status)
  }

  /* ---------------------------------------------------------------- status */

  async status(scope: Scope): Promise<IndexStatus> {
    const context = this.scopeContext(scope)
    const docs = (
      context.db
        .prepare(`SELECT COUNT(*) AS n FROM ${context.tables.docs} WHERE index_status <> ?`)
        .get(INDEX_STATUS.deleted) as { n: number }
    ).n
    const chunks = (
      context.db
        .prepare(
          `SELECT COUNT(*) AS n FROM ${context.tables.chunks} c
           JOIN ${context.tables.docs} d ON d.doc_id = c.doc_id WHERE d.index_status <> ?`
        )
        .get(INDEX_STATUS.deleted) as { n: number }
    ).n
    const dirty = (context.db.prepare(`SELECT COUNT(*) AS n FROM ${context.dirty}`).get() as { n: number }).n
    const failedDocs = (
      context.db
        .prepare(`SELECT COUNT(*) AS n FROM ${context.tables.docs} WHERE index_status = ?`)
        .get(INDEX_STATUS.failed) as { n: number }
    ).n
    const meta = context.db
      .prepare(
        `SELECT MAX(indexed_at) AS last_indexed,
                (SELECT error FROM ${context.tables.docs}
                 WHERE error IS NOT NULL AND error <> '' ORDER BY indexed_at DESC LIMIT 1) AS last_error
         FROM ${context.tables.docs}`
      )
      .get() as { last_indexed: string | null; last_error: string | null }
    const exhausted = (
      context.db
        .prepare(`SELECT COUNT(*) AS n FROM ${context.dirty} WHERE attempts >= ?`)
        .get(this.cfg.maxAttempts) as { n: number }
    ).n
    const queueError = context.db
      .prepare(
        `SELECT last_error FROM ${context.dirty}
         WHERE last_error IS NOT NULL AND last_error <> '' ORDER BY updated_at DESC LIMIT 1`
      )
      .pluck()
      .get() as string | undefined
    const stale = (
      context.db
        .prepare(`SELECT COUNT(*) AS n FROM ${context.tables.docs} WHERE index_status = ?`)
        .get(INDEX_STATUS.stale) as { n: number }
    ).n
    const parserVersions = context.db
      .prepare(`SELECT DISTINCT parser_version FROM ${context.sources} WHERE parser_version <> '' ORDER BY parser_version`)
      .pluck()
      .all() as string[]
    const jobs = this.jobs(context)
    const active = jobs.filter((job) => job.state === 'pending' || job.state === 'running')
    const latest = jobs[0]
    const progress = latest !== undefined && latest.total > 0 ? Math.min(1, latest.processed / latest.total) : 0
    return {
      docs,
      chunks,
      dirty,
      failed: failedDocs + exhausted,
      indexing: active.length > 0 || (this.runners.get(context.key)?.busy ?? false),
      progress,
      lastIndexedAt: meta.last_indexed,
      schemaVersion: String(SCHEMA_VERSION),
      parserVersion: parserVersions.length > 0 ? parserVersions.join(',') : PARSER_VERSION_MARKDOWN,
      embeddingModel: (await this.profile())?.id ?? '',
      stale: stale > 0,
      lastError: meta.last_error ?? queueError ?? null,
      jobs
    }
  }

  /** Latest jobs of one scope, newest first (status views and MCP jobs). */
  jobs(context: ScopeContext, limit = 10): IndexJob[] {
    const rows = context.db
      .prepare(
        `SELECT job_id, scope_kind, scope_id, reparse_pdf, rebuild_vector, status, total, processed,
                failed, last_error, cancel_requested, created_at, started_at, finished_at
         FROM ${context.jobs} ORDER BY created_at DESC, job_id DESC LIMIT ?`
      )
      .all(limit) as Array<{
      job_id: string
      scope_kind: string
      scope_id: string
      reparse_pdf: number
      rebuild_vector: number
      status: string
      total: number
      processed: number
      failed: number
      last_error: string | null
      cancel_requested: number
      created_at: string
      started_at: string | null
      finished_at: string | null
    }>
    return rows.map((row) => ({
      jobId: row.job_id,
      scopeKind: row.scope_kind === 'global' ? 'global' : 'project',
      scopeId: row.scope_id,
      reparsePdf: row.reparse_pdf === 1,
      rebuildVector: row.rebuild_vector === 1,
      state: row.status as IndexJob['state'],
      total: row.total,
      processed: row.processed,
      failed: row.failed,
      lastError: row.last_error,
      cancelRequested: row.cancel_requested === 1,
      createdAt: row.created_at,
      startedAt: row.started_at,
      finishedAt: row.finished_at
    }))
  }

  /** Stable epoch of the involved index batches (cache keying). */
  indexEpoch(targets: readonly { key: string; db: Db; tables: { docs: string } }[]): string {
    const parts = targets.map((target) => {
      const row = target.db
        .prepare(`SELECT COUNT(*) AS n, MAX(indexed_at) AS last FROM ${target.tables.docs}`)
        .get() as { n: number; last: string | null }
      return `${target.key}:${row.n}:${row.last ?? ''}`
    })
    return createHashHex(parts.join('|')).slice(0, 16)
  }

  /** Dirty-queue rows ready to run, ordered by due time. */
  dueRows(context: ScopeContext, force: boolean): DirtyRow[] {
    const columns = `project_id, entity_type, entity_id, op, attempts, run_after, last_error, created_at`
    const rows = force
      ? context.db
          .prepare(
            `SELECT ${columns} FROM ${context.dirty}
             WHERE attempts < ? ORDER BY run_after, entity_id`
          )
          .all(this.cfg.maxAttempts)
      : context.db
          .prepare(
            `SELECT ${columns} FROM ${context.dirty}
             WHERE run_after <= ? AND attempts < ? ORDER BY run_after, entity_id`
          )
          .all(this.nowIso(), this.cfg.maxAttempts)
    return rows as DirtyRow[]
  }

  nextDueDelay(context: ScopeContext): number | undefined {
    const raw = context.db
      .prepare(`SELECT MIN(run_after) AS due FROM ${context.dirty} WHERE attempts < ?`)
      .get(this.cfg.maxAttempts) as { due: string | null }
    if (raw.due === null || raw.due === '') return undefined
    const due = Date.parse(raw.due)
    if (Number.isNaN(due)) return 0
    return Math.max(0, due - this.now().getTime())
  }

  deleteDirty(context: ScopeContext, row: DirtyRow): void {
    context.db
      .prepare(`DELETE FROM ${context.dirty} WHERE project_id = ? AND entity_type = ? AND entity_id = ?`)
      .run(row.project_id, row.entity_type, row.entity_id)
  }

  rescheduleDirty(
    context: ScopeContext,
    row: DirtyRow,
    attempts: number,
    runAfter: string,
    errorText: string
  ): void {
    context.db
      .prepare(
        `UPDATE ${context.dirty} SET attempts = ?, run_after = ?, last_error = ?, updated_at = ?
         WHERE project_id = ? AND entity_type = ? AND entity_id = ?`
      )
      .run(attempts, runAfter, errorText, this.nowIso(), row.project_id, row.entity_type, row.entity_id)
  }

  async processRow(context: ScopeContext, row: DirtyRow): Promise<void> {
    if (row.attempts >= this.cfg.maxAttempts) return
    if (!isEntityType(row.entity_type)) {
      this.rescheduleDirty(
        context,
        row,
        this.cfg.maxAttempts,
        this.nowIso(),
        `unknown entity type ${row.entity_type}`
      )
      return
    }
    const change: Change = {
      projectId: row.project_id,
      entityType: row.entity_type,
      entityId: row.entity_id,
      kind: row.op as ChangeKind
    }
    try {
      if (change.kind === 'delete') this.markDeleted(context, change.entityType, change.entityId)
      else if (change.kind === 'upsert' || change.kind === 'restore') await this.indexEntity(context, change)
      else throw new Error(`unknown index change ${row.op}`)
      this.deleteDirty(context, row)
    } catch (error) {
      const cause = error instanceof Error ? error : new Error(String(error))
      const attempts = row.attempts + 1
      if (attempts >= this.cfg.maxAttempts) {
        this.rescheduleDirty(context, row, attempts, this.nowIso(), cause.message)
        this.markFailed(context, change.entityType, change.entityId, cause)
        this.cfg.logger?.warn('search: index retries exhausted', {
          scope: context.key,
          entityType: row.entity_type,
          entityId: row.entity_id,
          error: cause.message
        })
        return
      }
      const backoff = this.cfg.backoffMs[Math.min(attempts - 1, this.cfg.backoffMs.length - 1)] ?? 1000
      this.rescheduleDirty(
        context,
        row,
        attempts,
        formatTimestamp(this.now().getTime() + backoff),
        cause.message
      )
    }
  }

  /** Global literature changes come from the paper service's activity hook. */
  globalIndexer(): IndexNotifier {
    return {
      onEntityChanged: async (change) => {
        if (change.entityType !== 'paper') return
        await this.notify({ ...change, projectId: '' })
      },
      rebuild: async () => {
        await this.reindex({ scope: { kind: 'global' } })
      }
    }
  }

  /** Per-project indexer for notes/ideas/experiments/databases services. */
  projectIndexer(projectId: string): IndexNotifier {
    return {
      onEntityChanged: async (change) => {
        if (!isEntityType(change.entityType) || change.entityType === 'paper') return
        await this.notify({ ...change, projectId })
      },
      rebuild: async (scope) => {
        await this.reindex({ scope: { kind: 'project', projectId: scope.projectId ?? projectId } })
      }
    }
  }

  close(): void {
    if (this.closed) return
    this.closed = true
    for (const runner of this.runners.values()) runner.stop()
    this.runners.clear()
  }
}

/** One scope's single writer and debounce timer. */
export class ScopeRunner {
  readonly engine: SearchEngine
  readonly context: ScopeContext
  busy = false
  private stopped = false
  private timer: NodeJS.Timeout | undefined
  private chain: Promise<void> = Promise.resolve()

  constructor(engine: SearchEngine, context: ScopeContext) {
    this.engine = engine
    this.context = context
  }

  /** Arms the timer for the earliest due row; no-op while stopped. */
  wake(): void {
    if (this.stopped) return
    if (this.timer !== undefined) {
      clearTimeout(this.timer)
      this.timer = undefined
    }
    const delay = this.engine.nextDueDelay(this.context)
    if (delay === undefined) return
    this.timer = setTimeout(() => {
      this.timer = undefined
      void this.processDue(false).catch((error: unknown) => {
        this.engine.cfg.logger?.error('search: dirty queue pass failed', {
          scope: this.context.key,
          error: String(error)
        })
      })
    }, delay)
    this.timer.unref?.()
  }

  /** Processes currently due rows; `force` also ignores the debounce window. */
  async processDue(force: boolean): Promise<void> {
    if (this.stopped) return
    const run = this.chain.then(async () => {
      if (this.stopped) return
      this.busy = true
      try {
        const rows = this.engine.dueRows(this.context, force)
        for (const row of rows) {
          if (this.stopped) return
          await this.engine.processRow(this.context, row)
        }
      } finally {
        this.busy = false
        if (!this.stopped) this.wake()
      }
    })
    this.chain = run.catch(() => undefined)
    await run
  }

  /** Serializes a batch with dirty-queue processing (reindex jobs). */
  async serialize<T>(fn: () => T | Promise<T>): Promise<T> {
    const previous = this.chain
    let release: () => void = () => undefined
    this.chain = new Promise<void>((resolve) => {
      release = resolve
    })
    await previous.catch(() => undefined)
    try {
      return await fn()
    } finally {
      release()
    }
  }

  stop(): void {
    if (this.stopped) return
    this.stopped = true
    if (this.timer !== undefined) {
      clearTimeout(this.timer)
      this.timer = undefined
    }
  }
}

function createHashHex(text: string): string {
  return createHash('sha256').update(text).digest('hex')
}
