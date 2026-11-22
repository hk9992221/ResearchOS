import { SQLiteSyncDialect, getTableConfig } from 'drizzle-orm/sqlite-core'

import type { DbKind } from './migrate'
import type { Db } from './open'
import { TABLES_BY_KIND, VIEWS, VIRTUAL_TABLES } from './schema'

interface DrizzleColumnShape {
  name: string
  notNull: boolean
  primary: boolean
  isUnique: boolean
  hasDefault: boolean
  default: unknown
  columnType: string
}

interface DrizzleTableConfig {
  name: string
  columns: DrizzleColumnShape[]
  indexes: Array<{
    config: { name: string; unique: boolean; columns: unknown[]; where: unknown | undefined }
  }>
  foreignKeys: Array<{
    reference: () => {
      columns: DrizzleColumnShape[]
      foreignColumns: DrizzleColumnShape[]
      foreignTable: unknown
    }
    onDelete: string | undefined
  }>
  checks: Array<{ value: unknown }>
  primaryKeys: Array<{ columns: DrizzleColumnShape[] }>
  uniqueConstraints: Array<{ columns: DrizzleColumnShape[] }>
}

function tableConfig(table: unknown): DrizzleTableConfig {
  return getTableConfig(table as never) as unknown as DrizzleTableConfig
}

/**
 * Compares the physical schema produced by the verbatim Go migrations with the
 * hand-maintained Drizzle definitions. Any difference is a drift string; an
 * empty array means parity. This is the check behind
 * `scripts/check-schema-parity.mjs`.
 */

interface TableInfoRow {
  name: string
  type: string
  notnull: number
  dflt_value: string | null
  pk: number
}

interface IndexListRow {
  name: string
  unique: number
  origin: string
  partial: number
}

interface IndexInfoRow {
  seqno: number
  cid: number
  name: string | null
}

interface ForeignKeyRow {
  id: number
  seq: number
  table: string
  from: string
  to: string
  on_delete: string
}

interface MasterRow {
  type: string
  name: string
  sql: string | null
}

const dialect = new SQLiteSyncDialect()

/** Whitespace-, quote- and case-insensitive form used for expression comparison. */
export function normalizeSql(sql: string): string {
  return sql.replaceAll('"', '').replace(/\s+/g, '').toLowerCase()
}

function serialize(sql: unknown): string {
  // Drizzle SQL objects expose the dialect query builder through the class.
  return dialect.sqlToQuery(sql as Parameters<SQLiteSyncDialect['sqlToQuery']>[0]).sql
}

function splitTopLevel(list: string): string[] {
  const parts: string[] = []
  let depth = 0
  let quote: string | null = null
  let current = ''
  for (const character of list) {
    if (quote !== null) {
      current += character
      if (character === quote) quote = null
      continue
    }
    if (character === "'" || character === '"' || character === '`') {
      quote = character
      current += character
      continue
    }
    if (character === '(') depth += 1
    if (character === ')') depth -= 1
    if (character === ',' && depth === 0) {
      parts.push(current.trim())
      current = ''
      continue
    }
    current += character
  }
  if (current.trim() !== '') parts.push(current.trim())
  return parts
}

function matchingParen(text: string, openIndex: number): number {
  let depth = 0
  let quote: string | null = null
  for (let index = openIndex; index < text.length; index += 1) {
    const character = text[index]!
    if (quote !== null) {
      if (character === quote) quote = null
      continue
    }
    if (character === "'" || character === '"') {
      quote = character
      continue
    }
    if (character === '(') depth += 1
    if (character === ')') {
      depth -= 1
      if (depth === 0) return index
    }
  }
  throw new Error('schema parity: unbalanced parentheses')
}

