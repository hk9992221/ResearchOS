import { createHash } from 'node:crypto'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import * as path from 'node:path'

import type {
  GithubReleasePreview,
  MarketPluginDetail,
  MarketPluginSummary,
  MarketSettings,
  PluginInfo,
  PluginManifest,
  PluginMarketInterval,
  PluginPermission,
  PluginRegistryInfo,
  PluginUpdateCheckResult,
  RegistryDocument,
  RegistryPluginEntry,
  SilentUpdateInfo
} from '@researchos/shared'
import {
  PLUGIN_API_VERSION,
  PLUGIN_MARKET_DEFAULT_INTERVAL,
  PLUGIN_MARKET_INTERVALS,
  PLUGIN_MARKET_INTERVAL_MS,
  PLUGIN_MARKET_MAX_DOWNLOAD_BYTES,
  PLUGIN_MARKET_MAX_README_BYTES,
  PLUGIN_MARKET_MAX_REGISTRY_BYTES,
  PLUGIN_MARKET_OFFICIAL_REGISTRY_ID,
  PLUGIN_MARKET_OFFICIAL_REGISTRY_URL,
  PLUGIN_MARKET_SETTING_KEYS
} from '@researchos/shared'

import type { Clock } from '../../../clock'
import { SystemClock, formatTimestamp } from '../../../clock'
import { AppErrorCode, appError } from '../../../errors'
import type { Logger } from '../../../logger'
import type { SettingsStore } from '../../../settings'
import { PluginRegistryStore, type RegistryRow } from '../registry-store'
import type { EnableOptions, InstallOptions, PluginWorkspace, UpdateOptions } from '../service'
import { extractPluginZip } from '../source'
import { GithubReleaseClient, normalizeRepo } from './github'
import { MarketHttp, type MarketTransport } from './http'
import { assertRemoteUrl, isCompatible, parseRegistryDocument } from './registry'
import { semverGt } from './semver'

/**
 * The plugin market client. Two layers of trust are enforced here:
 * an index document is validated before it is cached, and every artifact is
 * downloaded into `.staging`, size-capped and SHA-256-checked before the
 * existing install/update path sees it. Nothing in this file mutates a plugin
 * folder directly — lifecycle stays in `PluginService`.
 */

export interface MarketSearchCriteria {
  query?: string
  tags?: string[]
  verifiedOnly?: boolean
  registryId?: string
}

export interface MarketInstallInput extends EnableOptions {
  pluginId: string
  registryId?: string
}

export interface MarketGithubInstallInput extends EnableOptions {
  repo: string
  tag?: string
  /** Required when the release ships no checksum sidecar. */
  confirmUnverified?: boolean
}

export interface MarketUpdateInput extends EnableOptions {
  pluginId: string
}

export interface PluginMarketServiceOptions {
  workspace: PluginWorkspace
  service: PluginServiceLike
  settings: SettingsStore
  clock?: Clock
  logger?: Logger
  transport?: MarketTransport
  http?: MarketHttp
  github?: GithubReleaseClient
  /** Renderer push for a background silent update. */
  onSilentUpdate?(update: SilentUpdateInfo): void
}

/** The slice of `PluginService` the market needs, kept narrow for tests. */
export interface PluginServiceLike {
  list(): PluginInfo[]
  inspect(sourcePath: string): { manifest: PluginManifest }
  install(sourcePath: string, options: InstallOptions): Promise<PluginInfo>
  update(pluginId: string, sourcePath: string, options?: UpdateOptions): Promise<PluginInfo>
  setAutoUpdate(pluginId: string, enabled: boolean): PluginInfo
  recordUpdateCheck(
    pluginId: string,
    result: {
      availableVersion?: string | null
      updateRequiresApproval?: boolean
      lastCheckedAt?: string | null
      lastError?: string | null
    }
  ): void
}

interface StagedArtifact {
  /** Directory holding the extracted, manifest-validated package. */
  dir: string
  digest: string
}

/** A remote candidate for one installed plugin, before its bytes are fetched. */
interface Candidate {
  version: string | null
  downloadUrl: string
  sha256: string | null
  /** Domains the published entry says the plugin needs. */
  networkDomains: string[]
  minApiVersion: number | null
  origin: string
}

