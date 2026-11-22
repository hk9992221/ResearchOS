/**
 * Filter builder over `FilterNode`: AND/OR groups, depth ≤ 3 and ≤ 30 leaves
 * The tree is edited immutably; only `toFilterNode` crosses the
 * boundary to the shared type, which does not yet name every operator the
 * engine accepts (`hasAny`, `isTrue`, `between`, …).
 */

import type { DbField, FilterNode } from '@researchos/shared'

import { Button, IconButton, SelectField, TextInput } from '../../design/components'
import { iconFor } from '../../app/icons'
import { useT } from '../../i18n'
import styles from './notes.module.css'

export const FILTER_MAX_DEPTH = 3
export const FILTER_MAX_LEAVES = 30

export interface UiLeaf {
  readonly kind: 'leaf'
  readonly fieldId: string
  readonly operator: string
  readonly value?: unknown
}

export interface UiGroup {
  readonly kind: 'group'
  readonly op: 'and' | 'or'
  readonly children: UiNode[]
}

export type UiNode = UiLeaf | UiGroup

export function emptyGroup(op: 'and' | 'or' = 'and'): UiGroup {
  return { kind: 'group', op, children: [] }
}

export function emptyLeaf(fieldId = ''): UiLeaf {
  return { kind: 'leaf', fieldId, operator: 'contains' }
}

export function countLeaves(node: UiNode): number {
  if (node.kind === 'leaf') return 1
  return node.children.reduce((total, child) => total + countLeaves(child), 0)
}

export function maxDepth(node: UiNode, depth = 1): number {
  if (node.kind === 'leaf') return depth
  return node.children.reduce((deepest, child) => Math.max(deepest, maxDepth(child, depth + 1)), depth)
}

export function canAddLeaf(root: UiNode): boolean {
  return countLeaves(root) < FILTER_MAX_LEAVES
}

/** canAddGroup keeps a new group from exceeding depth 3 once it has a leaf. */
export function canAddGroup(root: UiNode, depth: number): boolean {
  return depth + 2 <= FILTER_MAX_DEPTH && canAddLeaf(root)
}

export function fieldOptions(field: DbField | undefined): ReadonlyArray<{ id: string; name: string; color?: string }> {
  if (field === undefined) return []
  const config = field.config
  if (config === null || typeof config !== 'object' || Array.isArray(config)) return []
  const options = config['options']
  if (!Array.isArray(options)) return []
  const out: Array<{ id: string; name: string; color?: string }> = []
  for (const entry of options) {
    if (entry === null || typeof entry !== 'object' || Array.isArray(entry)) continue
    const id = 'id' in entry && typeof entry.id === 'string' ? entry.id : ''
    const name = 'name' in entry && typeof entry.name === 'string' ? entry.name : ''
    const color = 'color' in entry && typeof entry.color === 'string' ? entry.color : undefined
    if (id !== '' && name !== '') out.push({ id, name, color })
  }
  return out
}

interface OperatorSpec {
  readonly id: string
  readonly label: string
  readonly value: 'none' | 'single' | 'range' | 'multiple'
}

const TEXT_OPS: readonly string[] = ['eq', 'neq', 'contains', 'startsWith', 'isEmpty', 'isNotEmpty']

/** operatorsFor is the per-type operator menu, mirroring core `filter.ts`. */
export function operatorsFor(field: DbField | undefined): readonly OperatorSpec[] {
  if (field === undefined) return []
  const spec = (id: string, value: OperatorSpec['value']): OperatorSpec => ({
    id,
    label: `pages.notes.op_${OP_KEYS[id] ?? id}`,
    value
  })
  const none = (id: string): OperatorSpec => spec(id, 'none')
  const single = (id: string): OperatorSpec => spec(id, 'single')
  const multiple = (id: string): OperatorSpec => spec(id, 'multiple')
  switch (field.type) {
    case 'text':
    case 'url':
      return TEXT_OPS.map((id) => (id === 'isEmpty' || id === 'isNotEmpty' ? none(id) : single(id)))
    case 'number':
      return [
        single('eq'),
        single('neq'),
        single('gt'),
        single('gte'),
        single('lt'),
        single('lte'),
        spec('between', 'range'),
        none('isEmpty'),
        none('isNotEmpty')
      ]
    case 'date':
      return [
        spec('between', 'range'),
        single('gt'),
        single('gte'),
        single('lt'),
        single('lte'),
        none('isEmpty'),
        none('isNotEmpty')
      ]
    case 'select':
      return [single('eq'), single('neq'), multiple('isAnyOf'), none('isEmpty'), none('isNotEmpty')]
    case 'multi_select':
      return [multiple('hasAny'), multiple('hasAll'), multiple('hasNone'), none('isEmpty')]
    case 'tags':
      return [multiple('hasAny'), multiple('hasAll'), multiple('hasNone'), none('isEmpty')]
    case 'checkbox':
      return [none('isTrue'), none('isFalse')]
    case 'relation':
      return [multiple('hasAny'), multiple('hasNone'), none('isEmpty')]
  }
}

