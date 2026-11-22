import type { AppErrorShape } from './errors'

/** Plugin ABI version; bump on breaking host-API changes. */
export const PLUGIN_API_VERSION = 1

export const PLUGIN_PERMISSIONS = [
  'projects.read',
  'projects.write',
  'notes.read',
  'notes.write',
  'literature.read',
  'literature.write',
  'ideas.read',
  'ideas.write',
  'experiments.read',
  'experiments.write',
  'kanban.read',
  'kanban.write',
  'weekly.read',
  'weekly.write',
  'search.read',
  'files.read',
  'files.watch',
  'files.blobs',
  'network',
  'clipboard',
  'notifications',
  'ai.use',
  'ui.prompt',
  'relations.read',
  'relations.write'
] as const

export type PluginPermission = (typeof PLUGIN_PERMISSIONS)[number]

const PERMISSION_LOOKUP: Readonly<Record<string, true>> = Object.fromEntries(
  PLUGIN_PERMISSIONS.map((permission) => [permission, true])
)

export function isPluginPermission(value: unknown): value is PluginPermission {
  return typeof value === 'string' && PERMISSION_LOOKUP[value] === true
}

/** Domain write scopes need an explicit extra confirmation at grant time. */
export function isPluginWritePermission(permission: PluginPermission): boolean {
  return permission.endsWith('.write')
}

export const PLUGIN_WRITE_PERMISSIONS: readonly PluginPermission[] = PLUGIN_PERMISSIONS.filter(
  isPluginWritePermission
)

/** Scopes whose grant needs an explicit human acknowledgement. */
export const PLUGIN_SENSITIVE_PERMISSIONS = [
  'files.read',
  'files.watch',
  'files.blobs',
  'ai.use',
  'network',
  'ui.prompt'
] as const satisfies readonly PluginPermission[]

export function isPluginSensitivePermission(permission: PluginPermission): boolean {
  return (PLUGIN_SENSITIVE_PERMISSIONS as readonly PluginPermission[]).includes(permission)
}

export interface PluginCommandContribution {
  id: string
  title: string
  /**
   * Scopes this command needs. Omitted means it inherits the manifest's whole
   * permission set, so revoking any granted scope disables the command.
   */
  permissions?: PluginPermission[]
}

export interface PluginSidebarContribution {
  id: string
  title: string
  icon?: string
  /** Declarative entry name, or `renderer` for the plugin's custom panel. */
  entry: string
  /** 'project' mounts inside the open project's sub-sidebar (default). */
  scope?: 'workspace' | 'project'
  permissions?: PluginPermission[]
}

export interface PluginInspectorContribution {
  id: string
  title: string
  /** Declarative entry name, or `renderer` for the plugin's custom panel. */
  entry: string
  permissions?: PluginPermission[]
}

export interface PluginEditorContribution {
  id: string
  title: string
  /** The command id the editor asks the host to run. */
  command: string
  permissions?: PluginPermission[]
}

export interface PluginContributions {
  commands?: PluginCommandContribution[]
  sidebar?: PluginSidebarContribution[]
  inspector?: PluginInspectorContribution[]
  editor?: PluginEditorContribution[]
}

export interface PluginManifest {
  id: string
  name: string
  version: string
  apiVersion: number
  description?: string
  /** Required entry script, relative to the plugin root. */
  main: string
  /** Optional custom panel script, relative to the plugin root. */
  renderer?: string
  permissions: PluginPermission[]
  contributes?: PluginContributions
}

export type PluginState = 'installed' | 'enabled'

