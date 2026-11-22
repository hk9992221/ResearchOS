import { lstatSync, readFileSync } from 'node:fs'

import type {
  PluginAiCompleteParams,
  PluginAiCompleteResult,
  PluginAiSummarizeParams,
  PluginAiSummarizeResult,
  PluginHostContext,
  PluginNavigateTarget,
  PluginPermission,
  PluginPromptParams,
  PluginPromptResult,
  PluginRpcMethod,
  PluginUiConfirmParams,
  PluginUiToastParams
} from '@researchos/shared'
import {
  PLUGIN_AI_COMPLETE_MAX_PROMPT,
  PLUGIN_AI_COMPLETE_MAX_SYSTEM,
  PLUGIN_AI_SUMMARIZE_MAX_INSTRUCTION,
  PLUGIN_AI_SUMMARIZE_MAX_OUTPUT_TOKENS,
  PLUGIN_AI_SUMMARIZE_MAX_TEXT,
  PLUGIN_NAVIGATE_TARGETS,
  PLUGIN_RPC_AI_TIMEOUT_MS,
  PLUGIN_RPC_INTERACTIVE_TIMEOUT_MS,
  PLUGIN_RPC_TIMEOUT_MS,
  PLUGIN_WATCH_DEFAULT_DEBOUNCE_MS,
  PLUGIN_WATCH_MAX_DEBOUNCE_MS,
  PLUGIN_WATCH_MIN_DEBOUNCE_MS,
  isPluginPermission
} from '@researchos/shared'
import { z } from 'zod'

import { AppError, AppErrorCode, appError } from '../../errors'
import type { Logger } from '../../logger'
import type { Paths } from '../../paths'
import type { PluginStorageStore } from './storage'
import { assertPermission, isHostAllowed } from './permissions'
import type { PluginRecord } from './store'
import { PluginStore } from './store'
import type { PluginBlobStore } from './blobs'
import type {
  PluginFilesService,
  PluginHostOperations,
  PluginWatchGateway
} from './files'
import { PLUGIN_OPEN_DENY_EXTENSIONS } from './files'
import type { PluginPromptBroker, PluginPromptPresenter } from './prompt'

/** Every host API call re-checks this live grant snapshot before dispatch. */
export interface PluginRpcContext {
  pluginId: string
  method: string
  params: Record<string, unknown>
  record: PluginRecord
  /** Activity summary already prefixed with `[plugin:<id>]`. */
  summary: string
}

export type PluginRpcHandler = (params: Record<string, unknown>, context: PluginRpcContext) => unknown | Promise<unknown>

export interface PluginAuditEntry {
  pluginId: string
  method: string
  summary: string
  actor: 'user'
}

export interface NetworkResponse {
  status: number
  headers: Record<string, string>
  body: string
}

export interface PluginFetch {
  (url: string, init: {
    method: string
    headers: Record<string, string>
    redirect: 'manual'
  }): Promise<{
    status: number
    headers: { forEach(callback: (value: string, key: string) => void): void }
    url: string
    text(): Promise<string>
  }>
}

export interface PluginRpcRouterOptions {
  store: PluginStore
  paths: Paths
  handlers?: Partial<Record<PluginRpcMethod, PluginRpcHandler>>
  fetchImpl?: PluginFetch
  clipboard?: { write(text: string): void | Promise<void> }
  notifications?: { show(title: string, body: string): void | Promise<void> }
  editor?: { insertText(text: string): void | Promise<void> }
  audit?: (entry: PluginAuditEntry) => void
  logger?: Logger
  timeoutMs?: number
  onTimeout?: (pluginId: string, method: string, timeoutMs: number) => void
  maxResponseBytes?: number
  /** Host-side `researchos.storage` backend; built-in when present. */
  storage?: PluginStorageStore
  /** Authorized-directory, enumeration and hashing backend. */
  files?: PluginFilesService
  /** Content-addressed blob pool. */
  blobs?: PluginBlobStore
  /** Watch lifecycle owned by the desktop main process. */
  watch?: PluginWatchGateway
  /** One-dialog-per-plugin broker. */
  prompts?: PluginPromptBroker
  /** Desktop presenter for prompt/confirm; required for ui.prompt/ui.confirm. */
  promptPresenter?: PluginPromptPresenter
  /** Display name for the plugin badge in host dialogs. */
  pluginName?: (pluginId: string) => string
  /** Native dialogs, shell and navigation (desktop). */
  host?: PluginHostOperations
  /** AI gateway bridge; secrets never cross this boundary. */
  ai?: PluginAiHost
}