const OP_KEYS: Readonly<Record<string, string>> = {
  eq: 'eq',
  neq: 'neq',
  contains: 'contains',
  startsWith: 'starts',
  isEmpty: 'empty',
  isNotEmpty: 'not_empty',
  gt: 'gt',
  gte: 'gte',
  lt: 'lt',
  lte: 'lte',
  between: 'between',
  isAnyOf: 'any_of',
  hasAny: 'has_any',
  hasAll: 'has_all',
  hasNone: 'has_none',
  isTrue: 'true',
  isFalse: 'false'
}

/** toFilterNode is the one cast into the shared FilterNode contract. */
export function toFilterNode(node: UiNode): FilterNode {
  return node as unknown as FilterNode
}

function numberOrString(value: string, field: DbField): number | string {
  if (field.type === 'number') {
    const parsed = Number(value)
    return Number.isFinite(parsed) ? parsed : 0
  }
  return value
}

function leafValue(leaf: UiLeaf, field: DbField, operator: OperatorSpec): unknown {
  if (operator.value === 'none') return undefined
  if (operator.value === 'range') {
    const pair = Array.isArray(leaf.value) ? leaf.value : ['', '']
    return [numberOrString(String(pair[0] ?? ''), field), numberOrString(String(pair[1] ?? ''), field)]
  }
  if (operator.value === 'multiple') {
    return Array.isArray(leaf.value) ? leaf.value : []
  }
  return numberOrString(typeof leaf.value === 'string' ? leaf.value : '', field)
}

export function normalizeFilter(node: UiNode, fields: readonly DbField[]): UiNode {
  if (node.kind === 'group') {
    return { ...node, children: node.children.map((child) => normalizeFilter(child, fields)) }
  }
  const field = fields.find((candidate) => candidate.id === node.fieldId)
  if (field === undefined) return node
  const operator = operatorsFor(field).find((spec) => spec.id === node.operator) ?? operatorsFor(field)[0]
  if (operator === undefined) return node
  return { ...node, value: leafValue(node, field, operator) }
}

export interface FilterBuilderProps {
  readonly value: UiGroup
  readonly fields: readonly DbField[]
  readonly tags?: ReadonlyArray<{ id: string; name: string }>
  readonly onChange: (next: UiGroup) => void
}

/** FilterBuilder renders the draft tree; the caller applies it to the query. */
export function FilterBuilder({ value, fields, tags = [], onChange }: FilterBuilderProps): JSX.Element {
  return (
    <div className={styles.filterBuilder}>
      <GroupEditor
        node={value}
        fields={fields}
        tags={tags}
        depth={1}
        root={value}
        onChange={(next) => {
          if (next.kind === 'group') onChange(next)
        }}
        onRemove={undefined}
      />
      <div className={styles.filterHint}>
        {countLeaves(value)}/{FILTER_MAX_LEAVES}
      </div>
    </div>
  )
}

interface GroupEditorProps {
  readonly node: UiGroup
  readonly fields: readonly DbField[]
  readonly tags: ReadonlyArray<{ id: string; name: string }>
  readonly depth: number
  readonly root: UiGroup
  readonly onChange: (next: UiGroup) => void
  readonly onRemove: (() => void) | undefined
}

