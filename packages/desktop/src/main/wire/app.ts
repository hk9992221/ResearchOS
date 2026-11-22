import { SystemClock, type Clock } from '@researchos/core/clock'
import { ConfigStore, type AppConfig } from '@researchos/core/config'
import { ConfirmService } from '@researchos/core/confirm'
import type { Db } from '@researchos/core/db/open'
import { EventBus, type ActivityPublished } from '@researchos/core/events'
import { AppError, AppErrorCode } from '@researchos/core/errors'
import { NullLogger, type Logger } from '@researchos/core/logger'
import { SettingsStore } from '@researchos/core/settings'
import { RelationService } from '@researchos/core/relations'
import { ExternalRefService } from '@researchos/core/externalrefs'
import { withBehaviors, type ProjectBehaviors } from '@researchos/core/behaviors'
import { WorkspaceManager } from '@researchos/core/workspace/index'
import { readMarker } from '@researchos/core/workspace/marker'
import { createAiModule, createAiOutputService, type AiModule, type SecretStore } from '@researchos/core/modules/ai/index'
import { createExperimentsAiPort } from '@researchos/core/modules/experiments/index'
import type { ExperimentsAiPort, ExperimentsJobPort, ExperimentsJobRequest } from '@researchos/core/modules/experiments/index'
import { ExperimentsService } from '@researchos/core/modules/experiments/index'
import { createMetricRegistry } from '@researchos/core/modules/experiments/metrics'
import type { MetricExtractor } from '@researchos/core/modules/experiments/metrics'
import { createKanbanModule, type KanbanModule } from '@researchos/core/modules/kanban/index'
import { ExportService } from '@researchos/core/modules/export/index'
import { createImportConfirmEffect } from '@researchos/core/modules/export/mcp'
import { IdeasService } from '@researchos/core/modules/ideas/index'
import { NotesModule } from '@researchos/core/modules/notes/index'
import { LiteratureModule } from '@researchos/core/modules/literature/index'
import { ZoteroConnection } from '@researchos/core/modules/literature/zotero/connection'
import { ZoteroSourceService } from '@researchos/core/modules/literature/zotero/source'
import { literatureNotesPort } from '@researchos/core/modules/literature/notes-port'
import { createColumnDeleteEffect } from '@researchos/core/modules/kanban/mcp'
import { PHASE1_DOMAIN_SOURCES, toolInfo, type Catalog } from '@researchos/core/modules/mcp/catalog'
import { Clients } from '@researchos/core/modules/mcp/clients'
import { Confirmations } from '@researchos/core/modules/mcp/confirmations'
import { createConfirmationEffects, createTrashEmptyExecutor } from '@researchos/core/modules/mcp/executors'
import { IdempotencyStore } from '@researchos/core/modules/mcp/idempotency'
import { Jobs } from '@researchos/core/modules/mcp/jobs'
import { parseAccessMode } from '@researchos/core/modules/mcp/policy'
import { MCP_HOST, MCP_PORT, McpHttpServer } from '@researchos/core/modules/mcp/sdk-server'
import { aiEmbeddingPort, SearchEngine, type PdfParser } from '@researchos/core/modules/search/index'
import { createWeeklyAiPort, WeeklyService } from '@researchos/core/modules/weekly/index'
import { DashboardService } from '@researchos/core/modules/dashboard/index'
import { OverviewService } from '@researchos/core/modules/overview/index'
import { ProvenanceService } from '@researchos/core/modules/provenance/index'
import { ContextGraphService } from '@researchos/core/modules/contextgraph/index'
import { ReadinessService } from '@researchos/core/modules/readiness/index'
import { ProjectService } from '@researchos/core/modules/project/service'

import * as nodePath from 'node:path'

import type {
  AppEvent,
  AppEventPayload,
  AppEventType,
  CommandChannel,
  PluginHostContext,
  PluginHostEvent,
  PluginPermission,
  WorkspaceStatus
} from '@researchos/shared'

import { registerAiHandlers } from '../ipc/handlers/ai'
import { registerExperimentsHandlers } from '../ipc/handlers/experiments'
import { registerDiagnosticsHandlers } from '../ipc/handlers/diagnostics'
import { registerExportHandlers } from '../ipc/handlers/export'
import { registerIdeasHandlers } from '../ipc/handlers/ideas'
import { registerJobsHandlers } from '../ipc/handlers/jobs'
import { registerLiteratureHandlers } from '../ipc/handlers/literature'
import {
  MCP_SETTINGS_DEFAULTS,
  mcpStatusFrom,
  registerMcpHandlers,
  type McpRuntimeAccess
} from '../ipc/handlers/mcp'
import { registerPluginHandlers } from '../ipc/handlers/plugins'
import { registerKanbanHandlers } from '../ipc/handlers/kanban'
import { registerNotesHandlers } from '../ipc/handlers/notes'
import { registerTrashHandlers } from '../ipc/handlers/trash'
import { registerProjectHandlers } from '../ipc/handlers/project'
import { registerSearchHandlers } from '../ipc/handlers/search'
import { registerWeeklyHandlers } from '../ipc/handlers/weekly'
import { registerWorkspaceHandlers } from '../ipc/handlers/workspace'
import type { CommandRouter } from '../ipc/router'
import type {
  PluginMarketService,
  PluginService,
  PluginUpdateScheduler
} from '@researchos/core/modules/plugins/index'
import type { PluginRuntimeBundle } from '../plugins/runtime'
import type { PluginDeclarativeSkill, SilentUpdateInfo } from '@researchos/shared'
import type { PluginSkillSink } from '@researchos/core/modules/plugins/declarative'

export interface AppIntegrationDeps {
  appVersion: string
  /** Overrides the OS config path (tests). */
  configPath?: string
  /** Renderer event sink (`webContents.send` in production). */
  publish?: (event: AppEvent) => void
  /** Test seam: avoids the OS keyring. */
  secretStore?: SecretStore
  pdfParser?: PdfParser
  clock?: Clock
  logger?: Logger
}

