import type { FilterNode, SortKey } from '@researchos/shared'

import { isUuid } from '@researchos/shared'
import type { DbFieldType } from '@researchos/shared'
import { invalid, type Json } from './support'
import { parseFieldConfig } from './database'
import type { FieldRow } from './database'

export const MAX_FILTER_DEPTH = 3
export const MAX_FILTER_LEAVES = 30

type Operator =
  | 'eq'
  | 'neq'
  | 'contains'
  | 'startsWith'
  | 'gt'
  | 'gte'
  | 'lt'
  | 'lte'
  | 'isEmpty'
  | 'isNotEmpty'
  | 'between'
  | 'isAnyOf'
  | 'hasAny'
  | 'hasAll'
  | 'hasNone'
  | 'isTrue'
  | 'isFalse'

const OPERATOR_ALIASES: Readonly<Record<string, Operator>> = {
  eq: 'eq',
  neq: 'neq',
  contains: 'contains',
  starts_with: 'startsWith',
  startsWith: 'startsWith',
  gt: 'gt',
  gte: 'gte',
  lt: 'lt',
  lte: 'lte',
  is_empty: 'isEmpty',
  isEmpty: 'isEmpty',
  is_not_empty: 'isNotEmpty',
  isNotEmpty: 'isNotEmpty',
  between: 'between',
  is: 'eq',
  is_not: 'neq',
  is_any_of: 'isAnyOf',
  isAnyOf: 'isAnyOf',
  has_any: 'hasAny',
  hasAny: 'hasAny',
  has_all: 'hasAll',
  hasAll: 'hasAll',
  has_none: 'hasNone',
  hasNone: 'hasNone',
  is_true: 'isTrue',
  isTrue: 'isTrue',
  is_false: 'isFalse',
  isFalse: 'isFalse'
}

const FIELD_OPERATORS: Readonly<Record<DbFieldType, readonly Operator[]>> = {
  text: ['eq', 'neq', 'contains', 'startsWith', 'isEmpty', 'isNotEmpty'],
  url: ['eq', 'neq', 'contains', 'startsWith', 'isEmpty', 'isNotEmpty'],
  number: ['eq', 'neq', 'gt', 'gte', 'lt', 'lte', 'between', 'isEmpty', 'isNotEmpty'],
  date: ['between', 'gt', 'gte', 'lt', 'lte', 'isEmpty', 'isNotEmpty'],
  select: ['eq', 'neq', 'isAnyOf', 'isEmpty', 'isNotEmpty'],
  multi_select: ['hasAny', 'hasAll', 'hasNone', 'isEmpty'],
  tags: ['hasAny', 'hasAll', 'hasNone', 'isEmpty'],
  checkbox: ['isTrue', 'isFalse'],
  relation: ['hasAny', 'hasNone', 'isEmpty']
}

interface Leaf {
  kind: 'leaf'
  fieldId: string
  type: DbFieldType
  operator: Operator
  value?: unknown
  optionIds: string[]
}

interface Group {
  kind: 'group'
  op: 'and' | 'or'
  children: Predicate[]
}

type Predicate = Leaf | Group

interface RawNode {
  kind?: string
  op?: string
  operator?: string
  fieldId?: string
  field_id?: string
  field?: string
  value?: unknown
  children?: unknown[]
}

function rawNode(node: unknown): RawNode {
  if (node === null || typeof node !== 'object' || Array.isArray(node)) {
    throw invalid('filter node must be an object')
  }
  return node as RawNode
}

function childNodes(node: RawNode): RawNode[] {
  return Array.isArray(node.children) ? node.children.map(rawNode) : []
}

function checkDepth(node: RawNode, depth: number): void {
  if (depth > MAX_FILTER_DEPTH) {
    throw invalid(`filter nesting depth ${depth} exceeds the maximum of ${MAX_FILTER_DEPTH}`)
  }
  for (const child of childNodes(node)) checkDepth(child, depth + 1)
}

function countLeaves(node: RawNode): number {
  const children = childNodes(node)
  if (children.length === 0) return 1
  return children.reduce((total, child) => total + countLeaves(child), 0)
}

export function compileFilter(
  fields: ReadonlyMap<string, FieldRow>,
  filter: FilterNode | undefined
): Predicate | null {
  if (filter === undefined || filter === null) return null
  const root = rawNode(filter)
  checkDepth(root, 1)
  const leaves = countLeaves(root)
  if (leaves > MAX_FILTER_LEAVES) {
    throw invalid(`filter has ${leaves} leaves, at most ${MAX_FILTER_LEAVES} are allowed`)
  }
  const compiled = compileNode(root, fields, true)
  return compiled === null ? null : compiled.predicate
}

