import type { IsoDateTime } from '../types/common'

export interface DiagnosticsDbReport {
  path: string
  schemaVersion: number
  targetVersion: number
  integrityOk: boolean
  integrityError?: string
  foreignKeyViolations: string[]
  sizeBytes: number
  walBytes: number
}

export interface DiagnosticsProjectReport {
  projectId: string
  db?: DiagnosticsDbReport
  dirExists: boolean
  notesTotal: number
  notesMissing: number
  brokenRefs: number
}

export interface DiagnosticsConfigReport {
  path: string
  exists: boolean
  corruptBackup?: string
  workspaceRoot: string
  rootReachable: boolean
  zoteroDataDir: string
}

export interface DiagnosticsIntegrityReport {
  generatedAt: IsoDateTime
  global: DiagnosticsDbReport
  projects: DiagnosticsProjectReport[]
  config: DiagnosticsConfigReport
  warnings: string[]
}

export interface DiagnosticsStorageReport {
  info: {
    root: string
    freeBytes: number
    totalBytes: number
    dbBytes: number
    libraryBytes: number
    exportsBytes: number
    projectCount: number
    archivedCount: number
  }
  footprint: {
    projects: Array<{
      id: string
      name: string
      bytes: number
      noteCount: number
      attachmentBytes: number
      archived: boolean
    }>
    libraryBytes: number
    exportsBytes: number
    totalBytes: number
  }
}

export interface ExternalRefProbe {
  id: string
  ownerType: string
  ownerId: string
  field: string
  kind: 'directory' | 'file' | 'git_repository'
  path: string
  status: 'ok' | 'broken'
  detail: string | null
}

export interface DiagnosticsCommands {
  'diagnostics.integrity': {
    req: { projectIds?: string[]; includeFileScan?: boolean }
    res: DiagnosticsIntegrityReport
  }
  'diagnostics.storage': { req: void; res: DiagnosticsStorageReport }
  'diagnostics.externalRefs': { req: { projectId: string; refIds?: string[] }; res: ExternalRefProbe[] }
}