type WorkspaceEventType = AppEventType

function entityEventOp(eventType: string): 'created' | 'updated' | 'deleted' | 'restored' | null {
  switch (eventType) {
    case 'entity.created':
      return 'created'
    case 'entity.updated':
    case 'note.updated':
      return 'updated'
    case 'entity.deleted':
      return 'deleted'
    case 'entity.restored':
      return 'restored'
    default:
      return null
  }
}

/**
 * Phase-1 composition root: workspace lifecycle, project/readiness read models,
 * kanban/weekly/search/experiments services, AI module and the MCP host.
 * Electron-free so the same wiring runs in composition tests.
 */
export class AppIntegration {
  readonly config: ConfigStore
  private readonly deps: AppIntegrationDeps
  private readonly clock: Clock
  private readonly logger: Logger
  private readonly publish: (event: AppEvent) => void
  private readonly confirm = new ConfirmService({ clock: new SystemClock() })
  private readonly flushHandlers = new Map<string, () => Promise<void> | void>()

  private manager: WorkspaceManager | null = null
  private eventBus: EventBus
  private ai: AiModule | null = null
  private search: SearchEngine | null = null
  private projects: ProjectService | null = null
  private dashboard: DashboardService | null = null
  private overview: OverviewService | null = null
  private provenance: ProvenanceService | null = null
  private contextGraph: ContextGraphService | null = null
  private readiness: ReadinessService | null = null

  private readonly behaviors = new Map<string, ProjectBehaviors>()
  private readonly kanbanModules = new Map<string, KanbanModule>()
  private readonly weeklyServices = new Map<string, WeeklyService>()
  private readonly experimentsServices = new Map<string, ExperimentsService>()
  private readonly notesModules = new Map<string, NotesModule>()
  private ideasService: IdeasService | null = null
  private literatureService: LiteratureModule | null = null
  private exportService: ExportService | null = null

  private clients: Clients | null = null
  private confirmations: Confirmations | null = null
  private jobs: Jobs | null = null
  private idempotency: IdempotencyStore | null = null
  private mcpServer: McpHttpServer | null = null
  private mcpServerPort: number | null = null
  private mcpServerHost: string | null = null
  private mcpLastError: string | null = null
  private workspaceSettings: SettingsStore | null = null
  private workspaceOpenHook: ((manager: WorkspaceManager | null) => void) | null = null
  private aiAvailableCache = false
  private handlersRegistered = false
  private domainHandlersRegistered = false
  private domainChannels: CommandChannel[] = []
  private routerRef: CommandRouter | null = null
  private pluginService: PluginService | null = null
  private pluginMarket: PluginMarketService | null = null
  private pluginUpdates: PluginUpdateScheduler | null = null
  private pluginHandlersRegistered = false
  private pluginPromptResponder:
    | { respond(requestId: string, answer: { submitted?: boolean; confirmed?: boolean; value?: string }): boolean }
    | null = null
  private pluginContext: PluginHostContext = {
    activeProjectId: null,
    workspaceName: '',
    locale: 'en',
    theme: 'light'
  }
  private readonly metricRegistry = createMetricRegistry()

  constructor(deps: AppIntegrationDeps) {
    this.deps = deps
    this.clock = deps.clock ?? new SystemClock()
    this.logger = deps.logger ?? new NullLogger()
    this.config = new ConfigStore(deps.configPath)
    this.publish = deps.publish ?? (() => undefined)
    this.eventBus = this.createEventBus()
  }

  static async create(deps: AppIntegrationDeps): Promise<AppIntegration> {
    const integration = new AppIntegration(deps)
    await integration.start()
    return integration
  }

  /** Opens the configured workspace, if any, and brings up workspace services. */
  async start(): Promise<void> {
    let config: AppConfig
    try {
      config = this.config.load()
    } catch (error) {
      this.logger.warn('config load failed', { error: String(error) })
      return
    }
    if (config.workspace_root === '') return
    try {
      this.openWorkspace(config.workspace_root, 'open')
    } catch (error) {
      this.logger.warn('workspace open failed', { root: config.workspace_root, error: String(error) })
    }
  }

  private createEventBus(): EventBus {
    return new EventBus({
      clock: this.clock,
      publish: (event) => this.onActivityPublished(event),
      onPublishError: (error, event) =>
        this.logger.warn('activity publish failed', {
          activityId: event.activityId,
          error: String(error)
        })
    })
  }

  private onActivityPublished(event: ActivityPublished): void {
    const op = entityEventOp(event.eventType)
    if (op === null) return
    this.emit({
      type: 'entity.changed',
      projectId: this.projectIdForEntity(event.entityType, event.entityId) ?? '',
      entityType: event.entityType,
      entityId: event.entityId,
      op
    })
  }

  private projectIdForEntity(entityType: string, entityId: string): string | null {
    if (this.manager === null) return null
    if (entityType === 'project') return entityId
    const row = this.manager.global.db
      .prepare(`SELECT project_id FROM project_papers WHERE paper_id = ? LIMIT 1`)
      .get(entityId) as { project_id: string } | undefined
    for (const projectId of this.projects?.list({ includeArchived: true }).map((project) => project.id) ?? []) {
      const db = this.manager.project(projectId).db
      const found = db
        .prepare(`SELECT 1 AS hit FROM ${entityTypeTable(entityType)} WHERE id = ? LIMIT 1`)
        .get(entityId) as { hit: number } | undefined
      if (found !== undefined) return projectId
    }
    return row?.project_id ?? null
  }

  private emit<E extends WorkspaceEventType>(event: AppEventPayload<E>): void {
    this.publish(event)
  }

  get workspaceRoot(): string | null {
    return this.manager?.root ?? null
  }

  get ws(): WorkspaceManager {
    if (this.manager === null) {
      throw new AppError(AppErrorCode.NotWorkspace, 'workspace is not open')
    }
    return this.manager
  }

