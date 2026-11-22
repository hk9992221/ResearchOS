import type {
  DashboardProjectCard,
  DashboardSnapshot,
  DashboardTotals,
  DashboardWorkspaceSummary,
  EntityId,
  Project,
  ProjectActivityItem,
  ProjectSession,
  ProjectStats
} from '@researchos/shared'

import { formatTimestamp, type Clock } from '../../clock'
import type { ProjectService } from '../project/service'

/**
 * Global Dashboard: the project card grid, per-project statistics,
 * the workspace summary and the cross-project activity feed the UI renders
 * before a project is opened.
 *
 * The service is read-only. Listing, loading and switching are navigation, so
 * they never write an Activity. `Load` never fails because one project is
 * broken: that card is reported in `degradedProjects` with zero statistics and
 * the UI renders "—".
 */

const DEFAULT_CACHE_TTL_MS = 60_000
const DEFAULT_STATS_BUDGET_MS = 200

/** §5.11 merged-feed bounds. */
export const DASHBOARD_ACTIVITY = {
  defaultLimit: 20,
  maxLimit: 100,
  perProject: 50,
  maxProjects: 100
} as const

export interface DashboardOptions {
  project: ProjectService
  clock?: Clock
  /** Whole-snapshot cache lifetime; 0 falls back to 60 s. */
  cacheTtlMs?: number
  /** Per-project statistics budget; 0 falls back to 200 ms. */
  statsBudgetMs?: number
}

interface CacheEntry {
  snapshot: DashboardSnapshot
  expires: number
}

export class DashboardService {
  readonly project: ProjectService
  private readonly clock: Clock
  private readonly cacheTtlMs: number
  private readonly statsBudgetMs: number
  private cache: CacheEntry | null = null
  private generation = 0

  constructor(options: DashboardOptions) {
    this.project = options.project
    this.clock = options.clock ?? options.project.clock
    this.cacheTtlMs = options.cacheTtlMs !== undefined && options.cacheTtlMs > 0 ? options.cacheTtlMs : DEFAULT_CACHE_TTL_MS
    this.statsBudgetMs =
      options.statsBudgetMs !== undefined && options.statsBudgetMs > 0
        ? options.statsBudgetMs
        : DEFAULT_STATS_BUDGET_MS
  }

  /** The whole read model, or the cached copy while it is fresh. */
  load(options: { refresh?: boolean } = {}): DashboardSnapshot {
    if (options.refresh !== true) {
      const cached = this.cached()
      if (cached !== null) return cached
    }
    const generation = this.generation
    const snapshot = this.build()
    if (generation === this.generation) {
      this.cache = { snapshot, expires: this.clock.now().getTime() + this.cacheTtlMs }
    }
    return snapshot
  }

  /**
   * Recomputes one card and grafts it onto the cached snapshot. An unknown
   * project yields NOT_FOUND; a statistics failure keeps the card with zero
   * statistics and marks the project degraded, mirroring `load`.
   */
  refreshCard(projectId: string): DashboardProjectCard {
    const project = this.project.get(projectId)
    const { card, healthy } = this.card(project)
    if (this.cache !== null) {
      const index = this.cache.snapshot.projects.findIndex((entry) => entry.project.id === projectId)
      if (index >= 0) {
        this.cache.snapshot.projects[index] = card
        this.cache.snapshot.totals = totalsOf(this.cache.snapshot.projects)
        this.cache.snapshot.degradedProjects = degradedList(
          this.cache.snapshot.degradedProjects,
          projectId,
          !healthy
        )
      }
    }
    return card
  }

  /** Opens a project and drops the cached snapshot (CurrentProjectId changed). */
  switchProject(projectId: string): ProjectSession {
    try {
      return this.project.open(projectId)
    } finally {
      this.invalidate(projectId)
    }
  }

  /** The merged newest activities of every project (no project required). */
  recentActivity(limit?: number): ProjectActivityItem[] {
    const projects = this.project.list({ includeArchived: true })
    return this.mergeActivity(projects, limit)
  }

  /** Drops the cached snapshot after a project changed. */
  invalidate(_projectId?: string): void {
    this.cache = null
    this.generation += 1
  }

  invalidateAll(): void {
    this.invalidate()
  }

