/**
 * Zotero import. Preview is strictly
 * read-only. Import is serial: one Zotero item at a time, each with its own
 * outcome; a failure never rolls back earlier successes. De-duplication is
 * `(zotero_library_id, zotero_key)`, the copy is hashed while it is written,
 * `paper.imported` is emitted in the project transaction and the index hook is
 * notified after commit.
 */
import type { ImportOutcome, ImportPreview, ImportPreviewEntry, ImportReport } from '@researchos/shared'

import { appError, AppErrorCode } from '../../../errors'
import { newId } from '../../../ids'
import type { LiteratureContext } from '../context'
import type { Actor } from '../../../schema'
import {
  type ZoteroAttachmentRecord,
  type ZoteroItemRecord,
  type ZoteroReader
} from '../types'
import { authorsOf, yearOf } from '../types'
import { metadataJson, toPaperMetadata } from '../zotero/metadata'
import { hashFile } from './files'
import { findPaperByZotero, getPaperRow } from './query'

const PAGE_LIMIT = 100
const TITLE_LIMIT = 80

export interface ImportSelectionInternal {
  libraryId: string
  collectionKeys: string[]
  includeSubcollections: boolean
  itemKeys: string[]
  attachmentChoices: Record<string, string>
}

export function normalizeSelection(selection: ImportSelectionInternal): ImportSelectionInternal {
  return {
    libraryId: selection.libraryId.trim() === '' ? '0' : selection.libraryId.trim(),
    collectionKeys: selection.collectionKeys.filter((key) => key.trim() !== ''),
    includeSubcollections: selection.includeSubcollections,
    itemKeys: selection.itemKeys.filter((key) => key.trim() !== ''),
    attachmentChoices: { ...selection.attachmentChoices }
  }
}

function isPdfAttachment(attachment: ZoteroAttachmentRecord): boolean {
  if (attachment.contentType.toLowerCase() === 'application/pdf') return true
  return attachment.filename.toLowerCase().endsWith('.pdf')
}

function pdfAttachments(attachments: ZoteroAttachmentRecord[]): ZoteroAttachmentRecord[] {
  return attachments.filter(isPdfAttachment)
}

function chooseAttachment(
  attachments: ZoteroAttachmentRecord[],
  choiceKey: string
): { attachment: ZoteroAttachmentRecord | null; status: 'ok' | 'no_pdf' | 'needs_choice' } {
  if (choiceKey !== '') {
    const chosen = attachments.find((attachment) => attachment.key === choiceKey)
    if (chosen === undefined) {
      throw appError(AppErrorCode.NotFound, `Zotero 附件 ${choiceKey} 不存在`, {
        details: { attachment_key: choiceKey }
      })
    }
    if (!isPdfAttachment(chosen)) {
      throw appError(AppErrorCode.InvalidArgument, `附件 ${choiceKey} 不是 PDF`, {
        details: { attachment_key: choiceKey }
      })
    }
    return { attachment: chosen, status: 'ok' }
  }
  const pdfs = pdfAttachments(attachments)
  if (pdfs.length === 0) return { attachment: null, status: 'no_pdf' }
  if (pdfs.length === 1) return { attachment: pdfs[0] ?? null, status: 'ok' }
  return { attachment: null, status: 'needs_choice' }
}

function truncate(text: string, limit: number): string {
  const characters = Array.from(text)
  return characters.length <= limit ? text : characters.slice(0, limit).join('')
}

async function expandItems(
  ctx: LiteratureContext,
  reader: ZoteroReader,
  selection: ImportSelectionInternal
): Promise<{ items: ZoteroItemRecord[]; errors: Array<{ key: string; message: string }> }> {
  const items: ZoteroItemRecord[] = []
  const errors: Array<{ key: string; message: string }> = []
  const seen = new Set<string>()
  const append = (item: ZoteroItemRecord): void => {
    if (item.itemType.toLowerCase() === 'note' || item.itemType.toLowerCase() === 'attachment') return
    if (seen.has(item.key)) return
    seen.add(item.key)
    items.push(item)
  }

  if (selection.collectionKeys.length > 0) {
    const collections = await reader.listCollections(selection.libraryId)
    const byKey = new Map(collections.map((collection) => [collection.key, collection]))
    const children = new Map<string, typeof collections>()
    for (const collection of collections) {
      const bucket = children.get(collection.parentKey) ?? []
      bucket.push(collection)
      children.set(collection.parentKey, bucket)
    }
    const visited = new Set<string>()
    const visit = async (key: string): Promise<void> => {
      if (visited.has(key)) return
      visited.add(key)
      let offset = 0
      for (;;) {
        const page = await reader.listItems(selection.libraryId, key, {
          offset,
          limit: PAGE_LIMIT,
          query: ''
        })
        for (const item of page.items) append(item)
        if (page.items.length === 0) break
        offset += page.items.length
        if (page.total > 0 && offset >= page.total) break
      }
      if (selection.includeSubcollections) {
        for (const child of children.get(key) ?? []) await visit(child.key)
      }
    }
    for (const key of selection.collectionKeys) {
      if (!byKey.has(key)) {
        throw appError(AppErrorCode.NotFound, `Zotero 分类 ${key} 不存在`, {
          details: { collection_key: key }
        })
      }
      await visit(key)
    }
  }

  for (const key of selection.itemKeys) {
    try {
      append(await reader.getItem(selection.libraryId, key))
    } catch (error) {
      errors.push({ key, message: error instanceof Error ? error.message : String(error) })
    }
  }
  return { items, errors }
}

