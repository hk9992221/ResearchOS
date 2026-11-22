import Database from 'better-sqlite3'
import * as sqliteVec from 'sqlite-vec'

import type { Logger as FoundationLogger } from '../logger'
import { migrateDatabase, type MigrationOptions, type MigrationResult } from './migrate'

export type DbKind = 'global' | 'project'
export type Db = Database.Database

export interface OpenConnectionOptions {
  readonly?: boolean
  fileMustExist?: boolean
  /** Load the sqlite-vec extension (default true). Failure is not fatal. */
  loadVector?: boolean
  /** Probe FTS5 with the trigram tokenizer (default true). */
  verifyFts?: boolean
  logger?: FoundationLogger
}

export interface OpenedConnection {
  db: Db
  readonly: boolean
  /** False when the platform sqlite-vec binary could not be loaded. */
  vectorAvailable: boolean
  /** False when this SQLite build lacks FTS5 or the trigram tokenizer. */
  fts5TrigramAvailable: boolean
}

export interface OpenDbOptions extends OpenConnectionOptions, MigrationOptions {}

export interface OpenedDatabase extends OpenedConnection {
  path: string
  kind: DbKind
  migrations: MigrationResult
  close(): void
}

/**
 * Opens one SQLite file and applies the per-connection pragmas of the foundation spec
 * §4.1.8: `busy_timeout=5000`, `foreign_keys=ON`, `journal_mode=WAL` and
 * `synchronous=NORMAL` for writable connections.
 */
export function openConnection(file: string, options: OpenConnectionOptions = {}): OpenedConnection {
  const readonly = options.readonly ?? false
  const db = new Database(file, {
    readonly,
    fileMustExist: options.fileMustExist ?? readonly
  })

  try {
    db.pragma('busy_timeout = 5000')
    db.pragma('foreign_keys = ON')
    if (!readonly) {
      db.pragma('journal_mode = WAL')
      db.pragma('synchronous = NORMAL')
    }
    const vectorAvailable = (options.loadVector ?? true) ? loadVectorExtension(db, options.logger) : false
    const fts5TrigramAvailable = (options.verifyFts ?? true) ? verifyFts5Trigram(db) : false
    return { db, readonly, vectorAvailable, fts5TrigramAvailable }
  } catch (error) {
    db.close()
    throw error
  }
}

/**
 * Native extensions cannot be loaded from inside `app.asar`. electron-builder
 * unpacks `sqlite-vec-*` into `app.asar.unpacked`, but `require.resolve` still
 * returns the `app.asar` path inside a packaged app, so rewrite it.
 */
export function unpackedExtensionPath(value: string): string {
  return value.replace(/([\\/])app\.asar([\\/])/, '$1app.asar.unpacked$2')
}

/**
 * Loads sqlite-vec. The extension is an optional capability: a missing or
 * unloadable binary leaves `vectorAvailable=false` and keyword search keeps
 * working (decision §8.3), so startup never fails for it.
 */
export function loadVectorExtension(db: Db, logger?: FoundationLogger): boolean {
  try {
    db.loadExtension(unpackedExtensionPath(sqliteVec.getLoadablePath()))
    return true
  } catch (error) {
    logger?.warn('sqlite-vec unavailable; vector search degraded', {
      reason: error instanceof Error ? error.message : String(error),
      status: 'vector_unavailable'
    })
    return false
  }
}

/**
 * Verifies FTS5 plus the trigram tokenizer by building and dropping a temp
 * virtual table. A build without them cannot satisfy the search contract.
 */
export function verifyFts5Trigram(db: Db): boolean {
  try {
    db.exec("CREATE VIRTUAL TABLE temp.fts5_trigram_probe USING fts5(body, tokenize='trigram')")
    db.exec('DROP TABLE temp.fts5_trigram_probe')
    return true
  } catch {
    return false
  }
}

/**
 * Opens and migrates a database. This is the composition used by the workspace
 * manager for global.db and every project.db.
 */
export function openDb(file: string, kind: DbKind, options: OpenDbOptions = {}): OpenedDatabase {
  const opened = openConnection(file, options)
  try {
    const migrations = migrateDatabase(opened.db, kind, file, options)
    return {
      ...opened,
      path: file,
      kind,
      migrations,
      close: () => opened.db.close()
    }
  } catch (error) {
    opened.db.close()
    throw error
  }
}

/** Opens an existing database read-only, without write pragmas or migrations. */
export function openReadOnlyDb(file: string, options: OpenConnectionOptions = {}): OpenedConnection {
  return openConnection(file, { ...options, readonly: true, fileMustExist: true })
}