export interface PluginInfo {
  id: string
  name: string
  version: string
  apiVersion: number
  description: string
  /** Effective state: the row is enabled and the runtime is running. */
  enabled: boolean
  /** Workspace-relative plugin folder, always `plugins/<id>`. */
  path: string
  installedAt: string
  updatedAt: string
  /** Where the artifact came from. */
  sourceType: PluginSourceType
  /** Registry `downloadUrl` / release asset URL recorded at install time. */
  sourceUrl: string
  /** Owning registry id; `''` for local and GitHub-direct installs. */
  registryId: string
  /** Silent auto-update is allowed for this plugin (default on). */
  autoUpdate: boolean
  /** Newer remote version found by the checker; `null` when up to date. */
  availableVersion: string | null
  /** The available version needs a human: new scopes or new domains. */
  updateRequiresApproval: boolean
  lastCheckedAt: string | null
  /** Manifest `renderer` entry, or null for declarative-only plugins. */
  renderer: string | null
  /** Scopes declared by the manifest. */
  permissions: PluginPermission[]
  /** Scopes the human approved. */
  granted: PluginPermission[]
  /** Domain allow-list for the `network` scope. */
  networkDomains: string[]
  contributions: PluginContributions
  /** Non-fatal problems found while loading declarative contributions. */
  warnings: string[]
  lastError: string | null
}

export interface PluginGrants {
  permissions: PluginPermission[]
  networkDomains?: string[]
}

/* ------------------------------------------------------------ market  */

export const PLUGIN_SOURCE_TYPES = ['local', 'registry', 'github'] as const
export type PluginSourceType = (typeof PLUGIN_SOURCE_TYPES)[number]

export function isPluginSourceType(value: unknown): value is PluginSourceType {
  return (
    typeof value === 'string' && (PLUGIN_SOURCE_TYPES as readonly string[]).includes(value)
  )
}

export const PLUGIN_MARKET_SCHEMA_VERSION = 1
/** Remote request timeout. */
export const PLUGIN_MARKET_TIMEOUT_MS = 15_000
/** Release artifact cap. */
export const PLUGIN_MARKET_MAX_DOWNLOAD_BYTES = 20 * 1024 * 1024
/** `registry.json` cap: an index document, never a bundle. */
export const PLUGIN_MARKET_MAX_REGISTRY_BYTES = 4 * 1024 * 1024
/** README fetch cap. */
export const PLUGIN_MARKET_MAX_README_BYTES = 512 * 1024
/** First background check after the desktop is ready. */
export const PLUGIN_MARKET_STARTUP_DELAY_MS = 15_000
/** Default poll period. */
export const PLUGIN_MARKET_DEFAULT_INTERVAL = '24h'

export const PLUGIN_MARKET_INTERVALS = ['off', '6h', '12h', '24h'] as const
export type PluginMarketInterval = (typeof PLUGIN_MARKET_INTERVALS)[number]

export const PLUGIN_MARKET_INTERVAL_MS: Readonly<Record<Exclude<PluginMarketInterval, 'off'>, number>> = {
  '6h': 6 * 60 * 60 * 1000,
  '12h': 12 * 60 * 60 * 1000,
  '24h': 24 * 60 * 60 * 1000
}

export const PLUGIN_MARKET_OFFICIAL_REGISTRY_ID = 'official'
export const PLUGIN_MARKET_OFFICIAL_REGISTRY_URL =
  'https://raw.githubusercontent.com/ResearchOS/researchos-plugin-registry/main/registry.json'

/** Workspace settings that steer the market client. */
export const PLUGIN_MARKET_SETTING_KEYS = {
  registryUrl: 'plugins.market.registry_url',
  mirrorPrefix: 'plugins.market.github_mirror_prefix',
  interval: 'plugins.market.auto_update_interval'
} as const

/** Tags the discover page filters on; free-form tags stay searchable. */
export const PLUGIN_MARKET_TAG_LABELS = [
  'literature',
  'notes',
  'experiments',
  'ai',
  'productivity'
] as const

/** The market slice of workspace settings. */
export interface MarketSettings {
  /** Index URL for the official source; `''` means the built-in default. */
  registryUrl: string
  /** GitHub download prefix; `''` disables mirroring. */
  mirrorPrefix: string
  interval: PluginMarketInterval
}

export interface PluginRegistryInfo {
  id: string
  name: string
  url: string
  enabled: boolean
  /** The seeded index repository: cannot be removed or renamed. */
  official: boolean
  fetchedAt: string | null
  /** Cached plugin count, so the source dialog can show a size without parsing. */
  pluginCount: number
}

