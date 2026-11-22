/**
 * Compares the note files on disk with the notes metadata and applies
 * user-chosen repairs. It never deletes files or rows: the
 * database is the metadata authority and the Markdown file is the body
 * authority.
 */
import { existsSync, lstatSync, readFileSync, readdirSync, type Dirent, type Stats } from 'node:fs'
import * as path from 'node:path'

import { type Clock, SystemClock, formatTimestamp } from './clock'
import { AppErrorCode, appError } from './errors'
import type { EventBus, EventTx } from './events'
import { isValidId, newId } from './ids'
import { hasFrontmatter, hashBytes, splitNoteFile, writeFileAtomic } from './notefile'
import { type NoteKind, NOTE_KINDS } from './schema'
import type { ProjectHandle } from './workspace/handle'

export interface ReconcileOrphan {
  relPath: string
  size: number
  modTime: Date
}

export interface ReconcileMissing {
  noteId: string
  filePath: string
  title: string
}

export interface ReconcileModified {
  noteId: string
  filePath: string
  dbHash: string
  diskHash: string
}

export interface ReconcileReport {
  projectId: string
  scanned: number
  orphans: ReconcileOrphan[]
  missing: ReconcileMissing[]
  modified: ReconcileModified[]
  generatedAt: Date
}

export type ReconcileActionKind =
  | 'adopt'
  | 'accept_external_edit'
  | 'mark_broken'
  | 'reattach'
  | 'recreate'

export interface ReconcileAction {
  kind: ReconcileActionKind
  /** `adopt`: the workspace-relative file to register. */
  relPath?: string
  /** Every action except `adopt`. */
  noteId?: string
  /** `reattach`: the recovered file. */
  newRelPath?: string
  /** `adopt`: explicit title; otherwise derived from frontmatter/heading/name. */
  title?: string
  /** `adopt`: defaults to `scratchpad`. */
  noteKind?: NoteKind
  actor: 'user' | 'mcp' | 'ai'
}

export interface ReconcileActionResult {
  action: ReconcileAction
  noteId?: string
  /** Present when this single action failed; other actions still run. */
  error?: unknown
}

export interface ReconcileApplyResult {
  items: ReconcileActionResult[]
}

interface NoteRow {
  id: string
  file_path: string
  title: string
  hash: string | null
  state: string
  deleted: boolean
}

export class ReconcileService {
  private readonly project: ProjectHandle
  private readonly events: EventBus
  private readonly clock: Clock

  constructor(project: ProjectHandle, events: EventBus, clock: Clock = new SystemClock()) {
    this.project = project
    this.events = events
    this.clock = clock
  }

