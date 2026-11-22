import { closeSync, openSync, statSync, unlinkSync } from 'node:fs'
import { randomUUID } from 'node:crypto'
import * as path from 'node:path'

import { diskFree } from './disk'
import { isDirEmpty, MARKER_NAME } from './marker'

/** Read-only verdict on a candidate workspace root. */
export interface Probe {
  root: string
  exists: boolean
  empty: boolean
  writable: boolean
  freeBytes: number
  /** Ancestor or descendant of the current root. */
  nested: boolean
  sameAsCurrent: boolean
  /** Non-empty and not recognizable as a ResearchOS workspace. */
  hasForeignData: boolean
  problems: string[]
}

function fileExists(target: string): boolean {
  try {
    return !statSync(target).isDirectory()
  } catch {
    return false
  }
}

function exists(target: string): boolean {
  try {
    statSync(target)
    return true
  } catch {
    return false
  }
}

function isDirectory(target: string): boolean {
  try {
    return statSync(target).isDirectory()
  } catch {
    return false
  }
}

/** Path comparison folds case only on Windows, matching its filesystem. */
function normPath(target: string): string {
  const cleaned = path.resolve(target)
  return process.platform === 'win32' ? cleaned.toLowerCase() : cleaned
}

type PathRelation = 'none' | 'same' | 'ancestor' | 'descendant'

/**
 * Classifies how `target` relates to `base`. Only pure `..` segments make the
 * target an ancestor; a shared ancestor with a sibling (`../sibling`) is not
 * nesting.
 */
export function pathRelation(base: string, target: string): PathRelation {
  const normalizedBase = normPath(base)
  const normalizedTarget = normPath(target)
  if (normalizedBase === normalizedTarget) return 'same'
  const relative = path.relative(normalizedBase, normalizedTarget)
  if (relative === '') return 'same'
  if (path.isAbsolute(relative)) return 'none'
  const segments = relative.split(path.sep)
  if (segments.every((segment) => segment === '..')) return 'ancestor'
  if (segments[0] === '..' || segments[0] === '.') return 'none'
  return 'descendant'
}

/** Deepest existing directory at or above `target`; empty only on broken roots. */
function firstExistingDir(target: string): string {
  let current = path.resolve(target)
  for (;;) {
    if (isDirectory(current)) return current
    const parent = path.dirname(current)
    if (parent === current) return ''
    current = parent
  }
}

/** Temp-file writability probe; never creates a directory and cleans up. */
function writableDir(dir: string): boolean {
  const probe = path.join(dir, `.researchos-probe-${randomUUID()}`)
  try {
    const fd = openSync(probe, 'wx', 0o600)
    closeSync(fd)
    unlinkSync(probe)
    return true
  } catch {
    try {
      unlinkSync(probe)
    } catch {
      // Nothing to clean up.
    }
    return false
  }
}

/**
 * Inspects a candidate root without creating or modifying anything that
 * survives the call. The target may not exist; its parent must then be an
 * existing writable directory.
 */
export function probeRoot(currentRoot: string, candidate: string): Probe {
  const absolute = path.resolve(candidate)
  const probe: Probe = {
    root: absolute,
    exists: false,
    empty: false,
    writable: false,
    freeBytes: 0,
    nested: false,
    sameAsCurrent: false,
    hasForeignData: false,
    problems: []
  }

  switch (pathRelation(currentRoot, absolute)) {
    case 'same':
      probe.sameAsCurrent = true
      probe.problems.push(`${absolute} is the current workspace root`)
      break
    case 'ancestor':
      probe.nested = true
      probe.problems.push(`${absolute} contains the current workspace root ${currentRoot}`)
      break
    case 'descendant':
      probe.nested = true
      probe.problems.push(`${absolute} is inside the current workspace root ${currentRoot}`)
      break
    case 'none':
      break
  }

  const anchor = firstExistingDir(absolute)
  const space = anchor === '' ? { free: 0, total: 0, ok: false } : diskFree(anchor)
  probe.freeBytes = space.free
  if (!space.ok) probe.problems.push(`cannot determine free disk space for ${absolute}`)

  if (!exists(absolute)) {
    const parent = path.dirname(absolute)
    if (!isDirectory(parent)) {
      probe.writable = false
      probe.problems.push(`parent directory ${parent} does not exist`)
      return probe
    }
    probe.writable = writableDir(parent)
    if (!probe.writable) probe.problems.push(`parent directory ${parent} is not writable`)
    return probe
  }

  probe.exists = true
  if (!isDirectory(absolute)) {
    probe.problems.push(`${absolute} exists but is not a directory`)
    return probe
  }

  try {
    probe.empty = isDirEmpty(absolute)
  } catch (error) {
    probe.problems.push(`cannot read ${absolute}: ${String(error)}`)
  }
  const hasMarker = fileExists(path.join(absolute, MARKER_NAME))
  const hasGlobal = fileExists(path.join(absolute, 'global.db'))
  if (!probe.empty && (!hasMarker || !hasGlobal)) {
    probe.hasForeignData = true
    probe.writable = false
    probe.problems.push(
      `${absolute} already contains files but is not a ResearchOS workspace ` +
        `(missing ${MARKER_NAME} marker or global.db); it will not be touched`
    )
    return probe
  }
  probe.writable = writableDir(absolute)
  if (!probe.writable) probe.problems.push(`${absolute} is not writable`)
  return probe
}
