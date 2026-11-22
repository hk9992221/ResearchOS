import { readFileSync, statSync } from 'node:fs'

import type { Clock } from '../../clock'
import { formatTimestamp, parseTimestamp } from '../../clock'
import type { WorkspaceManager } from '../../workspace'
import { chunkMarkdown, chunkPdfBlocks, hashText } from './chunk'
import { detectLang } from './query'
import { PdfNoTextLayerError } from './pdf'
import {
  type Chunk,
  type EntityType,
  experimentDocId,
  type IndexDoc,
  PARSER_VERSION_MARKDOWN,
  PARSER_VERSION_PDF,
  paperFulltextDocId,
  paperMetadataDocId,
  type PdfDocument,
  type PdfParser,
  type SourceType
} from './types'

export interface ExtractOptions {
  /** Ignores the stored file hash and parses the PDF again. */
  reparsePdf?: boolean
}

export interface ExtractProblem {
  docId: string
  error: Error
}

export interface ExtractResult {
  docs: IndexDoc[]
  problems: ExtractProblem[]
}

/**
 * Read-only projections of the eight search sources.
 *
 * These are deliberate direct SELECTs: the module services expose page-oriented
 * APIs that would make a whole-project rebuild N+1 and would hide soft-deleted
 * rows the index must still be able to mark. Note bodies and PDFs are read from
 * the filesystem, which is their authority.
 */
export class Extractor {
  private readonly workspace: WorkspaceManager
  private readonly clock: Clock
  private readonly parser: PdfParser

  constructor(workspace: WorkspaceManager, clock: Clock, parser: PdfParser) {
    this.workspace = workspace
    this.clock = clock
    this.parser = parser
  }

  async entityDocs(
    projectId: string,
    entityType: EntityType,
    entityId: string,
    options: ExtractOptions = {}
  ): Promise<ExtractResult> {
    if (entityId === '') throw new Error('search: entity id is empty')
    switch (entityType) {
      case 'note':
        return this.noteDocs(projectId, entityId)
      case 'db_record':
        return this.recordDocs(projectId, entityId)
      case 'idea':
        return this.ideaDocs(projectId, entityId)
      case 'experiment':
        return this.experimentDocs(projectId, entityId)
      case 'paper':
        return this.paperDocs(entityId, options)
      default:
        throw new Error(`search: entity type ${JSON.stringify(entityType)} is not indexed`)
    }
  }

  /** Rebuild path: every searchable document of one project. */
  async allProjectDocs(projectId: string, options: ExtractOptions = {}): Promise<ExtractResult> {
    const db = this.workspace.project(projectId).db
    const lists: Array<{ sql: string; entityType: EntityType }> = [
      {
        sql: `SELECT id FROM notes WHERE kind IN ('scratchpad','paper_note') AND deleted_at IS NULL ORDER BY id`,
        entityType: 'note'
      },
      { sql: `SELECT id FROM db_records WHERE deleted_at IS NULL ORDER BY id`, entityType: 'db_record' },
      { sql: `SELECT id FROM ideas WHERE deleted_at IS NULL ORDER BY id`, entityType: 'idea' },
      { sql: `SELECT id FROM experiments WHERE deleted_at IS NULL ORDER BY id`, entityType: 'experiment' }
    ]
    const refs: Array<{ entityType: EntityType; entityId: string }> = []
    for (const list of lists) {
      for (const row of db.prepare(list.sql).all() as Array<{ id: string }>) {
        refs.push({ entityType: list.entityType, entityId: row.id })
      }
    }
    const docs: IndexDoc[] = []
    const problems: ExtractProblem[] = []
    for (const ref of refs) {
      const result = await this.entityDocs(projectId, ref.entityType, ref.entityId, options)
      docs.push(...result.docs)
      problems.push(...result.problems)
    }
    return { docs, problems }
  }