let labelCounter = 0

function marketError(
  message: string,
  code: AppErrorCode = AppErrorCode.ExternalUnavailable,
  details: Record<string, unknown> = {}
): never {
  throw appError(code, message, { reason: 'market', ...details })
}

function digestOf(bytes: Buffer): string {
  return createHash('sha256').update(bytes).digest('hex')
}

/** `owner/repo` out of a release asset URL; empty when it is not GitHub. */
export function repoFromUrl(url: string): string {
  try {
    const parts = new URL(url).pathname.split('/').filter((part) => part !== '')
    if (parts.length < 2) return ''
    return `${parts[0]}/${parts[1]}`
  } catch {
    return ''
  }
}

export class PluginMarketService {
  private readonly service: PluginServiceLike
  private readonly settings: SettingsStore
  private readonly clock: Clock
  private readonly logger: Logger | undefined
  private readonly http: MarketHttp
  private readonly github: GithubReleaseClient
  private readonly registryStore: PluginRegistryStore
  private readonly stagingRoot: string
  private readonly onSilentUpdate: ((update: SilentUpdateInfo) => void) | undefined
  /** Parsed index documents by registry id; the search path never hits the network. */
  private readonly documents = new Map<string, RegistryDocument>()
  private cacheLoaded = false

  constructor(options: PluginMarketServiceOptions) {
    this.service = options.service
    this.settings = options.settings
    this.clock = options.clock ?? new SystemClock()
    this.logger = options.logger
    this.http =
      options.http ??
      new MarketHttp({ transport: options.transport, settings: options.settings, logger: options.logger })
    this.github = new GithubReleaseClient(this.http)
    this.registryStore = new PluginRegistryStore(options.workspace.global)
    this.stagingRoot = path.join(options.workspace.root, 'plugins', '.staging')
    this.onSilentUpdate = options.onSilentUpdate
  }

  /* ------------------------------------------------------------------ index */

  /**
   * Refreshes every enabled source with a conditional GET. A `304` keeps the
   * cached document and costs no parse; a failed source keeps its previous
   * cache so browsing continues offline.
   */
  async refresh(input: { force?: boolean } = {}): Promise<{
    updatedRegistries: number
    totalPlugins: number
  }> {
    this.ensureCacheLoaded()
    const force = input.force === true
    let updated = 0
    for (const row of this.registryStore.enabled()) {
      const url = this.effectiveUrl(row)
      try {
        const resource = await this.http.get(assertRemoteUrl(url, 'registryUrl'), {
          maxBytes: PLUGIN_MARKET_MAX_REGISTRY_BYTES,
          etag: force ? '' : (row.etag ?? ''),
          accept: 'application/json'
        })
        if (resource.notModified) continue
        const parsed = parseRegistryDocument(JSON.parse(resource.body.toString('utf8')) as unknown)
        this.registryStore.setCache(
          row.id,
          JSON.stringify(parsed.document),
          resource.etag,
          this.clock.now()
        )
        this.documents.set(row.id, parsed.document)
        updated += 1
        for (const warning of parsed.warnings) {
          this.logger?.warn('registry entry skipped', { registryId: row.id, warning })
        }
      } catch (error) {
        // One unreachable source must not break the others.
        this.logger?.warn('registry refresh failed', {
          registryId: row.id,
          error: messageOf(error)
        })
      }
    }
    return { updatedRegistries: updated, totalPlugins: this.cachedPluginCount() }
  }

