import { z } from 'zod'

import {
  ENTITY_TYPES,
  RELATION_TYPES,
  TASK_COLUMN_KINDS,
  TASK_PRIORITIES,
  TASK_SORTS,
  type KanbanCommands
} from '@researchos/shared'

import { isValidId } from '../ids'

/**
 * Zod boundary for every `kanban.*` IPC command. The fields mirror the
 * service-level rules; the service still re-validates so MCP and direct core
 * callers get the same behavior.
 */

const uuid = z.string().refine((value) => isValidId(value), 'expected a lowercase UUIDv7')
const projectId = uuid
const title = z.string().min(1).max(200)
const descriptionMd = z.string().max(100_000)
const priority = z.enum(TASK_PRIORITIES)
const deadline = z.string().regex(/^\d{4}-\d{2}-\d{2}$/)
const weekPlan = z.string().regex(/^\d{4}-W\d{2}$/)
const labels = z.array(z.string().min(1).max(32)).max(20)
const columnName = z.string().min(1).max(40)
const limit = z.number().int().min(1).max(200)
const cursor = z.string()
const sort = z.enum(TASK_SORTS)

const patch = z
  .strictObject({
    title: title.optional(),
    descriptionMd: descriptionMd.optional(),
    priority: priority.optional(),
    deadline: deadline.nullable().optional(),
    weekPlan: weekPlan.nullable().optional(),
    blocked: z.boolean().optional(),
    labels: labels.optional(),
    column: columnName.optional()
  })
  .strict()

const listQuery = z.strictObject({
  projectId,
  column: z.string().min(1).optional(),
  parentId: uuid.optional(),
  onlyTopLevel: z.boolean().optional(),
  priority: z.array(priority).optional(),
  labels: z.array(z.string().min(1).max(32)).optional(),
  deadlineFrom: deadline.optional(),
  deadlineTo: deadline.optional(),
  blocked: z.boolean().optional(),
  weekPlan: weekPlan.optional(),
  q: z.string().optional(),
  includeArchived: z.boolean().optional(),
  includeDeleted: z.boolean().optional(),
  sort: sort.optional(),
  limit: limit.optional(),
  cursor: cursor.optional()
})

const taskRef = z.strictObject({ projectId, id: uuid })
const link = z.strictObject({
  projectId,
  taskId: uuid,
  relation: z.enum(RELATION_TYPES),
  targetType: z.enum(ENTITY_TYPES),
  targetId: uuid
})

const validators = {
  'kanban.columns.list': z.strictObject({ projectId }),
  'kanban.columns.create': z.strictObject({
    projectId,
    name: columnName,
    kind: z.enum(TASK_COLUMN_KINDS).optional(),
    isDone: z.boolean().optional(),
    after: columnName.optional()
  }),
  'kanban.columns.rename': z.strictObject({ projectId, name: columnName, newName: columnName }),
  'kanban.columns.move': z.strictObject({
    projectId,
    name: columnName,
    position: z.number().int().min(0)
  }),
  'kanban.columns.setDone': z.strictObject({ projectId, name: columnName, isDone: z.boolean() }),
  'kanban.columns.delete': z.strictObject({ projectId, name: columnName, migrateTo: columnName }),

  'kanban.tasks.list': listQuery,
  'kanban.tasks.get': taskRef,
  'kanban.tasks.create': z.strictObject({
    projectId,
    title,
    column: columnName.optional(),
    parentTaskId: uuid.optional(),
    descriptionMd: descriptionMd.optional(),
    priority: priority.optional(),
    deadline: deadline.nullable().optional(),
    labels: labels.optional(),
    weekPlan: weekPlan.nullable().optional()
  }),
  'kanban.tasks.update': z.strictObject({
    projectId,
    id: uuid,
    patch,
    expectedUpdatedAt: z.string().optional()
  }),
  'kanban.tasks.move': z.strictObject({
    projectId,
    id: uuid,
    column: columnName,
    beforeId: uuid.optional(),
    afterId: uuid.optional()
  }),
  'kanban.tasks.complete': z.strictObject({ projectId, id: uuid, done: z.boolean() }),
  'kanban.tasks.archive': taskRef,
  'kanban.tasks.unarchive': taskRef,
  'kanban.tasks.delete': taskRef,
  'kanban.tasks.restore': taskRef,

  'kanban.checklist.list': z.strictObject({ projectId, taskId: uuid }),
  'kanban.checklist.add': z.strictObject({
    projectId,
    taskId: uuid,
    text: z.string().min(1).max(500),
    beforeId: uuid.optional()
  }),
  'kanban.checklist.update': z.strictObject({
    projectId,
    itemId: uuid,
    text: z.string().min(1).max(500)
  }),
  'kanban.checklist.setDone': z.strictObject({ projectId, itemId: uuid, done: z.boolean() }),
  'kanban.checklist.remove': z.strictObject({ projectId, itemId: uuid }),
  'kanban.checklist.reorder': z.strictObject({
    projectId,
    taskId: uuid,
    itemIds: z.array(uuid)
  }),

  'kanban.labels.list': z.strictObject({ projectId }),

  'kanban.links.add': link,
  'kanban.links.remove': link,

  'kanban.views.board': z.strictObject({
    projectId,
    priority: z.array(priority).optional(),
    labels: z.array(z.string().min(1).max(32)).optional(),
    blocked: z.boolean().optional(),
    q: z.string().optional()
  }),
  'kanban.views.list': listQuery,
  'kanban.views.week': z.strictObject({
    projectId,
    weekPlan: weekPlan.optional(),
    limit: limit.optional(),
    cursor: cursor.optional()
  }),
  'kanban.views.blocked': z.strictObject({
    projectId,
    limit: limit.optional(),
    cursor: cursor.optional()
  }),
  'kanban.views.inbox': z.strictObject({
    projectId,
    limit: limit.optional(),
    cursor: cursor.optional()
  }),
  'kanban.views.archive': z.strictObject({
    projectId,
    q: z.string().optional(),
    limit: limit.optional(),
    cursor: cursor.optional(),
    sort: sort.optional()
  }),

  'kanban.suggestTasks': z.strictObject({ projectId, ideaId: uuid.optional() })
} satisfies { [C in keyof KanbanCommands]: z.ZodType<KanbanCommands[C]['req']> }

export type KanbanValidators = typeof validators
export type KanbanChannel = keyof KanbanValidators

export const kanbanValidators = validators

/**
 * Typed lookup used by the IPC registration loop. Zod's inferred output is
 * structurally the command request, which the `satisfies` clause pins.
 */
export function kanbanValidator<C extends KanbanChannel>(
  channel: C
): { parse(input: unknown): KanbanCommands[C]['req'] } {
  return validators[channel]
}