function checkExpressions(tableSql: string): string[] {
  const expressions: string[] = []
  const pattern = /\bCHECK\s*\(/gi
  for (let match = pattern.exec(tableSql); match !== null; match = pattern.exec(tableSql)) {
    const open = tableSql.indexOf('(', match.index)
    const close = matchingParen(tableSql, open)
    expressions.push(normalizeSql(tableSql.slice(open + 1, close)))
    pattern.lastIndex = close
  }
  return expressions.sort()
}

interface IndexParts {
  columns: string
  where: string | undefined
}

function indexParts(sql: string): IndexParts {
  const on = /\bON\b/i.exec(sql)
  if (on === null) throw new Error(`schema parity: cannot parse index SQL ${sql}`)
  const open = sql.indexOf('(', on.index)
  const close = matchingParen(sql, open)
  const columns = splitTopLevel(sql.slice(open + 1, close))
    .map((column) => normalizeSql(column).replace(/(?:asc|desc)$/, ''))
    .join(',')
  const rest = sql.slice(close + 1)
  const where = /\bWHERE\b/i.exec(rest)
  return { columns, where: where === null ? undefined : normalizeSql(rest.slice(where.index + 5)) }
}

function declaredType(columnType: string): string {
  switch (columnType) {
    case 'SQLiteText':
      return 'TEXT'
    case 'SQLiteInteger':
    case 'SQLiteBoolean':
      return 'INTEGER'
    case 'SQLiteReal':
      return 'REAL'
    default:
      return columnType.startsWith('SQLiteBlob') ? 'BLOB' : columnType.toUpperCase()
  }
}

function normalizeDefault(value: string | null): string | null {
  if (value === null) return null
  const trimmed = value.trim()
  if (trimmed.startsWith("'") && trimmed.endsWith("'")) return trimmed.slice(1, -1)
  return trimmed
}

export function compareSchema(db: Db, kind: DbKind): string[] {
  const drift: string[] = []
  const master = db.prepare('SELECT type, name, sql FROM sqlite_master ORDER BY name').all() as MasterRow[]
  const expectedTables = TABLES_BY_KIND[kind]
  const expectedVirtual = VIRTUAL_TABLES.filter((virtual) => virtual.kind === kind)
  const expectedViews = VIEWS.filter((view) => view.kind === kind)

  const physicalTables = master.filter(
    (row) => row.type === 'table' && !row.name.startsWith('sqlite_')
  )
  const virtualNames = physicalTables
    .filter((row) => /CREATE\s+VIRTUAL\s+TABLE/i.test(row.sql ?? ''))
    .map((row) => row.name)
  const isShadow = (name: string): boolean =>
    virtualNames.some((virtual) => name === virtual || name.startsWith(`${virtual}_`))
  const regular = physicalTables.filter((row) => !isShadow(row.name))
  const virtual = physicalTables.filter((row) => isShadow(row.name) && virtualNames.includes(row.name))

  const expectedNames = expectedTables.map((table) => tableConfig(table).name).sort()
  const actualNames = regular.map((row) => row.name).sort()
  if (expectedNames.join(',') !== actualNames.join(',')) {
    drift.push(`${kind}: table list ${actualNames.join(',')} != ${expectedNames.join(',')}`)
  }

  for (const table of expectedTables) {
    const config = tableConfig(table)
    const tableSql = master.find((row) => row.type === 'table' && row.name === config.name)?.sql ?? ''
    const actualColumns = db.prepare(`PRAGMA table_info(${quoteIdentifier(config.name)})`).all() as TableInfoRow[]
    if (actualColumns.length === 0) {
      drift.push(`${config.name}: table is missing`)
      continue
    }
    const actualByName = new Map(actualColumns.map((column) => [column.name, column]))

    for (const column of config.columns) {
      const actual = actualByName.get(column.name)
      if (actual === undefined) {
        drift.push(`${config.name}.${column.name}: column is missing`)
        continue
      }
      if (actual.type.toUpperCase() !== declaredType(column.columnType)) {
        drift.push(`${config.name}.${column.name}: type ${actual.type} != ${declaredType(column.columnType)}`)
      }
      if ((actual.notnull === 1) !== (column.notNull || column.primary)) {
        drift.push(`${config.name}.${column.name}: notNull ${actual.notnull} != ${column.notNull}`)
      }
      const expectedDefault =
        column.hasDefault && column.default !== undefined
          ? normalizeDefault(
              typeof column.default === 'object' && column.default !== null
                ? serialize(column.default)
                : String(column.default)
            )
          : null
      if (normalizeDefault(actual.dflt_value) !== expectedDefault) {
        drift.push(
          `${config.name}.${column.name}: default ${String(actual.dflt_value)} != ${String(expectedDefault)}`
        )
      }
    }
    if (actualColumns.length !== config.columns.length) {
      drift.push(`${config.name}: ${actualColumns.length} columns != ${config.columns.length}`)
    }

    const primaryColumns = expectedPrimaryColumns(config)
    for (const actual of actualColumns) {
      const expectedOrdinal = primaryColumns.get(actual.name) ?? 0
      if (actual.pk !== expectedOrdinal) {
        drift.push(`${config.name}.${actual.name}: primary key position ${actual.pk} != ${expectedOrdinal}`)
      }
    }

    drift.push(...compareIndexes(db, config.name, config))
    drift.push(...compareForeignKeys(db, config.name, config))
    drift.push(...compareChecks(config.name, tableSql, config.checks.map((check) => normalizeSql(serialize(check.value)))))
  }

  for (const expected of expectedVirtual) {
    const row = virtual.find((entry) => entry.name === expected.name)
    if (row === undefined) {
      drift.push(`${kind}: virtual table ${expected.name} is missing`)
      continue
    }
    const sql = row.sql ?? ''
    const using = /USING\s+fts5\s*\(/i.exec(sql)
    if (using === null) {
      drift.push(`${expected.name}: not an fts5 table`)
      continue
    }
    const open = sql.indexOf('(', using.index)
    const close = matchingParen(sql, open)
    const parts = splitTopLevel(sql.slice(open + 1, close))
    const columns = parts
      .filter((part) => !/^tokenize\s*=/i.test(part))
      .map((part) => part.split(/\s+/)[0] ?? '')
    const unindexed = parts
      .filter((part) => /\bUNINDEXED\b/i.test(part))
      .map((part) => part.split(/\s+/)[0] ?? '')
    const tokenize = /tokenize\s*=\s*'([^']+)'/i.exec(sql)?.[1]
    if (columns.join(',') !== expected.columns.join(',')) {
      drift.push(`${expected.name}: columns ${columns.join(',')} != ${expected.columns.join(',')}`)
    }
    if (unindexed.join(',') !== expected.unindexed.join(',')) {
      drift.push(`${expected.name}: unindexed ${unindexed.join(',')} != ${expected.unindexed.join(',')}`)
    }
    if (tokenize !== expected.tokenize) {
      drift.push(`${expected.name}: tokenizer ${String(tokenize)} != ${expected.tokenize}`)
    }
  }

  const actualViews = master.filter((row) => row.type === 'view')
  const expectedViewNames = expectedViews.map((view) => view.name).sort()
  if (actualViews.map((view) => view.name).sort().join(',') !== expectedViewNames.join(',')) {
    drift.push(`${kind}: view list mismatch`)
  }
  for (const view of expectedViews) {
    const columns = db.prepare(`PRAGMA table_info(${quoteIdentifier(view.name)})`).all() as TableInfoRow[]
    if (columns.map((column) => column.name).join(',') !== view.columns.join(',')) {
      drift.push(`${view.name}: view columns mismatch`)
    }
  }
  return drift
}

