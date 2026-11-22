import { existsSync, lstatSync, mkdirSync, realpathSync, renameSync, rmSync } from 'node:fs'
import * as path from 'node:path'

import type {
  PluginCommandView,
  PluginContributions,
  PluginInfo,
  PluginManifest,
  PluginPermission,
  PluginRpcMethod
} from '@researchos/shared'
import { isPluginWritePermission } from '@researchos/shared'

import { type Clock, SystemClock } from '../../clock'
import { ConfirmService } from '../../confirm'
import { AppErrorCode, appError } from '../../errors'
import type { Logger } from '../../logger'
import type { Paths } from '../../paths'
import type { GlobalHandle } from '../../workspace/handle'
import { PluginCommandRegistry } from './commands'
import type { DeclarativeImportResult, PluginRuleSink, PluginSkillSink } from './declarative'
import { importDeclarative } from './declarative'
import { invalidManifest, pluginConflict, permissionDenied } from './errors'
import {
  MANIFEST_FILE,
  MAX_MAIN_BYTES,
  normalizedContributions,
  parseManifestDocument,
  validateManifestSemantics
} from './manifest'
import {
  missingPermissions,
  newlyGrantedWriteScopes,
  normalizeGranted,
  normalizeNetworkDomains,
  scopesSatisfied
} from './permissions'
import type { PluginAuditEntry, PluginFetch, PluginRpcHandler } from './rpc'
import { PluginRpcRouter } from './rpc'
import {
  isInside,
  materializePluginSource,
  readRegularFile,
  requirePluginFile
} from './source'
import { PluginStorageStore } from './storage'
import type { PluginRecord, PluginSourceInfo } from './store'
import { PluginStore, recordFromManifest, toPluginInfo } from './store'
import { PluginBlobStore } from './blobs'
import {
  PluginFilesService,
  type PluginHostOperations,
  type PluginWatchGateway
} from './files'
import { PluginPromptBroker, type PluginPromptPresenter } from './prompt'
import type { PluginAiHost } from './rpc'

export interface PluginRuntimeStart {
  pluginId: string
  pluginDir: string
  mainPath: string
  rendererPath: string | null
  version: string
  permissions: readonly PluginPermission[]
  networkDomains: readonly string[]
}

/** The utility-process host (desktop) or a test double. */
export interface PluginRuntime {
  start(plugin: PluginRuntimeStart): Promise<void>
  stop(pluginId: string, reason?: string): Promise<void>
  isRunning(pluginId: string): boolean
  /** Host → plugin event push; absent in test doubles without events. */
  emitEvent?(pluginId: string, event: { type: string; payload?: unknown }): boolean
}

export interface PluginWorkspace {
  root: string
  paths: Paths
  global: GlobalHandle
}

export interface PluginServiceOptions {
  workspace: PluginWorkspace
  runtime: PluginRuntime
  clock?: Clock
  logger?: Logger
  confirm?: ConfirmService
  skills?: PluginSkillSink
  rules?: PluginRuleSink
  rpcHandlers?: Partial<Record<PluginRpcMethod, PluginRpcHandler>>
  fetchImpl?: PluginFetch
  clipboard?: { write(text: string): void | Promise<void> }
  notifications?: { show(title: string, body: string): void | Promise<void> }
  editor?: { insertText(text: string): void | Promise<void> }
  audit?: (entry: PluginAuditEntry) => void
  /** Renderer notification (`plugins.changed`); never throws into the service. */
  onChange?: (info?: PluginInfo) => void
  /** Host capabilities; desktop supplies the native halves. */
  files?: PluginFilesService
  blobs?: PluginBlobStore
  watch?: PluginWatchGateway
  prompts?: PluginPromptBroker
  promptPresenter?: PluginPromptPresenter
  pluginName?: (pluginId: string) => string
  host?: PluginHostOperations
  ai?: PluginAiHost
}

export interface EnableOptions {
  /** Full grant set approved by the permission prompt. */
  permissions?: readonly PluginPermission[]
  networkDomains?: readonly string[]
  confirmationToken?: string
  /** Set only by the IPC layer after the renderer's write-scope dialog. */
  confirmWriteScopes?: boolean
}

