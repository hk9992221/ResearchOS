import { createHash } from 'node:crypto'
import {
  closeSync,
  createReadStream,
  lstatSync,
  openSync,
  readFileSync,
  readSync,
  readdirSync,
  realpathSync,
  type Stats
} from 'node:fs'
import { isUtf8 } from 'node:buffer'
import * as path from 'node:path'
import { randomUUID } from 'node:crypto'

import type {
  PluginAuthorizedDir,
  PluginFileEntry,
  PluginFileStat,
  PluginHostContext,
  PluginNavigateTarget,
  PluginUiToastParams
} from '@researchos/shared'
import {
  PLUGIN_FILES_DEFAULT_IGNORES,
  PLUGIN_FILES_LIST_DEFAULT_MAX_DEPTH,
  PLUGIN_FILES_LIST_MAX_ENTRIES,
  PLUGIN_FILES_READ_TEXT_DEFAULT_BYTES,
  PLUGIN_FILES_READ_TEXT_MAX_BYTES
} from '@researchos/shared'

import { formatTimestamp, type Clock, SystemClock } from '../../clock'
import { AppErrorCode, appError } from '../../errors'
import type { GlobalHandle } from '../../workspace/handle'

/**
 * User-authorized plugin folders. The plugin only ever names an
 * opaque `dirId` plus a relative path; the host resolves it against the stored
 * root and refuses traversal and symlinks at every segment.
 */

export interface PluginWatchedDirRow {
  id: string
  pluginId: string
  projectId: string | null
  dirPath: string
  displayName: string
  createdAt: string
}

interface Row {
  id: string
  plugin_id: string
  project_id: string | null
  dir_path: string
  display_name: string
  created_at: string
}

function validation(message: string, field: string): never {
  throw appError(AppErrorCode.InvalidArgument, message, { field })
}

export function normalizeRelativePath(input: unknown, field = 'relativePath'): string {
  if (typeof input !== 'string') validation('relativePath must be a string', field)
  const value = input.replaceAll('\\', '/').trim()
  if (value === '' || value === '.') return ''
  if (value.startsWith('/') || /^[A-Za-z]:/.test(value)) {
    validation('relativePath must be relative', field)
  }
  if (value.includes('\0')) validation('relativePath must not contain NUL', field)
  const segments = value.split('/')
  for (const segment of segments) {
    if (segment === '' || segment === '.' || segment === '..') {
      validation('relativePath must not contain traversal segments', field)
    }
  }
  return segments.join('/')
}

function isInside(root: string, candidate: string): boolean {
  const relative = path.relative(root, candidate)
  return relative === '' || (!relative.startsWith('..') && !path.isAbsolute(relative))
}

function assertNoSymlink(root: string, segments: string[]): void {
  let current = root
  for (const segment of segments) {
    current = path.join(current, segment)
    const info = lstatSync(current, { throwIfNoEntry: false })
    if (info === undefined) return
    if (info.isSymbolicLink()) {
      throw appError(AppErrorCode.PathEscape, 'symbolic links are not allowed', {
        field: 'relativePath'
      })
    }
  }
}

function hashFile(absolute: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const hash = createHash('sha256')
    const stream = createReadStream(absolute)
    stream.on('error', reject)
    stream.on('data', (chunk) => hash.update(chunk))
    stream.on('end', () => resolve(hash.digest('hex')))
  })
}

/** Cheap text probe: no NUL byte and valid UTF-8 in the leading window. */
function probeText(absolute: string, size: number): boolean {
  if (size > 16 * 1024 * 1024) return false
  const head = Buffer.allocUnsafe(Math.min(size, 8192))
  const fd = openSync(absolute, 'r')
  let read = 0
  try {
    read = readSync(fd, head, 0, head.length, 0)
  } finally {
    closeSync(fd)
  }
  const slice = head.subarray(0, read)
  if (slice.includes(0)) return false
  return isUtf8(slice)
}

function toEntry(root: string, absolute: string, info: Stats): PluginFileEntry {
  return {
    relativePath: path.relative(root, absolute).split(path.sep).join('/'),
    kind: info.isDirectory() ? 'dir' : 'file',
    size: info.isDirectory() ? 0 : info.size,
    mtimeMs: Math.round(info.mtimeMs)
  }
}

function globToRegExp(pattern: string): RegExp {
  const escaped = pattern
    .replace(/[.+^${}()|[\]\\]/g, '\\$&')
    .replaceAll('*', '.*')
    .replaceAll('?', '.')
  return new RegExp(`^${escaped}$`)
}

export interface PluginFilesOptions {
  global: GlobalHandle
  clock?: Clock
}

export class PluginFilesService {
  private readonly global: GlobalHandle
  private readonly clock: Clock

  constructor(options: PluginFilesOptions) {
    this.global = options.global
    this.clock = options.clock ?? new SystemClock()
  }