  /**
   * Local search over the cached documents. Every filter is a
   * plain in-memory predicate, so this stays millisecond-fast and works with
   * the network unplugged.
   */
  search(criteria: MarketSearchCriteria = {}): MarketPluginSummary[] {
    this.ensureCacheLoaded()
    const rows = this.registryStore.enabled()
    const wantedTags = (criteria.tags ?? []).map((tag) => tag.trim().toLowerCase()).filter((tag) => tag !== '')
    const tokens = (criteria.query ?? '')
      .toLowerCase()
      .split(/\s+/)
      .filter((token) => token !== '')
    const installed = new Map(this.service.list().map((info) => [info.id, info]))
    const out: MarketPluginSummary[] = []

    for (const row of rows) {
      if (criteria.registryId !== undefined && criteria.registryId !== row.id) continue
      const document = this.documents.get(row.id)
      if (document === undefined) continue
      for (const entry of document.plugins) {
        if (criteria.verifiedOnly === true && !entry.verified) continue
        if (wantedTags.length > 0 && !wantedTags.every((tag) => entry.tags.includes(tag))) continue
        if (tokens.length > 0 && !matchesTokens(entry, tokens)) continue
        out.push(toSummary(entry, row, installed.get(entry.id) ?? null))
      }
    }
    out.sort((a, b) => a.name.localeCompare(b.name) || a.pluginId.localeCompare(b.pluginId))
    return out
  }

  /** Detail view: the cached entry plus its remote README. */
  async detail(pluginId: string, registryId?: string): Promise<MarketPluginDetail> {
    const located = await this.locateEntry(pluginId, registryId)
    const installed = this.service.list().find((info) => info.id === pluginId) ?? null
    let readme: string | null = null
    if (located.entry.readmeUrl !== '') {
      try {
        const resource = await this.http.text(located.entry.readmeUrl, {
          maxBytes: PLUGIN_MARKET_MAX_README_BYTES
        })
        readme = resource.notModified ? null : resource.body.toString('utf8')
      } catch (error) {
        this.logger?.warn('market readme fetch failed', {
          pluginId,
          error: messageOf(error)
        })
      }
    }
    return {
      ...toSummary(located.entry, located.row, installed),
      downloadUrl: located.entry.downloadUrl,
      sha256: located.entry.sha256,
      readmeUrl: located.entry.readmeUrl,
      readme
    }
  }

  /* ---------------------------------------------------------------- install */

  /** Downloads, verifies against the published checksum and installs. */
  async install(input: MarketInstallInput): Promise<PluginInfo> {
    const located = await this.locateEntry(input.pluginId, input.registryId)
    const entry = located.entry
    if (this.service.list().some((info) => info.id === entry.id)) {
      marketError(
        `plugin ${entry.id} is already installed at version ${entry.latestVersion}`,
        AppErrorCode.Conflict,
        { pluginId: entry.id }
      )
    }
    this.assertCompatible(entry)
    const staged = await this.stageArtifact({
      url: entry.downloadUrl,
      sha256: entry.sha256,
      label: labelFor(entry.id, entry.latestVersion),
      pluginId: entry.id
    })
    try {
      this.assertArtifactMatches(entry, this.service.inspect(staged.dir).manifest)
      return await this.service.install(staged.dir, {
        source: { sourceType: 'registry', sourceUrl: entry.downloadUrl, registryId: located.row.id },
        permissions: input.permissions,
        networkDomains: input.networkDomains,
        confirmWriteScopes: input.confirmWriteScopes,
        confirmationToken: input.confirmationToken
      })
    } finally {
      rmSync(staged.dir, { recursive: true, force: true })
    }
  }

  /** Release metadata for the direct-install dialog. */
  async previewGithub(repo: string, tag?: string): Promise<GithubReleasePreview> {
    return await this.github.preview(normalizeRepo(repo), tag)
  }

  async installFromGithub(input: MarketGithubInstallInput): Promise<PluginInfo> {
    const release = await this.github.resolve(input.repo, input.tag)
    if (release.asset.sha256 === null && input.confirmUnverified !== true) {
      throw appError(
        AppErrorCode.UserConfirmationRequired,
        `release ${release.repo}${release.tag === '' ? '' : ` ${release.tag}`} publishes no checksum; confirm the artifact before installing`,
        {
          reason: 'sha256_unverified',
          repo: release.repo,
          tag: release.tag,
          asset: release.asset.name,
          size: release.asset.size,
          warnings: release.warnings
        }
      )
    }
    const staged = await this.stageArtifact({
      url: release.asset.downloadUrl,
      sha256: release.asset.sha256,
      label: labelFor(release.asset.name.replace(/\.zip$/i, ''), release.tag || 'latest'),
      pluginId: release.repo
    })
    try {
      const manifest = this.service.inspect(staged.dir).manifest
      return await this.service.install(staged.dir, {
        source: { sourceType: 'github', sourceUrl: release.asset.downloadUrl, registryId: '' },
        permissions: input.permissions,
        networkDomains: input.networkDomains,
        confirmWriteScopes: input.confirmWriteScopes,
        confirmationToken: input.confirmationToken
      })
    } finally {
      rmSync(staged.dir, { recursive: true, force: true })
    }
  }

