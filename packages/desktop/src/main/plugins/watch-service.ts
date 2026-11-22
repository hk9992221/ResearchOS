import { lstatSync, watch } from 'node:fs'
import * as path from 'node:path'
import { randomUUID } from 'node:crypto'

import type { PluginHostEvent, PluginWatchChange } from '@researchos/shared'

import type {
  PluginWatchGateway,
  PluginWatchStartRequest
} from '@researchos/core/modules/plugins/index'

/**
 * Authorized-directory watcher. One recursive `fs.watch` per
 * `watchId`; writes are merged per path and flushed after the configured
 * debounce. Watches belong to a plugin, so disable/uninstall can stop them all.
 */

export interface WatchHandle {
  close(): void
}

export type WatchFactory = (
  root: string,
  listener: (eventType: 'rename' | 'change', filename: string | null) => void
) => WatchHandle

export interface PluginWatchServiceOptions {
  /** Test seam; defaults to `fs.watch(root, { recursive: true })`. */
  factory?: WatchFactory
  /** Delivers `files.changed` to the plugin runtime and mounted panels. */
  emit(pluginId: string, event: PluginHostEvent): void
  setTimer?: (callback: () => void, ms: number) => ReturnType<typeof setTimeout>
  clearTimer?: (timer: ReturnType<typeof setTimeout>) => void
  stat?: (absolute: string) => { isFile: boolean; size: number; mtimeMs: number } | null
}

interface WatchState {
  watchId: string
  pluginId: string
  dirId: string
  projectId: string | null
  root: string
  debounceMs: number
  ignore: RegExp[]
  handle: WatchHandle
  pending: Map<string, 'add' | 'change' | 'unlink'>
  timer: ReturnType<typeof setTimeout> | null
}

const TEMPORARY = [/\.tmp$/i, /\.swp$/i, /^~\$/, /^\.DS_Store$/i, /\.crdownload$/i]

function globToRegExp(pattern: string): RegExp {
  return new RegExp(
    `^${pattern
      .replace(/[.+^${}()|[\]\\]/g, '\\$&')
      .replaceAll('*', '.*')
      .replaceAll('?', '.')}$`
  )
}

function defaultFactory(root: string, listener: (eventType: 'rename' | 'change', filename: string | null) => void): WatchHandle {
  const watcher = watch(root, { recursive: true }, (eventType, filename) => {
    listener(eventType as 'rename' | 'change', filename === null ? null : String(filename))
  })
  return { close: () => watcher.close() }
}

export class PluginWatchService implements PluginWatchGateway {
  private readonly watches = new Map<string, WatchState>()
  private readonly byPlugin = new Map<string, Set<string>>()
  private readonly options: PluginWatchServiceOptions
  private readonly factory: WatchFactory
  private readonly setTimer: NonNullable<PluginWatchServiceOptions['setTimer']>
  private readonly clearTimer: NonNullable<PluginWatchServiceOptions['clearTimer']>

  constructor(options: PluginWatchServiceOptions) {
    this.options = options
    this.factory = options.factory ?? defaultFactory
    this.setTimer = options.setTimer ?? ((callback, ms) => setTimeout(callback, ms))
    this.clearTimer = options.clearTimer ?? ((timer) => clearTimeout(timer))
  }

  start(request: PluginWatchStartRequest): { watchId: string } {
    const watchId = randomUUID()
    const ignore = request.ignore
      .filter((pattern: string) => pattern.trim() !== '')
      .map(globToRegExp)
    const state: WatchState = {
      watchId,
      pluginId: request.pluginId,
      dirId: request.dirId,
      projectId: request.projectId,
      root: request.rootPath,
      debounceMs: request.debounceMs,
      ignore,
      handle: { close: () => undefined },
      pending: new Map(),
      timer: null
    }
    state.handle = this.factory(state.root, (eventType, filename) => {
      this.onRawEvent(state, eventType, filename)
    })
    this.watches.set(watchId, state)
    const set = this.byPlugin.get(state.pluginId) ?? new Set<string>()
    set.add(watchId)
    this.byPlugin.set(state.pluginId, set)
    return { watchId }
  }

