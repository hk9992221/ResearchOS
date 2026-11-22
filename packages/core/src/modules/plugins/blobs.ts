import { createHash } from 'node:crypto'
import {
  createReadStream,
  createWriteStream,
  existsSync,
  lstatSync,
  mkdirSync,
  readdirSync,
  renameSync,
  rmSync,
  statSync
} from 'node:fs'
import { pipeline } from 'node:stream/promises'
import * as path from 'node:path'

import type { PluginBlobCaptureResult, PluginBlobStats } from '@researchos/shared'

import { AppErrorCode, appError } from '../../errors'
import type { PluginFilesService } from './files'
import { PLUGIN_ID_PATTERN } from './manifest'

/**
 * Plugin-private content-addressed blob pool. Large binaries
 * live under `<workspace>/plugins/<id>/blobs/<sha256>` — never in SQLite and
 * never exported with a project. All copies are streamed, and only an existing
 * authorized file can be captured.
 */

const SHA256_PATTERN = /^[a-f0-9]{64}$/

export interface PluginBlobStoreOptions {
  pluginsRoot: string
  files: PluginFilesService
}

function invalid(message: string, field: string): never {
  throw appError(AppErrorCode.InvalidArgument, message, { field })
}

export class PluginBlobStore {
  private readonly pluginsRoot: string
  private readonly files: PluginFilesService

  constructor(options: PluginBlobStoreOptions) {
    this.pluginsRoot = options.pluginsRoot
    this.files = options.files
  }

  private blobsDir(pluginId: string): string {
    if (!PLUGIN_ID_PATTERN.test(pluginId)) invalid('invalid plugin id', 'pluginId')
    return path.join(this.pluginsRoot, pluginId, 'blobs')
  }

  private objectPath(pluginId: string, sha256: string): string {
    if (!SHA256_PATTERN.test(sha256)) invalid('sha256 must be 64 lowercase hex characters', 'sha256')
    return path.join(this.blobsDir(pluginId), sha256)
  }

  async capture(
    pluginId: string,
    params: { dirId: string; relativePath: string }
  ): Promise<PluginBlobCaptureResult> {
    const { absolute } = this.files.resolve(pluginId, params.dirId, params.relativePath)
    const info = lstatSync(absolute)
    if (!info.isFile()) invalid('relativePath must name a file', 'relativePath')
    const hash = createHash('sha256')
    const dir = this.blobsDir(pluginId)
    mkdirSync(dir, { recursive: true, mode: 0o755 })
    const temporary = path.join(
      dir,
      `.capture-${process.pid}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
    )
    try {
      // One pass: hash while streaming into a temp file, then dedupe or rename.
      await pipeline(
        createReadStream(absolute),
        async function* (source) {
          for await (const chunk of source) {
            hash.update(chunk as Buffer)
            yield chunk
          }
        },
        createWriteStream(temporary, { mode: 0o644 })
      )
      const sha256 = hash.digest('hex')
      const target = this.objectPath(pluginId, sha256)
      if (existsSync(target)) {
        rmSync(temporary, { force: true })
        return { sha256, size: info.size, deduplicated: true }
      }
      renameSync(temporary, target)
      return { sha256, size: info.size, deduplicated: false }
    } catch (error) {
      rmSync(temporary, { force: true })
      throw error
    }
  }

  async restore(
    pluginId: string,
    params: { sha256: string; dirId: string; relativePath: string; overwrite?: boolean }
  ): Promise<{ restored: boolean; size: number }> {
    const source = this.objectPath(pluginId, params.sha256)
    const sourceInfo = lstatSync(source, { throwIfNoEntry: false })
    if (sourceInfo === undefined || !sourceInfo.isFile()) {
      throw appError(AppErrorCode.NotFound, 'blob object not found', { field: 'sha256' })
    }
    const { absolute } = this.files.resolve(pluginId, params.dirId, params.relativePath, {
      allowMissing: true
    })
    const existing = lstatSync(absolute, { throwIfNoEntry: false })
    if (existing?.isSymbolicLink()) {
      throw appError(AppErrorCode.PathEscape, 'symbolic links are not allowed', {
        field: 'relativePath'
      })
    }
    if (existing !== undefined && params.overwrite !== true) {
      throw appError(AppErrorCode.Conflict, 'target already exists; pass overwrite to replace it', {
        field: 'relativePath'
      })
    }
    mkdirSync(path.dirname(absolute), { recursive: true, mode: 0o755 })
    const temporary = `${absolute}.restore-${process.pid}-${Date.now().toString(36)}`
    try {
      await pipeline(createReadStream(source), createWriteStream(temporary, { mode: 0o644 }))
      renameSync(temporary, absolute)
    } catch (error) {
      rmSync(temporary, { force: true })
      throw error
    }
    return { restored: true, size: sourceInfo.size }
  }

  delete(pluginId: string, sha256: string): { deleted: boolean } {
    const target = this.objectPath(pluginId, sha256)
    if (!existsSync(target)) return { deleted: false }
    rmSync(target)
    return { deleted: true }
  }

  stats(pluginId: string): PluginBlobStats {
    const dir = this.blobsDir(pluginId)
    if (!existsSync(dir)) return { objectCount: 0, totalBytes: 0 }
    let objectCount = 0
    let totalBytes = 0
    for (const entry of readdirSync(dir)) {
      if (!SHA256_PATTERN.test(entry)) continue
      const info = statSync(path.join(dir, entry), { throwIfNoEntry: false })
      if (info === undefined || !info.isFile()) continue
      objectCount += 1
      totalBytes += info.size
    }
    return { objectCount, totalBytes }
  }
}
