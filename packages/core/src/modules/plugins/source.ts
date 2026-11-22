import {
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  realpathSync,
  statSync,
  writeFileSync
} from 'node:fs'
import * as path from 'node:path'
import { inflateRawSync } from 'node:zlib'

import { AppErrorCode, appError } from '../../errors'
import { MAX_PLUGIN_BYTES, MAX_PLUGIN_FILES } from './manifest'

/**
 * Copies a plugin source (folder or zip) into a staging folder.
 *
 * Every entry passes through one gate: relative path segments only (no drive,
 * UNC, backslash or `.`/`..`), no symlinks, and the size/file-count caps. ZIP
 * reading is the minimal central-directory implementation v1 needs: stored and
 * deflated entries, no encryption, no zip64, no multi-disk.
 */

const ZIP_EOCD_SIGNATURE = 0x06054b50
const ZIP_CENTRAL_SIGNATURE = 0x02014b50
const ZIP_LOCAL_SIGNATURE = 0x04034b50
const ZIP_EOCD_MIN = 22
const ZIP_MAX_COMMENT = 0xffff

function sourceError(message: string, details: Record<string, unknown> = {}): never {
  throw appError(AppErrorCode.InvalidArgument, message, { reason: 'plugin_source', ...details })
}

/** True when `absolute` is the path itself or nested inside `root`. */
export function isInside(root: string, absolute: string): boolean {
  const relative = path.relative(root, absolute)
  return relative === '' || (!relative.startsWith('..') && !path.isAbsolute(relative))
}

/** Rejects anything that is not a safe relative POSIX path. */
export function safeRelativeEntry(name: string): string {
  if (name === '') sourceError('archive entry has an empty name')
  if (name.includes('\\')) sourceError('archive entry must use forward slashes', { entry: name })
  if (name.includes('\0')) sourceError('archive entry contains a NUL byte')
  if (name.startsWith('/') || /^[A-Za-z]:/.test(name)) {
    sourceError('archive entry must be relative', { entry: name })
  }
  const segments = name.split('/')
  if (segments.some((segment) => segment === '' || segment === '.')) {
    sourceError('archive entry has an empty or dot segment', { entry: name })
  }
  if (segments.includes('..')) sourceError('archive entry escapes the plugin root', { entry: name })
  return name
}

function writeEntry(targetRoot: string, name: string, data: Buffer): void {
  const destination = path.join(targetRoot, ...name.split('/'))
  if (!isInside(targetRoot, destination)) sourceError('archive entry escapes the plugin root', { entry: name })
  mkdirSync(path.dirname(destination), { recursive: true, mode: 0o755 })
  writeFileSync(destination, data, { mode: 0o644 })
}

export interface CopyStats {
  files: number
  bytes: number
}

/** Copies one plugin folder, refusing symlinks and escaping paths. */
export function copyPluginFolder(source: string, targetRoot: string): CopyStats {
  const realSource = realpathSync(source)
  if (!statSync(realSource).isDirectory()) sourceError('plugin source is not a directory')
  mkdirSync(targetRoot, { recursive: true, mode: 0o755 })
  const stats: CopyStats = { files: 0, bytes: 0 }
  const walk = (relative: string): void => {
    const directory = path.join(realSource, ...relative.split('/').filter(Boolean))
    const entries = readdirSync(directory, { withFileTypes: true })
    for (const entry of entries) {
      const childRelative = relative === '' ? entry.name : `${relative}/${entry.name}`
      const childAbsolute = path.join(directory, entry.name)
      const info = lstatSync(childAbsolute)
      if (info.isSymbolicLink()) sourceError('plugins may not contain symlinks', { entry: childRelative })
      if (info.isDirectory()) {
        mkdirSync(path.join(targetRoot, ...childRelative.split('/')), { recursive: true, mode: 0o755 })
        walk(childRelative)
        continue
      }
      if (!info.isFile()) sourceError('plugins may contain regular files only', { entry: childRelative })
      stats.files += 1
      stats.bytes += info.size
      if (stats.files > MAX_PLUGIN_FILES) sourceError(`plugin exceeds ${MAX_PLUGIN_FILES} files`)
      if (stats.bytes > MAX_PLUGIN_BYTES) sourceError(`plugin exceeds ${MAX_PLUGIN_BYTES} bytes`)
      const destination = path.join(targetRoot, ...childRelative.split('/'))
      mkdirSync(path.dirname(destination), { recursive: true, mode: 0o755 })
      copyFileSync(childAbsolute, destination)
    }
  }
  walk('')
  return stats
}

