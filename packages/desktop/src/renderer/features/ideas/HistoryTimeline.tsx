import { useState } from 'react'
import { useInfiniteQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'

import type { IdeaHistoryEntry, IdeaHistoryKind, IdeaStatus } from '@researchos/shared'

import { Badge, Button, EmptyState, ErrorState, Skeleton } from '../../design/components'
import { useT } from '../../i18n'
import { ideasApi } from './api'
import {
  HISTORY_KINDS,
  HISTORY_KIND_KEY,
  actorKey,
  entityRoute,
  formatTime,
  rosHref,
  statusKey
} from './model'
import styles from './ideas.module.css'

const HISTORY_PAGE_SIZE = 20

export interface HistoryTimelineProps {
  readonly projectId: string
  readonly ideaId: string
}

/** Merged, newest-first timeline: activity rows plus synthesized derivations. */
export function HistoryTimeline({ projectId, ideaId }: HistoryTimelineProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const [kinds, setKinds] = useState<readonly IdeaHistoryKind[]>([])

  const history = useInfiniteQuery({
    queryKey: ['ideas', projectId, 'history', ideaId, kinds],
    queryFn: ({ pageParam }) =>
      ideasApi.history(projectId, ideaId, {
        kinds: kinds.length === 0 ? undefined : kinds,
        limit: HISTORY_PAGE_SIZE,
        cursor: typeof pageParam === 'string' ? pageParam : undefined
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined
  })

  const entries = history.data?.pages.flatMap((page) => page.items) ?? []

  const toggleKind = (kind: IdeaHistoryKind): void => {
    setKinds((current) =>
      current.includes(kind) ? current.filter((item) => item !== kind) : [...current, kind]
    )
  }

  const refLink = (entry: IdeaHistoryEntry): JSX.Element | null => {
    const ref = entry.ref
    if (ref === null || ref.title.trim() === '') return null
    const destination = ref.brokenRef || ref.deleted
      ? undefined
      : entityRoute({ type: ref.type, id: ref.id }, projectId)
    return (
      <span className={styles.historyRef}>
        {destination === undefined ? (
          <span>{ref.title}</span>
        ) : (
          <a
            href={rosHref(ref)}
            className={styles.linkButton}
            onClick={(event) => {
              event.preventDefault()
              navigate(destination)
            }}
          >
            {ref.title}
          </a>
        )}
        {(ref.brokenRef || ref.deleted) && (
          <Badge tone="warning">{t('pages.ideas.target_deleted')}</Badge>
        )}
      </span>
    )
  }

  const statusLine = (entry: IdeaHistoryEntry): string | null => {
    if (entry.kind !== 'status' || entry.old === '' || entry.new === '') return null
    return `${t(statusKey(entry.old as IdeaStatus))} → ${t(statusKey(entry.new as IdeaStatus))}`
  }

  return (
    <section className={styles.panel}>
      <div className={styles.panelHead}>
        <h2 className={styles.sectionTitle}>{t('pages.ideas.tab_history')}</h2>
      </div>
      <div className={styles.chips} role="group" aria-label={t('pages.ideas.tab_history')}>
        {HISTORY_KINDS.map((kind) => (
          <button
            key={kind}
            type="button"
            className={styles.chip}
            aria-pressed={kinds.includes(kind)}
            onClick={() => toggleKind(kind)}
          >
            {t(HISTORY_KIND_KEY[kind])}
          </button>
        ))}
      </div>

      {history.isPending && <Skeleton rows={4} />}
      {history.isError && (
        <ErrorState
          title={t('pages.ideas.history_error')}
          message={history.error instanceof Error ? history.error.message : undefined}
          onRetry={() => void history.refetch()}
          retryLabel={t('pages.ideas.retry')}
        />
      )}
      {history.isSuccess && entries.length === 0 && (
        <EmptyState
          title={t('pages.ideas.history_empty_title')}
          description={t('pages.ideas.history_empty_reason')}
          headingLevel={3}
          compact
        />
      )}

      {entries.length > 0 && (
        <ol className={styles.timeline}>
          {entries.map((entry, index) => {
            const transition = statusLine(entry)
            return (
              <li
                key={`${entry.activityId ?? 'derived'}-${entry.at}-${index}`}
                className={styles.timelineItem}
              >
                <span className={styles.timelineDot} aria-hidden="true" />
                <div className={styles.timelineBody}>
                  <div className={styles.timelineHead}>
                    <Badge tone={entry.synthesized ? 'info' : 'neutral'}>
                      {t(HISTORY_KIND_KEY[entry.kind])}
                    </Badge>
                    <span className={styles.muted}>{formatTime(entry.at)}</span>
                    <Badge tone="neutral">{t(actorKey(entry.actor))}</Badge>
                    {entry.synthesized && (
                      <span className={styles.muted}>{t('pages.ideas.history_synthesized')}</span>
                    )}
                  </div>
                  {entry.summary !== '' && <p className={styles.timelineSummary}>{entry.summary}</p>}
                  {transition !== null && (
                    <p className={styles.timelineSummary}>{transition}</p>
                  )}
                  <div className={styles.timelineRef}>{refLink(entry)}</div>
                </div>
              </li>
            )
          })}
        </ol>
      )}

      {history.hasNextPage && (
        <div className={styles.loadMore}>
          <Button
            variant="secondary"
            size="sm"
            disabled={history.isFetchingNextPage}
            onClick={() => void history.fetchNextPage()}
          >
            {t('pages.ideas.history_more')}
          </Button>
        </div>
      )}
    </section>
  )
}