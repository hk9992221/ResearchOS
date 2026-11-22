import { createHash } from 'node:crypto'

import type Database from 'better-sqlite3'

import { formatTimestamp, type Clock, SystemClock } from '../clock'
import { AppError, AppErrorCode, appError } from '../errors'
import type { Logger } from '../logger'
import { backupDatabase } from './backup'
import { MIGRATION_BANDS, MIGRATION_SOURCES, TARGET_VERSIONS, type MigrationKind } from './bands'
import { type OpenedConnection, openReadOnlyDb } from './open'

type Db = Database.Database

export type DbKind = MigrationKind

export interface Migration {
  readonly version: number
  readonly name: string
  readonly filename: string
  readonly band: string
  readonly checksum: string
  readonly sql: string
}

/** The Go engine's checksum: lowercase hex SHA-256 of the migration text bytes. */
export function migrationChecksum(sql: string): string {
  return createHash('sha256').update(Buffer.from(sql, 'utf8')).digest('hex')
}

function loadCatalog(kind: DbKind): readonly Migration[] {
  const sources = MIGRATION_SOURCES[kind]
  const migrations = sources.map((source) => {
    const checksum = migrationChecksum(source.sql)
    if (checksum !== source.checksum) {
      throw new Error(
        `migration ${String(source.version).padStart(4, '0')}_${source.name} in bands.ts was modified ` +
          '(embedded checksum no longer matches the SQL text)'
      )
    }
    return {
      version: source.version,
      name: source.name,
      filename: `${String(source.version).padStart(4, '0')}_${source.name}.sql`,
      band: source.band,
      checksum,
      sql: source.sql
    }
  })
  for (let index = 1; index < migrations.length; index += 1) {
    if (migrations[index - 1]!.version >= migrations[index]!.version) {
      throw new Error(`migration versions for ${kind} are not strictly increasing`)
    }
  }
  return migrations
}

const CATALOG: Readonly<Record<DbKind, readonly Migration[]>> = {
  global: loadCatalog('global'),
  project: loadCatalog('project')
}

export function migrationsFor(kind: DbKind): readonly Migration[] {
  return CATALOG[kind]
}

/** The version this build migrates `kind` to. */
export function targetVersion(kind: DbKind): number {
  return TARGET_VERSIONS[kind]
}

export { MIGRATION_BANDS }

export interface MigrationOptions {
  clock?: Clock
  /** Defaults to `<OS cache dir>/ResearchOS/backups`. Tests inject a temp dir. */
  backupRoot?: string
  /** Disables the pre-migration snapshot. Tests only. */
  skipBackup?: boolean
  logger?: Logger
}

export interface MigrationResult {
  /** Highest applied version before the call. */
  from: number
  /** Highest applied version after the call. */
  to: number
  /** Versions applied by this call, in order. */
  applied: number[]
  /** Pre-migration snapshot path; empty when none was taken. */
  backupPath: string
}

export interface AppliedMigration {
  version: number
  name: string
  checksum: string
}

export interface Inspection {
  path: string
  kind: DbKind
  version: number
  target: number
  hasMigrationsTable: boolean
  issues: string[]
}

/**
 * Brings `db` to the target version of `kind`.
 *
 * - Only versions above the current maximum are applied.
 * - Already-applied migrations are checksum-verified; a mismatch or a missing
 *   migration refuses the run with `MIGRATION_TAMPERED`.
 * - A database newer than this build refuses with `SCHEMA_TOO_NEW`.
 * - A non-empty database is snapshotted with `VACUUM INTO` first (keep 5).
 * - Each migration and its `schema_migrations` row share one BEGIN IMMEDIATE
 *   transaction, so a failure leaves the database exactly as it was.
 * - Idempotent: a second call is a no-op.
 */
