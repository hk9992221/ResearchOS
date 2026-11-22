/**
 * Attachment path resolution (plan §4.3). Both readers share it
 * so one file owns the containment rules:
 *
 *   - imported_file / imported_url: `<dataDir>/storage/<key>/<filename>`
 *   - `storage:<name>`: the same rule
 *   - `attachments:<rel>`: `<linkedBaseDir>/<rel>`
 *   - an absolute path is used as-is
 *   - storage and linked-base targets must stay inside their root
 *   - the result must be an existing regular file
 *
 * Nothing here writes to Zotero's data directory.
 */
import { statSync } from 'node:fs'
import * as path from 'node:path'

import { appError, AppErrorCode } from '../../../errors'
import type { ResolvedFile, ZoteroAttachmentRecord } from '../types'

export interface ResolverOptions {
  /** Zotero data directory (parent of `storage/`). */
  dataDir: string
  /** `extensions.zotero.baseAttachmentPath`; empty disables `attachments:`. */
  linkedBaseDir?: string
}

export interface AttachmentResolver {
  readonly dataDir: string
  readonly linkedBaseDir: string
  resolve(attachment: ZoteroAttachmentRecord): ResolvedFile
}

function unresolved(message: string, details?: Record<string, unknown>) {
  return appError(AppErrorCode.ExternalUnavailable, message, { details })
}

function isInside(root: string, target: string): boolean {
  const relative = path.relative(path.resolve(root), path.resolve(target))
  return relative === '' || (!relative.startsWith('..') && !path.isAbsolute(relative))
}

export function containedJoin(root: string, relative: string): string {
  const base = path.resolve(root)
  const target = path.resolve(base, relative)
  if (!isInside(base, target)) {
    throw appError(AppErrorCode.PathEscape, `附件路径越出根目录：${target}`, {
      details: { root: base, target }
    })
  }
  return target
}

export function createResolver(options: ResolverOptions): AttachmentResolver {
  const dataDir = options.dataDir.trim()
  const linkedBaseDir = (options.linkedBaseDir ?? '').trim()

  function storageTarget(key: string, filename: string): string {
    if (dataDir === '') throw unresolved('未配置 Zotero 数据目录')
    if (key.trim() === '') throw unresolved('storage 附件缺少 key')
    const storageRoot = path.join(dataDir, 'storage', key)
    return containedJoin(storageRoot, filename)
  }

  function targetOf(attachment: ZoteroAttachmentRecord): string {
    const raw = attachment.path.trim()
    const lower = raw.toLowerCase()

    if (raw === '') {
      if (
        (attachment.linkMode === 'imported_file' || attachment.linkMode === 'imported_url') &&
        attachment.key !== '' &&
        attachment.filename !== ''
      ) {
        return storageTarget(attachment.key, attachment.filename)
      }
      throw unresolved(`附件 ${attachment.key} 没有路径记录`)
    }
    if (lower.startsWith('storage:')) {
      const name = raw.slice('storage:'.length).trim() || attachment.filename
      return storageTarget(attachment.key, name)
    }
    if (lower.startsWith('attachments:')) {
      const relative = raw.slice('attachments:'.length).trim()
      if (relative === '') throw unresolved(`附件 ${attachment.key} 的 attachments: 路径为空`)
      if (path.isAbsolute(relative)) return path.normalize(relative)
      if (linkedBaseDir === '') throw unresolved('未配置 Zotero 链接附件基目录')
      return containedJoin(linkedBaseDir, relative)
    }
    if (path.isAbsolute(raw)) return path.normalize(raw)
    throw unresolved(`不支持的附件路径 ${JSON.stringify(raw)}`)
  }

  return {
    dataDir,
    linkedBaseDir,
    resolve(attachment) {
      const target = targetOf(attachment)
      let info
      try {
        info = statSync(target)
      } catch (cause) {
        throw appError(AppErrorCode.ExternalUnavailable, `附件无法读取：${target}`, {
          cause,
          details: { attachmentKey: attachment.key, path: target }
        })
      }
      if (!info.isFile()) {
        throw unresolved(`附件不是普通文件：${target}`, { attachmentKey: attachment.key })
      }
      return { path: target, size: info.size, modTime: info.mtime }
    }
  }
}