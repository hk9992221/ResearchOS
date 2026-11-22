/**
 * Import: read-only inspection (§5.3.2 phase one) and staged restore (phase
 * two). Every manifest entry is hashed on both passes; a missing entry is
 * `NOT_FOUND`, a changed one is `INTEGRITY`, and no failure leaves a project
 * directory, global row, activity or copied PDF behind.
 */
import { createHash, type Hash } from 'node:crypto'
import {
  closeSync,
  existsSync,
  lstatSync,
  mkdirSync,
  openSync,
  readSync,
  renameSync,
  rmdirSync,
  rmSync,
  statSync,
  writeSync
} from 'node:fs'
import * as path from 'node:path'

import {
  AppErrorCode,
  type ExportFormat,
  type ImportFileCheck,
  type ImportInspectPlan,
  type ImportIssue,
  type ImportResult,
  type JsonObject
} from '@researchos/shared'

import { formatTimestamp, type Clock } from '../../clock'
import { openConnection, openDb, type Db } from '../../db/open'
import { targetVersion } from '../../db/migrate'
import { appError, isAppError } from '../../errors'
import type { EventBus } from '../../events'
import { isValidId, newId } from '../../ids'
import type { WorkspaceManager } from '../../workspace'
import {
  detectArchivePrefix,
  detectBundleFormat,
  listBundle,
  readBundleFile,
  sameOrInside,
  walkBundlePrefixed,
  type BundleEntryVisitor
} from './bundle'
import { ioError } from './io-errors'
import { IMPORT_PLAN_TTL_MS, IMPORT_STAGING_PREFIX, MANIFEST_VERSION } from './types'
import type {
  ImportFormat,
  ImportPlanState,
  ManifestData,
  ManifestFileEntry,
  PaperMetaEntry,
  ProjectMeta
} from './types'

export interface ImportDeps {
  workspace: WorkspaceManager
  clock: Clock
  events?: EventBus
  reindex?: (projectId: string) => Promise<void> | void
}

export interface ImportContext {
  signal?: AbortSignal
  report?: (percent: number, stage: string) => void
}

interface PaperMerge {
  entry: PaperMetaEntry
  finalId: string
  insert: boolean
  copied: boolean
  fileHash: string
  fileSize: number
  fileState: 'ok' | 'missing'
}

function abortOf(signal: AbortSignal | undefined): void {
  if (signal?.aborted === true) {
    throw appError(AppErrorCode.Interrupted, 'import cancelled')
  }
}

function invalid(message: string, details: Record<string, unknown> = {}): never {
  throw appError(AppErrorCode.InvalidArgument, message, details)
}

function jsonObjectOf(raw: Buffer, label: string, sourcePath: string): JsonObject {
  let value: unknown
  try {
    value = JSON.parse(raw.toString('utf8'))
  } catch {
    return invalid(`cannot parse ${label}`, { source_path: sourcePath, entry: label })
  }
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return invalid(`${label} is not a JSON object`, { source_path: sourcePath, entry: label })
  }
  return value as JsonObject
}

