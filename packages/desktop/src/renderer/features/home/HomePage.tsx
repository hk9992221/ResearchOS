import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import {
  Archive,
  BookOpen,
  FlaskConical,
  FolderOpen,
  ListChecks,
  Plus,
  RefreshCw,
  StickyNote,
  type LucideIcon
} from 'lucide-react'

import type {
  DashboardProjectCard,
  DashboardSnapshot,
  ProjectActivityItem,
  ProjectStats
} from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  Card,
  CardGrid,
  EmptyState,
  ErrorState,
  IconButton,
  Page,
  PageHeader,
  Skeleton
} from '../../design/components'
import { useI18n, useT, type Translations } from '../../i18n'
import { iconFor } from '../../app/icons'
import { isBridgeUnavailable } from '../../lib/ipc'
import { routeHref } from '../../routes'
import { useShellStore } from '../../state/shell'
import { DASHBOARD_FEED, homeApi } from './api'
import { activityHref, activityIcon, relativeTime } from './format'
import styles from './home.module.css'

const DASHBOARD_KEY = ['projects', 'dashboard'] as const

interface KnownCard {
  readonly stats: ProjectStats
  readonly lastActivityAt: string | null
}

/**
 * Home is the workspace workspace dashboard: the statistics strip, the
 * project cards and the merged cross-project feed. The query lives under the
 * `['projects']` root so any project write invalidates it together with the
 * list.
 */
