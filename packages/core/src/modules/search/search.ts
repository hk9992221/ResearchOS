import { AppErrorCode, appError } from '../../errors'
import { KeywordIndex, VectorIndex, type ChunkHit } from '../../index-store'
import type { SearchEngine } from './engine'
import {
  type DocFilter,
  type DocMeta,
  linkedPaperDocIds,
  loadDocMeta,
  makeTarget,
  resolveDocFilter,
  type SearchTarget
} from './filters'
import { buildSnippet, containsPhrases, normalizeQuery, retrievalText, type NormalizedQuery } from './query'
import { jumpTargetFor } from './target'
import { foldRunes, truncateRunes } from './chunk'
import {
  type DegradeReason,
  type EmbeddingProfile,
  type EntityType,
  type Locator,
  type RerankMode,
  type SearchHit,
  type SearchRequest,
  type SearchResponse,
  type ScoreBreakdown,
  type Snippet,
  type SourceType,
  validateScope
} from './types'

export type RuntimeTarget = SearchTarget

interface ChunkAgg {
  hit: ChunkHit
  score: number
  keywordRank: number
  vectorRank: number
}

export interface DocAgg {
  target: RuntimeTarget
  meta: DocMeta
  chunks: ChunkAgg[]
  score: number
  keywordRank: number
  vectorRank: number
  primary: boolean
}

export interface MergedResult {
  docs: DocAgg[]
  score: number
  keywordRank: number
  vectorRank: number
  rerank: number | null
}

interface RecallResult {
  hits: ChunkHit[]
  filter: DocFilter
  timedOut: boolean
  error?: Error
}

class SearchTimeout extends Error {
  constructor() {
    super('search stage timed out')
    this.name = 'SearchTimeout'
  }
}

/**
 * Runs one complete retrieval pipeline. Capability failures are
 * reported as visible degradations, not errors: only invalid parameters, a
 * missing scope and an unreadable index fail the call.
 */