  private build(): DashboardSnapshot {
    const projects = this.project.list({ includeArchived: true })
    const cards: DashboardProjectCard[] = []
    const degraded: string[] = []
    for (const project of projects) {
      const { card, healthy } = this.card(project)
      cards.push(card)
      if (!healthy) degraded.push(project.id)
    }
    degraded.sort()
    const workspace = this.workspaceSummary()
    const snapshot: DashboardSnapshot = {
      projects: cards,
      currentProjectId: this.project.current()?.project.id ?? null,
      workspace,
      activity: this.mergeActivity(projects, DASHBOARD_ACTIVITY.defaultLimit),
      totals: totalsOf(cards),
      degradedProjects: degraded,
      generatedAt: formatTimestamp(this.clock.now())
    }
    return snapshot
  }

  private card(project: Project): { card: DashboardProjectCard; healthy: boolean } {
    const started = performance.now()
    try {
      const stats: ProjectStats = this.project.stats(project.id)
      const elapsed = performance.now() - started
      if (elapsed > this.statsBudgetMs) {
        // The read finished late: report it, but never pretend the numbers are
        // trustworthy inside the §5.11 budget.
        return {
          card: { project, stats: emptyStats(), lastActivityAt: null, archived: project.status === 'archived', degraded: true },
          healthy: false
        }
      }
      return {
        card: {
          project,
          stats,
          lastActivityAt: stats.lastActivityAt,
          archived: project.status === 'archived',
          degraded: false
        },
        healthy: true
      }
    } catch {
      return {
        card: {
          project,
          stats: emptyStats(),
          lastActivityAt: null,
          archived: project.status === 'archived',
          degraded: true
        },
        healthy: false
      }
    }
  }

  private workspaceSummary(): DashboardWorkspaceSummary | null {
    try {
      const status = this.project.workspace.status()
      return {
        root: status.root,
        projectCount: status.projectCount,
        archivedCount: status.archivedCount,
        openProjects: status.openProjects,
        vectorAvailable: status.vectorAvailable
      }
    } catch {
      return null
    }
  }

  /**
   * Reads up to `perProject` activities from the newest `maxProjects` projects
   * and merges them newest-first with the id as tie-break. A project whose
   * timeline cannot be read is skipped: one broken database must not empty the
   * feed.
   */
  private mergeActivity(projects: Project[], limit?: number): ProjectActivityItem[] {
    const total = clampActivityLimit(limit)
    if (projects.length === 0) return []
    const candidates = [...projects]
      .sort((left, right) => {
        if (left.createdAt !== right.createdAt) return left.createdAt > right.createdAt ? -1 : 1
        return left.id > right.id ? -1 : left.id < right.id ? 1 : 0
      })
      .slice(0, DASHBOARD_ACTIVITY.maxProjects)

    const now = formatTimestamp(this.clock.now())
    const seen = new Set<string>()
    const all: ProjectActivityItem[] = []
    for (const project of candidates) {
      try {
        const page = this.project.activity(project.id, {
          from: new Date(0).toISOString(),
          to: now,
          limit: DASHBOARD_ACTIVITY.perProject
        })
        for (const item of page.items) {
          if (seen.has(item.id)) continue
          seen.add(item.id)
          all.push(item)
        }
      } catch {
        // Skip a broken project; the dashboard still renders.
      }
    }
    all.sort((left, right) => {
      if (left.createdAt !== right.createdAt) return left.createdAt > right.createdAt ? -1 : 1
      return left.id > right.id ? -1 : left.id < right.id ? 1 : 0
    })
    return all.slice(0, total)
  }

  private cached(): DashboardSnapshot | null {
    if (this.cache === null) return null
    if (this.clock.now().getTime() >= this.cache.expires) return null
    return this.cache.snapshot
  }
}

function emptyStats(): ProjectStats {
  return {
    notes: 0,
    ideasActive: 0,
    experiments: 0,
    papers: 0,
    tasksOpen: 0,
    tasksBlocked: 0,
    trashed: 0,
    dirBytes: 0,
    lastActivityAt: null
  }
}

function totalsOf(cards: DashboardProjectCard[]): DashboardTotals {
  const totals: DashboardTotals = { active: 0, archived: 0, notes: 0, papers: 0, openTasks: 0 }
  for (const card of cards) {
    if (card.archived) totals.archived += 1
    else totals.active += 1
    totals.notes += card.stats.notes
    totals.papers += card.stats.papers
    totals.openTasks += card.stats.tasksOpen
  }
  return totals
}

function degradedList(list: EntityId[], projectId: string, degraded: boolean): EntityId[] {
  const without = list.filter((id) => id !== projectId)
  if (degraded) without.push(projectId)
  return without.sort()
}

export function clampActivityLimit(limit?: number): number {
  if (limit === undefined || limit <= 0) return DASHBOARD_ACTIVITY.defaultLimit
  return Math.min(limit, DASHBOARD_ACTIVITY.maxLimit)
}
