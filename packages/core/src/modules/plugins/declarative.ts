import { lstatSync, readdirSync, readFileSync } from 'node:fs'
import * as path from 'node:path'

import type { Metric, PluginDeclarativeSkill, PluginRule, ScanFileEntry } from '@researchos/shared'
import {
  PLUGIN_MAX_DECLARATIVE_FILES,
  PLUGIN_RULE_KINDS,
  PLUGIN_SKILL_MAX_BYTES
} from '@researchos/shared'
import { z } from 'zod'

import type { MetricExtractor, MetricFileAccessor } from '../experiments/metrics'
import { walkJsonLeaves } from '../experiments/metrics'
import { parseCsvRecords } from '../experiments/metadata'
import { compileGlob } from '../experiments/rules'

/**
 * Declarative plugin contributions (plan §3.7): `skills/*.md` register into the
 * Weekly Review skill set and `rules/*.json` into the experiments metric
 * extractor registry. Nothing here executes plugin code.
 */

export interface PluginSkillSink {
  registerSkill(skill: PluginDeclarativeSkill): void
  unregisterSkill(id: string): void
}

/** The experiments-side hook; W10's registry satisfies this shape. */
export interface PluginRuleSink {
  registerExtractor(extractor: MetricExtractor): () => void
}

export interface DeclarativeImportResult {
  skills: string[]
  rules: string[]
  warnings: string[]
  dispose(): void
}

const ruleSchema = z
  .object({
    file: z.string().trim().min(1).max(300),
    kind: z.enum(PLUGIN_RULE_KINDS),
    prefix: z.string().trim().max(128).optional(),
    columns: z.record(z.string().trim().min(1).max(128), z.string().trim().min(1).max(128)).optional()
  })
  .strict()

/** Reads `skills/<name>.md` files; unreadable or oversized files are warnings. */
export function loadPluginSkills(pluginRoot: string, pluginId: string): {
  skills: PluginDeclarativeSkill[]
  warnings: string[]
} {
  const directory = path.join(pluginRoot, 'skills')
  const files = listRegularFiles(directory, '.md')
  const skills: PluginDeclarativeSkill[] = []
  const warnings: string[] = []
  for (const file of files) {
    const absolute = path.join(directory, file)
    let info
    try {
      info = lstatSync(absolute)
    } catch {
      continue
    }
    if (!info.isFile() || info.isSymbolicLink()) {
      warnings.push(`skills/${file}: not a regular file`)
      continue
    }
    if (info.size > PLUGIN_SKILL_MAX_BYTES) {
      warnings.push(`skills/${file}: exceeds ${PLUGIN_SKILL_MAX_BYTES} bytes`)
      continue
    }
    const prompt = readFileSync(absolute, 'utf8').trim()
    if (prompt === '') {
      warnings.push(`skills/${file}: empty skill`)
      continue
    }
    const name = skillName(prompt, file)
    skills.push({
      id: `plugin:${pluginId}:${file}`,
      name,
      prompt,
      source: 'plugin',
      builtin: false
    })
  }
  return { skills, warnings }
}