function parseManifest(raw: Buffer, sourcePath: string): ManifestData {
  const value = jsonObjectOf(raw, 'manifest.json', sourcePath)
  const manifestVersion = value['manifest_version']
  if (manifestVersion !== MANIFEST_VERSION) {
    return invalid(`unsupported manifest_version ${JSON.stringify(manifestVersion)}`, {
      source_path: sourcePath,
      expected: MANIFEST_VERSION
    })
  }
  const appValue = value['app']
  if (typeof appValue !== 'object' || appValue === null) {
    return invalid('manifest.app is missing', { source_path: sourcePath })
  }
  const app = appValue as JsonObject
  const projectValue = value['project']
  if (typeof projectValue !== 'object' || projectValue === null) {
    return invalid('manifest.project is missing', { source_path: sourcePath })
  }
  const project = projectValue as JsonObject
  const rawFiles = value['files']
  if (!Array.isArray(rawFiles)) {
    return invalid('manifest.files is not an array', { source_path: sourcePath })
  }
  const files: ManifestFileEntry[] = []
  const seen = new Set<string>()
  for (const candidate of rawFiles) {
    if (typeof candidate !== 'object' || candidate === null) {
      return invalid('manifest.files contains a non-object entry', { source_path: sourcePath })
    }
    const entry = candidate as JsonObject
    const bundlePath = entry['path']
    const bytes = entry['bytes']
    const sha = entry['sha256']
    if (typeof bundlePath !== 'string' || bundlePath === '') {
      return invalid('manifest entry has no path', { source_path: sourcePath })
    }
    if (bundlePath.includes('\\') || bundlePath.split('/').some((segment) => segment === '' || segment === '..' || segment === '.')) {
      return invalid(`manifest entry ${bundlePath} is unsafe`, { source_path: sourcePath, path: bundlePath })
    }
    if (seen.has(bundlePath)) {
      return invalid(`manifest lists ${bundlePath} twice`, { source_path: sourcePath, path: bundlePath })
    }
    if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes < 0) {
      return invalid(`manifest entry ${bundlePath} has an invalid size`, {
        source_path: sourcePath,
        path: bundlePath
      })
    }
    if (typeof sha !== 'string' || !/^[0-9a-f]{64}$/i.test(sha)) {
      return invalid(`manifest entry ${bundlePath} has an invalid sha256`, {
        source_path: sourcePath,
        path: bundlePath
      })
    }
    seen.add(bundlePath)
    files.push({ path: bundlePath, bytes, sha256: sha.toLowerCase() })
  }
  return {
    manifest_version: manifestVersion,
    app: {
      name: typeof app['name'] === 'string' ? app['name'] : 'ResearchOS',
      version: typeof app['version'] === 'string' ? app['version'] : '',
      schema_global: Number(app['schema_global'] ?? 0),
      schema_project: Number(app['schema_project'] ?? 0)
    },
    exported_at: typeof value['exported_at'] === 'string' ? value['exported_at'] : '',
    format: (typeof value['format'] === 'string' ? value['format'] : 'folder') as ExportFormat,
    project: {
      id: typeof project['id'] === 'string' ? project['id'] : '',
      name: typeof project['name'] === 'string' ? project['name'] : '',
      status: typeof project['status'] === 'string' ? project['status'] : 'active'
    },
    counts: {
      notes: Number((value['counts'] as JsonObject | undefined)?.['notes'] ?? 0),
      papers: Number((value['counts'] as JsonObject | undefined)?.['papers'] ?? 0),
      attachments: Number((value['counts'] as JsonObject | undefined)?.['attachments'] ?? 0),
      files: Number((value['counts'] as JsonObject | undefined)?.['files'] ?? files.length),
      bytes: Number((value['counts'] as JsonObject | undefined)?.['bytes'] ?? 0)
    },
    checksum: { algorithm: 'sha256' },
    files,
    papers: Array.isArray(value['papers'])
      ? (value['papers'] as JsonObject[]).map((paper) => ({
          id: String(paper['id'] ?? ''),
          zotero_key: String(paper['zotero_key'] ?? ''),
          source_pdf: String(paper['source_pdf'] ?? ''),
          file_hash: String(paper['file_hash'] ?? ''),
          missing: paper['missing'] === true
        }))
      : [],
    missing: Array.isArray(value['missing'])
      ? (value['missing'] as JsonObject[]).map((ref) => ({
          kind: String(ref['kind'] ?? ''),
          ref: String(ref['ref'] ?? ''),
          recorded_path: String(ref['recorded_path'] ?? '')
        }))
      : [],
    warnings: Array.isArray(value['warnings'])
      ? (value['warnings'] as JsonObject[]).map((warning) => ({
          code: String(warning['code'] ?? ''),
          path: String(warning['path'] ?? '')
        }))
      : []
  }
}

function parseProjectMeta(raw: Buffer, sourcePath: string): ProjectMeta {
  const value = jsonObjectOf(raw, 'meta/project.json', sourcePath)
  const id = value['id']
  if (typeof id !== 'string' || !isValidId(id)) {
    return invalid('meta/project.json has an invalid project id', { source_path: sourcePath })
  }
  const name = typeof value['name'] === 'string' ? value['name'].trim() : ''
  if (name === '') return invalid('meta/project.json has no project name', { source_path: sourcePath })
  return {
    id,
    name,
    summary: typeof value['summary'] === 'string' ? value['summary'] : '',
    goals: typeof value['goals'] === 'string' ? value['goals'] : '',
    status: value['status'] === 'archived' ? 'archived' : 'active',
    createdAt: typeof value['created_at'] === 'string' ? value['created_at'] : ''
  }
}