  /**
   * Walks `projects/<pid>/notes/**` (no symlink following, temp files skipped)
   * and compares it with the notes table. Read-only.
   */
  scan(): ReconcileReport {
    const report: ReconcileReport = {
      projectId: this.project.id,
      scanned: 0,
      orphans: [],
      missing: [],
      modified: [],
      generatedAt: this.clock.now()
    }
    const notesDir = this.project.paths.abs(this.project.paths.notesDir(this.project.id))
    const disk = new Map<string, ReconcileOrphan>()
    const hashes = new Map<string, string>()

    if (existsSync(notesDir)) {
      for (const file of walkMarkdown(notesDir)) {
        const relPath = this.project.paths.rel(file.absolutePath)
        disk.set(relPath, {
          relPath,
          size: file.size,
          modTime: file.modTime
        })
        hashes.set(relPath, hashBytes(readFileSync(file.absolutePath)))
      }
    }
    report.scanned = disk.size

    const rows = this.project.db
      .prepare('SELECT id, file_path, title, file_hash, file_state, deleted_at FROM notes')
      .all() as Array<{
      id: string
      file_path: string
      title: string
      file_hash: string | null
      file_state: string
      deleted_at: string | null
    }>
    const rowsByPath = new Map<string, NoteRow>()
    const active: NoteRow[] = []
    for (const row of rows) {
      const decoded: NoteRow = {
        id: row.id,
        file_path: row.file_path,
        title: row.title,
        hash: row.file_hash,
        state: row.file_state,
        deleted: row.deleted_at !== null
      }
      rowsByPath.set(decoded.file_path, decoded)
      if (!decoded.deleted) active.push(decoded)
    }

    for (const [relPath, orphan] of disk) {
      if (!rowsByPath.has(relPath)) report.orphans.push(orphan)
    }
    for (const row of active) {
      if (!disk.has(row.file_path)) {
        report.missing.push({ noteId: row.id, filePath: row.file_path, title: row.title })
        continue
      }
      const diskHash = hashes.get(row.file_path) ?? ''
      if (row.state === 'missing' || row.hash === null || row.hash !== diskHash) {
        // A file that reappeared while flagged missing is surfaced as a
        // difference so an action can clear the flag.
        report.modified.push({
          noteId: row.id,
          filePath: row.file_path,
          dbHash: row.hash ?? '',
          diskHash
        })
      }
    }

    report.orphans.sort((left, right) => compare(left.relPath, right.relPath))
    report.missing.sort((left, right) => compare(left.noteId, right.noteId))
    report.modified.sort((left, right) => compare(left.noteId, right.noteId))
    return report
  }

  /**
   * Runs each action in its own transaction; a failing action never blocks the
   * rest. It never deletes a file or a row.
   */
  apply(actions: readonly ReconcileAction[]): ReconcileApplyResult {
    const result: ReconcileApplyResult = { items: [] }
    for (const action of actions) {
      try {
        const noteId = this.project.inTx(() => this.applyOne(action))
        result.items.push(noteId === undefined ? { action } : { action, noteId })
      } catch (error) {
        result.items.push({ action, error })
      }
    }
    return result
  }

  private applyOne(action: ReconcileAction): string | undefined {
    switch (action.kind) {
      case 'adopt':
        return this.adopt(action)
      case 'accept_external_edit':
        return this.requireNoteId(action, 'accept_external_edit', (noteId) =>
          this.acceptExternalEdit(action, noteId)
        )
      case 'mark_broken':
        return this.requireNoteId(action, 'mark_broken', (noteId) => this.markBroken(action, noteId))
      case 'reattach':
        return this.requireNoteId(action, 'reattach', (noteId) => this.reattach(action, noteId))
      case 'recreate':
        return this.requireNoteId(action, 'recreate', (noteId) => this.recreate(action, noteId))
      default:
        throw new Error(`reconcile: unknown action ${JSON.stringify(String(action.kind))}`)
    }
  }

  private requireNoteId(
    action: ReconcileAction,
    name: string,
    run: (noteId: string) => string
  ): string {
    if (action.noteId === undefined || !isValidId(action.noteId)) {
      throw appError(AppErrorCode.BadEntityId, `reconcile: ${name} requires a note id`)
    }
    return run(action.noteId)
  }