  /** Rebuild path: every paper projection of the global library. */
  async allPaperDocs(options: ExtractOptions = {}): Promise<ExtractResult> {
    const ids = this.workspace.global.db.prepare(`SELECT id FROM papers ORDER BY id`).pluck().all() as string[]
    const docs: IndexDoc[] = []
    const problems: ExtractProblem[] = []
    for (const id of ids.map(String)) {
      const result = await this.paperDocs(id, options)
      docs.push(...result.docs)
      problems.push(...result.problems)
    }
    return { docs, problems }
  }

  private async noteDocs(projectId: string, noteId: string): Promise<ExtractResult> {
    const db = this.workspace.project(projectId).db
    const note = db
      .prepare(
        `SELECT id, kind, title, file_path, paper_id, updated_at, deleted_at
         FROM notes WHERE id = ?`
      )
      .get(noteId) as
      | {
          id: string
          kind: string
          title: string
          file_path: string
          paper_id: string | null
          updated_at: string
          deleted_at: string | null
        }
      | undefined
    if (note === undefined || note.deleted_at !== null) return { docs: [], problems: [] }
    const source: SourceType | undefined =
      note.kind === 'scratchpad' ? 'scratchpad' : note.kind === 'paper_note' ? 'paper_note' : undefined
    if (source === undefined) return { docs: [], problems: [] }
    const docId = `note:${note.id}`
    try {
      const body = this.readNoteBody(note.file_path)
      return {
        docs: [this.markdownDoc(docId, projectId, source, 'note', note.id, note.title, body, note.updated_at)],
        problems: []
      }
    } catch (error) {
      return { docs: [], problems: [{ docId, error: asError(error) }] }
    }
  }

  /**
   * A record is "field name: value" chunks located by field, plus its detail
   * note. The note is indexed inside the record document so it never appears
   * twice.
   */
  private async recordDocs(projectId: string, recordId: string): Promise<ExtractResult> {
    const db = this.workspace.project(projectId).db
    const record = db
      .prepare(
        `SELECT id, database_id, title, data, note_id, updated_at, deleted_at
         FROM db_records WHERE id = ?`
      )
      .get(recordId) as
      | {
          id: string
          database_id: string
          title: string
          data: string
          note_id: string | null
          updated_at: string
          deleted_at: string | null
        }
      | undefined
    if (record === undefined || record.deleted_at !== null) return { docs: [], problems: [] }
    const fieldNames = new Map<string, string>()
    const fields = db
      .prepare(`SELECT id, name FROM db_fields WHERE database_id = ? AND deleted_at IS NULL`)
      .all(record.database_id) as Array<{ id: string; name: string }>
    for (const field of fields) fieldNames.set(field.id, field.name)

    let values: Record<string, unknown> = {}
    if (record.data.trim() !== '') {
      try {
        values = JSON.parse(record.data) as Record<string, unknown>
      } catch (error) {
        return {
          docs: [],
          problems: [{ docId: `db_record:${record.id}`, error: new Error('record data is not JSON', { cause: error }) }]
        }
      }
    }
    const entries: Array<{ fieldId: string; name: string; value: string }> = []
    for (const [fieldId, raw] of Object.entries(values)) {
      const text = formatValue(raw)
      if (text === '') continue
      entries.push({ fieldId, name: fieldNames.get(fieldId) ?? '', value: text })
    }
    entries.sort((left, right) => {
      if (left.name !== right.name) return left.name < right.name ? -1 : 1
      return left.value < right.value ? -1 : left.value > right.value ? 1 : 0
    })

    const docId = `db_record:${record.id}`
    const chunks: Chunk[] = entries.map((entry, index) => {
      const name = entry.name === '' ? 'field' : entry.name
      const text = `${name}: ${entry.value}`
      return {
        chunkId: `${docId}#${index}`,
        docId,
        ordinal: index,
        text,
        contentHash: hashText(text),
        locator: { blockNo: index, heading: record.title, fieldId: entry.fieldId }
      }
    })

    if (record.note_id !== null && record.note_id !== '') {
      const note = db
        .prepare(`SELECT file_path, deleted_at FROM notes WHERE id = ? AND kind = 'db_record'`)
        .get(record.note_id) as { file_path: string; deleted_at: string | null } | undefined
      if (note !== undefined && note.deleted_at === null) {
        try {
          const body = this.readNoteBody(note.file_path)
          const base = chunks.length
          for (const [index, chunk] of chunkMarkdown(docId, body).entries()) {
            const ordinal = base + index
            chunks.push({
              ...chunk,
              ordinal,
              chunkId: `${docId}#${ordinal}`,
              locator: { ...chunk.locator, heading: chunk.locator.heading ?? record.title }
            })
          }
        } catch (error) {
          return { docs: [], problems: [{ docId, error: asError(error) }] }
        }
      }
    }

    return {
      docs: [
        {
          docId,
          projectId,
          source: 'db_record',
          entityType: 'db_record',
          entityId: record.id,
          title: record.title,
          lang: detectDocumentLang(record.title, chunks),
          updatedAt: this.parseTime(record.updated_at),
          contentHash: hashText(`${record.title}\u0000${joinChunkText(chunks)}`),
          parserVersion: PARSER_VERSION_MARKDOWN,
          chunks
        }
      ],
      problems: []
    }
  }

