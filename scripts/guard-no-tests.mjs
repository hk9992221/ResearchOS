#!/usr/bin/env node
/**
 * Fails when `main` contains test files or test-support directories (repo rule
 * §6.6). Tests live only on feature branches.
 *
 * Usage: node scripts/guard-no-tests.mjs
 */
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

export const TEST_PATH_PATTERNS = [
  /(^|\/)test\/[^/]+\.(ts|tsx|js|mjs|cjs)$/,
  /(^|\/)tests\/[^/]+\.(ts|tsx|js|mjs|cjs)$/,
  /(^|\/)(test-support|__fixtures__|__tests__)\//,
  /\.test\.(ts|tsx|js|mjs|cjs)$/,
  /\.spec\.(ts|tsx|js|mjs|cjs)$/,
  /_test\.go$/,
  /(^|\/)testdata\//
]

export function findTestPaths(paths) {
  return paths.filter((path) => TEST_PATH_PATTERNS.some((pattern) => pattern.test(path)))
}

export function listMainPaths() {
  const output = execFileSync('git', ['ls-tree', '-r', 'main', '--name-only'], {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe']
  })
  return output.split(/\r?\n/).filter((line) => line !== '')
}

function main() {
  let paths
  try {
    paths = listMainPaths()
  } catch (error) {
    console.error(`guard-no-tests: cannot read main (${error instanceof Error ? error.message : String(error)})`)
    process.exit(1)
  }

  const offenders = findTestPaths(paths)
  if (offenders.length > 0) {
    console.error('guard-no-tests: test artifacts found on main:')
    for (const path of offenders) console.error(`  ${path}`)
    console.error(`${offenders.length} offending path(s); tests must stay on feature branches.`)
    process.exit(1)
  }

  console.log(`guard-no-tests ok: ${paths.length} tracked path(s) on main are test-free`)
}

if (process.argv[1] === fileURLToPath(import.meta.url)) main()