export interface MarketPluginSummary {
  pluginId: string
  registryId: string
  registryName: string
  name: string
  author: string
  description: string
  tags: string[]
  repo: string
  latestVersion: string
  minApiVersion: number
  verified: boolean
  permissions: PluginPermission[]
  networkDomains: string[]
  updatedAt: string | null
  /** Installed version of the same id, when this plugin is on disk. */
  installedVersion: string | null
  /** `true` while the cached index is stale (offline browsing). */
  updateAvailable: boolean
  requiresApproval: boolean
}

export interface MarketPluginDetail extends MarketPluginSummary {
  downloadUrl: string
  sha256: string
  readmeUrl: string
  /** Fetched README markdown; `null` when absent, offline or over cap. */
  readme: string | null
}

export const PLUGIN_UPDATE_STATES = [
  'updated',
  'pending-approval',
  'available',
  'up-to-date',
  'failed'
] as const
export type PluginUpdateState = (typeof PLUGIN_UPDATE_STATES)[number]

export interface PluginUpdateCheckResult {
  pluginId: string
  name: string
  currentVersion: string
  availableVersion: string | null
  state: PluginUpdateState
  /** Set when the host replaced the plugin without asking. */
  silent: boolean
  /** Scopes the new version needs that were never granted. */
  addedPermissions: PluginPermission[]
  /** Domains the new version needs that were never allowed. */
  addedNetworkDomains: string[]
  error: string | null
}

export interface GithubReleaseAsset {
  name: string
  downloadUrl: string
  size: number
  /** From a `<asset>.sha256` sidecar; `null` when the developer ships none. */
  sha256: string | null
}

export interface GithubReleasePreview {
  repo: string
  tag: string
  /** Release name, empty when the developer left it out. */
  name: string
  body: string
  publishedAt: string | null
  assets: GithubReleaseAsset[]
  /** Non-fatal problems: no `.zip`, several `.zip` artifacts, no sidecar. */
  warnings: string[]
}

/** A background silent update replaced one plugin. */
export interface SilentUpdateInfo {
  pluginId: string
  name: string
  fromVersion: string
  version: string
}

/** One `registry.json` entry, as published by the developer's Action. */
export interface RegistryPluginEntry {
  id: string
  name: string
  author: string
  description: string
  tags: string[]
  repo: string
  latestVersion: string
  minApiVersion: number
  downloadUrl: string
  sha256: string
  permissions: PluginPermission[]
  networkDomains: string[]
  readmeUrl: string
  verified: boolean
  updatedAt: string | null
}

export interface RegistryDocument {
  schemaVersion: number
  updatedAt: string | null
  plugins: RegistryPluginEntry[]
}

/** Extra grant approval carried by market install / update commands. */
export interface MarketGrantInput {
  permissions?: PluginPermission[]
  networkDomains?: string[]
  confirmWriteScopes?: boolean
}

export interface PluginInstallRequest {
  sourcePath: string
  /** Best-effort enable for permission-free plugins; a permission prompt wins. */
  enable?: boolean
}

export interface PluginUpdateRequest extends PluginInstallRequest {
  pluginId: string
}

/** A human-confirmed write-scope grant carries this one-time token. */
export interface PluginGrantConfirmation {
  confirmationToken?: string
}

export interface PluginRpcRequest {
  requestId: string
  method: string
  params: unknown
}

export type PluginRpcResponse =
  | { requestId: string; ok: true; result: unknown }
  | { requestId: string; ok: false; error: AppErrorShape }

/** Default per-call timeout, plan §3.7. */
export const PLUGIN_RPC_TIMEOUT_MS = 15_000

/** Scope selector for `researchos.storage`: absent → plugin-global namespace. */
export interface PluginStorageScopeParams {
  projectId?: string
}

/** One stored key entry returned by `storage.list` (value payload excluded). */
export interface PluginStorageKeyItem {
  key: string
  byteSize: number
  updatedAt: string
}

/** A directory the user explicitly authorized for one plugin. */
export interface PluginAuthorizedDir {
  dirId: string
  displayName: string
  projectId: string | null
}

export interface PluginFileEntry {
  relativePath: string
  kind: 'file' | 'dir'
  size: number
  mtimeMs: number
}

export interface PluginFileStat {
  exists: boolean
  kind?: 'file' | 'dir'
  size?: number
  mtimeMs?: number
  sha256?: string
  isText?: boolean
}