export interface InstallOptions extends EnableOptions {
  enable?: boolean
  /** Provenance of a market or GitHub install. */
  source?: PluginSourceInfo
}

/** An update keeps its provenance; only the grant set can change with it. */
export type UpdateOptions = EnableOptions

interface LoadedPlugin {
  manifest: PluginManifest
  contributions: PluginContributions
}

/**
 * Owns the plugin registry and lifecycle (plan §3.7). It is independent of
 * Electron: the desktop utility-process host implements `PluginRuntime`, and
 * weekly/experiments contributions arrive through narrow sinks.
 */
export class PluginService {
  private readonly workspace: PluginWorkspace
  private readonly store: PluginStore
  private readonly runtime: PluginRuntime
  private readonly clock: Clock
  private readonly logger: Logger | undefined
  private readonly confirm: ConfirmService
  private readonly commands = new PluginCommandRegistry()
  private readonly rpc: PluginRpcRouter
  private readonly declarative = new Map<string, DeclarativeImportResult>()
  private readonly options: PluginServiceOptions
  private readonly files: PluginFilesService
  private readonly blobs: PluginBlobStore
  private readonly prompts: PluginPromptBroker

  constructor(options: PluginServiceOptions) {
    this.options = options
    this.workspace = options.workspace
    this.store = new PluginStore(options.workspace.global)
    this.runtime = options.runtime
    this.clock = options.clock ?? new SystemClock()
    this.logger = options.logger
    this.confirm = options.confirm ?? new ConfirmService({ clock: this.clock })
    this.files =
      options.files ?? new PluginFilesService({ global: options.workspace.global, clock: this.clock })
    this.blobs =
      options.blobs ?? new PluginBlobStore({ pluginsRoot: this.pluginsRoot(), files: this.files })
    this.prompts = options.prompts ?? new PluginPromptBroker()
    this.rpc = new PluginRpcRouter({
      store: this.store,
      paths: options.workspace.paths,
      storage: new PluginStorageStore(options.workspace.global, this.clock),
      files: this.files,
      blobs: this.blobs,
      prompts: this.prompts,
      ...(options.watch === undefined ? {} : { watch: options.watch }),
      ...(options.promptPresenter === undefined
        ? {}
        : { promptPresenter: options.promptPresenter }),
      ...(options.pluginName === undefined ? {} : { pluginName: options.pluginName }),
      ...(options.host === undefined ? {} : { host: options.host }),
      ...(options.ai === undefined ? {} : { ai: options.ai }),
      handlers: options.rpcHandlers,
      fetchImpl: options.fetchImpl,
      clipboard: options.clipboard,
      notifications: options.notifications,
      editor: options.editor,
      logger: options.logger,
      audit: (entry) => {
        this.logger?.info('plugin audit', {
          pluginId: entry.pluginId,
          method: entry.method,
          actor: entry.actor,
          summary: entry.summary
        })
        options.audit?.(entry)
      },
      onTimeout: (pluginId, method) => {
        this.logger?.error('plugin RPC timed out; disabling plugin', { pluginId, method })
        void this.disable(pluginId, `RPC timeout: ${method}`).catch(() => undefined)
      }
    })
  }

  list(): PluginInfo[] {
    return this.store.list().map(toPluginInfo)
  }

  get(pluginId: string): PluginInfo {
    return toPluginInfo(this.store.require(pluginId))
  }

  /** Host services, exposed for IPC bridges and tests. */
  get filesService(): PluginFilesService {
    return this.files
  }

  get blobStore(): PluginBlobStore {
    return this.blobs
  }

  get promptBroker(): PluginPromptBroker {
    return this.prompts
  }

  commandsList(): PluginCommandView[] {
    return this.commands.list()
  }