  private async ideaDocs(projectId: string, ideaId: string): Promise<ExtractResult> {
    const db = this.workspace.project(projectId).db
    const idea = db
      .prepare(
        `SELECT id, title, description, status, rejection_reason, updated_at, deleted_at
         FROM ideas WHERE id = ?`
      )
      .get(ideaId) as
      | {
          id: string
          title: string
          description: string
          status: string
          rejection_reason: string
          updated_at: string
          deleted_at: string | null
        }
      | undefined
    if (idea === undefined || idea.deleted_at !== null) return { docs: [], problems: [] }
    let body = `${idea.title}\n\n${idea.description}`
    if (idea.status !== '') body += `\n\nStatus: ${idea.status}`
    if (idea.rejection_reason !== '') body += `\n\nRejection reason: ${idea.rejection_reason}`
    return {
      docs: [
        this.markdownDoc(`idea:${idea.id}`, projectId, 'idea', 'idea', idea.id, idea.title, body, idea.updated_at)
      ],
      problems: []
    }
  }

  private async experimentDocs(projectId: string, experimentId: string): Promise<ExtractResult> {
    const db = this.workspace.project(projectId).db
    const experiment = db
      .prepare(`SELECT id, title, updated_at, deleted_at FROM experiments WHERE id = ?`)
      .get(experimentId) as
      | { id: string; title: string; updated_at: string; deleted_at: string | null }
      | undefined
    if (experiment === undefined || experiment.deleted_at !== null) return { docs: [], problems: [] }

    const docs: IndexDoc[] = []
    for (const kind of [
      { analysis: 'outline', source: 'experiment_outline' as SourceType },
      { analysis: 'results', source: 'experiment_results' as SourceType }
    ]) {
      const snapshot = db
        .prepare(
          `SELECT ai_analysis, created_at FROM snapshots
           WHERE target_type = 'experiment' AND target_id = ?
             AND CASE WHEN json_valid(ai_analysis) THEN json_extract(ai_analysis, '$.kind') END = ?
           ORDER BY created_at DESC, id DESC LIMIT 1`
        )
        .get(experimentId, kind.analysis) as { ai_analysis: string | null; created_at: string } | undefined
      if (snapshot === undefined || snapshot.ai_analysis === null) continue
      let content = ''
      try {
        const analysis = JSON.parse(snapshot.ai_analysis) as { content_md?: unknown }
        if (typeof analysis.content_md === 'string') content = analysis.content_md
      } catch {
        continue
      }
      if (content.trim() === '') continue
      const doc = this.markdownDoc(
        experimentDocId(experiment.id, kind.source),
        projectId,
        kind.source,
        'experiment',
        experiment.id,
        experiment.title,
        content,
        experiment.updated_at
      )
      const snapshotAt = this.parseTime(snapshot.created_at)
      if (snapshotAt > doc.updatedAt) doc.updatedAt = snapshotAt
      docs.push(doc)
    }
    return { docs, problems: [] }
  }