/** Text-in / text-out AI port backed by the configured default model. */
export interface PluginAiHost {
  summarize(pluginId: string, params: PluginAiSummarizeParams): Promise<PluginAiSummarizeResult>
  complete(pluginId: string, params: PluginAiCompleteParams): Promise<PluginAiCompleteResult>
}

interface MethodDefinition {
  /** `null` = built-in capability: enabled plugins may call it without grants. */
  scope: PluginPermission | null
  summary: string
  write: boolean
  /** Independent budget for model and human-in-the-loop calls. */
  timeoutMs?: number
}

const AI_TIMEOUT = { timeoutMs: PLUGIN_RPC_AI_TIMEOUT_MS } as const
const UI_TIMEOUT = { timeoutMs: PLUGIN_RPC_INTERACTIVE_TIMEOUT_MS } as const

const METHOD_DEFINITIONS: Readonly<Record<PluginRpcMethod, MethodDefinition>> = {
  'projects.list': { scope: 'projects.read', summary: 'list projects', write: false },
  'projects.get': { scope: 'projects.read', summary: 'read a project', write: false },
  'notes.list': { scope: 'notes.read', summary: 'list notes', write: false },
  'notes.get': { scope: 'notes.read', summary: 'read a note', write: false },
  'notes.create': { scope: 'notes.write', summary: 'create a note', write: true },
  'notes.updateMeta': { scope: 'notes.write', summary: 'update note metadata', write: true },
  'notes.saveBody': { scope: 'notes.write', summary: 'save a note body', write: true },
  'databases.list': { scope: 'notes.read', summary: 'list research databases', write: false },
  'databases.get': { scope: 'notes.read', summary: 'read a research database', write: false },
  'databases.create': { scope: 'notes.write', summary: 'create a research database', write: true },
  'records.list': { scope: 'notes.read', summary: 'list database records', write: false },
  'records.get': { scope: 'notes.read', summary: 'read a database record', write: false },
  'records.create': { scope: 'notes.write', summary: 'create a database record', write: true },
  'records.update': { scope: 'notes.write', summary: 'update a database record', write: true },
  'records.delete': { scope: 'notes.write', summary: 'delete a database record', write: true },
  'literature.list': { scope: 'literature.read', summary: 'list papers', write: false },
  'literature.get': { scope: 'literature.read', summary: 'read a paper', write: false },
  'ideas.list': { scope: 'ideas.read', summary: 'list ideas', write: false },
  'ideas.get': { scope: 'ideas.read', summary: 'read an idea', write: false },
  'ideas.create': { scope: 'ideas.write', summary: 'create an idea', write: true },
  'ideas.update': { scope: 'ideas.write', summary: 'update an idea', write: true },
  'evidence.list': { scope: 'ideas.read', summary: 'list idea evidence', write: false },
  'evidence.create': { scope: 'ideas.write', summary: 'create idea evidence', write: true },
  'experiments.list': { scope: 'experiments.read', summary: 'list experiments', write: false },
  'experiments.get': { scope: 'experiments.read', summary: 'read an experiment', write: false },
  'experiments.runs.list': { scope: 'experiments.read', summary: 'list experiment runs', write: false },
  'kanban.tasks.list': { scope: 'kanban.read', summary: 'list kanban tasks', write: false },
  'kanban.tasks.create': { scope: 'kanban.write', summary: 'create a kanban task', write: true },
  'kanban.tasks.update': { scope: 'kanban.write', summary: 'update a kanban task', write: true },
  'weekly.reports.list': { scope: 'weekly.read', summary: 'list weekly reports', write: false },
  'activity.list': { scope: 'projects.read', summary: 'list project activity', write: false },
  'relations.list': { scope: 'relations.read', summary: 'list entity relations', write: false },
  'relations.add': { scope: 'relations.write', summary: 'add an entity relation', write: true },
  'relations.remove': { scope: 'relations.write', summary: 'remove an entity relation', write: true },
  'search.query': { scope: 'search.read', summary: 'run a search', write: false },
  'files.read': { scope: 'files.read', summary: 'read a workspace file', write: false },
  'files.pickDirectory': { scope: 'files.watch', summary: 'authorize a directory', write: false },
  'files.listAuthorizedDirs': { scope: 'files.watch', summary: 'list authorized directories', write: false },
  'files.revokeDirectory': { scope: 'files.watch', summary: 'revoke a directory', write: false },
  'files.list': { scope: 'files.watch', summary: 'list an authorized directory', write: false },
  'files.stat': { scope: 'files.watch', summary: 'stat an authorized file', write: false },
  'files.readText': { scope: 'files.watch', summary: 'read an authorized text file', write: false },
  'files.watch.start': { scope: 'files.watch', summary: 'watch an authorized directory', write: false },
  'files.watch.stop': { scope: 'files.watch', summary: 'stop a directory watch', write: false },
  'files.revealInFolder': { scope: 'files.watch', summary: 'reveal a file in the OS', write: false },
  'files.open': { scope: 'files.watch', summary: 'open a file in the OS', write: false },
  'blobs.capture': { scope: 'files.blobs', summary: 'capture a file blob', write: false },
  'blobs.restore': { scope: 'files.blobs', summary: 'restore a file blob', write: false },
  'blobs.delete': { scope: 'files.blobs', summary: 'delete a file blob', write: false },
  'blobs.stats': { scope: 'files.blobs', summary: 'read blob pool stats', write: false },
  'ai.summarize': { scope: 'ai.use', summary: 'summarize text with AI', write: false, ...AI_TIMEOUT },
  'ai.complete': { scope: 'ai.use', summary: 'complete text with AI', write: false, ...AI_TIMEOUT },
  'ui.prompt': { scope: 'ui.prompt', summary: 'show a host prompt', write: false, ...UI_TIMEOUT },
  'ui.confirm': { scope: 'ui.prompt', summary: 'show a host confirmation', write: false, ...UI_TIMEOUT },
  'ui.toast': { scope: null, summary: 'show a host toast', write: false },
  'ui.navigate': { scope: null, summary: 'navigate the host shell', write: false },
  'host.getContext': { scope: null, summary: 'read host context', write: false },
  'network.fetch': { scope: 'network', summary: 'fetch a remote URL', write: false },
  'notifications.show': { scope: 'notifications', summary: 'show a notification', write: false },
  'clipboard.write': { scope: 'clipboard', summary: 'write to the clipboard', write: false },
  'editor.insertText': { scope: 'notes.write', summary: 'insert text in the editor', write: true },
  'storage.get': { scope: null, summary: 'read plugin private storage', write: false },
  'storage.set': { scope: null, summary: 'write plugin private storage', write: false },
  'storage.delete': { scope: null, summary: 'delete plugin private storage key', write: false },
  'storage.list': { scope: null, summary: 'list plugin private storage keys', write: false },
  'storage.clear': { scope: null, summary: 'clear plugin private storage', write: false }
}

