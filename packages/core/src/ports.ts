/**
 * Consumer-side narrow ports for cross-module calls
 * (`internal/ports`). A module depends on these interfaces only, adapts its own
 * service to them, and never imports a sibling module's internals — so no
 * import cycle can form.
 *
 * Methods are asynchronous because the boundary they sit behind (IPC, MCP,
 * export I/O, AI) is asynchronous. A synchronous database service satisfies
 * them with `async` methods.
 */
import type { Actor } from './schema'

export type PortActor = Actor

export const ActorUser: Actor = 'user'
export const ActorMcp: Actor = 'mcp'
export const ActorAi: Actor = 'ai'

/** One entity of the current project. */
export interface EntityRef {
  type: string
  id: string
}

/** Navigable destinations UI components link to. */
export const ROUTE_KINDS = [
  'project',
  'overview',
  'resources',
  'activity',
  'settings',
  'note',
  'idea',
  'experiment',
  'run',
  'task',
  'paper',
  'weekly_report',
  'trash',
  'export'
] as const

export type RouteKind = (typeof ROUTE_KINDS)[number]

/**
 * A navigation target. `params` carries view hints only (for example
 * `view=blocked` on a task route); navigation writes no Activity.
 */
export interface Route {
  kind: RouteKind
  projectId: string
  entityId: string
  params?: Record<string, string>
}

/** Read-only literature projection. `missing` reports a lost local PDF copy. */
export interface PaperBrief {
  id: string
  title: string
  authors: string
  year: number
  linkedAt: Date
  missing: boolean
}

/** Read-only note projection. `deleted` marks a soft-deleted note. */
export interface NoteBrief {
  id: string
  title: string
  kind: string
  updatedAt: Date
  deleted: boolean
}

/** Read-only experiment projection. */
export interface ExperimentBrief {
  id: string
  title: string
  lastRunStatus: string
  updatedAt: Date
  deleted: boolean
}

/** Read-only task projection. `blockedReason` stays empty until adopted. */
export interface TaskBrief {
  id: string
  title: string
  column: string
  priority: number
  deadline?: Date
  blocked: boolean
  blockedReason: string
  updatedAt: Date
}

/** One row of a project's recycle bin. */
export interface TrashItem {
  entityType: string
  entityId: string
  title: string
  deletedAt: Date
}

/** One entity for a purge; the caller has already obtained the confirm token. */
export interface TrashRef {
  entityType: string
  entityId: string
}

/**
 * Serializes workspace-wide and per-project write operations (§5.2). It lives
 * in process memory: it never crosses the process boundary and never touches
 * disk.
 */
export interface Locker {
  /** Workspace lock for migration: exclusive and fail-fast while held. */
  withWorkspaceLock<T>(fn: () => T | Promise<T>): Promise<T>
  /**
   * Per-project write lock. Waits until the lock is free and fails fast while a
   * workspace (migration) lock is held.
   */
  withProjectWriteLock<T>(projectId: string, fn: () => T | Promise<T>): Promise<T>
}

/**
 * The literature port. Without it, export and delete previews fall
 * back to `project_papers` directly.
 */
export interface PaperIndex {
  /** De-duplicated paper ids linked to the project. */
  listProjectPaperIDs(projectId: string): Promise<string[]>
  /**
   * How many other projects (archived included) still reference the paper. It
   * only informs the impact preview;  executes the real GC.
   */
  countPaperRefs(paperId: string, excludeProjectId: string): Promise<number>
  listProjectPapers(projectId: string, limit: number): Promise<PaperBrief[]>
}

/** The Kanban port used by Overview quick actions. */
export interface TaskCompleter {
  completeTask(projectId: string, taskId: string, actor: PortActor): Promise<void>
}

/** The Notes port: created notes are always `kind=scratchpad`. */
export interface NoteCreator {
  createScratchpad(
    projectId: string,
    title: string,
    bodyMd: string,
    actor: PortActor
  ): Promise<EntityRef>
}

/**
 * The wider notes port: literature creates `paper_note` rows, and context-graph
 * actions read a note back to quote it.
 */
export interface NotePort extends NoteCreator {
  createPaperNote(
    projectId: string,
    paperId: string,
    title: string,
    bodyMd: string,
    actor: PortActor
  ): Promise<EntityRef>
  /** Undefined when the note is missing or soft-deleted. */
  readNote(
    projectId: string,
    noteId: string
  ): Promise<{ id: string; title: string; bodyMd: string; updatedAt: Date } | undefined>
}

/**
 * The recycle-bin port (`internal/ports.TrashService`). Purging always needs an
 * in-app confirmation token.
 */
export interface TrashPort {
  list(projectId: string): Promise<TrashItem[]>
  restore(projectId: string, entityType: string, entityId: string, actor: PortActor): Promise<void>
  purge(projectId: string, items: readonly TrashRef[], token: string): Promise<void>
}

/** Export format of a project bundle. */
export type ExportFormat = 'folder' | 'zip' | 'tar.gz'

export interface ExportRequest {
  projectId: string
  /** Absolute destination outside the workspace; never overwritten. */
  destinationDir: string
  format: ExportFormat
}

export interface ExportResult {
  /** Absolute path of the written bundle. */
  bundlePath: string
  /** SHA-256 of `manifest.json`, hex. */
  manifestSha256: string
  fileCount: number
}

/** The export port consumed by project deletion previews and MCP export. */
export interface ExportPort {
  exportProject(request: ExportRequest): Promise<ExportResult>
}

/**
 * The AI gateway port. `role` names a configured role (for example
 * `idea.generate`); implementations resolve profile, model and secrets.
 */
export interface AiGatewayRequest {
  role: string
  prompt: string
  variables?: Record<string, string>
  /** Project whose outputs and settings apply. */
  projectId: string
}

export interface AiGatewayResult {
  /** `ai_outputs` row id when the result was persisted. */
  outputId?: string
  text: string
  model: string
}

export interface AiGateway {
  isAvailable(): Promise<boolean>
  complete(request: AiGatewayRequest): Promise<AiGatewayResult>
}