export async function runSearch(engine: SearchEngine, request: SearchRequest): Promise<SearchResponse> {
  const started = Date.now()
  validateScope(request.scope)
  if ((request.offset ?? 0) < 0) throw appError(AppErrorCode.InvalidArgument, 'offset must not be negative')
  if ((request.limit ?? 0) < 0) throw appError(AppErrorCode.InvalidArgument, 'limit must not be negative')
  const mode: RerankMode = request.rerank ?? 'auto'
  if (mode !== 'auto' && mode !== 'off' && mode !== 'force') {
    throw appError(AppErrorCode.InvalidArgument, `unknown rerank mode ${JSON.stringify(mode)}`)
  }
  const limit = Math.min(request.limit === undefined || request.limit === 0 ? engine.cfg.limit : request.limit, engine.cfg.maxLimit)
  const offset = request.offset ?? 0

  const normalizeStart = Date.now()
  let normalized: NormalizedQuery
  try {
    normalized = normalizeQuery(request.query)
  } catch (cause) {
    throw appError(AppErrorCode.InvalidArgument, cause instanceof Error ? cause.message : String(cause), { cause })
  }
  const normalizeMs = Date.now() - normalizeStart

  const targets = resolveTargets(engine, request)
  if (targets.length === 0) {
    return {
      hits: [],
      total: 0,
      hasMore: false,
      partial: false,
      degraded: [],
      tookMs: Date.now() - started,
      timings: { normalize: normalizeMs, total: Date.now() - started },
      indexEpoch: ''
    }
  }

  if (normalized.text === '' && normalized.phrases.length === 0) {
    return browse(engine, request, targets, limit, offset, normalizeMs, started)
  }

  const degraded = new Set<DegradeReason>()
  let partial = false
  const budgetDeadline = started + engine.cfg.globalBudgetMs
  const profile = await engine.profile()
  let embedding: number[] | null = null
  if (profile === null) {
    degraded.add('vector_unavailable')
  } else {
    embedding = await queryEmbedding(engine, normalized, profile, degraded)
  }

  const keywordStart = Date.now()
  const recalls = await mapLimit(targets, engine.cfg.maxConcurrency, async (target) => {
    return recallTarget(engine, target, request, normalized, budgetDeadline)
  })
  const keywordMs = Date.now() - keywordStart

  for (const [index, recall] of recalls.entries()) {
    if (recall.timedOut) {
      // A per-target budget expiry never fails the whole search (A11).
      partial = true
      degraded.add('project_timeout')
      continue
    }
    if (recall.error !== undefined) {
      if (request.scope.kind === 'project') throw recall.error
      partial = true
      engine.cfg.logger?.warn('search: project recall failed', {
        projectId: targets[index]?.projectId ?? '',
        error: recall.error.message
      })
    }
  }
  // Phase 2: vector recall per target with pushed-down filters.
  const vectorStart = Date.now()
  const vectorHits: ChunkHit[][] = targets.map(() => [])
  if (embedding !== null && Date.now() >= budgetDeadline) {
    partial = true
    degraded.add('project_timeout')
  } else if (embedding !== null) {
    const results = await mapLimit(targets, engine.cfg.maxConcurrency, async (target, index) => {
      const recall = recalls[index]
      if (recall === undefined || recall.timedOut || recall.error !== undefined) return []
      if (recall.filter.restricted && (recall.filter.allowed ?? []).length === 0) return []
      try {
        return new VectorIndex(target.db, target.partition, { vectorAvailable: engine.vectorAvailable }).query(
          profile?.id ?? '',
          target.projectId,
          embedding,
          engine.cfg.vectorTopK * 2,
          { docIds: recall.filter.restricted ? recall.filter.allowed : undefined, excludeDocIds: recall.filter.excluded }
        )
      } catch (error) {
        // A vector failure is a degradation, never a hard error.
        degraded.add('vector_unavailable')
        engine.cfg.logger?.warn('search: vector recall failed', { error: String(error) })
        return []
      }
    })
    for (const [index, hits] of results.entries()) vectorHits[index] = hits
  }
  const vectorMs = Date.now() - vectorStart

  const fusionStart = Date.now()
  const aggregated: DocAgg[][] = []
  for (const [index, target] of targets.entries()) {
    const recall = recalls[index]
    if (recall === undefined) continue
    const targetVectorHits = vectorHits[index] ?? []
    try {
      const metas = loadDocMeta(target, [
        ...recall.hits.map((hit) => hit.docId),
        ...targetVectorHits.map((hit) => hit.docId)
      ])
      aggregated.push(fuseTarget(target, recall.hits, targetVectorHits, metas, engine))
    } catch (error) {
      if (request.scope.kind === 'project') throw error
      partial = true
      engine.cfg.logger?.warn('search: fusion failed for a project', { error: String(error) })
    }
  }
  const merged = mergeResults(aggregated)
  sortMerged(merged)
  const fusionMs = Date.now() - fusionStart

  const rerankStart = Date.now()
  if (mode !== 'off' && merged.length > 0) {
    if (engine.cfg.rerank === undefined) {
      if (mode === 'force') degraded.add('rerank_unavailable')
    } else {
      const reason = await rerank(engine, normalized.raw, merged, mode)
      if (reason !== null) degraded.add(reason)
    }
  }
  const rerankMs = Date.now() - rerankStart

  const epoch = engine.indexEpoch(targets)
  const total = merged.length
  const page = merged.slice(offset, Math.min(offset + limit, total))
  const hits: SearchHit[] = []
  for (const item of page) {
    try {
      hits.push(await assembleResult(engine, item, normalized.terms))
    } catch (error) {
      if (request.scope.kind === 'project') throw error
      engine.cfg.logger?.warn('search: result assembly failed', { error: String(error) })
    }
  }

  return {
    hits,
    total,
    hasMore: offset + hits.length < total,
    partial,
    degraded: [...degraded],
    tookMs: Date.now() - started,
    timings: {
      normalize: normalizeMs,
      keyword: keywordMs,
      vector: vectorMs,
      fusion: fusionMs,
      rerank: rerankMs,
      total: Date.now() - started
    },
    indexEpoch: epoch
  }
}

