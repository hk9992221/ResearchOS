import { chmodSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import * as path from 'node:path'

import { formatTimestamp, isTimestamp, type Clock, SystemClock } from '../clock'
import { AppError, AppErrorCode, appError } from '../errors'

/** Workspace marker file name. */
export const MARKER_NAME = '.researchos'
export const MARKER_APP = 'researchos'
export const MARKER_SCHEMA_VERSION = 1

export interface Marker {
  app: string
  schema_version: number
  created_at: string
}

export type MarkerState = 'present' | 'missing' | 'invalid'

export interface MarkerRead {
  state: MarkerState
  marker?: Marker
}

export function markerPath(root: string): string {
  return path.join(root, MARKER_NAME)
}

/**
 * Reads and validates the marker. An unreadable file is corruption; a file that
 * exists but does not describe a ResearchOS workspace is `invalid` without an
 * exception so callers can distinguish "not ours" from "damaged".
 */
export function readMarker(root: string): MarkerRead {
  let body: string
  try {
    body = readFileSync(markerPath(root), 'utf8')
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') return { state: 'missing' }
    throw appError(AppErrorCode.WorkspaceCorrupt, `workspace: read marker: ${String(error)}`, { cause: error })
  }
  let parsed: unknown
  try {
    parsed = JSON.parse(body)
  } catch {
    return { state: 'invalid' }
  }
  if (typeof parsed !== 'object' || parsed === null) return { state: 'invalid' }
  const record = parsed as Record<string, unknown>
  if (record.app !== MARKER_APP) return { state: 'invalid' }
  if (record.schema_version !== MARKER_SCHEMA_VERSION) return { state: 'invalid' }
  if (typeof record.created_at !== 'string' || !isTimestamp(record.created_at)) return { state: 'invalid' }
  return {
    state: 'present',
    marker: {
      app: MARKER_APP,
      schema_version: MARKER_SCHEMA_VERSION,
      created_at: record.created_at
    }
  }
}

/** Creates the marker. Only init calls this; open/scan/repoint never rewrite it. */
export function writeMarker(root: string, clock: Clock = new SystemClock()): Marker {
  const marker: Marker = {
    app: MARKER_APP,
    schema_version: MARKER_SCHEMA_VERSION,
    created_at: formatTimestamp(clock.now())
  }
  const file = markerPath(root)
  try {
    writeFileSync(file, JSON.stringify(marker), { mode: 0o644 })
    chmodSync(file, 0o644)
  } catch (error) {
    throw appError(AppErrorCode.WorkspaceCorrupt, `workspace: write marker: ${String(error)}`, { cause: error })
  }
  return marker
}

/**
 * True when the directory holds no meaningful entries. The two OS droppings
 * (` .DS_Store`, `desktop.ini`) never count as foreign content.
 */
export function isDirEmpty(root: string): boolean {
  for (const entry of readdirSync(root, { withFileTypes: true })) {
    if (entry.name === '.DS_Store' || entry.name === 'desktop.ini') continue
    return false
  }
  return true
}