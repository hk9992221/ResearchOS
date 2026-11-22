import { createReadStream } from 'node:fs'
import { realpathSync, statSync } from 'node:fs'
import { extname, join, normalize, sep } from 'node:path'

/** Privileged scheme so pdf.js can `fetch()` it from the sandboxed renderer. */
export const ROS_ASSET_SCHEME = 'ros-asset'
export const ROS_ASSET_HOST = 'workspace'

const MEDIA_TYPES: Readonly<Record<string, string>> = {
  '.pdf': 'application/pdf',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.avif': 'image/avif',
  '.svg': 'image/svg+xml',
  '.bmp': 'image/bmp'
}

export interface AssetRange {
  start: number
  end: number
}

export interface AssetResult {
  status: number
  headers: Record<string, string>
  body: Uint8Array | null
}

/** Parses `bytes=start-end`; malformed or multi-range headers fall back to 200. */
export function parseRange(header: string | null, size: number): AssetRange | null {
  if (header === null) return null
  const match = /^bytes=(\d*)-(\d*)$/u.exec(header.trim())
  if (match === null) return null
  const startRaw = match[1] ?? ''
  const endRaw = match[2] ?? ''
  if (startRaw === '' && endRaw === '') return null
  let start: number
  let end: number
  if (startRaw === '') {
    const suffix = Number(endRaw)
    if (!Number.isSafeInteger(suffix) || suffix <= 0) return null
    start = Math.max(0, size - suffix)
    end = size - 1
  } else {
    start = Number(startRaw)
    end = endRaw === '' ? size - 1 : Number(endRaw)
    if (!Number.isSafeInteger(start) || !Number.isSafeInteger(end)) return null
    if (start < 0 || start >= size) return null
    end = Math.min(end, size - 1)
  }
  if (end < start) return null
  return { start, end }
}

function notFound(): AssetResult {
  return { status: 404, headers: {}, body: null }
}

function forbidden(): AssetResult {
  return { status: 403, headers: { 'content-type': 'text/plain' }, body: new Uint8Array() }
}

/**
 * Resolves `ros-asset://workspace/<workspace-relative-path>` with containment
 * validation. Absolute paths, `..` segments and symlinks escaping the
 * workspace are rejected. Only media and PDF extensions are served.
 */
export function resolveRosAsset(
  root: string | null,
  requestUrl: string,
  rangeHeader: string | null = null,
  method: string = 'GET'
): Promise<AssetResult> {
  if (root === null) return Promise.resolve(notFound())

  let url: URL
  try {
    url = new URL(requestUrl)
  } catch {
    return Promise.resolve(notFound())
  }
  if (url.protocol !== `${ROS_ASSET_SCHEME}:`) return Promise.resolve(notFound())
  if (url.hostname !== ROS_ASSET_HOST) return Promise.resolve(notFound())

  let relative: string
  try {
    relative = decodeURIComponent(url.pathname)
  } catch {
    return Promise.resolve(notFound())
  }
  relative = relative.replace(/^\/+/u, '')
  if (relative === '' || relative.includes('\0')) return Promise.resolve(notFound())

  const segments = relative.split('/')
  if (segments.some((segment) => segment === '' || segment === '.' || segment === '..')) {
    return Promise.resolve(forbidden())
  }

  const mediaType = MEDIA_TYPES[extname(relative).toLowerCase()]
  if (mediaType === undefined) return Promise.resolve(forbidden())

  let workspaceRoot: string
  try {
    workspaceRoot = realpathSync(root)
  } catch {
    return Promise.resolve(notFound())
  }

  const absolute = normalize(join(workspaceRoot, ...segments))
  if (absolute !== workspaceRoot && !absolute.startsWith(`${workspaceRoot}${sep}`)) {
    return Promise.resolve(forbidden())
  }

  let resolved: string
  try {
    resolved = realpathSync(absolute)
  } catch {
    return Promise.resolve(notFound())
  }
  if (resolved !== workspaceRoot && !resolved.startsWith(`${workspaceRoot}${sep}`)) {
    return Promise.resolve(forbidden())
  }

  let size: number
  try {
    const stat = statSync(resolved)
    if (!stat.isFile()) return Promise.resolve(notFound())
    size = stat.size
  } catch {
    return Promise.resolve(notFound())
  }

  if (rangeHeader !== null && rangeHeader.trim() !== '' && parseRange(rangeHeader, size) === null) {
    return Promise.resolve({
      status: 416,
      headers: {
        'access-control-allow-origin': '*',
        'access-control-expose-headers': 'Content-Range, Accept-Ranges, Content-Length',
        'accept-ranges': 'bytes',
        'content-range': `bytes */${size}`
      },
      body: null
    })
  }
  const range = parseRange(rangeHeader, size)
  const start = range?.start ?? 0
  const end = range?.end ?? size - 1
  const headers: Record<string, string> = {
    'content-type': mediaType,
    'accept-ranges': 'bytes',
    'cache-control': 'no-store',
    'content-length': String(Math.max(0, end - start + 1)),
    'access-control-allow-origin': '*',
    'access-control-expose-headers': 'Content-Range, Accept-Ranges, Content-Length',
    ...(range === null ? {} : { 'content-range': `bytes ${start}-${end}/${size}` })
  }
  if (method.toUpperCase() === 'HEAD') {
    return Promise.resolve({ status: range === null ? 200 : 206, headers, body: null })
  }

  return new Promise<AssetResult>((resolve) => {
    const chunks: Buffer[] = []
    const stream = createReadStream(resolved, { start, end })
    stream.on('data', (chunk: string | Buffer) => chunks.push(typeof chunk === 'string' ? Buffer.from(chunk) : chunk))
    stream.on('error', () => resolve(notFound()))
    stream.on('end', () => {
      const body = Buffer.concat(chunks)
      resolve({ status: range === null ? 200 : 206, headers, body })
    })
  })
}