function compileNode(
  node: RawNode,
  fields: ReadonlyMap<string, FieldRow>,
  root: boolean
): { predicate: Predicate } | null {
  const children = childNodes(node)
  const fieldId = node.fieldId ?? node.field_id ?? node.field
  if (children.length === 0) {
    if (fieldId === undefined || fieldId === '') {
      if (root) throw invalid('filter is empty')
      throw invalid('filter leaf has no field id')
    }
    if (node.op !== undefined && node.op !== 'and' && node.op !== 'or') {
      throw invalid(`filter leaf ${JSON.stringify(fieldId)} must not carry a logic operator`)
    }
    const field = fields.get(fieldId)
    if (field === undefined) return null // unknown/soft-deleted field: ignored
    return { predicate: compileLeaf(field, node) }
  }
  if (fieldId !== undefined) throw invalid('filter group must not carry a field id')
  const logic = node.op ?? node.kind
  if (logic !== 'and' && logic !== 'or') throw invalid(`filter group has invalid logic operator ${JSON.stringify(logic)}`)
  const group: Group = { kind: 'group', op: logic, children: [] }
  for (const child of children) {
    const compiled = compileNode(child, fields, false)
    if (compiled !== null) group.children.push(compiled.predicate)
  }
  if (group.children.length === 0) return null
  return { predicate: group }
}

function compileLeaf(field: FieldRow, node: RawNode): Leaf {
  const rawOperator = node.operator ?? (node.op !== 'and' && node.op !== 'or' ? node.op : undefined)
  if (rawOperator === undefined || rawOperator === '') {
    throw invalid(`filter on field ${JSON.stringify(field.name)} has no operator`)
  }
  const operator = OPERATOR_ALIASES[rawOperator]
  if (operator === undefined) throw invalid(`unknown filter operator ${JSON.stringify(rawOperator)}`)
  if (!FIELD_OPERATORS[field.type].includes(operator)) {
    throw invalid(`operator ${JSON.stringify(rawOperator)} is not allowed for field type ${JSON.stringify(field.type)}`)
  }
  const config = parseFieldConfig(field.config)
  const leaf: Leaf = {
    kind: 'leaf',
    fieldId: field.id,
    type: field.type,
    operator,
    value: node.value,
    optionIds: (config.options ?? []).map((option) => option.id)
  }
  validateLeafValue(leaf, field)
  return leaf
}

function asString(value: unknown, field: FieldRow): string {
  if (typeof value !== 'string') throw invalid(`field ${JSON.stringify(field.name)} expects a string filter value`)
  return value
}

function asNumber(value: unknown, field: FieldRow): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    throw invalid(`field ${JSON.stringify(field.name)} expects a numeric filter value`)
  }
  return value
}

function asStringArray(value: unknown, field: FieldRow): string[] {
  if (!Array.isArray(value) || value.length === 0 || !value.every((entry) => typeof entry === 'string' && entry !== '')) {
    throw invalid(`field ${JSON.stringify(field.name)} expects a non-empty array of strings`)
  }
  return value as string[]
}

function asDate(value: unknown, field: FieldRow): string {
  const text = asString(value, field)
  if (!/^\d{4}-\d{2}-\d{2}$/u.test(text) || Number.isNaN(Date.parse(`${text}T00:00:00Z`))) {
    throw invalid(`field ${JSON.stringify(field.name)} expects a YYYY-MM-DD date`)
  }
  return text
}

interface RelationRef {
  type: string
  id: string
}

function asRelationRefs(value: unknown, field: FieldRow): RelationRef[] {
  if (!Array.isArray(value) || value.length === 0) {
    throw invalid(`field ${JSON.stringify(field.name)} expects a non-empty array of {type,id} targets`)
  }
  return value.map((entry) => {
    if (entry === null || typeof entry !== 'object' || Array.isArray(entry)) {
      throw invalid('relation filter values must be {type,id} objects')
    }
    const record = entry as Json
    if (typeof record.type !== 'string' || typeof record.id !== 'string' || !isUuid(record.id)) {
      throw invalid('relation filter values must be {type,id} objects with a UUID id')
    }
    return { type: record.type, id: record.id }
  })
}

