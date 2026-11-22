import type { OutboundLink } from '@researchos/shared'
import { isUuid } from '@researchos/shared'

import { loadNoteRow, readNoteFile } from './notefile'
import { splitNoteFile } from './notefile'
import { notFound, type NotesContext } from './support'

const ROS_LINK = /ros:\/\/([a-z_]+)\/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})(?:#page=(\d+))?/gu
const MARKDOWN_LINK = /\]\(\s*([^()\s]+?\.md)(?:#[^()\s]*)?\s*\)/gu

interface LinkHit {
  position: number
  href: string
  type: string
  targetId: string
  pageNo: number | null
}

function resolveTarget(
  ctx: NotesContext,
  type: string,
  id: string
): { exists: boolean; deleted: boolean } {
  if (type === 'paper') {
    if (ctx.globalDb === null) return { exists: false, deleted: false }
    const row = ctx.globalDb.prepare(`SELECT 1 AS one FROM papers WHERE id = ?`).get(id)
    return { exists: row !== undefined, deleted: false }
  }
  const table =
    type === 'note'
      ? 'notes'
      : type === 'db_record'
        ? 'db_records'
        : type === 'idea'
          ? 'ideas'
          : type === 'experiment'
            ? 'experiments'
            : null
  if (table === null) return { exists: false, deleted: false }
  const row = ctx.db.prepare(`SELECT deleted_at FROM ${table} WHERE id = ?`).get(id) as
    | { deleted_at: string | null }
    | undefined
  if (row === undefined) return { exists: false, deleted: false }
  return { exists: true, deleted: row.deleted_at !== null }
}

export function outboundLinks(ctx: NotesContext, noteId: string): OutboundLink[] {
  const row = loadNoteRow(ctx, noteId)
  if (row === null) throw notFound(`note ${noteId} not found`)
  const file = readNoteFile(ctx, noteId, row.filePath)
  if (file === null) return []
  const body = splitNoteFile(file.data).body
  const hits: LinkHit[] = []

  for (const match of body.matchAll(ROS_LINK)) {
    const href = match[0]
    const type = match[1] ?? ''
    const targetId = match[2] ?? ''
    const pageText = match[3]
    const pageNo = pageText === undefined ? null : Number(pageText)
    hits.push({ position: match.index ?? 0, href, type, targetId, pageNo })
  }
  for (const match of body.matchAll(MARKDOWN_LINK)) {
    const href = match[1] ?? ''
    const base = href.split('/').pop() ?? ''
    const targetId = base.endsWith('.md') ? base.slice(0, -3) : base
    if (!isUuid(targetId)) {
      hits.push({ position: match.index ?? 0, href, type: 'note', targetId, pageNo: null })
      continue
    }
    hits.push({ position: match.index ?? 0, href, type: 'note', targetId, pageNo: null })
  }
  hits.sort((left, right) => left.position - right.position)
  return hits.map((hit) => {
    const target = resolveTarget(ctx, hit.type, hit.targetId)
    return {
      href: hit.href,
      type: hit.type === '' ? null : hit.type,
      targetId: hit.targetId === '' ? null : hit.targetId,
      pageNo: hit.pageNo,
      resolved: target.exists && !target.deleted
    }
  })
}

export interface LinkTargetView {
  type: string
  id: string
  title: string
  deleted: boolean
  missing: boolean
  filePath: string | null
}

/** Resolves one reference for the link renderer. */
export function resolveEntityLink(ctx: NotesContext, type: string, id: string): LinkTargetView {
  const missing = (): LinkTargetView => ({ type, id, title: '', deleted: false, missing: true, filePath: null })
  if (type === 'note') {
    const row = loadNoteRow(ctx, id)
    if (row === null) return missing()
    return {
      type,
      id,
      title: row.title,
      deleted: row.deletedAt !== null,
      missing: false,
      filePath: row.filePath
    }
  }
  if (type === 'db_record') {
    const row = ctx.db.prepare(`SELECT title, deleted_at FROM db_records WHERE id = ?`).get(id) as
      | { title: string; deleted_at: string | null }
      | undefined
    if (row === undefined) return missing()
    return { type, id, title: row.title, deleted: row.deleted_at !== null, missing: false, filePath: null }
  }
  if (type === 'idea' || type === 'experiment') {
    const table = type === 'idea' ? 'ideas' : 'experiments'
    const row = ctx.db.prepare(`SELECT title, deleted_at FROM ${table} WHERE id = ?`).get(id) as
      | { title: string; deleted_at: string | null }
      | undefined
    if (row === undefined) return missing()
    return { type, id, title: row.title, deleted: row.deleted_at !== null, missing: false, filePath: null }
  }
  if (type === 'paper') {
    if (ctx.globalDb === null) return missing()
    const row = ctx.globalDb
      .prepare(`SELECT COALESCE(json_extract(metadata, '$.title'), '') AS title FROM papers WHERE id = ?`)
      .get(id) as { title: string } | undefined
    if (row === undefined) return missing()
    return { type, id, title: row.title, deleted: false, missing: false, filePath: null }
  }
  return missing()
}