  /** Installs a folder or zip source; the row stays disabled until approval. */
  async install(sourcePath: string, options: InstallOptions = {}): Promise<PluginInfo> {
    const stagingRoot = this.stagingRoot()
    mkdirSync(stagingRoot, { recursive: true, mode: 0o755 })
    const temporary = path.join(stagingRoot, `.inspect-${randomSuffix()}`)
    let staged: string | null = null
    try {
      materializePluginSource(sourcePath, temporary)
      const loaded = this.loadFromDirectory(temporary)
      const record = recordFromManifest(
        loaded.manifest,
        loaded.contributions,
        this.clock.now(),
        options.source
      )
      if (this.store.get(record.id) !== undefined) {
        pluginConflict(`plugin ${record.id} is already installed`, { pluginId: record.id })
      }
      const destination = this.pluginDir(record.id)
      if (existsSync(destination)) {
        pluginConflict(`plugin folder ${record.id} already exists`, { pluginId: record.id })
      }
      staged = path.join(stagingRoot, record.id)
      rmSync(staged, { recursive: true, force: true })
      renameSync(temporary, staged)
      renameSync(staged, destination)
      staged = null
      try {
        this.store.insert(record, this.clock.now())
      } catch (error) {
        rmSync(destination, { recursive: true, force: true })
        pluginConflict(`plugin ${record.id} cannot be registered`, {
          pluginId: record.id,
          cause: error instanceof Error ? error.message : String(error)
        })
      }
      const installed = this.store.require(record.id)
      if (options.permissions !== undefined || options.networkDomains !== undefined) {
        // A market install carries the grant set the human just approved, so
        // Install and enable are one step.
        return this.enable(record.id, options)
      }
      if (options.enable === true && installed.permissions.length === 0) {
        return this.enable(record.id)
      }
      if (options.enable === true) {
        this.store.setRuntimeState(
          record.id,
          { warnings: ['permission approval is required before enabling'] },
          this.clock.now()
        )
      }
      this.emitChanged()
      return toPluginInfo(this.store.require(record.id))
    } finally {
      rmSync(temporary, { recursive: true, force: true })
      if (staged !== null) rmSync(staged, { recursive: true, force: true })
    }
  }

  /**
   * Validates a source folder or zip and reports what it contains, without
   * touching the registry. The market uses this to compare an update candidate
   * against the current grants before deciding to install it.
   */
  inspect(sourcePath: string): { manifest: PluginManifest; contributions: PluginContributions } {
    const stagingRoot = this.stagingRoot()
    mkdirSync(stagingRoot, { recursive: true, mode: 0o755 })
    const temporary = path.join(stagingRoot, `.inspect-${randomSuffix()}`)
    try {
      materializePluginSource(sourcePath, temporary)
      const loaded = this.loadFromDirectory(temporary)
      return { manifest: loaded.manifest, contributions: loaded.contributions }
    } finally {
      rmSync(temporary, { recursive: true, force: true })
    }
  }

