import { existsSync, statSync, writeFileSync } from 'node:fs'

import { app, dialog, nativeTheme, shell, type BrowserWindow, type FileFilter as ElectronFileFilter, type OpenDialogOptions, type SaveDialogOptions } from 'electron'

import type {
  AppInfo,
  AppThemeState,
  CommandValidator,
  DbSmokeResult,
  FileFilter,
  OpenPathResult,
  SavePathResult,
  ThemeMode
} from '@researchos/shared'

import type { FlushCoordinator, FlushReason } from '../../flush'
import { noPayloadValidator, type CommandRouter } from '../router'

export interface AppHandlerDeps {
  getMainWindow: () => BrowserWindow | null
  flush: FlushCoordinator
  runNativeSmoke: () => DbSmokeResult
  /** Composition-root flush: note editor buffers and pending file writes. */
  flushAll?: () => Promise<void>
}

interface PickFolderRequest {
  title?: string
  defaultPath?: string
  multi?: boolean
}

interface PickFileRequest {
  title?: string
  defaultPath?: string
  filters?: FileFilter[]
  multi?: boolean
}

interface SaveFileRequest {
  title?: string
  defaultPath?: string
  filters?: FileFilter[]
}

function optionalString(value: unknown, field: string): string | undefined {
  if (value === undefined) return undefined
  if (typeof value !== 'string') throw new Error(`${field} must be a string`)
  return value
}

function optionalBoolean(value: unknown, field: string): boolean | undefined {
  if (value === undefined) return undefined
  if (typeof value !== 'boolean') throw new Error(`${field} must be a boolean`)
  return value
}

function optionalFilters(value: unknown): FileFilter[] | undefined {
  if (value === undefined) return undefined
  if (!Array.isArray(value)) throw new Error('filters must be an array')

  return value.map((entry: unknown) => {
    if (typeof entry !== 'object' || entry === null) throw new Error('filter must be an object')
    if (!('name' in entry) || typeof entry.name !== 'string') throw new Error('filter.name must be a string')
    if (!('extensions' in entry) || !Array.isArray(entry.extensions)) {
      throw new Error('filter.extensions must be an array')
    }
    if (!entry.extensions.every((extension: unknown) => typeof extension === 'string')) {
      throw new Error('filter.extensions must contain strings')
    }
    return { name: entry.name, extensions: entry.extensions as string[] }
  })
}

function asElectronFilters(filters: FileFilter[] | undefined): ElectronFileFilter[] | undefined {
  return filters?.map((filter) => ({ name: filter.name, extensions: filter.extensions }))
}

const pickFolderValidator: CommandValidator<PickFolderRequest> = {
  parse: (input) => {
    if (typeof input !== 'object' || input === null) throw new Error('payload must be an object')
    const title = 'title' in input ? optionalString(input.title, 'title') : undefined
    const defaultPath = 'defaultPath' in input ? optionalString(input.defaultPath, 'defaultPath') : undefined
    const multi = 'multi' in input ? optionalBoolean(input.multi, 'multi') : undefined
    return { title, defaultPath, multi }
  }
}

const pickFileValidator: CommandValidator<PickFileRequest> = {
  parse: (input) => {
    if (typeof input !== 'object' || input === null) throw new Error('payload must be an object')
    const title = 'title' in input ? optionalString(input.title, 'title') : undefined
    const defaultPath = 'defaultPath' in input ? optionalString(input.defaultPath, 'defaultPath') : undefined
    const filters = 'filters' in input ? optionalFilters(input.filters) : undefined
    const multi = 'multi' in input ? optionalBoolean(input.multi, 'multi') : undefined
    return { title, defaultPath, filters, multi }
  }
}

const saveFileValidator: CommandValidator<SaveFileRequest> = {
  parse: (input) => {
    if (typeof input !== 'object' || input === null) throw new Error('payload must be an object')
    const title = 'title' in input ? optionalString(input.title, 'title') : undefined
    const defaultPath = 'defaultPath' in input ? optionalString(input.defaultPath, 'defaultPath') : undefined
    const filters = 'filters' in input ? optionalFilters(input.filters) : undefined
    return { title, defaultPath, filters }
  }
}

const themeSetValidator: CommandValidator<{ mode: ThemeMode }> = {
  parse: (input) => {
    if (typeof input !== 'object' || input === null || !('mode' in input)) throw new Error('mode is required')
    const mode = input.mode
    if (mode !== 'system' && mode !== 'light' && mode !== 'dark') throw new Error('mode must be system|light|dark')
    return { mode }
  }
}

const flushValidator: CommandValidator<{ reason: FlushReason }> = {
  parse: (input) => {
    if (typeof input !== 'object' || input === null || !('reason' in input)) throw new Error('reason is required')
    const reason = input.reason
    if (reason !== 'close' && reason !== 'switch-workspace') throw new Error('reason must be close|switch-workspace')
    return { reason }
  }
}

const saveTextValidator: CommandValidator<{
  title?: string
  defaultPath?: string
  filters?: FileFilter[]
  content: string
}> = {
  parse: (input) => {
    if (typeof input !== 'object' || input === null) throw new Error('payload must be an object')
    const title = 'title' in input ? optionalString(input.title, 'title') : undefined
    const defaultPath = 'defaultPath' in input ? optionalString(input.defaultPath, 'defaultPath') : undefined
    const filters = 'filters' in input ? optionalFilters(input.filters) : undefined
    if (!('content' in input) || typeof input.content !== 'string') throw new Error('content must be a string')
    return { title, defaultPath, filters, content: input.content }
  }
}

