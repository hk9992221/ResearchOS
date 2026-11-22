import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'

import type {
  IdeaDetail,
  IdeaEntityRef,
  IdeaLinkTarget,
  IdeaLinkTargetType,
  ProjectResources
} from '@researchos/shared'

import { Badge, Button, Modal, SelectField, TextInput } from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { ideasApi } from './api'
import { LINK_KIND_KEY, entityRoute, rosHref } from './model'
import styles from './ideas.module.css'

export interface LinksPanelProps {
  readonly projectId: string
  readonly idea: IdeaDetail
  readonly archived: boolean
}

interface Candidate {
  readonly type: IdeaLinkTargetType
  readonly id: string
  readonly title: string
}

/**
 * Relations tab: linked papers, notes and experiments with add/remove, plus
 * the read-only derivation chain (the lineage graph is its own tab).
 */
export function LinksPanel({ projectId, idea, archived }: LinksPanelProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [addOpen, setAddOpen] = useState(false)
  const [targetType, setTargetType] = useState<IdeaLinkTargetType>('paper')
  const [search, setSearch] = useState('')
  const [chosen, setChosen] = useState<Candidate | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const resources = useQuery({
    queryKey: ['project', projectId, 'resources'],
    queryFn: () => ipc.invoke('project.resources', { id: projectId }),
    enabled: addOpen
  })

  const candidates = useMemo(() => {
    const data: ProjectResources | undefined = resources.data
    if (data === undefined) return []
    const all: Candidate[] =
      targetType === 'paper'
        ? data.papers.map((paper) => ({ type: 'paper' as const, id: paper.id, title: paper.title }))
        : targetType === 'note'
          ? data.notes.map((note) => ({ type: 'note' as const, id: note.id, title: note.title }))
          : data.experiments.map((experiment) => ({
              type: 'experiment' as const,
              id: experiment.id,
              title: experiment.title
            }))
    const query = search.trim().toLowerCase()
    return query === ''
      ? all
      : all.filter((candidate) => candidate.title.toLowerCase().includes(query))
  }, [resources.data, targetType, search])

  const refresh = async (): Promise<void> => {
    await queryClient.invalidateQueries({ queryKey: queryKeys.ideas.all(projectId) })
  }

  const add = async (): Promise<void> => {
    if (chosen === null) return
    setBusy(true)
    setError(null)
    try {
      await ideasApi.links.add(projectId, idea.id, {
        type: chosen.type,
        id: chosen.id
      } satisfies IdeaLinkTarget)
      await refresh()
      setAddOpen(false)
      setChosen(null)
      setSearch('')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('pages.ideas.toast_link_fail'))
    } finally {
      setBusy(false)
    }
  }

  const remove = async (ref: IdeaEntityRef): Promise<void> => {
    if (ref.type !== 'paper' && ref.type !== 'note' && ref.type !== 'experiment') return
    setError(null)
    try {
      await ideasApi.links.remove(projectId, idea.id, { type: ref.type, id: ref.id })
      await refresh()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('pages.ideas.toast_unlink_fail'))
    }
  }

  const relationRow = (ref: IdeaEntityRef, removable: boolean): JSX.Element => {
    const destination = ref.brokenRef
      ? undefined
      : entityRoute({ type: ref.type, id: ref.id }, projectId)
    return (
      <li key={`${ref.type}:${ref.id}`} className={styles.relationRow}>
        <Badge tone="neutral">{t(LINK_KIND_KEY[ref.type] ?? 'pages.ideas.link_kind')}</Badge>
        {destination === undefined ? (
          <span>{ref.title.trim() === '' ? ref.id : ref.title}</span>
        ) : (
          <a
            href={rosHref({ type: ref.type, id: ref.id })}
            className={styles.linkButton}
            onClick={(event) => {
              event.preventDefault()
              navigate(destination)
            }}
          >
            {ref.title.trim() === '' ? ref.id : ref.title}
          </a>
        )}
        {(ref.deleted || ref.brokenRef) && (
          <Badge tone="warning">{t('pages.ideas.target_deleted')}</Badge>
        )}
        {removable && !archived && (
          <Button variant="ghost" size="sm" onClick={() => void remove(ref)}>
            {t('pages.ideas.btn_unlink')}
          </Button>
        )}
      </li>
    )
  }

  const groups: Array<{ key: string; refs: readonly IdeaEntityRef[]; removable: boolean }> = [
    { key: 'pages.ideas.rel_papers', refs: idea.relations.papers, removable: true },
    { key: 'pages.ideas.rel_notes', refs: idea.relations.notes, removable: true },
    { key: 'pages.ideas.rel_experiments', refs: idea.relations.experiments, removable: true },
    { key: 'pages.ideas.sidebar_derived_from', refs: idea.relations.derivedFrom, removable: false },
    { key: 'pages.ideas.sidebar_derived', refs: idea.relations.derived, removable: false }
  ]

  return (
    <section className={styles.panel}>
      <div className={styles.panelHead}>
        <h2 className={styles.sectionTitle}>{t('pages.ideas.tab_relations')}</h2>
        <Button variant="secondary" size="sm" disabled={archived} onClick={() => setAddOpen(true)}>
          {t('pages.ideas.btn_link')}
        </Button>
      </div>
      {error !== null && <p className={styles.fieldError} role="alert">{error}</p>}
      {groups.map((group) => (
        <section key={group.key} className={styles.relationGroup}>
          <h3 className={styles.subTitle}>{t(group.key)}</h3>
          {group.refs.length === 0 ? (
            <p className={styles.muted}>{t('pages.ideas.rel_none')}</p>
          ) : (
            <ul className={styles.relationList}>
              {group.refs.map((ref) => relationRow(ref, group.removable))}
            </ul>
          )}
        </section>
      ))}

      <Modal
        open={addOpen}
        title={t('pages.ideas.link_title')}
        onClose={() => {
          if (busy) return
          setAddOpen(false)
        }}
        footer={
          <>
            <Button
              variant="secondary"
              onClick={() => setAddOpen(false)}
              disabled={busy}
            >
              {t('pages.ideas.link_close')}
            </Button>
            <Button
              variant="primary"
              onClick={() => void add()}
              disabled={busy || chosen === null}
              aria-busy={busy}
            >
              {t('pages.ideas.link_confirm')}
            </Button>
          </>
        }
      >
        <SelectField
          label={t('pages.ideas.link_kind')}
          value={targetType}
          options={[
            { value: 'paper', label: t('pages.ideas.link_paper') },
            { value: 'note', label: t('pages.ideas.link_note') },
            { value: 'experiment', label: t('pages.ideas.link_experiment') }
          ]}
          onChange={(next) => {
            setTargetType(next as IdeaLinkTargetType)
            setChosen(null)
          }}
        />
        <TextInput
          label={t('pages.ideas.link_search')}
          hint={t('pages.ideas.link_search_hint')}
          value={search}
          onChange={setSearch}
        />
        {candidates.length === 0 ? (
          <p className={styles.muted}>{t('pages.ideas.link_empty')}</p>
        ) : (
          <ul className={styles.candidateList}>
            {candidates.map((candidate) => (
              <li key={`${candidate.type}:${candidate.id}`}>
                <button
                  type="button"
                  className={styles.candidate}
                  aria-pressed={chosen?.id === candidate.id}
                  onClick={() => setChosen(candidate)}
                >
                  {candidate.title}
                </button>
              </li>
            ))}
          </ul>
        )}
      </Modal>
    </section>
  )
}