  async enable(pluginId: string, options: EnableOptions = {}): Promise<PluginInfo> {
    const record = this.store.require(pluginId)
    if (record.enabled && this.runtime.isRunning(pluginId)) return toPluginInfo(record)
    const loaded = this.loadInstalled(record)
    const dir = this.pluginDir(pluginId)
    // Rows installed before the plugin-host release have no renderer column value; refresh it
    // from the manifest on the next enable so custom panels appear.
    const renderer = loaded.manifest.renderer ?? null
    if (record.renderer !== renderer) this.store.setRenderer(pluginId, renderer, this.clock.now())

    let granted = record.granted
    if (options.permissions !== undefined) {
      granted = normalizeGranted(loaded.manifest.permissions, options.permissions)
    } else {
      const missing = missingPermissions(loaded.manifest.permissions, granted)
      if (missing.length > 0) {
        throw appError(
          AppErrorCode.UserConfirmationRequired,
          `plugin ${pluginId} needs permission approval: ${missing.join(', ')}`,
          { pluginId, missing, permissions: [...loaded.manifest.permissions] }
        )
      }
    }
    const networkDomains =
      options.networkDomains !== undefined
        ? normalizeNetworkDomains(options.networkDomains)
        : [...record.networkDomains]
    if (granted.includes('network') && networkDomains.length === 0) {
      throw appError(AppErrorCode.InvalidArgument, 'network permission needs at least one allowed domain', {
        field: 'networkDomains'
      })
    }
    const newWrites = newlyGrantedWriteScopes(record.granted, granted)
    if (newWrites.length > 0) {
      this.consumeWriteConfirmation(pluginId, newWrites, options.confirmationToken, options.confirmWriteScopes)
    }

    this.store.setGrants(pluginId, granted, networkDomains, this.clock.now())
    // Mark enabled before starting: the plugin's main.js may call the host API
    // immediately, and those calls must not see a half-enabled plugin.
    this.store.setEnabled(pluginId, true, this.clock.now())
    try {
      await this.runtime.start({
        pluginId,
        pluginDir: dir,
        mainPath: this.requireEntry(dir, loaded.manifest.main),
        rendererPath:
          loaded.manifest.renderer === undefined ? null : this.requireEntry(dir, loaded.manifest.renderer),
        version: loaded.manifest.version,
        permissions: loaded.manifest.permissions,
        networkDomains
      })
    } catch (error) {
      // A failed start leaves nothing half-enabled, and the grant set that was
      // just approved is rolled back so a retry asks again.
      this.store.setGrants(pluginId, record.granted, record.networkDomains, this.clock.now())
      this.store.setRuntimeState(pluginId, { lastError: messageOf(error) }, this.clock.now())
      this.store.setEnabled(pluginId, false, this.clock.now(), messageOf(error))
      this.emitChanged()
      throw error
    }

    this.commands.register(pluginId, loaded.contributions, loaded.manifest.permissions, granted)
    const declarative = importDeclarative(dir, pluginId, {
      skills: this.options.skills,
      rules: this.options.rules
    })
    this.declarative.set(pluginId, declarative)
    const warnings = [...declarative.warnings]
    this.store.setRuntimeState(
      pluginId,
      { warnings, lastError: null, contributions: loaded.contributions },
      this.clock.now()
    )
    this.store.setEnabled(pluginId, true, this.clock.now())
    this.emitChanged()
    return toPluginInfo(this.store.require(pluginId))
  }

  async disable(pluginId: string, reason?: string): Promise<PluginInfo> {
    const record = this.store.require(pluginId)
    this.releaseHostResources(pluginId)
    try {
      await this.runtime.stop(pluginId, reason)
    } catch (error) {
      this.logger?.warn('plugin runtime stop failed', { pluginId, error: messageOf(error) })
    }
    this.commands.unregister(pluginId)
    const declarative = this.declarative.get(pluginId)
    if (declarative !== undefined) {
      declarative.dispose()
      this.declarative.delete(pluginId)
    }
    this.store.setEnabled(pluginId, false, this.clock.now(), reason ?? null)
    this.emitChanged()
    return toPluginInfo(this.store.require(pluginId))
  }