  /**
   * Opens (or initializes) a workspace and builds its services. Registration of
   * module IPC handlers happens once, on the first open.
   */
  openWorkspace(root: string, mode: 'open' | 'init' = 'open'): WorkspaceStatus {
    this.closeWorkspaceServices()
    this.manager =
      mode === 'init'
        ? WorkspaceManager.init(root, { clock: this.clock, logger: this.logger, config: this.config })
        : WorkspaceManager.open(root, { clock: this.clock, logger: this.logger, config: this.config })

    this.buildWorkspaceServices()
    if (this.routerRef !== null) this.registerDomainHandlers(this.routerRef)
    return this.status()
  }

  status(): WorkspaceStatus {
    if (this.manager === null || !this.manager.isOpen) {
      return {
        root: null,
        name: '',
        initialized: false,
        markerVersion: null,
        schemaVersion: null,
        open: false,
        projectCount: 0,
        warnings: []
      }
    }
    const status = this.manager.status()
    const marker = readMarker(this.manager.root)
    return {
      root: status.root,
      name: status.root.split(/[\\/]/).pop() ?? status.root,
      initialized: marker.state === 'present',
      markerVersion: marker.marker?.schema_version ?? null,
      schemaVersion: status.globalVersion,
      open: true,
      projectCount: status.projectCount,
      warnings: status.issues
    }
  }

  /** Registers workspace + landed domain handlers. Idempotent. */
  registerHandlers(router: CommandRouter): void {
    if (this.handlersRegistered) return
    this.handlersRegistered = true
    this.routerRef = router

    registerWorkspaceHandlers(router, {
      getManager: () => this.manager,
      setManager: (manager) => {
        if (manager === null) {
          void this.closeWorkspace()
        } else {
          this.closeWorkspaceServices()
          this.manager = manager
          this.buildWorkspaceServices()
          if (this.routerRef !== null) this.registerDomainHandlers(this.routerRef)
        }
      },
      config: this.config,
      onMoved: () => {
        // Root changed in place: remount plugin runtime + ros-asset root.
        this.workspaceOpenHook?.(this.manager)
      }
    })

    // MCP settings channels are registered eagerly: `mcp.status` must answer
    // before a workspace exists so Settings → MCP renders controls instead of
    // an error card. Runtime-bound channels answer NOT_WORKSPACE until then.
    registerMcpHandlers(router, {
      status: () => {
        const server = this.mcpServer
        if (server === null) return null
        const snapshot = server.status()
        return { ...snapshot, lastError: this.mcpLastError ?? snapshot.lastError }
      },
      settings: () => this.workspaceSettings,
      runtime: () => this.mcpRuntime(),
      appVersion: this.deps.appVersion,
      publish: (event) => this.publish(event)
    })

    if (this.manager !== null) this.registerDomainHandlers(router)
  }

  private registerDomainHandlers(router: CommandRouter): void {
    if (this.domainChannels.length > 0) {
      for (const channel of this.domainChannels) router.unregisterHandler(channel)
      this.domainChannels = []
    }
    this.domainHandlersRegistered = true
    const before = new Set(router.channels())

    registerProjectHandlers(router, {
      projects: this.requireProjects(),
      dashboard: this.requireDashboard(),
      overview: this.requireOverview(),
      provenance: this.requireProvenance(),
      context: this.requireContextGraph(),
      readiness: this.requireReadiness()
    })

    registerJobsHandlers(router, { jobs: () => this.jobs })
    registerNotesHandlers(router, { notes: (projectId) => this.notes(projectId) })
    registerIdeasHandlers(router, { service: this.ideas() })
    registerExportHandlers(router, { service: () => this.requireExport() })
    registerTrashHandlers(router, { behavior: (projectId) => this.behavior(projectId) })
    registerLiteratureHandlers(router, { module: () => this.literature() })
    registerDiagnosticsHandlers(router, {
      workspace: () => this.manager,
      externalRefs: (projectId, refIds) => {
        const behavior = this.behavior(projectId)
        const service = new ExternalRefService(behavior.handle, behavior.events, this.clock)
        return service.check(refIds ?? []).map((probe) => ({
          id: probe.id,
          ownerType: probe.ownerType,
          ownerId: probe.ownerId,
          field: probe.field,
          kind: probe.kind,
          path: probe.path,
          status: probe.status,
          detail: probe.exists ? null : probe.reason
        }))
      }
    })
    registerKanbanHandlers(router, { resolve: (projectId) => this.kanban(projectId) })
    registerWeeklyHandlers(router, {
      project: (projectId) => this.weekly(projectId),
      workspace: () => {
        const projectId = this.firstProjectId()
        if (projectId === null) throw new AppError(AppErrorCode.NotWorkspace, 'no project is open')
        const service = this.weekly(projectId)
        return {
          template: () => service.template(),
          skills: () => service.skills(),
          saveSkill: (input) => service.saveSkill(input),
          deleteSkill: (id) => service.deleteSkill(id),
          setDefaultSkill: (id) => service.setDefaultSkill(id)
        }
      },
      submitJob: async (request) => ({
        jobId: await this.submitJob(request.channel, request.projectId, false, request.run)
      })
    })
    registerSearchHandlers(router, { search: this.requireSearch() })
    registerAiHandlers(router, {
      ai: this.requireAi(),
      project: (projectId) => ({ db: this.ws.project(projectId).db })
    })
    registerExperimentsHandlers(router, {
      workspace: () => this.manager,
      jobs: this.experimentsJobPort(),
      ai: this.experimentsAiPort(),
      aiAvailable: () => this.aiAvailableCache
    })
    this.domainChannels = router.channels().filter((channel) => !before.has(channel))
  }

