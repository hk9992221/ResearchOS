/**
 * Ideas presentation model: the status graph, label keys and small formatting
 * helpers shared by the list and detail pages. Nothing here touches IPC.
 */

import {
  IDEA_HISTORY_KINDS,
  IDEA_STATUSES,
  type ActivityActor,
  type ContextGraphNode,
  type ContextEntityKind,
  type EvidenceSourceType,
  type IdeaEntityRef,
  type IdeaHistoryKind,
  type IdeaStatus,
  type ProvenanceLineageNode
} from '@researchos/shared'

import { routeHref } from '../../routes'

/** Go `ideaStatusOrder`: On Hold last. */
export const STATUS_ORDER: readonly IdeaStatus[] = IDEA_STATUSES

const STATUS_KEY: Readonly<Record<IdeaStatus, string>> = {
  New: 'pages.ideas.status.new',
  Researching: 'pages.ideas.status.researching',
  Validating: 'pages.ideas.status.validating',
  Supported: 'pages.ideas.status.supported',
  PartiallySupported: 'pages.ideas.status.partially_supported',
  Inconclusive: 'pages.ideas.status.inconclusive',
  Rejected: 'pages.ideas.status.rejected',
  OnHold: 'pages.ideas.status.on_hold'
}

export function statusKey(status: IdeaStatus): string {
  return STATUS_KEY[status]
}

export type StatusTone = 'neutral' | 'success' | 'warning' | 'danger' | 'info'

export function statusTone(status: IdeaStatus): StatusTone {
  switch (status) {
    case 'Supported':
      return 'success'
    case 'PartiallySupported':
      return 'warning'
    case 'Inconclusive':
      return 'info'
    case 'Rejected':
      return 'danger'
    case 'Researching':
    case 'Validating':
      return 'info'
    default:
      return 'neutral'
  }
}

/**
 * Next-step suggestions of the Go selector (`ideaNextSteps`). The graph is a
 * hint only; every other status stays reachable.
 */
export function nextSteps(from: IdeaStatus): readonly IdeaStatus[] {
  switch (from) {
    case 'New':
      return ['Researching', 'OnHold']
    case 'Researching':
      return ['Validating', 'OnHold']
    case 'Validating':
      return ['Supported', 'PartiallySupported', 'Inconclusive', 'Rejected', 'OnHold']
    case 'Supported':
    case 'PartiallySupported':
    case 'Inconclusive':
      return ['Rejected', 'OnHold']
    case 'Rejected':
      return ['Researching', 'OnHold']
    case 'OnHold':
      return ['Researching', 'Validating']
  }
}

export function otherStatuses(from: IdeaStatus): readonly IdeaStatus[] {
  const suggested = new Set(nextSteps(from))
  return STATUS_ORDER.filter((status) => status !== from && !suggested.has(status))
}

export const HISTORY_KIND_KEY: Readonly<Record<IdeaHistoryKind, string>> = {
  created: 'pages.ideas.history_kind_created',
  status: 'pages.ideas.history_kind_status',
  relation: 'pages.ideas.history_kind_relation',
  evidence: 'pages.ideas.history_kind_evidence',
  deleted: 'pages.ideas.history_kind_deleted',
  restored: 'pages.ideas.history_kind_restored',
  ai: 'pages.ideas.history_kind_ai',
  derived: 'pages.ideas.history_kind_derived'
}

export function actorKey(actor: ActivityActor): string {
  switch (actor) {
    case 'mcp':
      return 'pages.ideas.actor_mcp'
    case 'ai':
      return 'pages.ideas.actor_ai'
    default:
      return 'pages.ideas.actor_user'
  }
}

export const HISTORY_KINDS: readonly IdeaHistoryKind[] = IDEA_HISTORY_KINDS

export const SOURCE_KEY: Readonly<Record<EvidenceSourceType, string>> = {
  experiment: 'pages.ideas.source_experiment',
  run: 'pages.ideas.source_run',
  note: 'pages.ideas.source_note',
  paper: 'pages.ideas.source_paper'
}