function expectedPrimaryColumns(config: DrizzleTableConfig): Map<string, number> {
  const positions = new Map<string, number>()
  for (const primaryKey of config.primaryKeys) {
    primaryKey.columns.forEach((column, index) => positions.set(column.name, index + 1))
  }
  if (config.primaryKeys.length === 0) {
    for (const column of config.columns) if (column.primary) positions.set(column.name, 1)
  }
  return positions
}

function compareIndexes(
  db: Db,
  tableName: string,
  config: DrizzleTableConfig
): string[] {
  const drift: string[] = []
  const actual = db.prepare(`PRAGMA index_list(${quoteIdentifier(tableName)})`).all() as IndexListRow[]
  const actualExplicit = new Map<string, IndexListRow>()
  const actualAutomatic: string[][] = []
  for (const index of actual) {
    if (index.origin === 'pk' || index.origin === 'u') {
      const columns = db.prepare(`PRAGMA index_info(${quoteIdentifier(index.name)})`).all() as IndexInfoRow[]
      actualAutomatic.push(columns.map((column) => column.name ?? '').sort())
    } else {
      actualExplicit.set(index.name, index)
    }
  }

  const expectedNames = new Set(config.indexes.map((index) => index.config.name))
  for (const name of actualExplicit.keys()) {
    if (!expectedNames.has(name)) drift.push(`${tableName}: unexpected index ${name}`)
  }
  for (const index of config.indexes) {
    const row = actualExplicit.get(index.config.name)
    if (row === undefined) {
      drift.push(`${tableName}: index ${index.config.name} is missing`)
      continue
    }
    const parts = indexParts(findIndexSql(db, index.config.name) ?? '')
    const expectedColumns = index.config.columns
      .map((column) => {
        const candidate = column as { name?: string }
        return normalizeSql(candidate.name ?? serialize(column))
      })
      .join(',')
    if (parts.columns !== expectedColumns) {
      drift.push(`${tableName}: index ${index.config.name} columns ${parts.columns} != ${expectedColumns}`)
    }
    if ((row.unique === 1) !== index.config.unique) {
      drift.push(`${tableName}: index ${index.config.name} uniqueness mismatch`)
    }
    const expectedWhere = index.config.where === undefined ? undefined : normalizeSql(serialize(index.config.where))
    if (parts.where !== expectedWhere) {
      drift.push(`${tableName}: index ${index.config.name} predicate ${String(parts.where)} != ${String(expectedWhere)}`)
    }
    if ((row.partial === 1) !== (expectedWhere !== undefined)) {
      drift.push(`${tableName}: index ${index.config.name} partial flag mismatch`)
    }
  }

  const expectedAutomatic: string[][] = []
  for (const column of config.columns) {
    if (column.isUnique) expectedAutomatic.push([column.name])
    if (column.primary && config.primaryKeys.length === 0 && column.columnType !== 'SQLiteInteger') {
      expectedAutomatic.push([column.name])
    }
  }
  for (const primaryKey of config.primaryKeys) {
    expectedAutomatic.push(primaryKey.columns.map((column) => column.name).sort())
  }
  for (const unique of config.uniqueConstraints) {
    expectedAutomatic.push(unique.columns.map((column) => column.name).sort())
  }
  const shape = (sets: string[][]): string => sets.map((set) => set.join('|')).sort().join(';')
  if (shape(actualAutomatic) !== shape(expectedAutomatic)) {
    drift.push(`${tableName}: unique constraints ${shape(actualAutomatic)} != ${shape(expectedAutomatic)}`)
  }
  return drift
}

