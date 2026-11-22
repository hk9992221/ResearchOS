import { createHash } from 'node:crypto'
import { mkdirSync, readFileSync, readdirSync, rmSync, statSync } from 'node:fs'
import * as path from 'node:path'

import type { AttachmentInfo } from '@researchos/shared'

import { formatTimestamp } from '../../clock'
import { AppErrorCode } from '../../errors'
import { appError } from '../../errors'
import { loadNoteRow, readNoteFile, writeAtomicFile } from './notefile'
import { splitNoteFile } from './notefile'
import { entityDeleted, invalid, notFound, type NotesContext } from './support'
import { MAX_ATTACHMENT_BYTES } from './support'

interface ImageKind {
  ext: string
  contentType: string
}

function detectImage(data: Buffer): ImageKind | null {
  if (data.subarray(0, 8).equals(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]))) {
    return { ext: '.png', contentType: 'image/png' }
  }
  if (data.length >= 3 && data[0] === 0xff && data[1] === 0xd8 && data[2] === 0xff) {
    return { ext: '.jpg', contentType: 'image/jpeg' }
  }
  if (data.subarray(0, 6).toString('ascii') === 'GIF87a' || data.subarray(0, 6).toString('ascii') === 'GIF89a') {
    return { ext: '.gif', contentType: 'image/gif' }
  }
  if (data.length >= 12 && data.subarray(0, 4).toString('ascii') === 'RIFF' && data.subarray(8, 12).toString('ascii') === 'WEBP') {
    return { ext: '.webp', contentType: 'image/webp' }
  }
  const head = data.subarray(0, 1024).toString('utf8').replace(/^\uFEFF/u, '').trimStart().toLowerCase()
  if (head.startsWith('<svg') || (head.startsWith('<?xml') && head.includes('<svg'))) {
    return { ext: '.svg', contentType: 'image/svg+xml' }
  }
  return null
}

function sanitizeBase(filename: string): string {
  const base = path.basename(filename.split('\\').join('/'))
  const withoutExt = base.lastIndexOf('.') > 0 ? base.slice(0, base.lastIndexOf('.')) : base
  let result = ''
  let previousSeparator = false
  for (const character of withoutExt) {
    if (character === '.' || character === '_' || character === '-' || /\p{L}|\p{N}/u.test(character)) {
      result += character
      previousSeparator = false
    } else if (!previousSeparator) {
      result += '-'
      previousSeparator = true
    }
  }
  result = result.replace(/^[._-]+/u, '').replace(/[.-]+$/u, '')
  const runes = Array.from(result)
  if (runes.length > 80) result = runes.slice(0, 80).join('').replace(/[.-]+$/u, '')
  return result === '' ? 'image' : result
}

function attachmentsDirectory(ctx: NotesContext, noteId: string): { relative: string; absolute: string } {
  const relative = ctx.paths.attachmentsDir(ctx.projectId, noteId)
  return { relative, absolute: ctx.paths.abs(relative) }
}

function requireLiveNote(ctx: NotesContext, noteId: string): void {
  const row = loadNoteRow(ctx, noteId)
  if (row === null) throw notFound(`note ${noteId} not found`)
  if (row.deletedAt !== null) throw entityDeleted('note', noteId)
}

export interface AddAttachmentRequest {
  noteId: string
  sourcePath: string
  fileName?: string
  maxBytes?: number
}

export function addAttachment(ctx: NotesContext, request: AddAttachmentRequest): AttachmentInfo {
  requireLiveNote(ctx, request.noteId)
  const limit = request.maxBytes !== undefined && request.maxBytes > 0 ? request.maxBytes : MAX_ATTACHMENT_BYTES
  let data: Buffer
  let size: number
  try {
    size = statSync(request.sourcePath).size
    if (size > limit) {
      throw appError(AppErrorCode.LimitExceeded, `attachment exceeds ${limit} bytes`, {
        details: { limitBytes: limit, size }
      })
    }
    data = readFileSync(request.sourcePath)
  } catch (error) {
    if (error instanceof Error && 'code' in error) {
      const code = error.code
      if (code === 'ENOENT') throw invalid(`attachment source ${request.sourcePath} does not exist`)
    }
    throw error
  }
  if (data.length === 0) throw invalid('attachment is empty')
  if (data.length > limit) {
    throw appError(AppErrorCode.LimitExceeded, `attachment exceeds ${limit} bytes`, {
      details: { limitBytes: limit, size: data.length }
    })
  }
  const kind = detectImage(data)
  if (kind === null) throw invalid('only png, jpeg, gif, webp and svg images are accepted')
  const sha256 = createHash('sha256').update(data).digest('hex')
  const prefix = sha256.slice(0, 12)
  const directory = attachmentsDirectory(ctx, request.noteId)
  mkdirSync(directory.absolute, { recursive: true, mode: 0o755 })

  const entries = readdirSync(directory.absolute, { withFileTypes: true })
  for (const entry of entries) {
    if (entry.isDirectory() || entry.name.startsWith('.') || !entry.name.startsWith(`${prefix}-`)) continue
    const candidate = path.join(directory.absolute, entry.name)
    const candidateHash = createHash('sha256').update(readFileSync(candidate)).digest('hex')
    if (candidateHash === sha256) {
      const info = statSync(candidate)
      return {
        id: entry.name,
        noteId: request.noteId,
        fileName: entry.name,
        relativePath: `${directory.relative}/${entry.name}`,
        bytes: info.size,
        sha256,
        contentType: kind.contentType,
        createdAt: formatTimestamp(new Date(info.mtimeMs))
      }
    }
  }

  const base = sanitizeBase(request.fileName ?? path.basename(request.sourcePath))
  const fileName = `${prefix}-${base}${kind.ext}`
  writeAtomicFile(path.join(directory.absolute, fileName), data)
  return {
    id: fileName,
    noteId: request.noteId,
    fileName,
    relativePath: `${directory.relative}/${fileName}`,
    bytes: data.length,
    sha256,
    contentType: kind.contentType,
    createdAt: formatTimestamp(ctx.clock.now())
  }
}

