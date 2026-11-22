/**
 * The body-save state machine behind the editor.
 *
 * a save runs 600 ms after the last edit, the close flush saves
 * whatever is pending, and a stale hash is never overwritten implicitly. The
 * controller owns the timer and the token, so the React component only routes
 * text and renders the state it reports.
 */

import type { SaveResult } from '@researchos/shared'

import { conflictHashes, isConflict, notesApi } from './notes-api'

export type SaveState = 'saved' | 'unsaved' | 'saving' | 'conflict' | 'error'

export const SAVE_DEBOUNCE_MS = 600

export interface SaveConflict {
  readonly diskHash: string
  readonly expectedHash: string
}

export interface BodySaverOptions {
  readonly projectId: string
  readonly noteId: string
  readonly initialHash: string
  readonly debounceMs?: number
  readonly save?: (markdown: string, baseHash: string) => Promise<SaveResult>
  readonly onState: (state: SaveState) => void
  readonly onConflict: (conflict: SaveConflict, markdown: string) => void
  readonly onSaved?: (result: SaveResult) => void
}

export interface BodySaver {
  schedule(markdown: string): void
  /** Save now (Ctrl/Cmd+S, mode switch, flush); resolves when the write ends. */
  flush(): Promise<void>
  /** Accept the disk: new token, and the buffer is no longer dirty. */
  acceptDisk(markdown: string, hash: string): void
  /** Overwrite the disk after a conflict: the new token is the disk hash. */
  overwrite(conflict: SaveConflict): Promise<void>
  pending(): string | null
  dispose(): void
}

export function createBodySaver(options: BodySaverOptions): BodySaver {
  const debounceMs = options.debounceMs ?? SAVE_DEBOUNCE_MS
  const save =
    options.save ??
    ((markdown: string, baseHash: string) => notesApi.saveBody(options.projectId, options.noteId, markdown, baseHash))
  let baseHash = options.initialHash
  let pending: string | null = null
  let lastSaved: string | null = null
  let timer: number | null = null
  let inFlight: Promise<void> | null = null
  let disposed = false

  const setState = (state: SaveState): void => {
    if (!disposed) options.onState(state)
  }

  const clearTimer = (): void => {
    if (timer !== null) {
      window.clearTimeout(timer)
      timer = null
    }
  }

  const write = async (markdown: string): Promise<void> => {
    if (disposed) return
    setState('saving')
    try {
      const result = await save(markdown, baseHash)
      baseHash = result.fileHash
      lastSaved = markdown
      if (pending === markdown) pending = null
      setState(pending === null ? 'saved' : 'unsaved')
      options.onSaved?.(result)
      if (pending !== null) scheduleTimer()
    } catch (error) {
      if (isConflict(error)) {
        setState('conflict')
        const hashes = conflictHashes(error)
        options.onConflict(
          {
            diskHash: hashes.diskHash,
            // AppError details may omit the expected token; ours is the truth.
            expectedHash: baseHash
          },
          pending ?? markdown
        )
        return
      }
      setState('error')
      throw error
    }
  }

  const run = async (): Promise<void> => {
    if (inFlight !== null) {
      // Wait for the running write, then decide again: a close flush may have
      // arrived after that write started, and it must persist the newest text.
      try {
        await inFlight
      } catch {
        return
      }
    }
    if (pending === null || pending === lastSaved) {
      if (pending === null) setState('saved')
      return
    }
    const markdown = pending
    inFlight = write(markdown)
    try {
      await inFlight
    } finally {
      inFlight = null
    }
  }

  const scheduleTimer = (): void => {
    clearTimer()
    timer = window.setTimeout(() => {
      timer = null
      void run().catch(() => {
        // The error state was already reported; flush callers get the throw.
      })
    }, debounceMs)
  }

  return {
    schedule(markdown: string): void {
      if (disposed) return
      pending = markdown
      setState('unsaved')
      scheduleTimer()
    },
    async flush(): Promise<void> {
      clearTimer()
      try {
        await run()
      } catch {
        // The error state was already reported; a close handler must not
        // receive an unhandled rejection while the window is going away.
      }
    },
    acceptDisk(markdown: string, hash: string): void {
      clearTimer()
      baseHash = hash
      pending = null
      lastSaved = markdown
      setState('saved')
    },
    async overwrite(conflict: SaveConflict): Promise<void> {
      baseHash = conflict.diskHash
      setState('unsaved')
      await run()
    },
    pending(): string | null {
      return pending
    },
    dispose(): void {
      disposed = true
      clearTimer()
    }
  }
}