export const PLUGIN_RPC_METHODS = Object.keys(METHOD_DEFINITIONS) as PluginRpcMethod[]

const MAX_FILE_BYTES = 1024 * 1024
const MAX_NETWORK_BYTES = 1024 * 1024

const filesParams = z.object({ relativePath: z.string().min(1).max(1024) }).strict()
const networkParams = z
  .object({
    url: z.string().url().max(2048),
    method: z.enum(['GET', 'HEAD', 'POST']).optional(),
    headers: z.record(z.string().max(200), z.string().max(2000)).optional()
  })
  .strict()
const notificationParams = z
  .object({ title: z.string().trim().min(1).max(200), body: z.string().max(2000) })
  .strict()
const clipboardParams = z.object({ text: z.string().max(1024 * 1024) }).strict()
const editorParams = z.object({ text: z.string().max(256 * 1024) }).strict()
// Storage params carry only shape here; format and quotas are enforced by
// PluginStorageStore so the rules live in exactly one place.
const storageKeyParams = z.object({ key: z.string(), projectId: z.string().optional() }).strict()
const storageSetParams = z
  .object({ key: z.string(), value: z.unknown(), projectId: z.string().optional() })
  .strict()
const storageListParams = z
  .object({
    prefix: z.string().optional(),
    projectId: z.string().optional(),
    limit: z.number().optional(),
    cursor: z.string().optional()
  })
  .strict()
const storageScopeParams = z.object({ projectId: z.string().optional() }).strict()

const dirIdParams = z.object({ dirId: z.string().min(1).max(64) }).strict()
const pickDirectoryParams = z
  .object({ projectId: z.string().min(1).max(128).optional(), title: z.string().max(200).optional() })
  .strict()
const listDirsParams = z.object({ projectId: z.string().min(1).max(128).optional() }).strict()
const ignoreList = z.array(z.string().min(1).max(200)).max(50).optional()
const listFilesParams = z
  .object({
    dirId: z.string().min(1).max(64),
    relativePath: z.string().max(1024).optional(),
    ignore: ignoreList,
    maxDepth: z.number().int().min(1).max(64).optional()
  })
  .strict()
