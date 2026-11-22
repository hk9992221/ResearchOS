/**
 * Imported PDF copies below `<workspace>/library/papers/<paper-id>/source.pdf`
 * A copy is written to a same-directory temporary file,
 * fsynced and renamed: the target is never truncated. The store refuses to
 * overwrite an existing copy; replacement is only LIT-09's confirmed path.
 */
import { createHash } from 'node:crypto'
import {
  closeSync,
  existsSync,
  fsyncSync,
  mkdirSync,
  openSync,
  readSync,
  renameSync,
  rmSync,
  statSync,
  writeSync
} from 'node:fs'
import * as path from 'node:path'

import { appError, AppErrorCode } from '../../../errors'
import { isValidId } from '../../../ids'
import type { Paths } from '../../../paths'

const COPY_BUFFER = 1024 * 1024
const SOURCE_NAME = 'source.pdf'

export interface StoredCopy {
  relativePath: string
  hash: string
  size: number
  mtime: Date
}

export interface CopyTarget {
  absolutePath: string
  size: number
  mtime: Date
}

export function validPaperId(paperId: string): void {
  if (!isValidId(paperId)) {
    throw appError(AppErrorCode.InvalidArgument, `无效的文献 ID ${JSON.stringify(paperId)}`, {
      details: { field: 'paper_id' }
    })
  }
}

/** Lowercase hex SHA-256 of a file, read in 1 MiB chunks. */
export function hashFile(file: string): string {
  const fd = openSync(file, 'r')
  try {
    const hash = createHash('sha256')
    const buffer = Buffer.allocUnsafe(COPY_BUFFER)
    for (;;) {
      const read = readSync(fd, buffer, 0, buffer.length, null)
      if (read === 0) break
      hash.update(buffer.subarray(0, read))
    }
    return hash.digest('hex')
  } finally {
    closeSync(fd)
  }
}

export class PaperFileStore {
  private readonly paths: Paths

  constructor(paths: Paths) {
    this.paths = paths
  }

  private paperDir(paperId: string): string {
    validPaperId(paperId)
    return this.paths.abs(this.paths.paperDir(paperId))
  }

  stat(paperId: string): CopyTarget {
    validPaperId(paperId)
    const absolutePath = this.paths.abs(this.paths.paperPdf(paperId))
    let info
    try {
      info = statSync(absolutePath)
    } catch (cause) {
      throw appError(AppErrorCode.NotFound, `文献副本不存在：${paperId}`, {
        cause,
        details: { paper_id: paperId }
      })
    }
    if (!info.isFile()) {
      throw appError(AppErrorCode.NotFound, `文献副本不是普通文件：${paperId}`, {
        details: { paper_id: paperId }
      })
    }
    return { absolutePath, size: info.size, mtime: info.mtime }
  }

  missing(paperId: string): boolean {
    try {
      this.stat(paperId)
      return false
    } catch {
      return true
    }
  }

  /**
   * Copies `sourcePath` into the paper's directory. An existing target is a
   * CONFLICT: only the sync-apply path may replace it.
   */
  store(paperId: string, sourcePath: string, sizeHint: number): StoredCopy {
    validPaperId(paperId)
    const dir = this.paperDir(paperId)
    mkdirSync(dir, { recursive: true, mode: 0o755 })
    const target = path.join(dir, SOURCE_NAME)
    if (existsSync(target)) {
      throw appError(AppErrorCode.Conflict, `文献副本已存在，拒绝覆盖：${paperId}`, {
        details: { paper_id: paperId }
      })
    }
    const temp = path.join(dir, `.${SOURCE_NAME}.${process.pid}.${Date.now()}.tmp`)
    const src = openSync(sourcePath, 'r')
    let out: number | undefined
    try {
      out = openSync(temp, 'wx', 0o644)
      const hash = createHash('sha256')
      const buffer = Buffer.allocUnsafe(COPY_BUFFER)
      let written = 0
      for (;;) {
        const read = readSync(src, buffer, 0, buffer.length, null)
        if (read === 0) break
        writeSync(out, buffer, 0, read)
        hash.update(buffer.subarray(0, read))
        written += read
      }
      if (sizeHint > 0 && written !== sizeHint) {
        throw appError(
          AppErrorCode.Integrity,
          `复制大小不一致：期望 ${sizeHint} 字节，实际 ${written} 字节`,
          { details: { paper_id: paperId, expected: sizeHint, actual: written } }
        )
      }
      fsyncSync(out)
      closeSync(out)
      out = undefined
      if (existsSync(target)) {
        throw appError(AppErrorCode.Conflict, `文献副本已存在，拒绝覆盖：${paperId}`, {
          details: { paper_id: paperId }
        })
      }
      renameSync(temp, target)
      const info = statSync(target)
      return {
        relativePath: this.paths.paperPdf(paperId),
        hash: hash.digest('hex'),
        size: written,
        mtime: info.mtime
      }
    } finally {
      closeSync(src)
      if (out !== undefined) closeSync(out)
      rmSync(temp, { force: true })
    }
  }

