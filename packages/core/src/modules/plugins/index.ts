/**
 * Plugin subsystem (plan §3.7/§4.12): manifest validation, staged install,
 * registry, permissions, RPC routing and declarative skills/rules.
 */

export {
  PluginService,
  type EnableOptions,
  type InstallOptions,
  type PluginRuntime,
  type PluginRuntimeStart,
  type PluginServiceOptions,
  type PluginWorkspace
} from './service'
export {
  PluginRpcRouter,
  PLUGIN_RPC_METHODS,
  validateRpcResponse,
  type NetworkResponse,
  type PluginAiHost,
  type PluginAuditEntry,
  type PluginFetch,
  type PluginRpcContext,
  type PluginRpcHandler,
  type PluginRpcRouterOptions
} from './rpc'
export { PluginCommandRegistry } from './commands'
export {
  PluginFilesService,
  normalizeRelativePath,
  PLUGIN_OPEN_DENY_EXTENSIONS,
  type PluginHostOperations,
  type PluginWatchGateway,
  type PluginWatchStartRequest
} from './files'
export { PluginBlobStore, type PluginBlobStoreOptions } from './blobs'
export {
  PluginPromptBroker,
  type PluginPromptKind,
  type PluginPromptPresenter,
  type PluginPromptRequest
} from './prompt'
export {
  PluginStorageStore,
  pluginStorageScopeKey,
  PLUGIN_STORAGE_KEY_MAX_LENGTH,
  PLUGIN_STORAGE_LIST_DEFAULT_LIMIT,
  PLUGIN_STORAGE_LIST_MAX_LIMIT,
  PLUGIN_STORAGE_MAX_KEYS,
  PLUGIN_STORAGE_MAX_TOTAL_BYTES,
  PLUGIN_STORAGE_MAX_VALUE_BYTES,
  type PluginStorageGetResult,
  type PluginStorageListResult,
  type PluginStorageSetResult
} from './storage'
export {
  importDeclarative,
  loadPluginRules,
  loadPluginSkills,
  type DeclarativeImportResult,
  type DeclarativeSinks,
  type PluginRuleSink,
  type PluginSkillSink
} from './declarative'
export {
  isHostAllowed,
  missingPermissions,
  normalizeGranted,
  normalizeNetworkDomains,
  scopesSatisfied
} from './permissions'
export {
  MANIFEST_FILE,
  MAX_CONTRIBUTIONS,
  MAX_MANIFEST_BYTES,
  MAX_PLUGIN_BYTES,
  MAX_PLUGIN_FILES,
  PLUGIN_ID_PATTERN,
  isSafePluginId,
  normalizedContributions,
  parseManifestDocument,
  validateManifestSemantics
} from './manifest'
export { PluginStore, toPluginInfo, type PluginRecord, type PluginSourceInfo } from './store'
export { PluginRegistryStore, registryIdOf, type RegistryRow } from './registry-store'
export {
  PluginMarketService,
  repoFromUrl,
  type MarketGithubInstallInput,
  type MarketInstallInput,
  type MarketSearchCriteria,
  type MarketUpdateInput,
  type PluginMarketServiceOptions,
  type PluginServiceLike
} from './market/service'
export {
  PluginUpdateScheduler,
  type PluginUpdateSchedulerOptions,
  type SchedulerTimers,
  type UpdateChecker
} from './market/scheduler'
export {
  MarketHttp,
  fetchMarketTransport,
  type FetchedResource,
  type MarketHttpOptions,
  type MarketRawResponse,
  type MarketRequest,
  type MarketTransport
} from './market/http'
export { GithubReleaseClient, normalizeRepo, type ResolvedRelease } from './market/github'
export {
  assertRemoteUrl,
  isCompatible,
  parseRegistryDocument,
  type ParsedRegistry
} from './market/registry'
export {
  compareSemver,
  isSemver,
  parseSemver,
  semverGt,
  versionFromTag,
  type Semver
} from './market/semver'
export {
  createDebouncer,
  HOT_RELOAD_DEBOUNCE_MS,
  PluginHotReloader,
  type Debouncer,
  type HotReloaderOptions
} from './watcher'
export { extractPluginZip, materializePluginSource, copyPluginFolder, isInside, safeRelativeEntry } from './source'
export { PluginErrorCode } from './types'