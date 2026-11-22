/**
 * Read-only export preflight (§5.9.2–§5.9.5). Creates nothing except the
 * default `<workspace>/exports` directory and a temporary writability probe,
 * and never touches project data.
 */
import {
  closeSync,
  lstatSync,
  mkdirSync,
  openSync,
  readdirSync,
  realpathSync,
  rmSync,
  statSync,
  statfsSync,
  writeSync
} from 'node:fs'
import * as path from 'node:path'

import { AppErrorCode, type ExportFormat } from '@researchos/shared'

import type { Clock } from '../../clock'
import { appError } from '../../errors'
import { newId } from '../../ids'
import type { ProjectHandle } from '../../workspace/handle'
import type { WorkspaceManager } from '../../workspace'
import {
  COLLISION_MAX,
  EXPORT_PLAN_TTL_MS,
  STAGING_PREFIX
} from './types'
import type {
  ExportPlanState,
  MissingRef,
  PaperRow,
  PlannedFile,
  PlannedPaper,
  ProjectMeta,
  ProjectRow,
  Warning
} from './types'
import { candidateBase, sanitizeBase } from './sanitize'
import { sameOrInside } from './bundle'

export interface PlanDeps {
  workspace: WorkspaceManager
  clock: Clock
  diskFree?: (dir: string) => { free: number; total: number } | null
}

export interface PlanRequest {
  projectId: string
  format: ExportFormat
  destDir?: string
}

interface ProjectRecord {
  meta: ProjectMeta
  dirAbs: string
}

function projectOf(deps: PlanDeps, projectId: string): ProjectRecord {
  const row = deps.workspace.global.db
    .prepare('SELECT id, name, summary, goals, status, created_at FROM projects WHERE id = ?')
    .get(projectId) as ProjectRow | undefined
  if (row === undefined) {
    throw appError(AppErrorCode.NotFound, `project ${projectId} does not exist`, { project_id: projectId })
  }
  const paths = deps.workspace.paths
  let dirAbs: string
  try {
    dirAbs = paths.abs(paths.projectDir(projectId))
    if (!statSync(dirAbs).isDirectory()) throw new Error('not a directory')
  } catch (cause) {
    throw appError(AppErrorCode.NotFound, `project ${projectId} has no readable data directory`, {
      project_id: projectId,
      cause: cause instanceof Error ? cause.message : String(cause)
    })
  }
  return {
    meta: {
      id: row.id,
      name: row.name,
      summary: row.summary,
      goals: row.goals,
      status: row.status === 'archived' ? 'archived' : 'active',
      createdAt: row.created_at
    },
    dirAbs
  }
}

function defaultDiskFree(dir: string): { free: number; total: number } | null {
  try {
    const stats = statfsSync(dir)
    return { free: Number(stats.bavail) * Number(stats.bsize), total: Number(stats.blocks) * Number(stats.bsize) }
  } catch {
    return null
  }
}

/** Validates the destination and probes write+delete rights (defect 8). */
export function resolveDestDir(deps: PlanDeps, requested: string | undefined, projectDirAbs: string): string {
  const paths = deps.workspace.paths
  let dest = (requested ?? '').trim()
  if (dest === '') {
    const relative = paths.exportsDir()
    dest = path.join(deps.workspace.root, ...relative.split('/'))
    try {
      mkdirSync(dest, { recursive: true, mode: 0o755 })
    } catch (cause) {
      throw appError(AppErrorCode.ExternalUnavailable, `cannot create the default export directory ${dest}`, {
        destination: dest,
        cause: cause instanceof Error ? cause.message : String(cause)
      })
    }
  }
  if (!path.isAbsolute(dest)) {
    throw appError(AppErrorCode.InvalidArgument, `destination ${JSON.stringify(dest)} must be an absolute path`, {
      destination: dest
    })
  }
  let info
  try {
    info = statSync(dest)
  } catch (cause) {
    throw appError(
      AppErrorCode.InvalidArgument,
      `destination ${dest} does not exist; the parent directory must exist`,
      { destination: dest, cause: cause instanceof Error ? cause.message : String(cause) }
    )
  }
  if (!info.isDirectory()) {
    throw appError(AppErrorCode.InvalidArgument, `destination ${dest} is not a directory`, { destination: dest })
  }
  let resolved: string
  try {
    resolved = realpathSync(dest)
  } catch (cause) {
    throw appError(AppErrorCode.InvalidArgument, `cannot resolve destination ${dest}`, {
      destination: dest,
      cause: cause instanceof Error ? cause.message : String(cause)
    })
  }

  // Writability probe: create, write and remove a temp file.
  const probe = path.join(resolved, `.researchos-export-probe-${newId().slice(0, 8)}`)
  try {
    const handle = openSync(probe, 'w')
    try {
      writeSync(handle, 'probe')
    } finally {
      closeSync(handle)
    }
    rmSync(probe, { force: true })
  } catch (cause) {
    rmSync(probe, { force: true })
    throw appError(AppErrorCode.ExternalUnavailable, `destination ${resolved} is not writable`, {
      destination: resolved,
      cause: cause instanceof Error ? cause.message : String(cause)
    })
  }

  if (sameOrInside(projectDirAbs, resolved)) {
    throw appError(AppErrorCode.InvalidArgument, `destination must not be inside the project directory`, {
      destination: resolved
    })
  }
  const stagingRoot = path.join(deps.workspace.root, 'projects')
  if (sameOrInside(stagingRoot, resolved) && path.basename(resolved).startsWith('.import-')) {
    throw appError(AppErrorCode.InvalidArgument, `destination must not be an import staging directory`, {
      destination: resolved
    })
  }
  return resolved
}

