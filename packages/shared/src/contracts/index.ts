import type { AiCommands } from './ai'
import type { AppCommands } from './app'
import type { ExperimentsCommands } from './experiments'
import type { DiagnosticsCommands } from './diagnostics'
import type { ExportCommands } from './export'
import type { IdeaCommands } from './ideas'
import type { JobsCommands } from './jobs'
import type { KanbanCommands } from './kanban'
import type { LiteratureCommands } from './literature'
import type { McpCommands } from './mcp'
import type { NotesCommands } from './notes'
import type { PluginCommands } from './plugins'
import type { ProjectCommands } from './project'
import type { SearchCommands } from './search'
import type { SettingsCommands } from './settings'
import type { TrashCommands } from './trash'
import type { WeeklyCommands } from './weekly'
import type { WorkspaceCommands } from './workspace'

export * from './ai'
export * from './app'
export * from './experiments'
export * from './diagnostics'
export * from './export'
export * from './ideas'
export * from './jobs'
export * from './kanban'
export * from './literature'
export * from './mcp'
export * from './notes'
export * from './plugins'
export * from './project'
export * from './search'
export * from './settings'
export * from './trash'
export * from './weekly'
export * from './workspace'

export type CommandMap = AppCommands &
  WorkspaceCommands &
  ProjectCommands &
  NotesCommands &
  LiteratureCommands &
  IdeaCommands &
  ExperimentsCommands &
  KanbanCommands &
  JobsCommands &
  WeeklyCommands &
  SearchCommands &
  AiCommands &
  McpCommands &
  ExportCommands &
  DiagnosticsCommands &
  PluginCommands &
  SettingsCommands &
  TrashCommands

export type CommandChannel = keyof CommandMap
export type CommandRequest<C extends CommandChannel> = CommandMap[C]['req']
export type CommandResponse<C extends CommandChannel> = CommandMap[C]['res']

/**
 * Runtime registry for channel validation. The two assertions below make the
 * list and CommandMap unable to drift apart: a missing or extra entry is a
 * compile error, not a runtime surprise.
 */