const statFileParams = z
  .object({
    dirId: z.string().min(1).max(64),
    relativePath: z.string().min(1).max(1024),
    includeHash: z.boolean().optional()
  })
  .strict()
const readTextParams = z
  .object({
    dirId: z.string().min(1).max(64),
    relativePath: z.string().min(1).max(1024),
    maxBytes: z.number().int().min(1).optional()
  })
  .strict()
const watchStartParams = z
  .object({
    dirId: z.string().min(1).max(64),
    ignore: ignoreList,
    debounceMs: z.number().int().optional()
  })
  .strict()
const watchStopParams = z.object({ watchId: z.string().min(1).max(64) }).strict()
const revealParams = z
  .object({ dirId: z.string().min(1).max(64), relativePath: z.string().max(1024).optional() })
  .strict()
const openParams = z
  .object({ dirId: z.string().min(1).max(64), relativePath: z.string().min(1).max(1024) })
  .strict()
const captureParams = z
  .object({ dirId: z.string().min(1).max(64), relativePath: z.string().min(1).max(1024) })
  .strict()
const restoreParams = z
  .object({
    sha256: z.string().regex(/^[a-f0-9]{64}$/),
    dirId: z.string().min(1).max(64),
    relativePath: z.string().min(1).max(1024),
    overwrite: z.boolean().optional()
  })
  .strict()
const blobShaParams = z.object({ sha256: z.string().regex(/^[a-f0-9]{64}$/) }).strict()
const emptyParams = z.object({}).strict()
const summarizeParams = z
  .object({
    text: z.string().max(PLUGIN_AI_SUMMARIZE_MAX_TEXT),
    instruction: z.string().max(PLUGIN_AI_SUMMARIZE_MAX_INSTRUCTION).optional(),
    maxOutputTokens: z.number().int().min(1).max(PLUGIN_AI_SUMMARIZE_MAX_OUTPUT_TOKENS).optional()
  })
  .strict()
const completeParams = z
  .object({
    system: z.string().max(PLUGIN_AI_COMPLETE_MAX_SYSTEM).optional(),
    prompt: z.string().min(1).max(PLUGIN_AI_COMPLETE_MAX_PROMPT),
    jsonMode: z.boolean().optional(),
    maxOutputTokens: z.number().int().min(1).max(PLUGIN_AI_SUMMARIZE_MAX_OUTPUT_TOKENS).optional()
  })
  .strict()
const promptParams = z
  .object({
    title: z.string().trim().min(1).max(200),
    message: z.string().max(2000).optional(),
    placeholder: z.string().max(200).optional(),
    defaultValue: z.string().max(10_000).optional(),
    multiline: z.boolean().optional(),
    confirmLabel: z.string().max(80).optional(),
    cancelLabel: z.string().max(80).optional()
  })
  .strict()
const confirmParams = z
  .object({
    title: z.string().trim().min(1).max(200),
    message: z.string().max(4000),
    confirmLabel: z.string().max(80).optional(),
    cancelLabel: z.string().max(80).optional(),
    destructive: z.boolean().optional()
  })
  .strict()
const toastParams = z
  .object({
    message: z.string().trim().min(1).max(2000),
    level: z.enum(['info', 'success', 'warning', 'error']).optional()
  })
  .strict()
const navigateParams = z
  .object({
    target: z.enum(PLUGIN_NAVIGATE_TARGETS),
    projectId: z.string().min(1).max(128).optional(),
    entityType: z.string().min(1).max(64).optional(),
    entityId: z.string().min(1).max(128).optional()
  })
  .strict()

/**
 * Permission-checked plugin RPC dispatcher. It is transport-agnostic: the
 * utility-process host feeds it MessagePort requests and turns the result into
 * `PluginRpcResponse`.
 */
export class PluginRpcRouter {
  private readonly options: PluginRpcRouterOptions
  private readonly timeoutMs: number

  constructor(options: PluginRpcRouterOptions) {
    this.options = options
    this.timeoutMs = options.timeoutMs !== undefined && options.timeoutMs > 0 ? options.timeoutMs : PLUGIN_RPC_TIMEOUT_MS
  }

