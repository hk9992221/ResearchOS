/**
 * Manual sync check/apply.
 *
 * `syncCheck` REQUIRES a Zotero source. Every paper in scope is really hashed
 * and sized on both sides; `checked` is the number of papers compared, and an
 * unavailable source is `EXTERNAL_UNAVAILABLE`, never `checked:0, items:[]`.
 * `syncApply` only overwrites explicit decisions, re-checks the source hash and
 * the local copy, writes the new file atomically, then updates
 * `file_hash/file_size/last_synced_at` and emits `paper.overwrite_confirmed`.
 */
import type {
  PaperSourceState,
  SyncApplyResult,
  SyncDecision,
  SyncItem,
  SyncReport
} from '@researchos/shared'

import type { Actor } from '../../../schema'
import { appError, AppErrorCode } from '../../../errors'
import type { LiteratureContext } from '../context'
import { paperMetadataOf, type PaperRow, type ZoteroAttachmentRecord, type ZoteroReader } from '../types'
import { metadataJson } from '../zotero/metadata'
import { hashFile } from './files'
import { getPaperRow } from './query'

const REPORT_TTL_MS = 30 * 60 * 1000

export interface StoredSyncReport {
  projectId: string
  report: SyncReport
  items: Map<string, SyncItem>
  expiresAt: number
}

const reports = new WeakMap<LiteratureContext, Map<string, StoredSyncReport>>()

function reportStore(ctx: LiteratureContext): Map<string, StoredSyncReport> {
  const existing = reports.get(ctx)
  if (existing !== undefined) return existing
  const created = new Map<string, StoredSyncReport>()
  reports.set(ctx, created)
  return created
}

function storeKey(projectId: string | undefined): string {
  return projectId ?? ''
}

export interface SyncScope {
  projectId?: string
  paperIds?: string[]
}

function scopePapers(
  ctx: LiteratureContext,
  scope: SyncScope
): Array<{ row: PaperRow; linked: boolean }> {
  if (scope.paperIds !== undefined && scope.paperIds.length > 0) {
    return scope.paperIds.map((paperId) => {
      const row = getPaperRow(ctx, paperId)
      if (scope.projectId !== undefined) {
        const link = ctx.workspace
          .project(scope.projectId)
          .db.prepare(`SELECT 1 AS one FROM project_papers WHERE project_id = ? AND paper_id = ?`)
          .get(scope.projectId, paperId) as { one: number } | undefined
        if (link === undefined) {
          throw appError(AppErrorCode.Conflict, `文献 ${paperId} 未关联到该项目`, {
            details: { paper_id: paperId, project_id: scope.projectId }
          })
        }
      }
      return { row, linked: scope.projectId !== undefined }
    })
  }
  if (scope.projectId !== undefined) {
    ctx.requireProject(scope.projectId)
    const links = ctx.workspace
      .project(scope.projectId)
      .db.prepare(`SELECT paper_id FROM project_papers WHERE project_id = ? ORDER BY linked_at, paper_id`)
      .all(scope.projectId) as Array<{ paper_id: string }>
    const out: Array<{ row: PaperRow; linked: boolean }> = []
    for (const link of links) {
      try {
        out.push({ row: getPaperRow(ctx, link.paper_id), linked: true })
      } catch {
        // A corrupt association whose global row is gone is skipped.
      }
    }
    return out
  }
  throw appError(AppErrorCode.InvalidArgument, 'sync 检查需要 project_id 或 paper_ids')
}

function pickAttachment(
  attachments: readonly ZoteroAttachmentRecord[],
  key: string
): ZoteroAttachmentRecord | null {
  const pdfs = attachments.filter(
    (attachment) =>
      attachment.contentType.toLowerCase() === 'application/pdf' ||
      attachment.filename.toLowerCase().endsWith('.pdf')
  )
  if (key !== '') return pdfs.find((attachment) => attachment.key === key) ?? null
  if (pdfs.length === 1) return pdfs[0] ?? null
  return null
}

