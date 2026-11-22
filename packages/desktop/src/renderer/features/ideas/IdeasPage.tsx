import { useEffect, useMemo, useState } from 'react'
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'

import type { IdeaStatus, ReadinessReport } from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  EmptyState,
  ErrorState,
  Page,
  PageHeader,
  Skeleton,
  Table,
  TextInput,
  Toolbar
} from '../../design/components'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { routeHref } from '../../routes'
import { useCurrentProject } from '../../app/useCurrentProject'
import { ideasApi } from './api'
import { CreateIdeaDialog } from './CreateIdeaDialog'
import { StatusBadge } from './StatusBadge'
import { STATUS_ORDER, formatTime, statusKey } from './model'
import styles from './ideas.module.css'

const SEARCH_DEBOUNCE_MS = 250
const PAGE_SIZE = 20

export default function IdeasPage({ projectId }: { readonly projectId?: string }): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const { project, projectId: currentProjectId, archived, isLoading: projectLoading } = useCurrentProject()
  const resolvedProjectId = projectId ?? currentProjectId

  const [searchInput, setSearchInput] = useState('')
  const [search, setSearch] = useState('')
  const [statuses, setStatuses] = useState<readonly IdeaStatus[]>([])
  const [createOpen, setCreateOpen] = useState(false)

  useEffect(() => {
    const timer = window.setTimeout(() => setSearch(searchInput), SEARCH_DEBOUNCE_MS)
    return () => window.clearTimeout(timer)
  }, [searchInput])

  const listQuery = useInfiniteQuery({
    queryKey: queryKeys.ideas.list(resolvedProjectId ?? 'none', {
      status: statuses,
      q: search
    }),
    queryFn: ({ pageParam }) =>
      ideasApi.list(resolvedProjectId as string, {
        status: statuses.length === 0 ? undefined : statuses,
        q: search,
        cursor: typeof pageParam === 'string' ? pageParam : undefined,
        limit: PAGE_SIZE
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
    enabled: resolvedProjectId !== null
  })

  const readinessQuery = useQuery({
    queryKey: ['project', resolvedProjectId ?? 'none', 'readiness', 'all'],
    queryFn: () => ideasApi.readiness(resolvedProjectId as string),
    enabled: resolvedProjectId !== null
  })

  const readiness = useMemo(() => {
    const map = new Map<string, ReadinessReport['ideas'][number]>()
    for (const idea of readinessQuery.data?.ideas ?? []) map.set(idea.ideaId, idea)
    return map
  }, [readinessQuery.data])

  if (resolvedProjectId === null) {
    return (
      <Page>
        <EmptyState
          title={t('pages.ideas.needs_project_title')}
          description={t('pages.ideas.needs_project_reason')}
        />
      </Page>
    )
  }

  if (projectLoading && project === null) {
    return (
      <Page>
        <PageHeader title={t('pages.ideas.title')} />
        <Skeleton rows={6} />
      </Page>
    )
  }

  const ideas = listQuery.data?.pages.flatMap((page) => page.items) ?? []
  const total = listQuery.data?.pages[0]?.total ?? 0
  const hasFilter = search.trim() !== '' || statuses.length > 0
  const openIdea = (ideaId: string): void => {
    const href = routeHref('idea', { projectId: resolvedProjectId, entityId: ideaId })
    if (href !== undefined) navigate(href)
  }

  const toggleStatus = (status: IdeaStatus): void => {
    setStatuses((current) =>
      current.includes(status) ? current.filter((item) => item !== status) : [...current, status]
    )
  }

  return (
    <Page>
      <PageHeader
        title={t('pages.ideas.title')}
        actions={
          <Button
            variant="primary"
            disabled={archived}
            title={archived ? t('pages.ideas.readonly_tooltip') : undefined}
            onClick={() => setCreateOpen(true)}
          >
            {t('pages.ideas.btn_create')}
          </Button>
        }
      />

      {archived && (
        <Banner tone="warning" title={t('pages.ideas.readonly_title')}>
          {t('pages.ideas.readonly_message')}
        </Banner>
      )}

      <Toolbar label={t('pages.ideas.view_label')}>
        <div className={styles.searchField}>
          <TextInput
            label={t('pages.ideas.field_search')}
            placeholder={t('pages.ideas.field_search_hint')}
            value={searchInput}
            hideLabel
            onChange={setSearchInput}
          />
        </div>
        <Button
          variant={statuses.length === 0 ? 'primary' : 'secondary'}
          size="sm"
          aria-pressed={statuses.length === 0}
          onClick={() => setStatuses([])}
        >
          {t('pages.ideas.chip_all')}
        </Button>
        <div className={styles.chips} role="group" aria-label={t('pages.ideas.status_label')}>
          {STATUS_ORDER.map((status) => (
            <button
              key={status}
              type="button"
              className={styles.chip}
              aria-pressed={statuses.includes(status)}
              onClick={() => toggleStatus(status)}
            >
              {t(statusKey(status))}
            </button>
          ))}
        </div>
        <span className={styles.count}>{total}</span>
      </Toolbar>

      {listQuery.isPending && <Skeleton rows={6} />}

      {listQuery.isError && (
        <ErrorState
          title={t('pages.ideas.banner_problem')}
          message={listQuery.error instanceof Error ? listQuery.error.message : undefined}
          onRetry={() => void listQuery.refetch()}
          retryLabel={t('pages.common.retry')}
        />
      )}

      {listQuery.isSuccess && ideas.length === 0 && (
        <EmptyState
          title={hasFilter ? t('pages.ideas.no_results_title') : t('pages.ideas.empty_title')}
          description={hasFilter ? t('pages.ideas.no_results_reason') : t('pages.ideas.empty_reason')}
          action={
            archived ? undefined : (
              <Button variant="primary" onClick={() => setCreateOpen(true)}>
                {t('pages.ideas.btn_create')}
              </Button>
            )
          }
        />
      )}

      {ideas.length > 0 && (
        <>
          <Table
            label={t('pages.ideas.title')}
            columns={[
              t('pages.ideas.col_title'),
              t('pages.ideas.col_status'),
              t('pages.ideas.col_support'),
              t('pages.ideas.col_drafts'),
              t('pages.ideas.col_links'),
              t('pages.ideas.tab_readiness'),
              t('pages.ideas.col_updated')
            ]}
          >
            {ideas.map((idea) => {
              const check = readiness.get(idea.id)
              return (
                <tr key={idea.id} className={styles.row}>
                  <td className={styles.titleCell}>
                    <button
                      type="button"
                      className={styles.titleButton}
                      onClick={() => openIdea(idea.id)}
                    >
                      {idea.title.trim() === '' ? t('pages.ideas.untitled') : idea.title}
                    </button>
                    {idea.derivedFromTitle !== null && (
                      <span className={styles.derivedFrom}>
                        {t('pages.ideas.sidebar_derived_from')}: {idea.derivedFromTitle}
                      </span>
                    )}
                  </td>
                  <td>
                    <StatusBadge status={idea.status} />
                  </td>
                  <td className={styles.numeric}>
                    <span className={styles.support}>{idea.supportCount}</span>
                    {' / '}
                    <span className={styles.oppose}>{idea.opposeCount}</span>
                  </td>
                  <td className={styles.numeric}>
                    {idea.draftCount > 0 ? (
                      <Badge tone="info">{t('pages.ideas.drafts_badge', { n: idea.draftCount })}</Badge>
                    ) : (
                      <span className={styles.muted}>0</span>
                    )}
                  </td>
                  <td className={styles.linksCell}>
                    {idea.linkedPapers} · {idea.linkedNotes} · {idea.linkedExperiments}
                  </td>
                  <td>
                    {check === undefined ? (
                      <span className={styles.muted}>—</span>
                    ) : (
                      <Badge tone={check.passed === check.total ? 'success' : 'warning'}>
                        {t('pages.readiness.badge_complete', {
                          passed: check.passed,
                          total: check.total
                        })}
                      </Badge>
                    )}
                  </td>
                  <td className={styles.timeCell}>{formatTime(idea.updatedAt)}</td>
                </tr>
              )
            })}
          </Table>
          <div className={styles.loadMore}>
            {listQuery.hasNextPage ? (
              <Button
                variant="secondary"
                size="sm"
                disabled={listQuery.isFetchingNextPage}
                onClick={() => void listQuery.fetchNextPage()}
              >
                {t('pages.ideas.history_more')}
              </Button>
            ) : null}
          </div>
        </>
      )}

      <CreateIdeaDialog
        open={createOpen}
        projectId={resolvedProjectId}
        onClose={() => setCreateOpen(false)}
        onCreated={(idea) => {
          setCreateOpen(false)
          openIdea(idea.id)
        }}
      />
    </Page>
  )
}