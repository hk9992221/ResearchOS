import { createHash } from 'node:crypto'
import { closeSync, openSync, readSync, readdirSync, realpathSync, lstatSync, statSync } from 'node:fs'
import * as path from 'node:path'

import type {
  ExcludedEntry,
  ExclusionReason,
  FileKind,
  ScanFileEntry,
  ScanPlan,
  ScanWarning,
  TargetRef
} from '@researchos/shared'

import { formatTimestamp, type Clock } from '../../clock'
import { dirUnavailable } from './errors'
import { forcedExcludedDir, normalizeRelPath, RuleSet, type EffectiveScanRules } from './rules'

const WARNING_LIMIT = 200

export interface ScanRequest {
  target: TargetRef
  root: string
  rules: EffectiveScanRules
  clock: Clock
  signal?: AbortSignal
}

export function scanDirectory(request: ScanRequest): ScanPlan {
  const { root, rules, clock, signal } = request
  let isDirectory = false
  try {
    isDirectory = statSync(root).isDirectory()
  } catch (error) {
    throw dirUnavailable(root, error instanceof Error ? error.message : String(error))
  }
  if (!isDirectory) throw dirUnavailable(root, 'not a directory')

  let realRoot: string
  try {
    realRoot = realpathSync(root)
  } catch (error) {
    throw dirUnavailable(root, error instanceof Error ? error.message : String(error))
  }

  const walker = new Scanner({ target: request.target, root, realRoot, rules, clock, signal })
  walker.run()
  return walker.finish()
}

class Scanner {
  private readonly target: TargetRef
  private readonly root: string
  private readonly realRoot: string
  private readonly rules: EffectiveScanRules
  private readonly ruleSet: RuleSet
  private readonly clock: Clock
  private readonly signal: AbortSignal | undefined
  private readonly entries: ScanFileEntry[] = []
  private readonly pruned: ExcludedEntry[] = []
  private readonly warnings: ScanWarning[] = []
  private readonly visitedDirs: Set<string> = new Set()
  private truncated = false
  private stopped = false

  constructor(options: {
    target: TargetRef
    root: string
    realRoot: string
    rules: EffectiveScanRules
    clock: Clock
    signal?: AbortSignal
  }) {
    this.target = options.target
    this.root = options.root
    this.realRoot = options.realRoot
    this.rules = options.rules
    this.ruleSet = new RuleSet(options.rules)
    this.clock = options.clock
    this.signal = options.signal
  }

  run(): void {
    this.walk(this.root, '')
  }

  private checkAbort(): void {
    if (this.signal?.aborted === true) {
      const reason = this.signal.reason
      throw reason instanceof Error ? reason : new Error('scan aborted')
    }
  }

  private walk(absoluteDir: string, relativeDir: string): void {
    this.checkAbort()
    let entries
    try {
      entries = readdirSync(absoluteDir, { withFileTypes: true })
    } catch (error) {
      this.warn('unreadable', relativeDir, error instanceof Error ? error.message : String(error))
      return
    }
    entries.sort((left, right) => (left.name < right.name ? -1 : left.name > right.name ? 1 : 0))
    for (const entry of entries) {
      if (this.stopped) return
      this.checkAbort()
      const absolute = path.join(absoluteDir, entry.name)
      const relative = normalizeRelPath(relativeDir === '' ? entry.name : `${relativeDir}/${entry.name}`)
      if (entry.isDirectory()) {
        if (forcedExcludedDir(entry.name)) {
          this.prune(`${relative}/`, 'cache')
          continue
        }
        const decision = this.ruleSet.excluded(relative, true)
        if (decision.excluded) {
          this.prune(`${relative}/`, decision.reason ?? 'user_exclude')
          continue
        }
        this.walk(absolute, relative)
        continue
      }
      if (entry.isSymbolicLink()) {
        this.symlink(absolute, relative, entry.name)
        continue
      }
      if (!entry.isFile()) {
        this.prune(relative, 'unreadable')
        continue
      }
      this.file(absolute, relative)
    }
  }

  private symlink(absolute: string, relative: string, name: string): void {
    if (this.atFileLimit()) {
      this.stopped = true
      return
    }
    let real: string
    try {
      real = realpathSync(absolute)
    } catch (error) {
      this.warn('unreadable', relative, error instanceof Error ? error.message : String(error))
      this.prune(relative, 'unreadable')
      return
    }
    const inside = withinDir(this.realRoot, real)
    if (!inside || !this.rules.followSymlinks) {
      this.addIneligible(relative, 'binary', 0, this.clock.now().getTime(), 'symlink_outside')
      return
    }
    let info
    try {
      info = statSync(real)
    } catch {
      this.prune(relative, 'unreadable')
      return
    }
    if (info.isDirectory()) {
      if (this.visitedDirs.has(real)) {
        this.prune(`${relative}/`, 'symlink_outside')
        return
      }
      this.visitedDirs.add(real)
      this.walk(real, relative)
      return
    }
    if (!info.isFile()) {
      this.prune(relative, 'unreadable')
      return
    }
    this.file(real, relative, name)
  }

