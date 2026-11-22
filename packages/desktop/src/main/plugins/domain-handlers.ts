import { isEntityType, isRelation } from '@researchos/core/schema'
import { RelationService } from '@researchos/core/relations'
import type { PluginRpcHandler } from '@researchos/core/modules/plugins/index'
import type { IdeasService } from '@researchos/core/modules/ideas/index'
import type { NotesModule } from '@researchos/core/modules/notes/index'
import type { LiteratureModule } from '@researchos/core/modules/literature/index'
import type { ExperimentsService } from '@researchos/core/modules/experiments/index'
import type { KanbanModule } from '@researchos/core/modules/kanban/index'
import type { ProjectService } from '@researchos/core/modules/project/service'
import type { ProjectBehaviors } from '@researchos/core/behaviors'
import { appError, AppErrorCode } from '@researchos/core/errors'
import type { PluginRpcMethod } from '@researchos/shared'

/**
 * Part V of the plugin-host spec: research-domain RPCs reuse the exact services the IPC and
 * MCP layers call. The handlers only validate/translate the wire shape; every
 * write still runs through the domain service, its archived gate and activity
 * stream (actor `user`).
 */

export interface PluginDomainHandlerDeps {
  notes(projectId: string): NotesModule
  literature(): LiteratureModule
  ideas(): IdeasService
  experiments(projectId: string): ExperimentsService
  kanban(projectId: string): KanbanModule
  behavior(projectId: string): ProjectBehaviors
  projects(): ProjectService
  globalDb(): ProjectBehaviors['handle']['db']
}

function text(value: unknown, field: string, max = 4096): string {
  if (typeof value !== 'string' || value.trim() === '' || value.length > max) {
    throw appError(AppErrorCode.InvalidArgument, `${field} must be a non-empty string`, { field })
  }
  return value
}

function optionalText(value: unknown, field: string, max = 100_000): string | undefined {
  if (value === undefined) return undefined
  if (typeof value !== 'string' || value.length > max) {
    throw appError(AppErrorCode.InvalidArgument, `${field} must be a string`, { field })
  }
  return value
}

function record(value: unknown, field: string): Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw appError(AppErrorCode.InvalidArgument, `${field} must be an object`, { field })
  }
  return value as Record<string, unknown>
}

function entityType(value: unknown, field: string): string {
  if (typeof value !== 'string' || !isEntityType(value)) {
    throw appError(AppErrorCode.InvalidArgument, `unknown entity type ${String(value)}`, { field })
  }
  return value
}

function relationName(value: unknown): string {
  if (typeof value !== 'string' || !isRelation(value)) {
    throw appError(AppErrorCode.InvalidArgument, `unknown relation ${String(value)}`, {
      field: 'relation'
    })
  }
  return value
}

function relationOf(behavior: ProjectBehaviors, globalDb: PluginDomainHandlerDeps['globalDb']): RelationService {
  return new RelationService(behavior.handle, behavior.events, {
    global: { db: globalDb() }
  })
}

