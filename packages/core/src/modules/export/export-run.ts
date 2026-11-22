/**
 * One export run (§5.9): snapshot → notes (under the project write lock) →
 * attachments → papers → meta/manifest → atomic publish. A failure or cancel
 * removes only exporter-owned temporary state; an existing output is untouchable.
 */
import { lstatSync, openSync, closeSync, readSync, renameSync, rmSync, statSync, writeSync, mkdirSync } from 'node:fs'
import * as path from 'node:path'

import { AppErrorCode, type ExportArtifact, type ExportFormat } from '@researchos/shared'

import type { Clock } from '../../clock'
import { appError } from '../../errors'
import type { EventBus } from '../../events'
import { openReadOnlyDb, type Db } from '../../db/open'
import type { Locker } from '../../ports'
import type { ProjectHandle } from '../../workspace/handle'
import type { WorkspaceManager } from '../../workspace'
import { createSink, FolderSink, hashFile, sameOrInside, sha256Hex } from './bundle'
import { ioError } from './io-errors'
import { buildManifest, metaPapersJson, metaProjectJson, serializeManifest } from './manifest'
import { CHUNK_SIZE } from './types'
import type {
  ExportPlanState,
  ExportProgress,
  ExportServiceHooks,
  FileKind,
  ManifestFileEntry,
  MissingRef,
  Warning
} from './types'

export interface RunDeps {
  workspace: WorkspaceManager
  clock: Clock
  locker: Locker
  events?: EventBus
  hooks?: ExportServiceHooks
}

export interface RunContext {
  signal?: AbortSignal
  report?: (progress: ExportProgress) => void
}

const STAGE_BASE: Record<string, number> = {
  snapshot_db: 2,
  copy_notes: 20,
  copy_attachments: 45,
  copy_papers: 70,
  finalize: 92
}

function abortOf(signal: AbortSignal | undefined): void {
  if (signal?.aborted === true) {
    throw appError(AppErrorCode.Interrupted, 'export cancelled')
  }
}

function phaseFor(kind: FileKind, index: number, total: number): ExportProgress {
  const stage =
    kind === 'project_db'
      ? 'snapshot_db'
      : kind === 'note_md'
        ? 'copy_notes'
        : kind === 'attachment'
          ? 'copy_attachments'
          : kind === 'paper_pdf'
            ? 'copy_papers'
            : 'finalize'
  const base = STAGE_BASE[stage] ?? 0
  const span = total === 0 ? 0 : Math.round((index / total) * 18)
  return { stage, percent: Math.min(99, base + span) }
}