  /** Metadata projection plus PDF full text (global partition only). */
  async paperDocs(paperId: string, options: ExtractOptions): Promise<ExtractResult> {
    const db = this.workspace.global.db
    const paper = db
      .prepare(
        `SELECT id, metadata, file_path, file_hash, file_state, updated_at
         FROM papers WHERE id = ?`
      )
      .get(paperId) as
      | {
          id: string
          metadata: string
          file_path: string | null
          file_hash: string | null
          file_state: string
          updated_at: string
        }
      | undefined
    if (paper === undefined) return { docs: [], problems: [] }

    let metadata: PaperMetadata = {}
    if (paper.metadata.trim() !== '') {
      try {
        metadata = JSON.parse(paper.metadata) as PaperMetadata
      } catch (error) {
        return {
          docs: [],
          problems: [
            {
              docId: paperMetadataDocId(paper.id),
              error: new Error(`paper ${paper.id} metadata is not JSON`, { cause: error })
            }
          ]
        }
      }
    }
    const title = (metadata.title ?? '').trim() === '' ? 'Untitled paper' : metadata.title ?? ''
    const updatedAt = this.parseTime(paper.updated_at)
    const metadataChunks = paperMetadataChunks(paperMetadataDocId(paper.id), metadata)
    const docs: IndexDoc[] = [
      {
        docId: paperMetadataDocId(paper.id),
        projectId: '',
        source: 'paper_metadata',
        entityType: 'paper',
        entityId: paper.id,
        title,
        lang: detectLang(metaText(metadata)),
        updatedAt,
        contentHash: hashText(metaText(metadata)),
        parserVersion: PARSER_VERSION_MARKDOWN,
        chunks: metadataChunks
      }
    ]

    const fullDocId = paperFulltextDocId(paper.id)
    if (paper.file_path === null || paper.file_path.trim() === '') return { docs, problems: [] }

    let absolute: string
    try {
      absolute = this.workspace.paths.abs(paper.file_path)
    } catch (error) {
      return { docs, problems: [{ docId: fullDocId, error: asError(error) }] }
    }
    try {
      statSync(absolute)
    } catch (error) {
      return {
        docs,
        problems: [{ docId: fullDocId, error: new Error('PDF copy is missing', { cause: error }) }]
      }
    }

    if (!options.reparsePdf) {
      const reused = this.reusePdfDoc(db, fullDocId, paper, title, updatedAt)
      if (reused !== undefined) return { docs: [...docs, reused], problems: [] }
    }

    let parsed: PdfDocument
    try {
      parsed = await this.parser.parse(readFileSync(absolute))
    } catch (error) {
      if (error instanceof PdfNoTextLayerError) {
        this.recordParseState(db, paper.id, paper.file_hash ?? '', 0)
        return {
          docs: [
            ...docs,
            {
              docId: fullDocId,
              projectId: '',
              source: 'paper_fulltext',
              entityType: 'paper',
              entityId: paper.id,
              title,
              lang: '',
              updatedAt,
              contentHash: hashText(`no_text\u0000${paper.file_hash ?? ''}\u0000${title}`),
              parserVersion: PARSER_VERSION_PDF,
              chunks: [],
              noText: true
            }
          ],
          problems: []
        }
      }
      return { docs, problems: [{ docId: fullDocId, error: asError(error) }] }
    }

    const chunks = chunkPdfBlocks(fullDocId, parsed.blocks)
    this.recordParseState(db, paper.id, paper.file_hash ?? '', parsed.pageCount)
    return {
      docs: [
        ...docs,
        {
          docId: fullDocId,
          projectId: '',
          source: 'paper_fulltext',
          entityType: 'paper',
          entityId: paper.id,
          title,
          lang: detectLang(joinChunkText(chunks)),
          updatedAt,
          contentHash: hashText(`${paper.file_hash ?? ''}\u0000${title}\u0000${joinChunkText(chunks)}`),
          parserVersion: PARSER_VERSION_PDF,
          chunks
        }
      ],
      problems: []
    }
  }