export default function HomePage(): JSX.Element {
  const t = useT()
  const i18n = useI18n()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const currentProjectId = useShellStore((state) => state.currentProjectId)
  const setCurrentProjectId = useShellStore((state) => state.setCurrentProjectId)

  const [refreshing, setRefreshing] = useState(false)
  const [refreshError, setRefreshError] = useState<string | null>(null)
  const [extra, setExtra] = useState<ProjectActivityItem[] | null>(null)
  const [feedBusy, setFeedBusy] = useState(false)
  const [feedExhausted, setFeedExhausted] = useState(false)
  const [feedError, setFeedError] = useState<string | null>(null)

  const query = useQuery({
    queryKey: DASHBOARD_KEY,
    queryFn: () => homeApi.dashboard()
  })
  const snapshot: DashboardSnapshot | undefined = query.data

  // Until a degraded card recovers, its last known numbers stay on screen:
  // the domain returns zero statistics for a degraded project, and showing
  // those zeros would claim the project is empty.
  const known = useRef(new Map<string, KnownCard>())
  useEffect(() => {
    if (snapshot === undefined) return
    for (const card of snapshot.projects) {
      if (card.degraded) continue
      known.current.set(card.project.id, {
        stats: card.stats,
        lastActivityAt: card.lastActivityAt
      })
    }
  }, [snapshot])

  useEffect(() => {
    setExtra(null)
    setFeedExhausted(false)
    setFeedError(null)
  }, [snapshot?.generatedAt])

  const refresh = async (): Promise<void> => {
    if (refreshing) return
    setRefreshing(true)
    setRefreshError(null)
    try {
      queryClient.setQueryData(DASHBOARD_KEY, await homeApi.refresh())
    } catch (error) {
      setRefreshError(error instanceof Error ? error.message : String(error))
    } finally {
      setRefreshing(false)
    }
  }

  const feed = extra ?? snapshot?.activity ?? []

  const loadMore = async (): Promise<void> => {
    if (snapshot === undefined || feedBusy) return
    const target = Math.min(
      DASHBOARD_FEED.maxLimit,
      Math.max(feed.length, DASHBOARD_FEED.defaultLimit) + DASHBOARD_FEED.pageStep
    )
    setFeedBusy(true)
    setFeedError(null)
    try {
      const merged = await homeApi.mergedActivity(
        snapshot.projects.map((card) => card.project.id),
        target
      )
      setExtra(merged)
      if (merged.length < target) setFeedExhausted(true)
    } catch (error) {
      setFeedError(error instanceof Error ? error.message : String(error))
    } finally {
      setFeedBusy(false)
    }
  }

  const openProject = (projectId: string): void => {
    setCurrentProjectId(projectId)
    const href = routeHref('overview', { projectId })
    if (href !== undefined) navigate(href)
  }

  const openActivity = (item: ProjectActivityItem): void => {
    const href = activityHref(item)
    if (href === undefined) return
    setCurrentProjectId(item.route.projectId)
    navigate(href)
  }

  const projectNames = useMemo(() => {
    const map = new Map<string, string>()
    for (const card of snapshot?.projects ?? []) map.set(card.project.id, card.project.name)
    return map
  }, [snapshot])

  const activeProjects = useMemo(
    () =>
      (snapshot?.projects ?? [])
        .filter((card) => !card.archived)
        .map((card) => card.project),
    [snapshot]
  )

  if (query.isPending) {
    return (
      <Page>
        <PageHeader title={t('shell.page.home')} />
        <Card>
          <Skeleton rows={6} />
        </Card>
      </Page>
    )
  }

  if (query.isError && snapshot === undefined) {
    const unavailable = isBridgeUnavailable(query.error)
    return (
      <Page>
        <PageHeader title={t('shell.page.home')} />
        {unavailable ? (
          <EmptyState
            icon={iconFor('overview')}
            title={t('shell.home.not_ready_title')}
            description={t('shell.home.not_ready_reason')}
          />
        ) : (
          <ErrorState
            title={t('shell.home.banner_load_fail')}
            message={query.error instanceof Error ? query.error.message : undefined}
            retryLabel={t('shell.overview.error_retry')}
            onRetry={() => void query.refetch()}
          />
        )}
      </Page>
    )
  }

  if (snapshot === undefined) {
    return (
      <Page>
        <PageHeader title={t('shell.page.home')} />
        <Skeleton rows={6} />
      </Page>
    )
  }

  const selectedProjectId =
    currentProjectId !== null && activeProjects.some((project) => project.id === currentProjectId)
      ? currentProjectId
      : activeProjects[0]?.id ?? ''

  const hour = new Date().getHours()
  const greetingKey =
    hour < 12
      ? 'shell.home.greeting_morning'
      : hour < 18
        ? 'shell.home.greeting_afternoon'
        : 'shell.home.greeting_evening'
  const today = new Intl.DateTimeFormat(i18n.language, {
    month: 'long',
    day: 'numeric',
    weekday: 'long'
  }).format(new Date())

  return (
    <Page>
      <header className={styles.greeting}>
        <div className={styles.greetingText}>
          <h1 className={styles.greetingTitle}>
            {t(greetingKey)}
            <span className={styles.greetingDate}>{today}</span>
          </h1>
          <p className={styles.greetingStatus}>
            {t('shell.home.greeting_status', { n: activeProjects.length })}
          </p>
        </div>
        <div className={styles.greetingActions}>
          <IconButton
            icon={RefreshCw}
            label={t('shell.home.btn_refresh')}
            disabled={refreshing}
            onClick={() => void refresh()}
          />
          {activeProjects.length > 0 && (
            <Button
              variant="primary"
              icon={FolderOpen}
              onClick={() => openProject(selectedProjectId)}
            >
              {t('shell.home.open_project')}
            </Button>
          )}
          <Button
            variant="secondary"
            icon={Plus}
            onClick={() => navigate('/projects?create=1')}
          >
            {t('shell.projects.btn_create')}
          </Button>
        </div>
      </header>

      {refreshError !== null && (
        <Banner tone="danger" title={t('shell.home.banner_load_fail')}>
          {refreshError}
        </Banner>
      )}

      <div className={styles.stats}>
        <Stat icon={FolderOpen} label={t('shell.home.stat_active')} value={snapshot.totals.active} />
        <Stat icon={Archive} label={t('shell.home.stat_archived')} value={snapshot.totals.archived} />
        <Stat icon={BookOpen} label={t('shell.home.stat_papers')} value={snapshot.totals.papers} />
        <Stat icon={StickyNote} label={t('shell.home.stat_notes')} value={snapshot.totals.notes} />
        <Stat icon={ListChecks} label={t('shell.home.stat_tasks')} value={snapshot.totals.openTasks} />
      </div>

      {snapshot.projects.length === 0 ? (
        <EmptyState
          icon={iconFor('projects')}
          title={t('shell.projects.empty_title')}
          description={t('shell.projects.empty_reason_home')}
          action={
            <Button
              variant="primary"
              icon={Plus}
              onClick={() => navigate('/projects?create=1')}
            >
              {t('shell.projects.btn_create')}
            </Button>
          }
        />
      ) : (
        <div className={styles.columns}>
          <CardGrid>
            {snapshot.projects.map((card) => (
              <ProjectCard
                key={card.project.id}
                card={card}
                known={known.current.get(card.project.id)}
                onOpen={() => openProject(card.project.id)}
              />
            ))}
          </CardGrid>

          <Card title={t('shell.home.feed_title')}>
            {feed.length === 0 ? (
              <p className={styles.muted}>{t('shell.home.feed_empty')}</p>
            ) : (
              <ul className={styles.feed} data-testid="feed-items">
                {feed.map((item) => (
                  <li key={item.id}>
                    <FeedRow
                      item={item}
                      projectName={projectNames.get(item.route.projectId) ?? ''}
                      onOpen={() => openActivity(item)}
                    />
                  </li>
                ))}
              </ul>
            )}
            {feedError !== null && (
              <Banner tone="danger" title={t('shell.home.feed_more_fail')}>
                {feedError}
              </Banner>
            )}
            {!feedExhausted && feed.length > 0 && feed.length < DASHBOARD_FEED.maxLimit && (
              <Button variant="ghost" size="sm" onClick={() => void loadMore()} disabled={feedBusy}>
                {t('shell.home.feed_more')}
              </Button>
            )}
          </Card>
        </div>
      )}
    </Page>
  )
}