async function copyFile(source: string, destination: string, signal?: AbortSignal): Promise<void> {
  const inHandle = openSync(source, 'r')
  const outHandle = openSync(destination, 'w')
  try {
    const buffer = Buffer.allocUnsafe(CHUNK_SIZE)
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

function schemaVersionOf(db: Db): number {
  const row = db.prepare('SELECT coalesce(max(version), 0) AS version FROM schema_migrations').get() as
    | { version: number }
    | undefined
  return Number(row?.version ?? 0)
}

function globalSchemaVersion(workspace: WorkspaceManager): number {
  const row = workspace.global.db
    .prepare('SELECT coalesce(max(version), 0) AS version FROM schema_migrations')
    .get() as { version: number } | undefined
  return Number(row?.version ?? 0)
}

/** VACUUM INTO, falling back to a checkpoint+copy with an integrity check. */
async function snapshotDatabase(handle: ProjectHandle, sourcePath: string, destination: string): Promise<void> {
  rmSync(destination, { force: true })
  mkdirSync(path.dirname(destination), { recursive: true })
  try {
    handle.db.prepare('VACUUM INTO ?').run(destination)
    return
  } catch {
    // Fall through to the byte-copy path.
  }
  handle.db.pragma('wal_checkpoint(TRUNCATE)')
  await copyFile(sourcePath, destination)
  const check = openReadOnlyDb(destination, { loadVector: false, verifyFts: false })
  try {
    const row = check.db.prepare('PRAGMA integrity_check').get() as { integrity_check?: string } | undefined
    const value = row?.integrity_check ?? 'unknown'
    if (value.toLowerCase() !== 'ok') {
      throw appError(AppErrorCode.Integrity, `snapshot integrity check failed: ${value}`)
    }
  } finally {
    check.db.close()
  }
}

function humanBytes(bytes: number): string {
  if (bytes >= 1 << 30) return `${(bytes / (1 << 30)).toFixed(1)} GB`
  if (bytes >= 1 << 20) return `${(bytes / (1 << 20)).toFixed(1)} MB`
  if (bytes >= 1 << 10) return `${(bytes / (1 << 10)).toFixed(1)} KB`
  return `${bytes} B`
}

function formatLabel(format: ExportFormat): string {
  return format === 'folder' ? '文件夹' : format
}

/**
 * Runs the plan and publishes the artifact. Throws a typed AppError; the
 * caller decides job state and never sees a partially published bundle.
 */
export async function runExportPlan(
  plan: ExportPlanState,
  deps: RunDeps,
  ctx: RunContext = {},
  actor: 'user' | 'mcp' = 'user'
): Promise<ExportArtifact> {
  const staging = plan.stagingPath
  const { sink, partPath } = createSink(plan.format, staging, plan.destDir, plan.baseName)
  const files: ManifestFileEntry[] = []
  const written = new Set<string>()
  const missing: MissingRef[] = [...plan.missing]
  const warnings: Warning[] = [...plan.warnings]
  let published = false

  const report = (progress: ExportProgress): void => {
    ctx.report?.(progress)
  }

  const addEntry = (bundlePath: string, sha256: string, bytes: number): void => {
    files.push({ path: bundlePath, bytes, sha256 })
    written.add(bundlePath)
  }

  try {
    const handle = deps.workspace.project(plan.projectId)
    const projectDbRelative = deps.workspace.paths.projectDb(plan.projectId)
    const projectDbAbs = deps.workspace.paths.abs(projectDbRelative)
    const dbBundlePath = `workspace/${projectDbRelative}`

    // §5.9: the lock covers the database snapshot and the notes only.
    report({ stage: 'snapshot_db', percent: 1 })
    await deps.locker.withProjectWriteLock(plan.projectId, async () => {
      abortOf(ctx.signal)
      if (sink instanceof FolderSink) {
        const destination = sink.placePath(dbBundlePath)
        await snapshotDatabase(handle, projectDbAbs, destination)
        const hashed = await hashFile(destination, ctx.signal)
        addEntry(dbBundlePath, hashed.sha256, hashed.bytes)
      } else {
        const temporary = path.join(staging, 'snapshot.db')
        await snapshotDatabase(handle, projectDbAbs, temporary)
        const hashed = await sink.addFile(dbBundlePath, temporary, 'project_db', ctx.signal)
        rmSync(temporary, { force: true })
        addEntry(dbBundlePath, hashed.sha256, hashed.bytes)
      }
      report({ stage: 'snapshot_db', percent: 6 })

      const noteFiles = plan.files.filter((file) => file.kind === 'note_md')
      for (let index = 0; index < noteFiles.length; index += 1) {
        const file = noteFiles[index]!
        abortOf(ctx.signal)
        deps.hooks?.beforeFile?.('copy_notes', file.bundlePath)
        report(phaseFor('note_md', index, noteFiles.length))
        const hashed = await sink.addFile(file.bundlePath, file.sourceAbs, file.kind, ctx.signal)
        addEntry(file.bundlePath, hashed.sha256, hashed.bytes)
      }
    })

    const copyKind = async (kind: FileKind, stage: string): Promise<void> => {
      const entries = plan.files.filter((file) => file.kind === kind)
      for (let index = 0; index < entries.length; index += 1) {
        const file = entries[index]!
        abortOf(ctx.signal)
        deps.hooks?.beforeFile?.(stage, file.bundlePath)
        report(phaseFor(kind, index, entries.length))
        const hashed = await sink.addFile(file.bundlePath, file.sourceAbs, file.kind, ctx.signal)
        if (kind === 'paper_pdf') {
          const expected = plan.papers.find((paper) => paper.bundlePath === file.bundlePath)
          const normalized = (expected?.fileHash ?? '').trim().toLowerCase().replace(/^sha256:/, '')
          if (normalized.length === 64 && normalized !== hashed.sha256) {
            warnings.push({ code: 'hash_mismatch', path: file.bundlePath })
          }
        }
        addEntry(file.bundlePath, hashed.sha256, hashed.bytes)
      }
    }

    await copyKind('attachment', 'copy_attachments')
    await copyKind('paper_pdf', 'copy_papers')

    report({ stage: 'finalize', percent: 92 })
    const projectMeta = metaProjectJson(plan.project)
    const projectMetaEntry = await sink.addBytes('meta/project.json', projectMeta)
    addEntry('meta/project.json', projectMetaEntry.sha256, projectMetaEntry.bytes)
    const papersMeta = metaPapersJson(plan.papers)
    const papersMetaEntry = await sink.addBytes('meta/papers.json', papersMeta)
    addEntry('meta/papers.json', papersMetaEntry.sha256, papersMetaEntry.bytes)

    for (const file of plan.files) {
      if (!written.has(file.bundlePath)) {
        throw appError(AppErrorCode.Internal, `planned file was not written: ${file.bundlePath}`)
      }
    }

    const manifest = buildManifest({
      format: plan.format,
      project: plan.project,
      exportedAt: deps.clock.now(),
      schemaGlobal: globalSchemaVersion(deps.workspace),
      schemaProject: schemaVersionOf(handle.db),
      files,
      papers: plan.papers,
      missing,
      warnings
    })
    const manifestBytes = serializeManifest(manifest)
    await sink.addBytes('manifest.json', manifestBytes)
    await sink.finish()

    if (lstatSync(plan.outputPath, { throwIfNoEntry: false }) !== undefined) {
      throw appError(AppErrorCode.Conflict, `bundle already exists: ${plan.outputPath}`, {
        destination: plan.outputPath
      })
    }
    if (plan.format === 'folder') {
      if (!sameOrInside(plan.destDir, staging)) {
        throw appError(AppErrorCode.Internal, 'staging directory escaped the destination')
      }
      renameSync(staging, plan.outputPath)
    } else {
      renameSync(partPath, plan.outputPath)
    }
    published = true

    const archiveSha = sink.archiveSha256()
    const outputSize = plan.format === 'folder' ? manifest.counts.bytes + manifestBytes.length : statSync(plan.outputPath).size
    const summary =
      `导出为 ${formatLabel(plan.format)}（${manifest.counts.files} 文件，${humanBytes(manifest.counts.bytes)}）`
    if (deps.events !== undefined) {
      try {
        deps.events.emitStandalone(handle, {
          type: 'project.exported',
          entityType: 'project',
          entityId: plan.projectId,
          summary,
          actor
        })
      } catch {
        // The bundle is complete; a missing audit row must not fail the export.
      }
    }
    report({ stage: 'finalize', percent: 100 })
    return {
      projectId: plan.projectId,
      format: plan.format,
      path: plan.outputPath,
      bytes: outputSize,
      sha256: archiveSha ?? sha256Hex(manifestBytes),
      papersIncluded: manifest.counts.papers,
      fileCount: manifest.counts.files,
      warnings: warnings.map((warning) => `${warning.code}: ${warning.path}`)
    }
  } catch (error) {
    sink.abort()
    throw ioError(error, error instanceof Error ? error.message : String(error), {
      stage: 'export',
      project_id: plan.projectId
    })
  } finally {
    if (!published) {
      rmSync(staging, { recursive: true, force: true })
      if (partPath !== '') rmSync(partPath, { force: true })
    } else {
      rmSync(staging, { recursive: true, force: true })
    }
  }
}