function extensionOf(format: ExportFormat): string {
  if (format === 'zip') return '.zip'
  if (format === 'tar.gz') return '.tar.gz'
  return ''
}

/** Resolves `<base>`, `<base>-2` … `<base>-100` without overwriting (§5.9.4). */
export function resolveOutputName(
  destDir: string,
  baseName: string,
  format: ExportFormat
): { base: string; outputPath: string } {
  const extension = extensionOf(format)
  for (let index = 0; index < COLLISION_MAX; index += 1) {
    const base = candidateBase(baseName, index)
    const outputPath = path.join(destDir, `${base}${extension}`)
    const existing = lstatSync(outputPath, { throwIfNoEntry: false })
    if (existing === undefined) return { base, outputPath }
  }
  throw appError(
    AppErrorCode.Conflict,
    `cannot resolve a free bundle name under ${destDir} after ${COLLISION_MAX} attempts`,
    { destination: destDir, base: baseName }
  )
}

function warn(warnings: Warning[], code: Warning['code'], target: string): void {
  warnings.push({ code, path: target })
}

function validNotePath(relative: string, projectId: string): boolean {
  if (relative === '' || relative.includes('\\') || path.isAbsolute(relative)) return false
  const segments = relative.split('/')
  if (segments.some((segment) => segment === '' || segment === '.' || segment === '..')) return false
  return relative.startsWith(`projects/${projectId}/`) && relative.endsWith('.md')
}

function validLibraryPath(relative: string): boolean {
  if (relative === '' || relative.includes('\\') || path.isAbsolute(relative)) return false
  return relative.startsWith('library/papers/')
}

interface Collector {
  deps: PlanDeps
  handle: ProjectHandle
  plan: ExportPlanState
  seen: Set<string>
  projectId: string
}

function addNotes(collector: Collector): string[] {
  const { deps, handle, plan } = collector
  const rows = handle.db.prepare('SELECT id, file_path FROM notes ORDER BY id').all() as Array<{
    id: string
    file_path: string
  }>
  const ids: string[] = []
  for (const row of rows) {
    ids.push(row.id)
    if (!validNotePath(row.file_path, collector.projectId)) {
      warn(plan.warnings, 'path_rejected', row.file_path)
      continue
    }
    let absolute: string
    try {
      absolute = deps.workspace.paths.abs(row.file_path)
    } catch {
      warn(plan.warnings, 'path_rejected', row.file_path)
      continue
    }
    let info
    try {
      info = lstatSync(absolute)
    } catch {
      plan.missing.push({ kind: 'note_md', ref: row.id, recordedPath: row.file_path })
      continue
    }
    if (info.isSymbolicLink()) {
      warn(plan.warnings, 'symlink_skipped', `workspace/${row.file_path}`)
      continue
    }
    if (!info.isFile()) {
      plan.missing.push({ kind: 'note_md', ref: row.id, recordedPath: row.file_path })
      continue
    }
    const bundlePath = `workspace/${row.file_path}`
    if (collector.seen.has(bundlePath)) continue
    collector.seen.add(bundlePath)
    plan.files.push({
      sourceAbs: absolute,
      bundlePath,
      kind: 'note_md',
      bytes: info.size
    })
    plan.totalBytes += info.size
    plan.noteCount += 1
  }
  return ids
}

