/**
 * Internal shapes of the export/import module. The public IPC/MCP surface lives
 * in `@researchos/shared` (`types/export`, `contracts/export`); these types
 * carry the parts that never leave the core process (source paths, planned
 * entries, manifest state).
 */
import type { ExportArtifact, ImportInspectPlan, ImportResult } from '@researchos/shared'

import type { Clock } from '../../clock'
import type { Db } from '../../db/open'
import type { EventBus } from '../../events'
import type { Locker } from '../../ports'
import type { ProjectHandle } from '../../workspace/handle'
import type { WorkspaceManager } from '../../workspace'

export type ExportFormat = 'folder' | 'zip' | 'tar.gz'
export type ImportFormat = ExportFormat

/** Bundle contract version of §4.5. Readers accept only `"1"`. */
export const MANIFEST_VERSION = '1'
export const APP_VERSION = '1.0.0'

/** Streaming unit for every copy and hash pass. */
export const CHUNK_SIZE = 1 << 20

/** §5.9.8 preflight lifetime and the longer inspection window of §5.3.2. */
export const EXPORT_PLAN_TTL_MS = 5 * 60 * 1000
export const IMPORT_PLAN_TTL_MS = 10 * 60 * 1000

/** Suffix range of the name-collision policy (§5.9.4). */
export const COLLISION_MAX = 100

/** Import safety bounds: finite caps against a hostile archive. */
export const MAX_BUNDLE_FILE_BYTES = 16 * 1024 * 1024 * 1024
export const MAX_BUNDLE_TOTAL_BYTES = 64 * 1024 * 1024 * 1024

export const STAGING_PREFIX = '.researchos-export-'
export const IMPORT_STAGING_PREFIX = '.import-'

export type FileKind = 'project_db' | 'note_md' | 'attachment' | 'paper_pdf' | 'meta_json' | 'manifest'

export interface PlannedFile {
  /** Absolute workspace source; empty for generated metadata entries. */
  sourceAbs: string
  /** Bundle-relative path with `/` separators. */
  bundlePath: string
  kind: FileKind
  bytes: number
}

export interface MissingRef {
  kind: 'note_md' | 'paper_pdf'
  ref: string
  recordedPath: string
}

export type WarningCode =
  | 'symlink_skipped'
  | 'changed_during_copy'
  | 'hash_mismatch'
  | 'path_rejected'
  | 'attachment_dir_missing'
  | 'event_missing'
  | 'reindex_failed'
  | 'paper_invalid'
  | 'missing_ref'

export interface Warning {
  code: WarningCode
  path: string
}

export interface PlannedPaper {
  id: string
  zoteroKey: string
  fileHash: string
  recordedPath: string
  /** Bundle path of the copy; empty when the PDF is missing. */
  bundlePath: string
  metadata: string
  bytes: number
  missing: boolean
}

export interface ExportPlanState {
  id: string
  projectId: string
  format: ExportFormat
  /** Resolved absolute destination directory. */
  destDir: string
  /** Absolute output path (does not exist yet). */
  outputPath: string
  stagingPath: string
  baseName: string
  project: ProjectMeta
  files: PlannedFile[]
  papers: PlannedPaper[]
  totalBytes: number
  noteCount: number
  paperCount: number
  attachmentCount: number
  missing: MissingRef[]
  warnings: Warning[]
  expiresAt: number
  createdAt: number
}

export interface ProjectMeta {
  id: string
  name: string
  summary: string
  goals: string
  status: 'active' | 'archived'
  createdAt: string
}

export interface ManifestFileEntry {
  path: string
  bytes: number
  sha256: string
}

export interface ManifestData {
  manifest_version: string
  app: {
    name: string
    version: string
    schema_global: number
    schema_project: number
  }
  exported_at: string
  format: ExportFormat
  project: { id: string; name: string; status: string }
  counts: { notes: number; papers: number; attachments: number; files: number; bytes: number }
  checksum: { algorithm: string }
  files: ManifestFileEntry[]
  papers: Array<{ id: string; zotero_key: string; source_pdf: string; file_hash: string; missing: boolean }>
  missing: Array<{ kind: string; ref: string; recorded_path: string }>
  warnings: Array<{ code: string; path: string }>
}

export interface PaperMetaEntry {
  id: string
  zotero_key: string
  file_hash: string
  file_size: number
  source_pdf: string
  missing: boolean
  metadata?: unknown
}

export interface ImportPlanState {
  plan: ImportInspectPlan
  manifest: ManifestData
  meta: ProjectMeta
  papers: PaperMetaEntry[]
  sourcePath: string
  format: ImportFormat
  /** Detected single top-level archive folder, empty for root bundles. */
  archivePrefix: string
  expiresAt: number
}

export interface ImportOptions {
  /** `true` allocates a fresh project id and rewrites project_id columns. */
  newIdentity?: boolean
  /** Optional display name; a taken name gets a deterministic suffix. */
  name?: string
}

export interface ExportRunContext {
  signal?: AbortSignal
  progress?: (percent: number, stage: string) => void
}

export interface ExportProgress {
  stage: string
  percent: number
}

export interface ExportServiceHooks {
  /** Test seam: runs before each planned copy. */
  beforeFile?: (stage: string, bundlePath: string) => void
}

export interface ExportServiceOptions {
  workspace: WorkspaceManager
  clock?: Clock
  locker?: Locker
  events?: EventBus
  /** Free-space probe override (bytes); returning null skips the check. */
  diskFree?: (dir: string) => { free: number; total: number } | null
  /** Post-import search rebuild; a failure becomes a warning. */
  reindex?: (projectId: string) => Promise<void> | void
  /** Export progress sink; the IPC/main layer forwards it as `job.progress`. */
  onExportProgress?: (jobId: string, progress: ExportProgress) => void
  /** Import progress sink. */
  onImportProgress?: (jobId: string, percent: number, stage: string) => void
  /** Test seam; never set in production. */
  hooks?: ExportServiceHooks
}

export interface ExportJobRecord {
  id: string
  projectId: string
  format: ExportFormat
  state: 'running' | 'succeeded' | 'failed' | 'cancelled'
  percent: number
  stage: string
  artifact: ExportArtifact | null
  error: string | null
  controller: AbortController
  done: Promise<void>
}

export interface ImportJobRecord {
  id: string
  planId: string
  projectId: string
  state: 'running' | 'succeeded' | 'failed' | 'cancelled'
  percent: number
  stage: string
  result: ImportResult | null
  error: string | null
  finishedAt: string | null
  controller: AbortController
  done: Promise<void>
}

/** The raw project row the export reads from global.db. */
export interface ProjectRow {
  id: string
  name: string
  summary: string
  goals: string
  status: string
  created_at: string
}

export interface PaperRow {
  id: string
  zotero_library_id: string
  zotero_key: string
  metadata: string
  file_path: string | null
  file_hash: string | null
  file_size: number | null
  file_state: string
}

export interface ExportContext {
  handle: ProjectHandle
  globalDb: Db
}