interface ZipEntry {
  name: string
  method: number
  compressedSize: number
  uncompressedSize: number
  localOffset: number
  flags: number
  externalAttributes: number
  directory: boolean
}

function findEndOfCentralDirectory(buffer: Buffer): number {
  const minimum = Math.max(0, buffer.length - ZIP_EOCD_MIN - ZIP_MAX_COMMENT)
  for (let offset = buffer.length - ZIP_EOCD_MIN; offset >= minimum; offset -= 1) {
    if (buffer.readUInt32LE(offset) === ZIP_EOCD_SIGNATURE) return offset
  }
  sourceError('zip archive has no end-of-central-directory record')
}

function readCentralDirectory(buffer: Buffer): ZipEntry[] {
  const eocd = findEndOfCentralDirectory(buffer)
  const disk = buffer.readUInt16LE(eocd + 4)
  const centralDisk = buffer.readUInt16LE(eocd + 6)
  const entriesThisDisk = buffer.readUInt16LE(eocd + 8)
  const totalEntries = buffer.readUInt16LE(eocd + 10)
  const centralSize = buffer.readUInt32LE(eocd + 12)
  const centralOffset = buffer.readUInt32LE(eocd + 16)
  if (disk !== 0 || centralDisk !== 0 || entriesThisDisk !== totalEntries) {
    sourceError('multi-disk zip archives are not supported')
  }
  if (totalEntries === 0xffff || centralSize === 0xffffffff || centralOffset === 0xffffffff) {
    sourceError('zip64 archives are not supported')
  }
  if (centralOffset + centralSize > buffer.length) sourceError('zip central directory is truncated')

  const entries: ZipEntry[] = []
  const seen = new Set<string>()
  let cursor = centralOffset
  for (let index = 0; index < totalEntries; index += 1) {
    if (cursor + 46 > buffer.length || buffer.readUInt32LE(cursor) !== ZIP_CENTRAL_SIGNATURE) {
      sourceError('zip central directory entry is corrupt', { index })
    }
    const flags = buffer.readUInt16LE(cursor + 8)
    const method = buffer.readUInt16LE(cursor + 10)
    const compressedSize = buffer.readUInt32LE(cursor + 20)
    const uncompressedSize = buffer.readUInt32LE(cursor + 24)
    const nameLength = buffer.readUInt16LE(cursor + 28)
    const extraLength = buffer.readUInt16LE(cursor + 30)
    const commentLength = buffer.readUInt16LE(cursor + 32)
    const externalAttributes = buffer.readUInt32LE(cursor + 38)
    const localOffset = buffer.readUInt32LE(cursor + 42)
    const nameStart = cursor + 46
    const nameBytes = buffer.subarray(nameStart, nameStart + nameLength)
    const rawName = nameBytes.toString('utf8')
    const directory = rawName.endsWith('/') || (externalAttributes & 0x10) !== 0
    const name = rawName.endsWith('/') ? rawName.slice(0, -1) : rawName
    if (name !== '') safeRelativeEntry(name)
    if (seen.has(name)) sourceError('zip archive contains duplicate entries', { entry: name })
    seen.add(name)
    const madeByUnix = buffer.readUInt16LE(cursor + 4) >> 8 === 3
    const mode = externalAttributes >>> 16
    if (madeByUnix && (mode & 0o170000) === 0o120000) {
      sourceError('plugins may not contain symlinks', { entry: name })
    }
    if ((flags & 0x1) !== 0) sourceError('encrypted zip entries are not supported', { entry: name })
    if (compressedSize === 0xffffffff || uncompressedSize === 0xffffffff) {
      sourceError('zip64 entries are not supported', { entry: name })
    }
    entries.push({
      name,
      method,
      compressedSize,
      uncompressedSize,
      localOffset,
      flags,
      externalAttributes,
      directory
    })
    cursor = nameStart + nameLength + extraLength + commentLength
  }
  if (entries.length > MAX_PLUGIN_FILES) sourceError(`plugin exceeds ${MAX_PLUGIN_FILES} files`)
  return entries
}

