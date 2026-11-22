import type { GithubReleaseAsset, GithubReleasePreview } from '@researchos/shared'
import { PLUGIN_MARKET_MAX_DOWNLOAD_BYTES } from '@researchos/shared'

import { AppErrorCode, appError } from '../../../errors'
import { MarketHttp } from './http'
import { assertRemoteUrl } from './registry'
import { isSemver, versionFromTag } from './semver'

/**
 * GitHub Releases as an install source. Only the
 * Releases REST API is used — never the Search API — and the release list is
 * read straight from `owner/repo`, so a direct install works for any repo that
 * follows the packaging convention, registered or not.
 */

const REPO_PATTERN = /^[A-Za-z0-9_.-]{1,100}\/[A-Za-z0-9_.-]{1,100}$/
const GITHUB_API_HOST = 'api.github.com'
const SHA256_PATTERN = /[0-9a-f]{64}/
const MAX_SIDECAR_BYTES = 512
const ASSET_ACCEPT = 'application/vnd.github+json'

export interface ResolvedRelease {
  repo: string
  tag: string
  name: string
  body: string
  publishedAt: string | null
  asset: GithubReleaseAsset
  /** Semver of the release tag; `null` when the tag is not a version. */
  version: string | null
  warnings: string[]
}

function badRepo(repo: string): never {
  throw appError(AppErrorCode.InvalidArgument, `github repository must be "owner/repo": ${repo}`, {
    field: 'repo',
    reason: 'market_github'
  })
}

export function normalizeRepo(repo: string): string {
  const trimmed = repo.trim().replace(/^\/+|\/+$/g, '')
  const full = /^https?:\/\/github\.com\//i.test(trimmed)
    ? new URL(trimmed).pathname.replace(/^\/+/, '').replace(/\.git$/i, '')
    : trimmed
  if (!REPO_PATTERN.test(full)) badRepo(repo)
  return full
}

function releaseUrl(repo: string, tag?: string): string {
  return tag === undefined || tag.trim() === ''
    ? `https://${GITHUB_API_HOST}/repos/${repo}/releases/latest`
    : `https://${GITHUB_API_HOST}/repos/${repo}/releases/tags/${encodeURIComponent(tag.trim())}`
}

interface RawAsset {
  name: string
  downloadUrl: string
  size: number
}

function pickAssets(payload: unknown): { tag: string; name: string; body: string; publishedAt: string | null; assets: RawAsset[] } {
  if (typeof payload !== 'object' || payload === null) {
    throw appError(AppErrorCode.ExternalUnavailable, 'github release payload is not an object', {
      reason: 'market_github'
    })
  }
  const release = payload as Record<string, unknown>
  const tag = typeof release['tag_name'] === 'string' ? release['tag_name'] : ''
  const rawAssets = Array.isArray(release['assets']) ? (release['assets'] as unknown[]) : []
  const assets: RawAsset[] = []
  for (const item of rawAssets) {
    if (typeof item !== 'object' || item === null) continue
    const asset = item as Record<string, unknown>
    const url = typeof asset['browser_download_url'] === 'string' ? asset['browser_download_url'] : ''
    const name = typeof asset['name'] === 'string' ? asset['name'] : ''
    if (url === '' || name === '' || !url.startsWith('https://')) continue
    assets.push({
      name,
      downloadUrl: url,
      size: typeof asset['size'] === 'number' && Number.isFinite(asset['size']) ? asset['size'] : 0
    })
  }
  return {
    tag,
    name: typeof release['name'] === 'string' ? release['name'] : '',
    body: typeof release['body'] === 'string' ? release['body'] : '',
    publishedAt: typeof release['published_at'] === 'string' ? release['published_at'] : null,
    assets
  }
}

/** The `.zip` artifact of a release, ignoring checksum sidecars. */
function chooseZip(repo: string, assets: RawAsset[], warnings: string[]): RawAsset | null {
  const zips = assets.filter((asset) => asset.name.toLowerCase().endsWith('.zip'))
  if (zips.length === 0) {
    warnings.push('release has no .zip artifact')
    return null
  }
  if (zips.length > 1) {
    warnings.push('release has several .zip artifacts')
    const slug = repo.split('/')[1] ?? ''
    const named = zips.find((asset) => asset.name.toLowerCase().includes(slug.toLowerCase()))
    return named ?? zips[0]!
  }
  return zips[0]!
}