function resolveTargets(engine: SearchEngine, request: SearchRequest): RuntimeTarget[] {
  if (request.scope.kind === 'project') {
    const projectId = request.scope.projectId ?? ''
    const row = engine.workspace.global.db
      .prepare(`SELECT name, status FROM projects WHERE id = ?`)
      .get(projectId) as { name: string; status: string } | undefined
    if (row === undefined) {
      throw appError(AppErrorCode.NotFound, `project ${projectId} does not exist`, { details: { projectId } })
    }
    const targets: RuntimeTarget[] = [
      makeTarget(
        projectId,
        projectId,
        row.name,
        row.status === 'archived',
        () => engine.workspace.project(projectId).db,
        'project'
      )
    ]
    const linked = linkedPaperDocIds(engine.workspace.project(projectId).db)
    if (linked.length > 0) {
      targets.push(
        makeTarget(
          `global:${projectId}`,
          '',
          '',
          false,
          () => engine.workspace.global.db,
          'global',
          linked
        )
      )
    }
    return targets
  }

  const projects = engine.workspace.global.db
    .prepare(`SELECT id, name, status FROM projects ORDER BY name, id`)
    .all() as Array<{ id: string; name: string; status: string }>
  const targets: RuntimeTarget[] = []
  for (const project of projects) {
    try {
      // Validate that the project opens, then resolve the handle lazily on use.
      engine.workspace.project(project.id)
      targets.push(
        makeTarget(
          project.id,
          project.id,
          project.name,
          project.status === 'archived',
          () => engine.workspace.project(project.id).db,
          'project'
        )
      )
    } catch (error) {
      engine.cfg.logger?.warn('search: project could not be opened; skipped', {
        projectId: project.id,
        error: String(error)
      })
    }
  }
  targets.push(makeTarget('global', '', '', false, () => engine.workspace.global.db, 'global'))
  return targets
}

async function recallTarget(
  engine: SearchEngine,
  target: RuntimeTarget,
  request: SearchRequest,
  normalized: NormalizedQuery,
  budgetDeadline: number
): Promise<RecallResult> {
  const budget = engine.cfg.projectBudget?.(target.projectId) ?? engine.cfg.projectTimeoutMs
  const deadline = Math.min(Date.now() + budget, budgetDeadline)
  if (budget <= 0 || Date.now() >= budgetDeadline) {
    return { hits: [], filter: { restricted: false, excluded: [] }, timedOut: true }
  }
  try {
    const filter = resolveDocFilter(target, request.filters ?? {}, request.includeDeleted === true, engine.workspace.global.db)
    if (filter.restricted && (filter.allowed ?? []).length === 0) {
      return { hits: [], filter, timedOut: false }
    }
    const hits = new KeywordIndex(target.db, target.partition).query(
      target.projectId,
      retrievalText(normalized),
      engine.cfg.keywordTopK,
      {
        docIds: filter.restricted ? filter.allowed : undefined,
        excludeDocIds: filter.excluded
      }
    )
    const phrases = normalized.phrases.length > 0
    const filtered = phrases ? hits.filter((hit) => containsPhrases(foldRunes(hit.text), normalized.phrases)) : hits
    if (Date.now() >= deadline) {
      return { hits: [], filter, timedOut: true }
    }
    return { hits: filtered, filter, timedOut: false }
  } catch (error) {
    return {
      hits: [],
      filter: { restricted: false, excluded: [] },
      timedOut: false,
      error: error instanceof Error ? error : new Error(String(error))
    }
  }
}