const revealValidator: CommandValidator<{ path: string }> = {
  parse: (input) => {
    if (typeof input !== 'object' || input === null || !('path' in input)) throw new Error('path is required')
    if (typeof input.path !== 'string' || input.path.trim() === '') throw new Error('path must be a non-empty string')
    return { path: input.path }
  }
}

const logValidator: CommandValidator<{ level: 'info' | 'warn' | 'error'; message: string; fields?: Record<string, unknown> }> =
  {
    parse: (input) => {
      if (typeof input !== 'object' || input === null) throw new Error('payload must be an object')
      if (!('level' in input) || (input.level !== 'info' && input.level !== 'warn' && input.level !== 'error')) {
        throw new Error('level must be info|warn|error')
      }
      if (!('message' in input) || typeof input.message !== 'string') throw new Error('message must be a string')
      const fields =
        'fields' in input && input.fields !== undefined
          ? optionalObject(input.fields)
          : undefined
      return { level: input.level, message: input.message, fields }
    }
  }

function optionalObject(value: unknown): Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error('fields must be an object')
  }
  return value as Record<string, unknown>
}

export function registerAppHandlers(router: CommandRouter, deps: AppHandlerDeps): void {
  router.registerHandler('app.info', noPayloadValidator, (): AppInfo => {
    return {
      appVersion: app.getVersion(),
      electron: process.versions.electron,
      node: process.versions.node,
      chrome: process.versions.chrome,
      platform: process.platform,
      locale: 'en',
      workspaceRoot: null,
      schemaVersion: 1
    }
  })

  router.registerHandler('app.nativeSmoke', noPayloadValidator, (): DbSmokeResult => deps.runNativeSmoke())

  router.registerHandler('app.flush', flushValidator, async (request) => {
    await deps.flushAll?.()
    const acknowledged = deps.flush.acknowledge()
    return { flushed: acknowledged, at: new Date().toISOString() }
  })

  router.registerHandler('app.theme.get', noPayloadValidator, (): AppThemeState => {
    return { mode: nativeTheme.themeSource, dark: nativeTheme.shouldUseDarkColors }
  })

  router.registerHandler('app.theme.set', themeSetValidator, (request) => {
    nativeTheme.themeSource = request.mode
    return { mode: nativeTheme.themeSource, dark: nativeTheme.shouldUseDarkColors }
  })

  router.registerHandler('app.log', logValidator, (request) => {
    const fields = request.fields ?? {}
    if (request.level === 'error') console.error(`[renderer] ${request.message}`, fields)
    else if (request.level === 'warn') console.warn(`[renderer] ${request.message}`, fields)
    else console.info(`[renderer] ${request.message}`, fields)
  })

  router.registerHandler('app.revealPath', revealValidator, async (request) => {
    const isDirectory = existsSync(request.path) && statSync(request.path).isDirectory()
    if (isDirectory) {
      const error = await shell.openPath(request.path)
      if (error !== '') throw new Error(error)
      return
    }
    shell.showItemInFolder(request.path)
  })

  router.registerHandler('app.saveTextFile', saveTextValidator, async (request): Promise<SavePathResult> => {
    const options: SaveDialogOptions = {
      title: request.title,
      defaultPath: request.defaultPath,
      filters: asElectronFilters(request.filters)
    }
    const window = deps.getMainWindow()
    const result = window === null ? await dialog.showSaveDialog(options) : await dialog.showSaveDialog(window, options)
    if (result.canceled || result.filePath === undefined || result.filePath === '') {
      return { canceled: true }
    }
    const temporary = `${result.filePath}.tmp-${process.pid}`
    writeFileSync(temporary, request.content, 'utf8')
    try {
      const { renameSync } = await import('node:fs')
      renameSync(temporary, result.filePath)
    } catch (error) {
      const { rmSync } = await import('node:fs')
      rmSync(temporary, { force: true })
      throw error
    }
    return { canceled: false, path: result.filePath }
  })

  router.registerHandler('app.pickFolder', pickFolderValidator, async (request): Promise<OpenPathResult> => {
    const properties: OpenDialogOptions['properties'] = ['openDirectory']
    if (request.multi === true) properties.push('multiSelections')

    const options: OpenDialogOptions = {
      title: request.title,
      defaultPath: request.defaultPath,
      properties
    }
    const window = deps.getMainWindow()
    const result = window === null ? await dialog.showOpenDialog(options) : await dialog.showOpenDialog(window, options)
    return { canceled: result.canceled, paths: result.filePaths }
  })

  router.registerHandler('app.pickFile', pickFileValidator, async (request): Promise<OpenPathResult> => {
    const properties: OpenDialogOptions['properties'] = ['openFile']
    if (request.multi === true) properties.push('multiSelections')

    const options: OpenDialogOptions = {
      title: request.title,
      defaultPath: request.defaultPath,
      filters: asElectronFilters(request.filters),
      properties
    }
    const window = deps.getMainWindow()
    const result = window === null ? await dialog.showOpenDialog(options) : await dialog.showOpenDialog(window, options)
    return { canceled: result.canceled, paths: result.filePaths }
  })

  router.registerHandler('app.saveFile', saveFileValidator, async (request): Promise<SavePathResult> => {
    const options: SaveDialogOptions = {
      title: request.title,
      defaultPath: request.defaultPath,
      filters: asElectronFilters(request.filters)
    }
    const window = deps.getMainWindow()
    const result = window === null ? await dialog.showSaveDialog(options) : await dialog.showSaveDialog(window, options)
    if (result.filePath === undefined || result.filePath === '') return { canceled: result.canceled }
    return { canceled: result.canceled, path: result.filePath }
  })
}