/**
 * In-process write locker (§5.2). The foundation has no per-project lock, so
 * export keeps one here: it never crosses the process boundary and never
 * touches disk. A workspace (migration) lock is exclusive and fails fast; the
 * per-project write lock waits and refuses to start under a workspace lock.
 */
import { AppErrorCode } from '@researchos/shared'

import { appError } from '../../errors'
import type { Locker } from '../../ports'

export class MemoryLocker implements Locker {
  private workspaceHeld = false
  private readonly gates = new Map<string, Promise<void>>()

  async withWorkspaceLock<T>(fn: () => T | Promise<T>): Promise<T> {
    if (this.workspaceHeld) {
      throw appError(AppErrorCode.Conflict, 'workspace is busy with another exclusive operation', {
        reason: 'workspace_busy'
      })
    }
    this.workspaceHeld = true
    try {
      return await fn()
    } finally {
      this.workspaceHeld = false
    }
  }

  async withProjectWriteLock<T>(projectId: string, fn: () => T | Promise<T>): Promise<T> {
    if (this.workspaceHeld) {
      throw appError(AppErrorCode.Conflict, 'workspace is busy with a migration', { reason: 'workspace_busy' })
    }
    const previous = this.gates.get(projectId) ?? Promise.resolve()
    const current = previous.then(async () => {
      if (this.workspaceHeld) {
        throw appError(AppErrorCode.Conflict, 'workspace is busy with a migration', {
          reason: 'workspace_busy'
        })
      }
      return fn()
    })
    // The stored gate never rejects: the next caller must be able to acquire it.
    this.gates.set(
      projectId,
      current.then(
        () => undefined,
        () => undefined
      )
    )
    return current
  }
}