import type { JobRef } from '../types/common'
import type {
  MigratePlan,
  MigrateProgress,
  ProjectHandle,
  WorkspaceCreateRequest,
  WorkspaceOpenRequest,
  WorkspaceScanReport,
  WorkspaceStatus
} from '../types/workspace'

export interface WorkspaceCommands {
  'workspace.status': { req: void; res: WorkspaceStatus }

  'workspace.create': { req: WorkspaceCreateRequest; res: WorkspaceStatus }
  'workspace.open': { req: WorkspaceOpenRequest; res: WorkspaceStatus }
  'workspace.close': { req: void; res: WorkspaceStatus }

  'workspace.scan': { req: { root?: string }; res: WorkspaceScanReport }

  'workspace.migrate.plan': { req: { targetRoot: string }; res: MigratePlan }
  'workspace.migrate.start': { req: { targetRoot: string }; res: JobRef }
  'workspace.migrate.progress': { req: { jobId: string }; res: MigrateProgress }
  'workspace.migrate.cancel': { req: { jobId: string }; res: void }

  'workspace.projects.list': { req: { includeArchived?: boolean }; res: ProjectHandle[] }
  'workspace.projects.open': { req: { projectId: string }; res: ProjectHandle }
  'workspace.projects.close': { req: { projectId: string }; res: void }
}