export type PluginWatchChangeEvent = 'add' | 'change' | 'unlink'

export interface PluginWatchChange {
  relativePath: string
  event: PluginWatchChangeEvent
  size?: number
  mtimeMs?: number
  sha256?: string
}

/** Host → plugin `files.changed` payload. */
export interface PluginFilesChangedPayload {
  watchId: string
  dirId: string
  projectId: string | null
  changes: PluginWatchChange[]
}

export interface PluginPromptParams {
  title: string
  message?: string
  placeholder?: string
  defaultValue?: string
  multiline?: boolean
  confirmLabel?: string
  cancelLabel?: string
}

export interface PluginPromptResult {
  submitted: boolean
  value?: string
}

export interface PluginAiSummarizeParams {
  text: string
  instruction?: string
  maxOutputTokens?: number
}

export interface PluginAiSummarizeResult {
  summary: string
  model: string
}

export interface PluginBlobCaptureResult {
  sha256: string
  size: number
  deduplicated: boolean
}

export interface PluginBlobStats {
  objectCount: number
  totalBytes: number
}

export interface PluginAiCompleteParams {
  system?: string
  prompt: string
  jsonMode?: boolean
  maxOutputTokens?: number
}

export interface PluginAiCompleteResult {
  text: string
  model: string
}

export interface PluginUiConfirmParams {
  title: string
  message: string
  confirmLabel?: string
  cancelLabel?: string
  destructive?: boolean
}

/** What `ui.navigate` may ask the shell to open. */
export const PLUGIN_NAVIGATE_TARGETS = [
  'project',
  'notes',
  'literature',
  'ideas',
  'experiments',
  'kanban',
  'weekly',
  'entity'
] as const
export type PluginNavigateTarget = (typeof PLUGIN_NAVIGATE_TARGETS)[number]

export interface PluginUiToastParams {
  message: string
  level?: 'info' | 'success' | 'warning' | 'error'
}

export interface PluginHostContext {
  activeProjectId: string | null
  workspaceName: string
  locale: string
  theme: 'light' | 'dark'
}

/** Hard limits shared by host and docs. */
export const PLUGIN_FILES_LIST_MAX_ENTRIES = 5_000
export const PLUGIN_FILES_LIST_DEFAULT_MAX_DEPTH = 8
export const PLUGIN_FILES_DEFAULT_IGNORES = ['.git', 'node_modules', '*.tmp'] as const
export const PLUGIN_FILES_READ_TEXT_DEFAULT_BYTES = 256 * 1024
export const PLUGIN_FILES_READ_TEXT_MAX_BYTES = 1024 * 1024
export const PLUGIN_WATCH_DEFAULT_DEBOUNCE_MS = 800
export const PLUGIN_WATCH_MIN_DEBOUNCE_MS = 200
export const PLUGIN_WATCH_MAX_DEBOUNCE_MS = 10_000
export const PLUGIN_AI_SUMMARIZE_MAX_TEXT = 100_000
export const PLUGIN_AI_COMPLETE_MAX_PROMPT = 100_000
export const PLUGIN_AI_COMPLETE_MAX_SYSTEM = 10_000
export const PLUGIN_AI_SUMMARIZE_MAX_INSTRUCTION = 1_000
export const PLUGIN_AI_SUMMARIZE_MAX_OUTPUT_TOKENS = 8_192
/** Long-running host calls use their own budget. */
export const PLUGIN_RPC_AI_TIMEOUT_MS = 60_000
export const PLUGIN_RPC_INTERACTIVE_TIMEOUT_MS = 5 * 60_000