async function previewEntry(
  ctx: LiteratureContext,
  reader: ZoteroReader,
  item: ZoteroItemRecord,
  attachments: ZoteroAttachmentRecord[],
  choice: string
): Promise<ImportPreviewEntry> {
  const meta = toPaperMetadata(item)
  const chosen = chooseAttachment(attachments, choice)
  const existing = findPaperByZotero(ctx, item.libraryId, item.key)
  const warnings: string[] = []
  if (existing !== null) {
    try {
      ctx.files.stat(existing.id)
    } catch {
      warnings.push('local_file_missing')
    }
    if (chosen.attachment !== null) {
      try {
        const resolved = await reader.resolveAttachment(chosen.attachment)
        const sourceHash = hashFile(resolved.path)
        if (existing.fileHash !== null && existing.fileHash !== sourceHash) {
          warnings.push('source_hash_differs')
        }
      } catch {
        warnings.push('attachment_unresolved')
      }
    }
  }
  return {
    libraryId: item.libraryId,
    key: item.key,
    title: meta.title,
    creators: meta.creators.map((creator) => authorsOf([creator])),
    year: yearOf(item.date) === 0 ? null : yearOf(item.date),
    alreadyImported: existing !== null,
    pdfFound: chosen.status !== 'no_pdf',
    attachmentKey: chosen.attachment?.key ?? null,
    warnings
  }
}

export async function previewImport(
  ctx: LiteratureContext,
  projectId: string,
  raw: ImportSelectionInternal
): Promise<ImportPreview> {
  ctx.requireActive(projectId)
  const selection = normalizeSelection(raw)
  if (selection.libraryId !== '0') {
    throw appError(AppErrorCode.InvalidArgument, 'v1 仅支持个人库（library_id="0"）', {
      details: { field: 'library_id' }
    })
  }
  if (selection.collectionKeys.length === 0 && selection.itemKeys.length === 0) {
    throw appError(AppErrorCode.InvalidArgument, '导入选择为空：请选择条目或分类')
  }
  const reader = await ctx.requireZotero().reader()
  const expanded = await expandItems(ctx, reader, selection)
  const entries: ImportPreviewEntry[] = []
  for (const item of expanded.items) {
    try {
      const attachments = await reader.listAttachments(selection.libraryId, item.key)
      entries.push(
        await previewEntry(
          ctx,
          reader,
          item,
          attachments,
          selection.attachmentChoices[item.key] ?? ''
        )
      )
    } catch (error) {
      expanded.errors.push({
        key: item.key,
        message: error instanceof Error ? error.message : String(error)
      })
    }
  }
  return {
    entries,
    importable: entries.filter((entry) => entry.pdfFound && !entry.alreadyImported).length,
    alreadyPresent: entries.filter((entry) => entry.alreadyImported).length,
    missingPdf: entries.filter((entry) => !entry.pdfFound).length,
    errors: expanded.errors
  }
}

function importSummary(title: string): string {
  const trimmed = title.trim()
  return trimmed === '' ? '导入文献' : `导入文献《${truncate(trimmed, TITLE_LIMIT)}》`
}

function emitImported(
  ctx: LiteratureContext,
  projectId: string,
  paperId: string,
  item: ZoteroItemRecord,
  fileHash: string,
  reused: boolean,
  actor: Actor
): void {
  const handle = ctx.workspace.project(projectId)
  const added = handle.db
    .prepare(
      `INSERT INTO project_papers(project_id, paper_id, linked_at, linked_by)
       VALUES (?, ?, ?, ?) ON CONFLICT(project_id, paper_id) DO NOTHING`
    )
    .run(projectId, paperId, ctx.now(), actor === 'mcp' ? 'mcp' : 'user').changes
  if (added === 0) return
  ctx.events.emit(handle, {
    type: 'paper.imported',
    entityType: 'paper',
    entityId: paperId,
    new: {
      zotero_library_id: item.libraryId,
      zotero_key: item.key,
      file_hash: fileHash,
      project_id: projectId,
      reused
    },
    summary: importSummary(item.title),
    actor
  })
}

