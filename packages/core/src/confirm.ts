/**
 * In-app human confirmation tokens for destructive operations.
 *
 * Tokens live in process memory only: never persisted, never crossing the
 * process boundary, so an MCP client cannot mint one. Every token is one-time,
 * bound to one operation kind and one target set, and expires after five
 * minutes.
 */
import { randomBytes } from 'node:crypto'

import { type Clock, SystemClock } from './clock'
import { AppErrorCode, appError } from './errors'

export type ConfirmKind =
  | 'purge_entity'
  | 'empty_trash'
  | 'delete_project'
  | 'pdf_overwrite'
  | 'import_project'
  /** New in the Electron rewrite: a plugin write that needs human approval. */
  | 'plugin_write'

const CONFIRM_KINDS: readonly ConfirmKind[] = [
  'purge_entity',
  'empty_trash',
  'delete_project',
  'pdf_overwrite',
  'import_project',
  'plugin_write'
]

export function isConfirmKind(value: unknown): value is ConfirmKind {
  return typeof value === 'string' && (CONFIRM_KINDS as readonly string[]).includes(value)
}

/**
 * Binds a token to one operation and one target set. Entity types and ids are
 * compared as sets, so order and duplicates do not matter.
 */
export interface ConfirmScope {
  kind: ConfirmKind
  projectId: string
  entityTypes?: readonly string[]
  entityIds?: readonly string[]
}

/** Five minutes, from §5.8. */
export const DEFAULT_CONFIRM_TTL_MS = 5 * 60 * 1000

export interface ConfirmServiceOptions {
  clock?: Clock
  /** Token lifetime in milliseconds; zero or negative means the default. */
  ttlMs?: number
  /** Test seam so a token value can be pinned; production keeps randomBytes. */
  tokenFactory?: () => string
}

interface Entry {
  scope: NormalizedScope
  expiresAt: number
}

interface NormalizedScope {
  kind: ConfirmKind
  projectId: string
  entityTypes: string[]
  entityIds: string[]
}

export class ConfirmService {
  private readonly clock: Clock
  private readonly ttlMs: number
  private readonly tokenFactory: () => string
  private readonly tokens = new Map<string, Entry>()

  constructor(options: ConfirmServiceOptions = {}) {
    this.clock = options.clock ?? new SystemClock()
    this.ttlMs = options.ttlMs !== undefined && options.ttlMs > 0 ? options.ttlMs : DEFAULT_CONFIRM_TTL_MS
    this.tokenFactory = options.tokenFactory ?? (() => randomBytes(32).toString('hex'))
  }

  /** Issues a token. Only the in-app human confirmation callback may call this. */
  issue(scope: ConfirmScope): string {
    if (!isConfirmKind(scope.kind)) {
      throw appError(AppErrorCode.InvalidArgument, `confirm: unknown kind ${JSON.stringify(scope.kind)}`)
    }
    const now = this.clock.now().getTime()
    this.prune(now)
    const token = this.tokenFactory()
    this.tokens.set(token, { scope: normalize(scope), expiresAt: now + this.ttlMs })
    return token
  }

  /**
   * Invalidates the token. Unknown, expired and already-used tokens yield
   * `USER_CONFIRMATION_REQUIRED`; a scope mismatch yields
   * `CONFIRMATION_SCOPE_MISMATCH` and leaves the token usable.
   */
  consume(token: string, scope: ConfirmScope): void {
    const now = this.clock.now().getTime()
    this.prune(now)
    const entry = this.tokens.get(token)
    if (entry === undefined) {
      throw appError(
        AppErrorCode.UserConfirmationRequired,
        'token is unknown, expired, or already used'
      )
    }
    if (now >= entry.expiresAt) {
      this.tokens.delete(token)
      throw appError(AppErrorCode.UserConfirmationRequired, 'token expired')
    }
    if (!scopeMatches(entry.scope, normalize(scope))) {
      throw appError(
        AppErrorCode.ConfirmationScopeMismatch,
        'confirmation scope does not match the requested operation'
      )
    }
    this.tokens.delete(token)
  }

  /** Number of live tokens; diagnostics and tests only. */
  get liveCount(): number {
    this.prune(this.clock.now().getTime())
    return this.tokens.size
  }

  private prune(now: number): void {
    for (const [token, entry] of this.tokens) {
      if (now >= entry.expiresAt) this.tokens.delete(token)
    }
  }
}

function normalize(scope: ConfirmScope): NormalizedScope {
  return {
    kind: scope.kind,
    projectId: scope.projectId,
    entityTypes: sortedSet(scope.entityTypes),
    entityIds: sortedSet(scope.entityIds)
  }
}

function sortedSet(values: readonly string[] | undefined): string[] {
  return [...new Set(values ?? [])].sort()
}

function scopeMatches(left: NormalizedScope, right: NormalizedScope): boolean {
  return (
    left.kind === right.kind &&
    left.projectId === right.projectId &&
    sameSet(left.entityTypes, right.entityTypes) &&
    sameSet(left.entityIds, right.entityIds)
  )
}

function sameSet(left: readonly string[], right: readonly string[]): boolean {
  if (left.length !== right.length) return false
  return left.every((value, index) => value === right[index])
}