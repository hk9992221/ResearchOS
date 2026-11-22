/**
 * Desktop hot-reload surface. The watcher itself lives in core because
 * `chokidar` is a core dependency; this module is the stable import path for
 * the desktop runtime.
 */
export {
  createDebouncer,
  HOT_RELOAD_DEBOUNCE_MS,
  PluginHotReloader,
  type Debouncer,
  type HotReloaderOptions
} from '@researchos/core/modules/plugins/watcher'