function parsePapers(raw: Buffer, sourcePath: string): PaperMetaEntry[] {
  let value: unknown
  try {
    value = JSON.parse(raw.toString('utf8'))
  } catch {
    return invalid('cannot parse meta/papers.json', { source_path: sourcePath })
  }
  if (!Array.isArray(value)) return invalid('meta/papers.json is not an array', { source_path: sourcePath })
  const papers: PaperMetaEntry[] = []
  for (const candidate of value) {
    if (typeof candidate !== 'object' || candidate === null) continue
    const entry = candidate as JsonObject
    if (typeof entry['id'] !== 'string' || entry['id'] === '') continue
    papers.push({
      id: entry['id'],
      zotero_key: typeof entry['zotero_key'] === 'string' ? entry['zotero_key'] : '',
      file_hash: typeof entry['file_hash'] === 'string' ? entry['file_hash'] : '',
      file_size: Number(entry['file_size'] ?? 0),
      source_pdf: typeof entry['source_pdf'] === 'string' ? entry['source_pdf'] : '',
      missing: entry['missing'] === true,
      metadata: entry['metadata']
    })
  }
  return papers
}

function conflictOf(workspace: WorkspaceManager, project: ProjectMeta): { id: boolean; name: boolean } {
  const db = workspace.global.db
  const idRow = db.prepare('SELECT id FROM projects WHERE id = ?').get(project.id) as { id: string } | undefined
  const nameRow = db
    .prepare('SELECT id FROM projects WHERE name = ? ORDER BY created_at, id LIMIT 1')
    .get(project.name) as { id: string } | undefined
  return { id: idRow !== undefined, name: nameRow !== undefined }
}

async function verifyEntry(
  sourcePath: string,
  format: ImportFormat,
  prefix: string,
  expected: Map<string, ManifestFileEntry>,
  checks: ImportFileCheck[]
): Promise<void> {
  const issues: ImportIssue[] = []
  const visitor: BundleEntryVisitor = {
    start: (bundlePath, size) => {
      const entry = expected.get(bundlePath)
      if (entry === undefined) return
      if (size !== undefined && entry.bytes > 0 && size !== entry.bytes) {
        issues.push({ relativePath: bundlePath, reason: 'size_mismatch' })
      }
    },
    chunk: () => undefined,
    end: (bundlePath, bytes, sha256) => {
      const entry = expected.get(bundlePath)
      if (entry === undefined) return
      const matches = bytes === entry.bytes && sha256.toLowerCase() === entry.sha256
      checks.push({ relativePath: bundlePath, bytes, sha256, matchesManifest: matches })
      if (!matches) {
        const reason = bytes !== entry.bytes ? 'size_mismatch' : 'hash_mismatch'
        issues.push({ relativePath: bundlePath, reason })
      }
    }
  }
  await walkBundlePrefixed(sourcePath, format, prefix, visitor)
  const checked = new Set(checks.map((check) => check.relativePath))
  for (const entry of expected.values()) {
    if (!checked.has(entry.path)) {
      issues.push({ relativePath: entry.path, reason: 'missing' })
    }
  }
  if (issues.length > 0) {
    const missing = issues.filter((issue) => issue.reason === 'missing')
    if (missing.length > 0) {
      throw appError(AppErrorCode.NotFound, `bundle is missing ${missing.length} file(s)`, {
        source_path: sourcePath,
        issues
      })
    }
    throw appError(AppErrorCode.Integrity, `bundle failed integrity verification`, {
      source_path: sourcePath,
      issues
    })
  }
}

