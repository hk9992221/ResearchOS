import type { IsoDateTime, JsonObject } from '../types/common'
import type {
  AppConfig,
  AppConfigPatch,
  AppDiagnosticsReport,
  AppInfo,
  AppPreferences,
  AppThemeState,
  DbSmokeResult,
  FileFilter,
  RecentProject
} from '../types/app'

export interface OpenPathResult {
  canceled: boolean
  paths: string[]
}

export interface SavePathResult {
  canceled: boolean
  path?: string
}

export interface AppCommands {
  'app.info': { req: void; res: AppInfo }

  'app.preferences.get': { req: void; res: AppPreferences }
  'app.preferences.set': { req: { patch: Partial<AppPreferences> }; res: AppPreferences }

  'app.theme.get': { req: void; res: AppThemeState }
  'app.theme.set': { req: { mode: AppThemeState['mode'] }; res: AppThemeState }

  'app.config.get': { req: void; res: AppConfig }
  'app.config.update': { req: { patch: AppConfigPatch }; res: AppConfig }

  'app.diagnostics': { req: void; res: AppDiagnosticsReport }

  /** Native stack self-check surfaced in the renderer (better-sqlite3 + FTS5 + sqlite-vec + drizzle). */
  'app.nativeSmoke': { req: void; res: DbSmokeResult }

  'app.pickFolder': {
    req: { title?: string; defaultPath?: string; multi?: boolean }
    res: OpenPathResult
  }
  'app.pickFile': {
    req: { title?: string; defaultPath?: string; filters?: FileFilter[]; multi?: boolean }
    res: OpenPathResult
  }
  'app.saveFile': {
    req: { title?: string; defaultPath?: string; filters?: FileFilter[] }
    res: SavePathResult
  }
  /** Pick a destination and write the given text atomically. */
  'app.saveTextFile': {
    req: { title?: string; defaultPath?: string; filters?: FileFilter[]; content: string }
    res: SavePathResult
  }

  'app.recentProjects': { req: void; res: RecentProject[] }
  'app.flush': { req: { reason: 'close' | 'switch-workspace' }; res: { flushed: boolean; at: IsoDateTime } }

  'app.log': { req: { level: 'info' | 'warn' | 'error'; message: string; fields?: JsonObject }; res: void }

  /** Reveal a file/folder in the OS file manager (Go reveal-in-folder parity). */
  'app.revealPath': { req: { path: string }; res: void }
}