function GroupEditor({ node, fields, tags, depth, root, onChange, onRemove }: GroupEditorProps): JSX.Element {
  const t = useT()
  const updateChild = (index: number, child: UiNode): void => {
    onChange({ ...node, children: node.children.map((entry, position) => (position === index ? child : entry)) })
  }
  const leaves = countLeaves(root)
  return (
    <div className={styles.filterGroup} data-depth={depth}>
      <div className={styles.filterGroupHead}>
        <SelectField
          label={t('pages.notes.db_logic')}
          value={node.op}
          options={[
            { value: 'and', label: t('pages.notes.db_logic_and') },
            { value: 'or', label: t('pages.notes.db_logic_or') }
          ]}
          onChange={(op) => onChange({ ...node, op: op === 'or' ? 'or' : 'and' })}
        />
        {onRemove !== undefined && (
          <IconButton
            icon={iconFor('close')}
            label={t('pages.notes.db_remove_group')}
            onClick={onRemove}
          />
        )}
      </div>
      {node.children.map((child, index) =>
        child.kind === 'group' ? (
          <GroupEditor
            key={`group-${index}`}
            node={child}
            fields={fields}
            tags={tags}
            depth={depth + 1}
            root={root}
            onChange={(next) => updateChild(index, next)}
            onRemove={() =>
              onChange({ ...node, children: node.children.filter((_entry, position) => position !== index) })
            }
          />
        ) : (
          <LeafEditor
            key={`leaf-${index}`}
            leaf={child}
            fields={fields}
            tags={tags}
            onChange={(next) => updateChild(index, next)}
            onRemove={() =>
              onChange({ ...node, children: node.children.filter((_entry, position) => position !== index) })
            }
          />
        )
      )}
      <div className={styles.filterActions}>
        <Button
          variant="ghost"
          size="sm"
          icon={iconFor('plus')}
          disabled={leaves >= FILTER_MAX_LEAVES || depth >= FILTER_MAX_DEPTH}
          onClick={() => onChange({ ...node, children: [...node.children, emptyLeaf(fields[0]?.id ?? '')] })}
        >
          {t('pages.notes.db_add_condition')}
        </Button>
        <Button
          variant="ghost"
          size="sm"
          icon={iconFor('plus')}
          disabled={!canAddGroup(root, depth)}
          onClick={() => onChange({ ...node, children: [...node.children, { ...emptyGroup('or'), children: [emptyLeaf(fields[0]?.id ?? '')] }] })}
        >
          {t('pages.notes.db_add_group')}
        </Button>
        {leaves > 0 && (
          <Button variant="ghost" size="sm" onClick={() => onChange({ ...node, children: [] })}>
            {t('pages.notes.db_clear')}
          </Button>
        )}
      </div>
    </div>
  )
}

interface LeafEditorProps {
  readonly leaf: UiLeaf
  readonly fields: readonly DbField[]
  readonly tags: ReadonlyArray<{ id: string; name: string }>
  readonly onChange: (next: UiLeaf) => void
  readonly onRemove: () => void
}

function LeafEditor({ leaf, fields, tags, onChange, onRemove }: LeafEditorProps): JSX.Element {
  const t = useT()
  const field = fields.find((candidate) => candidate.id === leaf.fieldId)
  const operators = operatorsFor(field)
  const operator = operators.find((spec) => spec.id === leaf.operator) ?? operators[0]

  const changeField = (fieldId: string): void => {
    const nextField = fields.find((candidate) => candidate.id === fieldId)
    const nextOperator = operatorsFor(nextField)[0]
    onChange({ kind: 'leaf', fieldId, operator: nextOperator?.id ?? 'contains' })
  }

  return (
    <div className={styles.filterLeaf}>
      <SelectField
        label={t('pages.notes.db_condition_field')}
        value={leaf.fieldId}
        options={fields.map((candidate) => ({ value: candidate.id, label: candidate.name }))}
        onChange={changeField}
      />
      <SelectField
        label={t('pages.notes.db_condition_op')}
        value={operator?.id ?? ''}
        options={operators.map((spec) => ({ value: spec.id, label: t(spec.label) }))}
        onChange={(id) => onChange({ ...leaf, operator: id, value: undefined })}
      />
      {operator !== undefined && operator.value !== 'none' && (
        <ValueEditor leaf={leaf} field={field} operator={operator} tags={tags} onChange={onChange} />
      )}
      <IconButton
        icon={iconFor('close')}
        label={t('pages.notes.db_remove_condition')}
        onClick={onRemove}
      />
    </div>
  )
}