  /** Reuses stored chunks when neither the file hash nor parser version moved. */
  private reusePdfDoc(
    db: WorkspaceManager['global']['db'],
    docId: string,
    paper: { id: string; file_hash: string | null },
    title: string,
    updatedAt: Date
  ): IndexDoc | undefined {
    const state = db
      .prepare(`SELECT file_hash, parser_version FROM paper_parse_state WHERE paper_id = ?`)
      .get(paper.id) as { file_hash: string; parser_version: string } | undefined
    if (state === undefined || state.file_hash !== (paper.file_hash ?? '') || state.parser_version !== PARSER_VERSION_PDF) {
      return undefined
    }
    const indexed = db
      .prepare(`SELECT parser_version, index_status, lang FROM paper_index_docs WHERE doc_id = ?`)
      .get(docId) as { parser_version: string; index_status: string; lang: string } | undefined
    if (
      indexed === undefined ||
      indexed.parser_version !== PARSER_VERSION_PDF ||
      indexed.index_status === 'deleted' ||
      indexed.index_status === 'failed'
    ) {
      return undefined
    }
    const base: IndexDoc = {
      docId,
      projectId: '',
      source: 'paper_fulltext',
      entityType: 'paper',
      entityId: paper.id,
      title,
      lang: indexed.lang,
      updatedAt,
      contentHash: '',
      parserVersion: PARSER_VERSION_PDF,
      chunks: []
    }
    if (indexed.index_status === 'no_text') {
      base.noText = true
      base.contentHash = hashText(`no_text\u0000${paper.file_hash ?? ''}\u0000${title}`)
      return base
    }
    const chunks = db
      .prepare(
        `SELECT chunk_id, ordinal, text, locator_json, content_hash
         FROM paper_chunks WHERE doc_id = ? ORDER BY ordinal`
      )
      .all(docId) as Array<{
      chunk_id: string
      ordinal: number
      text: string
      locator_json: string
      content_hash: string
    }>
    if (chunks.length === 0) return undefined
    base.chunks = chunks.map((row) => ({
      chunkId: row.chunk_id,
      docId,
      ordinal: row.ordinal,
      text: row.text,
      locator: parseLocatorJson(row.locator_json),
      contentHash: row.content_hash
    }))
    base.contentHash = hashText(`${paper.file_hash ?? ''}\u0000${title}\u0000${joinChunkText(base.chunks)}`)
    if (base.lang === '') base.lang = detectLang(joinChunkText(base.chunks))
    return base
  }

  private recordParseState(
    db: WorkspaceManager['global']['db'],
    paperId: string,
    fileHash: string,
    pageCount: number
  ): void {
    db.prepare(
      `INSERT INTO paper_parse_state (paper_id, file_hash, parser_version, page_count, parsed_at)
       VALUES (?, ?, ?, ?, ?)
       ON CONFLICT(paper_id) DO UPDATE SET file_hash = excluded.file_hash,
         parser_version = excluded.parser_version, page_count = excluded.page_count,
         parsed_at = excluded.parsed_at`
    ).run(paperId, fileHash, PARSER_VERSION_PDF, pageCount, formatTimestamp(this.clock.now()))
  }

  private readNoteBody(relative: string): string {
    if (relative.trim() === '') throw new Error('note file path is empty')
    const absolute = this.workspace.paths.abs(relative)
    try {
      return readFileSync(absolute, 'utf8')
    } catch (cause) {
      throw new Error(`note file is missing: ${relative}`, { cause })
    }
  }

  private markdownDoc(
    docId: string,
    projectId: string,
    source: SourceType,
    entityType: EntityType,
    entityId: string,
    title: string,
    body: string,
    updatedAt: string
  ): IndexDoc {
    const chunks = chunkMarkdown(docId, body)
    return {
      docId,
      projectId,
      source,
      entityType,
      entityId,
      title,
      lang: detectLang(`${title}\n${body}`),
      updatedAt: this.parseTime(updatedAt),
      contentHash: hashText(`${title}\u0000${body}`),
      parserVersion: PARSER_VERSION_MARKDOWN,
      chunks
    }
  }