  /**
   * Stages, validates, swaps atomically and re-enables. Any failure restores the
   * previous folder and row, including its enabled state.
   */
  async update(
    pluginId: string,
    sourcePath: string,
    options: UpdateOptions = {}
  ): Promise<PluginInfo> {
    const previous = this.store.require(pluginId)
    const stagingRoot = this.stagingRoot()
    mkdirSync(stagingRoot, { recursive: true, mode: 0o755 })
    const temporary = path.join(stagingRoot, `.inspect-${randomSuffix()}`)
    const staged = path.join(stagingRoot, `${pluginId}.update-${randomSuffix()}`)
    const destination = this.pluginDir(pluginId)
    const backup = path.join(stagingRoot, `${pluginId}.backup-${randomSuffix()}`)
    let movedOld = false
    let movedNew = false
    const wasEnabled = previous.enabled
    try {
      materializePluginSource(sourcePath, temporary)
      const loaded = this.loadFromDirectory(temporary)
      if (loaded.manifest.id !== pluginId) {
        invalidManifest(`update source id ${loaded.manifest.id} does not match ${pluginId}`, {
          expected: pluginId,
          actual: loaded.manifest.id
        })
      }
      if (loaded.manifest.version === previous.version) {
        pluginConflict(`plugin ${pluginId} version ${previous.version} is already installed`, {
          pluginId,
          version: previous.version
        })
      }
      renameSync(temporary, staged)
      if (wasEnabled) await this.disable(pluginId, 'update')
      renameSync(destination, backup)
      movedOld = true
      renameSync(staged, destination)
      movedNew = true
      const next: PluginRecord = {
        ...previous,
        ...recordFromManifest(loaded.manifest, loaded.contributions, this.clock.now()),
        installedAt: previous.installedAt,
        enabled: false,
        granted: previous.granted,
        networkDomains: previous.networkDomains,
        warnings: [],
        lastError: null
      }
      this.store.replaceSnapshot(next, this.clock.now())
      const granted =
        options.permissions === undefined
          ? previous.granted
          : normalizeGranted(loaded.manifest.permissions, options.permissions)
      const networkDomains =
        options.networkDomains === undefined
          ? previous.networkDomains
          : normalizeNetworkDomains(options.networkDomains)
      this.store.setGrants(pluginId, granted, networkDomains, this.clock.now())
      if (wasEnabled) {
        await this.enable(pluginId, {
          ...options,
          permissions: granted,
          networkDomains
        })
      }
      // The offer is spent: an installed newer version clears both markers.
      this.store.setCheckResult(
        pluginId,
        { availableVersion: null, updateRequiresApproval: false },
        this.clock.now()
      )
      rmSync(backup, { recursive: true, force: true })
      this.emitChanged()
      return toPluginInfo(this.store.require(pluginId))
    } catch (error) {
      try {
        await this.runtime.stop(pluginId, 'update-rollback').catch(() => undefined)
      } catch {
        // The rollback must continue even when the new runtime refuses to stop.
      }
      this.commands.unregister(pluginId)
      const declarative = this.declarative.get(pluginId)
      if (declarative !== undefined) {
        declarative.dispose()
        this.declarative.delete(pluginId)
      }
      if (movedNew) rmSync(destination, { recursive: true, force: true })
      if (movedOld) {
        try {
          renameSync(backup, destination)
        } catch (restoreError) {
          this.logger?.error('plugin update rollback could not restore the folder', {
            pluginId,
            error: messageOf(restoreError)
          })
        }
      }
      this.store.replaceSnapshot(previous, this.clock.now())
      this.store.setGrants(pluginId, previous.granted, previous.networkDomains, this.clock.now())
      this.store.setEnabled(pluginId, false, this.clock.now(), messageOf(error))
      if (wasEnabled) {
        try {
          await this.enable(pluginId, {
            permissions: previous.granted,
            networkDomains: previous.networkDomains
          })
        } catch (restoreError) {
          this.logger?.error('plugin update rollback could not re-enable', {
            pluginId,
            error: messageOf(restoreError)
          })
        }
      }
      this.emitChanged()
      throw error
    } finally {
      rmSync(temporary, { recursive: true, force: true })
      rmSync(staged, { recursive: true, force: true })
    }
  }

  async uninstall(pluginId: string): Promise<{ removed: boolean }> {
    const record = this.store.require(pluginId)
    await this.disable(pluginId, 'uninstall')
    const dir = this.pluginDir(pluginId)
    rmSync(dir, { recursive: true, force: true })
    this.store.delete(pluginId)
    this.commands.unregister(pluginId)
    this.logger?.info('plugin uninstalled', { pluginId, version: record.version })
    this.emitChanged()
    return { removed: true }
  }

  /** Replaces grants; adding a write scope needs a fresh human confirmation. */
  setGrants(
    pluginId: string,
    permissions: readonly PluginPermission[],
    networkDomains?: readonly string[],
    confirmationToken?: string,
    confirmWriteScopes?: boolean
  ): PluginInfo {
    const record = this.store.require(pluginId)
    const granted = normalizeGranted(record.permissions, permissions)
    const domains = normalizeNetworkDomains(networkDomains ?? record.networkDomains)
    if (granted.includes('network') && domains.length === 0) {
      throw appError(AppErrorCode.InvalidArgument, 'network permission needs at least one allowed domain', {
        field: 'networkDomains'
      })
    }
    const newWrites = newlyGrantedWriteScopes(record.granted, granted)
    if (newWrites.length > 0) {
      this.consumeWriteConfirmation(pluginId, newWrites, confirmationToken, confirmWriteScopes)
    }
    this.store.setGrants(pluginId, granted, domains, this.clock.now())
    if (record.enabled) {
      const blocked = this.commands.reconcile(pluginId, granted)
      if (blocked.length > 0) {
        this.logger?.info('plugin commands disabled by revoked scopes', { pluginId, blocked })
      }
      const missing = missingPermissions(record.permissions, granted)
      this.store.setRuntimeState(
        pluginId,
        { warnings: missing.length === 0 ? [] : [`missing granted scopes: ${missing.join(', ')}`] },
        this.clock.now()
      )
    }
    this.emitChanged()
    return toPluginInfo(this.store.require(pluginId))
  }

