import { readFileSync } from 'node:fs'
import * as path from 'node:path'

import { parse as parseYaml } from 'yaml'

import type {
  CsvSummary,
  DependencySet,
  EntryCandidate,
  ExtractedConfig,
  FileKind,
  JsonObject,
  LogSummary,
  ParseError,
  ReadmeSummary,
  ScanFileEntry,
  ScanMetadata,
  ScanResult,
  ScanWarning
} from '@researchos/shared'

const CONFIG_VALUE_LIMIT = 64 * 1024
const ENTRY_CANDIDATE = /^(main|train|run[^/]*|eval[^/]*|infer[^/]*)\.py$/i

export interface MetadataExtraction {
  metadata: ScanMetadata
  warnings: ScanWarning[]
}

/**
 * Deterministic metadata extraction. It executes no code, opens
 * no network connection and never writes the experiment directory.
 */
export async function extractMetadata(scan: ScanResult): Promise<MetadataExtraction> {
  const kindCounts: Partial<Record<FileKind, number>> = {}
  const configs: ExtractedConfig[] = []
  const csvs: CsvSummary[] = []
  const readmes: ReadmeSummary[] = []
  const dependencies: DependencySet[] = []
  const entryPoints: EntryCandidate[] = []
  const languages: Record<string, number> = {}
  const logs: LogSummary[] = []
  const warnings: ScanWarning[] = []
  let totalFiles = 0
  let totalBytes = 0

  for (const entry of scan.plan.entries) {
    totalFiles += 1
    totalBytes += entry.size
    kindCounts[entry.kind] = (kindCounts[entry.kind] ?? 0) + 1
    if (!entry.contentEligible) continue
    const absolute = path.join(scan.plan.root, ...entry.path.split('/'))
    switch (entry.kind) {
      case 'config':
      case 'metrics': {
        const config = await parseConfig(absolute, entry)
        if (config.parseError !== undefined) {
          warnings.push({
            code: 'config_parse_failed',
            path: entry.path,
            message: config.parseError.message
          })
        }
        configs.push(config)
        break
      }
      case 'data': {
        const summary = parseCsv(absolute, entry.path)
        if (summary.parseError !== undefined) {
          warnings.push({ code: 'csv_parse_failed', path: entry.path, message: summary.parseError.message })
        }
        csvs.push(summary)
        break
      }
      case 'readme': {
        try {
          readmes.push(parseReadme(absolute, entry.path))
        } catch (error) {
          warnings.push({
            code: 'readme_parse_failed',
            path: entry.path,
            message: error instanceof Error ? error.message : String(error)
          })
        }
        break
      }
      case 'log': {
        try {
          logs.push(parseLog(absolute, entry.path))
        } catch (error) {
          warnings.push({
            code: 'log_parse_failed',
            path: entry.path,
            message: error instanceof Error ? error.message : String(error)
          })
        }
        break
      }
      case 'code': {
        const extension = path.posix.extname(entry.path).toLowerCase()
        if (extension !== '') languages[extension] = (languages[extension] ?? 0) + 1
        break
      }
      default:
        break
    }
    const dependencySet = await parseDependencies(absolute, entry.path)
    if (dependencySet !== undefined) dependencies.push(dependencySet)
    if (entry.kind === 'code' && ENTRY_CANDIDATE.test(path.posix.basename(entry.path).toLowerCase())) {
      entryPoints.push({ path: entry.path, heuristic: true })
    }
  }

  const byPath = <T extends { path: string }>(items: T[]): T[] =>
    items.sort((left, right) => (left.path < right.path ? -1 : left.path > right.path ? 1 : 0))

  return {
    metadata: {
      totalFiles,
      totalBytes,
      kindCounts,
      configs: byPath(configs),
      csvs: byPath(csvs),
      readmes: byPath(readmes),
      dependencies: byPath(dependencies),
      entryPoints: byPath(entryPoints),
      languages,
      logs: byPath(logs),
      warnings
    },
    warnings
  }
}

function readText(file: string): string {
  return readFileSync(file, 'utf8')
}