function addAttachments(collector: Collector, noteIds: readonly string[]): void {
  const { deps, plan } = collector
  for (const noteId of noteIds) {
    const relativeDir = deps.workspace.paths.attachmentsDir(collector.projectId, noteId)
    let absoluteDir: string
    try {
      absoluteDir = deps.workspace.paths.abs(relativeDir)
    } catch {
      warn(plan.warnings, 'path_rejected', relativeDir)
      continue
    }
    let info
    try {
      info = lstatSync(absoluteDir)
    } catch {
      warn(plan.warnings, 'attachment_dir_missing', `workspace/${relativeDir}`)
      continue
    }
    if (info.isSymbolicLink() || !info.isDirectory()) {
      warn(
        plan.warnings,
        info.isSymbolicLink() ? 'symlink_skipped' : 'attachment_dir_missing',
        `workspace/${relativeDir}`
      )
      continue
    }
    const stack: string[] = ['']
    const found: string[] = []
    while (stack.length > 0) {
      const sub = stack.pop()!
      const dir = sub === '' ? absoluteDir : path.join(absoluteDir, ...sub.split('/'))
      for (const entry of readdirSync(dir).sort()) {
        const relative = sub === '' ? entry : `${sub}/${entry}`
        const absoluteEntry = path.join(absoluteDir, ...relative.split('/'))
        const entryInfo = lstatSync(absoluteEntry)
        if (entryInfo.isSymbolicLink()) {
          warn(plan.warnings, 'symlink_skipped', `workspace/${relativeDir}/${relative}`)
          continue
        }
        if (entryInfo.isDirectory()) {
          stack.push(relative)
          continue
        }
        if (!entryInfo.isFile()) continue
        found.push(relative)
      }
    }
    found.sort()
    for (const relative of found) {
      const absoluteEntry = path.join(absoluteDir, ...relative.split('/'))
      const bundlePath = `workspace/${relativeDir}/${relative}`
      if (collector.seen.has(bundlePath)) continue
      collector.seen.add(bundlePath)
      const entryInfo = statSync(absoluteEntry)
      plan.files.push({
        sourceAbs: absoluteEntry,
        bundlePath,
        kind: 'attachment',
        bytes: entryInfo.size
      })
      plan.totalBytes += entryInfo.size
      plan.attachmentCount += 1
    }
  }
}

function linkedPaperIds(collector: Collector): string[] {
  const { handle, projectId } = collector
  const rows = handle.db
    .prepare('SELECT paper_id FROM project_papers WHERE project_id = ? ORDER BY paper_id')
    .all(projectId) as Array<{ paper_id: string }>
  if (rows.length > 0) return dedupeSorted(rows.map((row) => row.paper_id))
  const relations = handle.db
    .prepare(`SELECT DISTINCT dst_id AS paper_id FROM entity_relations WHERE dst_type = 'paper'`)
    .all() as Array<{ paper_id: string }>
  return dedupeSorted(relations.map((row) => row.paper_id))
}

function dedupeSorted(values: readonly string[]): string[] {
  return [...new Set(values.filter((value) => value !== ''))].sort()
}