  /**
   * Builds every workspace-bound service from the current manager. Both the
   * startup path (`openWorkspace`) and the wizard's `workspace.create` /
   * `workspace.open` (through `setManager`) end here, so neither can leave a
   * half-wired workspace behind for `registerDomainHandlers` to trip over.
   */
  private buildWorkspaceServices(): void {
    if (this.manager === null) return
    const manager = this.manager
    this.eventBus = this.createEventBus()
    this.ai = createAiModule({
      globalDb: manager.global.db,
      clock: this.clock,
      logger: this.logger,
      onActivity: (activity) => {
        const event: AppEventPayload<'ai.activity'> = { type: 'ai.activity', ...activity }
        this.emit(event)
      },
      ...(this.deps.secretStore === undefined ? {} : { secrets: this.deps.secretStore })
    })
    this.projects = new ProjectService({
      workspace: manager,
      clock: this.clock,
      events: this.activityEmitter(),
      confirmations: {
        issue: (kind, scope) => this.confirm.issue({ kind, projectId: scope[0] ?? '' }),
        consume: (token, kind, scope) =>
          this.confirm.consume(token, { kind, projectId: scope[0] ?? '' })
      }
    })
    this.dashboard = new DashboardService({ project: this.projects, clock: this.clock })
    this.overview = new OverviewService({ workspace: manager, clock: this.clock })
    this.provenance = new ProvenanceService({ workspace: manager, clock: this.clock })
    this.contextGraph = new ContextGraphService({ workspace: manager, clock: this.clock })
    this.readiness = new ReadinessService({ workspace: manager, clock: this.clock })
    this.search = new SearchEngine(manager, {
      clock: this.clock,
      logger: this.logger,
      embedding: aiEmbeddingPort(this.ai.gateway, this.ai.roles),
      ...(this.deps.pdfParser === undefined ? {} : { parser: this.deps.pdfParser })
    })
    this.exportService = new ExportService({
      workspace: manager,
      clock: this.clock,
      events: this.eventBus,
      onExportProgress: (jobId, progress) =>
        this.emit({ type: 'job.progress', jobId, percent: progress.percent, stage: progress.stage }),
      onImportProgress: (jobId, percent, stage) =>
        this.emit({ type: 'job.progress', jobId, percent, stage })
    })
    this.startMcp()
    this.refreshAiAvailability()
    this.workspaceOpenHook?.(manager)
  }

  private activityEmitter(): {
    emit: (
      db: Db,
      event: {
        type: ActivityPublished['eventType']
        entityType: ActivityPublished['entityType']
        entityId: string
        summary: string
        actor: ActivityPublished['actor']
        old?: Record<string, unknown>
        new?: Record<string, unknown>
      }
    ) => void
  } {
    return {
      emit: (db, event) => {
        this.eventBus.emit(
          { db },
          {
            type: event.type,
            entityType: event.entityType,
            entityId: event.entityId,
            summary: event.summary,
            actor: event.actor,
            ...(event.old === undefined ? {} : { old: event.old }),
            ...(event.new === undefined ? {} : { new: event.new })
          }
        )
      }
    }
  }

  behavior(projectId: string): ProjectBehaviors {
    const cached = this.behaviors.get(projectId)
    if (cached !== undefined) return cached
    const behavior = withBehaviors(this.ws.project(projectId), {
      clock: this.clock,
      confirm: this.confirm,
      logger: this.logger,
      publish: (event) => this.onActivityPublished(event)
    })
    this.behaviors.set(projectId, behavior)
    return behavior
  }

  kanban(projectId: string): KanbanModule {
    const cached = this.kanbanModules.get(projectId)
    if (cached !== undefined) return cached
    const behavior = this.behavior(projectId)
    const handle = behavior.handle
    const module = createKanbanModule({
      db: handle.db,
      projectId,
      projectStatus: (id) => (this.requireProjects().get(id).status === 'archived' ? 'archived' : 'active'),
      events: {
        emit: (tx, event) => {
          behavior.events.emit(tx, {
            type: event.type,
            entityType: event.entityType,
            entityId: event.entityId,
            summary: event.summary,
            actor: event.actor,
            ...(event.old === undefined ? {} : { old: event.old }),
            ...(event.new === undefined ? {} : { new: event.new })
          })
        }
      },
      trash: behavior.trash,
      relations: new RelationService(handle, behavior.events, {
        global: { db: this.ws.global.db },
        clock: this.clock
      }),
      clock: this.clock,
      logger: this.logger
    })
    this.kanbanModules.set(projectId, module)
    return module
  }

  notes(projectId: string): NotesModule {
    const cached = this.notesModules.get(projectId)
    if (cached !== undefined) return cached
    const module = NotesModule.forProject(this.ws.project(projectId), { clock: this.clock })
    this.notesModules.set(projectId, module)
    return module
  }

  literature(): LiteratureModule {
    if (this.literatureService !== null) return this.literatureService
    const connection = new ZoteroConnection({ config: this.config, clock: this.clock })
    this.literatureService = new LiteratureModule({
      workspace: this.ws,
      clock: this.clock,
      events: this.eventBus,
      logger: this.logger,
      connection,
      zotero: new ZoteroSourceService({ connection, clock: this.clock }),
      notes: (projectId) => literatureNotesPort(this.notes(projectId))
    })
    return this.literatureService
  }

  workspaceManager(): WorkspaceManager | null {
    return this.manager
  }

  /** Late registration once the Electron plugin runtime is constructed. */
  attachPluginRuntime(bundle: PluginRuntimeBundle): void {
    this.pluginService = bundle.service
    this.pluginMarket = bundle.market
    this.pluginUpdates = bundle.updates
    this.pluginPromptResponder = bundle.promptResponder ?? null
    if (this.pluginHandlersRegistered || this.routerRef === null) return
    registerPluginHandlers(this.routerRef, {
      service: () => this.requirePlugins(),
      market: () => this.requirePluginMarket(),
      updates: () => this.requirePluginUpdates(),
      respondPrompt: (requestId, answer) => this.pluginPromptResponder?.respond(requestId, answer) ?? false,
      hostContext: () => this.pluginHostContext(),
      setHostContext: (patch) => this.setPluginHostContext(patch)
    })
    this.pluginHandlersRegistered = true
  }

  /** AI gateway for host bridges; null until the AI module is created. */
  aiGateway(): AiModule['gateway'] | null {
    return this.ai?.gateway ?? null
  }