/** Methods the host exposes to a plugin's utility process. */
export interface PluginRpcMethods {
  'projects.list': { params: { query?: string }; result: unknown }
  'projects.get': { params: { projectId: string }; result: unknown }
  'notes.list': { params: { projectId: string; query?: string }; result: unknown }
  'notes.get': { params: { projectId: string; noteId: string }; result: unknown }
  'notes.saveBody': {
    params: { projectId: string; noteId: string; markdown: string; baseHash: string }
    result: unknown
  }
  'literature.list': { params: { projectId: string; query?: string }; result: unknown }
  'ideas.list': { params: { projectId: string }; result: unknown }
  'ideas.create': { params: { projectId: string; title: string; description?: string }; result: unknown }
  'experiments.list': { params: { projectId: string }; result: unknown }
  'kanban.tasks.list': { params: { projectId: string }; result: unknown }
  'kanban.tasks.create': { params: { projectId: string; title: string }; result: unknown }
  'weekly.reports.list': { params: { projectId: string }; result: unknown }
  'search.query': { params: Record<string, unknown>; result: unknown }
  'files.read': { params: { relativePath: string }; result: { text: string; bytes: number } }
  'files.pickDirectory': {
    params: { projectId?: string; title?: string }
    result: { cancelled: boolean; dirId?: string; displayName?: string }
  }
  'files.listAuthorizedDirs': {
    params: { projectId?: string }
    result: { dirs: PluginAuthorizedDir[] }
  }
  'files.revokeDirectory': { params: { dirId: string }; result: { removed: boolean } }
  'files.list': {
    params: { dirId: string; relativePath?: string; ignore?: string[]; maxDepth?: number }
    result: { entries: PluginFileEntry[]; truncated: boolean }
  }
  'files.stat': {
    params: { dirId: string; relativePath: string; includeHash?: boolean }
    result: PluginFileStat
  }
  'files.readText': {
    params: { dirId: string; relativePath: string; maxBytes?: number }
    result: { text: string; bytes: number; truncated: boolean }
  }
  'files.watch.start': {
    params: { dirId: string; ignore?: string[]; debounceMs?: number }
    result: { watchId: string }
  }
  'files.watch.stop': { params: { watchId: string }; result: { stopped: boolean } }
  'ai.summarize': { params: PluginAiSummarizeParams; result: PluginAiSummarizeResult }
  'ui.prompt': { params: PluginPromptParams; result: PluginPromptResult }
  'ui.confirm': { params: PluginUiConfirmParams; result: { confirmed: boolean } }
  'ui.toast': { params: PluginUiToastParams; result: void }
  'ui.navigate': {
    params: {
      target: PluginNavigateTarget
      projectId?: string
      entityType?: string
      entityId?: string
    }
    result: { navigated: boolean }
  }
  'host.getContext': { params: Record<string, never>; result: PluginHostContext }
  'ai.complete': { params: PluginAiCompleteParams; result: PluginAiCompleteResult }
  'files.revealInFolder': {
    params: { dirId: string; relativePath?: string }
    result: { ok: boolean }
  }
  'files.open': { params: { dirId: string; relativePath: string }; result: { ok: boolean } }
  'blobs.capture': {
    params: { dirId: string; relativePath: string }
    result: PluginBlobCaptureResult
  }
  'blobs.restore': {
    params: { sha256: string; dirId: string; relativePath: string; overwrite?: boolean }
    result: { restored: boolean; size: number }
  }
  'blobs.delete': { params: { sha256: string }; result: { deleted: boolean } }
  'blobs.stats': { params: Record<string, never>; result: PluginBlobStats }
  'notes.create': {
    params: { projectId: string; kind?: string; title?: string; bodyMd?: string; tags?: string[] }
    result: unknown
  }
  'notes.updateMeta': {
    params: {
      projectId: string
      noteId: string
      patch: { title?: string; description?: string; tags?: string[] }
    }
    result: unknown
  }
  'literature.get': { params: { projectId: string; paperId: string }; result: unknown }
  'ideas.get': { params: { projectId: string; ideaId: string }; result: unknown }
  'ideas.update': {
    params: {
      projectId: string
      ideaId: string
      patch: {
        title?: string
        description?: string
        status?: string
        rejectionReason?: string
      }
    }
    result: unknown
  }
  'experiments.get': { params: { projectId: string; experimentId: string }; result: unknown }
  'experiments.runs.list': { params: { projectId: string; experimentId: string }; result: unknown }
  'kanban.tasks.update': {
    params: {
      projectId: string
      id: string
      patch: {
        title?: string
        descriptionMd?: string
        priority?: string
        deadline?: string | null
        weekPlan?: string | null
        blocked?: boolean
        column?: string
      }
    }
    result: unknown
  }
  'relations.list': {
    params: { projectId: string; entityType: string; entityId: string; direction?: 'from' | 'to' }
    result: unknown
  }
  'relations.add': {
    params: {
      projectId: string
      srcType: string
      srcId: string
      relation: string
      dstType: string
      dstId: string
    }
    result: { relationId: string }
  }
  'relations.remove': {
    params: {
      projectId: string
      srcType: string
      srcId: string
      relation: string
      dstType: string
      dstId: string
    }
    result: { removed: boolean }
  }
  'databases.list': { params: { projectId: string }; result: unknown }
  'databases.get': { params: { projectId: string; databaseId: string }; result: unknown }
  'databases.create': {
    params: {
      projectId: string
      name: string
      description?: string
      fields?: Array<{ name: string; type: string; config?: unknown; options?: unknown }>
    }
    result: unknown
  }
  'records.list': {
    params: {
      projectId: string
      databaseId: string
      q?: string
      limit?: number
      offset?: number
    }
    result: unknown
  }
  'records.get': { params: { projectId: string; recordId: string }; result: unknown }
  'records.create': {
    params: { projectId: string; databaseId: string; values?: Record<string, unknown>; title?: string }
    result: unknown
  }
  'records.update': {
    params: { projectId: string; recordId: string; values?: Record<string, unknown>; title?: string }
    result: unknown
  }
  'records.delete': {
    params: { projectId: string; recordId: string; deleteNote?: boolean }
    result: { deleted: boolean }
  }
  'evidence.list': {
    params: { projectId: string; ideaId?: string; direction?: 'support' | 'oppose' }
    result: unknown
  }
  'evidence.create': {
    params: {
      projectId: string
      ideaId: string
      direction: 'support' | 'oppose'
      content: string
      sourceType?: string
      sourceId?: string
    }
    result: unknown
  }
  'activity.list': {
    params: { projectId: string; limit?: number; eventTypes?: string[]; entityType?: string }
    result: unknown
  }
  'network.fetch': {
    params: { url: string; method?: string; headers?: Record<string, string> }
    result: { status: number; headers: Record<string, string>; body: string }
  }
  'notifications.show': { params: { title: string; body: string }; result: void }
  'clipboard.write': { params: { text: string }; result: void }
  /** Host-mediated insertion; never a direct editor mutation. */
  'editor.insertText': { params: { text: string }; result: void }
  /** Plugin-private KV read. `pluginId` is host-injected, never a param. */
  'storage.get': {
    params: { key: string; projectId?: string }
    result: { found: boolean; value?: unknown; updatedAt?: string }
  }
  'storage.set': {
    params: { key: string; value: unknown; projectId?: string }
    result: { byteSize: number; updatedAt: string }
  }
  'storage.delete': { params: { key: string; projectId?: string }; result: { deleted: boolean } }
  'storage.list': {
    params: { prefix?: string; projectId?: string; limit?: number; cursor?: string }
    result: { keys: PluginStorageKeyItem[]; nextCursor?: string }
  }
  'storage.clear': { params: PluginStorageScopeParams; result: { deletedCount: number } }
}