function validateLeafValue(leaf: Leaf, field: FieldRow): void {
  switch (leaf.operator) {
    case 'isEmpty':
    case 'isNotEmpty':
    case 'isTrue':
    case 'isFalse':
      return
    case 'between':
      if (!Array.isArray(leaf.value) || leaf.value.length !== 2) {
        throw invalid(`operator between on field ${JSON.stringify(field.name)} needs [low, high]`)
      }
      if (leaf.type === 'number') {
        leaf.value = [asNumber(leaf.value[0], field), asNumber(leaf.value[1], field)]
      } else if (leaf.type === 'date') {
        leaf.value = [asDate(leaf.value[0], field), asDate(leaf.value[1], field)]
      }
      return
    case 'isAnyOf':
    case 'hasAny':
    case 'hasAll':
    case 'hasNone':
      if (leaf.type === 'relation') {
        leaf.value = asRelationRefs(leaf.value, field)
      } else {
        leaf.value = asStringArray(leaf.value, field)
      }
      return
    default:
      break
  }
  switch (leaf.type) {
    case 'number':
      leaf.value = asNumber(leaf.value, field)
      return
    case 'date':
      leaf.value = asDate(leaf.value, field)
      return
    case 'checkbox':
      return
    default:
      leaf.value = asString(leaf.value, field)
  }
}

function valueOf(values: Readonly<Record<string, unknown>>, fieldId: string): unknown {
  if (!(fieldId in values)) return undefined
  return values[fieldId]
}

function matchEmpty(leaf: Leaf): boolean {
  switch (leaf.operator) {
    case 'isEmpty':
      return true
    case 'isNotEmpty':
    case 'isTrue':
      return false
    case 'neq':
    case 'hasNone':
    case 'isFalse':
      return true
    default:
      return false
  }
}

function fold(value: string): string {
  return value.toLowerCase()
}

function intersects(have: readonly string[], want: readonly string[]): boolean {
  return want.some((entry) => have.includes(entry))
}

function refsIntersect(have: readonly RelationRef[], want: readonly RelationRef[]): boolean {
  return want.some((wanted) => have.some((entry) => entry.type === wanted.type && entry.id === wanted.id))
}

function matchLeaf(leaf: Leaf, values: Readonly<Record<string, unknown>>): boolean {
  const raw = valueOf(values, leaf.fieldId)
  if (raw === null || raw === undefined) return matchEmpty(leaf)
  switch (leaf.type) {
    case 'text':
    case 'url': {
      if (typeof raw !== 'string') return matchEmpty(leaf)
      const argument = typeof leaf.value === 'string' ? leaf.value : ''
      switch (leaf.operator) {
        case 'eq':
          return raw.toLowerCase() === argument.toLowerCase()
        case 'neq':
          return raw.toLowerCase() !== argument.toLowerCase()
        case 'contains':
          return fold(raw).includes(fold(argument))
        case 'startsWith':
          return fold(raw).startsWith(fold(argument))
        case 'isEmpty':
          return raw === ''
        case 'isNotEmpty':
          return raw !== ''
        default:
          return false
      }
    }
    case 'number': {
      if (typeof raw !== 'number') return matchEmpty(leaf)
      const argument = leaf.value as number
      switch (leaf.operator) {
        case 'eq':
          return raw === argument
        case 'neq':
          return raw !== argument
        case 'gt':
          return raw > argument
        case 'gte':
          return raw >= argument
        case 'lt':
          return raw < argument
        case 'lte':
          return raw <= argument
        case 'between': {
          const [low, high] = leaf.value as [number, number]
          return raw >= low && raw <= high
        }
        default:
          return matchEmpty(leaf)
      }
    }
    case 'date': {
      if (typeof raw !== 'string' || !/^\d{4}-\d{2}-\d{2}$/u.test(raw)) return matchEmpty(leaf)
      switch (leaf.operator) {
        case 'gt':
          return raw > (leaf.value as string)
        case 'gte':
          return raw >= (leaf.value as string)
        case 'lt':
          return raw < (leaf.value as string)
        case 'lte':
          return raw <= (leaf.value as string)
        case 'between': {
          const [low, high] = leaf.value as [string, string]
          return raw >= low && raw <= high
        }
        default:
          return matchEmpty(leaf)
      }
    }
    case 'select': {
      if (typeof raw !== 'string' || raw === '') return matchEmpty(leaf)
      if (leaf.operator === 'eq') return raw === leaf.value
      if (leaf.operator === 'neq') return raw !== leaf.value
      if (leaf.operator === 'isAnyOf') return (leaf.value as string[]).includes(raw)
      return false
    }
    case 'multi_select':
    case 'tags': {
      if (!Array.isArray(raw) || !raw.every((entry) => typeof entry === 'string')) return matchEmpty(leaf)
      const have = raw as string[]
      if (leaf.operator === 'isEmpty') return have.length === 0
      const want = leaf.value as string[]
      if (leaf.operator === 'hasAny') return intersects(have, want)
      if (leaf.operator === 'hasAll') return want.every((entry) => have.includes(entry))
      if (leaf.operator === 'hasNone') return !intersects(have, want)
      return false
    }
    case 'checkbox': {
      if (typeof raw !== 'boolean') return matchEmpty(leaf)
      return leaf.operator === 'isTrue' ? raw : !raw
    }
    case 'relation': {
      if (!Array.isArray(raw)) return matchEmpty(leaf)
      const have: RelationRef[] = []
      for (const entry of raw) {
        if (entry === null || typeof entry !== 'object' || Array.isArray(entry)) continue
        const type = 'type' in entry ? entry.type : undefined
        const id = 'id' in entry ? entry.id : undefined
        if (typeof type === 'string' && typeof id === 'string') have.push({ type, id })
      }
      if (leaf.operator === 'isEmpty') return have.length === 0
      const want = leaf.value as RelationRef[]
      if (leaf.operator === 'hasAny') return refsIntersect(have, want)
      if (leaf.operator === 'hasNone') return !refsIntersect(have, want)
      return false
    }
    default:
      return false
  }
}