  /** Toggles background updates for one plugin; remote sources only. */
  setAutoUpdate(pluginId: string, enabled: boolean): PluginInfo {
    this.store.require(pluginId)
    this.store.setAutoUpdate(pluginId, enabled, this.clock.now())
    this.emitChanged()
    return toPluginInfo(this.store.require(pluginId))
  }

  /**
   * Stores the outcome of one update check. The changed event is emitted only
   * when the offer actually moved, so a nightly poll of current plugins does
   * not invalidate the whole plugin UI.
   */
  recordUpdateCheck(
    pluginId: string,
    result: {
      availableVersion?: string | null
      updateRequiresApproval?: boolean
      lastCheckedAt?: string | null
      lastError?: string | null
    }
  ): void {
    const before = this.store.require(pluginId)
    this.store.setCheckResult(pluginId, result, this.clock.now())
    const after = this.store.require(pluginId)
    if (
      before.availableVersion !== after.availableVersion ||
      before.updateRequiresApproval !== after.updateRequiresApproval ||
      before.lastError !== after.lastError
    ) {
      this.emitChanged()
    }
  }

  /** Hot reload entry point: re-read the manifest and restart the runtime. */
  async reload(pluginId: string): Promise<PluginInfo> {
    const previous = this.store.require(pluginId)
    const wasEnabled = previous.enabled
    try {
      const loaded = this.loadInstalled(previous)
      if (wasEnabled) await this.disable(pluginId, 'reload')
      const next: PluginRecord = {
        ...previous,
        ...recordFromManifest(loaded.manifest, loaded.contributions, this.clock.now()),
        installedAt: previous.installedAt,
        granted: previous.granted,
        networkDomains: previous.networkDomains,
        enabled: false
      }
      this.store.replaceSnapshot(next, this.clock.now())
      if (wasEnabled) {
        await this.enable(pluginId, {
          permissions: previous.granted,
          networkDomains: previous.networkDomains
        })
      } else {
        this.emitChanged()
      }
      return toPluginInfo(this.store.require(pluginId))
    } catch (error) {
      await this.runtime.stop(pluginId, 'reload-error').catch(() => undefined)
      this.commands.unregister(pluginId)
      const declarative = this.declarative.get(pluginId)
      if (declarative !== undefined) {
        declarative.dispose()
        this.declarative.delete(pluginId)
      }
      this.store.setEnabled(pluginId, false, this.clock.now(), messageOf(error))
      this.emitChanged()
      throw error
    }
  }

  /** Pushes one host event to every enabled plugin. */
  broadcastEvent(event: { type: string; payload?: unknown }): void {
    for (const record of this.store.list()) {
      if (!record.enabled) continue
      try {
        this.runtime.emitEvent?.(record.id, event)
      } catch {
        // One dead child must not stop the broadcast.
      }
    }
  }

  /** Runs one permission-checked RPC call for the runtime host. */
  call(pluginId: string, method: string, params: unknown, timeoutMs?: number): Promise<unknown> {
    return this.rpc.call(pluginId, method, params, timeoutMs)
  }

  /** App shutdown: stop every running plugin without touching the registry. */
  async shutdown(): Promise<void> {
    for (const record of this.store.list()) {
      if (!record.enabled) continue
      this.releaseHostResources(record.id)
      await this.runtime.stop(record.id, 'shutdown').catch(() => undefined)
      this.commands.unregister(record.id)
      const declarative = this.declarative.get(record.id)
      if (declarative !== undefined) {
        declarative.dispose()
        this.declarative.delete(record.id)
      }
    }
  }

  /* --------------------------------------------------------------- internals */