  pluginHostContext(): PluginHostContext {
    const workspaceName =
      this.pluginContext.workspaceName !== ''
        ? this.pluginContext.workspaceName
        : this.manager === null
          ? ''
          : nodePath.basename(this.manager.root)
    return { ...this.pluginContext, workspaceName }
  }

  /**
   * Renderer reports the active project, theme and locale. Only real changes
   * are broadcast, so a plugin main.js sees `project.changed` once per switch.
   */
  setPluginHostContext(patch: {
    projectId?: string | null
    theme?: 'light' | 'dark'
    locale?: string
  }): PluginHostContext {
    const previous = this.pluginHostContext()
    const next: PluginHostContext = {
      ...previous,
      ...(patch.projectId === undefined ? {} : { activeProjectId: patch.projectId }),
      ...(patch.theme === undefined ? {} : { theme: patch.theme }),
      ...(patch.locale === undefined ? {} : { locale: patch.locale })
    }
    this.pluginContext = next
    if (next.activeProjectId !== previous.activeProjectId) {
      this.publishPluginHostEvent({
        type: 'project.changed',
        payload: { projectId: next.activeProjectId }
      })
    }
    if (next.theme !== previous.theme) {
      this.publishPluginHostEvent({ type: 'theme.changed', payload: { theme: next.theme } })
    }
    return next
  }

  /** Pushes one host event to running plugins and mounted panels. */
  publishPluginHostEvent(event: PluginHostEvent): void {
    this.pluginService?.broadcastEvent(event)
    this.publishPluginEvent(
      typeof (event.payload as { pluginId?: unknown } | undefined)?.pluginId === 'string'
        ? String((event.payload as { pluginId: string }).pluginId)
        : '',
      event
    )
  }

  publishPluginEvent(pluginId: string, event: PluginHostEvent): void {
    this.emit({ type: 'plugins.event', pluginId, event })
  }

  publishPluginPrompt(request: {
    requestId: string
    pluginId: string
    pluginName: string
    kind: 'prompt' | 'confirm'
    params: unknown
    permissions: PluginPermission[]
  }): void {
    this.emit({
      type: 'plugins.prompt.request',
      requestId: request.requestId,
      pluginId: request.pluginId,
      pluginName: request.pluginName,
      kind: request.kind,
      params: request.params as never,
      permissions: request.permissions
    })
  }

  publishPluginPromptCancel(requestId: string): void {
    this.emit({ type: 'plugins.prompt.cancel', requestId })
  }

  get plugins(): PluginService {
    return this.requirePlugins()
  }

  private requirePlugins(): PluginService {
    if (this.pluginService === null) {
      throw new AppError(AppErrorCode.NotWorkspace, 'plugin runtime is not mounted')
    }
    return this.pluginService
  }

  private requirePluginMarket(): PluginMarketService {
    if (this.pluginMarket === null) {
      throw new AppError(AppErrorCode.NotWorkspace, 'plugin market is not mounted')
    }
    return this.pluginMarket
  }

  private requirePluginUpdates(): PluginUpdateScheduler {
    if (this.pluginUpdates === null) {
      throw new AppError(AppErrorCode.NotWorkspace, 'plugin update scheduler is not mounted')
    }
    return this.pluginUpdates
  }

  registerPluginSkill(skill: PluginDeclarativeSkill): void {
    for (const service of this.weeklyServices.values()) {
      service.registerSkill({
        id: skill.id,
        name: skill.name,
        prompt: skill.prompt,
        source: 'plugin',
        builtin: false
      })
    }
  }

  unregisterPluginSkill(id: string): void {
    for (const service of this.weeklyServices.values()) service.unregisterSkill(id)
  }

  publishPluginsChanged(): void {
    this.emit({ type: 'plugins.changed' })
  }

  /** A background update replaced a plugin: the renderer shows the toast. */
  publishPluginsUpdated(update: SilentUpdateInfo): void {
    this.emit({
      type: 'plugins.updated',
      pluginId: update.pluginId,
      name: update.name,
      fromVersion: update.fromVersion,
      version: update.version
    })
  }

  registerPluginMetricExtractor(extractor: MetricExtractor): () => void {
    return this.metricRegistry.register(extractor)
  }

  /** Sink object handed to createPluginRuntimeBundle. */
  pluginSkillSink(): PluginSkillSink {
    return {
      registerSkill: (skill) => this.registerPluginSkill(skill),
      unregisterSkill: (id) => this.unregisterPluginSkill(id)
    }
  }

  ideas(): IdeasService {
    if (this.ideasService !== null) return this.ideasService
    this.ideasService = new IdeasService({
      workspace: this.ws,
      events: this.eventBus,
      clock: this.clock,
      logger: this.logger
    })
    return this.ideasService
  }

  private requireExport(): ExportService {
    if (this.exportService === null) throw new AppError(AppErrorCode.NotWorkspace, 'export service is not open')
    return this.exportService
  }

  weekly(projectId: string): WeeklyService {
    const cached = this.weeklyServices.get(projectId)
    if (cached !== undefined) return cached
    const behavior = this.behavior(projectId)
    const handle = behavior.handle
    const service = new WeeklyService({
      projectId,
      handle,
      globalDb: this.ws.global.db,
      root: this.ws.root,
      clock: this.clock,
      events: {
        emit: (event) => {
          behavior.events.emit(
            { db: handle.db },
            {
              type: event.type,
              entityType: event.entityType,
              entityId: event.entityId,
              summary: event.summary,
              actor: event.actor,
              at: event.at,
              ...(event.old === undefined ? {} : { old: event.old }),
              ...(event.new === undefined ? {} : { new: event.new })
            }
          )
        }
      },
      trash: {
        softDelete: (ref, actor) => behavior.trash.softDelete({ db: handle.db }, ref, actor),
        restore: (ref, actor) => behavior.trash.restore({ db: handle.db }, ref, actor)
      },
      logger: this.logger,
      ...(this.ai === null
        ? {}
        : {
            ai: createWeeklyAiPort({
              gateway: this.ai.gateway,
              outputs: createAiOutputService(handle.db, { projectId }),
              projectId
            })
          })
    })
    this.weeklyServices.set(projectId, service)
    return service
  }

