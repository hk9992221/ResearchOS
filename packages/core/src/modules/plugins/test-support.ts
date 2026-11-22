import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import * as path from 'node:path'

import type {
  PluginManifest,
  PluginPermission,
  PluginRpcMethod,
  SilentUpdateInfo
} from '@researchos/shared'
import { PLUGIN_MARKET_MAX_DOWNLOAD_BYTES, PLUGIN_MARKET_OFFICIAL_REGISTRY_URL } from '@researchos/shared'

import { FixedClock } from '../../clock'
import { ConfirmService } from '../../confirm'
import { AppErrorCode, appError } from '../../errors'
import { SettingsStore } from '../../settings'
import { WorkspaceManager } from '../../workspace'
import type { PluginRuleSink, PluginSkillSink } from './declarative'
import type { MarketRawResponse, MarketRequest, MarketTransport } from './market/http'
import { PluginMarketService } from './market/service'
import type { PluginFetch, PluginRpcHandler } from './rpc'
import type { PluginRuntime, PluginRuntimeStart } from './service'
import { PluginService } from './service'

export interface PluginFixture {
  root: string
  workspace: WorkspaceManager
  clock: FixedClock
  confirm: ConfirmService
  runtime: FakePluginRuntime
  service: PluginService
  cleanup: () => void
}

export class FakePluginRuntime implements PluginRuntime {
  readonly started = new Map<string, PluginRuntimeStart>()
  readonly stopped: string[] = []
  failStart = false
  failStop = false

  async start(plugin: PluginRuntimeStart): Promise<void> {
    if (this.failStart) {
      this.failStart = false
      throw new Error('fake runtime failed to start')
    }
    this.started.set(plugin.pluginId, plugin)
  }

  async stop(pluginId: string): Promise<void> {
    this.stopped.push(pluginId)
    this.started.delete(pluginId)
    if (this.failStop) {
      this.failStop = false
      throw new Error('fake runtime failed to stop')
    }
  }

  isRunning(pluginId: string): boolean {
    return this.started.has(pluginId)
  }
}

export interface FixtureOptions {
  skills?: PluginSkillSink
  rules?: PluginRuleSink
  handlers?: Partial<Record<PluginRpcMethod, PluginRpcHandler>>
  fetchImpl?: PluginFetch
  onChange?: () => void
}

function createService(fixture: {
  workspace: WorkspaceManager
  clock: FixedClock
  confirm: ConfirmService
  runtime: FakePluginRuntime
}, options: FixtureOptions): PluginService {
  return new PluginService({
    workspace: {
      root: fixture.workspace.root,
      paths: fixture.workspace.paths,
      global: fixture.workspace.global
    },
    clock: fixture.clock,
    confirm: fixture.confirm,
    runtime: fixture.runtime,
    skills: options.skills,
    rules: options.rules,
    rpcHandlers: options.handlers,
    fetchImpl: options.fetchImpl,
    onChange: options.onChange,
    audit: () => undefined
  })
}

export function createPluginFixture(label: string, options: FixtureOptions = {}): PluginFixture {
  const root = mkdtempSync(path.join(tmpdir(), `researchos-plugins-${label}-`))
  const clock = new FixedClock('2026-11-18T10:00:00.000Z')
  const workspace = WorkspaceManager.init(root, { clock, skipBackup: true })
  let counter = 0
  const confirm = new ConfirmService({
    clock,
    tokenFactory: () => `token-${++counter}`
  })
  const runtime = new FakePluginRuntime()
  const service = createService({ workspace, clock, confirm, runtime }, options)
  return {
    root,
    workspace,
    clock,
    confirm,
    runtime,
    service,
    cleanup: () => {
      try {
        workspace.close()
      } finally {
        rmSync(root, { recursive: true, force: true })
      }
    }
  }
}

export interface PluginSourceOptions {
  id?: string
  name?: string
  version?: string
  permissions?: PluginPermission[]
  main?: string
  renderer?: string
  contributions?: PluginManifest['contributes']
  skills?: Record<string, string>
  rules?: Record<string, unknown>
  extraFiles?: Record<string, string>
}