async function parseConfig(file: string, entry: ScanFileEntry): Promise<ExtractedConfig> {
  const out: ExtractedConfig = { path: entry.path, sha256: entry.sha256 ?? '', values: {} }
  let raw: string
  try {
    raw = readText(file)
  } catch (error) {
    out.parseError = { message: error instanceof Error ? error.message : String(error) }
    return out
  }
  const extension = path.posix.extname(entry.path).toLowerCase()
  let parsed: unknown
  try {
    if (extension === '.json') {
      parsed = JSON.parse(raw)
    } else if (extension === '.yaml' || extension === '.yml') {
      parsed = parseYaml(raw)
    } else if (extension === '.toml') {
      parsed = await parseToml(raw)
    } else {
      return out
    }
  } catch (error) {
    out.parseError = parseErrorMessage(error)
    return out
  }
  let values: JsonObject
  if (isPlainObject(parsed)) {
    values = parsed
  } else {
    values = { value: parsed ?? null }
  }
  const truncated = truncateConfig(values)
  out.values = truncated.values
  if (truncated.truncated) out.truncated = true
  return out
}

function parseErrorMessage(error: unknown): ParseError {
  const message = error instanceof Error ? error.message : String(error)
  if (typeof error === 'object' && error !== null && 'line' in error && 'column' in error) {
    const located = error as { line?: unknown; column?: unknown }
    if (typeof located.line === 'number' && typeof located.column === 'number') {
      return { line: located.line, column: located.column, message }
    }
  }
  const match = /at line (\d+), column (\d+)/i.exec(message)
  if (match !== null) {
    return {
      line: Number.parseInt(match[1] ?? '0', 10),
      column: Number.parseInt(match[2] ?? '0', 10),
      message
    }
  }
  return { message }
}

function isPlainObject(value: unknown): value is JsonObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/** Keeps top-level keys in sorted order until the 64 KiB budget is exhausted. */
function truncateConfig(values: JsonObject): { values: JsonObject; truncated: boolean } {
  const encoded = JSON.stringify(values)
  if (encoded === undefined || Buffer.byteLength(encoded, 'utf8') <= CONFIG_VALUE_LIMIT) {
    return { values, truncated: false }
  }
  const keys = Object.keys(values).sort()
  const out: JsonObject = {}
  let size = 0
  for (const key of keys) {
    const part = JSON.stringify({ [key]: values[key] })
    if (size + Buffer.byteLength(part, 'utf8') > CONFIG_VALUE_LIMIT) break
    out[key] = values[key]
    size += Buffer.byteLength(part, 'utf8')
  }
  return { values: out, truncated: true }
}

/** Splits CSV text honoring quotes and escaped quotes. */
export function parseCsvRecords(text: string, delimiter: string): string[][] {
  const records: string[][] = []
  let record: string[] = []
  let field = ''
  let quoted = false
  let index = 0
  const pushField = (): void => {
    record.push(field)
    field = ''
  }
  const pushRecord = (): void => {
    pushField()
    records.push(record)
    record = []
  }
  while (index < text.length) {
    const char = text[index]
    if (char === undefined) break
    if (quoted) {
      if (char === '"') {
        if (text[index + 1] === '"') {
          field += '"'
          index += 2
          continue
        }
        quoted = false
        index += 1
        continue
      }
      field += char
      index += 1
      continue
    }
    if (char === '"') {
      quoted = true
      index += 1
      continue
    }
    if (char === delimiter) {
      pushField()
      index += 1
      continue
    }
    if (char === '\r') {
      if (text[index + 1] === '\n') index += 1
      pushRecord()
      index += 1
      continue
    }
    if (char === '\n') {
      pushRecord()
      index += 1
      continue
    }
    field += char
    index += 1
  }
  if (quoted) throw new Error('unterminated quoted CSV field')
  if (field !== '' || record.length > 0) pushRecord()
  return records
}