function linkExisting(
  ctx: LiteratureContext,
  projectId: string,
  item: ZoteroItemRecord,
  paperId: string,
  fileHash: string | null,
  actor: Actor
): ImportOutcome {
  const handle = ctx.workspace.project(projectId)
  const added = ctx.inTx(handle, () => {
    const changed = handle.db
      .prepare(
        `INSERT INTO project_papers(project_id, paper_id, linked_at, linked_by)
         VALUES (?, ?, ?, ?) ON CONFLICT(project_id, paper_id) DO NOTHING`
      )
      .run(projectId, paperId, ctx.now(), actor === 'mcp' ? 'mcp' : 'user').changes
    if (changed === 1) {
      ctx.events.emit(handle, {
        type: 'paper.imported',
        entityType: 'paper',
        entityId: paperId,
        new: {
          zotero_library_id: item.libraryId,
          zotero_key: item.key,
          file_hash: fileHash,
          project_id: projectId,
          reused: true
        },
        summary: importSummary(item.title),
        actor
      })
    }
    return changed === 1
  })
  ctx.notifyIndex(paperId, 'paper.imported', '导入文献')
  return {
    libraryId: item.libraryId,
    key: item.key,
    paperId,
    status: added ? 'reused' : 'skipped',
    warnings: [],
    error: null
  }
}

function compensate(ctx: LiteratureContext, tempId: string, paperId: string, moved: boolean): void {
  let removeRow = true
  if (paperId !== '') {
    try {
      const refs = ctx
        .global()
        .db.prepare(`SELECT COUNT(*) AS n FROM project_papers WHERE paper_id = ?`)
        .get(paperId) as { n: number }
      removeRow = refs.n === 0
    } catch {
      // The reference table is unreadable; removing the orphan is still safer
      // than leaving a row that points at a missing copy.
      removeRow = true
    }
  }
  if (removeRow && paperId !== '') {
    try {
      ctx.global().db.prepare(`DELETE FROM papers WHERE id = ?`).run(paperId)
      if (moved) ctx.files.remove(paperId)
    } catch {
      // Keep the copy when the row cannot be removed.
    }
  }
  ctx.files.remove(tempId)
}

