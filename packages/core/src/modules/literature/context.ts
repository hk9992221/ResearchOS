/**
 * Shared state and guards of the literature services. One instance is
 * workspace-scoped; every operation names the project it touches.
 */
import { type Clock, SystemClock, formatTimestamp } from '../../clock'
import type { Logger } from '../../logger'
import { EventBus } from '../../events'
import { appError, AppErrorCode } from '../../errors'
import type { GlobalHandle, ProjectHandle } from '../../workspace/handle'
import type { WorkspaceManager } from '../../workspace'
import { RelationService } from '../../relations'
import type { LiteratureIndexHook, LiteratureNotesPort } from './deps'
import { PaperFileStore } from './library/files'
import type { ZoteroSource } from './types'

export interface LiteratureContextOptions {
  workspace: WorkspaceManager
  clock?: Clock
  zotero?: ZoteroSource
  notes: LiteratureNotesFactory
  events?: EventBus
  indexer?: LiteratureIndexHook
  logger?: Logger
  timeZone?: string
}

export type LiteratureNotesFactory = (projectId: string) => LiteratureNotesPort

export interface ProjectInfo {
  id: string
  name: string
  status: 'active' | 'archived'
}

export class LiteratureContext {
  readonly workspace: WorkspaceManager
  readonly clock: Clock
  readonly zotero: ZoteroSource | undefined
  private readonly notesFactory: LiteratureNotesFactory
  readonly events: EventBus
  readonly files: PaperFileStore
  private readonly indexer: LiteratureIndexHook | undefined
  private readonly logger: Logger | undefined

  constructor(options: LiteratureContextOptions) {
    this.workspace = options.workspace
    this.clock = options.clock ?? new SystemClock()
    this.zotero = options.zotero
    this.notesFactory = options.notes
    this.events =
      options.events ??
      new EventBus({
        clock: this.clock,
        ...(options.timeZone === undefined ? {} : { timeZone: options.timeZone })
      })
    this.files = new PaperFileStore(options.workspace.paths)
    this.indexer = options.indexer
    this.logger = options.logger
  }

  now(): string {
    return formatTimestamp(this.clock.now())
  }

  global(): GlobalHandle {
    return this.workspace.global
  }

  project(projectId: string): ProjectHandle {
    this.requireProject(projectId)
    return this.workspace.project(projectId)
  }

  /** Registered project metadata; missing is NOT_FOUND. */
  projectInfo(projectId: string): ProjectInfo {
    const row = this.global().db
      .prepare(`SELECT id, name, status FROM projects WHERE id = ?`)
      .get(projectId) as { id: string; name: string; status: string } | undefined
    if (row === undefined) {
      throw appError(AppErrorCode.NotFound, `项目 ${projectId} 不存在`, {
        details: { project_id: projectId }
      })
    }
    return {
      id: row.id,
      name: row.name,
      status: row.status === 'archived' ? 'archived' : 'active'
    }
  }

  requireProject(projectId: string): ProjectInfo {
    return this.projectInfo(projectId)
  }

  /** Rejects an archived project for every write path. */
  requireActive(projectId: string): ProjectInfo {
    const info = this.projectInfo(projectId)
    if (info.status === 'archived') {
      throw appError(AppErrorCode.ArchivedReadOnly, `项目《${info.name}》已归档，只能查看`, {
        details: { project_id: projectId }
      })
    }
    return info
  }

  requireZotero(): ZoteroSource {
    if (this.zotero === undefined) {
      throw appError(AppErrorCode.ExternalUnavailable, 'Zotero 未配置，无法读取文献库')
    }
    return this.zotero
  }

  notesFor(projectId: string): LiteratureNotesPort {
    this.requireProject(projectId)
    return this.notesFactory(projectId)
  }

  relations(handle: ProjectHandle): RelationService {
    return new RelationService(handle, this.events, {
      global: this.global(),
      clock: this.clock
    })
  }

  inTx<T>(handle: ProjectHandle | GlobalHandle, fn: () => T): T {
    const value = handle.inTx(fn)
    this.events.flush()
    return value
  }

  notifyIndex(paperId: string, eventType: string, summary: string): void {
    if (this.indexer === undefined || paperId === '') return
    try {
      this.indexer.onPaperChanged(paperId, eventType, summary)
    } catch (error) {
      this.logger?.warn('literature index hook failed', {
        paperId,
        reason: error instanceof Error ? error.message : String(error)
      })
    }
  }

  listProjectIds(): string[] {
    const rows = this.global().db
      .prepare(`SELECT id FROM projects ORDER BY created_at, id`)
      .all() as Array<{ id: string }>
    return rows.map((row) => row.id)
  }
}