  experiments(projectId: string): ExperimentsService {
    const cached = this.experimentsServices.get(projectId)
    if (cached !== undefined) return cached
    const service = new ExperimentsService({
      workspace: this.ws,
      projectId,
      clock: this.clock,
      logger: this.logger,
      metrics: this.metricRegistry,
      ai: this.experimentsAiPortFor(projectId)
    })
    this.experimentsServices.set(projectId, service)
    return service
  }

  private experimentsAiPortFor(projectId: string): ExperimentsAiPort {
    const ai = this.requireAi()
    return createExperimentsAiPort({
      gateway: ai.gateway,
      outputs: createAiOutputService(this.ws.project(projectId).db, { projectId }),
      projectId
    })
  }

  /** Port handed to IPC handlers: resolves the owning project per request. */
  private experimentsAiPort(): ExperimentsAiPort | undefined {
    if (this.ai === null) return undefined
    return {
      completeStructured: (request) => {
        const projectId = request.projectId !== '' ? request.projectId : this.firstProjectId()
        if (projectId === null) {
          throw new AppError(AppErrorCode.NotWorkspace, 'no project is open')
        }
        return this.experimentsAiPortFor(projectId).completeStructured(request)
      }
    }
  }

  private experimentsJobPort(): ExperimentsJobPort {
    return {
      submit: async (request: ExperimentsJobRequest) => {
        const jobs = this.jobs
        if (jobs === null) throw new AppError(AppErrorCode.NotWorkspace, 'MCP runtime is not open')
        const job = jobs.submit(
          {
            clientId: request.clientId === '' ? 'desktop' : request.clientId,
            clientName: 'Desktop',
            transport: 'http'
          },
          {
            tool: 'experiments.ai',
            projectId: request.projectId,
            readOnly: request.readOnly,
            params: request.params,
            traceId: request.traceId,
            run: async ({
              signal,
              progress
            }: {
              signal: AbortSignal
              progress: (percent: number, stage?: string) => void
            }) => request.run({ signal, progress: (percent, stage) => progress(percent, stage ?? '') })
          }
        )
        return { jobId: job.id }
      }
    }
  }

  private submitJob(
    channel: string,
    projectId: string,
    readOnly: boolean,
    run: (context: { signal?: AbortSignal; progress?: (percent: number, stage: string) => void }) => Promise<unknown>,
    clientId = 'desktop'
  ): Promise<string> {
    const jobs = this.jobs
    if (jobs === null) return Promise.reject(new AppError(AppErrorCode.NotWorkspace, 'MCP runtime is not open'))
    const job = jobs.submit(
      { clientId, clientName: clientId === 'desktop' ? 'Desktop' : clientId, transport: 'http' },
      {
        tool: channel,
        projectId,
        readOnly,
        run: async ({
          signal,
          progress
        }: {
          signal: AbortSignal
          progress: (percent: number, stage?: string) => void
        }) => run({ signal, progress: (percent, stage) => progress(percent, stage) })
      }
    )
    return Promise.resolve(job.id)
  }

  private startMcp(): void {
    if (this.manager === null || this.ai === null) return
    const globalDb = this.manager.global.db
    const settings = new SettingsStore(globalDb, 'workspace', this.clock)
    this.workspaceSettings = settings
    this.clients = new Clients(globalDb, { clock: this.clock })
    this.confirmations = new Confirmations(globalDb, { clock: this.clock })
    this.jobs = new Jobs(globalDb, {
      clock: this.clock,
      onProgress: (job) => {
        this.emit({
          type: 'job.progress',
          jobId: job.id,
          percent: job.progress,
          stage: job.stage
        })
      },
      onDone: (job) => {
        if (job.state !== 'succeeded' && job.state !== 'failed' && job.state !== 'cancelled') return
        const event: AppEventPayload<'job.done'> = {
          type: 'job.done',
          jobId: job.id,
          state: job.state,
          ...(job.error === null ? {} : { error: job.error })
        }
        this.emit(event)
      }
    })
    this.idempotency = new IdempotencyStore(globalDb, { clock: this.clock })

    const trashExecutor = createTrashEmptyExecutor({
      workspace: this.manager,
      events: this.eventBus,
      confirm: this.confirm,
      clock: this.clock
    })
    this.confirmationEffects = createConfirmationEffects(trashExecutor, {
      columnDelete: createColumnDeleteEffect((projectId: string) => this.kanban(projectId)),
      ...(this.exportService === null
        ? {}
        : { projectImportConfirm: createImportConfirmEffect(this.exportService) })
    })

    this.mcpLastError = null
    this.mcpServer = this.buildMcpServer(settings)
    if (this.mcpDesiredRunning(settings)) {
      void this.mcpServer
        .start()
        .then(() => {
          this.mcpLastError = null
          this.publishMcpStatus()
        })
        .catch((error: unknown) => {
          this.mcpLastError = error instanceof Error ? error.message : String(error)
          this.logger.error('mcp server start failed', { error: this.mcpLastError })
          this.publishMcpStatus()
        })
    }
  }

  private buildMcpServer(settings: SettingsStore): McpHttpServer {
    if (this.manager === null) throw new AppError(AppErrorCode.NotWorkspace, 'workspace is not open')
    const port = settings.getNumber('mcp.port', MCP_PORT)
    const host = settings.get('mcp.http.host') ?? MCP_HOST
    const server = new McpHttpServer({
      workspace: this.manager,
      clients: this.requireMcpClients(),
      confirmations: this.requireMcpConfirmations(),
      jobs: this.requireMcpJobs(),
      idempotency: this.requireMcpIdempotency(),
      events: this.eventBus,
      confirm: this.confirm,
      clock: this.clock,
      appVersion: this.deps.appVersion,
      host,
      port,
      accessMode: () => parseAccessMode(settings.get('mcp.access_mode')),
      aiAvailable: () => this.computeAiAvailable(),
      domainTools: PHASE1_DOMAIN_TOOLS,
      servicesFor: (projectId: string | null) => this.mcpServices(projectId),
      enforceToolCount: true
    })
    this.mcpServerPort = port
    this.mcpServerHost = host
    return server
  }