  private adopt(action: ReconcileAction): string {
    if (action.relPath === undefined || action.relPath === '') {
      throw appError(AppErrorCode.InvalidArgument, 'reconcile: adopt requires a relative path')
    }
    const absolutePath = this.project.paths.abs(action.relPath)
    const info = lstatSync(absolutePath)
    if (info.isSymbolicLink()) {
      throw appError(AppErrorCode.InvalidArgument, `reconcile: adopt ${action.relPath}: refusing a symlink`)
    }
    if (!info.isFile()) {
      throw appError(AppErrorCode.InvalidArgument, `reconcile: adopt ${action.relPath}: not a regular file`)
    }
    const body = readFileSync(absolutePath)
    const hash = hashBytes(body)
    const text = body.toString('utf8')
    const { meta, body: markdown } = splitNoteFile(text)
    const title = deriveTitle(action.title, meta.title, markdown, absolutePath)
    const kind = action.noteKind ?? 'scratchpad'
    if (!(NOTE_KINDS as readonly string[]).includes(kind)) {
      throw appError(AppErrorCode.InvalidArgument, `reconcile: adopt: invalid note kind ${JSON.stringify(kind)}`)
    }

    const registered = this.project.db
      .prepare('SELECT 1 AS one FROM notes WHERE file_path = ?')
      .get(action.relPath)
    if (registered !== undefined) {
      throw appError(AppErrorCode.Conflict, `reconcile: adopt ${action.relPath}: path is already registered`)
    }

    const noteId = newId(this.clock)
    const now = this.clock.now()
    const tx = this.project
    tx.db
      .prepare(
        `INSERT INTO notes(id, title, description, file_path, kind, created_at, updated_at, file_hash, file_mtime, file_state)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'ok')`
      )
      .run(
        noteId,
        title,
        meta.description ?? '',
        action.relPath,
        kind,
        formatTimestamp(now),
        formatTimestamp(now),
        hash,
        formatTimestamp(info.mtime)
      )
    this.events.emit(tx, {
      type: 'entity.created',
      entityType: 'note',
      entityId: noteId,
      summary: `创建笔记：${title}`,
      actor: action.actor,
      at: now
    })
    return noteId
  }

  private acceptExternalEdit(action: ReconcileAction, noteId: string): string {
    const filePath = this.noteFilePath(noteId)
    const absolutePath = this.project.paths.abs(filePath)
    if (!existsSync(absolutePath)) {
      throw appError(AppErrorCode.NoteFileMissing, `note ${noteId}`)
    }
    const body = readFileSync(absolutePath)
    const info = lstatSync(absolutePath)
    const hash = hashBytes(body)
    const text = body.toString('utf8')
    const { meta } = splitNoteFile(text)
    const withMeta = hasFrontmatter(text)

    const now = this.clock.now()
    const sets = [`file_state = 'ok'`, 'file_hash = ?', 'file_mtime = ?', 'updated_at = ?']
    const args: unknown[] = [hash, formatTimestamp(info.mtime), formatTimestamp(now)]
    if (withMeta) {
      sets.push('description = ?')
      args.push(meta.description ?? '')
      if (meta.title !== undefined && meta.title.trim() !== '') {
        sets.push('title = ?')
        args.push(meta.title.trim())
      }
    }
    args.push(noteId)
    const result = this.project.db
      .prepare(`UPDATE notes SET ${sets.join(', ')} WHERE id = ?`)
      .run(...args)
    if (result.changes === 0) {
      throw appError(AppErrorCode.NotFound, `reconcile: accept external edit ${noteId}: no such note`)
    }
    this.events.emitNoteUpdated(this.project, {
      noteId,
      summary: '对账采纳磁盘内容',
      actor: action.actor,
      at: now
    })
    return noteId
  }

  private markBroken(action: ReconcileAction, noteId: string): string {
    const result = this.project.db
      .prepare(`UPDATE notes SET file_state = 'missing' WHERE id = ?`)
      .run(noteId)
    if (result.changes === 0) {
      throw appError(AppErrorCode.NotFound, `reconcile: mark broken ${noteId}: no such note`)
    }
    return noteId
  }