  /* ---------------------------------------------------------------- updates */

  /**
   * Checks every installed remote plugin and applies the safe subset of the
   * updates. Local installs are never fetched, disabled or
   * uninstalled plugins are skipped, and one failing plugin cannot stop the
   * rest: each result is reported independently.
   */
  async checkUpdates(): Promise<PluginUpdateCheckResult[]> {
    const targets = this.service.list().filter((info) => info.sourceType !== 'local' && info.enabled)
    if (targets.length === 0) return []
    if (targets.some((info) => info.sourceType === 'registry')) {
      await this.refresh()
    }
    const results: PluginUpdateCheckResult[] = []
    for (const target of targets) {
      try {
        results.push(await this.checkOne(target))
      } catch (error) {
        const message = messageOf(error)
        this.logger?.warn('plugin update check failed', { pluginId: target.id, error: message })
        this.service.recordUpdateCheck(target.id, {
          lastCheckedAt: this.stamp(),
          lastError: message
        })
        results.push({
          pluginId: target.id,
          name: target.name,
          currentVersion: target.version,
          availableVersion: target.availableVersion,
          state: 'failed',
          silent: false,
          addedPermissions: [],
          addedNetworkDomains: [],
          error: message
        })
      }
    }
    return results
  }

  /** Manual update, or the confirmed update after a permission escalation. */
  async update(input: MarketUpdateInput): Promise<PluginInfo> {
    const target = this.service.list().find((info) => info.id === input.pluginId)
    if (target === undefined) {
      marketError(`plugin ${input.pluginId} is not installed`, AppErrorCode.NotFound, {
        pluginId: input.pluginId
      })
    }
    const candidate = await this.candidateFor(target)
    const staged = await this.stageArtifact({
      url: candidate.downloadUrl,
      sha256: candidate.sha256,
      label: labelFor(target.id, candidate.version ?? 'latest'),
      pluginId: target.id
    })
    try {
      const manifest = this.service.inspect(staged.dir).manifest
      if (!semverGt(manifest.version, target.version)) {
        marketError(
          `plugin ${target.id} is already at ${manifest.version}`,
          AppErrorCode.Conflict,
          { pluginId: target.id, version: manifest.version }
        )
      }
      const escalation = escalationOf(manifest, candidate, target)
      if (escalation.permissions.length > 0 && input.permissions === undefined) {
        throw appError(
          AppErrorCode.UserConfirmationRequired,
          `update of ${target.id} needs new permissions: ${escalation.permissions.join(', ')}`,
          {
            pluginId: target.id,
            version: manifest.version,
            reason: 'permission_escalation',
            addedPermissions: escalation.permissions,
            addedNetworkDomains: escalation.domains
          }
        )
      }
      return await this.service.update(target.id, staged.dir, {
        permissions: input.permissions,
        networkDomains: input.networkDomains,
        confirmWriteScopes: input.confirmWriteScopes,
        confirmationToken: input.confirmationToken
      })
    } finally {
      rmSync(staged.dir, { recursive: true, force: true })
    }
  }

  setAutoUpdate(pluginId: string, enabled: boolean): PluginInfo {
    const info = this.service.list().find((candidate) => candidate.id === pluginId)
    if (info === undefined) {
      marketError(`plugin ${pluginId} is not installed`, AppErrorCode.NotFound, { pluginId })
    }
    if (info.sourceType === 'local') {
      marketError(
        'a locally installed plugin has no update source to poll',
        AppErrorCode.InvalidArgument,
        { pluginId, sourceType: info.sourceType }
      )
    }
    return this.service.setAutoUpdate(pluginId, enabled)
  }

