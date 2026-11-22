import * as path from 'node:path'

import type {
  ComparisonMatrix,
  ComparisonRun,
  ComparisonTableOptions,
  MetricComparisonRow,
  MetricDirection,
  MetricSet,
  ParamDiffRow,
  RunParams,
  RunStatus,
  TableFormat
} from '@researchos/shared'

import { invalidInput } from './errors'
import { alignmentKey, percentChange } from './metrics'

export interface MatrixRunInput {
  id: string
  title: string
  dirRef: string | null
  params: RunParams
  gitShortSha: string
  dirty: boolean
  status: RunStatus
  metrics: MetricSet | null
}

export interface MatrixOptions {
  experimentId?: string
  baselineId?: string
  diffParams?: boolean
  directions?: Record<string, MetricDirection>
}

const LOWER_BETTER_KEYWORDS = [
  'loss',
  'err',
  'mae',
  'mse',
  'rmse',
  'mape',
  'nll',
  'ppl',
  'perplexity',
  'fid',
  'latency',
  'time',
  'cost',
  'params',
  'flops'
] as const

export function inferMetricDirection(key: string): MetricDirection {
  const lower = key.toLowerCase()
  return LOWER_BETTER_KEYWORDS.some((keyword) => lower.includes(keyword)) ? 'lower' : 'higher'
}

/** UI display name: title, directory basename, `name` param, else short id. */
export function runDisplayName(run: {
  id: string
  title: string
  dirRef: string | null
  params: RunParams
}): string {
  if (run.title.trim() !== '') return run.title.trim()
  if (run.dirRef !== null) {
    const base = path.basename(run.dirRef.replaceAll('\\', '/')).replace(/\/+$/, '')
    if (base !== '' && base !== '.' && base !== '/') return base
  }
  const name = run.params['name']?.value.trim()
  if (name !== undefined && name !== '') return name
  return `Run ${run.id.slice(0, 8)}`
}

function matrixRunLabel(run: MatrixRunInput): string {
  if (run.title.trim() !== '') return run.title.trim()
  if (run.dirRef !== null) {
    const base = run.dirRef.replaceAll('\\', '/').replace(/\/+$/, '').split('/').pop() ?? ''
    if (base !== '' && base !== '.') return base
  }
  const name = run.params['name']?.value.trim()
  if (name !== undefined && name !== '') return name
  if (run.gitShortSha !== '') return `run@${run.gitShortSha}`
  return run.id.slice(0, 8)
}

/** Builds the deterministic cross-run matrix (rewrite plan §4.6). */
export function buildComparisonMatrix(runs: MatrixRunInput[], options: MatrixOptions = {}): ComparisonMatrix {
  const baselineId =
    options.baselineId !== undefined && options.baselineId !== ''
      ? options.baselineId
      : (runs[0]?.id ?? null)
  if (baselineId !== null && !runs.some((run) => run.id === baselineId)) {
    throw invalidInput(`基准 Run 不在对比集合中：${baselineId}`, { reason: 'baseline_missing' })
  }
  const columns: ComparisonRun[] = runs.map((run) => ({
    runId: run.id,
    label: matrixRunLabel(run),
    gitShortSha: run.gitShortSha,
    dirty: run.dirty,
    status: run.status
  }))
  const paramRows = buildParamRows(runs, options.diffParams === true)
  const metricRows = buildMetricRows(runs, baselineId, options.directions ?? {})
  return {
    experimentId: options.experimentId === undefined || options.experimentId === '' ? null : options.experimentId,
    baselineId,
    runs: columns,
    paramRows,
    metricRows
  }
}

function buildParamRows(runs: MatrixRunInput[], diffOnly: boolean): ParamDiffRow[] {
  const keys = new Set<string>()
  for (const run of runs) {
    for (const key of Object.keys(run.params)) keys.add(key)
  }
  const rows: ParamDiffRow[] = []
  for (const key of [...keys].sort()) {
    const values: Record<string, string> = {}
    const distinct = new Set<string>()
    for (const run of runs) {
      const value = run.params[key]?.value ?? ''
      values[run.id] = value
      distinct.add(value)
    }
    const isDiff = distinct.size > 1
    if (diffOnly && !isDiff) continue
    rows.push({ key, values, isDiff })
  }
  return rows
}