export function migrateDatabase(db: Db, kind: DbKind, dbPath: string, options: MigrationOptions = {}): MigrationResult {
  const clock = options.clock ?? new SystemClock()
  const migrations = migrationsFor(kind)
  const applied = readApplied(db)
  const from = applied.reduce((highest, row) => Math.max(highest, row.version), 0)
  const target = targetVersion(kind)
  const result: MigrationResult = { from, to: from, applied: [], backupPath: '' }

  if (from > target) {
    throw appError(
      AppErrorCode.SchemaTooNew,
      `database ${dbPath} is at version ${from}, this build targets ${target}`
    )
  }

  const byVersion = new Map(migrations.map((migration) => [migration.version, migration]))
  for (const row of applied) {
    const migration = byVersion.get(row.version)
    if (migration === undefined) {
      throw appError(
        AppErrorCode.MigrationTampered,
        `version ${padVersion(row.version)} (${row.name}) is applied but the migration is gone`
      )
    }
    if (migration.checksum !== row.checksum) {
      throw appError(
        AppErrorCode.MigrationTampered,
        `version ${padVersion(row.version)} checksum ${migration.checksum} does not match the recorded ${row.checksum}`
      )
    }
  }

  // Every migration this build ships but the database has not recorded is
  // pending — including versions *below* the recorded maximum. A workspace that
  // ran 0510 before 0501/0502 existed must still gain those schema additions,
  // otherwise `SELECT *` callers meet missing columns and tables.
  const recorded = new Set(applied.map((row) => row.version))
  const pending = migrations.filter((migration) => !recorded.has(migration.version))
  if (pending.length === 0) return result

  // Nothing of value exists in a brand-new database, so only back up when an
  // applied schema is at risk.
  if (from > 0 && !options.skipBackup) {
    result.backupPath = backupDatabase(db, dbPath, from, {
      clock,
      backupRoot: options.backupRoot,
      logger: options.logger
    })
  }

  for (const migration of pending) {
    applyOne(db, migration, formatTimestamp(clock.now()))
    result.applied.push(migration.version)
  }
  // The recorded maximum stays the reported version when only older gaps were
  // filled (a 0510 database that gained 0501 and 0502 is still at 510).
  result.to = Math.max(from, ...pending.map((migration) => migration.version))
  return result
}

function applyOne(db: Db, migration: Migration, appliedAt: string): void {
  try {
    db.transaction(() => {
      db.exec(migration.sql)
      db.prepare(
        'INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)'
      ).run(migration.version, migration.name, migration.checksum, appliedAt)
    }).immediate()
  } catch (error) {
    throw appError(
      AppErrorCode.Internal,
      `migration ${padVersion(migration.version)}_${migration.name} failed: ${
        error instanceof Error ? error.message : String(error)
      }`,
      { cause: error, details: { version: migration.version, name: migration.name } }
    )
  }
}

/** Reads schema_migrations; an absent table means "fresh database". */
export function readApplied(db: Db): AppliedMigration[] {
  return readAppliedState(db).rows
}

function readAppliedState(db: Db): { rows: AppliedMigration[]; hasTable: boolean } {
  const exists = db
    .prepare("SELECT count(*) AS n FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'")
    .get() as { n: number }
  if (exists.n === 0) return { rows: [], hasTable: false }
  const rows = db
    .prepare('SELECT version, name, checksum FROM schema_migrations ORDER BY version')
    .all() as AppliedMigration[]
  return { rows, hasTable: true }
}

/**
 * Read-only schema health report used by workspace.scan and diagnostics. It
 * never migrates or repairs.
 */
export function inspectDatabase(dbPath: string, kind: DbKind, logger?: Logger): Inspection {
  const target = targetVersion(kind)
  const inspection: Inspection = {
    path: dbPath,
    kind,
    version: 0,
    target,
    hasMigrationsTable: false,
    issues: []
  }
  let opened: OpenedConnection
  try {
    opened = openReadOnlyDb(dbPath, { loadVector: false, verifyFts: false, logger })
  } catch (error) {
    inspection.issues.push(error instanceof Error ? error.message : String(error))
    return inspection
  }
  try {
    const state = readAppliedState(opened.db)
    inspection.hasMigrationsTable = state.hasTable
    inspection.version = state.rows.reduce((highest, row) => Math.max(highest, row.version), 0)
    if (!state.hasTable) {
      inspection.issues.push('missing schema_migrations table')
    } else if (inspection.version > target) {
      inspection.issues.push(`schema version ${inspection.version} is newer than target ${target}`)
    }
    const byVersion = new Map(migrationsFor(kind).map((migration) => [migration.version, migration]))
    for (const row of state.rows) {
      const migration = byVersion.get(row.version)
      if (migration === undefined) {
        inspection.issues.push(
          `migration ${padVersion(row.version)} (${row.name}) is recorded but missing from the build`
        )
      } else if (migration.checksum !== row.checksum) {
        inspection.issues.push(`migration ${padVersion(row.version)} checksum mismatch`)
      }
    }
    // A gap under the recorded maximum means an older build already ran past it.
    const recordedVersions = new Set(state.rows.map((row) => row.version))
    for (const migration of byVersion.values()) {
      if (migration.version <= inspection.version && !recordedVersions.has(migration.version)) {
        inspection.issues.push(
          `migration ${padVersion(migration.version)} (${migration.name}) is not recorded in this database`
        )
      }
    }
    inspection.issues.sort()
    return inspection
  } finally {
    opened.db.close()
  }
}

function padVersion(version: number): string {
  return String(version).padStart(4, '0')
}