  /* -------------------------------------------------------------- registries */

  registries(): PluginRegistryInfo[] {
    this.ensureCacheLoaded()
    const rows = [...this.registryStore.list()].sort(
      (a, b) =>
        Number(b.id === PLUGIN_MARKET_OFFICIAL_REGISTRY_ID) -
          Number(a.id === PLUGIN_MARKET_OFFICIAL_REGISTRY_ID) ||
        a.name.localeCompare(b.name)
    )
    return rows.map((row) => ({
      id: row.id,
      name: row.name,
      url: this.effectiveUrl(row),
      enabled: row.enabled,
      official: row.id === PLUGIN_MARKET_OFFICIAL_REGISTRY_ID,
      fetchedAt: row.fetchedAt,
      pluginCount: this.documents.get(row.id)?.plugins.length ?? 0
    }))
  }

  /** Proves the source before storing it, so a bad URL never appears in the list. */
  async addRegistry(name: string, url: string): Promise<PluginRegistryInfo> {
    const trimmed = name.trim()
    if (trimmed === '' || trimmed.length > 120) {
      marketError('registry name must be 1–120 characters', AppErrorCode.InvalidArgument, { field: 'name' })
    }
    const target = assertRemoteUrl(url.trim(), 'url')
    if (this.registryStore.list().some((row) => row.url === target)) {
      marketError(`registry ${target} is already configured`, AppErrorCode.Conflict, { url: target })
    }
    const resource = await this.http.get(target, {
      maxBytes: PLUGIN_MARKET_MAX_REGISTRY_BYTES,
      accept: 'application/json'
    })
    const parsed = parseRegistryDocument(JSON.parse(resource.body.toString('utf8')) as unknown)
    const row = this.registryStore.insert(trimmed, target)
    this.registryStore.setCache(row.id, JSON.stringify(parsed.document), resource.etag, this.clock.now())
    this.documents.set(row.id, parsed.document)
    this.logger?.info('registry added', { registryId: row.id, url: target, plugins: parsed.document.plugins.length })
    return {
      id: row.id,
      name: row.name,
      url: row.url,
      enabled: row.enabled,
      official: false,
      fetchedAt: formatTimestamp(this.clock.now()),
      pluginCount: parsed.document.plugins.length
    }
  }

  removeRegistry(id: string): { removed: boolean } {
    const removed = this.registryStore.remove(id)
    if (!removed) {
      marketError(`registry ${id} is not configured`, AppErrorCode.NotFound, { id })
    }
    this.documents.delete(id)
    return { removed }
  }

  /* ---------------------------------------------------------------- settings */

  /** Poll delay for the scheduler; `null` when background updates are off. */
  pollIntervalMs(): number | null {
    const interval = this.interval()
    return interval === 'off' ? null : PLUGIN_MARKET_INTERVAL_MS[interval]
  }

  marketSettings(): MarketSettings {
    return {
      registryUrl: this.effectiveOfficialUrl(),
      mirrorPrefix: (this.settings.get(PLUGIN_MARKET_SETTING_KEYS.mirrorPrefix) ?? '').trim(),
      interval: this.interval()
    }
  }

  /**
   * Writes the market settings. A changed index URL drops the official cache,
   * otherwise the stale document would keep answering searches.
   */
  updateMarketSettings(input: Partial<MarketSettings>): MarketSettings {
    if (input.registryUrl !== undefined) {
      const url = input.registryUrl.trim()
      const value = url === '' ? '' : assertRemoteUrl(url, 'registryUrl')
      if (value === '') {
        this.settings.delete(PLUGIN_MARKET_SETTING_KEYS.registryUrl)
      } else {
        this.settings.set(PLUGIN_MARKET_SETTING_KEYS.registryUrl, value)
      }
      this.registryStore.invalidate(PLUGIN_MARKET_OFFICIAL_REGISTRY_ID)
      this.documents.delete(PLUGIN_MARKET_OFFICIAL_REGISTRY_ID)
    }
    if (input.mirrorPrefix !== undefined) {
      const prefix = input.mirrorPrefix.trim()
      if (prefix === '') {
        this.settings.delete(PLUGIN_MARKET_SETTING_KEYS.mirrorPrefix)
      } else {
        this.settings.set(PLUGIN_MARKET_SETTING_KEYS.mirrorPrefix, assertRemoteUrl(prefix, 'mirrorPrefix'))
      }
    }
    if (input.interval !== undefined) {
      if (!(PLUGIN_MARKET_INTERVALS as readonly string[]).includes(input.interval)) {
        marketError(`unknown update interval ${input.interval}`, AppErrorCode.InvalidArgument, {
          field: 'interval'
        })
      }
      this.settings.set(PLUGIN_MARKET_SETTING_KEYS.interval, input.interval)
    }
    return this.marketSettings()
  }

