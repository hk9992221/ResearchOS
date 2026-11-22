import { closeSync, openSync, readFileSync, readSync, statSync } from 'node:fs'
import * as path from 'node:path'

import type { ExcludedEntry, FileKind, MetricSet, ScanFileEntry, ScanResult } from '@researchos/shared'

import { DEFAULT_MAX_TOTAL_BYTES } from './rules'

const CONTEXT_WINDOW_BYTES = 32 * 1024
const CONTEXT_SMALL_FULL = 64 * 1024
const CONTEXT_LOG_LINES = 100
const CONTEXT_CSV_HEAD_ROWS = 200
const CONTEXT_CSV_TAIL_ROWS = 20

export interface ContextEntry {
  path: string
  kind: FileKind
  bytes: number
  sha256: string
  sampled: boolean
  content: string
  priority: number
}

export interface ContextBundle {
  entries: ContextEntry[]
  metricSet: MetricSet | null
  excluded: ExcludedEntry[]
  bytes: number
  truncated: boolean
}

export interface ContextBudget {
  maxBytes?: number
}

const KIND_PRIORITY: Readonly<Record<FileKind, number>> = {
  readme: 0,
  config: 1,
  metrics: 2,
  code: 3,
  notebook: 3,
  data: 4,
  log: 5,
  image: 6,
  other: 7,
  binary: 8
}

/**
 * Builds the budgeted AI context. Every file that does not enter
 * the bundle is reported in `excluded` with its reason; sampled content carries
 * an explicit omission marker.
 */
export function buildContext(
  scan: ScanResult,
  metrics: MetricSet | null,
  budget: ContextBudget = {}
): ContextBundle {
  const maxBytes = budget.maxBytes !== undefined && budget.maxBytes > 0 ? budget.maxBytes : DEFAULT_MAX_TOTAL_BYTES
  const excluded: ExcludedEntry[] = [...scan.plan.pruned]
  const candidates: ScanFileEntry[] = []
  for (const entry of scan.plan.entries) {
    if (!entry.contentEligible) {
      excluded.push({ path: entry.path, reason: entry.exclusionReason ?? 'user_exclude' })
      continue
    }
    if (entry.kind === 'image') {
      excluded.push({ path: entry.path, reason: 'user_exclude' })
      continue
    }
    candidates.push(entry)
  }
  candidates.sort((left, right) => {
    const byPriority = KIND_PRIORITY[left.kind] - KIND_PRIORITY[right.kind]
    if (byPriority !== 0) return byPriority
    return left.path < right.path ? -1 : left.path > right.path ? 1 : 0
  })
  const entries: ContextEntry[] = []
  let bytes = 0
  let truncated = false
  for (const candidate of candidates) {
    if (bytes >= maxBytes) {
      truncated = true
      excluded.push({ path: candidate.path, reason: 'too_large' })
      continue
    }
    const absolute = path.join(scan.plan.root, ...candidate.path.split('/'))
    let sample: { content: string; sampled: boolean }
    try {
      sample = readSample(absolute, candidate)
    } catch {
      excluded.push({ path: candidate.path, reason: 'unreadable' })
      continue
    }
    let content = sample.content
    const remaining = maxBytes - bytes
    if (Buffer.byteLength(content, 'utf8') > remaining) {
      if (remaining <= 0) {
        truncated = true
        excluded.push({ path: candidate.path, reason: 'too_large' })
        continue
      }
      content = truncateUtf8(content, remaining)
      sample = { content, sampled: true }
      truncated = true
    }
    const size = Buffer.byteLength(content, 'utf8')
    entries.push({
      path: candidate.path,
      kind: candidate.kind,
      bytes: size,
      sha256: candidate.sha256 ?? '',
      sampled: sample.sampled,
      content,
      priority: KIND_PRIORITY[candidate.kind]
    })
    bytes += size
  }
  return { entries, metricSet: metrics, excluded, bytes, truncated }
}

function readSample(file: string, entry: ScanFileEntry): { content: string; sampled: boolean } {
  switch (entry.kind) {
    case 'log':
      return readLineWindow(file, CONTEXT_LOG_LINES)
    case 'data':
      return readCsvWindow(file, CONTEXT_CSV_HEAD_ROWS, CONTEXT_CSV_TAIL_ROWS)
    case 'notebook':
      return readNotebookWindow(file)
    default:
      return readByteWindow(file, CONTEXT_WINDOW_BYTES, CONTEXT_SMALL_FULL)
  }
}

