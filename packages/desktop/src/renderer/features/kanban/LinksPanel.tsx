import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, X } from 'lucide-react'

import type { EntityType, RelationType, TaskDetail, TaskLink } from '@researchos/shared'

import { Button, IconButton, SelectField, TextInput } from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { kanbanApi } from './api'
import { kanbanKeys } from './keys'
import type { ToastInput } from './useFeatureToasts'
import styles from './kanban.module.css'

const LINK_TYPES: readonly { value: EntityType; key: string }[] = [
  { value: 'paper', key: 'pages.kanban.link_group_paper' },
  { value: 'note', key: 'pages.kanban.link_group_note' },
  { value: 'idea', key: 'pages.kanban.link_group_idea' },
  { value: 'experiment', key: 'pages.kanban.link_group_experiment' },
  { value: 'run', key: 'pages.kanban.link_group_run' }
]

const TYPE_LABEL: Readonly<Record<string, string>> = Object.fromEntries(
  LINK_TYPES.map((type) => [type.value, type.key])
)

interface Candidate {
  readonly type: EntityType
  readonly id: string
  readonly title: string
}

function paperTitle(paper: { id: string; zoteroKey: string; metadata: Record<string, unknown> }): string {
  const title = paper.metadata['title']
  return typeof title === 'string' && title.trim() !== '' ? title : paper.zoteroKey || paper.id
}

async function loadCandidates(
  projectId: string,
  targetType: EntityType,
  q: string
): Promise<Candidate[]> {
  switch (targetType) {
    case 'paper': {
      const page = await ipc.invoke('literature.papers.list', { projectId, q, limit: 20 })
      return page.items.map((paper) => ({
        type: 'paper',
        id: paper.id,
        title: paperTitle(paper)
      }))
    }
    case 'note': {
      const page = await ipc.invoke('notes.list', { projectId, q, limit: 20 })
      return page.items.map((note) => ({ type: 'note', id: note.id, title: note.title }))
    }
    case 'idea': {
      const page = await ipc.invoke('ideas.list', { projectId, q, limit: 20 })
      return page.items.map((idea) => ({ type: 'idea', id: idea.id, title: idea.title }))
    }
    case 'experiment': {
      const experiments = await ipc.invoke('experiments.list', { projectId, q, limit: 20 })
      return experiments.map((experiment) => ({
        type: 'experiment',
        id: experiment.id,
        title: experiment.title
      }))
    }
    case 'run': {
      const experiments = await ipc.invoke('experiments.list', { projectId, q, limit: 10 })
      const runs = await Promise.all(
        experiments.map((experiment) =>
          ipc.invoke('experiments.runs.list', { projectId, experimentId: experiment.id })
        )
      )
      return runs.flatMap((page) =>
        page.map((run) => ({ type: 'run' as const, id: run.id, title: run.displayName || run.title }))
      )
    }
    default:
      return []
  }
}

export interface LinksPanelProps {
  readonly projectId: string
  readonly detail: TaskDetail
  readonly readOnly: boolean
  readonly notify: (toast: ToastInput) => void
}

/**
 * Links tab: `related_to` context and `deliverable_of` outputs are separate
 * lists so the two meanings never merge. A soft-deleted endpoint stays with a
 * "target deleted" badge (§3.12 AC-24) instead of disappearing.
 */
