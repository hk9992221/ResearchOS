import { realpathSync } from 'node:fs'
import * as path from 'node:path'

import { AppError, AppErrorCode, appError } from './errors'

/**
 * Every workspace path decision. Internal paths are recorded
 * relative to the workspace root with forward slashes; external paths are
 * absolute and are never written by the foundation layer.
 *
 * The rules are Windows-safe on every host: a backslash, a drive prefix, a UNC
 * root, an empty segment and `.`/`..` are all rejected before the filesystem
 * sees the path, and a symlink that resolves outside the root is refused.
 */
export class Paths {
  readonly root: string

  constructor(root: string) {
    if (root.trim() === '') throw appError(AppErrorCode.PathEscape, 'paths: empty workspace root')
    const absolute = path.resolve(root)
    let resolved: string
    try {
      resolved = realpathSync(absolute)
    } catch (cause) {
      throw appError(AppErrorCode.PathEscape, `paths: stat root ${absolute}: ${String(cause)}`, { cause })
    }
    this.root = path.resolve(resolved)
  }

  noteFile(projectId: string, noteId: string): string {
    return `projects/${projectId}/notes/${noteId}.md`
  }

  attachmentsDir(projectId: string, noteId: string): string {
    return `projects/${projectId}/attachments/${noteId}`
  }

  projectDir(projectId: string): string {
    return `projects/${projectId}`
  }

  notesDir(projectId: string): string {
    return `projects/${projectId}/notes`
  }

  projectDb(projectId: string): string {
    return `projects/${projectId}/project.db`
  }

  paperDir(paperId: string): string {
    return `library/papers/${paperId}`
  }

  paperPdf(paperId: string): string {
    return `library/papers/${paperId}/source.pdf`
  }

  exportsDir(): string {
    return 'exports'
  }

  /**
   * Converts an internal relative path to an absolute OS path. Symlinks for the
   * deepest existing ancestor are resolved, then re-checked against the root.
   */
  abs(relative: string): string {
    this.validateInternal(relative)
    const joined = path.join(this.root, ...relative.split('/'))
    return this.resolveInside(joined, relative)
  }

  /** Converts an absolute path inside the workspace to a canonical internal path. */
  rel(absolute: string): string {
    if (!path.isAbsolute(absolute)) {
      throw appError(AppErrorCode.PathEscape, `paths: ${JSON.stringify(absolute)} is not absolute`)
    }
    const resolved = resolveExisting(path.resolve(absolute))
    if (!isWithin(this.root, resolved)) {
      throw appError(AppErrorCode.PathEscape, `paths: ${absolute} is outside the workspace root`)
    }
    const relative = path.relative(this.root, resolved).split(path.sep).join('/')
    if (relative === '') throw appError(AppErrorCode.PathEscape, 'paths: path is the workspace root')
    return relative
  }

  /** Canonical check: relative, forward slashes, no drive, no `.`/`..`/empty segment. */
  validateInternal(relative: string): void {
    const reject = (reason: string): never => {
      throw appError(AppErrorCode.PathEscape, `paths: ${JSON.stringify(relative)} ${reason}`)
    }
    if (relative === '') reject('is empty')
    if (relative.includes('\\')) reject('must use forward slashes')
    if (relative.startsWith('/')) reject('must be relative')
    if (/^[A-Za-z]:/.test(relative)) reject('must not carry a drive')
    if (path.isAbsolute(relative.split('/').join(path.sep))) reject('must be relative')
    for (const segment of relative.split('/')) {
      if (segment === '') reject('has an empty segment')
      if (segment === '.' || segment === '..') reject(`has a ${JSON.stringify(segment)} segment`)
    }
  }

  /** True when the absolute path lies inside the workspace root. */
  isInternal(absolute: string): boolean {
    if (!path.isAbsolute(absolute)) return false
    return isWithin(this.root, resolveExisting(path.resolve(absolute)))
  }

  private resolveInside(joined: string, relative: string): string {
    const resolved = resolveExisting(joined)
    if (!isWithin(this.root, resolved)) {
      throw appError(
        AppErrorCode.PathEscape,
        `paths: ${JSON.stringify(relative)} resolves outside the workspace root`
      )
    }
    return resolved
  }
}

/**
 * Resolves symlinks for the deepest existing ancestor and re-appends the
 * missing tail, so a path that does not exist yet still gets a stable answer.
 */
export function resolveExisting(target: string): string {
  const tail: string[] = []
  let current = path.resolve(target)
  for (;;) {
    try {
      const resolved = realpathSync(current)
      return tail.length === 0 ? resolved : path.join(resolved, ...tail)
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error
      const parent = path.dirname(current)
      if (parent === current) throw error
      tail.unshift(path.basename(current))
      current = parent
    }
  }
}

function isWithin(root: string, target: string): boolean {
  const relative = path.relative(root, target)
  if (relative === '') return true
  if (relative === '..' || relative.startsWith(`..${path.sep}`)) return false
  return !path.isAbsolute(relative)
}