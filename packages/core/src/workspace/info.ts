import * as path from 'node:path'

import type { WorkspaceManager } from './index'
import { dbFilesSize, dirSize, diskSpace } from './disk'

export interface WorkspaceInfo {
  root: string
  freeBytes: number
  totalBytes: number
  dbBytes: number
  libraryBytes: number
  exportsBytes: number
  projectCount: number
  archivedCount: number
}

export interface ProjectFootprint {
  id: string
  name: string
  bytes: number
  noteCount: number
  attachmentBytes: number
  archived: boolean
}

export interface Footprint {
  projects: ProjectFootprint[]
  libraryBytes: number
  exportsBytes: number
  totalBytes: number
}

/** Concurrent per-project measurements. */
const FOOTPRINT_WORKERS = 4

export function workspaceInfo(manager: WorkspaceManager): WorkspaceInfo {
  const global = manager.global
  const root = manager.root
  const info: WorkspaceInfo = {
    root,
    freeBytes: 0,
    totalBytes: 0,
    dbBytes: dbFilesSize(path.join(root, 'global.db')),
    libraryBytes: dirSize(path.join(root, 'library')),
    exportsBytes: dirSize(path.join(root, 'exports')),
    projectCount: 0,
    archivedCount: 0
  }
  const counts = global.db
    .prepare('SELECT status, COUNT(*) AS n FROM projects GROUP BY status')
    .all() as Array<{ status: string; n: number }>
  for (const row of counts) {
    info.projectCount += row.n
    if (row.status === 'archived') info.archivedCount += row.n
  }
  const space = diskSpace(root)
  info.freeBytes = space.free
  info.totalBytes = space.total
  return info
}

/**
 * Measures every registered project plus library and exports. A project whose
 * database cannot be opened still contributes its directory sizes; only the
 * note count degrades to zero.
 */
export async function workspaceFootprint(manager: WorkspaceManager): Promise<Footprint> {
  const global = manager.global
  const root = manager.root
  const rows = global.db
    .prepare('SELECT id, name, status FROM projects ORDER BY name, id')
    .all() as Array<{ id: string; name: string; status: string }>

  const projects: ProjectFootprint[] = new Array(rows.length)
  let cursor = 0
  const worker = async (): Promise<void> => {
    for (;;) {
      const index = cursor
      cursor += 1
      if (index >= rows.length) return
      projects[index] = measureProject(manager, root, rows[index]!)
    }
  }
  await Promise.all(
    Array.from({ length: Math.min(FOOTPRINT_WORKERS, rows.length) }, () => worker())
  )

  const footprint: Footprint = {
    projects,
    libraryBytes: dirSize(path.join(root, 'library')),
    exportsBytes: dirSize(path.join(root, 'exports')),
    totalBytes: dbFilesSize(path.join(root, 'global.db'))
  }
  footprint.totalBytes += footprint.libraryBytes + footprint.exportsBytes
  for (const project of projects) footprint.totalBytes += project.bytes
  return footprint
}

function measureProject(manager: WorkspaceManager, root: string, row: { id: string; name: string; status: string }): ProjectFootprint {
  const directory = path.join(root, 'projects', row.id)
  const footprint: ProjectFootprint = {
    id: row.id,
    name: row.name,
    bytes: dirSize(directory),
    noteCount: 0,
    attachmentBytes: dirSize(path.join(directory, 'attachments')),
    archived: row.status === 'archived'
  }
  try {
    const handle = manager.project(row.id)
    const count = handle.db
      .prepare('SELECT count(*) AS n FROM notes WHERE deleted_at IS NULL')
      .get() as { n: number }
    footprint.noteCount = count.n
  } catch {
    // Missing or unreadable project database: keep the byte totals.
  }
  return footprint
}