  private reattach(action: ReconcileAction, noteId: string): string {
    if (action.newRelPath === undefined || action.newRelPath === '') {
      throw appError(AppErrorCode.InvalidArgument, 'reconcile: reattach requires a new relative path')
    }
    const absolutePath = this.project.paths.abs(action.newRelPath)
    const info = lstatSync(absolutePath)
    if (info.isSymbolicLink() || !info.isFile()) {
      throw appError(AppErrorCode.InvalidArgument, `reconcile: reattach ${action.newRelPath}: not a regular file`)
    }
    const body = readFileSync(absolutePath)
    const hash = hashBytes(body)

    this.noteFilePath(noteId) // surfaces NotFound before touching the path column
    const owner = this.project.db
      .prepare('SELECT id FROM notes WHERE file_path = ?')
      .get(action.newRelPath) as { id: string } | undefined
    if (owner !== undefined && owner.id !== noteId) {
      throw appError(
        AppErrorCode.Conflict,
        `reconcile: reattach ${action.newRelPath}: path is already used by note ${owner.id}`
      )
    }
    const now = this.clock.now()
    this.project.db
      .prepare(
        `UPDATE notes SET file_path = ?, file_state = 'ok', file_hash = ?, file_mtime = ?, updated_at = ? WHERE id = ?`
      )
      .run(action.newRelPath, hash, formatTimestamp(info.mtime), formatTimestamp(now), noteId)
    this.events.emitNoteUpdated(this.project, {
      noteId,
      summary: '重新关联文件',
      actor: action.actor,
      at: now
    })
    return noteId
  }

  private recreate(action: ReconcileAction, noteId: string): string {
    const filePath = this.noteFilePath(noteId)
    const absolutePath = this.project.paths.abs(filePath)
    const stat = writeFileAtomic(absolutePath, '')
    const now = this.clock.now()
    const result = this.project.db
      .prepare(
        `UPDATE notes SET file_state = 'ok', file_hash = ?, file_mtime = ?, updated_at = ? WHERE id = ?`
      )
      .run(stat.hash, formatTimestamp(stat.mtime), formatTimestamp(now), noteId)
    if (result.changes === 0) {
      throw appError(AppErrorCode.NotFound, `reconcile: recreate ${noteId}: no such note`)
    }
    this.events.emitNoteUpdated(this.project, {
      noteId,
      summary: '重建空正文',
      actor: action.actor,
      at: now
    })
    return noteId
  }

  private noteFilePath(noteId: string): string {
    const row = this.project.db
      .prepare('SELECT file_path FROM notes WHERE id = ?')
      .get(noteId) as { file_path: string } | undefined
    if (row === undefined) {
      throw appError(AppErrorCode.NotFound, `reconcile: read note ${noteId}: no such note`)
    }
    return row.file_path
  }
}

interface DiskFile {
  absolutePath: string
  size: number
  modTime: Date
}

/** Recursively collects `.md` files, never following symlinks or dot/temp files. */
function walkMarkdown(directory: string): DiskFile[] {
  const found: DiskFile[] = []
  let entries: Dirent[]
  try {
    entries = readdirSync(directory, { withFileTypes: true })
  } catch {
    return found
  }
  for (const entry of entries) {
    if (entry.isSymbolicLink()) continue
    const absolutePath = path.join(directory, entry.name)
    if (entry.isDirectory()) {
      found.push(...walkMarkdown(absolutePath))
      continue
    }
    const name = entry.name
    if (name.startsWith('.') || name.includes('.tmp-') || !name.endsWith('.md')) continue
    let info: Stats
    try {
      info = lstatSync(absolutePath)
    } catch {
      continue
    }
    found.push({ absolutePath, size: info.size, modTime: info.mtime })
  }
  return found
}

/**
 * The §5.11 rule: explicit title, else the frontmatter title, else the first
 * `# ` heading, else the file name without its extension.
 */
function deriveTitle(
  explicit: string | undefined,
  frontmatterTitle: string | undefined,
  body: string,
  absolutePath: string
): string {
  if (explicit !== undefined && explicit.trim() !== '') return explicit
  if (frontmatterTitle !== undefined && frontmatterTitle.trim() !== '') return frontmatterTitle
  for (const line of body.split('\n')) {
    if (line.startsWith('# ')) {
      const title = line.slice(2).trim()
      if (title !== '') return title
    }
  }
  return path.basename(absolutePath, path.extname(absolutePath))
}

function compare(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0
}