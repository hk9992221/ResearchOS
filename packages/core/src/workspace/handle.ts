import type Database from 'better-sqlite3'

import { createDrizzle, inImmediateTransaction, type Orm } from '../db/drizzle'
import type { Paths } from '../paths'

export type Db = Database.Database

/** Handle of the workspace global database. */
export class GlobalHandle {
  readonly db: Db
  readonly orm: Orm

  constructor(db: Db) {
    this.db = db
    this.orm = createDrizzle(db)
  }

  /** Runs `fn` in a BEGIN IMMEDIATE transaction; an error rolls back. */
  inTx<T>(fn: () => T): T {
    return inImmediateTransaction(this.db, fn)
  }

  close(): void {
    this.db.close()
  }
}

/**
 * Handle of one project database, handed to module services. Every write goes
 * through `inTx`, which opens the transaction immediately so two writers cannot
 * both start from a stale snapshot.
 */
export class ProjectHandle {
  readonly id: string
  readonly db: Db
  readonly orm: Orm
  readonly paths: Paths

  constructor(id: string, db: Db, paths: Paths) {
    this.id = id
    this.db = db
    this.orm = createDrizzle(db)
    this.paths = paths
  }

  inTx<T>(fn: () => T): T {
    return inImmediateTransaction(this.db, fn)
  }

  close(): void {
    this.db.close()
  }
}