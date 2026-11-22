import { useEffect, useState } from 'react'
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import ReactMarkdown from 'react-markdown'
import rehypeKatex from 'rehype-katex'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'

import type {
  EvidenceDirection,
  EvidenceView,
  IdeaDetail
} from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  EmptyState,
  ErrorState,
  ProgressBar,
  Skeleton
} from '../../design/components'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { ideasApi } from './api'
import { EvidenceCreateDialog } from './EvidenceCreateDialog'
import { ExtractDialog } from './ExtractDialog'
import { SOURCE_KEY, entityRoute, formatTime, rosHref } from './model'
import styles from './ideas.module.css'

const EVIDENCE_PAGE_SIZE = 30
const EXTRACT_POLL_MS = 900

export interface EvidencePanelProps {
  readonly projectId: string
  readonly idea: IdeaDetail
  readonly archived: boolean
}

/**
 * Evidence tab: confirmed support/oppose columns, the unconfirmed AI drafts,
 * the manual add form and the extraction job strip. Drafts never move the
 * counts until confirmed — the counts come from the confirmed query only.
 */
export function EvidencePanel({ projectId, idea, archived }: EvidencePanelProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [createOpen, setCreateOpen] = useState(false)
  const [extractOpen, setExtractOpen] = useState(false)
  const [jobId, setJobId] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [pendingId, setPendingId] = useState<string | null>(null)

  const confirmed = useInfiniteQuery({
    queryKey: ['ideas', projectId, 'evidence', idea.id, 'confirmed'],
    queryFn: ({ pageParam }) =>
      ideasApi.evidence.list(projectId, idea.id, {
        confirmed: true,
        limit: EVIDENCE_PAGE_SIZE,
        cursor: typeof pageParam === 'string' ? pageParam : undefined
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined
  })

  const drafts = useQuery({
    queryKey: ['ideas', projectId, 'evidence', idea.id, 'drafts'],
    queryFn: () =>
      ideasApi.evidence.list(projectId, idea.id, { confirmed: false, limit: 50 })
  })

  const job = useQuery({
    queryKey: ['ideas', 'extract', jobId ?? 'none'],
    queryFn: () => ideasApi.evidence.extractPreview(jobId as string),
    enabled: jobId !== null,
    refetchInterval: (query) => {
      const state = query.state.data?.state
      return state === 'queued' || state === 'running' ? EXTRACT_POLL_MS : false
    }
  })

  // A finished extraction changed the drafts; the list and detail aggregates
  // are refreshed when the job reports success.
  const jobState = job.data?.state
  useEffect(() => {
    if (jobState !== 'succeeded' || jobId === null) return
    void queryClient.invalidateQueries({ queryKey: queryKeys.ideas.all(projectId) })
    void queryClient.invalidateQueries({
      queryKey: ['ideas', projectId, 'evidence', idea.id, 'drafts']
    })
  }, [jobState, jobId, queryClient, projectId, idea.id])

  const refresh = async (): Promise<void> => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: queryKeys.ideas.all(projectId) }),
      queryClient.invalidateQueries({
        queryKey: ['ideas', projectId, 'evidence', idea.id, 'confirmed']
      }),
      queryClient.invalidateQueries({
        queryKey: ['ideas', projectId, 'evidence', idea.id, 'drafts']
      })
    ])
  }

  const confirmDraft = async (draft: EvidenceView): Promise<void> => {
    setPendingId(draft.id)
    setActionError(null)
    try {
      await ideasApi.evidence.confirm(projectId, draft.id)
      await refresh()
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : t('pages.ideas.toast_evidence_fail'))
    } finally {
      setPendingId(null)
    }
  }

  const discardDraft = async (draft: EvidenceView): Promise<void> => {
    setPendingId(draft.id)
    setActionError(null)
    try {
      await ideasApi.evidence.discard(projectId, idea.id, draft.id)
      await refresh()
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : t('pages.ideas.toast_evidence_fail'))
    } finally {
      setPendingId(null)
    }
  }

  const evidence = confirmed.data?.pages.flatMap((page) => page.items) ?? []
  const support = evidence.filter((item) => item.direction === 'support')
  const oppose = evidence.filter((item) => item.direction === 'oppose')
  const draftItems = drafts.data?.items ?? []

  const sourceLink = (item: EvidenceView): JSX.Element => {
    const href = rosHref({ type: item.source.type, id: item.source.id })
    const label = item.sourceTitle.trim() === '' ? item.source.id : item.sourceTitle
    const destination =
      item.sourceBroken || item.sourceDeleted
        ? undefined
        : entityRoute({ type: item.source.type, id: item.source.id }, projectId)
    return (
      <span className={styles.sourceLink}>
        <Badge tone="neutral">{t(SOURCE_KEY[item.source.type])}</Badge>
        {destination === undefined ? (
          <span>{label}</span>
        ) : (
          <a
            href={href}
            className={styles.linkButton}
            onClick={(event) => {
              event.preventDefault()
              navigate(destination)
            }}
          >
            {label}
          </a>
        )}
        {(item.sourceBroken || item.sourceDeleted) && (
          <Badge tone="warning">{t('pages.ideas.target_deleted')}</Badge>
        )}
      </span>
    )
  }

  const card = (item: EvidenceView): JSX.Element => (
    <article key={item.id} className={styles.evidenceCard}>
      <header className={styles.evidenceHead}>
        <Badge tone={item.direction === 'support' ? 'success' : 'danger'}>
          {t(item.direction === 'support' ? 'pages.ideas.direction_support' : 'pages.ideas.direction_oppose')}
        </Badge>
        {sourceLink(item)}
        {item.aiGenerated && <Badge tone="info">{t('pages.ideas.ai_marker')}</Badge>}
        <span className={styles.muted}>{formatTime(item.createdAt)}</span>
      </header>
      <div className={styles.markdownBody}>
        <ReactMarkdown remarkPlugins={[remarkGfm, remarkMath]} rehypePlugins={[rehypeKatex]}>
          {item.content}
        </ReactMarkdown>
      </div>
    </article>
  )

  return (
    <section className={styles.panel}>
      <div className={styles.panelHead}>
        <h2 className={styles.sectionTitle}>{t('pages.ideas.tab_evidence')}</h2>
        <div className={styles.panelActions}>
          <Button variant="secondary" size="sm" disabled={archived} onClick={() => setCreateOpen(true)}>
            {t('pages.ideas.btn_add_evidence')}
          </Button>
          <Button variant="secondary" size="sm" disabled={archived} onClick={() => setExtractOpen(true)}>
            {t('pages.ideas.btn_ai')}
          </Button>
        </div>
      </div>

      {jobId !== null && (
        <ExtractJobStrip
          jobId={jobId}
          state={job.data?.state}
          warnings={job.data?.warnings}
          errorMessage={job.data?.error?.message ?? job.error?.message}
          onDismiss={() => setJobId(null)}
        />
      )}

      {actionError !== null && (
        <Banner tone="danger" title={t('pages.ideas.banner_action')}>
          {actionError}
        </Banner>
      )}

      {draftItems.length > 0 && (
        <section className={styles.drafts}>
          <h3 className={styles.subTitle}>
            {t('pages.ideas.drafts_card')} ({draftItems.length})
          </h3>
          {draftItems.map((draft) => (
            <article key={draft.id} className={styles.evidenceCard}>
              <header className={styles.evidenceHead}>
                <Badge tone="info">{t('pages.ideas.draft_marker')}</Badge>
                {sourceLink(draft)}
              </header>
              <div className={styles.markdownBody}>
                <ReactMarkdown remarkPlugins={[remarkGfm, remarkMath]} rehypePlugins={[rehypeKatex]}>
                  {draft.content}
                </ReactMarkdown>
              </div>
              <div className={styles.panelActions}>
                <Button
                  variant="primary"
                  size="sm"
                  disabled={archived || pendingId === draft.id}
                  onClick={() => void confirmDraft(draft)}
                >
                  {t('pages.ideas.evidence_accept')}
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={archived || pendingId === draft.id}
                  onClick={() => void discardDraft(draft)}
                >
                  {t('pages.ideas.evidence_discard')}
                </Button>
              </div>
            </article>
          ))}
        </section>
      )}

      {confirmed.isPending && <Skeleton rows={4} />}
      {confirmed.isError && (
        <ErrorState
          title={t('pages.ideas.banner_problem')}
          message={confirmed.error instanceof Error ? confirmed.error.message : undefined}
          onRetry={() => void confirmed.refetch()}
          retryLabel={t('pages.ideas.retry')}
        />
      )}
      {confirmed.isSuccess && evidence.length === 0 && (
        <EmptyState
          title={t('pages.ideas.evidence_empty_title')}
          description={t('pages.ideas.evidence_empty_reason')}
          headingLevel={3}
          compact
        />
      )}

      {evidence.length > 0 && (
        <div className={styles.evidenceColumns}>
          <EvidenceColumn
            title={t('pages.ideas.evidence_support_n', { n: idea.evidence.support })}
            tone="support"
            items={support}
            render={card}
          />
          <EvidenceColumn
            title={t('pages.ideas.evidence_oppose_n', { n: idea.evidence.oppose })}
            tone="oppose"
            items={oppose}
            render={card}
          />
        </div>
      )}

      {confirmed.hasNextPage && (
        <div className={styles.loadMore}>
          <Button
            variant="secondary"
            size="sm"
            disabled={confirmed.isFetchingNextPage}
            onClick={() => void confirmed.fetchNextPage()}
          >
            {t('pages.ideas.history_more')}
          </Button>
        </div>
      )}

      <EvidenceCreateDialog
        open={createOpen}
        projectId={projectId}
        idea={idea}
        onClose={() => setCreateOpen(false)}
        onCreated={() => {
          setCreateOpen(false)
          void refresh()
        }}
      />
      <ExtractDialog
        open={extractOpen}
        projectId={projectId}
        ideaId={idea.id}
        onClose={() => setExtractOpen(false)}
        onStarted={(startedJobId) => {
          setExtractOpen(false)
          setJobId(startedJobId)
        }}
      />
    </section>
  )
}