function addPapers(collector: Collector): void {
  const { deps, plan } = collector
  const global = deps.workspace.global.db
  const paths = deps.workspace.paths
  for (const paperId of linkedPaperIds(collector)) {
    const planned: PlannedPaper = {
      id: paperId,
      zoteroKey: '',
      fileHash: '',
      recordedPath: paths.paperPdf(paperId),
      bundlePath: '',
      metadata: '',
      bytes: 0,
      missing: false
    }
    const row = global
      .prepare(
        `SELECT id, zotero_library_id, zotero_key, metadata, file_path, file_hash, file_size, file_state
           FROM papers WHERE id = ?`
      )
      .get(paperId) as PaperRow | undefined
    if (row === undefined) {
      planned.missing = true
      plan.missing.push({ kind: 'paper_pdf', ref: paperId, recordedPath: planned.recordedPath })
      plan.papers.push(planned)
      continue
    }
    planned.zoteroKey = row.zotero_key
    planned.fileHash = row.file_hash ?? ''
    planned.metadata = row.metadata
    planned.bytes = row.file_size ?? 0
    const recorded = (row.file_path ?? '').trim()
    if (recorded !== '') {
      planned.recordedPath = recorded
      if (!validLibraryPath(recorded)) warn(plan.warnings, 'path_rejected', recorded)
    }
    if (row.file_state !== 'ok' || (recorded !== '' && !validLibraryPath(recorded))) {
      planned.missing = true
      plan.missing.push({ kind: 'paper_pdf', ref: paperId, recordedPath: planned.recordedPath })
      plan.papers.push(planned)
      continue
    }
    const relative = paths.paperPdf(paperId)
    let absolute: string
    try {
      absolute = paths.abs(relative)
    } catch {
      warn(plan.warnings, 'path_rejected', relative)
      planned.missing = true
      plan.missing.push({ kind: 'paper_pdf', ref: paperId, recordedPath: planned.recordedPath })
      plan.papers.push(planned)
      continue
    }
    let info
    try {
      info = lstatSync(absolute)
    } catch {
      info = null
    }
    if (info === null || !info.isFile() || info.isSymbolicLink()) {
      if (info?.isSymbolicLink() === true) warn(plan.warnings, 'symlink_skipped', `workspace/${relative}`)
      planned.missing = true
      plan.missing.push({ kind: 'paper_pdf', ref: paperId, recordedPath: planned.recordedPath })
      plan.papers.push(planned)
      continue
    }
    planned.bundlePath = `workspace/${relative}`
    planned.bytes = info.size
    plan.files.push({
      sourceAbs: absolute,
      bundlePath: planned.bundlePath,
      kind: 'paper_pdf',
      bytes: info.size
    })
    plan.totalBytes += info.size
    plan.paperCount += 1
    plan.papers.push(planned)
  }
}

/** Runs the full preflight and returns the plan state kept in memory. */
export function planExport(deps: PlanDeps, request: PlanRequest): ExportPlanState {
  if (request.format !== 'folder' && request.format !== 'zip' && request.format !== 'tar.gz') {
    throw appError(AppErrorCode.InvalidArgument, `unknown export format ${JSON.stringify(request.format)}`, {
      format: request.format
    })
  }
  const project = projectOf(deps, request.projectId)
  const destDir = resolveDestDir(deps, request.destDir, project.dirAbs)
  const baseName = sanitizeBase(project.meta.name, project.meta.id)
  const { base, outputPath } = resolveOutputName(destDir, baseName, request.format)

  const handle = deps.workspace.project(request.projectId)
  const planId = newId(deps.clock)
  const now = deps.clock.now()
  const plan: ExportPlanState = {
    id: planId,
    projectId: request.projectId,
    format: request.format,
    destDir,
    outputPath,
    stagingPath: path.join(destDir, `${STAGING_PREFIX}${planId}`),
    baseName: base,
    project: project.meta,
    files: [],
    papers: [],
    totalBytes: 0,
    noteCount: 0,
    paperCount: 0,
    attachmentCount: 0,
    missing: [],
    warnings: [],
    expiresAt: now.getTime() + EXPORT_PLAN_TTL_MS,
    createdAt: now.getTime()
  }
  const collector: Collector = { deps, handle, plan, seen: new Set<string>(), projectId: request.projectId }

  const projectDb = deps.workspace.paths.projectDb(request.projectId)
  const projectDbAbs = deps.workspace.paths.abs(projectDb)
  let dbSize = 0
  try {
    dbSize = statSync(projectDbAbs).size
  } catch {
    dbSize = 0
  }
  plan.files.push({
    sourceAbs: projectDbAbs,
    bundlePath: `workspace/${projectDb}`,
    kind: 'project_db',
    bytes: dbSize
  })
  plan.totalBytes += dbSize

  const noteIds = addNotes(collector)
  addAttachments(collector, noteIds)
  addPapers(collector)
  plan.files.push({ sourceAbs: '', bundlePath: 'meta/project.json', kind: 'meta_json', bytes: 0 })
  plan.files.push({ sourceAbs: '', bundlePath: 'meta/papers.json', kind: 'meta_json', bytes: 0 })

  const free = (deps.diskFree ?? defaultDiskFree)(destDir)
  if (free !== null) {
    const required = plan.totalBytes + Math.floor(plan.totalBytes * 0.05)
    if (free.free < required) {
      throw appError(AppErrorCode.ExternalUnavailable, 'insufficient disk space for the bundle', {
        destination: destDir,
        required_bytes: required,
        free_bytes: free.free
      })
    }
  }
  return plan
}

/** Free bytes of the destination; 0 when the platform probe is unavailable. */
export function freeBytesOf(deps: PlanDeps, dir: string): number {
  const free = (deps.diskFree ?? defaultDiskFree)(dir)
  return free?.free ?? 0
}