  authorize(
    pluginId: string,
    dirPath: string,
    displayName: string,
    projectId: string | null
  ): PluginAuthorizedDir {
    const absolute = path.resolve(dirPath)
    let real: string
    try {
      real = realpathSync(absolute)
    } catch {
      throw appError(AppErrorCode.NotFound, `directory does not exist: ${displayName}`, {
        field: 'dirPath'
      })
    }
    const info = lstatSync(real, { throwIfNoEntry: false })
    if (info === undefined || !info.isDirectory() || info.isSymbolicLink()) {
      throw appError(AppErrorCode.InvalidArgument, 'selected path must be a real directory', {
        field: 'dirPath'
      })
    }
    const id = randomUUID()
    const name = displayName.trim() === '' ? path.basename(real) : displayName.trim()
    this.global.db
      .prepare(
        `INSERT INTO plugin_watched_dirs(id, plugin_id, project_id, dir_path, display_name, created_at)
         VALUES (?, ?, ?, ?, ?, ?)`
      )
      .run(id, pluginId, projectId, real, name, formatTimestamp(this.clock.now()))
    return { dirId: id, displayName: name, projectId }
  }

  listDirs(pluginId: string, projectId?: string | null): PluginAuthorizedDir[] {
    const rows =
      projectId === undefined || projectId === null
        ? (this.global.db
            .prepare('SELECT * FROM plugin_watched_dirs WHERE plugin_id = ? ORDER BY created_at ASC')
            .all(pluginId) as Row[])
        : (this.global.db
            .prepare(
              'SELECT * FROM plugin_watched_dirs WHERE plugin_id = ? AND (project_id IS NULL OR project_id = ?) ORDER BY created_at ASC'
            )
            .all(pluginId, projectId) as Row[])
    return rows.map((row) => ({
      dirId: row.id,
      displayName: row.display_name,
      projectId: row.project_id
    }))
  }

  revoke(pluginId: string, dirId: string): boolean {
    const result = this.global.db
      .prepare('DELETE FROM plugin_watched_dirs WHERE id = ? AND plugin_id = ?')
      .run(dirId, pluginId)
    return result.changes > 0
  }

  requireOwned(pluginId: string, dirId: string): PluginWatchedDirRow {
    const row = this.global.db
      .prepare('SELECT * FROM plugin_watched_dirs WHERE id = ? AND plugin_id = ?')
      .get(dirId, pluginId) as Row | undefined
    if (row === undefined) {
      throw appError(AppErrorCode.NotFound, 'authorized directory not found', { field: 'dirId' })
    }
    return {
      id: row.id,
      pluginId: row.plugin_id,
      projectId: row.project_id,
      dirPath: row.dir_path,
      displayName: row.display_name,
      createdAt: row.created_at
    }
  }

  /**
   * Resolves `relativePath` under the stored root; every existing segment is
   * checked with `lstat` so a symlink cannot smuggle the target outside.
   */
  resolve(
    pluginId: string,
    dirId: string,
    relativePath: unknown,
    options: { allowMissing?: boolean } = {}
  ): { row: PluginWatchedDirRow; absolute: string; relativePath: string } {
    const row = this.requireOwned(pluginId, dirId)
    const relative = normalizeRelativePath(relativePath)
    const rootInfo = lstatSync(row.dirPath, { throwIfNoEntry: false })
    if (rootInfo === undefined || !rootInfo.isDirectory() || rootInfo.isSymbolicLink()) {
      throw appError(AppErrorCode.NotFound, 'authorized directory is no longer available', {
        field: 'dirId'
      })
    }
    const segments = relative === '' ? [] : relative.split('/')
    assertNoSymlink(row.dirPath, segments)
    const absolute = path.resolve(row.dirPath, relative)
    if (!isInside(row.dirPath, absolute)) {
      throw appError(AppErrorCode.PathEscape, 'relativePath escapes the authorized directory', {
        field: 'relativePath'
      })
    }
    if (options.allowMissing !== true) {
      const info = lstatSync(absolute, { throwIfNoEntry: false })
      if (info === undefined) {
        throw appError(AppErrorCode.NotFound, `path not found: ${relative}`, { field: 'relativePath' })
      }
      if (info.isSymbolicLink()) {
        throw appError(AppErrorCode.PathEscape, 'symbolic links are not allowed', {
          field: 'relativePath'
        })
      }
    }
    return { row, absolute, relativePath: relative }
  }

