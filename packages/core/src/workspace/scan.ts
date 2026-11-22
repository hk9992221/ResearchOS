import { readdirSync, statSync } from 'node:fs'
import * as path from 'node:path'

import type { Db } from './handle'
import { inspectDatabase } from '../db/migrate'
import { readMarker, type MarkerState } from './marker'

export interface WorkspaceScan {
  marker: MarkerState
  /** Registered projects whose directory is gone. */
  registeredWithoutDir: string[]
  /** Registered projects whose project.db is gone. */
  registeredWithoutDb: string[]
  /** `projects/<id>/` directories with no registration. */
  unregisteredDirs: string[]
  /** `"<projectID>: <reason>"`; the global database uses the `global` prefix. */
  schemaIssues: string[]
}

function fileExists(target: string): boolean {
  try {
    return !statSync(target).isDirectory()
  } catch {
    return false
  }
}

/**
 * Compares the global project registry with the directory tree and checks the
 * schema state of every project database. Reports, never repairs.
 */
export function scanWorkspace(root: string, global: Db): WorkspaceScan {
  const scan: WorkspaceScan = {
    marker: readMarker(root).state,
    registeredWithoutDir: [],
    registeredWithoutDb: [],
    unregisteredDirs: [],
    schemaIssues: []
  }

  const registered = new Set(
    (global.prepare('SELECT id FROM projects').pluck().all() as string[]).map((id) => String(id))
  )

  const projectsDir = path.join(root, 'projects')
  const dirSet = new Set<string>()
  try {
    for (const entry of readdirSync(projectsDir, { withFileTypes: true })) {
      if (entry.isDirectory()) dirSet.add(entry.name)
    }
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error
  }

  for (const id of registered) {
    if (!dirSet.has(id)) {
      scan.registeredWithoutDir.push(id)
      continue
    }
    const dbPath = path.join(projectsDir, id, 'project.db')
    if (!fileExists(dbPath)) {
      scan.registeredWithoutDb.push(id)
      continue
    }
    const inspection = inspectDatabase(dbPath, 'project')
    for (const issue of inspection.issues) scan.schemaIssues.push(`${id}: ${issue}`)
  }
  for (const id of dirSet) {
    if (!registered.has(id)) scan.unregisteredDirs.push(id)
  }

  const globalInspection = inspectDatabase(path.join(root, 'global.db'), 'global')
  for (const issue of globalInspection.issues) scan.schemaIssues.push(`global: ${issue}`)

  scan.registeredWithoutDir.sort()
  scan.registeredWithoutDb.sort()
  scan.unregisteredDirs.sort()
  scan.schemaIssues.sort()
  return scan
}