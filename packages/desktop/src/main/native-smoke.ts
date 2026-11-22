import { performance } from 'node:perf_hooks'

import Database from 'better-sqlite3'
import { sql } from 'drizzle-orm'
import { drizzle } from 'drizzle-orm/better-sqlite3'
import { integer, sqliteTable, text } from 'drizzle-orm/sqlite-core'
import * as sqliteVec from 'sqlite-vec'

import type { DbSmokeResult } from '@researchos/shared'

const smokeItems = sqliteTable('smoke_items', {
  id: integer('id').primaryKey({ autoIncrement: true }),
  name: text('name').notNull()
})

type NearestRow = { rowid: number; distance: number }

function float32Blob(values: number[]): Buffer {
  return Buffer.from(new Float32Array(values).buffer)
}

/**
 * Startup self-check for the main process: better-sqlite3, FTS5, the
 * sqlite-vec loadable extension, vec0 KNN and a drizzle round-trip. The result
 * crosses IPC so the renderer can display the live native stack status.
 */
export function runNativeSmoke(databasePath = ':memory:'): DbSmokeResult {
  const startedAt = performance.now()
  const db = new Database(databasePath)

  try {
    db.pragma('busy_timeout = 5000')
    db.pragma('foreign_keys = ON')
    db.loadExtension(sqliteVec.getLoadablePath())

    db.exec('CREATE VIRTUAL TABLE smoke_fts USING fts5(body)')
    db.prepare('INSERT INTO smoke_fts(body) VALUES (?)').run('native sqlite stack works')

    const ftsRow: unknown = db
      .prepare("SELECT body FROM smoke_fts WHERE smoke_fts MATCH 'native' LIMIT 1")
      .pluck()
      .get()
    const fts5Match = typeof ftsRow === 'string' ? ftsRow : ''

    const vecVersionRow: unknown = db.prepare('SELECT vec_version()').pluck().get()
    const vecVersion = typeof vecVersionRow === 'string' ? vecVersionRow : ''

    db.exec('CREATE VIRTUAL TABLE smoke_vec USING vec0(embedding float[4])')
    db.prepare('INSERT INTO smoke_vec(rowid, embedding) VALUES (?, ?)').run(1n, float32Blob([1, 0, 0, 0]))
    db.prepare('INSERT INTO smoke_vec(rowid, embedding) VALUES (?, ?)').run(2n, float32Blob([0, 1, 0, 0]))

    // better-sqlite3 returns untyped rows; vec0's projection is fixed by the query.
    const ranked = db
      .prepare('SELECT rowid, distance FROM smoke_vec WHERE embedding MATCH ? ORDER BY distance LIMIT 1')
      .all(float32Blob([1, 0, 0, 0])) as NearestRow[]
    const nearest = ranked[0]

    const orm = drizzle(db)
    orm.run(sql`CREATE TABLE smoke_items (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL)`)
    orm.insert(smokeItems).values({ name: 'alpha' }).run()
    orm.insert(smokeItems).values({ name: 'beta' }).run()
    const rows = orm.select().from(smokeItems).all()

    const ok =
      fts5Match === 'native sqlite stack works' &&
      vecVersion.length > 0 &&
      nearest?.rowid === 1 &&
      nearest.distance < 1e-6 &&
      rows.length === 2

    return {
      ok,
      databasePath,
      fts5Match,
      vecVersion,
      vecNearestRowId: nearest?.rowid ?? -1,
      vecDistance: nearest?.distance ?? Number.NaN,
      drizzleRows: rows.length,
      durationMs: Math.round((performance.now() - startedAt) * 100) / 100
    }
  } finally {
    db.close()
  }
}