export function matchesFilter(
  filter: Predicate | null,
  values: Readonly<Record<string, unknown>>
): boolean {
  if (filter === null) return true
  if (filter.kind === 'group') {
    return filter.op === 'and'
      ? filter.children.every((child) => matchesFilter(child, values))
      : filter.children.some((child) => matchesFilter(child, values))
  }
  return matchLeaf(filter, values)
}

/* --------------------------------------------------------------- sorting */

export interface SortRow {
  id: string
  title: string
  createdAt: string
  updatedAt: string
  values: Readonly<Record<string, unknown>>
}

interface SortSpec {
  builtin?: string
  fieldId?: string
  type?: DbFieldType
  desc: boolean
}

export const BUILTIN_SORTS = ['__title__', '__created_at__', '__updated_at__'] as const

export function compileSorts(
  fields: ReadonlyMap<string, FieldRow>,
  sorts: SortKey[] | undefined
): SortSpec[] {
  const compiled: SortSpec[] = []
  for (const raw of sorts ?? []) {
    const key = raw as unknown as Json
    const fieldId = typeof key.field === 'string' ? key.field : typeof key.field_id === 'string' ? key.field_id : typeof key.fieldId === 'string' ? key.fieldId : ''
    if (fieldId === '') continue
    const desc = typeof key.desc === 'boolean' ? key.desc : key.direction === 'desc'
    if ((BUILTIN_SORTS as readonly string[]).includes(fieldId)) {
      compiled.push({ builtin: fieldId, desc })
      continue
    }
    const field = fields.get(fieldId)
    if (field === undefined) continue
    compiled.push({ fieldId, type: field.type, desc })
  }
  return compiled
}

interface SortValue {
  kind: 'string' | 'number' | 'boolean'
  value: string | number | boolean
}

function sortValue(row: SortRow, spec: SortSpec): SortValue | null {
  if (spec.builtin === '__title__') return { kind: 'string', value: row.title }
  if (spec.builtin === '__created_at__') return { kind: 'string', value: row.createdAt }
  if (spec.builtin === '__updated_at__') return { kind: 'string', value: row.updatedAt }
  if (spec.fieldId === undefined) return null
  const raw = valueOf(row.values, spec.fieldId)
  if (raw === null || raw === undefined) return null
  switch (spec.type) {
    case 'text':
    case 'url':
    case 'date':
    case 'select':
      return typeof raw === 'string' ? { kind: 'string', value: raw } : null
    case 'number':
      return typeof raw === 'number' ? { kind: 'number', value: raw } : null
    case 'checkbox':
      return typeof raw === 'boolean' ? { kind: 'boolean', value: raw } : null
    case 'multi_select':
    case 'tags':
      return Array.isArray(raw) && typeof raw[0] === 'string' ? { kind: 'string', value: raw[0] } : null
    case 'relation':
      if (Array.isArray(raw) && raw.length > 0) {
        const first: unknown = raw[0]
        if (first !== null && typeof first === 'object' && !Array.isArray(first) && 'type' in first && 'id' in first) {
          return { kind: 'string', value: `${String(first.type)}:${String(first.id)}` }
        }
      }
      return null
    default:
      return null
  }
}

function compareValues(left: SortValue, right: SortValue): number {
  if (left.value < right.value) return -1
  if (left.value > right.value) return 1
  return 0
}

export function sortRows(rows: SortRow[], specs: SortSpec[]): SortRow[] {
  if (specs.length === 0) return rows
  return [...rows].sort((left, right) => {
    for (const spec of specs) {
      const a = sortValue(left, spec)
      const b = sortValue(right, spec)
      if (a === null && b === null) continue
      if (a === null) return 1
      if (b === null) return -1
      const compared = compareValues(a, b)
      if (compared !== 0) return spec.desc ? -compared : compared
    }
    return left.id < right.id ? -1 : left.id > right.id ? 1 : 0
  })
}