/** Phase one: inspect a container without writing to the workspace. */
export async function inspectBundle(deps: ImportDeps, rawPath: string): Promise<ImportPlanState> {
  const sourcePath = path.resolve(rawPath.trim())
  if (!existsSync(sourcePath)) {
    throw appError(AppErrorCode.NotFound, `bundle ${sourcePath} does not exist`, { source_path: sourcePath })
  }
  const format = detectBundleFormat(sourcePath)
  const names = await listBundle(sourcePath, format)
  const prefix = detectArchivePrefix(names, sourcePath)

  const manifestRaw = await readBundleFile(sourcePath, format, prefix, 'manifest.json', 8 << 20)
  const manifest = parseManifest(manifestRaw, sourcePath)
  if (manifest.app.schema_project > targetVersion('project')) {
    throw appError(
      AppErrorCode.SchemaTooNew,
      `bundle project schema ${manifest.app.schema_project} is newer than this build (${targetVersion('project')})`,
      { source_path: sourcePath, schema_project: manifest.app.schema_project, target: targetVersion('project') }
    )
  }
  const projectRaw = await readBundleFile(sourcePath, format, prefix, 'meta/project.json', 4 << 20)
  const meta = parseProjectMeta(projectRaw, sourcePath)
  const papersRaw = await readBundleFile(sourcePath, format, prefix, 'meta/papers.json', 16 << 20)
  const papers = parsePapers(papersRaw, sourcePath)

  const dbEntry = manifest.files.find(
    (entry) => entry.path === `workspace/projects/${meta.id}/project.db`
  )
  if (dbEntry === undefined) {
    return invalid('bundle has no project database', { source_path: sourcePath, project_id: meta.id })
  }

  const expected = new Map(manifest.files.map((entry) => [entry.path, entry]))
  const checks: ImportFileCheck[] = []
  await verifyEntry(sourcePath, format, prefix, expected, checks)

  const conflict = conflictOf(deps.workspace, meta)
  const exportedAt = manifest.exported_at === '' ? null : manifest.exported_at
  const plan: ImportInspectPlan = {
    planId: newId(deps.clock),
    container: format,
    sourcePath,
    projectId: meta.id,
    projectName: meta.name,
    schemaVersion: manifest.app.schema_project,
    schemaGlobal: manifest.app.schema_global,
    exportedAt,
    expiresAt: formatTimestamp(new Date(deps.clock.now().getTime() + IMPORT_PLAN_TTL_MS)),
    fileCount: manifest.files.length,
    totalBytes: manifest.files.reduce((sum, entry) => sum + entry.bytes, 0),
    files: checks,
    counts: {
      notes: manifest.counts.notes,
      papers: manifest.counts.papers,
      attachments: manifest.counts.attachments,
      files: manifest.counts.files,
      bytes: manifest.counts.bytes
    },
    issues: [],
    idConflict: conflict.id,
    nameConflict: conflict.name,
    warnings: manifest.warnings.map((warning) => `${warning.code}: ${warning.path}`),
    canImport: true
  }
  return {
    plan,
    manifest,
    meta,
    papers,
    sourcePath,
    format,
    archivePrefix: prefix,
    expiresAt: deps.clock.now().getTime() + IMPORT_PLAN_TTL_MS
  }
}

function maxSchemaVersion(dbPath: string): number {
  const connection = openConnection(dbPath, { readonly: true, loadVector: false, verifyFts: false })
  try {
    const row = connection.db
      .prepare('SELECT coalesce(max(version), 0) AS version FROM schema_migrations')
      .get() as { version: number } | undefined
    return Number(row?.version ?? 0)
  } finally {
    connection.db.close()
  }
}

function rewriteProjectIdentity(db: Db, oldId: string, newIdValue: string): void {
  const prefix = `projects/${oldId}/`
  db.transaction(() => {
    db.prepare('UPDATE project_papers SET project_id = ? WHERE project_id = ?').run(newIdValue, oldId)
    db.prepare('UPDATE notes SET file_path = ? || substr(file_path, ?) WHERE file_path LIKE ?').run(
      `projects/${newIdValue}/`,
      prefix.length + 1,
      `${prefix}%`
    )
    const indexDocs = db
      .prepare(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'index_docs'`)
      .get() as { name: string } | undefined
    if (indexDocs !== undefined) {
      db.prepare('UPDATE index_docs SET project_id = ? WHERE project_id = ?').run(newIdValue, oldId)
    }
    const searchState = db
      .prepare(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'search_index_state'`)
      .get() as { name: string } | undefined
    if (searchState !== undefined) {
      db.prepare('DELETE FROM search_index_state WHERE project_id = ?').run(oldId)
    }
  }).immediate()
}

