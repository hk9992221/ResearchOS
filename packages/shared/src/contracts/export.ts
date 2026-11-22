import type { IsoDateTime, JobRef, JobState } from '../types/common'
import type {
  ExportFormat,
  ExportJobStatus,
  ExportPlan,
  ImportContainer,
  ImportInspectPlan,
  ImportResult
} from '../types/export'

export interface ImportJobStatus {
  jobId: string
  state: JobState
  percent: number
  stage: string
  result: ImportResult | null
  error: string | null
  finishedAt: IsoDateTime | null
}

export interface ExportCommands {
  'export.project.plan': {
    req: { projectId: string; format: ExportFormat; destDir?: string }
    res: ExportPlan
  }
  'export.project.start': {
    req: { projectId: string; format: ExportFormat; destDir?: string; overwrite?: boolean }
    res: JobRef
  }
  'export.project.status': { req: { jobId: string }; res: ExportJobStatus }
  'export.cancel': { req: { jobId: string }; res: { cancelled: boolean } }

  'export.import.inspect': { req: { sourcePath: string }; res: ImportInspectPlan }
  'export.import.start': { req: { planId: string; newIdentity?: boolean; name?: string }; res: JobRef }
  'export.import.status': { req: { jobId: string }; res: ImportJobStatus }
}

/** Convenience union used by dialogs that accept folder or archive bundles. */
export type ExportContainerName = ImportContainer