function parseCsv(file: string, relative: string): CsvSummary {
  const out: CsvSummary = {
    path: relative,
    delimiter: path.posix.extname(relative).toLowerCase() === '.tsv' ? '\t' : ',',
    header: [],
    columns: 0,
    rows: 0
  }
  let text: string
  try {
    text = readText(file)
  } catch (error) {
    out.parseError = { message: error instanceof Error ? error.message : String(error) }
    return out
  }
  try {
    const records = parseCsvRecords(text, out.delimiter)
    const header = records[0]
    if (header === undefined) {
      out.parseError = { message: 'empty CSV' }
      return out
    }
    out.header = header
    out.columns = header.length
    out.rows = Math.max(0, records.length - 1)
  } catch (error) {
    out.parseError = { message: error instanceof Error ? error.message : String(error) }
  }
  return out
}

function parseReadme(file: string, relative: string): ReadmeSummary {
  const out: ReadmeSummary = { path: relative, title: '', headings: [] }
  for (const line of readText(file).split(/\r?\n/)) {
    const trimmed = line.trim()
    if (!trimmed.startsWith('#')) continue
    const title = trimmed.replace(/^#+/, '').trim()
    if (title === '') continue
    out.headings.push(title)
    if (out.title === '' && trimmed.startsWith('# ')) out.title = title
  }
  if (out.title === '') {
    const base = path.posix.basename(relative)
    out.title = base.slice(0, Math.max(0, base.length - path.posix.extname(base).length))
  }
  return out
}

function parseLog(file: string, relative: string): LogSummary {
  const out: LogSummary = { path: relative, errorCount: 0, warnCount: 0, lines: 0 }
  const content = readText(file)
  const lines = content.endsWith('\n') ? content.slice(0, -1).split(/\r?\n/) : content.split(/\r?\n/)
  for (const line of lines) {
    out.lines += 1
    const lower = line.toLowerCase()
    if (lower.includes('error')) out.errorCount += 1
    if (lower.includes('warn')) out.warnCount += 1
  }
  return out
}

async function parseToml(raw: string): Promise<unknown> {
  // smol-toml is ESM-only; the core package is CJS, so the import stays lazy.
  const module = await import('smol-toml')
  return module.parse(raw)
}

async function parseDependencies(file: string, relative: string): Promise<DependencySet | undefined> {
  const base = path.posix.basename(relative).toLowerCase()
  if (base.startsWith('requirements') && base.endsWith('.txt')) {
    const dependencies: string[] = []
    for (const rawLine of readText(file).split(/\r?\n/)) {
      let line = rawLine.trim()
      if (line === '' || line.startsWith('#')) continue
      const comment = line.indexOf(' #')
      if (comment > 0) line = line.slice(0, comment).trim()
      dependencies.push(line)
    }
    return { path: relative, kind: 'requirements', dependencies }
  }
  if (base === 'pyproject.toml') {
    let parsed: unknown
    try {
      parsed = await parseToml(readText(file))
    } catch {
      return undefined
    }
    if (!isPlainObject(parsed)) return undefined
    const dependencies: string[] = []
    const project = parsed['project']
    if (isPlainObject(project) && Array.isArray(project['dependencies'])) {
      for (const item of project['dependencies']) {
        if (typeof item === 'string') dependencies.push(item)
      }
    }
    const tool = parsed['tool']
    if (isPlainObject(tool)) {
      const poetry = tool['poetry']
      if (isPlainObject(poetry) && isPlainObject(poetry['dependencies'])) {
        dependencies.push(...Object.keys(poetry['dependencies']).sort())
      }
    }
    return { path: relative, kind: 'pyproject', dependencies }
  }
  if (base === 'environment.yml' || base === 'environment.yaml') {
    let parsed: unknown
    try {
      parsed = parseYaml(readText(file))
    } catch {
      return undefined
    }
    if (!isPlainObject(parsed) || !Array.isArray(parsed['dependencies'])) return undefined
    const dependencies: string[] = []
    for (const item of parsed['dependencies']) {
      if (typeof item === 'string' && item.trim() !== '') dependencies.push(item.trim())
    }
    return { path: relative, kind: 'environment', dependencies }
  }
  return undefined
}