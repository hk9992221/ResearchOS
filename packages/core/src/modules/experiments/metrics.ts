import { readFileSync } from 'node:fs'
import * as path from 'node:path'

import type { JsonObject, Metric, MetricSet, ScanFileEntry, ScanResult, UnparsedFile } from '@researchos/shared'

import { parseCsvRecords } from './metadata'

export const EXTRACTOR_JSON_LEAVES = 'json_numeric_leaves.v1'
export const EXTRACTOR_CSV_SCALARS = 'csv_scalar_columns.v1'

export interface MetricFileAccessor {
  read(relative: string): Buffer
}

export interface MetricExtractor {
  readonly name: string
  supports(entry: ScanFileEntry): boolean
  extract(accessor: MetricFileAccessor, entry: ScanFileEntry): Metric[]
}

/**
 * Extractor registry. The two baseline extractors are
 * registered first; plugin rules append through `register`.
 */
export class MetricRegistry {
  private readonly list: MetricExtractor[] = []

  /**
   * Registers one extractor (plugin rule hook) and returns the disposer that
   * removes exactly it; the plugin host calls the disposer on disable/uninstall.
   */
  register(extractor: MetricExtractor): () => void {
    if (this.list.some((item) => item.name === extractor.name)) {
      throw new Error(`metric extractor already registered: ${extractor.name}`)
    }
    this.list.push(extractor)
    return () => {
      const index = this.list.indexOf(extractor)
      if (index >= 0) this.list.splice(index, 1)
    }
  }

  get extractors(): readonly MetricExtractor[] {
    return this.list
  }
}

export function createMetricRegistry(): MetricRegistry {
  const registry = new MetricRegistry()
  registry.register(jsonLeafExtractor)
  registry.register(csvScalarExtractor)
  return registry
}

export function extractMetrics(scan: ScanResult, registry: MetricRegistry): MetricSet {
  const metrics: Metric[] = []
  const unparsed: UnparsedFile[] = []
  const accessor: MetricFileAccessor = {
    read: (relative) => readFileSync(path.join(scan.plan.root, ...relative.split('/')))
  }
  for (const entry of scan.plan.entries) {
    if (!entry.contentEligible || entry.sha256 === null) continue
    let matched = false
    for (const extractor of registry.extractors) {
      if (!extractor.supports(entry)) continue
      matched = true
      try {
        metrics.push(...extractor.extract(accessor, entry))
      } catch (error) {
        unparsed.push({
          path: entry.path,
          reason: `${extractor.name}: ${error instanceof Error ? error.message : String(error)}`
        })
      }
    }
    if (!matched && (entry.kind === 'metrics' || entry.kind === 'data')) {
      unparsed.push({ path: entry.path, reason: 'no_rule_match' })
    }
  }
  metrics.sort((left, right) => {
    if (left.source.path !== right.source.path) return left.source.path < right.source.path ? -1 : 1
    return left.key < right.key ? -1 : left.key > right.key ? 1 : 0
  })
  return { v: 1, metrics, unparsed }
}

const jsonLeafExtractor: MetricExtractor = {
  name: EXTRACTOR_JSON_LEAVES,
  supports(entry) {
    return (entry.kind === 'metrics' || entry.kind === 'config') && entry.path.toLowerCase().endsWith('.json')
  },
  extract(accessor, entry) {
    const parsed: unknown = JSON.parse(accessor.read(entry.path).toString('utf8'))
    const metrics: Metric[] = []
    walkJsonLeaves(parsed, '', (pointer, value) => {
      if (typeof value !== 'number' || !Number.isFinite(value)) return
      const key = pointer.replace(/^\//, '') || '(root)'
      metrics.push({
        key,
        label: metricLabel(pointer),
        value,
        raw: String(value),
        unit: null,
        source: { path: entry.path, pointer, line: null, sha256: entry.sha256 },
        extractor: EXTRACTOR_JSON_LEAVES
      })
    })
    metrics.sort((left, right) => (left.key < right.key ? -1 : left.key > right.key ? 1 : 0))
    return metrics
  }
}

const csvScalarExtractor: MetricExtractor = {
  name: EXTRACTOR_CSV_SCALARS,
  supports(entry) {
    if (entry.kind !== 'data') return false
    const extension = path.posix.extname(entry.path).toLowerCase()
    return extension === '.csv' || extension === '.tsv'
  },
  extract(accessor, entry) {
    const delimiter = path.posix.extname(entry.path).toLowerCase() === '.tsv' ? '\t' : ','
    const records = parseCsvRecords(accessor.read(entry.path).toString('utf8'), delimiter)
    const header = records[0]
    if (header === undefined) throw new Error('empty header')
    if (header.length === 0) throw new Error('empty header')
    const lastIndex = records.length - 1
    const last = records[lastIndex]
    if (last === undefined || lastIndex === 0) return []
    const line = lastIndex + 1
    const metrics: Metric[] = []
    for (let index = 0; index < header.length; index += 1) {
      const cell = last[index]
      if (cell === undefined) continue
      const text = cell.trim()
      if (text === '') continue
      const value = Number(text)
      if (!Number.isFinite(value)) continue
      const key = (header[index] ?? '').trim() || `column_${index + 1}`
      metrics.push({
        key,
        label: key,
        value,
        raw: text,
        unit: null,
        source: { path: entry.path, pointer: null, line, sha256: entry.sha256 },
        extractor: EXTRACTOR_CSV_SCALARS
      })
    }
    return metrics
  }
}

/** Visits scalar leaves in sorted key order (RFC 6901 pointers). */
export function walkJsonLeaves(value: unknown, pointer: string, visit: (pointer: string, value: unknown) => void): void {
  if (Array.isArray(value)) {
    value.forEach((item, index) => walkJsonLeaves(item, `${pointer}/${index}`, visit))
    return
  }
  if (typeof value === 'object' && value !== null) {
    const object = value as JsonObject
    for (const key of Object.keys(object).sort()) {
      const escaped = key.replaceAll('~', '~0').replaceAll('/', '~1')
      walkJsonLeaves(object[key], `${pointer}/${escaped}`, visit)
    }
    return
  }
  visit(pointer, value)
}

function metricLabel(pointer: string): string {
  if (pointer === '') return 'value'
  const segments = pointer.split('/')
  for (let index = segments.length - 1; index >= 0; index -= 1) {
    const segment = segments[index]
    if (segment !== undefined && segment !== '') {
      return segment.replaceAll('~1', '/').replaceAll('~0', '~')
    }
  }
  return 'value'
}

/** Canonical alignment key: metric key plus its deterministic context. */
export function alignmentKey(metric: Metric): string {
  return `${metric.key}\u0000${JSON.stringify(metric.context ?? {})}`
}

export function percentChange(base: number, value: number): number | undefined {
  if (base === 0) return undefined
  return ((value - base) / Math.abs(base)) * 100
}