export const COMMAND_CHANNELS = [
  'ai.availability',
  'ai.chat',
  'ai.models.addManual',
  'ai.models.list',
  'ai.models.refresh',
  'ai.models.remove',
  'ai.outputs.confirm',
  'ai.outputs.discard',
  'ai.outputs.get',
  'ai.outputs.list',
  'ai.outputs.update',
  'ai.probe',
  'ai.providers.create',
  'ai.providers.delete',
  'ai.providers.list',
  'ai.providers.test',
  'ai.providers.update',
  'ai.roles.clear',
  'ai.roles.list',
  'ai.roles.set',
  'ai.secrets.delete',
  'ai.secrets.set',
  'ai.secrets.status',
  'app.config.get',
  'app.config.update',
  'app.diagnostics',
  'app.flush',
  'app.info',
  'app.log',
  'app.nativeSmoke',
  'app.pickFile',
  'app.pickFolder',
  'app.preferences.get',
  'app.preferences.set',
  'app.recentProjects',
  'app.revealPath',
  'app.saveFile',
  'app.saveTextFile',
  'app.theme.get',
  'app.theme.set',
  'experiments.ai.outline',
  'experiments.ai.results',
  'experiments.compare',
  'experiments.create',
  'experiments.delete',
  'experiments.get',
  'experiments.git.info',
  'experiments.list',
  'experiments.metrics.extract',
  'experiments.runs.create',
  'experiments.runs.delete',
  'experiments.runs.get',
  'experiments.runs.list',
  'experiments.runs.update',
  'experiments.scan.preview',
  'experiments.scan.start',
  'experiments.snapshots.create',
  'experiments.snapshots.get',
  'experiments.snapshots.list',
  'experiments.update',
  'export.import.inspect',
  'export.import.start',
  'diagnostics.externalRefs',
  'diagnostics.integrity',
  'diagnostics.storage',
  'export.cancel',
  'export.import.status',
  'export.project.plan',
  'export.project.start',
  'export.project.status',
  'ideas.changeStatus',
  'ideas.create',
  'ideas.delete',
  'ideas.derive',
  'ideas.evidence.confirm',
  'ideas.evidence.create',
  'ideas.evidence.delete',
  'ideas.evidence.discard',
  'ideas.evidence.extract',
  'ideas.evidence.extractPreview',
  'ideas.evidence.list',
  'ideas.evidence.update',
  'ideas.get',
  'ideas.history',
  'ideas.links.add',
  'ideas.links.remove',
  'ideas.list',
  'ideas.restore',
  'ideas.update',
  'jobs.get',
  'jobs.list',
  'kanban.checklist.add',
  'kanban.checklist.list',
  'kanban.checklist.remove',
  'kanban.checklist.reorder',
  'kanban.checklist.setDone',
  'kanban.checklist.update',
  'kanban.columns.create',
  'kanban.columns.delete',
  'kanban.columns.list',
  'kanban.columns.move',
  'kanban.columns.rename',
  'kanban.columns.setDone',
  'kanban.labels.list',
  'kanban.links.add',
  'kanban.links.remove',
  'kanban.suggestTasks',
  'kanban.tasks.archive',
  'kanban.tasks.complete',
  'kanban.tasks.create',
  'kanban.tasks.delete',
  'kanban.tasks.get',
  'kanban.tasks.list',
  'kanban.tasks.move',
  'kanban.tasks.restore',
  'kanban.tasks.unarchive',
  'kanban.tasks.update',
  'kanban.views.archive',
  'kanban.views.blocked',
  'kanban.views.board',
  'kanban.views.inbox',
  'kanban.views.list',
  'kanban.views.week',
  'literature.annotations.create',
  'literature.annotations.delete',
  'literature.annotations.list',
  'literature.annotations.update',
  'literature.import.preview',
  'literature.import.report',
  'literature.import.start',
  'literature.overview.get',
  'literature.overview.save',
  'literature.paperNote.create',
  'literature.paperNote.get',
  'literature.paperNote.saveBody',
  'literature.paperNote.saveFields',
  'literature.papers.get',
  'literature.papers.link',
  'literature.papers.list',
  'literature.papers.unlink',
  'literature.sync.apply',
  'literature.sync.check',
  'literature.sync.refreshMetadata',
  'literature.zotero.collections',
  'literature.zotero.items',
  'literature.zotero.libraries',
  'literature.zotero.status',
  'mcp.clients.create',
  'mcp.clients.delete',
  'mcp.clients.list',
  'mcp.clients.update',
  'mcp.confirmations.approve',
  'mcp.confirmations.cancel',
  'mcp.confirmations.get',
  'mcp.confirmations.list',
  'mcp.confirmations.reject',
  'mcp.jobs.cancel',
  'mcp.jobs.get',
  'mcp.jobs.list',
  'mcp.probe',
  'mcp.retention.get',
  'mcp.retention.set',
  'mcp.settings.update',
  'mcp.start',
  'mcp.status',
  'mcp.stop',
  'mcp.tools.list',
  'notes.attachments.add',
  'notes.attachments.list',
  'notes.attachments.remove',
  'notes.body.get',
  'notes.create',
  'notes.databases.create',
  'notes.databases.delete',
  'notes.databases.fields.create',
  'notes.databases.fields.delete',
  'notes.databases.fields.list',
  'notes.databases.fields.reorder',
  'notes.databases.fields.update',
  'notes.databases.get',
  'notes.databases.list',
  'notes.databases.records.create',
  'notes.databases.records.delete',
  'notes.databases.records.get',
  'notes.databases.records.list',
  'notes.databases.records.reorder',
  'notes.databases.records.update',
  'notes.databases.update',
  'notes.delete',
  'notes.detach',
  'notes.get',
  'notes.links.outbound',
  'notes.list',
  'notes.promote',
  'notes.reconcile.apply',
  'notes.reconcile.scan',
  'notes.restore',
  'notes.saveBody',
  'notes.tags.add',
  'notes.tags.list',
  'notes.tags.remove',
  'notes.updateMeta',
  'plugins.commands.list',
  'plugins.disable',
  'plugins.enable',
  'plugins.grants.set',
  'plugins.host.context.set',
  'plugins.install',
  'plugins.list',
  'plugins.market.checkUpdates',
  'plugins.market.detail',
  'plugins.market.install',
  'plugins.market.installFromGithub',
  'plugins.market.previewGithub',
  'plugins.market.refresh',
  'plugins.market.search',
  'plugins.market.settings.get',
  'plugins.market.settings.update',
  'plugins.market.update',
  'plugins.registries.add',
  'plugins.registries.list',
  'plugins.registries.remove',
  'plugins.prompt.respond',
  'plugins.rpc.call',
  'plugins.setAutoUpdate',
  'plugins.uninstall',
  'plugins.update',
  'project.activity',
  'project.archive',
  'project.context',
  'project.create',
  'project.dashboard',
  'project.delete.confirm',
  'project.delete.plan',
  'project.get',
  'project.ideas.quickAdd',
  'project.list',
  'project.notes.quickAdd',
  'project.overview',
  'project.provenance',
  'project.readiness',
  'project.resources',
  'project.session',
  'project.stats',
  'project.taskSuggestions',
  'project.tasks.quickAdd',
  'project.unarchive',
  'project.update',
  'search.index.cancel',
  'search.index.reindex',
  'search.index.status',
  'search.query',
  'settings.priority.resolve',
  'settings.project.delete',
  'settings.project.get',
  'settings.project.list',
  'settings.project.set',
  'settings.workspace.delete',
  'settings.workspace.get',
  'settings.workspace.list',
  'settings.workspace.set',
  'trash.empty.confirm',
  'trash.empty.plan',
  'trash.list',
  'trash.purge.confirm',
  'trash.purge.plan',
  'trash.restore',
  'weekly.digest.get',
  'weekly.periods.list',
  'weekly.reports.confirm',
  'weekly.reports.create',
  'weekly.reports.export',
  'weekly.reports.generate',
  'weekly.reports.get',
  'weekly.reports.list',
  'weekly.reports.revert',
  'weekly.reports.delete',
  'weekly.reports.restore',
  'weekly.reports.update',
  'weekly.reports.validate',
  'weekly.reports.revise',
  'weekly.skills.list',
  'weekly.skills.save',
  'weekly.skills.delete',
  'weekly.skills.setDefault',
  'weekly.template.get',
  'workspace.close',
  'workspace.create',
  'workspace.migrate.cancel',
  'workspace.migrate.plan',
  'workspace.migrate.progress',
  'workspace.migrate.start',
  'workspace.open',
  'workspace.projects.close',
  'workspace.projects.list',
  'workspace.projects.open',
  'workspace.scan',
  'workspace.status'
] as const

export type ListedCommandChannel = (typeof COMMAND_CHANNELS)[number]

type AssertNever<T extends never> = T

export type _NoMissingChannels = AssertNever<Exclude<CommandChannel, ListedCommandChannel>>
export type _NoExtraChannels = AssertNever<Exclude<ListedCommandChannel, CommandChannel>>