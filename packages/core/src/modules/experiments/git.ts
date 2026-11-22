import { inflateSync } from 'node:zlib'
import { readFileSync, statSync } from 'node:fs'
import * as path from 'node:path'
import { spawnSync } from 'node:child_process'

import type { GitInfo } from '@researchos/shared'

import { formatTimestamp, type Clock } from '../../clock'
import { dirUnavailable, invalidInput } from './errors'

const DIFF_DEFAULT_MAX = 256 * 1024
const STATUS_LIMIT = 20_000

export interface GitDiffFile {
  path: string
  added: number
  deleted: number
  binary: boolean
}

export interface GitDiff {
  files: GitDiffFile[]
  patch: string
  truncated: boolean
}

export interface GitDiffOptions {
  from?: string
  to?: string
  pathspec?: string[]
  maxBytes?: number
}

/**
 * Read-only Git inspection: repository discovery,
 * branch and commit resolution read plain files; status and diff shell out to
 * a whitelist of read-only subcommands with locks, pager and external diffs
 * disabled. No fetch, no index write, no network.
 */
export function inspectGit(dir: string, clock: Clock): GitInfo {
  let isDirectory = false
  try {
    isDirectory = statSync(dir).isDirectory()
  } catch (error) {
    throw dirUnavailable(dir, error instanceof Error ? error.message : String(error))
  }
  if (!isDirectory) throw dirUnavailable(dir, 'not a directory')
  const absolute = path.resolve(dir)
  const found = findGitRoot(absolute)
  const capturedAt = formatTimestamp(clock.now())
  if (found === undefined) {
    return {
      detected: false,
      available: false,
      repoRoot: '',
      branch: null,
      commit: null,
      commitShort: null,
      commitTime: null,
      dirty: false,
      changedFiles: 0,
      untrackedFiles: 0,
      capturedAt,
      source: 'read_at_analysis',
      truncated: false,
      error: null
    }
  }
  const head = readHead(found.gitDir)
  const info: GitInfo = {
    detected: true,
    available: true,
    repoRoot: found.root,
    branch: head.branch,
    commit: head.commit,
    commitShort: head.commit === null ? null : head.commit.slice(0, 7),
    commitTime: null,
    dirty: false,
    changedFiles: 0,
    untrackedFiles: 0,
    capturedAt,
    source: 'read_at_analysis',
    truncated: false,
    error: null
  }
  if (head.commit !== null) {
    const inflated = readCommitTime(found.gitDir, head.commit)
    info.commitTime =
      inflated ?? gitCommitTime(found.root, head.commit)
  }
  const counts = gitStatusCounts(found.root)
  if (!counts.ok) {
    info.available = false
    info.error = counts.error
    return info
  }
  info.changedFiles = counts.changed
  info.untrackedFiles = counts.untracked
  info.dirty = counts.changed > 0 || counts.untracked > 0
  if (counts.changed + counts.untracked > STATUS_LIMIT) info.truncated = true
  return info
}