  private parseTime(raw: string): Date {
    if (raw.trim() === '') return this.clock.now()
    try {
      return parseTimestamp(raw)
    } catch {
      return this.clock.now()
    }
  }
}

/* ------------------------------------------------------------------ paper metadata */

export interface PaperMetadata {
  title?: string
  itemType?: string
  date?: string
  publicationTitle?: string
  DOI?: string
  url?: string
  abstractNote?: string
  language?: string
  creators?: Array<{ firstName?: string; lastName?: string; name?: string; creatorType?: string }>
  tags?: Array<{ tag?: string }>
}

function metaAuthors(metadata: PaperMetadata): string {
  const names: string[] = []
  for (const creator of metadata.creators ?? []) {
    const name = `${creator.lastName ?? ''} ${creator.firstName ?? ''}`.trim() || (creator.name ?? '').trim()
    if (name !== '') names.push(name)
  }
  return names.join('; ')
}

function metaTags(metadata: PaperMetadata): string {
  return (metadata.tags ?? [])
    .map((tag) => (tag.tag ?? '').trim())
    .filter((tag) => tag !== '')
    .join(', ')
}

function metaYear(date: string | undefined): string {
  for (const field of (date ?? '').split(/[^0-9]+/)) {
    if (field.length === 4) {
      const year = Number(field)
      if (year >= 1000 && year <= 2999) return field
    }
  }
  return ''
}

/** Searchable metadata text, one field per line so a hit points at a field. */
export function metaText(metadata: PaperMetadata): string {
  const parts: string[] = []
  const add = (label: string, value: string | undefined): void => {
    if (value !== undefined && value.trim() !== '') parts.push(`${label}: ${value}`)
  }
  add('Title', metadata.title)
  add('Type', metadata.itemType)
  add('Authors', metaAuthors(metadata))
  add('Year', metaYear(metadata.date))
  add('Venue', metadata.publicationTitle)
  add('DOI', metadata.DOI)
  add('URL', metadata.url)
  add('Abstract', metadata.abstractNote)
  add('Tags', metaTags(metadata))
  add('Language', metadata.language)
  return parts.join('\n')
}

function paperMetadataChunks(docId: string, metadata: PaperMetadata): Chunk[] {
  const lines = metaText(metadata).split('\n')
  const chunks: Chunk[] = []
  for (const [index, line] of lines.entries()) {
    if (line.trim() === '') continue
    chunks.push({
      chunkId: `${docId}#${chunks.length}`,
      docId,
      ordinal: chunks.length,
      text: line,
      contentHash: hashText(line),
      locator: { blockNo: index, heading: 'Metadata' }
    })
  }
  return chunks
}

function formatValue(raw: unknown): string {
  if (raw === null || raw === undefined) return ''
  if (typeof raw === 'string') return raw.trim()
  if (typeof raw === 'boolean') return raw ? 'true' : 'false'
  if (typeof raw === 'number') return String(raw)
  if (Array.isArray(raw)) {
    return raw.map(formatValue).filter((value) => value !== '').join(', ')
  }
  if (typeof raw === 'object') {
    try {
      return JSON.stringify(raw)
    } catch {
      return ''
    }
  }
  return String(raw)
}

export function joinChunkText(chunks: readonly Chunk[]): string {
  return chunks.map((chunk) => `${chunk.text}\n`).join('')
}

function parseLocatorJson(raw: string): IndexDoc['chunks'][number]['locator'] {
  if (raw === '') return {}
  try {
    return JSON.parse(raw) as IndexDoc['chunks'][number]['locator']
  } catch {
    return {}
  }
}

function asError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error))
}

function detectDocumentLang(title: string, chunks: readonly Chunk[]): string {
  return detectLang(`${title}\n${joinChunkText(chunks)}`)
}
