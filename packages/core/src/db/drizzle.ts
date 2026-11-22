import { type BetterSQLite3Database, drizzle } from 'drizzle-orm/better-sqlite3'

import type { Db } from './open'
import * as schema from './schema'

export type Orm = BetterSQLite3Database<typeof schema>

/** One Drizzle instance per connection; the schema barrel is the typed source. */
export function createDrizzle(db: Db): Orm {
  return drizzle(db, { schema })
}

/**
 * Runs `fn` in a write transaction opened with `BEGIN IMMEDIATE`, as required
 * for every write path. Nesting is handled by
 * better-sqlite3's savepoints, so a service may call into another service.
 */
export function inImmediateTransaction<T>(db: Db, fn: () => T): T {
  return db.transaction(fn).immediate()
}

export { schema }