  /** Watches and pending dialogs die with the plugin. */
  private releaseHostResources(pluginId: string): void {
    try {
      this.options.watch?.stopAll(pluginId)
    } catch (error) {
      this.logger?.warn('plugin watch cleanup failed', { pluginId, error: messageOf(error) })
    }
    try {
      this.prompts.cancelAll(pluginId)
    } catch (error) {
      this.logger?.warn('plugin prompt cleanup failed', { pluginId, error: messageOf(error) })
    }
  }

  private loadInstalled(record: PluginRecord): LoadedPlugin {
    const dir = this.pluginDir(record.id)
    if (!existsSync(dir)) {
      throw appError(AppErrorCode.NotFound, `plugin folder ${record.path} is missing`, {
        pluginId: record.id
      })
    }
    const info = lstatSync(dir)
    if (!info.isDirectory() || info.isSymbolicLink()) {
      invalidManifest(`plugin folder ${record.path} must be a real directory`, { pluginId: record.id })
    }
    const loaded = this.loadFromDirectory(dir)
    if (loaded.manifest.id !== record.id) {
      invalidManifest(`manifest id ${loaded.manifest.id} does not match installed id ${record.id}`, {
        pluginId: record.id
      })
    }
    return loaded
  }

  private loadFromDirectory(dir: string): LoadedPlugin {
    const realDir = realpathSync(dir)
    if (!isInside(this.pluginsRoot(), realDir)) {
      invalidManifest('plugin folder escapes the workspace plugins directory')
    }
    const manifestPath = path.join(realDir, MANIFEST_FILE)
    const bytes = readRegularFile(manifestPath, 64 * 1024)
    if (bytes === null) invalidManifest(`${MANIFEST_FILE} is missing or not a regular file`)
    const manifest = parseManifestDocument(bytes.toString('utf8'))
    validateManifestSemantics(manifest)
    this.requireEntry(realDir, manifest.main)
    if (manifest.renderer !== undefined) this.requireEntry(realDir, manifest.renderer)
    return { manifest, contributions: normalizedContributions(manifest) }
  }

  private requireEntry(dir: string, relative: string): string {
    return requirePluginFile(dir, relative, MAX_MAIN_BYTES)
  }

  private pluginDir(pluginId: string): string {
    const dir = path.join(this.pluginsRoot(), pluginId)
    if (!isInside(this.pluginsRoot(), dir)) {
      throw appError(AppErrorCode.PathEscape, `plugin id ${pluginId} escapes the plugins directory`, {
        pluginId
      })
    }
    return dir
  }

  private pluginsRoot(): string {
    return path.join(this.workspace.root, 'plugins')
  }

  private stagingRoot(): string {
    return path.join(this.pluginsRoot(), '.staging')
  }

  private consumeWriteConfirmation(
    pluginId: string,
    scopes: readonly PluginPermission[],
    token: string | undefined,
    confirmedInRenderer = false
  ): void {
    if (token === undefined || token.trim() === '') {
      if (!confirmedInRenderer) {
        throw appError(
          AppErrorCode.UserConfirmationRequired,
          `granting write scopes requires confirmation: ${scopes.join(', ')}`,
          { pluginId, writeScopes: [...scopes] }
        )
      }
      token = this.confirm.issue({
        kind: 'plugin_write',
        projectId: '',
        entityTypes: ['plugin'],
        entityIds: [pluginId]
      })
    }
    this.confirm.consume(token, {
      kind: 'plugin_write',
      projectId: '',
      entityTypes: ['plugin'],
      entityIds: [pluginId]
    })
  }

  /** Creates the token a human confirmation dialog passes back to enable/setGrants. */
  issueWriteConfirmation(pluginId: string): string {
    this.store.require(pluginId)
    return this.confirm.issue({
      kind: 'plugin_write',
      projectId: '',
      entityTypes: ['plugin'],
      entityIds: [pluginId]
    })
  }

  private emitChanged(): void {
    try {
      this.options.onChange?.()
    } catch (error) {
      this.logger?.warn('plugins.changed listener failed', { error: messageOf(error) })
    }
  }
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

let suffixCounter = 0
function randomSuffix(): string {
  suffixCounter += 1
  return `${process.pid}-${Date.now().toString(36)}-${suffixCounter}`
}

/** Re-exported so tests and the desktop host share the permission predicate. */
export { isPluginWritePermission }