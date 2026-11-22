import type { IsoDateTime, JobState } from './common'

export interface WorkspaceStatus {
  root: string | null
  name: string
  initialized: boolean
  markerVersion: number | null
  schemaVersion: number | null
  open: boolean
  projectCount: number
  warnings: string[]
}

export interface WorkspaceOpenRequest {
  root: string
}

export interface WorkspaceCreateRequest {
  root: string
  name?: string
}

export interface MigratePlanItem {
  kind: 'copy-db' | 'copy-notes' | 'copy-attachments' | 'copy-papers' | 'rewrite-paths' | 'verify'
  relativePath: string
  bytes: number
  action: 'create' | 'keep' | 'conflict'
}

export interface MigratePlan {
  sourceRoot: string
  targetRoot: string
  canProceed: boolean
  requiredBytes: number
  freeBytes: number
  items: MigratePlanItem[]
  warnings: string[]
}

export interface MigrateProgress {
  jobId: string
  state: JobState
  currentStep: string
  processedBytes: number
  totalBytes: number
  error: string | null
  startedAt: IsoDateTime | null
  finishedAt: IsoDateTime | null
}

export interface WorkspaceScanEntry {
  relativePath: string
  kind: 'file' | 'directory' | 'symlink'
  bytes: number
  modifiedAt: IsoDateTime | null
}

export interface WorkspaceScanReport {
  root: string
  entries: WorkspaceScanEntry[]
  totalBytes: number
  /** Free bytes on the volume holding `root`; null when it cannot be determined. */
  freeBytes: number | null
  /** global.db plus its existing -wal/-shm sidecars. */
  dbBytes: number
  /** Everything under the workspace library directory. */
  libraryBytes: number
  warnings: string[]
}

export interface ProjectHandle {
  projectId: string
  name: string
  dbPath: string
  open: boolean
}