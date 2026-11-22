#!/usr/bin/env node
// Schema parity gate.
//
// 1. Every copied migration must be byte-identical to its Go source file
//    (internal/**/migrations/<kind>/<name>.sql).
// 2. bands.ts must be up to date (node scripts/gen-bands.mjs --check).
// 3. The migration engine is applied to a fresh temp database and the physical
//    schema is compared with the Drizzle definitions, including a fixture
//    database built by executing the Go SQL directly. Any drift exits 1.
//
// The comparison lives in packages/core/src/db/parity.ts and is executed by
// vitest so it shares the exact TypeScript sources the application uses.

import { spawnSync } from 'node:child_process'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const migrationsRoot = join(repoRoot, 'packages/core/src/db/migrations')
const kinds = ['global', 'project']

function walk(directory, visit) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    if (entry.name === 'node_modules' || entry.name === '.git') continue
    const full = join(directory, entry.name)
    if (entry.isDirectory()) walk(full, visit)
    else visit(full)
  }
}

const goSources = new Map()
const internalRoot = join(repoRoot, 'internal')
let hasGoTree = true
try {
  statSync(internalRoot)
  walk(internalRoot, (file) => {
    if (!file.endsWith('.sql')) return
    const parts = file.split(/[\\/]/)
    const kind = parts.at(-2)
    const name = parts.at(-1)
    goSources.set(`${kind}/${name}`, file)
  })
} catch {
  hasGoTree = false
}

const failures = []
if (hasGoTree) {
  for (const kind of kinds) {
    const directory = join(migrationsRoot, kind)
    for (const name of readdirSync(directory)) {
      if (!name.endsWith('.sql')) continue
      const copied = readFileSync(join(directory, name))
      const source = goSources.get(`${kind}/${name}`)
      if (source === undefined) {
        failures.push(`${kind}/${name}: no Go source file under internal/**/migrations/${kind}/`)
        continue
      }
      if (!copied.equals(readFileSync(source))) {
        failures.push(`${kind}/${name}: differs from ${relative(repoRoot, source)}`)
      }
    }
  }
} else {
  console.warn('check-schema-parity: Go reference tree absent; skipping the byte-for-byte source check')
}

const bands = spawnSync(process.execPath, [join(repoRoot, 'scripts/gen-bands.mjs'), '--check'], {
  cwd: repoRoot,
  stdio: 'inherit'
})
if (bands.status !== 0) failures.push('bands.ts is stale or the generator failed')

const requireFromRoot = createRequire(join(repoRoot, 'package.json'))
const vitestManifest = requireFromRoot.resolve('vitest/package.json')
const vitestEntry = join(dirname(vitestManifest), requireFromRoot(vitestManifest).bin?.vitest ?? 'vitest.mjs')
const vitest = spawnSync(
  process.execPath,
  [vitestEntry, 'run', '--config', 'packages/core/vitest.config.ts', 'src/db/parity.test.ts'],
  { cwd: repoRoot, stdio: 'inherit' }
)
if (vitest.status !== 0) failures.push(`schema parity suite failed (exit ${vitest.status})`)

if (failures.length > 0) {
  for (const failure of failures) console.error(`check-schema-parity: ${failure}`)
  process.exit(1)
}
console.log('check-schema-parity: migrations verbatim, Drizzle schema matches fresh and Go-built databases')