export function gitDiff(dir: string, options: GitDiffOptions = {}): GitDiff {
  const absolute = path.resolve(dir)
  const found = findGitRoot(absolute)
  if (found === undefined) return { files: [], patch: '', truncated: false }
  for (const revision of [options.from, options.to]) {
    if (revision !== undefined && revision !== '') validateRevision(revision)
  }
  const pathspec = options.pathspec ?? []
  for (const spec of pathspec) {
    if (spec.startsWith('-') || /[\n\r\u0000]/.test(spec)) {
      throw invalidInput(`无效的 pathspec：${spec}`, { reason: 'git_pathspec', pathspec: spec })
    }
  }
  const maxBytes = options.maxBytes !== undefined && options.maxBytes > 0 ? options.maxBytes : DIFF_DEFAULT_MAX
  const base = [
    '-C',
    found.root,
    '--no-pager',
    '-c',
    'core.fsmonitor=false',
    '-c',
    'core.pager=cat',
    '-c',
    'diff.external='
  ]
  const common = ['diff', '--no-color', '--no-ext-diff', '--no-textconv']
  if (options.from !== undefined && options.from !== '' && options.to !== undefined && options.to !== '') {
    common.push(`${options.from}..${options.to}`)
  } else if (options.from !== undefined && options.from !== '') {
    common.push(options.from)
  } else if (options.to !== undefined && options.to !== '') {
    common.push(options.to)
  }
  if (pathspec.length > 0) common.push('--', ...pathspec)

  const stat = runGit(base, [...common, '--numstat', '-z'])
  if (!stat.ok) throw new Error(`git diff --numstat: ${stat.error}`)
  const files = parseNumstat(stat.output)
  const patch = runGit(base, [...common, '--patch'])
  if (!patch.ok) throw new Error(`git diff --patch: ${patch.error}`)
  const raw = Buffer.from(patch.output)
  if (raw.length > maxBytes) {
    return { files, patch: raw.subarray(0, maxBytes).toString('utf8'), truncated: true }
  }
  return { files, patch: raw.toString('utf8'), truncated: false }
}

type GitRun = { ok: true; output: string } | { ok: false; error: string }

function runGit(baseArgs: string[], args: string[]): GitRun {
  const result = spawnSync('git', [...baseArgs, ...args], {
    encoding: 'utf8',
    timeout: 30_000,
    maxBuffer: 32 * 1024 * 1024,
    windowsHide: true,
    env: {
      ...process.env,
      GIT_OPTIONAL_LOCKS: '0',
      GIT_TERMINAL_PROMPT: '0',
      GIT_PAGER: 'cat'
    }
  })
  if (result.error !== undefined) return { ok: false, error: result.error.message }
  if (result.status !== 0) {
    const message = (result.stderr ?? '').trim()
    return { ok: false, error: message === '' ? `git exited with ${result.status ?? 'unknown'}` : message }
  }
  return { ok: true, output: result.stdout ?? '' }
}

function validateRevision(revision: string): void {
  if (revision.startsWith('-') || /[ \t\n\r\u0000]/.test(revision)) {
    throw invalidInput(`无效的 Git 版本：${revision}`, { reason: 'git_revision', revision })
  }
}

export function parseNumstat(raw: Buffer | string): GitDiffFile[] {
  const text = typeof raw === 'string' ? raw : raw.toString('utf8')
  const files: GitDiffFile[] = []
  for (const field of text.split('\u0000')) {
    if (field === '') continue
    const tabs = field.split('\t')
    if (tabs.length < 3) continue
    const added = tabs[0] ?? ''
    const deleted = tabs[1] ?? ''
    const filePath = tabs.slice(2).join('\t')
    if (added === '-' || deleted === '-') {
      files.push({ path: filePath, added: 0, deleted: 0, binary: true })
      continue
    }
    files.push({
      path: filePath,
      added: Number.parseInt(added, 10) || 0,
      deleted: Number.parseInt(deleted, 10) || 0,
      binary: false
    })
  }
  return files
}

type StatusCounts =
  | { ok: true; changed: number; untracked: number }
  | { ok: false; error: string }

function gitStatusCounts(root: string): StatusCounts {
  const result = runGit(
    ['-C', root, '--no-pager', '-c', 'core.fsmonitor=false', '-c', 'core.pager=cat', '-c', 'core.quotepath=false'],
    ['status', '--porcelain=v1', '-z', '--untracked-files=normal']
  )
  if (!result.ok) return { ok: false, error: result.error }
  let changed = 0
  let untracked = 0
  let renameSkip = false
  for (const record of result.output.split('\u0000')) {
    if (record.length < 2) continue
    if (renameSkip) {
      renameSkip = false
      continue
    }
    const status = record.slice(0, 2)
    if (status === '??') {
      untracked += 1
      continue
    }
    changed += 1
    if (/[RC]/.test(status)) renameSkip = true
  }
  return { ok: true, changed, untracked }
}

