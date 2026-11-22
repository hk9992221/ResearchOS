import type { IsoDateTime } from './common'

export type LocaleCode = 'en' | 'zh'
export type ThemeMode = 'system' | 'light' | 'dark'

export interface AppInfo {
  appVersion: string
  electron: string
  node: string
  chrome: string
  platform: string
  locale: LocaleCode
  workspaceRoot: string | null
  schemaVersion: number
}

export interface AppPreferences {
  locale: LocaleCode
  theme: ThemeMode
  weekStartsOn: 0 | 1
  timezone: string
}

export interface AppThemeState {
  mode: ThemeMode
  dark: boolean
}

export interface AppDiagnosticsEntry {
  id: string
  area: 'workspace' | 'database' | 'search' | 'ai' | 'mcp' | 'plugins'
  status: 'ok' | 'degraded' | 'broken'
  message: string
  at: IsoDateTime
}

export interface AppDiagnosticsReport {
  generatedAt: IsoDateTime
  entries: AppDiagnosticsEntry[]
}

export interface RecentProject {
  projectId: string
  name: string
  openedAt: IsoDateTime
}

export interface AppConfig {
  schemaVersion: number
  workspaceRoot: string | null
  zoteroDataDir: string | null
  language: LocaleCode
}

export interface AppConfigPatch {
  workspaceRoot?: string | null
  zoteroDataDir?: string | null
  language?: LocaleCode
}

export interface FileFilter {
  name: string
  extensions: string[]
}

/**
 * Result of exercising the native stack (better-sqlite3 + FTS5 + sqlite-vec +
 * drizzle) in the Electron main process.
 */
export interface DbSmokeResult {
  ok: boolean
  databasePath: string
  fts5Match: string
  vecVersion: string
  vecNearestRowId: number
  vecDistance: number
  drizzleRows: number
  durationMs: number
}