function findIndexSql(db: Db, name: string): string | undefined {
  const row = db.prepare("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?").get(name) as
    | { sql: string | null }
    | undefined
  return row?.sql ?? undefined
}

function compareForeignKeys(db: Db, tableName: string, config: DrizzleTableConfig): string[] {
  const drift: string[] = []
  const actual = db
    .prepare(`PRAGMA foreign_key_list(${quoteIdentifier(tableName)})`)
    .all() as ForeignKeyRow[]
  const actualKeys = actual.map(
    (row) => `${row.table}.${row.from}->${row.to}:${row.on_delete.toLowerCase()}`
  )
  const expectedKeys: string[] = []
  for (const foreignKey of config.foreignKeys) {
    const reference = foreignKey.reference()
    const foreignTable = tableConfig(reference.foreignTable).name
    reference.columns.forEach((column, index) => {
      const foreignColumn = reference.foreignColumns[index]
      expectedKeys.push(
        `${foreignTable}.${column.name}->${foreignColumn?.name ?? ''}:${foreignKey.onDelete ?? 'no action'}`
      )
    })
  }
  if (actualKeys.sort().join(',') !== expectedKeys.sort().join(',')) {
    drift.push(`${tableName}: foreign keys ${actualKeys.join(',')} != ${expectedKeys.join(',')}`)
  }
  return drift
}

function compareChecks(tableName: string, tableSql: string, expected: string[]): string[] {
  const actual = checkExpressions(tableSql)
  const sortedExpected = [...expected].sort()
  if (actual.join('|') !== sortedExpected.join('|')) {
    return [`${tableName}: checks ${actual.join('|')} != ${sortedExpected.join('|')}`]
  }
  return []
}

function quoteIdentifier(name: string): string {
  return `"${name.replaceAll('"', '""')}"`
}