interface AlignedMetrics {
  key: string
  context: MetricComparisonRow['context']
  values: Record<string, number | null>
}

function buildMetricRows(
  runs: MatrixRunInput[],
  baselineId: string | null,
  overrides: Record<string, MetricDirection>
): MetricComparisonRow[] {
  const aligned = new Map<string, AlignedMetrics>()
  for (const run of runs) {
    for (const metric of run.metrics?.metrics ?? []) {
      const key = alignmentKey(metric)
      const row = aligned.get(key) ?? { key: metric.key, context: metric.context, values: {} }
      row.values[run.id] = metric.value
      aligned.set(key, row)
    }
  }
  const ordered = [...aligned.values()].sort((left, right) => {
    if (left.key !== right.key) return left.key < right.key ? -1 : 1
    return alignmentOrder(left.context) < alignmentOrder(right.context) ? -1 : 1
  })
  return ordered.map((row) => {
    const direction = overrides[row.key] ?? inferMetricDirection(row.key)
    const delta: Record<string, number> = {}
    const pct: Record<string, number> = {}
    const base = baselineId === null ? null : (row.values[baselineId] ?? null)
    if (base !== null) {
      for (const [runId, value] of Object.entries(row.values)) {
        if (runId === baselineId || value === null) continue
        delta[runId] = value - base
        const change = percentChange(base, value)
        if (change !== undefined) pct[runId] = change
      }
    }
    const extremes = bestAndSecond(runs, row.values, direction)
    return {
      key: row.key,
      ...(row.context === undefined ? {} : { context: row.context }),
      values: row.values,
      delta,
      pct,
      bestRunId: extremes.best,
      secondBestRunId: extremes.second,
      direction
    }
  })
}

function alignmentOrder(context: MetricComparisonRow['context']): string {
  if (context === undefined) return ''
  return Object.keys(context)
    .sort()
    .map((key) => `${key}=${String(context[key])}`)
    .join(',')
}

function bestAndSecond(
  runs: MatrixRunInput[],
  values: Record<string, number | null>,
  direction: MetricDirection
): { best: string | null; second: string | null } {
  let best: string | null = null
  let second: string | null = null
  let bestValue = 0
  let secondValue = 0
  const better = (candidate: number, current: number): boolean =>
    direction === 'lower' ? candidate < current : candidate > current
  for (const run of runs) {
    const value = values[run.id]
    if (value === undefined || value === null) continue
    if (best === null) {
      best = run.id
      bestValue = value
      continue
    }
    if (better(value, bestValue)) {
      second = best
      secondValue = bestValue
      best = run.id
      bestValue = value
      continue
    }
    if (second === null) {
      second = run.id
      secondValue = value
      continue
    }
    if (better(value, secondValue)) {
      second = run.id
      secondValue = value
    }
  }
  return { best, second }
}

export function resolveTableFormat(format: string | undefined): TableFormat {
  if (format === undefined || format === '') return 'latex_booktabs'
  if (format === 'latex_booktabs' || format === 'markdown' || format === 'csv') return format
  throw invalidInput(`未知的表格格式：${format}`, { reason: 'unknown_table_format', format })
}

/** Renders the matrix as a paper-ready table. */
export function formatComparisonTable(matrix: ComparisonMatrix, options: ComparisonTableOptions = {}): string {
  const format = resolveTableFormat(options.format)
  const precision =
    options.precision === undefined || options.precision <= 0 ? 4 : Math.min(10, options.precision)
  const params = selectRows(matrix.paramRows, options.includeParams)
  const metrics = selectMetricRows(matrix.metricRows, options.includeMetrics)
  const highlight = options.highlightBest !== false
  const showDelta = options.showDelta === true
  if (format === 'markdown') {
    return formatMarkdown(matrix, params, metrics, precision, highlight, showDelta, options.caption)
  }
  if (format === 'csv') return formatCsv(matrix, params, metrics, precision, showDelta)
  return formatLatex(matrix, params, metrics, precision, highlight, showDelta, options)
}