  private file(absolute: string, relative: string, _name?: string): void {
    const decision = this.ruleSet.excluded(relative, false)
    if (decision.excluded) {
      this.prune(relative, decision.reason ?? 'user_exclude')
      return
    }
    if (this.atFileLimit()) {
      this.stopped = true
      return
    }
    let info
    try {
      info = statSync(absolute)
    } catch (error) {
      this.warn('unreadable', relative, error instanceof Error ? error.message : String(error))
      this.addIneligible(relative, 'other', 0, this.clock.now().getTime(), 'unreadable')
      return
    }
    const classified = classifyFile(relative, (length) => readHead(absolute, length))
    let eligible = classified.reason === null
    let exclusionReason = classified.reason
    if (classified.kind === 'image') {
      // Image understanding is not implemented in v1: images never enter AI
      // content, whatever include_images_in_ai says.
      eligible = false
      exclusionReason = 'user_exclude'
    }
    if (info.size > this.rules.maxFileBytes && eligible) {
      eligible = false
      exclusionReason = 'too_large'
    }
    let sha256: string | null = null
    let hashSkipped: 'too_large' | 'binary' | null = null
    if (info.size > this.rules.hashMaxBytes) {
      hashSkipped = 'too_large'
    } else if (classified.kind === 'binary') {
      hashSkipped = 'binary'
    } else {
      try {
        sha256 = hashFile(absolute)
      } catch (error) {
        this.warn('unreadable', relative, error instanceof Error ? error.message : String(error))
        eligible = false
        exclusionReason = 'unreadable'
      }
    }
    this.entries.push({
      path: relative,
      kind: classified.kind,
      size: info.size,
      mtime: formatTimestamp(info.mtime),
      sha256,
      hashSkipped,
      contentEligible: eligible,
      exclusionReason: eligible ? null : (exclusionReason ?? 'user_exclude'),
      sampled: false
    })
  }

  private atFileLimit(): boolean {
    if (this.entries.length < this.rules.maxFiles) return false
    if (!this.truncated) {
      this.truncated = true
      this.warn('scan_truncated', '', `file limit ${this.rules.maxFiles} reached`)
    }
    return true
  }

  private addIneligible(
    relative: string,
    kind: FileKind,
    size: number,
    mtime: number,
    reason: ExclusionReason
  ): void {
    this.entries.push({
      path: relative,
      kind,
      size,
      mtime: formatTimestamp(mtime),
      sha256: null,
      hashSkipped: null,
      contentEligible: false,
      exclusionReason: reason,
      sampled: false
    })
  }

  private prune(relative: string, reason: ExclusionReason): void {
    this.pruned.push({ path: relative, reason })
  }

  private warn(code: string, relative: string, message: string): void {
    if (this.warnings.length >= WARNING_LIMIT) return
    this.warnings.push(relative === '' ? { code, message } : { code, path: relative, message })
  }

  finish(): ScanPlan {
    this.entries.sort((left, right) => (left.path < right.path ? -1 : left.path > right.path ? 1 : 0))
    this.pruned.sort((left, right) => (left.path < right.path ? -1 : left.path > right.path ? 1 : 0))
    let totalBytes = 0
    let includedFiles = 0
    let includedBytes = 0
    for (const entry of this.entries) {
      totalBytes += entry.size
      if (entry.contentEligible) {
        includedFiles += 1
        includedBytes += Math.min(entry.size, this.rules.maxFileBytes)
      }
    }
    return {
      target: this.target,
      root: this.root,
      entries: this.entries,
      pruned: this.pruned,
      totalFiles: this.entries.length,
      totalBytes,
      includedFiles,
      includedBytes,
      contextBytes: Math.min(includedBytes, this.rules.maxTotalBytes),
      warnings: this.warnings,
      truncated: this.truncated
    }
  }
}

function withinDir(root: string, target: string): boolean {
  const relative = path.relative(root, target)
  return relative === '' || (!relative.startsWith(`..${path.sep}`) && relative !== '..' && !path.isAbsolute(relative))
}

function readHead(file: string, length: number): Buffer {
  let fd: number | undefined
  try {
    fd = openSync(file, 'r')
    const buffer = Buffer.alloc(length)
    const read = readSync(fd, buffer, 0, length, 0)
    return buffer.subarray(0, read)
  } catch {
    return Buffer.alloc(0)
  } finally {
    if (fd !== undefined) closeSync(fd)
  }
}

function hashFile(file: string): string {
  const hash = createHash('sha256')
  const fd = openSync(file, 'r')
  try {
    const chunk = Buffer.alloc(64 * 1024)
    for (;;) {
      const read = readSync(fd, chunk, 0, chunk.length, null)
      if (read <= 0) break
      hash.update(chunk.subarray(0, read))
    }
  } finally {
    closeSync(fd)
  }
  return hash.digest('hex')
}

