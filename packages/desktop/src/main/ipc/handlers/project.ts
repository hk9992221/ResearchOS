import type { ActivityFilter, EntityContext } from '@researchos/shared'
import type { OverviewWidgetKind, ProvenanceFormat } from '@researchos/shared'

import type { ContextGraphService } from '@researchos/core/modules/contextgraph/index'
import type { DashboardService } from '@researchos/core/modules/dashboard/index'
import { ProjectService } from '@researchos/core/modules/project/service'
import type { OverviewService } from '@researchos/core/modules/overview/index'
import type { ProvenanceService } from '@researchos/core/modules/provenance/index'
import type { ReadinessService } from '@researchos/core/modules/readiness/index'
import { projectValidators } from '@researchos/core/validation/project'

import type { CommandRouter } from '../router'

/**
 * Project-domain IPC ( project/dashboard/overview,  provenance/
 * context/readiness). The handler is a thin adapter: validation comes from
 * `core/src/validation/project.ts`, every rejection is an AppError the router
 * serializes, and every write drops the cached read models it invalidates.
 */

export interface ProjectHandlerDeps {
  projects: ProjectService
  dashboard: DashboardService
  overview: OverviewService
  provenance: ProvenanceService
  context: ContextGraphService
  readiness: ReadinessService
}

export function registerProjectHandlers(router: CommandRouter, deps: ProjectHandlerDeps): void {
  const changed = (projectId: string): void => {
    deps.dashboard.invalidate(projectId)
    deps.overview.invalidate(projectId)
  }

  router.registerHandler('project.list', projectValidators['project.list'], (request) =>
    deps.projects.list({
      includeArchived: request.includeArchived,
      query: request.query,
      limit: request.limit,
      offset: request.offset
    })
  )

  router.registerHandler('project.get', projectValidators['project.get'], (request) =>
    deps.projects.get(request.id)
  )

  router.registerHandler('project.create', projectValidators['project.create'], (request) => {
    const created = deps.projects.create(
      {
        name: request.name,
        ...(request.summary === undefined ? {} : { summary: request.summary }),
        ...(request.goals === undefined ? {} : { goals: request.goals })
      },
      'user'
    )
    changed(created.id)
    return created
  })

  router.registerHandler('project.update', projectValidators['project.update'], (request) => {
    const updated = deps.projects.update({
      id: request.id,
      patch: request.patch,
      ...(request.expectedUpdatedAt === undefined ? {} : { expectedUpdatedAt: request.expectedUpdatedAt }),
      actor: 'user'
    })
    changed(updated.id)
    return updated
  })

  router.registerHandler('project.archive', projectValidators['project.archive'], (request) => {
    const archived = deps.projects.archive(request.id, 'user')
    changed(request.id)
    return archived
  })

  router.registerHandler('project.unarchive', projectValidators['project.unarchive'], (request) => {
    const active = deps.projects.unarchive(request.id, 'user')
    changed(request.id)
    return active
  })

  router.registerHandler('project.delete.plan', projectValidators['project.delete.plan'], (request) =>
    deps.projects.prepareDelete(request.id, { issueToken: true })
  )

  router.registerHandler('project.delete.confirm', projectValidators['project.delete.confirm'], (request) => {
    const result = deps.projects.permanentDelete({
      projectId: request.id,
      confirmName: request.confirmName,
      token: request.token
    })
    changed(request.id)
    return result
  })

  router.registerHandler('project.session', projectValidators['project.session'], (request) =>
    deps.projects.open(request.id)
  )

  router.registerHandler('project.stats', projectValidators['project.stats'], (request) =>
    deps.projects.stats(request.id)
  )

  router.registerHandler('project.resources', projectValidators['project.resources'], (request) =>
    deps.projects.resources(request.id)
  )

  router.registerHandler('project.activity', projectValidators['project.activity'], (request) =>
    deps.projects.activity(request.projectId, (request.filter ?? {}) as ActivityFilter)
  )

  router.registerHandler('project.overview', projectValidators['project.overview'], (request) =>
    deps.overview.load(request.projectId, {
      ...(request.refresh === undefined ? {} : { refresh: request.refresh as OverviewWidgetKind })
    })
  )

  router.registerHandler('project.dashboard', projectValidators['project.dashboard'], (request) =>
    deps.dashboard.load({ refresh: request.refresh === true })
  )

  router.registerHandler('project.provenance', projectValidators['project.provenance'], (request) => {
    const format = (request.format ?? 'json') as ProvenanceFormat
    const report = deps.provenance.load({
      projectId: request.projectId,
      ...(request.from === undefined ? {} : { from: request.from }),
      ...(request.to === undefined ? {} : { to: request.to }),
      ...(request.focusIdeaId === undefined ? {} : { focusIdeaId: request.focusIdeaId }),
      ...(request.milestones === undefined ? {} : { milestones: request.milestones }),
      ...(request.granularity === undefined ? {} : { granularity: request.granularity })
    })
    return format === 'markdown' ? { ...report, markdown: deps.provenance.markdown(report) } : report
  })

  router.registerHandler('project.context', projectValidators['project.context'], (request) => {
    const target = { type: request.entityType, id: request.entityId }
    const context: EntityContext = deps.context.load(request.projectId, target)
    if (request.includeGraph !== true) return context
    return {
      ...context,
      graph: deps.context.graph(request.projectId, target, request.depth ?? 1)
    }
  })

  router.registerHandler('project.readiness', projectValidators['project.readiness'], (request) =>
    deps.readiness.check(request.projectId, request.ideaId)
  )

  router.registerHandler('project.taskSuggestions', projectValidators['project.taskSuggestions'], (request) =>
    deps.readiness.suggestions(request.projectId, request.ideaId)
  )

  router.registerHandler('project.tasks.quickAdd', projectValidators['project.tasks.quickAdd'], (request) => {
    const created = deps.projects.quickAddTask(request.projectId, {
      title: request.title,
      ...(request.column === undefined ? {} : { column: request.column }),
      ...(request.weekPlan === undefined ? {} : { weekPlan: request.weekPlan })
    })
    changed(request.projectId)
    return created
  })

  router.registerHandler('project.notes.quickAdd', projectValidators['project.notes.quickAdd'], (request) => {
    const created = deps.projects.quickAddNote(request.projectId, {
      ...(request.title === undefined ? {} : { title: request.title }),
      ...(request.bodyMd === undefined ? {} : { bodyMd: request.bodyMd })
    })
    changed(request.projectId)
    return created
  })

  router.registerHandler('project.ideas.quickAdd', projectValidators['project.ideas.quickAdd'], (request) => {
    const created = deps.projects.quickAddIdea(request.projectId, request.title)
    changed(request.projectId)
    return created
  })
}