async function queryEmbedding(
  engine: SearchEngine,
  normalized: NormalizedQuery,
  profile: EmbeddingProfile,
  degraded: Set<DegradeReason>
): Promise<number[] | null> {
  const port = engine.cfg.embedding
  if (port === undefined || !engine.vectorAvailable) {
    degraded.add('vector_unavailable')
    return null
  }
  const controller = new AbortController()
  try {
    const vectors = await withTimeout(
      port.embed([retrievalText(normalized)], controller.signal),
      engine.cfg.rerankTimeoutMs,
      controller
    )
    const vector = vectors[0]
    if (vector === undefined || vector.length === 0 || (profile.dim > 0 && vector.length !== profile.dim)) {
      degraded.add('embedding_failed')
      return null
    }
    return [...vector]
  } catch (error) {
    degraded.add('embedding_failed')
    engine.cfg.logger?.warn('search: query embedding failed', { error: String(error) })
    return null
  }
}

function withTimeout<T>(promise: Promise<T>, milliseconds: number, controller: AbortController): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => {
      controller.abort()
      reject(new SearchTimeout())
    }, milliseconds)
    timer.unref?.()
    promise.then(
      (value) => {
        clearTimeout(timer)
        resolve(value)
      },
      (error: unknown) => {
        clearTimeout(timer)
        reject(error)
      }
    )
  })
}

/** Runs `fn` over items with at most `limit` concurrent promises. */
async function mapLimit<T, R>(
  items: readonly T[],
  limit: number,
  fn: (item: T, index: number) => Promise<R>
): Promise<R[]> {
  const results = new Array<R>(items.length)
  let cursor = 0
  const workers = Array.from({ length: Math.max(1, Math.min(limit, items.length)) }, async () => {
    for (;;) {
      const index = cursor
      cursor += 1
      if (index >= items.length) return
      const item = items[index]
      if (item === undefined) return
      results[index] = await fn(item, index)
    }
  })
  await Promise.all(workers)
  return results
}

/* ------------------------------------------------------------------- fusion */

export function fuseTarget(
  target: RuntimeTarget,
  keywordHits: readonly ChunkHit[],
  vectorHits: readonly ChunkHit[],
  metas: Map<string, DocMeta>,
  engine: SearchEngine
): DocAgg[] {
  const k = engine.cfg.rrfK
  const byChunk = new Map<string, ChunkAgg>()
  const order: string[] = []
  const get = (hit: ChunkHit): ChunkAgg => {
    const existing = byChunk.get(hit.chunkId)
    if (existing !== undefined) return existing
    const created: ChunkAgg = { hit, score: 0, keywordRank: 0, vectorRank: 0 }
    byChunk.set(hit.chunkId, created)
    order.push(hit.chunkId)
    return created
  }
  for (const [index, hit] of keywordHits.entries()) {
    const agg = get(hit)
    agg.keywordRank = index + 1
    agg.score += engine.cfg.keywordWeight / (k + agg.keywordRank)
  }
  for (const [index, hit] of vectorHits.entries()) {
    const agg = get(hit)
    agg.vectorRank = index + 1
    agg.score += engine.cfg.vectorWeight / (k + agg.vectorRank)
  }

  const byDoc = new Map<string, DocAgg>()
  const docOrder: string[] = []
  for (const chunkId of order) {
    const chunk = byChunk.get(chunkId)
    if (chunk === undefined) continue
    let doc = byDoc.get(chunk.hit.docId)
    if (doc === undefined) {
      doc = {
        target,
        meta: metas.get(chunk.hit.docId) ?? {
          docId: chunk.hit.docId,
          source: '',
          entityType: '',
          entityId: '',
          title: '',
          lang: '',
          updatedAt: new Date(0),
          status: ''
        },
        chunks: [],
        score: 0,
        keywordRank: 0,
        vectorRank: 0,
        primary: false
      }
      byDoc.set(chunk.hit.docId, doc)
      docOrder.push(chunk.hit.docId)
    }
    doc.chunks.push(chunk)
  }

  const out: DocAgg[] = []
  for (const docId of docOrder) {
    const doc = byDoc.get(docId)
    if (doc === undefined) continue
    doc.chunks.sort((left, right) => {
      if (left.score !== right.score) return right.score - left.score
      return left.hit.chunkId < right.hit.chunkId ? -1 : left.hit.chunkId > right.hit.chunkId ? 1 : 0
    })
    if (doc.chunks.length > 3) doc.chunks = doc.chunks.slice(0, 3)
    for (const chunk of doc.chunks) {
      if (chunk.score > doc.score) doc.score = chunk.score
      if (chunk.keywordRank > 0 && (doc.keywordRank === 0 || chunk.keywordRank < doc.keywordRank)) {
        doc.keywordRank = chunk.keywordRank
      }
      if (chunk.vectorRank > 0 && (doc.vectorRank === 0 || chunk.vectorRank < doc.vectorRank)) {
        doc.vectorRank = chunk.vectorRank
      }
    }
    doc.primary = doc.meta.source === 'paper_fulltext'
    out.push(doc)
  }
  return out
}