export function writePluginSource(target: string, options: PluginSourceOptions = {}): string {
  const id = options.id ?? 'lab-helper'
  mkdirSync(target, { recursive: true })
  const manifest: PluginManifest = {
    id,
    name: options.name ?? 'Lab Helper',
    version: options.version ?? '1.0.0',
    apiVersion: 1,
    description: 'Test plugin',
    main: options.main ?? 'main.js',
    ...(options.renderer === undefined ? {} : { renderer: options.renderer }),
    permissions: options.permissions ?? [],
    ...(options.contributions === undefined ? {} : { contributes: options.contributions })
  }
  writeFileSync(path.join(target, 'manifest.json'), JSON.stringify(manifest, null, 2), 'utf8')
  writeFileSync(path.join(target, 'main.js'), 'module.exports = {}\n', 'utf8')
  if (options.renderer !== undefined) {
    writeFileSync(path.join(target, options.renderer), 'export default {}\n', 'utf8')
  }
  if (options.skills !== undefined) {
    mkdirSync(path.join(target, 'skills'), { recursive: true })
    for (const [name, content] of Object.entries(options.skills)) {
      writeFileSync(path.join(target, 'skills', name), content, 'utf8')
    }
  }
  if (options.rules !== undefined) {
    mkdirSync(path.join(target, 'rules'), { recursive: true })
    for (const [name, content] of Object.entries(options.rules)) {
      writeFileSync(path.join(target, 'rules', name), JSON.stringify(content, null, 2), 'utf8')
    }
  }
  for (const [name, content] of Object.entries(options.extraFiles ?? {})) {
    const absolute = path.join(target, ...name.split('/'))
    mkdirSync(path.dirname(absolute), { recursive: true })
    writeFileSync(absolute, content, 'utf8')
  }
  return target
}

export function addSymlink(target: string, linkName: string, linkTarget: string): boolean {
  try {
    symlinkSync(linkTarget, path.join(target, linkName), 'file')
    return true
  } catch {
    return false
  }
}

/* ------------------------------------------------------------------ zip */

interface ZipInput {
  name: string
  data: string
  symlink?: boolean
  directory?: boolean
}

function crcTable(): Uint32Array {
  const table = new Uint32Array(256)
  for (let index = 0; index < 256; index += 1) {
    let value = index
    for (let bit = 0; bit < 8; bit += 1) {
      value = (value & 1) !== 0 ? 0xedb88320 ^ (value >>> 1) : value >>> 1
    }
    table[index] = value >>> 0
  }
  return table
}

const CRC_TABLE = crcTable()

function crc32(data: Buffer): number {
  let crc = 0xffffffff
  for (const byte of data) {
    crc = (CRC_TABLE[(crc ^ byte) & 0xff] ?? 0) ^ (crc >>> 8)
  }
  return (crc ^ 0xffffffff) >>> 0
}

/** Builds a store-only zip; enough to exercise the reader and path gates. */
export function buildZip(entries: ZipInput[]): Buffer {
  const locals: Buffer[] = []
  const centrals: Buffer[] = []
  let offset = 0
  for (const entry of entries) {
    const name = entry.name
    const nameBytes = Buffer.from(name, 'utf8')
    const data = entry.directory === true ? Buffer.alloc(0) : Buffer.from(entry.data, 'utf8')
    const checksum = crc32(data)
    const local = Buffer.alloc(30)
    local.writeUInt32LE(0x04034b50, 0)
    local.writeUInt16LE(20, 4)
    local.writeUInt16LE(0x0800, 6)
    local.writeUInt16LE(0, 8)
    local.writeUInt32LE(checksum, 14)
    local.writeUInt32LE(data.length, 18)
    local.writeUInt32LE(data.length, 22)
    local.writeUInt16LE(nameBytes.length, 26)
    local.writeUInt16LE(0, 28)
    locals.push(local, nameBytes, data)

    const central = Buffer.alloc(46)
    central.writeUInt32LE(0x02014b50, 0)
    central.writeUInt16LE((3 << 8) | 20, 4)
    central.writeUInt16LE(20, 6)
    central.writeUInt16LE(0x0800, 8)
    central.writeUInt16LE(0, 10)
    central.writeUInt32LE(checksum, 16)
    central.writeUInt32LE(data.length, 20)
    central.writeUInt32LE(data.length, 24)
    central.writeUInt16LE(nameBytes.length, 28)
    central.writeUInt16LE(0, 30)
    central.writeUInt16LE(0, 32)
    central.writeUInt16LE(0, 34)
    const mode = entry.symlink === true ? 0o120777 : entry.directory === true ? 0o040755 : 0o100644
    central.writeUInt32LE((mode << 16) >>> 0, 38)
    central.writeUInt32LE(offset, 42)
    centrals.push(central, nameBytes)
    offset += local.length + nameBytes.length + data.length
  }
  const centralDirectory = Buffer.concat(centrals)
  const eocd = Buffer.alloc(22)
  eocd.writeUInt32LE(0x06054b50, 0)
  eocd.writeUInt16LE(entries.length, 8)
  eocd.writeUInt16LE(entries.length, 10)
  eocd.writeUInt32LE(centralDirectory.length, 12)
  eocd.writeUInt32LE(offset, 16)
  return Buffer.concat([...locals, centralDirectory, eocd])
}