async function importOne(
  ctx: LiteratureContext,
  reader: ZoteroReader,
  projectId: string,
  selection: ImportSelectionInternal,
  item: ZoteroItemRecord,
  actor: Actor
): Promise<ImportOutcome> {
  const outcome: ImportOutcome = {
    libraryId: item.libraryId,
    key: item.key,
    paperId: null,
    status: 'failed',
    warnings: [],
    error: null
  }
  try {
    const attachments = await reader.listAttachments(selection.libraryId, item.key)
    const chosen = chooseAttachment(attachments, selection.attachmentChoices[item.key] ?? '')
    if (chosen.status === 'no_pdf') {
      outcome.status = 'skipped'
      outcome.warnings.push('no_pdf_attachment')
      return outcome
    }
    if (chosen.status === 'needs_choice') {
      outcome.status = 'needs_attachment'
      outcome.warnings.push('multiple_pdf_attachments')
      return outcome
    }
    const attachment = chosen.attachment
    if (attachment === null) {
      outcome.status = 'skipped'
      return outcome
    }

    const existing = findPaperByZotero(ctx, item.libraryId, item.key)
    if (existing !== null) {
      let sourceHash: string | null = null
      try {
        sourceHash = hashFile((await reader.resolveAttachment(attachment)).path)
      } catch {
        // The local copy is authoritative; a missing source is only a warning.
      }
      const linked = linkExisting(ctx, projectId, item, existing.id, sourceHash, actor)
      if (sourceHash !== null && existing.fileHash !== null && sourceHash !== existing.fileHash) {
        linked.warnings.push('source_hash_differs')
      }
      try {
        ctx.files.stat(existing.id)
      } catch {
        linked.warnings.push('local_file_missing')
      }
      return linked
    }

    const resolved = await reader.resolveAttachment(attachment)
    if (!resolved.path.toLowerCase().endsWith('.pdf')) {
      throw appError(AppErrorCode.InvalidArgument, '附件不是 PDF')
    }
    const tempId = newId(ctx.clock)
    const stored = ctx.files.store(tempId, resolved.path, resolved.size)
    const metadata = metadataJson(item)
    const now = ctx.now()
    let paperId = tempId
    let moved = false
    let created = false
    try {
      const result = ctx.inTx(ctx.global(), () => {
        const inserted = ctx
          .global()
          .db.prepare(
            `INSERT INTO papers(id, zotero_library_id, zotero_key, attachment_key, metadata,
               file_path, file_hash, file_size, file_state, source_state, imported_at, updated_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'ok', 'ok', ?, ?)
             ON CONFLICT(zotero_library_id, zotero_key) DO NOTHING`
          )
          .run(
            tempId,
            item.libraryId,
            item.key,
            attachment.key,
            metadata,
            stored.relativePath,
            stored.hash,
            stored.size,
            now,
            now
          )
        if (inserted.changes === 1) return { created: true, id: tempId }
        const row = ctx
          .global()
          .db.prepare(`SELECT id FROM papers WHERE zotero_library_id = ? AND zotero_key = ?`)
          .get(item.libraryId, item.key) as { id: string } | undefined
        if (row === undefined) throw new Error('papers upsert conflict without an existing row')
        return { created: false, id: row.id }
      })
      created = result.created
      paperId = result.id
      if (created && paperId !== tempId) {
        ctx.files.move(tempId, paperId)
        moved = true
        ctx.global().db
          .prepare(`UPDATE papers SET file_path = ? WHERE id = ?`)
          .run(`library/papers/${paperId}/source.pdf`, paperId)
      }
    } catch (error) {
      compensate(ctx, tempId, paperId, moved)
      throw error
    }

    if (!created) {
      ctx.files.remove(tempId)
      return linkExisting(ctx, projectId, item, paperId, stored.hash, actor)
    }

    try {
      emitImported(ctx, projectId, paperId, item, stored.hash, false, actor)
    } catch (error) {
      compensate(ctx, tempId, paperId, moved)
      throw error
    }
    outcome.paperId = paperId
    outcome.status = 'imported'
    ctx.notifyIndex(paperId, 'paper.imported', '导入文献')
    return outcome
  } catch (error) {
    outcome.status = 'failed'
    outcome.error = error instanceof Error ? error.message : String(error)
    return outcome
  }
}

export async function runImport(
  ctx: LiteratureContext,
  projectId: string,
  raw: ImportSelectionInternal,
  actor: Actor,
  progress?: (percent: number, stage: string) => void
): Promise<ImportReport> {
  ctx.requireActive(projectId)
  if (actor === 'ai') {
    throw appError(AppErrorCode.InvalidArgument, '导入不支持 AI 操作者')
  }
  const preview = await previewImport(ctx, projectId, raw)
  const selection = normalizeSelection(raw)
  const reader = await ctx.requireZotero().reader()
  const outcomes: ImportOutcome[] = preview.errors.map((entry) => ({
    libraryId: selection.libraryId,
    key: entry.key,
    paperId: null,
    status: 'failed',
    warnings: [],
    error: entry.message
  }))
  const total = preview.entries.length
  for (let index = 0; index < preview.entries.length; index += 1) {
    const entry = preview.entries[index]
    if (entry === undefined) continue
    let item: ZoteroItemRecord
    try {
      item = await reader.getItem(selection.libraryId, entry.key)
    } catch (error) {
      outcomes.push({
        libraryId: selection.libraryId,
        key: entry.key,
        paperId: null,
        status: 'failed',
        warnings: [],
        error: error instanceof Error ? error.message : String(error)
      })
      progress?.(Math.round(((index + 1) / Math.max(total, 1)) * 100), entry.key)
      continue
    }
    outcomes.push(await importOne(ctx, reader, projectId, selection, item, actor))
    progress?.(Math.round(((index + 1) / Math.max(total, 1)) * 100), entry.key)
  }
  return {
    state: 'succeeded',
    progress: 100,
    outcomes,
    imported: outcomes.filter((outcome) => outcome.status === 'imported').length,
    reused: outcomes.filter((outcome) => outcome.status === 'reused').length,
    skipped: outcomes.filter((outcome) => outcome.status === 'skipped' || outcome.status === 'needs_attachment').length,
    failed: outcomes.filter((outcome) => outcome.status === 'failed').length,
    warnings: [],
    error: null
  }
}

export function importPreviewSummary(preview: ImportPreview): string {
  return `${preview.importable} 个可导入，${preview.alreadyPresent} 个已存在，${preview.missingPdf} 个缺少 PDF`
}