export class GithubReleaseClient {
  private readonly http: MarketHttp

  constructor(http: MarketHttp) {
    this.http = http
  }

  /**
   * Reads the release metadata and any `<asset>.sha256` sidecar. Nothing is
   * downloaded here: the UI shows the result and asks the user to confirm
   * before the artifact is fetched.
   */
  async resolve(repoInput: string, tag?: string): Promise<ResolvedRelease> {
    const repo = normalizeRepo(repoInput)
    const response = await this.http.get(assertRemoteUrl(releaseUrl(repo, tag), 'url'), {
      maxBytes: 2 * 1024 * 1024,
      accept: ASSET_ACCEPT
    })
    if (response.notModified) {
      throw appError(AppErrorCode.ExternalUnavailable, 'github release lookup returned no content', {
        reason: 'market_github',
        repo
      })
    }
    let payload: unknown
    try {
      payload = JSON.parse(response.body.toString('utf8')) as unknown
    } catch (cause) {
      throw appError(AppErrorCode.ExternalUnavailable, 'github release lookup returned invalid JSON', {
        reason: 'market_github',
        repo,
        cause: cause instanceof Error ? cause.message : String(cause)
      })
    }
    const release = pickAssets(payload)
    const warnings: string[] = []
    const chosen = chooseZip(repo, release.assets, warnings)
    if (chosen === null) {
      throw appError(
        AppErrorCode.NotFound,
        `github release ${repo}${release.tag === '' ? '' : ` ${release.tag}`} has no installable .zip artifact`,
        { reason: 'market_github', repo, tag: release.tag }
      )
    }
    if (chosen.size > PLUGIN_MARKET_MAX_DOWNLOAD_BYTES) {
      warnings.push(
        `artifact is ${String(Math.round(chosen.size / 1024 / 1024))} MiB, over the ${String(
          Math.floor(PLUGIN_MARKET_MAX_DOWNLOAD_BYTES / 1024 / 1024)
        )} MiB cap`
      )
    }
    const sidecar = release.assets.find((asset) => asset.name === `${chosen.name}.sha256`)
    let sha256: string | null = null
    if (sidecar !== undefined) {
      sha256 = await this.readSidecar(sidecar.downloadUrl, repo)
      if (sha256 === null) warnings.push('checksum sidecar did not contain a SHA-256 digest')
    } else {
      warnings.push('release publishes no .sha256 sidecar')
    }
    const version = release.tag === '' ? '' : versionFromTag(release.tag)
    if (version !== '' && !isSemver(version)) warnings.push(`release tag ${release.tag} is not a version`)
    return {
      repo,
      tag: release.tag,
      name: release.name,
      body: release.body,
      publishedAt: release.publishedAt,
      asset: { name: chosen.name, downloadUrl: chosen.downloadUrl, size: chosen.size, sha256 },
      version: version !== '' && isSemver(version) ? version : null,
      warnings
    }
  }

  /** The preview shape exposed over IPC: every asset, not just the chosen one. */
  async preview(repoInput: string, tag?: string): Promise<GithubReleasePreview> {
    const release = await this.resolve(repoInput, tag)
    return {
      repo: release.repo,
      tag: release.tag,
      name: release.name,
      body: release.body,
      publishedAt: release.publishedAt,
      assets: [release.asset],
      warnings: release.warnings
    }
  }

  private async readSidecar(url: string, repo: string): Promise<string | null> {
    try {
      const response = await this.http.get(assertRemoteUrl(url, 'url'), { maxBytes: MAX_SIDECAR_BYTES })
      const match = SHA256_PATTERN.exec(response.body.toString('utf8').toLowerCase())
      return match?.[0] ?? null
    } catch {
      // A missing checksum is a warning, not a failed lookup; the install path
      // then requires the explicit unverified confirmation.
      return null
    }
  }
}
