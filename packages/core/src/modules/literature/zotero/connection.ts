/**
 * Zotero connection and data-directory detection (plan §4.3).
 *
 * Platform defaults:
 *   - Windows `%APPDATA%\Zotero\Zotero`
 *   - macOS   `~/Library/Application Support/Zotero`
 *   - Linux   `~/.zotero/zotero`
 * These are *configuration/profile* directories; the data directory is read
 * from `prefs.js` (`extensions.zotero.dataDir`) or defaults to `~/Zotero`.
 * A data directory contains `zotero.sqlite` and `storage/`.
 */
import { existsSync, readFileSync, readdirSync, realpathSync, statSync } from 'node:fs'
import { homedir } from 'node:os'
import * as path from 'node:path'

import type { ZoteroStatus } from '@researchos/shared'

import type { Clock } from '../../../clock'
import { SystemClock, formatTimestamp } from '../../../clock'
import type { ConfigStore } from '../../../config'
import { appError, AppErrorCode } from '../../../errors'
import { LocalApiReader, normalizeUsersBase } from './localapi'
import { createResolver } from './resolve'
import { readZoteroSchemaVersion, SqliteReader } from './sqlite'
import { ZoteroSourceService } from './source'

export const ZOTERO_LOCAL_API_URL = 'http://127.0.0.1:23119'

const HINT_CONFIG_DIR = '这是 Zotero 配置目录，不是数据目录'
const HINT_INSTALL_DIR = '这是安装目录'
const HINT_NO_STORAGE = '缺少 storage 目录，附件路径无法解析'
const HINT_API_DISABLED = '本地 API 未启用'
const HINT_API_ALLOW_APPS = '在 Zotero 设置中允许其他应用通信'
const HINT_ZOTERO_CLOSED = 'Zotero 未运行'

export interface ZoteroConnectionOptions {
  goos?: string
  homeDir?: string
  appDataDir?: string
  configDir?: string
  localApiBaseUrl?: string
  linkedBaseDir?: string
  version?: string
  clock?: Clock
  config?: ConfigStore
  fetch?: (input: string, init?: RequestInit) => Promise<Response>
}

export interface ZoteroDataDirState {
  path: string
  sqlitePath: string
  storageDir: string
  source: 'user' | 'prefs' | 'default'
  schemaVersion: number
  valid: boolean
  problems: string[]
}

export interface ZoteroCandidate {
  path: string
  source: 'user' | 'prefs' | 'default'
  exists: boolean
  valid: boolean
  problems: string[]
}

export interface LocalApiProbe {
  available: boolean
  baseUrl: string
  version: string
  error: string
}

/** Collapses Node's `process.platform` (`win32`) and friendly names (`windows`). */
export function normalizeGoos(value: string): 'windows' | 'darwin' | 'linux' {
  const lower = value.trim().toLowerCase()
  if (lower === 'win32' || lower === 'windows') return 'windows'
  if (lower === 'darwin' || lower === 'macos') return 'darwin'
  return 'linux'
}

function canonical(value: string): string {
  const trimmed = value.trim()
  if (trimmed === '') return ''
  const absolute = path.resolve(trimmed)
  try {
    return realpathSync(absolute)
  } catch {
    return path.normalize(absolute)
  }
}

function isDir(target: string): boolean {
  try {
    return statSync(target).isDirectory()
  } catch {
    return false
  }
}

function isFile(target: string): boolean {
  try {
    return statSync(target).isFile()
  } catch {
    return false
  }
}

interface PrefFile {
  path: string
  data: Record<string, unknown>
}

const PREF_LINE = /^\s*(?:user_)?pref\s*\(\s*"([^"]+)"\s*,\s*(.+?)\s*\)\s*;/gm

function readPrefs(file: string): PrefFile | null {
  let body: string
  try {
    body = readFileSync(file, 'utf8').replace(/^\uFEFF/, '')
  } catch {
    return null
  }
  const data: Record<string, unknown> = {}
  for (const match of body.matchAll(PREF_LINE)) {
    const name = match[1]
    const raw = match[2]
    if (name === undefined || raw === undefined) continue
    try {
      data[name] = JSON.parse(raw) as unknown
    } catch {
      // prefs.js is JavaScript; only JSON literals are understood.
    }
  }
  return { path: file, data }
}

function prefsFiles(configDir: string, goos: string): string[] {
  if (configDir.trim() === '') return []
  const pattern =
    goos === 'windows' || goos === 'darwin'
      ? path.join(configDir, 'Profiles')
      : configDir
  if (!isDir(pattern)) return []
  const out: string[] = []
  try {
    const entries = readdirSync(pattern, { withFileTypes: true })
    for (const entry of entries) {
      if (!entry.isDirectory()) continue
      const candidate = path.join(pattern, entry.name, 'prefs.js')
      if (isFile(candidate)) out.push(candidate)
    }
  } catch {
    return []
  }
  return out.sort()
}