export function hasPathSegment(relative: string, segment: string): boolean {
  const parts = relative.split('/')
  for (let index = 0; index < parts.length - 1; index += 1) {
    if ((parts[index] ?? '').toLowerCase() === segment) return true
  }
  return false
}

const MODEL_WEIGHT_EXTENSIONS: Readonly<Record<string, true>> = {
  '.pt': true,
  '.pth': true,
  '.ckpt': true,
  '.safetensors': true,
  '.onnx': true,
  '.h5': true,
  '.hdf5': true,
  '.pkl': true,
  '.joblib': true,
  '.bin': true,
  '.gguf': true
}
const ARCHIVE_MEDIA_EXTENSIONS: Readonly<Record<string, true>> = {
  '.zip': true,
  '.tar': true,
  '.gz': true,
  '.tgz': true,
  '.7z': true,
  '.rar': true,
  '.mp4': true,
  '.mov': true,
  '.avi': true
}
const BINARY_DATA_EXTENSIONS: Readonly<Record<string, true>> = {
  '.parquet': true,
  '.arrow': true,
  '.feather': true,
  '.npz': true,
  '.npy': true,
  '.hdf5': true,
  '.sqlite': true,
  '.db': true
}
const IMAGE_EXTENSIONS: Readonly<Record<string, true>> = {
  '.png': true,
  '.jpg': true,
  '.jpeg': true,
  '.webp': true,
  '.bmp': true,
  '.tiff': true,
  '.svg': true
}
const CODE_EXTENSIONS: Readonly<Record<string, true>> = {
  '.py': true,
  '.go': true,
  '.r': true,
  '.jl': true,
  '.m': true,
  '.c': true,
  '.cc': true,
  '.cpp': true,
  '.h': true,
  '.hpp': true,
  '.java': true,
  '.js': true,
  '.ts': true,
  '.tsx': true,
  '.jsx': true,
  '.sh': true,
  '.ps1': true
}
const CONFIG_EXTENSIONS: Readonly<Record<string, true>> = {
  '.yaml': true,
  '.yml': true,
  '.toml': true,
  '.ini': true,
  '.cfg': true
}
const DATA_EXTENSIONS: Readonly<Record<string, true>> = { '.csv': true, '.tsv': true }
const METRIC_JSON_NAMES: Readonly<Record<string, true>> = {
  'metrics.json': true,
  'results.json': true,
  'result.json': true,
  'scores.json': true,
  'eval.json': true,
  'stats.json': true
}

export function secretName(base: string): boolean {
  const lower = base.toLowerCase()
  return (
    lower.startsWith('.env') ||
    lower.endsWith('.pem') ||
    lower.endsWith('.key') ||
    lower.startsWith('id_rsa') ||
    lower.startsWith('credentials') ||
    lower === '.netrc'
  )
}

export interface Classification {
  kind: FileKind
  reason: ExclusionReason | null
}

/** Classifies one path and reports its default exclusion. */
export function classifyFile(relative: string, head: (length: number) => Buffer): Classification {
  const base = path.posix.basename(relative)
  const lower = base.toLowerCase()
  const extension = path.posix.extname(lower)
  const parent = path.posix.dirname(relative).toLowerCase().replace(/\.$/, '')

  if (secretName(base)) return { kind: 'other', reason: 'secret' }
  if (MODEL_WEIGHT_EXTENSIONS[extension] === true) return { kind: 'binary', reason: 'model_weight' }
  if (ARCHIVE_MEDIA_EXTENSIONS[extension] === true) return { kind: 'binary', reason: 'archive_or_media' }
  if (BINARY_DATA_EXTENSIONS[extension] === true) return { kind: 'binary', reason: 'binary' }
  if (IMAGE_EXTENSIONS[extension] === true) return { kind: 'image', reason: null }
  if (extension === '.ipynb') return { kind: 'notebook', reason: null }
  if (DATA_EXTENSIONS[extension] === true) return { kind: 'data', reason: null }
  if (extension === '.json') {
    if (
      METRIC_JSON_NAMES[lower] === true ||
      parent.includes('metric') ||
      parent.includes('result')
    ) {
      return { kind: 'metrics', reason: null }
    }
    return { kind: 'config', reason: null }
  }
  if (CONFIG_EXTENSIONS[extension] === true) return { kind: 'config', reason: null }
  if (extension === '.md' || lower.startsWith('readme')) return { kind: 'readme', reason: null }
  if (extension === '.log' || hasPathSegment(relative, 'logs')) return { kind: 'log', reason: null }
  if (CODE_EXTENSIONS[extension] === true) return { kind: 'code', reason: null }
  if (extension === '.txt') return { kind: 'other', reason: null }
  if (extension === '' && lower.includes('makefile')) return { kind: 'code', reason: null }
  if (head(8 * 1024).includes(0)) return { kind: 'binary', reason: 'binary' }
  return { kind: 'other', reason: null }
}