export const LINK_KIND_KEY: Readonly<Record<string, string>> = {
  paper: 'pages.ideas.link_paper',
  note: 'pages.ideas.link_note',
  experiment: 'pages.ideas.link_experiment',
  idea: 'pages.ideas.link_idea'
}

/** truncateRunes mirrors the Go 60-rune rejection summary. */
export function truncateRunes(text: string, max: number): string {
  const runes = Array.from(text.replace(/\s+/g, ' ').trim())
  return runes.length <= max ? runes.join('') : `${runes.slice(0, max).join('')}…`
}

export const IDEA_TITLE_MAX = 200
export const EXTRACT_INSTRUCTION_MAX = 500

/**
 * entityRoute returns the in-app destination for an entity reference, or
 * undefined when the renderer has no route for it. The ros:// href is always
 * available so a reference still identifies itself when it cannot be opened.
 */
export function entityRoute(
  ref: { type: string; id: string },
  projectId: string
): string | undefined {
  const kind = ref.type
  if (
    kind === 'idea' ||
    kind === 'note' ||
    kind === 'paper' ||
    kind === 'experiment' ||
    kind === 'run' ||
    kind === 'task'
  ) {
    return routeHref(kind, { projectId, entityId: ref.id })
  }
  return undefined
}

export function rosHref(ref: { type: string; id: string }): string {
  return `ros://${ref.type}/${ref.id}`
}

export function formatTime(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return date.toLocaleString()
}

export function refLabel(ref: IdeaEntityRef): string {
  return ref.title.trim() === '' ? ref.id : ref.title
}

export function isContextKind(value: string): value is ContextEntityKind {
  return (
    value === 'note' ||
    value === 'paper' ||
    value === 'idea' ||
    value === 'experiment' ||
    value === 'run' ||
    value === 'task' ||
    value === 'db_record' ||
    value === 'evidence'
  )
}

/* ------------------------------------------------------------------ lineage */

export interface LineageNodeData extends Record<string, unknown> {
  readonly id: string
  readonly title: string
  readonly status: string
  readonly supportCount: number
  readonly opposeCount: number
  readonly current: boolean
  readonly depth: number
}

export interface LineageLayout {
  readonly nodes: ReadonlyArray<{
    id: string
    data: LineageNodeData
    position: { x: number; y: number }
  }>
  readonly edges: ReadonlyArray<{
    id: string
    source: string
    target: string
    label: string
  }>
}

/**
 * layoutLineage turns the provenance tree into a left-to-right forest. The
 * width of a subtree positions its siblings so branches never overlap.
 */
export function layoutLineage(
  roots: readonly ProvenanceLineageNode[],
  currentId: string
): LineageLayout {
  const nodes: Array<{ id: string; data: LineageNodeData; position: { x: number; y: number } }> = []
  const edges: Array<{ id: string; source: string; target: string; label: string }> = []
  let cursor = 0

  const place = (node: ProvenanceLineageNode, depth: number): number => {
    const childYs = node.children.map((child) => place(child, depth + 1))
    const y = childYs.length === 0 ? cursor++ : (childYs[0]! + childYs[childYs.length - 1]!) / 2
    nodes.push({
      id: node.id,
      data: {
        id: node.id,
        title: node.title,
        status: node.status,
        supportCount: node.supportCount,
        opposeCount: node.opposeCount,
        current: node.id === currentId,
        depth
      },
      position: { x: depth * 240, y: y * 110 }
    })
    for (const child of node.children) {
      edges.push({ id: `${node.id}->${child.id}`, source: node.id, target: child.id, label: 'derived_from' })
    }
    return y
  }

  for (const root of roots) place(root, 0)
  return { nodes, edges }
}

/** flattenLineage lists every node in the tree, parents before children. */
export function flattenLineage(
  roots: readonly ProvenanceLineageNode[]
): ProvenanceLineageNode[] {
  const out: ProvenanceLineageNode[] = []
  const walk = (node: ProvenanceLineageNode): void => {
    out.push(node)
    for (const child of node.children) walk(child)
  }
  for (const root of roots) walk(root)
  return out
}

/** Graph edge label text; the graph itself is visual, this is its accessible form. */
export function graphNodeLabel(node: ContextGraphNode): string {
  return node.title.trim() === '' ? node.id : node.title
}