function selectRows(rows: ParamDiffRow[], include: string[] | undefined): ParamDiffRow[] {
  if (include === undefined || include.length === 0) return rows
  const wanted = new Set(include)
  return rows.filter((row) => wanted.has(row.key))
}

function selectMetricRows(
  rows: MetricComparisonRow[],
  include: string[] | undefined
): MetricComparisonRow[] {
  if (include === undefined || include.length === 0) return rows
  const wanted = new Set(include)
  return rows.filter((row) => wanted.has(row.key))
}

function formatValue(value: number | null | undefined, precision: number): string {
  return value === undefined || value === null ? '' : value.toFixed(precision)
}

function formatPct(pct: number): string {
  const sign = pct >= 0 ? '+' : ''
  return `${sign}${pct.toFixed(2)}%`
}

const LATEX_ESCAPES: Readonly<Record<string, string>> = {
  '\\': '\\textbackslash{}',
  '&': '\\&',
  '%': '\\%',
  $: '\\$',
  '#': '\\#',
  _: '\\_',
  '{': '\\{',
  '}': '\\}',
  '~': '\\textasciitilde{}',
  '^': '\\textasciicircum{}'
}

function latexEscape(text: string): string {
  return text.replace(/[\\&%$#_{}~^]/g, (char) => LATEX_ESCAPES[char] ?? char)
}

function latexMetricCell(
  row: MetricComparisonRow,
  runId: string,
  highlight: boolean,
  precision: number
): string {
  const value = row.values[runId]
  const text = formatValue(value, precision)
  if (text === '') return '--'
  if (highlight) {
    if (runId === row.bestRunId) return `\\textbf{${text}}`
    if (runId === row.secondBestRunId) return `\\underline{${text}}`
  }
  return text
}

function latexDeltaCell(baselineId: string | null, row: MetricComparisonRow, runId: string): string {
  if (runId === baselineId) return '--'
  const pct = row.pct[runId]
  return pct === undefined ? '--' : `${pct >= 0 ? '+' : ''}${pct.toFixed(2)}\\%`
}

function formatLatex(
  matrix: ComparisonMatrix,
  params: ParamDiffRow[],
  metrics: MetricComparisonRow[],
  precision: number,
  highlight: boolean,
  showDelta: boolean,
  options: ComparisonTableOptions
): string {
  let columns = 1 + matrix.runs.length
  if (showDelta) columns += matrix.runs.length
  const lines: string[] = ['\\begin{table}[t]', '\\centering']
  if (options.caption !== undefined && options.caption.trim() !== '') {
    lines.push(`\\caption{${options.caption}}`)
  }
  if (options.label !== undefined && options.label.trim() !== '') {
    lines.push(`\\label{${options.label}}`)
  }
  lines.push(`\\begin{tabular}{l${'r'.repeat(columns - 1)}}`, '\\toprule')
  if (showDelta) {
    const cells = matrix.runs.map((run) => `\\multicolumn{2}{c}{${latexEscape(run.label)}}`)
    lines.push(` & ${cells.join(' & ')} \\\\`)
    lines.push(` & ${matrix.runs.map(() => ' & $\\Delta\\%$').join('')} \\\\`)
  } else {
    const cells = matrix.runs.map((run) => ` & ${latexEscape(run.label)}`)
    lines.push(` ${cells.join('')} \\\\`)
  }
  lines.push('\\midrule')
  if (params.length > 0) {
    lines.push(`\\multicolumn{${columns}}{l}{\\emph{Hyperparameters}} \\\\`)
    for (const row of params) {
      let line = latexEscape(row.key)
      for (const run of matrix.runs) {
        line += ` & ${latexEscape(row.values[run.runId] ?? '')}`
        if (showDelta) line += ' & '
      }
      lines.push(`${line} \\\\`)
    }
    if (metrics.length > 0) lines.push('\\midrule')
  }
  if (metrics.length > 0) {
    lines.push(`\\multicolumn{${columns}}{l}{\\emph{Metrics}} \\\\`)
    for (const row of metrics) {
      let line = latexEscape(row.key)
      for (const run of matrix.runs) {
        line += ` & ${latexMetricCell(row, run.runId, highlight, precision)}`
        if (showDelta) line += ` & ${latexDeltaCell(matrix.baselineId, row, run.runId)}`
      }
      lines.push(`${line} \\\\`)
    }
  }
  lines.push('\\bottomrule', '\\end{tabular}', '\\end{table}')
  return `${lines.join('\n')}\n`
}

function markdownEscape(text: string): string {
  return text.replaceAll('|', '\\|').replaceAll('\n', ' ')
}

function formatMarkdown(
  matrix: ComparisonMatrix,
  params: ParamDiffRow[],
  metrics: MetricComparisonRow[],
  precision: number,
  highlight: boolean,
  showDelta: boolean,
  caption: string | undefined
): string {
  const lines: string[] = []
  if (caption !== undefined && caption.trim() !== '') lines.push(caption, '')
  const header = ['item']
  for (const run of matrix.runs) {
    header.push(run.label)
    if (showDelta) header.push('Δ%')
  }
  lines.push(`| ${header.join(' | ')} |`)
  lines.push(`|${header.map((_, index) => (index === 0 ? ' --- |' : ' ---: |')).join('')}`)
  if (params.length > 0) {
    lines.push(`| **Hyperparameters** |${'  |'.repeat(header.length - 1)}`)
    for (const row of params) {
      let line = `| ${markdownEscape(row.key)} |`
      for (const run of matrix.runs) {
        line += ` ${markdownEscape(row.values[run.runId] ?? '')} |`
        if (showDelta) line += '  |'
      }
      lines.push(line)
    }
  }
  if (metrics.length > 0) {
    lines.push(`| **Metrics** |${'  |'.repeat(header.length - 1)}`)
    for (const row of metrics) {
      let line = `| ${markdownEscape(row.key)} |`
      for (const run of matrix.runs) {
        let text = formatValue(row.values[run.runId], precision)
        if (text === '') text = '—'
        else if (highlight && run.runId === row.bestRunId) text = `**${text}**`
        line += ` ${text} |`
        if (showDelta) {
          const pct = run.runId === matrix.baselineId ? undefined : row.pct[run.runId]
          line += ` ${pct === undefined ? '—' : formatPct(pct)} |`
        }
      }
      lines.push(line)
    }
  }
  return lines.join('\n')
}

function csvField(value: string): string {
  return /[",\n\r]/.test(value) ? `"${value.replaceAll('"', '""')}"` : value
}

function formatCsv(
  matrix: ComparisonMatrix,
  params: ParamDiffRow[],
  metrics: MetricComparisonRow[],
  precision: number,
  showDelta: boolean
): string {
  const rows: string[][] = []
  const header = ['item']
  for (const run of matrix.runs) {
    header.push(run.label)
    if (showDelta) header.push(`${run.label} delta_pct`)
  }
  rows.push(header)
  for (const row of params) {
    const record = [row.key]
    for (const run of matrix.runs) {
      record.push(row.values[run.runId] ?? '')
      if (showDelta) record.push('')
    }
    rows.push(record)
  }
  for (const row of metrics) {
    const record = [row.key]
    for (const run of matrix.runs) {
      record.push(formatValue(row.values[run.runId], precision))
      if (showDelta) {
        const pct = run.runId === matrix.baselineId ? undefined : row.pct[run.runId]
        record.push(pct === undefined ? '' : pct.toFixed(2))
      }
    }
    rows.push(record)
  }
  return `${rows.map((record) => record.map(csvField).join(',')).join('\n')}\n`
}