function skillName(prompt: string, file: string): string {
  const frontmatter = /^---\r?\n([\s\S]*?)\r?\n---\r?\n/.exec(prompt)
  if (frontmatter !== null) {
    const nameLine = /^name:\s*(.+)$/m.exec(frontmatter[1] ?? '')
    if (nameLine !== null) {
      const value = (nameLine[1] ?? '').trim().replace(/^["']|["']$/g, '')
      if (value !== '') return value.slice(0, 60)
    }
  }
  const heading = /^#\s+(.+)$/m.exec(prompt)
  if (heading !== null) {
    const value = (heading[1] ?? '').trim()
    if (value !== '') return value.slice(0, 60)
  }
  return file.replace(/\.md$/i, '').slice(0, 60)
}

/** Reads and validates `rules/*.json`; invalid rules become warnings. */
export function loadPluginRules(pluginRoot: string, pluginId: string): {
  rules: Array<{ name: string; rule: PluginRule; extractor: MetricExtractor }>
  warnings: string[]
} {
  const directory = path.join(pluginRoot, 'rules')
  const files = listRegularFiles(directory, '.json')
  const rules: Array<{ name: string; rule: PluginRule; extractor: MetricExtractor }> = []
  const warnings: string[] = []
  for (const file of files) {
    const absolute = path.join(directory, file)
    let info
    try {
      info = lstatSync(absolute)
    } catch {
      continue
    }
    if (!info.isFile() || info.isSymbolicLink()) {
      warnings.push(`rules/${file}: not a regular file`)
      continue
    }
    if (info.size > 64 * 1024) {
      warnings.push(`rules/${file}: exceeds 64 KiB`)
      continue
    }
    let raw: unknown
    try {
      raw = JSON.parse(readFileSync(absolute, 'utf8')) as unknown
    } catch (error) {
      warnings.push(`rules/${file}: invalid JSON (${error instanceof Error ? error.message : String(error)})`)
      continue
    }
    const parsed = ruleSchema.safeParse(raw)
    if (!parsed.success) {
      warnings.push(`rules/${file}: ${parsed.error.issues[0]?.message ?? 'invalid rule'}`)
      continue
    }
    const rule = parsed.data
    try {
      const compiled = compileGlob(rule.file)
      const name = `plugin:${pluginId}:${file}`
      rules.push({ name, rule, extractor: createRuleExtractor(name, rule, compiled.re) })
    } catch (error) {
      warnings.push(`rules/${file}: ${error instanceof Error ? error.message : String(error)}`)
    }
  }
  return { rules, warnings }
}

export interface DeclarativeSinks {
  skills?: PluginSkillSink
  rules?: PluginRuleSink
}

/**
 * Registers every declarative contribution; the returned disposer removes
 * exactly what was registered, even when a sink throws part-way through.
 */
export function importDeclarative(
  pluginRoot: string,
  pluginId: string,
  sinks: DeclarativeSinks
): DeclarativeImportResult {
  const skillLoad = loadPluginSkills(pluginRoot, pluginId)
  const ruleLoad = loadPluginRules(pluginRoot, pluginId)
  const warnings = [...skillLoad.warnings, ...ruleLoad.warnings]
  const registeredSkills: string[] = []
  const registeredRules: string[] = []
  const ruleDisposers: Array<() => void> = []

  if (sinks.skills !== undefined) {
    for (const skill of skillLoad.skills) {
      try {
        sinks.skills.registerSkill(skill)
        registeredSkills.push(skill.id)
      } catch (error) {
        warnings.push(`skills/${skill.id}: ${error instanceof Error ? error.message : String(error)}`)
      }
    }
  }
  if (sinks.rules !== undefined) {
    for (const item of ruleLoad.rules) {
      try {
        const dispose = sinks.rules.registerExtractor(item.extractor)
        ruleDisposers.push(dispose)
        registeredRules.push(item.name)
      } catch (error) {
        warnings.push(`rules/${item.name}: ${error instanceof Error ? error.message : String(error)}`)
      }
    }
  }

  return {
    skills: registeredSkills,
    rules: registeredRules,
    warnings,
    dispose: () => {
      if (sinks.skills !== undefined) {
        for (const id of registeredSkills) {
          try {
            sinks.skills.unregisterSkill(id)
          } catch {
            // Disposal is best-effort: one failing sink must not leak the rest.
          }
        }
      }
      for (const dispose of ruleDisposers) {
        try {
          dispose()
        } catch {
          // See above.
        }
      }
    }
  }
}

function listRegularFiles(directory: string, extension: string): string[] {
  let entries
  try {
    entries = readdirSync(directory, { withFileTypes: true })
  } catch {
    return []
  }
  return entries
    .filter((entry) => entry.isFile() && entry.name.toLowerCase().endsWith(extension))
    .map((entry) => entry.name)
    .sort()
    .slice(0, PLUGIN_MAX_DECLARATIVE_FILES)
}

function createRuleExtractor(name: string, rule: PluginRule, pattern: RegExp): MetricExtractor {
  const prefix = rule.prefix ?? ''
  const json = rule.kind === 'json_numeric_leaves'
  return {
    name,
    supports(entry: ScanFileEntry) {
      if (!pattern.test(entry.path)) return false
      const extension = path.posix.extname(entry.path).toLowerCase()
      if (json) return extension === '.json' && (entry.kind === 'metrics' || entry.kind === 'config' || entry.kind === 'data')
      return extension === '.csv' || extension === '.tsv'
    },
    extract(accessor: MetricFileAccessor, entry: ScanFileEntry): Metric[] {
      const text = accessor.read(entry.path).toString('utf8')
      if (json) {
        const parsed: unknown = JSON.parse(text)
        const metrics: Metric[] = []
        walkJsonLeaves(parsed, '', (pointer, value) => {
          if (typeof value !== 'number' || !Number.isFinite(value)) return
          const key = `${prefix}${pointer.replace(/^\//, '') || '(root)'}`
          metrics.push({
            key,
            label: key,
            value,
            raw: String(value),
            unit: null,
            source: { path: entry.path, pointer, line: null, sha256: entry.sha256 },
            extractor: name
          })
        })
        metrics.sort((left, right) => (left.key < right.key ? -1 : left.key > right.key ? 1 : 0))
        return metrics
      }
      const delimiter = path.posix.extname(entry.path).toLowerCase() === '.tsv' ? '\t' : ','
      const records = parseCsvRecords(text, delimiter)
      const header = records[0]
      const last = records[records.length - 1]
      if (header === undefined || last === undefined || records.length < 2) return []
      const line = records.length
      const wanted = rule.columns === undefined ? null : new Map(Object.entries(rule.columns))
      const metrics: Metric[] = []
      for (let index = 0; index < header.length; index += 1) {
        const column = (header[index] ?? '').trim()
        const cell = last[index]
        if (column === '' || cell === undefined) continue
        if (wanted !== null && ![...wanted.values()].includes(column)) continue
        const textValue = cell.trim()
        if (textValue === '') continue
        const value = Number(textValue)
        if (!Number.isFinite(value)) continue
        let key = column
        if (wanted !== null) {
          const mapped = [...wanted.entries()].find(([, source]) => source === column)
          if (mapped !== undefined) key = mapped[0]
        }
        metrics.push({
          key: `${prefix}${key}`,
          label: `${prefix}${key}`,
          value,
          raw: textValue,
          unit: null,
          source: { path: entry.path, pointer: null, line, sha256: entry.sha256 },
          extractor: name
        })
      }
      return metrics
    }
  }
}