function optionsFor(
  field: DbField | undefined,
  tags: ReadonlyArray<{ id: string; name: string }>
): ReadonlyArray<{ id: string; name: string; color?: string }> {
  if (field?.type === 'tags') return tags.map((tag) => ({ id: tag.id, name: tag.name }))
  return fieldOptions(field)
}

function ValueEditor({
  leaf,
  field,
  operator,
  tags,
  onChange
}: {
  readonly leaf: UiLeaf
  readonly field: DbField | undefined
  readonly operator: OperatorSpec
  readonly tags: ReadonlyArray<{ id: string; name: string }>
  readonly onChange: (next: UiLeaf) => void
}): JSX.Element {
  const t = useT()
  if (field === undefined) return <span />
  if (operator.value === 'range') {
    const pair = Array.isArray(leaf.value) ? leaf.value : ['', '']
    const type = field.type === 'date' ? 'date' : 'number'
    return (
      <div className={styles.rangeRow}>
        <TextInput
          label={t('pages.notes.db_condition_value')}
          type={type}
          value={String(pair[0] ?? '')}
          onChange={(low) => onChange({ ...leaf, value: [low, String(pair[1] ?? '')] })}
        />
        <TextInput
          label={t('pages.notes.db_condition_high')}
          type={type}
          value={String(pair[1] ?? '')}
          onChange={(high) => onChange({ ...leaf, value: [String(pair[0] ?? ''), high] })}
        />
      </div>
    )
  }
  if (operator.value === 'multiple') {
    const options = optionsFor(field, tags)
    const selected = Array.isArray(leaf.value) ? leaf.value.map(String) : []
    if (options.length > 0) {
      return (
        <div className={styles.multiPick} role="group" aria-label={t('pages.notes.db_condition_value')}>
          {options.map((option) => {
            const active = selected.includes(option.id)
            return (
              <button
                key={option.id}
                type="button"
                className={[styles.tag, active ? styles.tagActive : ''].join(' ')}
                aria-pressed={active}
                onClick={() =>
                  onChange({
                    ...leaf,
                    value: active ? selected.filter((id) => id !== option.id) : [...selected, option.id]
                  })
                }
              >
                {option.color !== undefined && option.color !== '' && (
                  <span className={styles.colorDot} style={{ backgroundColor: option.color }} />
                )}
                {option.name}
              </button>
            )
          })}
        </div>
      )
    }
    return (
      <TextInput
        label={t('pages.notes.db_condition_value')}
        value={selected.join(', ')}
        hint={t('pages.notes.db_value_hint')}
        onChange={(text) =>
          onChange({
            ...leaf,
            value: text
              .split(',')
              .map((entry) => entry.trim())
              .filter((entry) => entry !== '')
          })
        }
      />
    )
  }
  if (field.type === 'select') {
    const options = fieldOptions(field)
    return (
      <SelectField
        label={t('pages.notes.db_condition_value')}
        value={typeof leaf.value === 'string' ? leaf.value : ''}
        options={[{ value: '', label: t('pages.notes.db_pick') }, ...options.map((option) => ({ value: option.id, label: option.name }))]}
        onChange={(value) => onChange({ ...leaf, value })}
      />
    )
  }
  if (field.type === 'relation') {
    return (
      <TextInput
        label={t('pages.notes.db_condition_value')}
        value={Array.isArray(leaf.value) ? leaf.value.join(', ') : ''}
        hint={t('pages.notes.db_value_hint')}
        onChange={(text) =>
          onChange({
            ...leaf,
            value: text
              .split(',')
              .map((entry) => entry.trim())
              .filter((entry) => entry !== '')
              .map((entry) => {
                const [type, id] = entry.split(':')
                return { type: type ?? '', id: id ?? '' }
              })
          })
        }
      />
    )
  }
  return (
    <TextInput
      label={t('pages.notes.db_condition_value')}
      type={field.type === 'number' ? 'number' : field.type === 'date' ? 'date' : 'text'}
      value={typeof leaf.value === 'string' ? leaf.value : String(leaf.value ?? '')}
      onChange={(value) => onChange({ ...leaf, value })}
    />
  )
}