  list(
    pluginId: string,
    params: { dirId: string; relativePath?: string; ignore?: string[]; maxDepth?: number }
  ): { entries: PluginFileEntry[]; truncated: boolean } {
    const { row, absolute } = this.resolve(pluginId, params.dirId, params.relativePath ?? '')
    const info = lstatSync(absolute)
    if (!info.isDirectory()) validation('relativePath must name a directory', 'relativePath')
    const ignore = [...PLUGIN_FILES_DEFAULT_IGNORES, ...(params.ignore ?? [])]
      .filter((pattern) => typeof pattern === 'string' && pattern.trim() !== '')
      .map(globToRegExp)
    const maxDepth = Math.min(
      Math.max(params.maxDepth ?? PLUGIN_FILES_LIST_DEFAULT_MAX_DEPTH, 1),
      64
    )
    const entries: PluginFileEntry[] = []
    let truncated = false
    const walk = (directory: string, depth: number): void => {
      if (truncated || depth > maxDepth) return
      let children: string[]
      try {
        children = readdirSync(directory).sort()
      } catch {
        return
      }
      for (const child of children) {
        if (ignore.some((pattern) => pattern.test(child))) continue
        if (entries.length >= PLUGIN_FILES_LIST_MAX_ENTRIES + 1) {
          truncated = true
          return
        }
        const childPath = path.join(directory, child)
        const childInfo = lstatSync(childPath, { throwIfNoEntry: false })
        if (childInfo === undefined || childInfo.isSymbolicLink()) continue
        if (entries.length >= PLUGIN_FILES_LIST_MAX_ENTRIES) {
          truncated = true
          return
        }
        entries.push(toEntry(row.dirPath, childPath, childInfo))
        if (childInfo.isDirectory()) walk(childPath, depth + 1)
      }
    }
    walk(absolute, 1)
    return { entries, truncated }
  }

  async stat(
    pluginId: string,
    params: { dirId: string; relativePath: string; includeHash?: boolean }
  ): Promise<PluginFileStat> {
    const { absolute } = this.resolve(pluginId, params.dirId, params.relativePath, {
      allowMissing: true
    })
    const info = lstatSync(absolute, { throwIfNoEntry: false })
    if (info === undefined) return { exists: false }
    if (info.isSymbolicLink()) {
      throw appError(AppErrorCode.PathEscape, 'symbolic links are not allowed', {
        field: 'relativePath'
      })
    }
    if (info.isDirectory()) {
      return { exists: true, kind: 'dir', size: 0, mtimeMs: Math.round(info.mtimeMs) }
    }
    if (!info.isFile()) return { exists: true, kind: 'file', size: info.size }
    const result: PluginFileStat = {
      exists: true,
      kind: 'file',
      size: info.size,
      mtimeMs: Math.round(info.mtimeMs),
      isText: probeText(absolute, info.size)
    }
    if (params.includeHash === true) result.sha256 = await hashFile(absolute)
    return result
  }

  readText(
    pluginId: string,
    params: { dirId: string; relativePath: string; maxBytes?: number }
  ): { text: string; bytes: number; truncated: boolean } {
    const { absolute } = this.resolve(pluginId, params.dirId, params.relativePath)
    const info = lstatSync(absolute)
    if (!info.isFile()) validation('relativePath must name a file', 'relativePath')
    const limit = Math.min(
      Math.max(params.maxBytes ?? PLUGIN_FILES_READ_TEXT_DEFAULT_BYTES, 1),
      PLUGIN_FILES_READ_TEXT_MAX_BYTES
    )
    const buffer = readFileSync(absolute)
    const truncated = buffer.length > limit
    const slice = truncated ? buffer.subarray(0, limit) : buffer
    if (slice.includes(0)) {
      validation('file appears to be binary (NUL byte)', 'relativePath')
    }
    return {
      text: slice.toString('utf8'),
      bytes: buffer.length,
      truncated
    }
  }
}

/** Host-side watch lifecycle the RPC router drives (desktop implementation). */
export interface PluginWatchStartRequest {
  pluginId: string
  dirId: string
  projectId: string | null
  rootPath: string
  ignore: string[]
  debounceMs: number
}

export interface PluginWatchGateway {
  start(request: PluginWatchStartRequest): { watchId: string }
  stop(pluginId: string, watchId: string): boolean
  stopAll(pluginId: string): void
  stopDir(pluginId: string, dirId: string): number
}

/** Desktop-only operations the core validates before delegating. */
export interface PluginHostOperations {
  /** Native folder picker; the host persists the grant in the core service. */
  pickDirectory?(
    pluginId: string,
    params: { projectId?: string; title?: string }
  ): Promise<{ cancelled: boolean; dirPath?: string; displayName?: string }>
  revealInFolder?(pluginId: string, absolutePath: string): boolean | Promise<boolean>
  openPath?(pluginId: string, absolutePath: string): boolean | Promise<boolean>
  navigate?(
    pluginId: string,
    params: { target: PluginNavigateTarget; projectId?: string; entityType?: string; entityId?: string }
  ): boolean
  toast?(pluginId: string, params: PluginUiToastParams): void
  context?(): PluginHostContext
}

/** File extensions `files.open` refuses even though the OS may execute them. */
export const PLUGIN_OPEN_DENY_EXTENSIONS = new Set([
  '.exe',
  '.bat',
  '.cmd',
  '.com',
  '.msi',
  '.sh',
  '.ps1',
  '.vbs',
  '.js',
  '.jse',
  '.wsf',
  '.scr',
  '.pif',
  '.jar'
])