function resolveImportName(workspace: WorkspaceManager, desired: string, archivedId: string): string {
  const name = desired.trim() === '' ? '导入项目' : desired.trim()
  const existing = workspace.global.db
    .prepare('SELECT id FROM projects WHERE name = ? LIMIT 1')
    .get(name) as { id: string } | undefined
  if (existing === undefined) return name
  void archivedId
  for (let index = 2; index < 100; index += 1) {
    const candidate = `${name}（导入 ${index}）`
    const row = workspace.global.db
      .prepare('SELECT id FROM projects WHERE name = ? LIMIT 1')
      .get(candidate) as { id: string } | undefined
    if (row === undefined) return candidate
  }
  throw appError(AppErrorCode.Conflict, `cannot resolve a free project name for ${JSON.stringify(name)}`, {
    name
  })
}

function libraryIdOf(metadata: unknown): string {
  if (typeof metadata !== 'object' || metadata === null) return '0'
  const record = metadata as Record<string, unknown>
  for (const key of ['libraryID', 'library_id', 'libraryId', 'zotero_library_id']) {
    const value = record[key]
    if (value !== undefined && String(value).trim() !== '') return String(value).trim()
  }
  return '0'
}

function probePath(kind: string, target: string): boolean {
  try {
    const info = statSync(target)
    if (kind === 'directory' || kind === 'git_repository') return info.isDirectory()
    return info.isFile()
  } catch {
    return false
  }
}

function probeExternalRefs(db: Db, now: string): number {
  const rows = db.prepare('SELECT id, ref_kind, path FROM external_refs').all() as Array<{
    id: string
    ref_kind: string
    path: string
  }>
  let broken = 0
  db.transaction(() => {
    const update = db.prepare(
      'UPDATE external_refs SET status = ?, last_checked_at = ?, updated_at = ? WHERE id = ?'
    )
    for (const row of rows) {
      const exists = probePath(row.ref_kind, row.path)
      if (!exists) broken += 1
      update.run(exists ? 'ok' : 'broken', now, now, row.id)
    }
  }).immediate()
  return broken
}

interface RestoredPaperAction {
  entry: PaperMetaEntry
  finalId: string
  libraryId: string
  zoteroKey: string
  insert: boolean
  copySource: string | null
  fileHash: string
  fileSize: number
  metadataJson: string
}

function resolvePaperActions(
  deps: ImportDeps,
  state: ImportPlanState,
  rawRoot: string
): { actions: RestoredPaperAction[]; remap: Map<string, string> } {
  const actions: RestoredPaperAction[] = []
  const remap = new Map<string, string>()
  for (const entry of state.papers) {
    if (!isValidId(entry.id)) {
      state.plan.warnings.push(`paper_invalid: ${entry.id}`)
      continue
    }
    const libraryId = libraryIdOf(entry.metadata)
    const zoteroKey = entry.zotero_key
    const byId = deps.workspace.global.db
      .prepare('SELECT id FROM papers WHERE id = ?')
      .get(entry.id) as { id: string } | undefined
    let existingId = byId?.id
    if (existingId === undefined && zoteroKey !== '') {
      const byKey = deps.workspace.global.db
        .prepare('SELECT id FROM papers WHERE zotero_library_id = ? AND zotero_key = ?')
        .get(libraryId, zoteroKey) as { id: string } | undefined
      existingId = byKey?.id
    }
    const finalId = existingId ?? entry.id
    remap.set(entry.id, finalId)

    const sourcePdf = entry.source_pdf === '' ? `workspace/library/papers/${entry.id}/source.pdf` : entry.source_pdf
    const sourceAbs = path.join(rawRoot, ...sourcePdf.split('/'))
    const sourceExists =
      !entry.missing && sameOrInside(rawRoot, sourceAbs) && existsSync(sourceAbs) && statSync(sourceAbs).isFile()
    const targetAbs = deps.workspace.paths.abs(deps.workspace.paths.paperPdf(finalId))
    const targetExists = existsSync(targetAbs) && lstatSync(targetAbs).isFile()

    let fileHash = entry.file_hash
    if (fileHash === '' && sourceExists) fileHash = sha256HexFile(sourceAbs)
    const copySource = !targetExists && sourceExists ? sourceAbs : null
    const fileSize = targetExists
      ? statSync(targetAbs).size
      : copySource !== null
        ? statSync(copySource).size
        : entry.file_size

    actions.push({
      entry,
      finalId,
      libraryId,
      zoteroKey,
      insert: existingId === undefined,
      copySource,
      fileHash,
      fileSize,
      metadataJson: JSON.stringify(entry.metadata ?? {})
    })
  }
  return { actions, remap }
}

