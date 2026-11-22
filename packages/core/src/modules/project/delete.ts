import { existsSync, rmSync } from 'node:fs'
import * as path from 'node:path'

import type { DeleteProjectPlan, DeleteProjectResult } from '@researchos/shared'
import { AppErrorCode } from '@researchos/shared'

import { formatTimestamp } from '../../clock'
import { appError } from '../../errors'
import type { Db } from '../../db/open'
import { deleteNotConfirmed, featureUnavailable, projectNotFound } from './errors'
import type { ProjectService } from './service'
import { directoryBytes } from './util'

/**
 * Permanent-delete pipeline. The impact plan is read-only and
 * never mutates the project; the delete itself is only reachable with a matching
 * project name plus a one-time in-app confirmation token. Shared paper copies
 * survive because a copy is removed only when no registered project references
 * it any more.
 */

/** §5.4 entity count list: every table keeps purged rows out by deleting them. */
const DELETE_COUNT_TABLES: ReadonlyArray<{ table: string; key: string }> = [
  { table: 'notes', key: 'note' },
  { table: 'ideas', key: 'idea' },
  { table: 'experiments', key: 'experiment' },
  { table: 'runs', key: 'run' },
  { table: 'tasks', key: 'task' },
  { table: 'evidence', key: 'evidence' },
  { table: 'weekly_reports', key: 'weekly_report' }
]

function countAll(db: Db, table: string): number {
  const row = db.prepare(`SELECT count(*) AS n FROM ${table}`).get() as { n: number } | undefined
  return row === undefined ? 0 : Number(row.n)
}

function projectPaperIds(db: Db): string[] {
  const rows = db.prepare(`SELECT DISTINCT paper_id FROM project_papers`).all() as Array<{
    paper_id: string
  }>
  if (rows.length > 0) return [...new Set(rows.map((row) => row.paper_id))].sort()

  // Fallback for a hand-built project: references relations pointing at a paper.
  const relations = db
    .prepare(`SELECT DISTINCT dst_id FROM entity_relations WHERE dst_type = 'paper'`)
    .all() as Array<{ dst_id: string }>
  return [...new Set(relations.map((row) => row.dst_id))].sort()
}

/**
 * Builds the §5.4 impact preview. `issueToken` is true for the desktop
 * `project.delete.plan` command (the renderer cannot reach the in-process
 * confirmation service) and false for the MCP `dry_run`, which must never
 * receive a token.
 */
export function prepareDeletePlan(
  service: ProjectService,
  projectId: string,
  options: { issueToken: boolean }
): DeleteProjectPlan {
  const project = service.get(projectId)
  const db = service.projectDb(projectId)

  const entityCounts: Record<string, number> = {}
  for (const entry of DELETE_COUNT_TABLES) entityCounts[entry.key] = countAll(db, entry.table)

  const candidates = projectPaperIds(db)
  const { shared, exclusive, unknown } = paperImpact(service, projectId, candidates)

  const last = db
    .prepare(`SELECT max(created_at) AS last FROM activities WHERE event_type = 'project.exported'`)
    .get() as { last: string | null }

  const blockers: DeleteProjectPlan['blockers'] = []
  if (service.workspace.workspaceLock.isHeld) {
    blockers.push({ code: 'migration_running', message: 'a workspace migration is running' })
  }
  const busy = service.busyCode(projectId)
  if (busy !== null) blockers.push({ code: busy, message: blockerMessage(busy) })

  let confirmationToken: string | null = null
  if (options.issueToken) {
    if (service.confirmations === undefined) throw featureUnavailable('confirmations')
    confirmationToken = service.confirmations.issue('delete_project', [projectId])
  }

  return {
    projectId,
    projectName: project.name,
    entityCounts,
    trashedCount: countAllView(db),
    dirBytes: directoryBytes(service.projectDir(projectId)),
    paperIds: candidates,
    sharedPaperIds: shared,
    exclusivePaperIds: exclusive,
    paperImpactUnknown: unknown,
    lastExportedAt: last?.last ?? null,
    exportRecommended: (last?.last ?? null) === null,
    blockers,
    preparedAt: formatTimestamp(service.clock.now()),
    confirmationToken
  }
}

function countAllView(db: Db): number {
  const row = db.prepare(`SELECT count(*) AS n FROM v_trash`).get() as { n: number } | undefined
  return row === undefined ? 0 : Number(row.n)
}

/**
 * Splits the candidate papers into those another project still references and
 * those only this project held. An unreadable project database makes the whole
 * split unknown: retention then keeps every copy instead of guessing.
 */
