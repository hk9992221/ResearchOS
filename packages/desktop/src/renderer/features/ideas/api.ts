/**
 * Ideas IPC adapter. The typed client mirrors the shared command contract;
 * this module only names the channels the feature uses, so components never
 * carry a channel string.
 */

import type {
  EntityId,
  EvidenceCreateInput,
  EvidenceDirection,
  EvidenceExtractionPreview,
  EvidenceExtractionRequest,
  EvidencePage,
  EvidencePatch,
  EvidenceView,
  Idea,
  IdeaDetail,
  IdeaHistoryKind,
  IdeaHistoryPage,
  IdeaListQuery,
  IdeaPage,
  IdeaPatch,
  IdeaStatus,
  IdeaLinkTarget,
  ReadinessReport,
  ContextEntityKind,
  ContextGraph,
  EntityContext,
  ProvenanceReport,
  RunSummary
} from '@researchos/shared'

import { ipc } from '../../lib/ipc'

export interface IdeasListParams {
  readonly status?: readonly IdeaStatus[]
  readonly q?: string
  readonly cursor?: string
  readonly limit?: number
}

export interface EvidenceListParams {
  readonly direction?: EvidenceDirection
  readonly confirmed?: boolean
  readonly cursor?: string
  readonly limit?: number
}

export interface HistoryParams {
  readonly kinds?: readonly IdeaHistoryKind[]
  readonly cursor?: string
  readonly limit?: number
}

export const ideasApi = {
  list: (projectId: string, params: IdeasListParams = {}): Promise<IdeaPage> => {
    const query: IdeaListQuery = { projectId }
    if (params.status !== undefined) query.status = [...params.status]
    if (params.q !== undefined && params.q.trim() !== '') query.q = params.q.trim()
    if (params.cursor !== undefined) query.cursor = params.cursor
    if (params.limit !== undefined) query.limit = params.limit
    return ipc.invoke('ideas.list', query)
  },

  get: (projectId: string, ideaId: EntityId): Promise<IdeaDetail> =>
    ipc.invoke('ideas.get', { projectId, ideaId }),

  create: (projectId: string, title: string, description?: string): Promise<Idea> =>
    ipc.invoke('ideas.create', { projectId, title, description }),

  update: (
    projectId: string,
    ideaId: EntityId,
    patch: IdeaPatch,
    expectedUpdatedAt?: string
  ): Promise<Idea> =>
    ipc.invoke('ideas.update', { projectId, ideaId, patch, expectedUpdatedAt }),

  changeStatus: (
    projectId: string,
    ideaId: EntityId,
    status: IdeaStatus,
    options: { rejectionReason?: string; note?: string } = {}
  ): Promise<Idea> =>
    ipc.invoke('ideas.changeStatus', {
      projectId,
      ideaId,
      status,
      ...(options.rejectionReason === undefined || options.rejectionReason.trim() === ''
        ? {}
        : { rejectionReason: options.rejectionReason.trim() }),
      ...(options.note === undefined || options.note.trim() === ''
        ? {}
        : { note: options.note.trim() })
    }),

  delete: (projectId: string, ideaId: EntityId): Promise<{ deleted: boolean }> =>
    ipc.invoke('ideas.delete', { projectId, ideaId }),

  derive: (
    projectId: string,
    parentIdeaId: EntityId,
    title: string,
    description?: string
  ): Promise<Idea> =>
    ipc.invoke('ideas.derive', { projectId, parentIdeaId, title, description }),

  history: (
    projectId: string,
    ideaId: EntityId,
    params: HistoryParams = {}
  ): Promise<IdeaHistoryPage> => {
    const query: {
      projectId: string
      ideaId: string
      kinds?: IdeaHistoryKind[]
      limit?: number
      cursor?: string
    } = { projectId, ideaId }
    if (params.kinds !== undefined && params.kinds.length > 0) query.kinds = [...params.kinds]
    if (params.limit !== undefined) query.limit = params.limit
    if (params.cursor !== undefined) query.cursor = params.cursor
    return ipc.invoke('ideas.history', query)
  },

  evidence: {
    list: (
      projectId: string,
      ideaId: EntityId,
      params: EvidenceListParams = {}
    ): Promise<EvidencePage> => {
      const query: {
        projectId: string
        ideaId: string
        direction?: EvidenceDirection
        confirmed?: boolean
        cursor?: string
        limit?: number
      } = { projectId, ideaId }
      if (params.direction !== undefined) query.direction = params.direction
      if (params.confirmed !== undefined) query.confirmed = params.confirmed
      if (params.cursor !== undefined) query.cursor = params.cursor
      if (params.limit !== undefined) query.limit = params.limit
      return ipc.invoke('ideas.evidence.list', query)
    },

    create: (input: EvidenceCreateInput): Promise<EvidenceView> =>
      ipc.invoke('ideas.evidence.create', input),

    update: (
      projectId: string,
      evidenceId: EntityId,
      patch: EvidencePatch,
      expectedUpdatedAt?: string
    ): Promise<EvidenceView> =>
      ipc.invoke('ideas.evidence.update', {
        projectId,
        evidenceId,
        patch,
        expectedUpdatedAt
      }),

    delete: (projectId: string, evidenceId: EntityId): Promise<{ deleted: boolean }> =>
      ipc.invoke('ideas.evidence.delete', { projectId, evidenceId }),

    confirm: (projectId: string, evidenceId: EntityId): Promise<EvidenceView> =>
      ipc.invoke('ideas.evidence.confirm', { projectId, evidenceId }),

    discard: (
      projectId: string,
      ideaId: EntityId,
      evidenceId: EntityId
    ): Promise<{ discarded: boolean }> =>
      ipc.invoke('ideas.evidence.discard', { projectId, ideaId, evidenceId }),

    extract: (request: EvidenceExtractionRequest): Promise<{ jobId: string }> =>
      ipc.invoke('ideas.evidence.extract', request),

    extractPreview: (jobId: string): Promise<EvidenceExtractionPreview> =>
      ipc.invoke('ideas.evidence.extractPreview', { jobId })
  },

  links: {
    add: (projectId: string, ideaId: EntityId, target: IdeaLinkTarget): Promise<{ added: boolean }> =>
      ipc.invoke('ideas.links.add', { projectId, ideaId, target }),

    remove: (
      projectId: string,
      ideaId: EntityId,
      target: IdeaLinkTarget
    ): Promise<{ removed: boolean }> =>
      ipc.invoke('ideas.links.remove', { projectId, ideaId, target })
  },

  readiness: (projectId: string, ideaId?: EntityId): Promise<ReadinessReport> =>
    ipc.invoke('project.readiness', { projectId, ...(ideaId === undefined ? {} : { ideaId }) }),

  context: (
    projectId: string,
    entityId: EntityId,
    depth: 1 | 2
  ): Promise<EntityContext & { graph?: ContextGraph }> => {
    const entityType: ContextEntityKind = 'idea'
    return ipc.invoke('project.context', {
      projectId,
      entityType,
      entityId,
      includeGraph: true,
      depth
    })
  },

  lineage: (projectId: string, ideaId: EntityId): Promise<ProvenanceReport> =>
    ipc.invoke('project.provenance', {
      projectId,
      focusIdeaId: ideaId,
      milestones: true,
      format: 'json'
    }),

  runs: (projectId: string, experimentId: EntityId): Promise<RunSummary[]> =>
    ipc.invoke('experiments.runs.list', { projectId, experimentId })
}