  /* ---------------------------------------------------------------- internals */

  private stamp(): string {
    return formatTimestamp(this.clock.now())
  }

  private interval(): PluginMarketInterval {
    const raw = this.settings.get(PLUGIN_MARKET_SETTING_KEYS.interval)
    return (PLUGIN_MARKET_INTERVALS as readonly string[]).includes(raw ?? '')
      ? (raw as PluginMarketInterval)
      : PLUGIN_MARKET_DEFAULT_INTERVAL
  }

  private effectiveOfficialUrl(): string {
    const override = (this.settings.get(PLUGIN_MARKET_SETTING_KEYS.registryUrl) ?? '').trim()
    if (override !== '') return override
    return this.registryStore.get(PLUGIN_MARKET_OFFICIAL_REGISTRY_ID)?.url ?? PLUGIN_MARKET_OFFICIAL_REGISTRY_URL
  }

  private effectiveUrl(row: { id: string; url: string }): string {
    return row.id === PLUGIN_MARKET_OFFICIAL_REGISTRY_ID ? this.effectiveOfficialUrl() : row.url
  }

  private ensureCacheLoaded(): void {
    if (this.cacheLoaded) return
    this.cacheLoaded = true
    for (const row of this.registryStore.list()) {
      if (row.cachedJson === null || row.cachedJson === '') continue
      try {
        this.documents.set(
          row.id,
          parseRegistryDocument(JSON.parse(row.cachedJson) as unknown).document
        )
      } catch (error) {
        this.logger?.warn('cached registry document rejected', {
          registryId: row.id,
          error: messageOf(error)
        })
        this.registryStore.invalidate(row.id)
      }
    }
  }

  private cachedPluginCount(): number {
    let total = 0
    for (const row of this.registryStore.enabled()) {
      total += this.documents.get(row.id)?.plugins.length ?? 0
    }
    return total
  }

  /**
   * Finds an entry in the cached index, refreshing once when it is missing:
   * adding a source or restarting the app should not require an explicit
   * "refresh" before the first install works.
   */
  private async locateEntry(
    pluginId: string,
    registryId?: string
  ): Promise<{ entry: RegistryPluginEntry; row: RegistryRow }> {
    const found = this.tryLocate(pluginId, registryId)
    if (found !== null) return found
    await this.refresh({})
    const afterRefresh = this.tryLocate(pluginId, registryId)
    if (afterRefresh !== null) return afterRefresh
    marketError(
      registryId === undefined
        ? `plugin ${pluginId} is not listed in any enabled registry`
        : `plugin ${pluginId} is not listed in registry ${registryId}`,
      AppErrorCode.NotFound,
      { pluginId, registryId }
    )
  }

  private tryLocate(pluginId: string, registryId?: string): { entry: RegistryPluginEntry; row: RegistryRow } | null {
    this.ensureCacheLoaded()
    const rows = this.registryStore.enabled().filter((row) => registryId === undefined || row.id === registryId)
    for (const row of rows) {
      const entry = this.documents.get(row.id)?.plugins.find((plugin) => plugin.id === pluginId)
      if (entry !== undefined) return { entry, row }
    }
    return null
  }

  private assertCompatible(entry: RegistryPluginEntry): void {
    if (!isCompatible(entry)) {
      marketError(
        `plugin ${entry.id} needs plugin API v${String(entry.minApiVersion)}; this host is v${String(PLUGIN_API_VERSION)}`,
        AppErrorCode.Unsupported,
        { pluginId: entry.id, minApiVersion: entry.minApiVersion, apiVersion: PLUGIN_API_VERSION }
      )
    }
  }