export type PluginRpcMethod = keyof PluginRpcMethods

/** Runtime list for bridge validation; the union above is the compile contract. */
export const PLUGIN_RPC_METHOD_NAMES = [
  'projects.list',
  'projects.get',
  'notes.list',
  'notes.get',
  'notes.saveBody',
  'literature.list',
  'ideas.list',
  'ideas.create',
  'experiments.list',
  'kanban.tasks.list',
  'kanban.tasks.create',
  'weekly.reports.list',
  'search.query',
  'files.read',
  'files.pickDirectory',
  'files.listAuthorizedDirs',
  'files.revokeDirectory',
  'files.list',
  'files.stat',
  'files.readText',
  'files.watch.start',
  'files.watch.stop',
  'ai.summarize',
  'ai.complete',
  'ui.prompt',
  'ui.confirm',
  'ui.toast',
  'ui.navigate',
  'host.getContext',
  'files.revealInFolder',
  'files.open',
  'blobs.capture',
  'blobs.restore',
  'blobs.delete',
  'blobs.stats',
  'notes.create',
  'notes.updateMeta',
  'literature.get',
  'ideas.get',
  'ideas.update',
  'experiments.get',
  'experiments.runs.list',
  'kanban.tasks.update',
  'relations.list',
  'relations.add',
  'relations.remove',
  'databases.list',
  'databases.get',
  'databases.create',
  'records.list',
  'records.get',
  'records.create',
  'records.update',
  'records.delete',
  'evidence.list',
  'evidence.create',
  'activity.list',
  'network.fetch',
  'notifications.show',
  'clipboard.write',
  'editor.insertText',
  'storage.get',
  'storage.set',
  'storage.delete',
  'storage.list',
  'storage.clear'
] as const satisfies readonly PluginRpcMethod[]