  private mcpDesiredRunning(settings: SettingsStore): boolean {
    return (
      settings.getBoolean('mcp.enabled', MCP_SETTINGS_DEFAULTS.enabled) &&
      settings.getBoolean('mcp.http.enabled', MCP_SETTINGS_DEFAULTS.httpEnabled)
    )
  }

  /** Workspace-bound MCP services for IPC; null until `startMcp` has run. */
  private mcpRuntime(): McpRuntimeAccess | null {
    const clients = this.clients
    const confirmations = this.confirmations
    const jobs = this.jobs
    const effects = this.confirmationEffects
    if (
      this.mcpServer === null ||
      this.workspaceSettings === null ||
      clients === null ||
      confirmations === null ||
      jobs === null ||
      effects === null
    ) {
      return null
    }
    return {
      clients,
      confirmations,
      jobs,
      effects,
      tools: () => {
        const server = this.mcpServer
        if (server === null) throw new AppError(AppErrorCode.NotWorkspace, 'MCP runtime is not open')
        return server.visibleTools().map(toolInfo)
      },
      start: () => this.startMcpServer(),
      stop: () => this.stopMcpServer(),
      applySettings: () => this.applyMcpSettings()
    }
  }

  private async startMcpServer(): Promise<void> {
    const settings = this.requireMcpSettings()
    settings.set('mcp.enabled', '1')
    settings.set('mcp.http.enabled', '1')
    await this.applyMcpSettings()
  }

  private async stopMcpServer(): Promise<void> {
    const settings = this.requireMcpSettings()
    settings.set('mcp.enabled', '0')
    settings.set('mcp.http.enabled', '0')
    await this.applyMcpSettings()
  }

  /**
   * Applies the persisted `mcp.*` keys: stop when disabled, rebind when the
   * port changed, and start idempotently when enabled. The access mode is
   * read per request by the server, so switching it needs no restart.
   */
  private async applyMcpSettings(): Promise<void> {
    const settings = this.requireMcpSettings()
    const port = settings.getNumber('mcp.port', MCP_PORT)
    const host = settings.get('mcp.http.host') ?? MCP_HOST
    if (!this.mcpDesiredRunning(settings)) {
      if (this.mcpServer?.running === true) await this.mcpServer.stop()
      return
    }
    if (this.mcpServer === null || this.mcpServerPort !== port || this.mcpServerHost !== host) {
      if (this.mcpServer?.running === true) await this.mcpServer.stop()
      this.mcpServer = this.buildMcpServer(settings)
    }
    try {
      await this.mcpServer.start()
      this.mcpLastError = null
    } catch (error) {
      this.mcpLastError = error instanceof Error ? error.message : String(error)
      throw error
    }
  }

  private publishMcpStatus(): void {
    this.publish({
      type: 'mcp.status.changed',
      status: mcpStatusFrom(this.mcpServer?.status() ?? null, this.workspaceSettings)
    })
  }

  private requireMcpSettings(): SettingsStore {
    if (this.workspaceSettings === null) {
      throw new AppError(AppErrorCode.NotWorkspace, 'workspace is not open')
    }
    return this.workspaceSettings
  }

  private requireMcpClients(): Clients {
    if (this.clients === null) throw new AppError(AppErrorCode.NotWorkspace, 'MCP runtime is not open')
    return this.clients
  }

  private requireMcpConfirmations(): Confirmations {
    if (this.confirmations === null) {
      throw new AppError(AppErrorCode.NotWorkspace, 'MCP runtime is not open')
    }
    return this.confirmations
  }

  private requireMcpJobs(): Jobs {
    if (this.jobs === null) throw new AppError(AppErrorCode.NotWorkspace, 'MCP runtime is not open')
    return this.jobs
  }

  private requireMcpIdempotency(): IdempotencyStore {
    if (this.idempotency === null) {
      throw new AppError(AppErrorCode.NotWorkspace, 'MCP runtime is not open')
    }
    return this.idempotency
  }

  private mcpServices(projectId: string | null): Record<string, unknown> {
    const workspaceServices: Record<string, unknown> = {
      project: this.requireProjects(),
      dashboard: this.requireDashboard(),
      overview: this.requireOverview(),
      provenance: this.requireProvenance(),
      context: this.requireContextGraph(),
      readiness: this.requireReadiness(),
      'search.runtime': { engine: this.requireSearch(), jobs: this.mcpJobPort() },
      // Workspace-scoped domains: their tools take no `project_id`
      // (`paper_list`, `zotero_status`, `literature_overview_get`, …), so they
      // must resolve from the workspace-level bag as well as the project one.
      'ideas.runtime': { service: this.ideas(), aiAvailable: this.aiAvailableCache },
      'literature.runtime': { service: () => this.literature(), jobs: this.mcpJobPort() },
      'export.runtime': {
        service: this.requireExport(),
        jobs: this.mcpJobPort(),
        confirmations: this.requireMcpConfirmations()
      }
    }
    if (projectId === null) return workspaceServices
    return {
      ...workspaceServices,
      kanban: this.kanban(projectId),
      'weekly.runtime': {
        service: (id: string) => this.weekly(id),
        jobs: this.mcpJobPort(),
        confirmations: this.requireMcpConfirmations()
      },
      notes: this.notes(projectId),
      'experiments.runtime': {
        service: (id: string) => this.experiments(id),
        jobs: this.experimentsJobPort(),
        aiAvailable: this.aiAvailableCache
      }
    }
  }

