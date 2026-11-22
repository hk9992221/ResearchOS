import * as path from 'node:path'

import { watch, type FSWatcher } from 'chokidar'

import { MANIFEST_FILE } from './manifest'

/**
 * Hot reload (plan §3.7): chokidar watches the manifest and entry scripts;
 * changes are debounced for 300 ms, then the plugin's runtime is rebuilt and
 * the renderer is notified. A burst of editor writes produces one reload.
 *
 * It lives in core because `chokidar` is a core dependency; the desktop host
 * consumes it through the public plugin module.
 */

export const HOT_RELOAD_DEBOUNCE_MS = 300

export interface Debouncer {
  schedule(): void
  cancel(): void
}

export function createDebouncer(action: () => void, delayMs = HOT_RELOAD_DEBOUNCE_MS): Debouncer {
  let timer: ReturnType<typeof setTimeout> | undefined
  return {
    schedule() {
      if (timer !== undefined) clearTimeout(timer)
      timer = setTimeout(() => {
        timer = undefined
        action()
      }, delayMs)
    },
    cancel() {
      if (timer !== undefined) clearTimeout(timer)
      timer = undefined
    }
  }
}

export interface HotReloaderOptions {
  debounceMs?: number
  onReload(pluginId: string): void | Promise<void>
  onError?(pluginId: string, error: unknown): void
}

interface WatchEntry {
  watcher: FSWatcher
  debouncer: Debouncer
}

export class PluginHotReloader {
  private readonly options: HotReloaderOptions
  private readonly entries = new Map<string, WatchEntry>()

  constructor(options: HotReloaderOptions) {
    this.options = options
  }

  watchPlugin(pluginId: string, pluginDir: string, manifest: { main: string; renderer?: string }): void {
    this.unwatch(pluginId)
    const files = new Set([
      path.join(pluginDir, MANIFEST_FILE),
      path.join(pluginDir, ...manifest.main.split('/'))
    ])
    if (manifest.renderer !== undefined) {
      files.add(path.join(pluginDir, ...manifest.renderer.split('/')))
    }
    const debouncer = createDebouncer(() => {
      void Promise.resolve()
        .then(() => this.options.onReload(pluginId))
        .catch((error: unknown) => {
          this.options.onError?.(pluginId, error)
        })
    }, this.options.debounceMs ?? HOT_RELOAD_DEBOUNCE_MS)
    const watcher = watch([...files], {
      ignoreInitial: true,
      awaitWriteFinish: { stabilityThreshold: 50, pollInterval: 10 }
    })
    watcher.on('all', () => debouncer.schedule())
    this.entries.set(pluginId, { watcher, debouncer })
  }

  unwatch(pluginId: string): void {
    const entry = this.entries.get(pluginId)
    if (entry === undefined) return
    this.entries.delete(pluginId)
    entry.debouncer.cancel()
    void entry.watcher.close()
  }

  close(): void {
    for (const pluginId of [...this.entries.keys()]) this.unwatch(pluginId)
  }

  watching(pluginId: string): boolean {
    return this.entries.has(pluginId)
  }
}