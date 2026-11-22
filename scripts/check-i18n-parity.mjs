#!/usr/bin/env node
/**
 * Compares locale area files: every JSON file present in one directory must
 * exist in the others with exactly the same flattened key set.
 *
 * Usage: node scripts/check-i18n-parity.mjs <dir> <dir> [<dir>...]
 */
import { readdir, readFile } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

async function listJsonFiles(dir, base = dir) {
  const entries = await readdir(dir, { withFileTypes: true })
  const files = []
  for (const entry of entries) {
    const fullPath = join(dir, entry.name)
    if (entry.isDirectory()) {
      files.push(...(await listJsonFiles(fullPath, base)))
    } else if (entry.isFile() && entry.name.endsWith('.json')) {
      files.push(relative(base, fullPath).split('\\').join('/'))
    }
  }
  return files.sort()
}

function flattenKeys(value, prefix = '', keys = new Set()) {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    keys.add(prefix)
    return keys
  }
  for (const [key, child] of Object.entries(value)) {
    const next = prefix === '' ? key : `${prefix}.${key}`
    flattenKeys(child, next, keys)
  }
  return keys
}

export async function collectLocaleKeys(dir) {
  const files = await listJsonFiles(dir)
  const result = new Map()
  for (const file of files) {
    const raw = await readFile(join(dir, file), 'utf8')
    let parsed
    try {
      parsed = JSON.parse(raw)
    } catch (error) {
      throw new Error(`${file}: invalid JSON (${error instanceof Error ? error.message : String(error)})`)
    }
    result.set(file, flattenKeys(parsed))
  }
  return result
}

export async function checkI18nParity(dirs) {
  if (dirs.length < 2) throw new Error('check-i18n-parity needs at least two locale directories')
  for (const dir of dirs) {
    if (!existsSync(dir)) throw new Error(`locale directory not found: ${dir}`)
  }

  const reference = await collectLocaleKeys(dirs[0])
  const issues = []

  for (const dir of dirs.slice(1)) {
    const compared = await collectLocaleKeys(dir)
    for (const [file, referenceKeys] of reference) {
      const comparedKeys = compared.get(file)
      if (comparedKeys === undefined) {
        issues.push(`${dir}: missing file ${file}`)
        continue
      }
      for (const key of referenceKeys) {
        if (!comparedKeys.has(key)) issues.push(`${dir}: ${file}: missing key ${key}`)
      }
      for (const key of comparedKeys) {
        if (!referenceKeys.has(key)) issues.push(`${dir}: ${file}: extra key ${key}`)
      }
    }
    for (const file of compared.keys()) {
      if (!reference.has(file)) issues.push(`${dir}: unexpected file ${file}`)
    }
  }

  return { ok: issues.length === 0, files: reference.size, issues }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const dirs = process.argv.slice(2)
  try {
    const report = await checkI18nParity(dirs)
    if (report.ok) {
      console.log(`i18n parity ok: ${report.files} file(s) across ${dirs.length} locale dirs`)
    } else {
      for (const issue of report.issues) console.error(issue)
      console.error(`i18n parity failed: ${report.issues.length} issue(s)`)
      process.exit(1)
    }
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error))
    process.exit(1)
  }
}