function findGitRoot(dir: string): { root: string; gitDir: string } | undefined {
  let current = dir
  for (;;) {
    const dotGit = path.join(current, '.git')
    try {
      const info = statSync(dotGit)
      if (info.isDirectory()) return { root: current, gitDir: dotGit }
      if (info.isFile()) {
        const gitDir = readGitFile(dotGit)
        if (gitDir !== undefined) return { root: current, gitDir }
      }
    } catch {
      // No .git here; walk up.
    }
    const parent = path.dirname(current)
    if (parent === current) return undefined
    current = parent
  }
}

function readGitFile(file: string): string | undefined {
  let raw: string
  try {
    raw = readFileSync(file, 'utf8')
  } catch {
    return undefined
  }
  const line = raw.trim()
  if (!line.startsWith('gitdir:')) return undefined
  const target = line.slice('gitdir:'.length).trim()
  return path.resolve(path.dirname(file), target)
}

function commonDir(gitDir: string): string {
  try {
    const raw = readFileSync(path.join(gitDir, 'commondir'), 'utf8').trim()
    return path.resolve(gitDir, raw)
  } catch {
    return gitDir
  }
}

function readHead(gitDir: string): { branch: string | null; commit: string | null } {
  let line: string
  try {
    line = readFileSync(path.join(gitDir, 'HEAD'), 'utf8').trim()
  } catch {
    return { branch: null, commit: null }
  }
  if (line.startsWith('ref: ')) {
    const ref = line.slice('ref: '.length).trim()
    return {
      branch: ref.replace(/^refs\/heads\//, ''),
      commit: resolveRef(gitDir, ref)
    }
  }
  return { branch: null, commit: isHexSha(line) ? line : null }
}

function resolveRef(gitDir: string, ref: string): string | null {
  const common = commonDir(gitDir)
  for (const dir of [gitDir, common]) {
    try {
      const value = readFileSync(path.join(dir, ...ref.split('/')), 'utf8').trim()
      if (isHexSha(value)) return value
    } catch {
      // Try the next location.
    }
  }
  try {
    for (const line of readFileSync(path.join(common, 'packed-refs'), 'utf8').split(/\r?\n/)) {
      const trimmed = line.trim()
      if (trimmed === '' || trimmed.startsWith('#') || trimmed.startsWith('^')) continue
      const parts = trimmed.split(/\s+/)
      if (parts.length === 2 && parts[1] === ref) return parts[0] ?? null
    }
  } catch {
    return null
  }
  return null
}

function readCommitTime(gitDir: string, commit: string): string | null {
  if (commit.length < 3) return null
  try {
    const objectPath = path.join(commonDir(gitDir), 'objects', commit.slice(0, 2), commit.slice(2))
    const inflated = inflateSync(readFileSync(objectPath), { maxOutputLength: 1024 * 1024 }).toString('utf8')
    const headerEnd = inflated.indexOf('\u0000')
    if (headerEnd < 0) return null
    for (const line of inflated.slice(headerEnd + 1).split('\n')) {
      if (!line.startsWith('committer ')) continue
      const fields = line.split(/\s+/)
      const seconds = Number.parseInt(fields[fields.length - 2] ?? '', 10)
      if (!Number.isFinite(seconds)) return null
      return formatTimestamp(new Date(seconds * 1000))
    }
  } catch {
    return null
  }
  return null
}

function gitCommitTime(root: string, commit: string): string | null {
  const result = runGit(['-C', root, '--no-pager', '-c', 'core.pager=cat'], [
    'show',
    '-s',
    '--no-color',
    '--format=%cI',
    commit
  ])
  if (!result.ok) return null
  const at = new Date(result.output.trim())
  return Number.isNaN(at.getTime()) ? null : formatTimestamp(at)
}

function isHexSha(value: string): boolean {
  return value.length >= 40 && /^[0-9a-fA-F]+$/.test(value)
}