function paperImpact(
  service: ProjectService,
  projectId: string,
  candidates: string[]
): { shared: string[]; exclusive: string[]; unknown: boolean } {
  if (candidates.length === 0) return { shared: [], exclusive: [], unknown: false }
  const candidatesSet = new Set(candidates)
  const sharedSet = new Set<string>()
  let unknown = false

  const others = service
    .list({ includeArchived: true })
    .map((project) => project.id)
    .filter((id) => id !== projectId)

  for (const other of others) {
    let referenced: string[]
    try {
      referenced = projectPaperIds(service.projectDb(other))
    } catch {
      unknown = true
      continue
    }
    for (const paperId of referenced) {
      if (candidatesSet.has(paperId)) sharedSet.add(paperId)
    }
  }

  const shared = candidates.filter((id) => sharedSet.has(id))
  const exclusive = unknown ? [] : candidates.filter((id) => !sharedSet.has(id))
  return { shared, exclusive, unknown }
}

function blockerMessage(code: string): string {
  switch (code) {
    case 'export_running':
      return 'an export job is running for this project'
    case 'project_open_busy':
      return 'the project is busy and cannot be deleted yet'
    case 'migration_running':
      return 'a workspace migration is running'
    default:
      return code
  }
}

function escapeLike(value: string): string {
  return value.replaceAll('\\', '\\\\').replaceAll('%', '\\%').replaceAll('_', '\\_')
}

/**
 * Executes the §5.4 deletion: validate confirmation → close handles → remove
 * `projects/<id>` → delete the registration row and project settings → collect
 * unreferenced paper copies. No Activity row is written because the project
 * database disappears with the project.
 */
export function executePermanentDelete(
  service: ProjectService,
  request: { projectId: string; confirmName: string; token: string }
): DeleteProjectResult {
  const started = Date.now()
  const project = service.get(request.projectId)
  const confirmName = request.confirmName.trim()
  if (confirmName === '' || request.token.trim() === '') {
    throw deleteNotConfirmed('confirmName and token are required')
  }
  if (confirmName !== project.name.trim()) {
    throw deleteNotConfirmed('confirmName does not match the project name')
  }
  if (service.confirmations === undefined) throw featureUnavailable('confirmations')
  // Consume before any side effect: a wrong scope must not delete anything.
  service.confirmations.consume(request.token, 'delete_project', [request.projectId])

  const release = service.workspace.workspaceLock.tryAcquire()
  if (release === undefined) {
    throw appError(AppErrorCode.Conflict, '工作区正忙，暂时无法删除项目', {
      details: { blocker: 'migration_running' }
    })
  }
  const warnings: string[] = []
  try {
    const directory = service.projectDir(request.projectId)
    if (!existsSync(path.join(directory, 'project.db'))) throw projectNotFound(request.projectId)

    const candidates = projectPaperIds(service.projectDb(request.projectId))
    service.close(request.projectId)
    service.workspace.closeProject(request.projectId)

    rmSync(directory, { recursive: true, force: true })

    const global = service.workspace.global
    global.inTx(() => {
      global.db.prepare(`DELETE FROM projects WHERE id = ?`).run(request.projectId)
      global.db
        .prepare(`DELETE FROM settings WHERE key LIKE ? ESCAPE '\\'`)
        .run(`project.${escapeLike(request.projectId)}.%`)
    })

    const removed: string[] = []
    const retained: string[] = []
    for (const paperId of candidates) {
      if (isPaperReferenced(service, paperId, request.projectId, warnings)) {
        retained.push(paperId)
        continue
      }
      try {
        global.inTx(() => {
          global.db.prepare(`DELETE FROM papers WHERE id = ?`).run(paperId)
        })
        removed.push(paperId)
        const paperDirectory = service.workspace.paths.abs(service.workspace.paths.paperDir(paperId))
        if (existsSync(paperDirectory)) rmSync(paperDirectory, { recursive: true, force: true })
      } catch (error) {
        warnings.push(
          `paper copy ${paperId} could not be removed: ${error instanceof Error ? error.message : String(error)}`
        )
      }
    }

    return {
      projectId: request.projectId,
      removedPapers: removed,
      retainedPapers: retained,
      warnings,
      durationMs: Date.now() - started
    }
  } finally {
    release()
  }
}

/**
 * True when any *other* registered project still links the paper. An unreadable
 * project is treated as "referenced" and emits a warning, so a copy is never
 * deleted while its only evidence could not be read.
 */
function isPaperReferenced(
  service: ProjectService,
  paperId: string,
  deletedProject: string,
  warnings: string[]
): boolean {
  for (const project of service.list({ includeArchived: true })) {
    if (project.id === deletedProject) continue
    try {
      const row = service
        .projectDb(project.id)
        .prepare(`SELECT 1 AS x FROM project_papers WHERE paper_id = ? LIMIT 1`)
        .get(paperId)
      if (row !== undefined) return true
    } catch (error) {
      warnings.push(
        `project ${project.id} could not be read; paper ${paperId} retained: ${
          error instanceof Error ? error.message : String(error)
        }`
      )
      return true
    }
  }
  return false
}