export class ZoteroConnection {
  private readonly goos: string
  private readonly home: string
  private readonly appData: string
  private readonly config: string
  private readonly apiBase: string
  private readonly version: string
  private readonly linkedBase: string
  private readonly clock: Clock
  private readonly configStore: ConfigStore | undefined
  private readonly fetchImpl: (input: string, init?: RequestInit) => Promise<Response>

  constructor(options: ZoteroConnectionOptions = {}) {
    this.goos = normalizeGoos(options.goos ?? process.platform)
    this.home = (options.homeDir ?? homedir()).trim()
    this.appData = (options.appDataDir ?? process.env['APPDATA'] ?? '').trim()
    this.version = (options.version ?? '').trim()
    this.linkedBase = (options.linkedBaseDir ?? '').trim()
    this.clock = options.clock ?? new SystemClock()
    this.configStore = options.config
    this.fetchImpl = options.fetch ?? ((input, init) => fetch(input, init))
    this.apiBase = normalizeUsersBase(options.localApiBaseUrl ?? ZOTERO_LOCAL_API_URL)
    this.config = this.resolveConfigDir(options.configDir)
  }

  private resolveConfigDir(override: string | undefined): string {
    const explicit = (override ?? '').trim()
    if (explicit !== '') return canonical(explicit)
    if (this.goos === 'windows') {
      return this.appData === '' ? '' : path.join(this.appData, 'Zotero', 'Zotero')
    }
    if (this.goos === 'darwin') {
      return this.home === ''
        ? ''
        : path.join(this.home, 'Library', 'Application Support', 'Zotero')
    }
    return this.home === '' ? '' : path.join(this.home, '.zotero', 'zotero')
  }

  profileDir(): string | null {
    return this.config === '' ? null : this.config
  }

  localApiBaseUrl(): string {
    return this.apiBase
  }

  /** The manually configured directory, when the app config is wired. */
  manualDataDir(): string {
    if (this.configStore === undefined) return ''
    return this.configStore.load().zotero_data_dir.trim()
  }

  setDataDir(dir: string): void {
    if (this.configStore === undefined) {
      throw appError(AppErrorCode.Internal, '应用配置未初始化，无法保存 Zotero 数据目录')
    }
    const config = this.configStore.load()
    config.zotero_data_dir = dir.trim()
    this.configStore.save(config)
  }

  validate(dir: string): ZoteroDataDirState {
    const problems: string[] = []
    const raw = dir.trim()
    if (raw === '') {
      return {
        path: '',
        sqlitePath: '',
        storageDir: '',
        source: 'user',
        schemaVersion: 0,
        valid: false,
        problems: ['路径为空']
      }
    }
    const resolved = canonical(raw)
    const sqlitePath = path.join(resolved, 'zotero.sqlite')
    const storageDir = path.join(resolved, 'storage')
    const state: ZoteroDataDirState = {
      path: resolved,
      sqlitePath,
      storageDir,
      source: 'user',
      schemaVersion: 0,
      valid: false,
      problems
    }
    if (!isDir(resolved)) {
      problems.push(existsSync(resolved) ? '路径不是目录' : '目录不存在')
      return state
    }
    const sqliteOk = isFile(sqlitePath)
    if (!sqliteOk) {
      problems.push('缺少 zotero.sqlite')
      if (isFile(path.join(resolved, 'prefs.js')) || isFile(path.join(resolved, 'profiles.ini'))) {
        problems.push(HINT_CONFIG_DIR)
      }
    } else {
      try {
        const info = statSync(sqlitePath)
        if (info.size === 0) problems.push('zotero.sqlite 为空')
      } catch {
        problems.push('zotero.sqlite 不可读')
      }
      const version = readZoteroSchemaVersion(sqlitePath)
      if (version.known) state.schemaVersion = version.version
      else problems.push('无法识别 Zotero 数据库 schema 版本')
    }
    if (!isDir(storageDir)) problems.push(HINT_NO_STORAGE)
    const install =
      isFile(path.join(resolved, 'zotero.exe')) ||
      isFile(path.join(resolved, 'Zotero.app')) ||
      isFile(path.join(resolved, 'zotero'))
    if (install) problems.push(HINT_INSTALL_DIR)
    state.valid = sqliteOk && !install
    return state
  }

  candidates(): ZoteroCandidate[] {
    const ordered: Array<{ path: string; source: 'user' | 'prefs' | 'default' }> = []
    const manual = this.manualDataDir()
    if (manual !== '') ordered.push({ path: manual, source: 'user' })
    for (const file of prefsFiles(this.config, this.goos)) {
      const prefs = readPrefs(file)
      if (prefs === null) continue
      if (prefs.data['extensions.zotero.useDataDir'] === false) continue
      const dir = prefs.data['extensions.zotero.dataDir']
      if (typeof dir === 'string' && dir.trim() !== '') {
        ordered.push({ path: dir, source: 'prefs' })
      }
    }
    if (this.home !== '') ordered.push({ path: path.join(this.home, 'Zotero'), source: 'default' })

    const seen = new Set<string>()
    const out: ZoteroCandidate[] = []
    for (const candidate of ordered) {
      const resolved = canonical(candidate.path)
      if (resolved === '') continue
      const key = this.goos === 'windows' ? resolved.toLowerCase() : resolved
      if (seen.has(key)) continue
      seen.add(key)
      const state = this.validate(resolved)
      out.push({
        path: resolved,
        source: candidate.source,
        exists: isDir(resolved),
        valid: state.valid,
        problems: state.problems
      })
    }
    return out
  }