  /**
   * Downloads into `.staging`, caps the size and compares the digest before a
   * single byte reaches the plugin folder.
   */
  private async stageArtifact(input: {
    url: string
    sha256: string | null
    label: string
    pluginId: string
  }): Promise<StagedArtifact> {
    const resource = await this.http.bytes(input.url, { maxBytes: PLUGIN_MARKET_MAX_DOWNLOAD_BYTES })
    const digest = digestOf(resource.body)
    if (input.sha256 !== null && input.sha256 !== '' && digest !== input.sha256) {
      // Nothing was written yet, so `plugins/` cannot be polluted by a
      // Truncated or swapped artifact.  names this VALIDATION_ERROR;
      // the closed application taxonomy expresses it as INTEGRITY plus the
      // `sha256_mismatch` reason, which is what callers branch on.
      throw appError(
        AppErrorCode.Integrity,
        `downloaded artifact for ${input.pluginId} does not match the published SHA-256`,
        {
          reason: 'sha256_mismatch',
          pluginId: input.pluginId,
          expected: input.sha256,
          actual: digest
        }
      )
    }
    mkdirSync(this.stagingRoot, { recursive: true, mode: 0o755 })
    const dir = path.join(this.stagingRoot, `market-${input.label}`)
    const zipPath = path.join(this.stagingRoot, `market-${input.label}.zip`)
    rmSync(dir, { recursive: true, force: true })
    try {
      writeFileSync(zipPath, resource.body, { mode: 0o600 })
      extractPluginZip(zipPath, dir)
    } finally {
      rmSync(zipPath, { force: true })
    }
    return { dir, digest }
  }

  /** Guards against an index entry that points at someone else's package. */
  private assertArtifactMatches(entry: RegistryPluginEntry, manifest: PluginManifest): void {
    if (manifest.id !== entry.id) {
      marketError(
        `artifact declares id ${manifest.id} but the registry entry is ${entry.id}`,
        AppErrorCode.Integrity,
        { expected: entry.id, actual: manifest.id }
      )
    }
    if (manifest.version !== entry.latestVersion) {
      this.logger?.warn('registry version differs from artifact manifest', {
        pluginId: entry.id,
        registryVersion: entry.latestVersion,
        manifestVersion: manifest.version
      })
    }
  }

  /** Resolves the remote candidate for one installed plugin. */
  private async candidateFor(info: PluginInfo): Promise<Candidate> {
    if (info.sourceType === 'registry') {
      const located = await this.locateEntry(info.id, info.registryId === '' ? undefined : info.registryId)
      this.assertCompatible(located.entry)
      return {
        version: located.entry.latestVersion,
        downloadUrl: located.entry.downloadUrl,
        sha256: located.entry.sha256,
        networkDomains: located.entry.networkDomains,
        minApiVersion: located.entry.minApiVersion,
        origin: located.row.id
      }
    }
    const repo = repoFromUrl(info.sourceUrl)
    if (repo === '') {
      marketError(
        `cannot resolve the GitHub source of ${info.id}: recorded URL ${info.sourceUrl || '(none)'}`,
        AppErrorCode.NotFound,
        { pluginId: info.id }
      )
    }
    const release = await this.github.resolve(repo)
    return {
      // A non-semver tag is only comparable once the artifact manifest is read.
      version: release.version,
      downloadUrl: release.asset.downloadUrl,
      sha256: release.asset.sha256,
      networkDomains: [],
      minApiVersion: null,
      origin: release.repo
    }
  }