  /**
   * One job-submission port for every MCP runtime that runs work in the
   * background. The tool name comes from the request, so an export job is not
   * filed under another tool's name, and the new job id is handed back because
   * the MCP contract reports `job_id` to the caller.
   */
  private mcpJobPort(): {
    submit(request: {
      tool: string
      projectId: string | null
      clientId: string
      readOnly: boolean
      params: Record<string, unknown>
      traceId: string
      signal: AbortSignal
      run: (context: {
        signal: AbortSignal
        progress: (percent: number, stage?: string) => void
      }) => Promise<unknown>
    }): Promise<{ jobId: string }>
  } {
    return {
      submit: async (request) => {
        const jobId = await this.submitJob(
          request.tool,
          request.projectId ?? '',
          request.readOnly,
          (context) =>
            request.run({
              signal: context.signal ?? new AbortController().signal,
              progress: (percent, stage) => context.progress?.(percent, stage ?? '')
            }),
          request.clientId
        )
        return { jobId }
      }
    }
  }

  private refreshAiAvailability(): void {
    this.aiAvailableCache = this.computeAiAvailable()
  }

  private computeAiAvailable(): boolean {
    const ai = this.ai
    if (ai === null) return false
    try {
      ai.roles.resolve('chat_main')
      return true
    } catch {
      return false
    }
  }

  registerFlushHandler(name: string, handler: () => Promise<void> | void): void {
    this.flushHandlers.set(name, handler)
  }

  /** Runs every registered editor/note flush; failures are logged, never fatal. */
  async flushAll(): Promise<void> {
    for (const [name, handler] of this.flushHandlers) {
      try {
        await handler()
      } catch (error) {
        this.logger.error('flush handler failed', { name, error: String(error) })
      }
    }
  }

  catalog(): Catalog {
    if (this.mcpServer === null) {
      throw new AppError(AppErrorCode.NotWorkspace, 'MCP runtime is not open')
    }
    return this.mcpServer.catalog
  }

  get mcp(): { clients: Clients | null; confirmations: Confirmations | null; jobs: Jobs | null; idempotency: IdempotencyStore | null } {
    return {
      clients: this.clients,
      confirmations: this.confirmations,
      jobs: this.jobs,
      idempotency: this.idempotency
    }
  }

  confirmationEffects: ReturnType<typeof createConfirmationEffects> | null = null

  private firstProjectId(): string | null {
    return this.requireProjects().list({ limit: 1 })[0]?.id ?? null
  }

  private requireAi(): AiModule {
    if (this.ai === null) throw new AppError(AppErrorCode.NotWorkspace, 'AI module is not open')
    return this.ai
  }

  /** Project service for read-only host bridges (activity, etc.). */
  projectService(): ProjectService {
    return this.requireProjects()
  }

  private requireProjects(): ProjectService {
    if (this.projects === null) throw new AppError(AppErrorCode.NotWorkspace, 'workspace is not open')
    return this.projects
  }

  private requireDashboard(): DashboardService {
    if (this.dashboard === null) throw new AppError(AppErrorCode.NotWorkspace, 'workspace is not open')
    return this.dashboard
  }

  private requireOverview(): OverviewService {
    if (this.overview === null) throw new AppError(AppErrorCode.NotWorkspace, 'workspace is not open')
    return this.overview
  }

  private requireProvenance(): ProvenanceService {
    if (this.provenance === null) throw new AppError(AppErrorCode.NotWorkspace, 'workspace is not open')
    return this.provenance
  }

  private requireContextGraph(): ContextGraphService {
    if (this.contextGraph === null) throw new AppError(AppErrorCode.NotWorkspace, 'workspace is not open')
    return this.contextGraph
  }

  private requireReadiness(): ReadinessService {
    if (this.readiness === null) throw new AppError(AppErrorCode.NotWorkspace, 'workspace is not open')
    return this.readiness
  }

  private requireSearch(): SearchEngine {
    if (this.search === null) throw new AppError(AppErrorCode.NotWorkspace, 'search is not open')
    return this.search
  }

  /** After a migration commit the manager repointed in place; remount consumers. */
  notifyWorkspaceMoved(): void {
    this.workspaceOpenHook?.(this.manager)
  }

  setWorkspaceOpenHook(hook: (manager: WorkspaceManager | null) => void): void {
    this.workspaceOpenHook = hook
    hook(this.manager)
  }

  async closeWorkspace(): Promise<void> {
    this.workspaceOpenHook?.(null)
    this.closeWorkspaceServices()
    this.manager?.close()
    this.manager = null
  }

  private closeWorkspaceServices(): void {
    if (this.mcpServer !== null) void this.mcpServer.stop()
    this.mcpServer = null
    this.mcpServerPort = null
    this.mcpServerHost = null
    this.mcpLastError = null
    this.search?.close()
    this.search = null
    this.behaviors.clear()
    this.kanbanModules.clear()
    this.weeklyServices.clear()
    this.experimentsServices.clear()
    this.notesModules.clear()
    this.ideasService = null
    this.literatureService = null
    this.pluginService = null
    this.pluginMarket = null
    this.pluginUpdates = null
    this.exportService = null
    this.workspaceSettings = null
    this.clients = null
    this.confirmations = null
    this.jobs = null
    this.idempotency = null
  }
}

/* ------------------------------------------------------------------ helpers */

function entityTypeTable(entityType: string): string {
  switch (entityType) {
    case 'note':
      return 'notes'
    case 'research_database':
      return 'research_databases'
    case 'db_field':
      return 'db_fields'
    case 'db_record':
      return 'db_records'
    case 'idea':
      return 'ideas'
    case 'evidence':
      return 'evidence'
    case 'experiment':
      return 'experiments'
    case 'run':
      return 'runs'
    case 'snapshot':
      return 'snapshots'
    case 'task':
      return 'tasks'
    case 'weekly_report':
      return 'weekly_reports'
    case 'entity_relation':
      return 'entity_relations'
    default:
      return 'notes'
  }
}

const PHASE1_DOMAIN_TOOLS = PHASE1_DOMAIN_SOURCES