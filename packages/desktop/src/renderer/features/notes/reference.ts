/**
 * `ros://` references: parsing, candidate lookup for `[[` completion, and the
 * one insertion shape the editor and its tests share.
 *
 * A reference is a normal Markdown link whose target is
 * `ros://<type>/<uuid>`; the body keeps plain Markdown, while the editor
 * decorates and the preview intercepts clicks. `db_record` targets have no
 * route of their own, so they resolve as unavailable instead of guessing.
 */

import type { EditorState } from '@codemirror/state'
import type { ChangeSpec } from '@codemirror/state'
import type { ProjectPaper } from '@researchos/shared'

import { ipc } from '../../lib/ipc'
import { notesApi } from './notes-api'

export type ReferenceType = 'note' | 'paper' | 'idea' | 'experiment'

export interface ReferenceCandidate {
  readonly type: ReferenceType
  readonly id: string
  readonly title: string
}

export interface RosTarget {
  readonly type: string
  readonly id: string
  /** Page hint from `?page=N`; papers only. */
  readonly page: number | null
}

const ROS_LINK = /ros:\/\/([a-z_]+)\/([0-9a-fA-F-]{36})(?:\?[^\s)]*page=(\d+))?/g

/** parseRosTarget reads a `ros://` URL, or null for anything else. */
export function parseRosTarget(href: string): RosTarget | null {
  const match = /^ros:\/\/([a-z_]+)\/([0-9a-fA-F-]{36})(?:\?(.*))?$/u.exec(href)
  if (match === null) return null
  const type = match[1] ?? ''
  const id = match[2] ?? ''
  const query = match[3] ?? ''
  const pageMatch = /(?:^|&)page=(\d+)/u.exec(query)
  return { type, id, page: pageMatch === null ? null : Number(pageMatch[1]) }
}

export function rosHref(type: string, id: string, page: number | null = null): string {
  return page === null ? `ros://${type}/${id}` : `ros://${type}/${id}?page=${page}`
}

/** linkTargets maps a route-capable entity type to its registry route kind. */
export function routeKindForReference(type: string): 'note' | 'paper' | 'idea' | 'experiment' | null {
  switch (type) {
    case 'note':
      return 'note'
    case 'paper':
      return 'paper'
    case 'idea':
      return 'idea'
    case 'experiment':
      return 'experiment'
    default:
      return null
  }
}

/** referenceLabel is the human type name used by the picker. */
export function referenceKindLabel(type: ReferenceType): string {
  switch (type) {
    case 'note':
      return 'pages.notes.tab_scratchpad'
    case 'paper':
      return 'shell.page.literature'
    case 'idea':
      return 'shell.page.ideas'
    case 'experiment':
      return 'shell.page.experiments'
  }
}

/**
 * fetchCandidates queries the three list channels for the `[[` picker. One
 * module failing keeps the others: a missing idea module must not hide notes.
 */
export async function fetchReferenceCandidates(
  projectId: string,
  query: string,
  limit = 8
): Promise<ReferenceCandidate[]> {
  const results = await Promise.allSettled([
    notesApi.list(projectId, { q: query, limit }),
    ipc.invoke('ideas.list', { projectId, q: query, limit }),
    ipc.invoke('literature.papers.list', { projectId, q: query, limit }),
    ipc.invoke('experiments.list', { projectId, q: query, limit })
  ])
  const candidates: ReferenceCandidate[] = []
  const notes = results[0]
  if (notes.status === 'fulfilled') {
    for (const note of notes.value.items) {
      candidates.push({ type: 'note', id: note.id, title: note.title })
    }
  }
  const ideas = results[1]
  if (ideas.status === 'fulfilled') {
    for (const idea of ideas.value.items) {
      candidates.push({ type: 'idea', id: idea.id, title: idea.title })
    }
  }
  const papers = results[2]
  if (papers.status === 'fulfilled') {
    for (const paper of papers.value.items) {
      candidates.push({ type: 'paper', id: paper.id, title: paperTitle(paper) })
    }
  }
  const experiments = results[3]
  if (experiments.status === 'fulfilled') {
    for (const experiment of experiments.value) {
      candidates.push({ type: 'experiment', id: experiment.id, title: experiment.title })
    }
  }
  return candidates
}

function paperTitle(paper: ProjectPaper): string {
  const meta = paper.metadata
  for (const key of ['title', 'Title']) {
    const value = meta[key]
    if (typeof value === 'string' && value.trim() !== '') return value
  }
  return paper.zoteroKey
}

/** referenceMarkdown is what replaces `[[query`. */
export function referenceMarkdown(candidate: ReferenceCandidate): string {
  const title = candidate.title.trim() === '' ? candidate.type : candidate.title
  return `[${title}](${rosHref(candidate.type, candidate.id)})`
}

/**
 * referenceCompletion finds the `[[query` trigger before the cursor. The
 * returned `from` is the offset of the first bracket.
 */
export function referenceTrigger(
  state: EditorState,
  pos: number
): { from: number; query: string } | null {
  const line = state.doc.lineAt(pos)
  const before = state.sliceDoc(line.from, pos)
  const match = /\[\[([^[\]\n]*)$/u.exec(before)
  if (match === null) return null
  const query = match[1] ?? ''
  return { from: pos - query.length - 2, query }
}

/** replaceReference builds the transaction that swaps the trigger for a link. */
export function replaceReference(
  state: EditorState,
  from: number,
  to: number,
  candidate: ReferenceCandidate
): { changes: ChangeSpec; selection: { anchor: number } } {
  const text = referenceMarkdown(candidate)
  return {
    changes: { from, to, insert: text },
    selection: { anchor: from + text.length }
  }
}

/** DECORATION_RE is the body matcher for clickable reference spans. */
export function rosLinkMatches(text: string): Array<{ from: number; to: number; href: string }> {
  const out: Array<{ from: number; to: number; href: string }> = []
  for (const match of text.matchAll(ROS_LINK)) {
    const index = match.index
    if (index === undefined) continue
    out.push({ from: index, to: index + match[0].length, href: match[0] })
  }
  return out
}