/** Combines target aggregations; one global paper merges metadata + full text. */
export function mergeResults(aggregated: readonly DocAgg[][]): MergedResult[] {
  const groups = new Map<string, MergedResult>()
  const order: string[] = []
  for (const docs of aggregated) {
    for (const doc of docs) {
      const key =
        doc.target.partition === 'global' && doc.meta.entityType === 'paper'
          ? `global/paper:${doc.meta.entityId}`
          : `${doc.target.key}/${doc.meta.docId}`
      let merged = groups.get(key)
      if (merged === undefined) {
        merged = { docs: [], score: 0, keywordRank: 0, vectorRank: 0, rerank: null }
        groups.set(key, merged)
        order.push(key)
      }
      merged.docs.push(doc)
      if (doc.score > merged.score) merged.score = doc.score
      if (doc.keywordRank > 0 && (merged.keywordRank === 0 || doc.keywordRank < merged.keywordRank)) {
        merged.keywordRank = doc.keywordRank
      }
      if (doc.vectorRank > 0 && (merged.vectorRank === 0 || doc.vectorRank < merged.vectorRank)) {
        merged.vectorRank = doc.vectorRank
      }
    }
  }
  const out: MergedResult[] = []
  for (const key of order) {
    const merged = groups.get(key)
    if (merged === undefined) continue
    merged.docs.sort((left, right) => Number(right.primary) - Number(left.primary))
    out.push(merged)
  }
  return out
}

export function sortedKey(merged: MergedResult): string {
  const doc = merged.docs[0]
  return doc === undefined ? '' : `${doc.target.key}/${doc.meta.docId}`
}

export function sortMerged(merged: MergedResult[]): void {
  merged.sort((left, right) => {
    if (left.score !== right.score) return right.score - left.score
    return sortedKey(left) < sortedKey(right) ? -1 : sortedKey(left) > sortedKey(right) ? 1 : 0
  })
}

function rerankText(merged: MergedResult): string {
  const parts: string[] = []
  if (merged.docs[0] !== undefined) parts.push(merged.docs[0].meta.title)
  let count = 0
  for (const doc of merged.docs) {
    for (const chunk of doc.chunks) {
      if (count >= 2) break
      parts.push(chunk.hit.text)
      count += 1
    }
  }
  return parts.join('\n')
}