  stop(pluginId: string, watchId: string): boolean {
    const state = this.watches.get(watchId)
    if (state === undefined || state.pluginId !== pluginId) return false
    this.dispose(state)
    return true
  }

  stopAll(pluginId: string): void {
    for (const watchId of [...(this.byPlugin.get(pluginId) ?? [])]) {
      const state = this.watches.get(watchId)
      if (state !== undefined) this.dispose(state)
    }
    this.byPlugin.delete(pluginId)
  }

  stopDir(pluginId: string, dirId: string): number {
    let stopped = 0
    for (const watchId of [...(this.byPlugin.get(pluginId) ?? [])]) {
      const state = this.watches.get(watchId)
      if (state?.dirId !== dirId) continue
      this.dispose(state)
      stopped += 1
    }
    return stopped
  }

  /** Flushes pending events immediately (tests and shutdown). */
  flush(watchId: string): void {
    const state = this.watches.get(watchId)
    if (state !== undefined) this.flushState(state)
  }

  activeCount(pluginId?: string): number {
    if (pluginId === undefined) return this.watches.size
    return this.byPlugin.get(pluginId)?.size ?? 0
  }

  close(): void {
    for (const state of [...this.watches.values()]) this.dispose(state)
  }

  private onRawEvent(
    state: WatchState,
    eventType: 'rename' | 'change',
    filename: string | null
  ): void {
    if (filename === null || filename === '') return
    const relative = filename.split(path.sep).join('/')
    const basename = relative.slice(relative.lastIndexOf('/') + 1)
    if (TEMPORARY.some((pattern) => pattern.test(basename))) return
    if (state.ignore.some((pattern) => pattern.test(basename) || pattern.test(relative))) return
    const absolute = path.join(state.root, ...relative.split('/'))
    const info = (this.options.stat ?? defaultStat)(absolute)
    const previous = state.pending.get(relative)
    const next = info === null ? 'unlink' : previous !== undefined ? 'change' : eventType === 'rename' ? 'add' : 'change'
    state.pending.set(relative, next)
    if (state.timer !== null) this.clearTimer(state.timer)
    state.timer = this.setTimer(() => {
      state.timer = null
      this.flushState(state)
    }, state.debounceMs)
    state.timer.unref?.()
  }

  private flushState(state: WatchState): void {
    if (state.timer !== null) {
      this.clearTimer(state.timer)
      state.timer = null
    }
    if (state.pending.size === 0) return
    const changes: PluginWatchChange[] = []
    for (const [relativePath, event] of state.pending) {
      const info =
        event === 'unlink'
          ? null
          : (this.options.stat ?? defaultStat)(
              path.join(state.root, ...relativePath.split('/'))
            )
      changes.push({
        relativePath,
        event,
        ...(info === null ? {} : { size: info.size, mtimeMs: Math.round(info.mtimeMs) })
      })
    }
    state.pending.clear()
    if (changes.length === 0) return
    try {
      this.options.emit(state.pluginId, {
        type: 'files.changed',
        payload: {
          watchId: state.watchId,
          dirId: state.dirId,
          projectId: state.projectId,
          changes
        }
      })
    } catch {
      // A dead renderer or crashed child must not break the watcher.
    }
  }

  private dispose(state: WatchState): void {
    if (state.timer !== null) this.clearTimer(state.timer)
    state.timer = null
    state.pending.clear()
    try {
      state.handle.close()
    } catch {
      // Already closed.
    }
    this.watches.delete(state.watchId)
    const set = this.byPlugin.get(state.pluginId)
    set?.delete(state.watchId)
    if (set !== undefined && set.size === 0) this.byPlugin.delete(state.pluginId)
  }
}

function defaultStat(absolute: string): { isFile: boolean; size: number; mtimeMs: number } | null {
  const info = lstatSync(absolute, { throwIfNoEntry: false })
  if (info === undefined) return null
  return { isFile: info.isFile(), size: info.size, mtimeMs: info.mtimeMs }
}