function readZipEntryData(buffer: Buffer, entry: ZipEntry): Buffer {
  if (entry.localOffset + 30 > buffer.length) sourceError('zip entry is truncated', { entry: entry.name })
  const local = entry.localOffset
  if (buffer.readUInt32LE(local) !== ZIP_LOCAL_SIGNATURE) {
    sourceError('zip entry has a bad local header', { entry: entry.name })
  }
  const nameLength = buffer.readUInt16LE(local + 26)
  const extraLength = buffer.readUInt16LE(local + 28)
  const dataStart = local + 30 + nameLength + extraLength
  const dataEnd = dataStart + entry.compressedSize
  if (dataEnd > buffer.length) sourceError('zip entry is truncated', { entry: entry.name })
  const compressed = buffer.subarray(dataStart, dataEnd)
  let data: Buffer
  if (entry.method === 0) data = Buffer.from(compressed)
  else if (entry.method === 8) {
    try {
      data = inflateRawSync(compressed, { maxOutputLength: entry.uncompressedSize })
    } catch (error) {
      sourceError(`zip entry failed to inflate: ${error instanceof Error ? error.message : String(error)}`, {
        entry: entry.name
      })
    }
  } else {
    sourceError(`zip compression method ${entry.method} is not supported`, { entry: entry.name })
  }
  if (data.length !== entry.uncompressedSize) {
    sourceError('zip entry size mismatch', { entry: entry.name })
  }
  return data
}

/** Extracts one zip plugin source into `targetRoot`. */
export function extractPluginZip(zipPath: string, targetRoot: string): CopyStats {
  const buffer = readFileSync(zipPath)
  if (buffer.length === 0) sourceError('zip archive is empty')
  const entries = readCentralDirectory(buffer)
  mkdirSync(targetRoot, { recursive: true, mode: 0o755 })
  const stats: CopyStats = { files: 0, bytes: 0 }
  for (const entry of entries) {
    if (entry.name === '') continue
    if (entry.directory) {
      mkdirSync(path.join(targetRoot, ...entry.name.split('/')), { recursive: true, mode: 0o755 })
      continue
    }
    const data = readZipEntryData(buffer, entry)
    stats.files += 1
    stats.bytes += data.length
    if (stats.bytes > MAX_PLUGIN_BYTES) sourceError(`plugin exceeds ${MAX_PLUGIN_BYTES} bytes`)
    writeEntry(targetRoot, entry.name, data)
  }
  return stats
}

/** Classifies a source as folder or zip without trusting its extension. */
export function readPluginSource(source: string): 'folder' | 'zip' {
  let info
  try {
    info = statSync(source)
  } catch (error) {
    throw appError(AppErrorCode.InvalidArgument, `plugin source does not exist: ${source}`, {
      reason: 'plugin_source',
      cause: error instanceof Error ? error.message : String(error)
    })
  }
  if (info.isDirectory()) return 'folder'
  if (info.isFile()) return 'zip'
  sourceError('plugin source must be a folder or zip file')
}

/** Copies the source into a fresh staging folder (caller owns cleanup). */
export function materializePluginSource(source: string, stagingRoot: string): CopyStats {
  const kind = readPluginSource(source)
  if (kind === 'folder') return copyPluginFolder(source, stagingRoot)
  return extractPluginZip(source, stagingRoot)
}

/** Reads a file only when it is a regular file (never a symlink). */
export function readRegularFile(absolute: string, limit: number): Buffer | null {
  let info
  try {
    info = lstatSync(absolute)
  } catch {
    return null
  }
  if (!info.isFile() || info.isSymbolicLink()) return null
  if (info.size > limit) return null
  return readFileSync(absolute)
}

/** Ensures the file exists, is regular and stays inside the plugin root. */
export function requirePluginFile(root: string, relative: string, limit: number): string {
  const absolute = path.join(root, ...relative.split('/'))
  if (!isInside(root, absolute)) sourceError('plugin entry escapes the plugin root', { file: relative })
  if (!existsSync(absolute)) sourceError('plugin entry is missing', { file: relative })
  const info = lstatSync(absolute)
  if (info.isSymbolicLink() || !info.isFile()) {
    sourceError('plugin entry must be a regular file', { file: relative })
  }
  if (info.size > limit) sourceError(`plugin entry exceeds ${limit} bytes`, { file: relative })
  return absolute
}