export function createPluginDomainHandlers(
  deps: PluginDomainHandlerDeps
): Partial<Record<PluginRpcMethod, PluginRpcHandler>> {
  return {
    'notes.create': (params) => {
      const projectId = text(params['projectId'], 'projectId', 128)
      const title = optionalText(params['title'], 'title', 200)
      const bodyMd = optionalText(params['bodyMd'], 'bodyMd', 5 * 1024 * 1024)
      const kind = optionalText(params['kind'], 'kind', 32)
      const tags = Array.isArray(params['tags'])
        ? (params['tags'] as unknown[]).map((tag) => text(tag, 'tags', 64))
        : undefined
      return deps.notes(projectId).createNote({
        ...(title === undefined ? {} : { title }),
        ...(bodyMd === undefined ? {} : { bodyMd }),
        ...(tags === undefined ? {} : { tags }),
        ...(kind === undefined ? {} : { kind: kind as never })
      })
    },
    'notes.updateMeta': (params) => {
      const projectId = text(params['projectId'], 'projectId', 128)
      const noteId = text(params['noteId'], 'noteId', 128)
      const patch = record(params['patch'], 'patch')
      const notes = deps.notes(projectId)
      const updated = notes.updateNoteMeta(noteId, {
        ...(patch['title'] === undefined ? {} : { title: text(patch['title'], 'title', 200) }),
        ...(patch['description'] === undefined
          ? {}
          : { description: optionalText(patch['description'], 'description', 4000) })
      })
      if (Array.isArray(patch['tags'])) {
        notes.addNoteTags(
          noteId,
          (patch['tags'] as unknown[]).map((tag) => text(tag, 'tags', 64))
        )
      }
      return updated
    },
    'databases.list': (params) => deps.notes(text(params['projectId'], 'projectId', 128)).listDatabases(),
    'databases.get': (params) =>
      deps
        .notes(text(params['projectId'], 'projectId', 128))
        .getDatabase(text(params['databaseId'], 'databaseId', 128)),
    'databases.create': (params) => {
      const projectId = text(params['projectId'], 'projectId', 128)
      const fields = Array.isArray(params['fields']) ? (params['fields'] as unknown[]) : undefined
      return deps.notes(projectId).createDatabase({
        name: text(params['name'], 'name', 200),
        ...(params['description'] === undefined
          ? {}
          : { description: optionalText(params['description'], 'description', 2000) }),
        ...(fields === undefined ? {} : { fields: fields as never })
      })
    },
    'records.list': (params) =>
      deps.notes(text(params['projectId'], 'projectId', 128)).listRecords({
        databaseId: text(params['databaseId'], 'databaseId', 128),
        ...(params['q'] === undefined ? {} : { q: optionalText(params['q'], 'q', 200) }),
        ...(typeof params['limit'] === 'number' ? { limit: params['limit'] } : {}),
        ...(typeof params['offset'] === 'number' ? { offset: params['offset'] } : {})
      }),
    'records.get': (params) =>
      deps
        .notes(text(params['projectId'], 'projectId', 128))
        .getRecord(text(params['recordId'], 'recordId', 128)),
    'records.create': (params) => {
      const projectId = text(params['projectId'], 'projectId', 128)
      return deps.notes(projectId).createRecord({
        databaseId: text(params['databaseId'], 'databaseId', 128),
        ...(params['values'] === undefined ? {} : { values: record(params['values'], 'values') }),
        ...(params['title'] === undefined ? {} : { title: text(params['title'], 'title', 200) })
      })
    },
    'records.update': (params) => {
      const projectId = text(params['projectId'], 'projectId', 128)
      return deps.notes(projectId).updateRecord({
        recordId: text(params['recordId'], 'recordId', 128),
        values: params['values'] === undefined ? {} : record(params['values'], 'values'),
        ...(params['title'] === undefined ? {} : { title: text(params['title'], 'title', 200) })
      })
    },
    'records.delete': (params) => {
      const projectId = text(params['projectId'], 'projectId', 128)
      return deps
        .notes(projectId)
        .deleteRecord(text(params['recordId'], 'recordId', 128), params['deleteNote'] === true)
    },
    'literature.get': (params) =>
      deps
        .literature()
        .getPaper(
          text(params['paperId'], 'paperId', 128),
          text(params['projectId'], 'projectId', 128)
        ),
    'ideas.get': (params) =>
      deps
        .ideas()
        .getIdea(
          text(params['projectId'], 'projectId', 128),
          text(params['ideaId'], 'ideaId', 128)
        ),
    'ideas.update': (params) => {
      const patch = record(params['patch'], 'patch')
      const projectId = text(params['projectId'], 'projectId', 128)
      const ideaId = text(params['ideaId'], 'ideaId', 128)
      const ideas = deps.ideas()
      if (patch['status'] !== undefined) {
        ideas.changeStatus(projectId, ideaId, {
          status: text(patch['status'], 'status', 32) as never,
          ...(patch['rejectionReason'] === undefined
            ? {}
            : { rejectionReason: optionalText(patch['rejectionReason'], 'rejectionReason', 4000) }),
          actor: 'user'
        })
      }
      return ideas.updateIdea(
        projectId,
        ideaId,
        {
          ...(patch['title'] === undefined ? {} : { title: text(patch['title'], 'title', 200) }),
          ...(patch['description'] === undefined
            ? {}
            : { description: optionalText(patch['description'], 'description', 20_000) })
        },
        { actor: 'user' }
      )
    },
    'evidence.list': (params) =>
      deps.ideas().listEvidence(text(params['projectId'], 'projectId', 128), {
        ...(params['ideaId'] === undefined
          ? {}
          : { ideaId: text(params['ideaId'], 'ideaId', 128) }),
        ...(params['direction'] === 'support' || params['direction'] === 'oppose'
          ? { direction: params['direction'] }
          : {})
      }),
    'evidence.create': (params) => {
      const sourceType = entityType(params['sourceType'] ?? 'note', 'sourceType')
      const sourceId = text(params['sourceId'], 'sourceId', 128)
      return deps.ideas().addEvidence(text(params['projectId'], 'projectId', 128), {
        ideaId: text(params['ideaId'], 'ideaId', 128),
        direction: params['direction'] === 'oppose' ? 'oppose' : 'support',
        content: text(params['content'], 'content', 20_000),
        source: { type: sourceType as never, id: sourceId },
        actor: 'user'
      })
    },
    'experiments.get': (params) =>
      deps
        .experiments(text(params['projectId'], 'projectId', 128))
        .getExperiment(text(params['experimentId'], 'experimentId', 128)),
    'experiments.runs.list': (params) =>
      deps
        .experiments(text(params['projectId'], 'projectId', 128))
        .listRuns(text(params['experimentId'], 'experimentId', 128)),
    'kanban.tasks.update': (params) => {
      const projectId = text(params['projectId'], 'projectId', 128)
      return deps.kanban(projectId).tasks.update('user', {
        projectId,
        id: text(params['id'], 'id', 128),
        patch: record(params['patch'], 'patch') as never
      })
    },
    'activity.list': (params) => {
      const projectId = text(params['projectId'], 'projectId', 128)
      return deps.projects().activity(projectId, {
        ...(typeof params['limit'] === 'number' ? { limit: params['limit'] } : {}),
        ...(Array.isArray(params['eventTypes'])
          ? { eventTypes: (params['eventTypes'] as unknown[]).map(String) }
          : {}),
        ...(params['entityType'] === undefined
          ? {}
          : { entityType: entityType(params['entityType'], 'entityType') as never })
      })
    },
    'relations.list': (params) => {
      const behavior = deps.behavior(text(params['projectId'], 'projectId', 128))
      const sourceType = entityType(params['entityType'], 'entityType')
      const sourceId = text(params['entityId'], 'entityId', 128)
      const relations = relationOf(behavior, deps.globalDb)
      return {
        outgoing: relations.from(sourceType as never, sourceId),
        incoming: relations.to(sourceType as never, sourceId)
      }
    },
    'relations.add': (params) => {
      const behavior = deps.behavior(text(params['projectId'], 'projectId', 128))
      const link = {
        srcType: entityType(params['srcType'], 'srcType'),
        srcId: text(params['srcId'], 'srcId', 128),
        relation: relationName(params['relation']),
        dstType: entityType(params['dstType'], 'dstType'),
        dstId: text(params['dstId'], 'dstId', 128)
      }
      const relations = relationOf(behavior, deps.globalDb)
      const relationId = behavior.inTx(() =>
        relations.link({ db: behavior.handle.db }, link as never, 'user')
      )
      return { relationId }
    },
    'relations.remove': (params) => {
      const behavior = deps.behavior(text(params['projectId'], 'projectId', 128))
      const link = {
        srcType: entityType(params['srcType'], 'srcType'),
        srcId: text(params['srcId'], 'srcId', 128),
        relation: relationName(params['relation']),
        dstType: entityType(params['dstType'], 'dstType'),
        dstId: text(params['dstId'], 'dstId', 128)
      }
      const relations = relationOf(behavior, deps.globalDb)
      const existing = relations
        .from(link.srcType as never, link.srcId, link.relation as never)
        .some(
          (candidate) => candidate.dstType === link.dstType && candidate.dstId === link.dstId
        )
      behavior.inTx(() => relations.unlink({ db: behavior.handle.db }, link as never, 'user'))
      return { removed: existing }
    }
  }
}