  /** Manual directory wins (even when invalid), then the first valid candidate. */
  effectiveDataDir(): ZoteroDataDirState | null {
    const manual = this.manualDataDir()
    if (manual !== '') {
      const state = this.validate(manual)
      state.source = 'user'
      return state
    }
    for (const candidate of this.candidates()) {
      if (!candidate.valid) continue
      const state = this.validate(candidate.path)
      state.source = candidate.source
      return state
    }
    return null
  }

  linkedAttachmentBaseDir(): string {
    if (this.linkedBase !== '') return canonical(this.linkedBase)
    for (const file of prefsFiles(this.config, this.goos)) {
      const prefs = readPrefs(file)
      const value = prefs?.data['extensions.zotero.baseAttachmentPath']
      if (typeof value === 'string' && value.trim() !== '') return canonical(value)
    }
    return ''
  }

  async probeLocalApi(): Promise<LocalApiProbe> {
    const probe: LocalApiProbe = {
      available: false,
      baseUrl: this.apiBase,
      version: '',
      error: ''
    }
    let response: Response
    try {
      response = await this.fetchImpl(`${this.apiBase}/collections?limit=1`, {
        method: 'GET',
        headers: { Accept: 'application/json' },
        redirect: 'manual',
        signal: AbortSignal.timeout(1000)
      })
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : String(cause)
      probe.error =
        message.toLowerCase().includes('timeout') || message.toLowerCase().includes('aborted')
          ? `本地 API 探测超时（${HINT_ZOTERO_CLOSED}）`
          : `${HINT_ZOTERO_CLOSED}：${message}`
      return probe
    }
    if (response.status === 200) {
      probe.available = true
      probe.version =
        response.headers.get('zotero-version') ?? response.headers.get('x-zotero-version') ?? this.version
      return probe
    }
    if (response.status === 403) {
      probe.error = `${HINT_API_DISABLED}：${HINT_API_ALLOW_APPS}`
      return probe
    }
    probe.error = `本地 API 返回 HTTP ${response.status}`
    return probe
  }

  async status(): Promise<ZoteroStatus> {
    const probe = await this.probeLocalApi()
    const dataDir = this.effectiveDataDir()
    const diagnostics: string[] = []
    if (dataDir !== null) diagnostics.push(...dataDir.problems)
    if (probe.error !== '') diagnostics.push(probe.error)
    const sqliteExists = dataDir !== null && isFile(dataDir.sqlitePath)
    let mode: ZoteroStatus['mode'] = 'unavailable'
    if (probe.available) mode = 'local_api'
    else if (sqliteExists) mode = 'sqlite'
    return {
      available: probe.available || sqliteExists,
      mode,
      dataDir: dataDir?.path ?? null,
      localApiUrl: this.apiBase,
      profileDir: this.profileDir(),
      databasePath: dataDir?.sqlitePath ?? null,
      storageDir: dataDir?.storageDir ?? null,
      lastError: probe.error === '' ? null : probe.error,
      diagnostics
    }
  }

  createReader(): LocalApiReader | null {
    const dataDir = this.effectiveDataDir()
    if (dataDir === null) return null
    return new LocalApiReader({
      baseUrl: this.apiBase,
      fetch: this.fetchImpl,
      resolver: createResolver({
        dataDir: dataDir.path,
        linkedBaseDir: this.linkedAttachmentBaseDir()
      })
    })
  }

  createSqliteReader(): SqliteReader | null {
    const dataDir = this.effectiveDataDir()
    if (dataDir === null || dataDir.path === '') return null
    try {
      return new SqliteReader({
        dataDir: dataDir.path,
        linkedBaseDir: this.linkedAttachmentBaseDir()
      })
    } catch {
      return null
    }
  }

  async createSource(): Promise<ZoteroSourceService> {
    return new ZoteroSourceService({
      connection: this,
      clock: this.clock,
      fetch: this.fetchImpl
    })
  }

  /** Diagnostics text for the settings page; stable enough to test. */
  statusLine(status: ZoteroStatus): string {
    if (status.available) return status.mode === 'local_api' ? 'Zotero 本地 API' : 'Zotero SQLite'
    return status.lastError ?? 'Zotero 不可用'
  }
}

/** Exported for tests and the settings page's diagnostics list. */
export const ZOTERO_DIAGNOSTIC_HINTS = {
  configDir: HINT_CONFIG_DIR,
  installDir: HINT_INSTALL_DIR,
  noStorage: HINT_NO_STORAGE,
  apiDisabled: HINT_API_DISABLED,
  allowApps: HINT_API_ALLOW_APPS,
  closed: HINT_ZOTERO_CLOSED
} as const

export function nowStamp(clock: Clock): string {
  return formatTimestamp(clock.now())
}