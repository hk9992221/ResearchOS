import type { EntityId, IsoDateTime, JobState, JsonObject } from './common'

export type ExportFormat = 'folder' | 'zip' | 'tar.gz'

export interface ExportRequest {
  projectId: EntityId
  format: ExportFormat
  /** Absolute OS path; defaults to `<workspace>/exports`. */
  destDir?: string
  overwrite?: boolean
}

export interface ExportFilePlan {
  relativePath: string
  bytes: number
  sha256: string | null
}

export interface ExportPlan {
  /** Handle of the cached preflight; `start` reuses a same-request plan. */
  planId?: string
  projectId: EntityId
  projectName: string
  format: ExportFormat
  /** Absolute bundle path (folder directory or archive file). */
  destination: string
  requiredBytes: number
  freeBytes: number
  files: ExportFilePlan[]
  /** Manifest `missing[]` entries: a referenced file the bundle cannot carry. */
  missing?: string[]
  warnings: string[]
}

export interface ExportArtifact {
  projectId: EntityId
  format: ExportFormat
  path: string
  bytes: number
  sha256: string
  papersIncluded: number
  /** Number of bundle entries excluding `manifest.json`. */
  fileCount?: number
  warnings: string[]
}

export interface ExportJobStatus {
  jobId: string
  state: JobState
  percent: number
  stage: string
  artifact: ExportArtifact | null
  error: string | null
}

export type ImportContainer = 'folder' | 'zip' | 'tar.gz'

export interface ImportFileCheck {
  relativePath: string
  bytes: number
  sha256: string
  matchesManifest: boolean
}

/** One integrity finding of an inspected bundle. */
export interface ImportIssue {
  relativePath: string
  /** `missing` | `size_mismatch` | `hash_mismatch` */
  reason: string
}

export interface ImportCounts {
  notes: number
  papers: number
  attachments: number
  files: number
  bytes: number
}

export interface ImportInspectPlan {
  planId: string
  container: ImportContainer
  sourcePath: string
  projectId: EntityId
  projectName: string
  schemaVersion: number
  schemaGlobal?: number
  exportedAt?: IsoDateTime | null
  expiresAt?: IsoDateTime
  fileCount: number
  totalBytes: number
  files: ImportFileCheck[]
  counts?: ImportCounts
  issues?: ImportIssue[]
  idConflict: boolean
  nameConflict: boolean
  warnings: string[]
  canImport: boolean
}

export interface ImportRequest {
  planId: string
  newIdentity?: boolean
  /** Optional display-name override; collisions get a deterministic suffix. */
  name?: string
}

export interface ImportResult {
  projectId: EntityId
  projectName: string
  mode?: 'restore_exact' | 'new_identity'
  path?: string
  restoredFiles: number
  migratedSchema: boolean
  schemaFrom?: number
  schemaTo?: number
  notes?: number
  attachments?: number
  papers?: number
  brokenRefs?: number
  warnings: string[]
}

export interface ImportMeta {
  project: JsonObject
  papers: JsonObject[]
  manifest: JsonObject
}