function Stat({
  icon: Icon,
  label,
  value
}: {
  readonly icon: LucideIcon
  readonly label: string
  readonly value: number
}): JSX.Element {
  return (
    <div className={styles.stat}>
      <Icon aria-hidden="true" className={styles.statIcon} strokeWidth={2} />
      <span className={styles.statText}>
        <span className={styles.statValue}>{value}</span>
        <span className={styles.statLabel}>{label}</span>
      </span>
    </div>
  )
}

interface ProjectCardProps {
  readonly card: DashboardProjectCard
  readonly known: KnownCard | undefined
  readonly onOpen: () => void
}

function ProjectCard({ card, known, onOpen }: ProjectCardProps): JSX.Element {
  const t = useT()
  const stats = card.degraded ? (known?.stats ?? null) : card.stats
  const lastActivityAt = card.degraded ? (known?.lastActivityAt ?? null) : card.lastActivityAt
  const dash = '—'

  return (
    <button
      type="button"
      className={styles.projectCard}
      data-archived={card.archived}
      aria-label={card.project.name}
      onClick={onOpen}
    >
      <span className={styles.projectHead}>
        <span className={styles.projectName}>{card.project.name}</span>
        <span className={styles.badges}>
          {card.archived && <Badge tone="neutral">{t('shell.home.card_archived')}</Badge>}
          {card.degraded && <Badge tone="warning">{t('shell.home.card_degraded')}</Badge>}
        </span>
      </span>
      {card.project.summary !== '' && (
        <span className={styles.projectSummary}>{card.project.summary}</span>
      )}
      <span className={styles.projectCounts}>
        <ProjectCount icon={BookOpen} label={t('shell.home.count_papers')} value={stats?.papers ?? dash} />
        <ProjectCount icon={StickyNote} label={t('shell.home.count_notes')} value={stats?.notes ?? dash} />
        <ProjectCount icon={FlaskConical} label={t('shell.home.count_experiments')} value={stats?.experiments ?? dash} />
        <ProjectCount icon={ListChecks} label={t('shell.home.count_tasks')} value={stats?.tasksOpen ?? dash} />
      </span>
      {lastActivityAt !== null && (
        <span className={styles.projectMeta}>
          {t('shell.home.card_last_active', {
            time: formatRelative(t, lastActivityAt)
          })}
        </span>
      )}
    </button>
  )
}

function ProjectCount({
  icon: Icon,
  label,
  value
}: {
  readonly icon: LucideIcon
  readonly label: string
  readonly value: number | string
}): JSX.Element {
  return (
    <span className={styles.projectCount} title={label}>
      <Icon aria-hidden="true" strokeWidth={2} />
      {value}
    </span>
  )
}

interface FeedRowProps {
  readonly item: ProjectActivityItem
  readonly projectName: string
  readonly onOpen: () => void
}

function FeedRow({ item, projectName, onOpen }: FeedRowProps): JSX.Element {
  const t = useT()
  const Icon = iconFor(activityIcon(item.entityType))
  const gone = item.entityGone
  const content = (
    <>
      <Icon aria-hidden="true" className={styles.feedIcon} strokeWidth={2} />
      <span className={styles.feedSummary}>{item.summary}</span>
      {projectName !== '' && <span className={styles.feedProject}>{projectName}</span>}
      {gone ? (
        <Badge tone="neutral">{t('shell.home.feed_gone')}</Badge>
      ) : (
        <span className={styles.feedTime}>{formatRelative(t, item.createdAt)}</span>
      )}
    </>
  )
  if (gone) return <div className={styles.feedRow}>{content}</div>
  return (
    <button type="button" className={styles.feedRow} onClick={onOpen}>
      {content}
    </button>
  )
}

export function formatRelative(t: Translations, iso: string): string {
  const relative = relativeTime(iso)
  return relative.vars === undefined ? t(relative.key) : t(relative.key, relative.vars)
}