export function listAttachments(ctx: NotesContext, noteId: string): AttachmentInfo[] {
  const directory = attachmentsDirectory(ctx, noteId)
  let entries
  try {
    entries = readdirSync(directory.absolute, { withFileTypes: true })
  } catch (error) {
    if (error instanceof Error && 'code' in error && error.code === 'ENOENT') return []
    throw error
  }
  const out: AttachmentInfo[] = []
  for (const entry of entries.sort((left, right) => left.name.localeCompare(right.name))) {
    if (entry.isDirectory() || entry.name.startsWith('.')) continue
    const absolute = path.join(directory.absolute, entry.name)
    const data = readFileSync(absolute)
    const info = statSync(absolute)
    const kind = detectImage(data)
    out.push({
      id: entry.name,
      noteId,
      fileName: entry.name,
      relativePath: `${directory.relative}/${entry.name}`,
      bytes: info.size,
      sha256: createHash('sha256').update(data).digest('hex'),
      contentType: kind?.contentType ?? null,
      createdAt: formatTimestamp(new Date(info.mtimeMs))
    })
  }
  return out
}

export function removeAttachment(ctx: NotesContext, noteId: string, attachmentId: string): { deleted: boolean } {
  requireLiveNote(ctx, noteId)
  if (attachmentId === '' || attachmentId.includes('/') || attachmentId.includes('\\') || attachmentId.startsWith('.')) {
    throw invalid('attachment id must be a stored file name')
  }
  const directory = attachmentsDirectory(ctx, noteId)
  const absolute = path.join(directory.absolute, attachmentId)
  try {
    rmSync(absolute)
  } catch (error) {
    if (error instanceof Error && 'code' in error && error.code === 'ENOENT') {
      throw notFound(`attachment ${attachmentId} not found`)
    }
    throw error
  }
  return { deleted: true }
}

export type AttachmentIssueKind = 'missing_reference' | 'unreferenced_file'

export interface AttachmentIssue {
  kind: AttachmentIssueKind
  noteId: string
  markdownPath: string
  path: string
  detail: string
}

const IMAGE_REFERENCE = /!?\[[^\]]*\]\(\s*<?([^)>\s]+)>?[^)]*\)/gu

/** Compares body image references with the files on disk; never deletes. */
export function checkAttachmentReferences(ctx: NotesContext, noteId: string): AttachmentIssue[] {
  const row = loadNoteRow(ctx, noteId)
  if (row === null) throw notFound(`note ${noteId} not found`)
  const file = readNoteFile(ctx, noteId, row.filePath)
  const body = file === null ? '' : splitNoteFile(file.data).body
  const directory = attachmentsDirectory(ctx, noteId)
  const marker = `attachments/${noteId}/`
  const referenced = new Map<string, true>()
  const issues: AttachmentIssue[] = []

  for (const match of body.matchAll(IMAGE_REFERENCE)) {
    const target = match[1] ?? ''
    let decoded = target
    try {
      decoded = decodeURIComponent(target)
    } catch {
      decoded = target
    }
    const index = decoded.indexOf(marker)
    if (index < 0) continue
    const filePart = decoded.slice(index + marker.length)
    if (filePart === '' || filePart.includes('/') || filePart.includes('\\') || filePart === '..' || filePart.startsWith('.')) {
      issues.push({
        kind: 'missing_reference',
        noteId,
        markdownPath: target,
        path: '',
        detail: 'reference escapes the attachment directory'
      })
      continue
    }
    if (referenced.has(filePart)) continue
    referenced.set(filePart, true)
    try {
      statSync(path.join(directory.absolute, filePart))
    } catch (error) {
      if (error instanceof Error && 'code' in error && error.code === 'ENOENT') {
        issues.push({
          kind: 'missing_reference',
          noteId,
          markdownPath: target,
          path: `${directory.relative}/${filePart}`,
          detail: 'referenced image is not on disk'
        })
        continue
      }
      throw error
    }
  }

  let entries
  try {
    entries = readdirSync(directory.absolute, { withFileTypes: true })
  } catch (error) {
    if (error instanceof Error && 'code' in error && error.code === 'ENOENT') return issues
    throw error
  }
  for (const entry of entries) {
    if (entry.isDirectory() || entry.name.startsWith('.') || referenced.has(entry.name)) continue
    issues.push({
      kind: 'unreferenced_file',
      noteId,
      markdownPath: `../${marker}${entry.name}`,
      path: `${directory.relative}/${entry.name}`,
      detail: 'file on disk is not referenced by the body'
    })
  }
  return issues
}