  private async checkOne(info: PluginInfo): Promise<PluginUpdateCheckResult> {
    const base = {
      pluginId: info.id,
      name: info.name,
      currentVersion: info.version,
      silent: false,
      addedPermissions: [],
      addedNetworkDomains: []
    } satisfies Omit<PluginUpdateCheckResult, 'availableVersion' | 'state' | 'error'>
    const candidate = await this.candidateFor(info)
    if (candidate.version !== null && !semverGt(candidate.version, info.version)) {
      this.service.recordUpdateCheck(info.id, {
        availableVersion: null,
        updateRequiresApproval: false,
        lastCheckedAt: this.stamp()
      })
      return { ...base, availableVersion: null, state: 'up-to-date', error: null }
    }

    const staged = await this.stageArtifact({
      url: candidate.downloadUrl,
      sha256: candidate.sha256,
      label: labelFor(info.id, candidate.version ?? 'latest'),
      pluginId: info.id
    })
    try {
      const manifest = this.service.inspect(staged.dir).manifest
      const version = manifest.version
      if (!semverGt(version, info.version)) {
        this.service.recordUpdateCheck(info.id, {
          availableVersion: null,
          updateRequiresApproval: false,
          lastCheckedAt: this.stamp()
        })
        return { ...base, availableVersion: null, state: 'up-to-date', error: null }
      }
      const escalation = escalationOf(manifest, candidate, info)
      if (escalation.permissions.length > 0 || escalation.domains.length > 0) {
        // Never silent: hold the artifact and ask.
        this.service.recordUpdateCheck(info.id, {
          availableVersion: version,
          updateRequiresApproval: true,
          lastCheckedAt: this.stamp(),
          lastError: null
        })
        return {
          ...base,
          availableVersion: version,
          state: 'pending-approval',
          addedPermissions: escalation.permissions,
          addedNetworkDomains: escalation.domains,
          error: null
        }
      }
      if (!info.autoUpdate) {
        this.service.recordUpdateCheck(info.id, {
          availableVersion: version,
          updateRequiresApproval: false,
          lastCheckedAt: this.stamp()
        })
        return { ...base, availableVersion: version, state: 'available', error: null }
      }
      await this.service.update(info.id, staged.dir)
      this.onSilentUpdate?.({
        pluginId: info.id,
        name: info.name,
        fromVersion: info.version,
        version
      })
      return { ...base, availableVersion: version, state: 'updated', silent: true, error: null }
    } finally {
      rmSync(staged.dir, { recursive: true, force: true })
    }
  }
}

/**
 * Permissions/domains the new version asks for that were never approved.
 * Domains are only known for registry entries: a GitHub-direct release
 * publishes no domain list, so a scope change there is caught by `network`
 * appearing in the manifest and the domains are approved in the grant dialog.
 */
function escalationOf(
  manifest: PluginManifest,
  candidate: Candidate,
  info: PluginInfo
): { permissions: PluginPermission[]; domains: string[] } {
  const permissions = manifest.permissions.filter((scope) => !info.granted.includes(scope))
  const domains = candidate.networkDomains.filter((domain) => !info.networkDomains.includes(domain))
  return { permissions, domains }
}

function matchesTokens(entry: RegistryPluginEntry, tokens: readonly string[]): boolean {
  const haystack = [entry.id, entry.name, entry.author, entry.description, ...entry.tags]
    .join(' ')
    .toLowerCase()
  return tokens.every((token) => haystack.includes(token))
}

function toSummary(
  entry: RegistryPluginEntry,
  row: { id: string; name: string },
  installed: PluginInfo | null
): MarketPluginSummary {
  const updateAvailable =
    installed !== null && (semverGt(entry.latestVersion, installed.version) || installed.availableVersion !== null)
  return {
    pluginId: entry.id,
    registryId: row.id,
    registryName: row.name,
    name: entry.name,
    author: entry.author,
    description: entry.description,
    tags: [...entry.tags],
    repo: entry.repo,
    latestVersion: entry.latestVersion,
    minApiVersion: entry.minApiVersion,
    verified: entry.verified,
    permissions: [...entry.permissions],
    networkDomains: [...entry.networkDomains],
    updatedAt: entry.updatedAt,
    installedVersion: installed?.version ?? null,
    updateAvailable,
    requiresApproval: installed?.updateRequiresApproval ?? false
  }
}

function labelFor(id: string, version: string): string {
  labelCounter += 1
  const safe = `${id}-${version}`.replace(/[^A-Za-z0-9._-]+/g, '-').slice(0, 96)
  return `${safe}-${process.pid.toString(36)}-${labelCounter}`
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}