function EvidenceColumn({
  title,
  tone,
  items,
  render
}: {
  readonly title: string
  readonly tone: EvidenceDirection
  readonly items: readonly EvidenceView[]
  readonly render: (item: EvidenceView) => JSX.Element
}): JSX.Element {
  const t = useT()
  return (
    <section className={[styles.evidenceColumn, styles[tone]].join(' ')}>
      <h3 className={styles.subTitle}>{title}</h3>
      {items.length === 0 ? (
        <p className={styles.muted}>{t('pages.ideas.evidence_none')}</p>
      ) : (
        items.map(render)
      )}
    </section>
  )
}

function ExtractJobStrip({
  jobId,
  state,
  warnings,
  errorMessage,
  onDismiss
}: {
  readonly jobId: string
  readonly state?: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'
  readonly warnings?: readonly string[]
  readonly errorMessage?: string
  readonly onDismiss: () => void
}): JSX.Element {
  const t = useT()
  const running = state === undefined || state === 'queued' || state === 'running'
  if (state === 'failed' || state === 'cancelled') {
    return (
      <Banner
        tone="danger"
        title={t('pages.ideas.extract_failed', { text: errorMessage ?? '' })}
        action={
          <Button variant="secondary" size="sm" onClick={onDismiss}>
            {t('pages.ideas.ai_close')}
          </Button>
        }
      />
    )
  }
  return (
    <div className={styles.jobStrip} role="status" aria-label={t('pages.ideas.task_ai')}>
      <span className={styles.jobLabel}>
        {running ? t('pages.ideas.ai_running') : t('pages.ideas.ai_generate')}
      </span>
      {running ? (
        <ProgressBar value={state === undefined ? 20 : 60} label={t('pages.ideas.task_ai')} />
      ) : (
        <Badge tone="success">{t('pages.ideas.ai_generate')}</Badge>
      )}
      <span className={styles.muted}>{jobId}</span>
      {warnings !== undefined && warnings.length > 0 && (
        <span className={styles.muted}>{warnings.join(' · ')}</span>
      )}
      {!running && (
        <Button variant="ghost" size="sm" onClick={onDismiss}>
          {t('pages.ideas.ai_close')}
        </Button>
      )}
    </div>
  )
}