  /**
   * Atomically replaces the copy after an explicit override approval. The old
   * copy is kept as `.bak` until the new bytes are in place.
   */
  replace(paperId: string, sourcePath: string): StoredCopy {
    validPaperId(paperId)
    const dir = this.paperDir(paperId)
    mkdirSync(dir, { recursive: true, mode: 0o755 })
    const target = path.join(dir, SOURCE_NAME)
    const backup = `${target}.bak`
    const temp = path.join(dir, `.${SOURCE_NAME}.${process.pid}.${Date.now()}.tmp`)
    const src = openSync(sourcePath, 'r')
    let out: number | undefined
    try {
      out = openSync(temp, 'wx', 0o644)
      const hash = createHash('sha256')
      const buffer = Buffer.allocUnsafe(COPY_BUFFER)
      let written = 0
      for (;;) {
        const read = readSync(src, buffer, 0, buffer.length, null)
        if (read === 0) break
        writeSync(out, buffer, 0, read)
        hash.update(buffer.subarray(0, read))
        written += read
      }
      fsyncSync(out)
      closeSync(out)
      out = undefined
      const digest = hash.digest('hex')
      const sourceDigest = hashFile(sourcePath)
      if (digest !== sourceDigest) {
        throw appError(AppErrorCode.Integrity, '复制内容与源文件哈希不一致', {
          details: { paper_id: paperId }
        })
      }
      rmSync(backup, { force: true })
      if (existsSync(target)) renameSync(target, backup)
      try {
        renameSync(temp, target)
      } catch (cause) {
        if (existsSync(backup)) renameSync(backup, target)
        throw cause
      }
      rmSync(backup, { force: true })
      const info = statSync(target)
      return {
        relativePath: this.paths.paperPdf(paperId),
        hash: digest,
        size: written,
        mtime: info.mtime
      }
    } finally {
      closeSync(src)
      if (out !== undefined) closeSync(out)
      rmSync(temp, { force: true })
    }
  }

  remove(paperId: string): void {
    validPaperId(paperId)
    const dir = this.paperDir(paperId)
    rmSync(path.join(dir, SOURCE_NAME), { force: true })
    rmSync(`${path.join(dir, SOURCE_NAME)}.bak`, { force: true })
    rmSync(dir, { recursive: false, force: true })
  }

  move(fromPaperId: string, toPaperId: string): void {
    validPaperId(fromPaperId)
    validPaperId(toPaperId)
    if (fromPaperId === toPaperId) return
    const fromDir = this.paperDir(fromPaperId)
    const toDir = this.paperDir(toPaperId)
    if (existsSync(toDir)) {
      throw appError(AppErrorCode.Conflict, `目标文献目录已存在：${toPaperId}`, {
        details: { paper_id: toPaperId }
      })
    }
    mkdirSync(path.dirname(toDir), { recursive: true, mode: 0o755 })
    renameSync(fromDir, toDir)
  }
}