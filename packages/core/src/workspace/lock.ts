/**
 * Exclusive per-workspace write lock. It fails fast instead of
 * queueing: a migration that overlaps another workspace-wide operation must be
 * refused, not silently serialized behind an unknown amount of work.
 */
export type ReleaseWorkspaceLock = () => void

export class WorkspaceLock {
  private held = false

  get isHeld(): boolean {
    return this.held
  }

  /** Returns a release function, or undefined when the lock is already held. */
  tryAcquire(): ReleaseWorkspaceLock | undefined {
    if (this.held) return undefined
    this.held = true
    let released = false
    return () => {
      if (released) return
      released = true
      this.held = false
    }
  }
}