import { readdirSync, statfsSync, statSync } from 'node:fs'
import * as path from 'node:path'

/** Free/total bytes of the volume holding a path. */
export interface DiskSpace {
  free: number
  total: number
  ok: boolean
}

/**
 * Reports the bytes available to the calling user and the volume total. Node's
 * statfs maps to statvfs(2) / GetDiskFreeSpaceEx on every supported platform.
 */
export function diskSpace(target: string): DiskSpace {
  if (target === '') return { free: 0, total: 0, ok: false }
  try {
    const stats = statfsSync(path.resolve(target))
    const blockSize = Number(stats.bsize)
    const free = Number(stats.bavail) * blockSize
    const total = Number(stats.blocks) * blockSize
    if (!Number.isFinite(free) || !Number.isFinite(total)) return { free: 0, total: 0, ok: false }
    return { free, total, ok: true }
  } catch {
    return { free: 0, total: 0, ok: false }
  }
}

/**
 * Like `diskSpace`, but the path may not exist yet: it walks up to the nearest
 * existing ancestor so callers can reserve space before creating a directory.
 */
export function diskFree(target: string): DiskSpace {
  if (target === '') return { free: 0, total: 0, ok: false }
  let current = path.resolve(target)
  for (;;) {
    try {
      statSync(current)
      return diskSpace(current)
    } catch {
      const parent = path.dirname(current)
      if (parent === current) return { free: 0, total: 0, ok: false }
      current = parent
    }
  }
}

/**
 * Sums regular files under `dir`. Symlinks are never followed and unreadable
 * entries are skipped; a missing directory contributes 0.
 */
export function dirSize(dir: string): number {
  let total = 0
  let entries
  try {
    entries = readdirSync(dir, { withFileTypes: true })
  } catch {
    return 0
  }
  for (const entry of entries) {
    if (entry.isSymbolicLink()) continue
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) {
      total += dirSize(full)
      continue
    }
    if (!entry.isFile()) continue
    try {
      total += statSync(full).size
    } catch {
      // Vanished between readdir and stat: not workspace data any more.
    }
  }
  return total
}

/** Sums a SQLite database and its existing `-wal` / `-shm` siblings. */
export function dbFilesSize(file: string): number {
  let total = 0
  for (const suffix of ['', '-wal', '-shm']) {
    try {
      const stats = statSync(`${file}${suffix}`)
      if (stats.isFile()) total += stats.size
    } catch {
      // Missing sidecar.
    }
  }
  return total
}