function sha256HexFile(file: string): string {
  const fd = openSync(file, 'r')
  try {
    const hash = createHash('sha256')
    const buffer = Buffer.allocUnsafe(1 << 20)
    for (;;) {
      const read = readSync(fd, buffer, 0, buffer.length, null)
      if (read === 0) break
      hash.update(buffer.subarray(0, read))
    }
    return hash.digest('hex')
  } finally {
    closeSync(fd)
  }
}

function copyFileStreamed(source: string, destination: string, signal?: AbortSignal): void {
  mkdirSync(path.dirname(destination), { recursive: true })
  const inHandle = openSync(source, 'r')
  const outHandle = openSync(destination, 'w')
  try {
    const buffer = Buffer.allocUnsafe(1 << 20)
    for (;;) {
      abortOf(signal)
      const read = readSync(inHandle, buffer, 0, buffer.length, null)
      if (read === 0) break
      writeSync(outHandle, buffer.subarray(0, read))
    }
  } finally {
    closeSync(inHandle)
    closeSync(outHandle)
  }
}

function remapPaperIds(db: Db, remap: Map<string, string>): void {
  const changed = [...remap.entries()].filter(([oldId, newIdValue]) => oldId !== newIdValue)
  if (changed.length === 0) return
  db.transaction(() => {
    for (const [oldId, newIdValue] of changed) {
      db.prepare('UPDATE OR IGNORE project_papers SET paper_id = ? WHERE paper_id = ?').run(newIdValue, oldId)
      db.prepare('DELETE FROM project_papers WHERE paper_id = ?').run(oldId)
      db.prepare('UPDATE notes SET paper_id = ? WHERE paper_id = ?').run(newIdValue, oldId)
      db.prepare(`UPDATE entity_relations SET dst_id = ? WHERE dst_type = 'paper' AND dst_id = ?`).run(
        newIdValue,
        oldId
      )
      db.prepare(`UPDATE evidence SET source_id = ? WHERE source_type = 'paper' AND source_id = ?`).run(
        newIdValue,
        oldId
      )
    }
  }).immediate()
}