  /** Validates a request envelope from the plugin side of the MessagePort. */
  static validateRequest(value: unknown): {
    requestId: string
    method: PluginRpcMethod
    params: Record<string, unknown>
  } {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) {
      throw appError(AppErrorCode.InvalidArgument, 'plugin RPC message must be an object')
    }
    const record = value as Record<string, unknown>
    if (typeof record.requestId !== 'string' || record.requestId === '' || record.requestId.length > 128) {
      throw appError(AppErrorCode.InvalidArgument, 'plugin RPC requestId must be a non-empty string')
    }
    if (typeof record.method !== 'string' || !(PLUGIN_RPC_METHODS as string[]).includes(record.method)) {
      throw appError(AppErrorCode.NotFound, `unknown plugin RPC method ${String(record.method)}`)
    }
    const params = record.params
    if (params === undefined) {
      return { requestId: record.requestId, method: record.method as PluginRpcMethod, params: {} }
    }
    if (typeof params !== 'object' || params === null || Array.isArray(params)) {
      throw appError(AppErrorCode.InvalidArgument, 'plugin RPC params must be an object')
    }
    return {
      requestId: record.requestId,
      method: record.method as PluginRpcMethod,
      params: params as Record<string, unknown>
    }
  }

  async call(pluginId: string, method: string, params: unknown, timeoutOverride?: number): Promise<unknown> {
    const definition = (METHOD_DEFINITIONS as Record<string, MethodDefinition | undefined>)[method]
    if (definition === undefined) {
      throw appError(AppErrorCode.NotFound, `unknown plugin RPC method ${method}`, { method })
    }
    const record = this.options.store.require(pluginId)
    if (!record.enabled) {
      throw appError(AppErrorCode.Forbidden, `plugin ${pluginId} is disabled`, { pluginId })
    }
    // Re-read persisted grants on every call: a revoked scope must fail the
    // very next request, not only after a restart. Built-in capabilities
    // (scope `null`, e.g. storage.*) only require the enabled check above.
    if (definition.scope !== null) assertPermission(pluginId, record.granted, definition.scope)
    if (definition.scope === 'network' && record.networkDomains.length === 0) {
      throw appError(AppErrorCode.Forbidden, `plugin ${pluginId} has no network domains granted`, { pluginId })
    }
    const paramsObject = params === undefined ? {} : params
    if (typeof paramsObject !== 'object' || paramsObject === null || Array.isArray(paramsObject)) {
      throw appError(AppErrorCode.InvalidArgument, 'plugin RPC params must be an object', { method })
    }
    const summary = `[plugin:${pluginId}] ${definition.summary}`
    const context: PluginRpcContext = {
      pluginId,
      method,
      params: paramsObject as Record<string, unknown>,
      record,
      summary
    }
    this.options.audit?.({ pluginId, method, summary, actor: 'user' })
    const handler = this.handlerFor(method as PluginRpcMethod, context)
    const defaultTimeout = definition.timeoutMs ?? this.timeoutMs
    const timeoutMs =
      timeoutOverride !== undefined && timeoutOverride > 0 ? timeoutOverride : defaultTimeout
    return withTimeout(
      Promise.resolve().then(() => handler(context.params, context)),
      timeoutMs,
      () => {
        this.options.onTimeout?.(pluginId, method, timeoutMs)
        return appError(
          AppErrorCode.Internal,
          `plugin ${pluginId} call ${method} timed out after ${timeoutMs} ms`,
          { pluginId, method, timeout: true, timeoutMs }
        )
      }
    ).catch((error: unknown) => {
      this.options.logger?.warn('plugin RPC failed', {
        pluginId,
        method,
        error: error instanceof Error ? error.message : String(error)
      })
      throw error
    })
  }

  private handlerFor(method: PluginRpcMethod, context: PluginRpcContext): PluginRpcHandler {
    switch (method) {
      case 'files.read':
        return (params) => this.readWorkspaceFile(params)
      case 'files.pickDirectory':
        return (params, ctx) => this.pickDirectory(ctx.pluginId, params)
      case 'files.listAuthorizedDirs':
        return (params, ctx) => {
          const value = listDirsParams.parse(params)
          return { dirs: this.filesService().listDirs(ctx.pluginId, value.projectId) }
        }
      case 'files.revokeDirectory':
        return (params, ctx) => {
          const value = dirIdParams.parse(params)
          this.filesService().requireOwned(ctx.pluginId, value.dirId)
          this.options.watch?.stopDir(ctx.pluginId, value.dirId)
          return { removed: this.filesService().revoke(ctx.pluginId, value.dirId) }
        }
      case 'files.list':
        return (params, ctx) => this.filesService().list(ctx.pluginId, listFilesParams.parse(params))
      case 'files.stat':
        return (params, ctx) => this.filesService().stat(ctx.pluginId, statFileParams.parse(params))
      case 'files.readText':
        return (params, ctx) => {
          const value = readTextParams.parse(params)
          return this.filesService().readText(ctx.pluginId, {
            dirId: value.dirId,
            relativePath: value.relativePath,
            ...(value.maxBytes === undefined ? {} : { maxBytes: value.maxBytes })
          })
        }
      case 'files.watch.start':
        return (params, ctx) => this.startWatch(ctx.pluginId, params)
      case 'files.watch.stop':
        return (params, ctx) => {
          const value = watchStopParams.parse(params)
          const watch = this.options.watch
          if (watch === undefined) throw externalUnavailable('files.watch.stop')
          return { stopped: watch.stop(ctx.pluginId, value.watchId) }
        }
      case 'files.revealInFolder':
        return (params, ctx) => {
          const value = revealParams.parse(params)
          const host = this.options.host
          if (host?.revealInFolder === undefined) {
            throw externalUnavailable('files.revealInFolder')
          }
          const resolved = this.filesService().resolve(
            ctx.pluginId,
            value.dirId,
            value.relativePath ?? ''
          )
          return Promise.resolve(host.revealInFolder(ctx.pluginId, resolved.absolute)).then((ok) => ({
            ok
          }))
        }
      case 'files.open':
        return (params, ctx) => {
          const value = openParams.parse(params)
          const host = this.options.host
          if (host?.openPath === undefined) throw externalUnavailable('files.open')
          const resolved = this.filesService().resolve(ctx.pluginId, value.dirId, value.relativePath)
          const info = lstatSync(resolved.absolute)
          if (!info.isFile()) {
            throw appError(AppErrorCode.InvalidArgument, 'only regular files can be opened', {
              field: 'relativePath'
            })
          }
          const extension = resolved.absolute.slice(resolved.absolute.lastIndexOf('.')).toLowerCase()
          if (PLUGIN_OPEN_DENY_EXTENSIONS.has(extension)) {
            throw appError(AppErrorCode.Unsupported, `refusing to open ${extension} files`, {
              field: 'relativePath'
            })
          }
          return Promise.resolve(host.openPath(ctx.pluginId, resolved.absolute)).then((ok) => ({
            ok
          }))
        }
      case 'blobs.capture': {
        const blobs = this.blobStore()
        return (params, ctx) => blobs.capture(ctx.pluginId, captureParams.parse(params))
      }
      case 'blobs.restore': {
        const blobs = this.blobStore()
        return (params, ctx) => {
          const value = restoreParams.parse(params)
          return blobs.restore(ctx.pluginId, {
            sha256: value.sha256,
            dirId: value.dirId,
            relativePath: value.relativePath,
            ...(value.overwrite === undefined ? {} : { overwrite: value.overwrite })
          })
        }
      }
      case 'blobs.delete': {
        const blobs = this.blobStore()
        return (params, ctx) => blobs.delete(ctx.pluginId, blobShaParams.parse(params).sha256)
      }
      case 'blobs.stats': {
        const blobs = this.blobStore()
        return (params, ctx) => {
          emptyParams.parse(params)
          return blobs.stats(ctx.pluginId)
        }
      }
      case 'ai.summarize': {
        const ai = this.options.ai
        return (params, ctx) => {
          const value = summarizeParams.parse(params)
          if (ai === undefined) throw externalUnavailable('ai.summarize')
          return ai.summarize(ctx.pluginId, value)
        }
      }
      case 'ai.complete': {
        const ai = this.options.ai
        return (params, ctx) => {
          const value = completeParams.parse(params)
          if (ai === undefined) throw externalUnavailable('ai.complete')
          return ai.complete(ctx.pluginId, value)
        }
      }
      case 'ui.prompt':
        return (params, ctx) => {
          const value = promptParams.parse(params)
          const broker = this.options.prompts
          const presenter = this.options.promptPresenter
          if (broker === undefined || presenter === undefined) {
            throw externalUnavailable('ui.prompt')
          }
          return broker.prompt(
            ctx.pluginId,
            this.options.pluginName?.(ctx.pluginId) ?? ctx.pluginId,
            value,
            presenter
          )
        }
      case 'ui.confirm':
        return (params, ctx) => {
          const value = confirmParams.parse(params)
          const broker = this.options.prompts
          const presenter = this.options.promptPresenter
          if (broker === undefined || presenter === undefined) {
            throw externalUnavailable('ui.confirm')
          }
          return broker.confirm(
            ctx.pluginId,
            this.options.pluginName?.(ctx.pluginId) ?? ctx.pluginId,
            value,
            presenter
          )
        }
      case 'ui.toast':
        return (params, ctx) => {
          const value = toastParams.parse(params)
          this.options.host?.toast?.(ctx.pluginId, value as PluginUiToastParams)
        }
      case 'ui.navigate':
        return (params, ctx) => {
          const value = navigateParams.parse(params)
          const navigate = this.options.host?.navigate
          if (navigate === undefined) throw externalUnavailable('ui.navigate')
          return {
            navigated: navigate(ctx.pluginId, {
              target: value.target as PluginNavigateTarget,
              ...(value.projectId === undefined ? {} : { projectId: value.projectId }),
              ...(value.entityType === undefined ? {} : { entityType: value.entityType }),
              ...(value.entityId === undefined ? {} : { entityId: value.entityId })
            })
          }
        }
      case 'host.getContext':
        return (params) => {
          emptyParams.parse(params)
          const context = this.options.host?.context
          if (context === undefined) throw externalUnavailable('host.getContext')
          return context()
        }
      case 'network.fetch':
        return (params, ctx) => this.networkFetch(params, ctx)
      case 'notifications.show': {
        const sink = this.options.notifications
        return (params) => {
          const value = notificationParams.parse(params)
          if (sink === undefined) throw externalUnavailable(method)
          return sink.show(value.title, value.body)
        }
      }
      case 'clipboard.write': {
        const sink = this.options.clipboard
        return (params) => {
          const value = clipboardParams.parse(params)
          if (sink === undefined) throw externalUnavailable(method)
          return sink.write(value.text)
        }
      }
      case 'editor.insertText': {
        const sink = this.options.editor
        return (params) => {
          const value = editorParams.parse(params)
          if (sink === undefined) throw externalUnavailable(method)
          return sink.insertText(value.text)
        }
      }
      case 'storage.get': {
        const storage = this.storageStore()
        return (params, ctx) => storage.get(ctx?.pluginId ?? context.pluginId, storageKeyParams.parse(params))
      }
      case 'storage.set': {
        const storage = this.storageStore()
        return (params, ctx) => storage.set(ctx?.pluginId ?? context.pluginId, storageSetParams.parse(params))
      }
      case 'storage.delete': {
        const storage = this.storageStore()
        return (params, ctx) => storage.remove(ctx?.pluginId ?? context.pluginId, storageKeyParams.parse(params))
      }
      case 'storage.list': {
        const storage = this.storageStore()
        return (params, ctx) => storage.list(ctx?.pluginId ?? context.pluginId, storageListParams.parse(params))
      }
      case 'storage.clear': {
        const storage = this.storageStore()
        return (params, ctx) => storage.clear(ctx?.pluginId ?? context.pluginId, storageScopeParams.parse(params))
      }
      default: {
        const handler = this.options.handlers?.[method]
        if (handler === undefined) throw externalUnavailable(method)
        return (params, ctx) => handler(params, ctx ?? context)
      }
    }
  }

  private filesService(): PluginFilesService {
    const files = this.options.files
    if (files === undefined) throw externalUnavailable('files')
    return files
  }

  private blobStore(): PluginBlobStore {
    const blobs = this.options.blobs
    if (blobs === undefined) throw externalUnavailable('blobs')
    return blobs
  }

  private async pickDirectory(
    pluginId: string,
    params: Record<string, unknown>
  ): Promise<{ cancelled: boolean; dirId?: string; displayName?: string }> {
    const value = pickDirectoryParams.parse(params)
    const picker = this.options.host?.pickDirectory
    if (picker === undefined) throw externalUnavailable('files.pickDirectory')
    const picked = await picker(pluginId, {
      ...(value.projectId === undefined ? {} : { projectId: value.projectId }),
      ...(value.title === undefined ? {} : { title: value.title })
    })
    if (picked.cancelled || picked.dirPath === undefined) return { cancelled: true }
    const granted = this.filesService().authorize(
      pluginId,
      picked.dirPath,
      picked.displayName ?? '',
      value.projectId ?? null
    )
    return { cancelled: false, dirId: granted.dirId, displayName: granted.displayName }
  }

  private startWatch(
    pluginId: string,
    params: Record<string, unknown>
  ): { watchId: string } {
    const value = watchStartParams.parse(params)
    const watch = this.options.watch
    if (watch === undefined) throw externalUnavailable('files.watch.start')
    const dir = this.filesService().requireOwned(pluginId, value.dirId)
    const debounceMs = Math.min(
      Math.max(value.debounceMs ?? PLUGIN_WATCH_DEFAULT_DEBOUNCE_MS, PLUGIN_WATCH_MIN_DEBOUNCE_MS),
      PLUGIN_WATCH_MAX_DEBOUNCE_MS
    )
    return watch.start({
      pluginId,
      dirId: dir.id,
      projectId: dir.projectId,
      rootPath: dir.dirPath,
      ignore: value.ignore ?? [],
      debounceMs
    })
  }

  private storageStore(): PluginStorageStore {
    const storage = this.options.storage
    if (storage === undefined) throw externalUnavailable('storage')
    return storage
  }

  private readWorkspaceFile(params: Record<string, unknown>): { text: string; bytes: number } {
    const value = filesParams.parse(params)
    const absolute = this.options.paths.abs(value.relativePath)
    const info = lstatSync(absolute, { throwIfNoEntry: false })
    if (info === undefined || !info.isFile() || info.isSymbolicLink()) {
      throw appError(AppErrorCode.NotFound, `file not found: ${value.relativePath}`, { field: 'relativePath' })
    }
    if (info.size > MAX_FILE_BYTES) {
      throw appError(AppErrorCode.LimitExceeded, `file exceeds ${MAX_FILE_BYTES} bytes`, {
        field: 'relativePath',
        size: info.size
      })
    }
    const data = readFileSync(absolute)
    return { text: data.toString('utf8'), bytes: data.length }
  }

  private async networkFetch(params: Record<string, unknown>, context: PluginRpcContext): Promise<NetworkResponse> {
    const value = networkParams.parse(params)
    let url: URL
    try {
      url = new URL(value.url)
    } catch {
      throw appError(AppErrorCode.InvalidArgument, 'invalid URL', { field: 'url' })
    }
    if (url.protocol !== 'http:' && url.protocol !== 'https:') {
      throw appError(AppErrorCode.Unsupported, `unsupported URL protocol ${url.protocol}`, { field: 'url' })
    }
    if (!isHostAllowed(url.hostname, context.record.networkDomains)) {
      throw appError(AppErrorCode.Forbidden, `host ${url.hostname} is not allow-listed`, {
        pluginId: context.pluginId,
        host: url.hostname
      })
    }
    const fetchImpl = this.options.fetchImpl ?? (globalThis.fetch as unknown as PluginFetch | undefined)
    if (fetchImpl === undefined) throw externalUnavailable('network.fetch')
    const response = await fetchImpl(value.url, {
      method: value.method ?? 'GET',
      headers: value.headers ?? {},
      redirect: 'manual'
    })
    if (response.status >= 300 && response.status < 400) {
      throw appError(
        AppErrorCode.Forbidden,
        `redirect responses are not allowed for plugin network calls`,
        { pluginId: context.pluginId, status: response.status }
      )
    }
    const finalUrl = new URL(response.url)
    if (!isHostAllowed(finalUrl.hostname, context.record.networkDomains)) {
      throw appError(AppErrorCode.Forbidden, `redirect host ${finalUrl.hostname} is not allow-listed`, {
        pluginId: context.pluginId,
        host: finalUrl.hostname
      })
    }
    const text = await response.text()
    if (Buffer.byteLength(text, 'utf8') > (this.options.maxResponseBytes ?? MAX_NETWORK_BYTES)) {
      throw appError(AppErrorCode.LimitExceeded, 'network response is too large', { field: 'url' })
    }
    const headers: Record<string, string> = {}
    response.headers.forEach((headerValue, key) => {
      headers[key] = headerValue
    })
    return { status: response.status, headers, body: text }
  }
}

/** Validates a host → frame response envelope. */
export function validateRpcResponse(value: unknown): { requestId: string; ok: boolean } {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw appError(AppErrorCode.InvalidArgument, 'plugin RPC response must be an object')
  }
  const record = value as Record<string, unknown>
  if (typeof record.requestId !== 'string' || record.requestId === '') {
    throw appError(AppErrorCode.InvalidArgument, 'plugin RPC response is missing requestId')
  }
  if (typeof record.ok !== 'boolean') {
    throw appError(AppErrorCode.InvalidArgument, 'plugin RPC response is missing ok')
  }
  return { requestId: record.requestId, ok: record.ok }
}

function externalUnavailable(method: string): AppError {
  return appError(AppErrorCode.ExternalUnavailable, `host method ${method} is not available`, { method })
}

function withTimeout<T>(promise: Promise<T>, timeoutMs: number, onTimeout: () => AppError): Promise<T> {
  // ES2022 target: Promise.withResolvers is not in the lib yet.
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(onTimeout()), timeoutMs)
    timer.unref?.()
    promise.then(
      (value) => {
        clearTimeout(timer)
        resolve(value)
      },
      (error: unknown) => {
        clearTimeout(timer)
        reject(error)
      }
    )
  })
}