import { z } from 'zod'

import type { CommandMap, CommandValidator } from '@researchos/shared'

import { PROJECT_LIMITS } from '../modules/project/service'

/**
 * Validation schemas for the project-domain commands. Every
 * object is strict: an unknown field is INVALID_ARGUMENT at the IPC boundary
 * rather than silently ignored. The same limits are enforced by the service.
 */

const isoDateTime = z.string().min(1).max(64)
const identifier = z.string().min(1).max(64)

const projectId = z.object({ id: identifier })
const projectRef = z.object({ projectId: identifier })

export const projectListSchema = z.strictObject({
  includeArchived: z.boolean().optional(),
  query: z.string().max(200).optional(),
  limit: z.number().int().min(1).max(200).optional(),
  offset: z.number().int().min(0).optional()
})

export const projectCreateSchema = z.strictObject({
  name: z.string().min(1).max(PROJECT_LIMITS.name),
  summary: z.string().max(PROJECT_LIMITS.summary).optional(),
  goals: z.string().max(PROJECT_LIMITS.goals).optional()
})

export const projectPatchSchema = z
  .strictObject({
    name: z.string().max(PROJECT_LIMITS.name).optional(),
    summary: z.string().max(PROJECT_LIMITS.summary).optional(),
    goals: z.string().max(PROJECT_LIMITS.goals).optional(),
    focusMd: z.string().max(PROJECT_LIMITS.focus).optional()
  })
  .refine((patch) => Object.keys(patch).length > 0, { message: 'patch must name at least one field' })

export const projectUpdateSchema = z.strictObject({
  id: identifier,
  patch: projectPatchSchema,
  expectedUpdatedAt: isoDateTime.optional()
})

export const projectDeleteConfirmSchema = z.strictObject({
  id: identifier,
  confirmName: z.string().min(1).max(PROJECT_LIMITS.name),
  token: z.string().min(1).max(256)
})

export const projectActivitySchema = z.strictObject({
  projectId: identifier,
  filter: z
    .strictObject({
      from: isoDateTime.optional(),
      to: isoDateTime.optional(),
      entityTypes: z.array(z.string().min(1).max(64)).max(64).optional(),
      eventTypes: z.array(z.string().min(1).max(64)).max(64).optional(),
      actors: z.array(z.enum(['user', 'mcp', 'ai'])).max(3).optional(),
      cursor: z.string().max(512).optional(),
      limit: z.number().int().min(1).max(200).optional()
    })
    .optional()
})

const overviewWidgetKind = z.enum([
  'current_focus',
  'active_ideas',
  'recent_experiments',
  'my_tasks',
  'recent_notes',
  'literature',
  'blocked_tasks',
  'weekly_progress'
])

const contextEntityKind = z.enum([
  'note',
  'paper',
  'idea',
  'experiment',
  'run',
  'task',
  'db_record',
  'evidence'
])

export const projectOverviewSchema = z.strictObject({
  projectId: identifier,
  refresh: overviewWidgetKind.optional()
})

export const projectDashboardSchema = z.strictObject({
  refresh: z.boolean().optional()
})

export const projectProvenanceSchema = z.strictObject({
  projectId: identifier,
  from: isoDateTime.optional(),
  to: isoDateTime.optional(),
  focusIdeaId: identifier.optional(),
  milestones: z.boolean().optional(),
  granularity: z.enum(['week', 'month']).optional(),
  format: z.enum(['json', 'markdown']).optional()
})

export const projectContextSchema = z.strictObject({
  projectId: identifier,
  entityType: contextEntityKind,
  entityId: identifier,
  includeGraph: z.boolean().optional(),
  depth: z.union([z.literal(1), z.literal(2)]).optional()
})

export const projectReadinessSchema = z.strictObject({
  projectId: identifier,
  ideaId: identifier.optional()
})

export const projectTaskSuggestionsSchema = projectReadinessSchema

export const projectQuickTaskSchema = z.strictObject({
  projectId: identifier,
  title: z.string().min(1).max(200),
  column: z.string().min(1).max(64).optional(),
  weekPlan: z.string().max(16).optional()
})

export const projectQuickNoteSchema = z.strictObject({
  projectId: identifier,
  title: z.string().max(200).optional(),
  bodyMd: z.string().max(200_000).optional()
})

export const projectQuickIdeaSchema = z.strictObject({
  projectId: identifier,
  title: z.string().min(1).max(200)
})

function validator<T>(schema: z.ZodType<T>): CommandValidator<T> {
  return { parse: (input) => schema.parse(input) }
}

const byId = validator(projectId)
const byProject = validator(projectRef)

/** One validator per project channel; the handler registers all of them. */
export const projectValidators: {
  [C in keyof CommandMap as C extends `project.${string}` ? C : never]: CommandValidator<CommandMap[C]['req']>
} = {
  'project.list': validator(projectListSchema),
  'project.get': byId,
  'project.create': validator(projectCreateSchema),
  'project.update': validator(projectUpdateSchema),
  'project.archive': byId,
  'project.unarchive': byId,
  'project.delete.plan': byId,
  'project.delete.confirm': validator(projectDeleteConfirmSchema),
  'project.session': byId,
  'project.stats': byId,
  'project.resources': byId,
  'project.activity': validator(projectActivitySchema),
  'project.overview': validator(projectOverviewSchema),
  'project.dashboard': validator(projectDashboardSchema),
  'project.provenance': validator(projectProvenanceSchema),
  'project.context': validator(projectContextSchema),
  'project.readiness': validator(projectReadinessSchema),
  'project.taskSuggestions': validator(projectTaskSuggestionsSchema),
  'project.tasks.quickAdd': validator(projectQuickTaskSchema),
  'project.notes.quickAdd': validator(projectQuickNoteSchema),
  'project.ideas.quickAdd': validator(projectQuickIdeaSchema)
}