/** Phase two: restore a validated plan into the workspace. */
export async function restoreBundle(
  deps: ImportDeps,
  state: ImportPlanState,
  options: { newIdentity?: boolean; name?: string; actor?: 'user' | 'mcp' },
  ctx: ImportContext = {}
): Promise<ImportResult> {
  const report = (percent: number, stage: string): void => ctx.report?.(percent, stage)
  const archivedId = state.plan.projectId
  const newIdentity = options.newIdentity === true
  if (!newIdentity && state.plan.idConflict) {
    throw appError(
      AppErrorCode.Conflict,
      `project ${archivedId} already exists; choose new_identity or delete the old project first`,
      { project_id: archivedId }
    )
  }
  const finalId = newIdentity ? newId(deps.clock) : archivedId
  const name = resolveImportName(deps.workspace, options.name ?? state.plan.projectName, archivedId)

  const staging = deps.workspace.paths.abs(
    deps.workspace.paths.projectDir(`${IMPORT_STAGING_PREFIX}${state.plan.planId}`)
  )
  rmSync(staging, { recursive: true, force: true })
  mkdirSync(staging, { recursive: true, mode: 0o755 })
  const rawRoot = path.join(staging, 'raw')
  const createdFiles: string[] = []
  const createdDirs: string[] = []
  let publishedPath: string | null = null
  let committed = false
  const warnings: string[] = []

  try {
    report(5, 'extract')
    const expected = new Map(state.manifest.files.map((entry) => [entry.path, entry]))
    const openEntries = new Map<string, { fd: number; hash: Hash; bytes: number }>()
    const seen = new Set<string>()
    let restoredFiles = 0
    let notes = 0
    let attachments = 0

    const closeExtracted = (): void => {
      for (const [, current] of openEntries) {
        try {
          closeSync(current.fd)
        } catch {
          // Already closed by the successful `end` callback.
        }
      }
      openEntries.clear()
    }
    try {
      await walkBundlePrefixed(
        state.sourcePath,
        state.format,
        state.archivePrefix,
        {
          start: (bundlePath) => {
            const entry = expected.get(bundlePath)
            if (entry === undefined) return
            const destination = path.join(rawRoot, ...bundlePath.split('/'))
            if (!sameOrInside(rawRoot, destination)) {
              invalid(`bundle entry ${bundlePath} escapes the staging root`, { path: bundlePath })
            }
            mkdirSync(path.dirname(destination), { recursive: true })
            const fd = openSync(destination, 'w')
            openEntries.set(bundlePath, { fd, hash: createHash('sha256'), bytes: 0 })
          },
          chunk: (bundlePath, chunk) => {
            const current = openEntries.get(bundlePath)
            if (current === undefined) return
            abortOf(ctx.signal)
            writeSync(current.fd, chunk)
            current.hash.update(chunk)
            current.bytes += chunk.length
          },
          end: (bundlePath, bytes, sha256) => {
            const current = openEntries.get(bundlePath)
            if (current === undefined) return
            closeSync(current.fd)
            openEntries.delete(bundlePath)
            const expectedEntry = expected.get(bundlePath)!
            if (bytes !== expectedEntry.bytes || sha256.toLowerCase() !== expectedEntry.sha256) {
              throw appError(AppErrorCode.Integrity, `${bundlePath} changed during import`, {
                path: bundlePath,
                expected_sha256: expectedEntry.sha256,
                actual_sha256: sha256
              })
            }
            seen.add(bundlePath)
            restoredFiles += 1
            if (bundlePath.includes('/notes/') && bundlePath.endsWith('.md')) notes += 1
            if (bundlePath.includes('/attachments/')) attachments += 1
          }
        },
        ctx.signal
      )
    } finally {
      closeExtracted()
    }
    for (const entry of expected.values()) {
      if (!seen.has(entry.path)) {
        throw appError(AppErrorCode.NotFound, `bundle is missing ${entry.path}`, { path: entry.path })
      }
    }

    const sourceDir = path.join(rawRoot, 'workspace', 'projects', archivedId)
    const stagedDb = path.join(sourceDir, 'project.db')
    if (!existsSync(stagedDb)) {
      invalid('bundle has no project database at workspace/projects/<id>/project.db', {
        project_id: archivedId
      })
    }

    report(55, 'migrate')
    let schemaFrom = 0
    let schemaTo = 0
    try {
      schemaFrom = maxSchemaVersion(stagedDb)
      const opened = openDb(stagedDb, 'project', { clock: deps.clock, skipBackup: true })
      try {
        schemaFrom = opened.migrations.from
        schemaTo = opened.migrations.to
      } finally {
        opened.close()
      }
    } catch (cause) {
      if (isAppError(cause)) throw cause
      throw appError(AppErrorCode.InvalidArgument, 'the restored project database cannot be migrated', {
        cause: cause instanceof Error ? cause.message : String(cause)
      })
    }

    report(62, 'identity')
    const rewriteDb = openConnection(stagedDb, { loadVector: false, verifyFts: false })
    let brokenRefs = 0
    let actions: RestoredPaperAction[] = []
    try {
      if (newIdentity) rewriteProjectIdentity(rewriteDb.db, archivedId, finalId)
      const resolved = resolvePaperActions(deps, state, rawRoot)
      actions = resolved.actions
      remapPaperIds(rewriteDb.db, resolved.remap)
      brokenRefs = probeExternalRefs(rewriteDb.db, formatTimestamp(deps.clock.now()))
    } finally {
      rewriteDb.db.close()
    }

    report(70, 'papers')
    const paperRows: Array<{ action: RestoredPaperAction; fileHash: string; fileSize: number; fileState: 'ok' | 'missing' }> = []
    for (const action of actions) {
      abortOf(ctx.signal)
      let fileHash = action.fileHash
      let fileSize = action.fileSize
      let fileState: 'ok' | 'missing' = 'missing'
      if (action.copySource !== null) {
        const destination = deps.workspace.paths.abs(deps.workspace.paths.paperPdf(action.finalId))
        const directory = path.dirname(destination)
        const directoryExisted = existsSync(directory)
        copyFileStreamed(action.copySource, destination, ctx.signal)
        createdFiles.push(destination)
        if (!directoryExisted) createdDirs.push(directory)
        fileSize = statSync(destination).size
        if (fileHash === '') fileHash = sha256HexFile(destination)
        fileState = 'ok'
      } else {
        const target = deps.workspace.paths.abs(deps.workspace.paths.paperPdf(action.finalId))
        if (existsSync(target) && lstatSync(target).isFile()) {
          fileState = 'ok'
          fileSize = statSync(target).size
        }
      }
      paperRows.push({ action, fileHash, fileSize, fileState })
    }

    report(82, 'publish')
    const destinationDir = deps.workspace.paths.abs(deps.workspace.paths.projectDir(finalId))
    if (existsSync(destinationDir)) {
      throw appError(AppErrorCode.Conflict, `project directory already exists: ${destinationDir}`, {
        project_id: finalId,
        path: destinationDir
      })
    }
    mkdirSync(path.dirname(destinationDir), { recursive: true, mode: 0o755 })
    renameSync(sourceDir, destinationDir)
    publishedPath = destinationDir

    report(90, 'register')
    const now = formatTimestamp(deps.clock.now())
    let created = 0
    const insertPaper = deps.workspace.global.db.prepare(
      `INSERT INTO papers(id, zotero_library_id, zotero_key, metadata, file_path, file_hash, file_size,
                          file_state, imported_at, updated_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
    )
    const fillPaper = deps.workspace.global.db.prepare(
      `UPDATE papers SET file_path = ?, file_hash = ?, file_size = ?, file_state = 'ok', updated_at = ?
        WHERE id = ?`
    )
    const register = deps.workspace.global.db.transaction(() => {
      for (const row of paperRows) {
        const action = row.action
        if (action.insert) {
          insertPaper.run(
            action.finalId,
            action.libraryId,
            action.zoteroKey,
            action.metadataJson,
            row.fileState === 'ok' ? deps.workspace.paths.paperPdf(action.finalId) : null,
            row.fileState === 'ok' ? row.fileHash : null,
            row.fileState === 'ok' ? row.fileSize : null,
            row.fileState,
            now,
            now
          )
        } else if (action.copySource !== null && row.fileState === 'ok') {
          fillPaper.run(
            deps.workspace.paths.paperPdf(action.finalId),
            row.fileHash,
            row.fileSize,
            now,
            action.finalId
          )
        }
        created += 1
      }
      const createdAt = parseCreatedAt(state.meta.createdAt, deps.clock.now())
      deps.workspace.global.db
        .prepare(
          `INSERT INTO projects(id, name, summary, goals, focus_md, status, archived_at, created_at, updated_at)
           VALUES (?, ?, ?, ?, '', ?, ?, ?, ?)`
        )
        .run(
          finalId,
          name,
          state.meta.summary,
          state.meta.goals,
          state.meta.status,
          state.meta.status === 'archived' ? now : null,
          createdAt,
          now
        )
    })
    register.immediate()
    committed = true

    if (deps.events !== undefined) {
      try {
        const handle = deps.workspace.project(finalId)
        deps.events.emitStandalone(handle, {
          type: 'entity.created',
          entityType: 'project',
          entityId: finalId,
          summary: '从归档包导入项目',
          actor: options.actor ?? 'user'
        })
      } catch {
        warnings.push('event_missing: entity.created')
      }
    }
    if (deps.reindex !== undefined) {
      try {
        await deps.reindex(finalId)
      } catch {
        warnings.push(`reindex_failed: ${finalId}`)
      }
    }
    report(100, 'done')
    return {
      projectId: finalId,
      projectName: name,
      mode: newIdentity ? 'new_identity' : 'restore_exact',
      path: destinationDir,
      restoredFiles,
      migratedSchema: schemaTo > schemaFrom,
      schemaFrom,
      schemaTo,
      notes,
      attachments,
      papers: created,
      brokenRefs,
      warnings: [...warnings, ...state.plan.warnings]
    }
  } catch (error) {
    if (!committed) {
      if (publishedPath !== null) rmSync(publishedPath, { recursive: true, force: true })
      for (const file of createdFiles) rmSync(file, { force: true })
      for (const directory of [...createdDirs].reverse()) {
        try {
          rmdirSync(directory)
        } catch {
          // Non-empty or already gone; the file removal above is the contract.
        }
      }
    }
    throw ioError(error, error instanceof Error ? error.message : String(error), {
      stage: 'import',
      plan_id: state.plan.planId
    })
  } finally {
    rmSync(staging, { recursive: true, force: true })
  }
}

function parseCreatedAt(raw: string, fallback: Date): string {
  const parsed = new Date(raw)
  if (Number.isNaN(parsed.getTime())) return formatTimestamp(fallback)
  return formatTimestamp(parsed)
}