export function writeZip(target: string, entries: ZipInput[]): string {
  writeFileSync(target, buildZip(entries))
  return target
}

/* ---------------------------------------------------------------- market */

/**
 * Market fixtures: a route-table fake for the HTTP seam, artifact publishers
 * that keep zip bytes and published checksums consistent, and a composed
 * `PluginMarketService` over a real workspace. Everything else — staging,
 * extraction, the install/update state machine — is the production code.
 */

export interface MarketRouteInput {
  body?: Buffer | string
  /** Any status except 200 answers without a body. */
  status?: number
  etag?: string
}

export interface MarketNetwork {
  /** Requests in call order, with the conditional header the client sent. */
  readonly calls: Array<{ url: string; ifNoneMatch: string }>
  set(url: string, route: MarketRouteInput): void
  remove(url: string): void
  reset(): void
  transport: MarketTransport
}

export function createMarketNetwork(): MarketNetwork {
  const routes = new Map<string, MarketRouteInput>()
  const calls: Array<{ url: string; ifNoneMatch: string }> = []
  const transport: MarketTransport = async (request: MarketRequest): Promise<MarketRawResponse> => {
    const ifNoneMatch = request.headers?.['if-none-match'] ?? ''
    calls.push({ url: request.url, ifNoneMatch })
    const route = routes.get(request.url)
    if (route === undefined) return { status: 404, headers: {}, body: Buffer.alloc(0) }
    if (route.status !== undefined && route.status !== 200) {
      return { status: route.status, headers: {}, body: Buffer.alloc(0) }
    }
    if (route.etag !== undefined && ifNoneMatch === route.etag) {
      return { status: 304, headers: { etag: route.etag }, body: Buffer.alloc(0) }
    }
    const body =
      route.body === undefined
        ? Buffer.alloc(0)
        : typeof route.body === 'string'
          ? Buffer.from(route.body, 'utf8')
          : route.body
    if (body.length > request.maxBytes) {
      throw appError(AppErrorCode.LimitExceeded, `remote resource exceeds ${String(request.maxBytes)} bytes`, {
        reason: 'market_size'
      })
    }
    return {
      status: 200,
      headers: route.etag === undefined ? {} : { etag: route.etag },
      body
    }
  }
  return {
    calls,
    set: (url, route) => {
      routes.set(url, route)
    },
    remove: (url) => {
      routes.delete(url)
    },
    reset: () => {
      routes.clear()
      calls.length = 0
    },
    transport
  }
}

export interface ArtifactInput {
  id: string
  version: string
  permissions?: PluginPermission[]
  name?: string
  files?: Record<string, string>
}

export function pluginZip(artifact: ArtifactInput): Buffer {
  const manifest: PluginManifest = {
    id: artifact.id,
    name: artifact.name ?? artifact.id,
    version: artifact.version,
    apiVersion: 1,
    description: 'Market fixture plugin',
    main: 'main.js',
    permissions: artifact.permissions ?? []
  }
  return buildZip([
    { name: 'manifest.json', data: JSON.stringify(manifest, null, 2) },
    { name: 'main.js', data: 'module.exports = {}\n' },
    ...Object.entries(artifact.files ?? {}).map(([name, data]) => ({ name, data }))
  ])
}

export function sha256Of(bytes: Buffer): string {
  return createHash('sha256').update(bytes).digest('hex')
}

export function assetUrlOf(id: string, version: string): string {
  return `https://github.com/researchos-plugins/${id}/releases/download/v${version}/${id}-${version}.zip`
}

export interface PublishedArtifact extends ArtifactInput {
  verified?: boolean
  tags?: string[]
  networkDomains?: string[]
  minApiVersion?: number
  /** `null` publishes no README route at all. */
  readme?: string | null
}

export function readmeUrlOf(id: string): string {
  return `https://raw.githubusercontent.com/researchos-plugins/${id}/main/README.md`
}

/** Registers the artifact download and README routes, returns its index entry. */
export function publishArtifact(
  net: MarketNetwork,
  input: PublishedArtifact
): { entry: Record<string, unknown>; zip: Buffer; url: string } {
  const zip = pluginZip(input)
  const url = assetUrlOf(input.id, input.version)
  net.set(url, { body: zip })
  const readmeUrl = readmeUrlOf(input.id)
  if (input.readme !== null) {
    net.set(readmeUrl, { body: input.readme ?? `# ${input.name ?? input.id}\n\nMarket fixture README.` })
  }
  return {
    zip,
    url,
    entry: {
      id: input.id,
      name: input.name ?? input.id,
      author: 'ResearchOS Community',
      description: 'A plugin published to the fixture registry.',
      tags: input.tags ?? ['literature'],
      repo: `researchos-plugins/${input.id}`,
      latestVersion: input.version,
      minApiVersion: input.minApiVersion ?? 1,
      downloadUrl: url,
      sha256: sha256Of(zip),
      permissions: input.permissions ?? [],
      networkDomains: input.networkDomains ?? [],
      readmeUrl,
      verified: input.verified ?? true,
      updatedAt: '2026-11-21T10:00:00.000Z'
    }
  }
}

