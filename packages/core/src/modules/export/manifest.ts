/**
 * Bundle JSON documents (§4.5). Field order, names and units are the v1
 * contract: older Go-created bundles are read with the same shapes and the
 * manifest only ever grows new fields.
 */
import { APP_VERSION, MANIFEST_VERSION } from './types'
import type {
  ExportFormat,
  ManifestData,
  ManifestFileEntry,
  MissingRef,
  PlannedPaper,
  ProjectMeta,
  Warning
} from './types'

/** RFC3339 in local time, matching Go's `time.RFC3339` without sub-seconds. */
export function rfc3339Local(date: Date): string {
  const pad = (value: number): string => String(value).padStart(2, '0')
  const offset = -date.getTimezoneOffset()
  const sign = offset >= 0 ? '+' : '-'
  const absolute = Math.abs(offset)
  return (
    `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}` +
    `T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}` +
    `${sign}${pad(Math.trunc(absolute / 60))}:${pad(absolute % 60)}`
  )
}

interface ManifestInput {
  format: ExportFormat
  project: ProjectMeta
  exportedAt: Date
  schemaGlobal: number
  schemaProject: number
  files: readonly ManifestFileEntry[]
  papers: readonly PlannedPaper[]
  missing: readonly MissingRef[]
  warnings: readonly Warning[]
}

export function buildManifest(input: ManifestInput): ManifestData {
  const files = [...input.files].sort((left, right) => (left.path < right.path ? -1 : left.path > right.path ? 1 : 0))
  let bytes = 0
  let notes = 0
  let papers = 0
  let attachments = 0
  for (const file of files) {
    bytes += file.bytes
    if (file.path.includes('/notes/') && file.path.endsWith('.md')) notes += 1
    if (file.path.includes('/attachments/')) attachments += 1
    if (file.path.startsWith('workspace/library/papers/') && file.path.endsWith('/source.pdf')) papers += 1
  }
  return {
    manifest_version: MANIFEST_VERSION,
    app: {
      name: 'ResearchOS',
      version: APP_VERSION,
      schema_global: input.schemaGlobal,
      schema_project: input.schemaProject
    },
    exported_at: rfc3339Local(input.exportedAt),
    format: input.format,
    project: { id: input.project.id, name: input.project.name, status: input.project.status },
    counts: { notes, papers, attachments, files: files.length, bytes },
    checksum: { algorithm: 'sha256' },
    files,
    papers: input.papers.map((paper) => ({
      id: paper.id,
      zotero_key: paper.zoteroKey,
      source_pdf: paper.bundlePath,
      file_hash: paper.fileHash,
      missing: paper.missing
    })),
    missing: [...input.missing]
      .sort((left, right) =>
        left.kind === right.kind
          ? left.ref === right.ref
            ? left.recordedPath.localeCompare(right.recordedPath)
            : left.ref.localeCompare(right.ref)
          : left.kind.localeCompare(right.kind)
      )
      .map((ref) => ({ kind: ref.kind, ref: ref.ref, recorded_path: ref.recordedPath })),
    warnings: [...input.warnings]
      .sort((left, right) =>
        left.code === right.code ? left.path.localeCompare(right.path) : left.code.localeCompare(right.code)
      )
      .map((warning) => ({ code: warning.code, path: warning.path }))
  }
}

export function serializeManifest(manifest: ManifestData): Buffer {
  return Buffer.from(JSON.stringify(manifest, null, 2), 'utf8')
}

export function metaProjectJson(project: ProjectMeta): Buffer {
  const meta = {
    id: project.id,
    name: project.name,
    summary: project.summary,
    goals: project.goals,
    status: project.status,
    created_at: project.createdAt
  }
  return Buffer.from(JSON.stringify(meta, null, 2), 'utf8')
}

export function metaPapersJson(papers: readonly PlannedPaper[]): Buffer {
  const entries = papers.map((paper) => {
    const entry: Record<string, unknown> = {
      id: paper.id,
      zotero_key: paper.zoteroKey,
      file_hash: paper.fileHash,
      file_size: paper.bytes,
      source_pdf: paper.bundlePath,
      missing: paper.missing
    }
    if (paper.metadata !== '') {
      try {
        entry['metadata'] = JSON.parse(paper.metadata)
      } catch {
        entry['metadata'] = {}
      }
    }
    return entry
  })
  return Buffer.from(JSON.stringify(entries, null, 2), 'utf8')
}