export function isPluginRpcMethod(value: unknown): value is PluginRpcMethod {
  return typeof value === 'string' && (PLUGIN_RPC_METHOD_NAMES as readonly string[]).includes(value)
}

/** Typed facade the plugin utility process sees as `researchos.pluginBridge`. */
export interface PluginBridge {
  request<M extends PluginRpcMethod>(
    method: M,
    params: PluginRpcMethods[M]['params']
  ): Promise<PluginRpcMethods[M]['result']>
  onEvent(handler: (event: PluginHostEvent) => void): () => void
  resize(width: number, height: number): void
}

export interface PluginHostEvent {
  type: string
  payload?: unknown
}

/* ----------------------------------------------------- declarative contributions */

export const PLUGIN_SKILL_MAX_BYTES = 8_000
export const PLUGIN_MAX_DECLARATIVE_FILES = 100

export interface PluginDeclarativeSkill {
  /** `plugin:<plugin-id>:<file>`, exactly as Weekly Review stores it. */
  id: string
  name: string
  prompt: string
  source: 'plugin'
  builtin: false
}

export const PLUGIN_RULE_KINDS = ['json_numeric_leaves', 'csv_column_map'] as const
export type PluginRuleKind = (typeof PLUGIN_RULE_KINDS)[number]

export interface PluginRule {
  /** Gitignore-style glob, matched against the scan-relative path. */
  file: string
  kind: PluginRuleKind
  /** Prefix prepended to every extracted metric key. */
  prefix?: string
  /** `csv_column_map`: metric key -> CSV header name. */
  columns?: Record<string, string>
}

/* --------------------------------------------------------------- renderer slots */

/** Declarative slot vocabulary rendered by the host renderer. */
export type PluginSlotKind = 'text' | 'markdown' | 'list' | 'table' | 'form' | 'button' | 'chart' | 'iframe'

export interface PluginSlotField {
  name: string
  label: string
  type?: 'text' | 'number' | 'textarea' | 'checkbox'
  value?: string | number | boolean
  placeholder?: string
}

export interface PluginSlotChartPoint {
  label: string
  value: number
}

export interface PluginSlot {
  kind: PluginSlotKind
  id?: string
  title?: string
  body?: string
  items?: string[]
  columns?: string[]
  rows?: string[][]
  fields?: PluginSlotField[]
  submitCommand?: string
  command?: string
  url?: string
  points?: PluginSlotChartPoint[]
  height?: number
}

/** Frame → host messages; anything else is rejected by the bridge. */
export const PLUGIN_BRIDGE_MESSAGE_TYPES = ['request', 'resize', 'event'] as const

export interface PluginBridgeRequestMessage {
  type: 'request'
  requestId: string
  method: string
  params: unknown
}

export interface PluginBridgeResizeMessage {
  type: 'resize'
  width: number
  height: number
}

export interface PluginEvent {
  type: 'plugin.changed' | 'plugin.error' | 'plugin.command' | 'plugin.event'
  pluginId: string
  payload?: unknown
}

/** Read-only values the iframe receives at init. */
export interface PluginFrameTheme {
  mode: 'light' | 'dark'
  tokens: Record<string, string>
}

export interface PluginFrameInit {
  type: 'plugin:init'
  pluginId: string
  locale: string
  theme: PluginFrameTheme
}