async function rerank(
  engine: SearchEngine,
  query: string,
  merged: MergedResult[],
  mode: RerankMode
): Promise<DegradeReason | null> {
  const port = engine.cfg.rerank
  if (port === undefined) return mode === 'force' ? 'rerank_unavailable' : null
  const count = Math.min(engine.cfg.rerankTopN, merged.length)
  if (count === 0) return null
  const documents = merged.slice(0, count).map(rerankText)
  const controller = new AbortController()
  try {
    const hits = await withTimeout(
      port.rerank(query, documents, count, controller.signal),
      engine.cfg.rerankTimeoutMs,
      controller
    )
    if (hits.length === 0) return 'rerank_unavailable'
    const scores = new Map<number, number>()
    for (const hit of hits) {
      if (hit.index >= 0 && hit.index < count) scores.set(hit.index, hit.score)
    }
    const head = merged.slice(0, count)
    for (const [index, item] of head.entries()) {
      const score = scores.get(index)
      if (score !== undefined) item.rerank = score
    }
    head.sort((left, right) => {
      const leftScore = left.rerank
      const rightScore = right.rerank
      if (leftScore !== null && rightScore !== null) {
        if (leftScore !== rightScore) return rightScore - leftScore
        return right.score - left.score
      }
      if (leftScore !== null) return -1
      if (rightScore !== null) return 1
      return right.score - left.score
    })
    for (const [index, item] of head.entries()) merged[index] = item
    return null
  } catch (error) {
    return error instanceof SearchTimeout ? 'rerank_timeout' : 'rerank_unavailable'
  }
}

/* ----------------------------------------------------------------- assembly */

function bestChunk(merged: MergedResult): ChunkAgg | undefined {
  // Prefer the leading document (full text leads its paper group) so a paper
  // hit carries the PDF page locator, not a metadata field locator.
  for (const doc of merged.docs) {
    let best: ChunkAgg | undefined
    for (const chunk of doc.chunks) {
      if (best === undefined || chunk.score > best.score) best = chunk
    }
    if (best !== undefined) return best
  }
  return undefined
}

function parseLocator(json: string): Locator {
  if (json === '') return {}
  try {
    return JSON.parse(json) as Locator
  } catch {
    return {}
  }
}

async function assembleResult(engine: SearchEngine, merged: MergedResult, terms: readonly string[]): Promise<SearchHit> {
  const primary = merged.docs[0]
  if (primary === undefined) throw new Error('search: empty result group')
  const best = bestChunk(merged)
  const locator = best === undefined ? {} : parseLocator(best.hit.locatorJson)
  let updatedAt = primary.meta.updatedAt
  let deleted = primary.meta.status === 'deleted'
  for (const doc of merged.docs) {
    if (doc.meta.updatedAt > updatedAt) updatedAt = doc.meta.updatedAt
    if (doc.meta.status === 'deleted') deleted = true
  }
  let title = primary.meta.title
  if (title.trim() === '' && best !== undefined) title = truncateRunes(best.hit.text, 80)
  const snippets: Snippet[] = []
  for (const doc of merged.docs) {
    for (const chunk of doc.chunks) {
      if (snippets.length >= 3) break
      snippets.push(buildSnippet(chunk.hit.text, terms, engine.cfg.snippetRunes, parseLocator(chunk.hit.locatorJson)))
    }
  }
  const score: ScoreBreakdown = {
    keywordRank: merged.keywordRank,
    vectorRank: merged.vectorRank,
    fusion: merged.score,
    rerank: merged.rerank
  }
  const target = await jumpTargetFor(engine, primary.target, {
    entityType: primary.meta.entityType as EntityType,
    entityId: primary.meta.entityId,
    projectId: primary.target.projectId,
    source: primary.meta.source as SourceType,
    locator
  })
  return {
    docId: primary.meta.docId,
    source: primary.meta.source as SourceType,
    entityType: primary.meta.entityType as EntityType,
    entityId: primary.meta.entityId,
    projectId: primary.target.projectId,
    projectName: primary.target.name,
    archived: primary.target.archived,
    deleted,
    title,
    snippets,
    locator,
    score,
    updatedAt: updatedAt.toISOString(),
    target
  }
}