export function LinksPanel({ projectId, detail, readOnly, notify }: LinksPanelProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [relation, setRelation] = useState<RelationType>('related_to')
  const [targetType, setTargetType] = useState<EntityType>('note')
  const [q, setQ] = useState('')
  const [pickerOpen, setPickerOpen] = useState(false)
  const [busy, setBusy] = useState(false)

  const candidates = useQuery({
    queryKey: kanbanKeys.linkCandidates(projectId, targetType, q),
    queryFn: () => loadCandidates(projectId, targetType, q),
    enabled: pickerOpen
  })

  const invalidateDetail = async (): Promise<void> => {
    await queryClient.invalidateQueries({ queryKey: kanbanKeys.detail(projectId, detail.id) })
  }

  const add = async (candidate: Candidate): Promise<void> => {
    setBusy(true)
    try {
      await kanbanApi.addLink(projectId, detail.id, relation, candidate.type, candidate.id)
      setPickerOpen(false)
    } catch (cause) {
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_link_fail'),
        message: cause instanceof Error ? cause.message : String(cause)
      })
    } finally {
      setBusy(false)
      await invalidateDetail()
    }
  }

  const remove = async (link: TaskLink): Promise<void> => {
    setBusy(true)
    try {
      await kanbanApi.removeLink(projectId, detail.id, link.relation, link.dstType, link.dstId)
    } catch (cause) {
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_link_fail'),
        message: cause instanceof Error ? cause.message : String(cause)
      })
    } finally {
      setBusy(false)
      await invalidateDetail()
    }
  }

  const section = (title: string, groupRelation: RelationType): JSX.Element => {
    const links = detail.links.filter((link) => link.relation === groupRelation)
    return (
      <section className={styles.linkGroup} aria-label={title}>
        <span className={styles.sectionTitle}>{title}</span>
        {links.length === 0 ? (
          <p className={styles.muted}>{t('pages.kanban.links_empty')}</p>
        ) : (
          links.map((link) => (
            <div key={`${link.relationId}`} className={styles.linkRow}>
              <span className={styles.linkType}>
                {t(TYPE_LABEL[link.dstType] ?? 'pages.kanban.link_type')}
              </span>
              <span className={styles.linkTitle}>{link.dstTitle}</span>
              {link.targetState === 'deleted' && (
                <span className={styles.linkDeleted}>{t('pages.kanban.link_target_deleted')}</span>
              )}
              {!readOnly && (
                <IconButton
                  icon={X}
                  label={t('pages.kanban.link_remove')}
                  size="sm"
                  disabled={busy}
                  onClick={() => void remove(link)}
                />
              )}
            </div>
          ))
        )}
        {!readOnly && (
          <div>
            <Button
              size="sm"
              variant="secondary"
              icon={Plus}
              aria-expanded={pickerOpen && relation === groupRelation}
              onClick={() => {
                setRelation(groupRelation)
                setPickerOpen((open) => !(open && relation === groupRelation))
              }}
            >
              {t('pages.kanban.link_add')}
            </Button>
          </div>
        )}
        {pickerOpen && relation === groupRelation && (
          <div className={styles.picker}>
            <div className={styles.pickerRow}>
              <SelectField
                label={t('pages.kanban.link_type')}
                value={targetType}
                options={LINK_TYPES.map((type) => ({ value: type.value, label: t(type.key) }))}
                onChange={(value) => {
                  const selected = LINK_TYPES.find((type) => type.value === value)
                  if (selected !== undefined) setTargetType(selected.value)
                }}
              />
              <div className={styles.filterField}>
                <TextInput
                  label={t('pages.kanban.link_search')}
                  hideLabel
                  placeholder={t('pages.kanban.link_search_hint')}
                  value={q}
                  onChange={setQ}
                />
              </div>
            </div>
            {candidates.isPending ? (
              <p className={styles.muted}>{t('pages.kanban.task_load')}…</p>
            ) : candidates.isError ? (
              <p className={styles.errorLine}>{candidates.error.message}</p>
            ) : candidates.data.length === 0 ? (
              <p className={styles.muted}>{t('pages.kanban.link_no_candidates')}</p>
            ) : (
              <div className={styles.candidateList}>
                {candidates.data.map((candidate) => (
                  <div key={`${candidate.type}:${candidate.id}`} className={styles.candidate}>
                    <span className={styles.candidateTitle}>{candidate.title}</span>
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={busy}
                      onClick={() => void add(candidate)}
                    >
                      {t('pages.kanban.link_add')}
                    </Button>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </section>
    )
  }

  return (
    <div className={styles.links}>
      {section(t('pages.kanban.links_context'), 'related_to')}
      {section(t('pages.kanban.links_deliverables'), 'deliverable_of')}
    </div>
  )
}