function trimPartialRune(text: string, atStart: boolean): string {
  return atStart ? text.replace(/^\uFFFD+/, '') : text.replace(/\uFFFD+$/, '')
}

export function truncateUtf8(text: string, limit: number): string {
  if (limit <= 0) return ''
  let cut = Buffer.from(text, 'utf8').subarray(0, limit).toString('utf8')
  cut = cut.replace(/\uFFFD$/, '')
  return cut
}

function readByteWindow(file: string, window: number, small: number): { content: string; sampled: boolean } {
  const info = statSync(file)
  if (info.size <= small) return { content: readFileSync(file, 'utf8'), sampled: false }
  const fd = openSync(file, 'r')
  try {
    const headBuffer = Buffer.alloc(window)
    const headRead = readSync(fd, headBuffer, 0, window, 0)
    const tailBuffer = Buffer.alloc(window)
    const tailRead = readSync(fd, tailBuffer, 0, window, Math.max(0, info.size - window))
    const head = trimPartialRune(headBuffer.subarray(0, headRead).toString('utf8'), false)
    const tail = trimPartialRune(tailBuffer.subarray(0, tailRead).toString('utf8'), true)
    return { content: `${head}\n…（中间内容已省略）…\n${tail}`, sampled: true }
  } finally {
    closeSync(fd)
  }
}

function readLineWindow(file: string, count: number): { content: string; sampled: boolean } {
  const lines = readFileSync(file, 'utf8').split(/\r?\n/)
  if (lines.length <= 2 * count) return { content: lines.join('\n'), sampled: false }
  const head = lines.slice(0, count).join('\n')
  const tail = lines.slice(lines.length - count).join('\n')
  return { content: `${head}\n…（中间日志已省略）…\n${tail}`, sampled: true }
}

function readCsvWindow(file: string, headCount: number, tailCount: number): { content: string; sampled: boolean } {
  const lines = readFileSync(file, 'utf8').split(/\r?\n/)
  if (lines.length === 0) return { content: '', sampled: false }
  const header = lines[0] ?? ''
  const body = lines.slice(1)
  if (body.length <= headCount + tailCount) return { content: lines.join('\n'), sampled: false }
  const head = body.slice(0, headCount).join('\n')
  const tail = body.slice(body.length - tailCount).join('\n')
  return { content: `${header}\n${head}\n…（中间数据行已省略）…\n${tail}`, sampled: true }
}

function readNotebookWindow(file: string): { content: string; sampled: boolean } {
  const raw = readFileSync(file, 'utf8')
  const content = stripNotebookOutputs(raw)
  if (Buffer.byteLength(content, 'utf8') <= CONTEXT_SMALL_FULL) return { content, sampled: false }
  if (Buffer.byteLength(content, 'utf8') <= CONTEXT_WINDOW_BYTES * 2) return { content, sampled: true }
  const buffer = Buffer.from(content, 'utf8')
  const head = trimPartialRune(buffer.subarray(0, CONTEXT_WINDOW_BYTES).toString('utf8'), false)
  const tail = trimPartialRune(
    buffer.subarray(buffer.length - CONTEXT_WINDOW_BYTES).toString('utf8'),
    true
  )
  return { content: `${head}\n…（中间内容已省略）…\n${tail}`, sampled: true }
}

/** Removes cell outputs and execution counts; invalid JSON keeps the raw text. */
export function stripNotebookOutputs(raw: string): string {
  let document: Record<string, unknown>
  try {
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return raw
    document = parsed as Record<string, unknown>
  } catch {
    return raw
  }
  const cells = document['cells']
  if (Array.isArray(cells)) {
    for (const cell of cells) {
      if (typeof cell === 'object' && cell !== null && !Array.isArray(cell)) {
        delete (cell as Record<string, unknown>)['outputs']
        delete (cell as Record<string, unknown>)['execution_count']
      }
    }
  }
  return JSON.stringify(document, null, 2)
}