async function compareOne(
  ctx: LiteratureContext,
  reader: ZoteroReader,
  row: PaperRow
): Promise<SyncItem> {
  const meta = paperMetadataOf(row)
  const item: SyncItem = {
    paperId: row.id,
    title: meta.title,
    zoteroKey: row.zoteroKey,
    localHash: row.fileHash,
    sourceHash: null,
    localSize: row.fileSize,
    sourceSize: null,
    changed: false,
    missing: false,
    error: null,
    warnings: []
  }

  let sourcePath: string | null = null
  try {
    const attachments = await reader.listAttachments(
      row.zoteroLibraryId === '' ? '0' : row.zoteroLibraryId,
      row.zoteroKey
    )
    const attachment = pickAttachment(attachments, row.attachmentKey ?? '')
    if (attachment === null) {
      item.missing = true
      item.warnings.push('no_pdf_attachment')
    } else {
      const resolved = await reader.resolveAttachment(attachment)
      sourcePath = resolved.path
      item.sourceHash = hashFile(resolved.path)
      item.sourceSize = resolved.size
    }
  } catch (error) {
    item.error = error instanceof Error ? error.message : String(error)
    item.warnings.push('source_unavailable')
  }

  try {
    const local = ctx.files.stat(row.id)
    item.localSize = local.size
    item.localHash = hashFile(local.absolutePath)
  } catch {
    item.missing = true
    item.warnings.push('local_file_missing')
  }

  if (item.missing || item.sourceHash === null || item.localHash === null) {
    item.changed = item.missing || item.sourceHash !== item.localHash
  } else {
    item.changed = item.sourceHash !== item.localHash || item.sourceSize !== item.localSize
  }
  return item
}

export async function syncCheck(ctx: LiteratureContext, scope: SyncScope): Promise<SyncReport> {
  const source = ctx.requireZotero()
  let reader: ZoteroReader
  try {
    reader = await source.reader()
  } catch (error) {
    throw appError(AppErrorCode.ExternalUnavailable, 'Zotero 不可用，无法检查同步差异', {
      cause: error
    })
  }
  const papers = scopePapers(ctx, scope)
  const items: SyncItem[] = []
  for (const paper of papers) {
    items.push(await compareOne(ctx, reader, paper.row))
  }
  const report: SyncReport = {
    checked: items.length,
    items,
    source: reader.source(),
    checkedAt: ctx.now()
  }
  const key = storeKey(scope.projectId)
  reportStore(ctx).set(key, {
    projectId: scope.projectId ?? '',
    report,
    items: new Map(items.map((item) => [item.paperId, item])),
    expiresAt: ctx.clock.now().getTime() + REPORT_TTL_MS
  })
  return report
}

function linkedProjectIds(ctx: LiteratureContext, paperId: string): string[] {
  const out: string[] = []
  for (const projectId of ctx.listProjectIds()) {
    try {
      const row = ctx.workspace
        .project(projectId)
        .db.prepare(`SELECT 1 AS one FROM project_papers WHERE project_id = ? AND paper_id = ?`)
        .get(projectId, paperId) as { one: number } | undefined
      if (row !== undefined) out.push(projectId)
    } catch {
      continue
    }
  }
  return out
}

function emitOverwrite(
  ctx: LiteratureContext,
  paperId: string,
  projectIds: readonly string[],
  actor: Actor
): void {
  for (const projectId of projectIds) {
    try {
      const handle = ctx.workspace.project(projectId)
      ctx.inTx(handle, () => {
        ctx.events.emit(handle, {
          type: 'paper.overwrite_confirmed',
          entityType: 'paper',
          entityId: paperId,
          summary: '确认覆盖 PDF 副本',
          actor
        })
      })
    } catch {
      // One project's activity failure must not undo the committed copy.
    }
  }
}