export function registryDocument(
  entries: readonly unknown[],
  updatedAt = '2026-11-21T12:00:00.000Z'
): string {
  return JSON.stringify({ schemaVersion: 1, updatedAt, plugins: entries })
}

export interface PublishedRelease {
  releaseUrl: string
  assetUrl: string
  sha256: string
  zip: Buffer
}

/** Publishes one GitHub release, with or without a checksum sidecar. */
export function publishGithubRelease(
  net: MarketNetwork,
  input: PublishedArtifact & { repo?: string; sidecar?: boolean }
): PublishedRelease {
  const repo = input.repo ?? `researchos-plugins/${input.id}`
  const zip = pluginZip(input)
  const digest = sha256Of(zip)
  const assetName = `${input.id}-${input.version}.zip`
  const assetUrl = `https://github.com/${repo}/releases/download/v${input.version}/${assetName}`
  net.set(assetUrl, { body: zip })
  const assets: Array<Record<string, unknown>> = [
    { name: assetName, browser_download_url: assetUrl, size: zip.length }
  ]
  if (input.sidecar !== false) {
    const sidecarUrl = `https://github.com/${repo}/releases/download/v${input.version}/${assetName}.sha256`
    net.set(sidecarUrl, { body: `${digest}  ${assetName}\n` })
    assets.push({ name: `${assetName}.sha256`, browser_download_url: sidecarUrl, size: 78 })
  }
  const releaseUrl = `https://api.github.com/repos/${repo}/releases/latest`
  net.set(releaseUrl, {
    body: JSON.stringify({
      tag_name: `v${input.version}`,
      name: `v${input.version}`,
      body: 'Release notes',
      published_at: '2026-11-21T10:00:00.000Z',
      assets
    })
  })
  return { releaseUrl, assetUrl, sha256: digest, zip }
}

export interface MarketFixtureOptions {
  interval?: string
  mirrorPrefix?: string
  registryUrl?: string
}

export interface MarketFixture {
  fx: PluginFixture
  net: MarketNetwork
  settings: SettingsStore
  market: PluginMarketService
  /** Silent updates the market reported, in order. */
  silences: SilentUpdateInfo[]
  registryUrl: string
  pluginsRoot(): string
  stagingDir(): string
  cleanup(): void
}

export const OFFICIAL_REGISTRY_URL = PLUGIN_MARKET_OFFICIAL_REGISTRY_URL

export function createMarketFixture(label: string, options: MarketFixtureOptions = {}): MarketFixture {
  const fx = createPluginFixture(label)
  const net = createMarketNetwork()
  const settings = new SettingsStore(fx.workspace.global.db, 'workspace', fx.clock)
  if (options.interval !== undefined) {
    settings.set('plugins.market.auto_update_interval', options.interval)
  }
  if (options.mirrorPrefix !== undefined) {
    settings.set('plugins.market.github_mirror_prefix', options.mirrorPrefix)
  }
  const registryUrl = options.registryUrl ?? OFFICIAL_REGISTRY_URL
  if (options.registryUrl !== undefined) {
    settings.set('plugins.market.registry_url', registryUrl)
  }
  net.set(registryUrl, { body: registryDocument([]), etag: 'initial' })
  const silences: SilentUpdateInfo[] = []
  const market = new PluginMarketService({
    workspace: { root: fx.workspace.root, paths: fx.workspace.paths, global: fx.workspace.global },
    service: fx.service,
    settings,
    clock: fx.clock,
    transport: net.transport,
    onSilentUpdate: (update) => {
      silences.push(update)
    }
  })
  return {
    fx,
    net,
    settings,
    market,
    silences,
    registryUrl,
    pluginsRoot: () => path.join(fx.workspace.root, 'plugins'),
    stagingDir: () => path.join(fx.workspace.root, 'plugins', '.staging'),
    cleanup: () => fx.cleanup()
  }
}

/** The downloaded artifact size cap, spelled once for tests. */
export const MARKET_DOWNLOAD_CAP = PLUGIN_MARKET_MAX_DOWNLOAD_BYTES