/* ------------------------------------------------------------------- browse */

interface BrowseRow {
  target: RuntimeTarget
  meta: DocMeta
}

async function browse(
  engine: SearchEngine,
  request: SearchRequest,
  targets: RuntimeTarget[],
  limit: number,
  offset: number,
  normalizeMs: number,
  started: number
): Promise<SearchResponse> {
  const rows: BrowseRow[] = []
  let total = 0
  const need = offset + limit
  for (const target of targets) {
    let filter: DocFilter
    try {
      filter = resolveDocFilter(target, request.filters ?? {}, request.includeDeleted === true, engine.workspace.global.db)
    } catch (error) {
      if (request.scope.kind === 'project') throw error
      continue
    }
    if (filter.restricted && (filter.allowed ?? []).length === 0) continue
    const conditions: string[] = []
    const args: string[] = []
    if (target.partition === 'project') {
      conditions.push('project_id = ?')
      args.push(target.projectId)
    }
    if (filter.restricted) {
      conditions.push(`doc_id IN (${(filter.allowed ?? []).map(() => '?').join(',')})`)
      args.push(...(filter.allowed ?? []))
    }
    if (filter.excluded.length > 0) {
      conditions.push(`doc_id NOT IN (${filter.excluded.map(() => '?').join(',')})`)
      args.push(...filter.excluded)
    }
    const where = conditions.length > 0 ? ` WHERE ${conditions.join(' AND ')}` : ''
    total += (
      target.db.prepare(`SELECT COUNT(*) AS n FROM ${target.tables.docs}${where}`).get(...args) as { n: number }
    ).n
    const docs = target.db
      .prepare(
        `SELECT doc_id, source, entity_type, entity_id, title, lang, updated_at, index_status
         FROM ${target.tables.docs}${where} ORDER BY updated_at DESC, doc_id LIMIT ?`
      )
      .all(...args, need) as Array<{
      doc_id: string
      source: string
      entity_type: string
      entity_id: string
      title: string
      lang: string
      updated_at: string
      index_status: string
    }>
    for (const doc of docs) {
      rows.push({
        target,
        meta: {
          docId: doc.doc_id,
          source: doc.source,
          entityType: doc.entity_type,
          entityId: doc.entity_id,
          title: doc.title,
          lang: doc.lang,
          updatedAt: parseDbTime(doc.updated_at),
          status: doc.index_status
        }
      })
    }
  }
  rows.sort((left, right) => {
    if (left.meta.updatedAt.getTime() !== right.meta.updatedAt.getTime()) {
      return right.meta.updatedAt.getTime() - left.meta.updatedAt.getTime()
    }
    return left.meta.docId < right.meta.docId ? -1 : left.meta.docId > right.meta.docId ? 1 : 0
  })
  const page = rows.slice(offset, offset + limit)
  const hits: SearchHit[] = []
  for (const row of page) {
    try {
      hits.push(
        await assembleResult(
          engine,
          {
            docs: [
              {
                target: row.target,
                meta: row.meta,
                chunks: [],
                score: 0,
                keywordRank: 0,
                vectorRank: 0,
                primary: false
              }
            ],
            score: 0,
            keywordRank: 0,
            vectorRank: 0,
            rerank: null
          },
          []
        )
      )
    } catch (error) {
      engine.cfg.logger?.warn('search: browse assembly failed', { error: String(error) })
    }
  }
  const epoch = engine.indexEpoch(targets)
  return {
    hits,
    total,
    hasMore: offset + hits.length < total,
    partial: false,
    degraded: [],
    tookMs: Date.now() - started,
    timings: { normalize: normalizeMs, total: Date.now() - started },
    indexEpoch: epoch
  }
}


function parseDbTime(value: string): Date {
  const parsed = Date.parse(value)
  return Number.isNaN(parsed) ? new Date(0) : new Date(parsed)
}
