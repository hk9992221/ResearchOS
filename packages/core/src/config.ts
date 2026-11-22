import {
  closeSync,
  fsyncSync,
  mkdirSync,
  openSync,
  readFileSync,
  renameSync,
  writeFileSync
} from 'node:fs'
import { homedir } from 'node:os'
import * as path from 'node:path'

import { AppError, AppErrorCode, appError } from './errors'

/** File name inside the application config directory. */
export const CONFIG_FILE = 'config.json'

export interface AppConfig {
  schema_version: number
  workspace_root: string
  zotero_data_dir: string
  /** `''`/`system` follows the OS; otherwise a bundled pack tag (`en`, `zh`). */
  language: string
}

export const EMPTY_CONFIG: AppConfig = {
  schema_version: 0,
  workspace_root: '',
  zotero_data_dir: '',
  language: ''
}

/** Per-platform directory name: Linux keeps the conventional lowercase name. */
export function appConfigDirName(): string {
  return process.platform === 'linux' ? 'researchos' : 'ResearchOS'
}

/** `%AppData%\ResearchOS`, `~/Library/Application Support/ResearchOS`, `$XDG_CONFIG_HOME/researchos`. */
export function defaultConfigDir(): string {
  const base =
    process.platform === 'win32'
      ? (process.env.APPDATA ?? path.join(homedir(), 'AppData', 'Roaming'))
      : process.platform === 'darwin'
        ? path.join(homedir(), 'Library', 'Application Support')
        : (process.env.XDG_CONFIG_HOME ?? path.join(homedir(), '.config'))
  return path.join(base, appConfigDirName())
}

export function defaultConfigPath(): string {
  return path.join(defaultConfigDir(), CONFIG_FILE)
}

/**
 * Reads and writes the application configuration file.
 *
 * - A missing file is the zero value with no error.
 * - Invalid JSON is quarantined to `config.json.corrupt-<stamp>` and reported
 *   as `CONFIG_CORRUPT`, so the next save can start clean.
 * - Saves are atomic (temp file + fsync + rename) and mode 0600.
 */
export class ConfigStore {
  readonly filePath: string

  constructor(filePath: string = defaultConfigPath()) {
    if (filePath.trim() === '') throw appError(AppErrorCode.ConfigCorrupt, 'config: empty config path')
    this.filePath = path.resolve(filePath)
  }

  path(): string {
    return this.filePath
  }

  load(): AppConfig {
    let body: string
    try {
      body = readFileSync(this.filePath, 'utf8')
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return { ...EMPTY_CONFIG }
      throw appError(AppErrorCode.ConfigCorrupt, `config: read ${this.filePath}: ${String(error)}`, {
        cause: error
      })
    }
    let parsed: unknown
    try {
      parsed = JSON.parse(body)
    } catch (error) {
      const quarantined = this.quarantine()
      throw appError(
        AppErrorCode.ConfigCorrupt,
        `config: ${this.filePath} is not valid JSON (moved to ${quarantined})`,
        { cause: error }
      )
    }
    return normalizeConfig(parsed)
  }

  save(config: AppConfig): void {
    const next: AppConfig = {
      schema_version: config.schema_version === 0 ? 1 : config.schema_version,
      workspace_root: config.workspace_root,
      zotero_data_dir: config.zotero_data_dir,
      language: config.language
    }
    const body = JSON.stringify({ ...next, language: next.language === '' ? undefined : next.language })
    const dir = path.dirname(this.filePath)
    mkdirSync(dir, { recursive: true, mode: 0o755 })
    const tmp = path.join(dir, `.config-${process.pid}-${Date.now()}.tmp`)
    try {
      writeFileSync(tmp, body, { mode: 0o600 })
      const fd = openSync(tmp, 'r+')
      try {
        fsyncSync(fd)
      } finally {
        closeSync(fd)
      }
      renameSync(tmp, this.filePath)
    } catch (error) {
      throw appError(AppErrorCode.ConfigCorrupt, `config: save ${this.filePath}: ${String(error)}`, {
        cause: error
      })
    }
    try {
      const dirFd = openSync(dir, 'r')
      try {
        fsyncSync(dirFd)
      } finally {
        closeSync(dirFd)
      }
    } catch {
      // Directory fsync is unsupported on Windows; the rename is still atomic.
    }
  }

  private quarantine(): string {
    const iso = new Date().toISOString()
    const stamp =
      `${iso.slice(0, 4)}${iso.slice(5, 7)}${iso.slice(8, 10)}` +
      `-${iso.slice(11, 13)}${iso.slice(14, 16)}${iso.slice(17, 19)}.${iso.slice(20, 23)}`
    const backup = `${this.filePath}.corrupt-${stamp}`
    try {
      renameSync(this.filePath, backup)
      return backup
    } catch {
      return this.filePath
    }
  }
}

function normalizeConfig(value: unknown): AppConfig {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw appError(AppErrorCode.ConfigCorrupt, 'config: root is not a JSON object')
  }
  const record = value as Record<string, unknown>
  const schemaVersion = record.schema_version
  if (schemaVersion !== undefined && (typeof schemaVersion !== 'number' || !Number.isInteger(schemaVersion))) {
    throw appError(AppErrorCode.ConfigCorrupt, 'config: schema_version is not an integer')
  }
  const config: AppConfig = {
    schema_version: typeof schemaVersion === 'number' ? schemaVersion : 0,
    workspace_root: typeof record.workspace_root === 'string' ? record.workspace_root : '',
    zotero_data_dir: typeof record.zotero_data_dir === 'string' ? record.zotero_data_dir : '',
    language: typeof record.language === 'string' ? record.language : ''
  }
  return config
}