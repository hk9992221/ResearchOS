/**
 * Overview labels: idea status, run status, task priority, milestone kinds and
 * the plain-text helpers the cards and the provenance view share.
 */

import type { ProvenanceMilestoneKind } from '@researchos/shared'

import type { Tone } from '../../design/components'
import type { Translations } from '../../i18n'

const IDEA_STATUS_KEY: Readonly<Record<string, string>> = {
  new: 'pages.ideas.status.new',
  researching: 'pages.ideas.status.researching',
  validating: 'pages.ideas.status.validating',
  supported: 'pages.ideas.status.supported',
  partiallysupported: 'pages.ideas.status.partially_supported',
  partially_supported: 'pages.ideas.status.partially_supported',
  inconclusive: 'pages.ideas.status.inconclusive',
  rejected: 'pages.ideas.status.rejected',
  onhold: 'pages.ideas.status.on_hold',
  on_hold: 'pages.ideas.status.on_hold'
}

export function ideaStatusLabel(t: Translations, status: string): string {
  const key = IDEA_STATUS_KEY[status.trim().toLowerCase()]
  return key === undefined ? status : t(key)
}

export function ideaStatusTone(status: string): Tone {
  switch (status.trim().toLowerCase()) {
    case 'supported':
      return 'success'
    case 'partiallysupported':
    case 'partially_supported':
      return 'info'
    case 'validating':
    case 'researching':
      return 'warning'
    case 'rejected':
      return 'danger'
    default:
      return 'neutral'
  }
}

const RUN_STATUS_KEY: Readonly<Record<string, string>> = {
  running: 'pages.experiments.run_running',
  finished: 'pages.experiments.run_finished',
  failed: 'pages.experiments.run_failed',
  unknown: 'pages.experiments.run_unknown'
}

export function runStatusLabel(t: Translations, status: string): string {
  const key = RUN_STATUS_KEY[status.trim().toLowerCase()]
  return key === undefined ? status : t(key)
}

export function taskPriorityLabel(t: Translations, rank: number): string {
  switch (rank) {
    case 3:
      return t('pages.kanban.priority_high')
    case 2:
      return t('pages.kanban.priority_medium')
    case 1:
      return t('pages.kanban.priority_low')
    default:
      return t('pages.kanban.priority_none')
  }
}

export function taskPriorityTone(rank: number): Tone {
  switch (rank) {
    case 3:
      return 'warning'
    case 2:
      return 'info'
    default:
      return 'neutral'
  }
}

const MILESTONE_KEY: Readonly<Record<ProvenanceMilestoneKind, string>> = {
  paper_imported: 'shell.prov.kind_paper',
  note_created: 'shell.prov.kind_note',
  idea_proposed: 'shell.prov.kind_idea',
  idea_derived: 'shell.prov.kind_derived',
  idea_status_changed: 'shell.prov.kind_status',
  experiment_analyzed: 'shell.prov.kind_exp',
  evidence_recorded: 'shell.prov.kind_evidence',
  weekly_confirmed: 'shell.prov.kind_weekly'
}

export function milestoneLabel(t: Translations, kind: ProvenanceMilestoneKind): string {
  return t(MILESTONE_KEY[kind])
}

export function milestoneTone(kind: ProvenanceMilestoneKind): Tone {
  switch (kind) {
    case 'idea_proposed':
    case 'idea_derived':
      return 'info'
    case 'experiment_analyzed':
    case 'idea_status_changed':
      return 'success'
    case 'evidence_recorded':
    case 'paper_imported':
      return 'info'
    default:
      return 'neutral'
  }
}

/** Author line and year, skipping whichever is absent. */
export function paperMeta(authors: string, year: number): string {
  return [authors.trim(), year > 0 ? String(year) : ''].filter((part) => part !== '').join(' · ')
}

/** At most `max` runes, marked with one ellipsis; never splits a rune. */
export function truncateRunes(value: string, max: number): string {
  const runes = [...value]
  return runes.length <= max ? value : `${runes.slice(0, max).join('')}…`
}

/** Month sections drop the year the section already names; weeks lead with the day. */
export function nodeTime(iso: string, month: boolean): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return ''
  const pad = (value: number): string => String(value).padStart(2, '0')
  const time = `${pad(date.getHours())}:${pad(date.getMinutes())}`
  if (month) return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${time}`
  const weekdays = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
  return `${weekdays[date.getDay()] ?? ''} ${time}`
}

/** Activity counts sorted by count desc, then entity type, for a stable card. */
export function activityRows(
  counts: Readonly<Record<string, number>>
): Array<{ label: string; count: number }> {
  return Object.entries(counts)
    .map(([label, count]) => ({ label, count }))
    .sort((left, right) =>
      left.count !== right.count ? right.count - left.count : left.label < right.label ? -1 : 1
    )
}

export function activityLabel(t: Translations, entityType: string): string {
  const key = `shell.overview.activity.${entityType}`
  const label = t(key)
  return label === key ? entityType : label
}