export async function syncApply(
  ctx: LiteratureContext,
  projectId: string,
  decisions: readonly SyncDecision[],
  actor: Actor,
  expectedSourceHashes?: Record<string, string>
): Promise<SyncApplyResult> {
  ctx.requireActive(projectId)
  const stored = reportStore(ctx).get(storeKey(projectId))
  if (stored === undefined || ctx.clock.now().getTime() > stored.expiresAt) {
    throw appError(AppErrorCode.Conflict, '同步报告已过期，请重新检查差异', {
      details: { project_id: projectId }
    })
  }
  const source = ctx.requireZotero()
  let reader: ZoteroReader
  try {
    reader = await source.reader()
  } catch (error) {
    throw appError(AppErrorCode.ExternalUnavailable, 'Zotero 不可用，无法应用同步', {
      cause: error
    })
  }

  const result: SyncApplyResult = {
    applied: 0,
    skipped: 0,
    conflicted: [],
    failures: []
  }
  for (const decision of decisions) {
    if (decision.action !== 'overwrite') {
      result.skipped += 1
      continue
    }
    const item = stored.items.get(decision.paperId)
    if (item === undefined) {
      result.failures.push({ paperId: decision.paperId, error: '同步报告中没有该文献' })
      continue
    }
    if (item.missing || item.sourceHash === null) {
      result.failures.push({
        paperId: decision.paperId,
        error: item.error ?? '上游文件不可用'
      })
      continue
    }
    const expected = expectedSourceHashes?.[decision.paperId]
    if (expected !== undefined && expected !== item.sourceHash) {
      result.conflicted.push(decision.paperId)
      continue
    }
    try {
      const row = getPaperRow(ctx, decision.paperId)
      const attachments = await reader.listAttachments(
        row.zoteroLibraryId === '' ? '0' : row.zoteroLibraryId,
        row.zoteroKey
      )
      const attachment = pickAttachment(attachments, row.attachmentKey ?? '')
      if (attachment === null) throw new Error('上游 PDF 附件不可用')
      const resolved = await reader.resolveAttachment(attachment)
      const currentSourceHash = hashFile(resolved.path)
      if (currentSourceHash !== item.sourceHash) {
        result.conflicted.push(decision.paperId)
        continue
      }
      if (item.localHash !== null) {
        try {
          const local = ctx.files.stat(decision.paperId)
          if (hashFile(local.absolutePath) !== item.localHash) {
            result.conflicted.push(decision.paperId)
            continue
          }
        } catch {
          // A missing local copy is exactly what overwrite repairs.
        }
      }
      const replaced = ctx.files.replace(decision.paperId, resolved.path)
      const now = ctx.now()
      ctx.inTx(ctx.global(), () => {
        ctx
          .global()
          .db.prepare(
            `UPDATE papers SET file_path = ?, file_hash = ?, file_size = ?, file_state = 'ok',
                    source_state = 'ok', last_synced_at = ?, updated_at = ? WHERE id = ?`
          )
          .run(
            replaced.relativePath,
            replaced.hash,
            replaced.size,
            now,
            now,
            decision.paperId
          )
      })
      const projectIds = linkedProjectIds(ctx, decision.paperId)
      emitOverwrite(ctx, decision.paperId, projectIds, actor)
      ctx.notifyIndex(decision.paperId, 'paper.overwrite_confirmed', '覆盖文献副本')
      result.applied += 1
    } catch (error) {
      result.failures.push({
        paperId: decision.paperId,
        error: error instanceof Error ? error.message : String(error)
      })
    }
  }
  return result
}

export interface MetadataRefreshReport {
  updated: string[]
  missing: string[]
  failed: Array<{ paperId: string; error: string }>
  refreshedAt: string
}

export async function refreshMetadata(
  ctx: LiteratureContext,
  scope: SyncScope
): Promise<MetadataRefreshReport> {
  if (scope.projectId !== undefined) ctx.requireActive(scope.projectId)
  const source = ctx.requireZotero()
  let reader: ZoteroReader
  try {
    reader = await source.reader()
  } catch (error) {
    throw appError(AppErrorCode.ExternalUnavailable, 'Zotero 不可用，无法刷新元数据', {
      cause: error
    })
  }
  const papers = scopePapers(ctx, scope)
  const result: MetadataRefreshReport = {
    updated: [],
    missing: [],
    failed: [],
    refreshedAt: ctx.now()
  }
  for (const paper of papers) {
    const row = paper.row
    try {
      let item
      try {
        item = await reader.getItem(row.zoteroLibraryId === '' ? '0' : row.zoteroLibraryId, row.zoteroKey)
      } catch (error) {
        const code = error instanceof Error && 'code' in error ? error.code : undefined
        if (code === 'NOT_FOUND') {
          const now = ctx.now()
          ctx.inTx(ctx.global(), () => {
            ctx
              .global()
              .db.prepare(`UPDATE papers SET source_state = 'missing', updated_at = ? WHERE id = ?`)
              .run(now, row.id)
          })
          result.missing.push(row.id)
          continue
        }
        throw error
      }
      const metadata = metadataJson(item)
      const now = ctx.now()
      ctx.inTx(ctx.global(), () => {
        ctx
          .global()
          .db.prepare(
            `UPDATE papers SET metadata = ?, source_state = 'ok', updated_at = ? WHERE id = ?`
          )
          .run(metadata, now, row.id)
      })
      result.updated.push(row.id)
      for (const projectId of linkedProjectIds(ctx, row.id)) {
        try {
          const handle = ctx.workspace.project(projectId)
          ctx.inTx(handle, () => {
            ctx.events.emit(handle, {
              type: 'paper.metadata_updated',
              entityType: 'paper',
              entityId: row.id,
              summary: '更新文献元数据',
              actor: 'user'
            })
          })
        } catch {
          // Activity is best-effort here; the global metadata already changed.
        }
      }
      ctx.notifyIndex(row.id, 'paper.metadata_updated', '更新文献元数据')
    } catch (error) {
      result.failed.push({
        paperId: row.id,
        error: error instanceof Error ? error.message : String(error)
      })
    }
  }
  return result
}

export function sourceStateOf(value: string): PaperSourceState {
  return value === 'missing' ? 'missing' : value === 'unreadable' ? 'unreadable' : 'ok'
}