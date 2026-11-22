import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'

import type { WeeklyReportsListQuery } from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  EmptyState,
  ErrorState,
  Mono,
  Page,
  PageHeader,
  SelectField,
  Skeleton,
  SwitchField,
  Table,
  TextInput,
  Toolbar
} from '../../design/components'
import { useCurrentProject } from '../../app/useCurrentProject'
import { useI18n, useT } from '../../i18n'
import { routeHref, type RouteFeatureProps } from '../../routes'
import { GenerateDialog } from './GenerateDialog'
import { weeklyApi, weeklyKeys } from './weekly-api'
import { formatDay, formatInstant, periodLabel } from './weekly-format'
import styles from './weekly.module.css'

const PAGE_SIZE = 20
const STATUS_VALUES = ['all', 'draft', 'confirmed'] as const
type StatusFilter = (typeof STATUS_VALUES)[number]

/**
 * Weekly review history. This is the module's entry point: the report list
 * with its filters, the generate action (the empty state carries it too), and
 * the period picker seeded from the workspace's week start and timezone.
 */
export default function WeeklyReviewPage({ projectId: routeProjectId }: RouteFeatureProps): JSX.Element {
  const t = useT()
  const { language } = useI18n()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { projectId: currentProjectId, archived } = useCurrentProject()
  const projectId = routeProjectId ?? currentProjectId

  const [status, setStatus] = useState<StatusFilter>('all')
  const [search, setSearch] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [showDeleted, setShowDeleted] = useState(false)
  const [page, setPage] = useState(1)
  const [generateOpen, setGenerateOpen] = useState(false)

  const preferencesQuery = useQuery({
    queryKey: weeklyKeys.preferences,
    queryFn: weeklyApi.preferences
  })
  const weekStartsOn = preferencesQuery.data?.weekStartsOn ?? 1

  const params = useMemo(() => {
    const query: WeeklyReportsListQuery = {
      projectId: projectId ?? '',
      page,
      pageSize: PAGE_SIZE,
      order: '-period_start'
    }
    if (status !== 'all') query.status = [status]
    if (from !== '') query.from = from
    if (to !== '') query.to = to
    if (showDeleted) query.includeDeleted = true
    return query
  }, [from, page, projectId, showDeleted, status, to])

  const reportsQuery = useQuery({
    queryKey: weeklyKeys.reports(projectId ?? 'none', params),
    queryFn: () => weeklyApi.reports(params),
    enabled: projectId !== null
  })

  if (projectId === null) {
    return (
      <EmptyState
        title={t('pages.weekly_review.needs_project_title')}
        description={t('pages.weekly_review.needs_project_reason')}
      />
    )
  }

  const items = reportsQuery.data?.items ?? []
  const needle = search.trim().toLowerCase()
  const visible = needle === '' ? items : items.filter((item) => item.title.toLowerCase().includes(needle))
  const total = reportsQuery.data?.total ?? 0
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const openReport = (reportId: string): void => {
    const href = routeHref('weekly_report', { projectId, entityId: reportId })
    if (href !== undefined) navigate(href)
  }

  const applyFilter = (next: () => void): void => {
    next()
    setPage(1)
  }

  const reload = async (): Promise<void> => {
    await queryClient.invalidateQueries({ queryKey: weeklyKeys.reportsRoot(projectId) })
  }

  return (
    <Page>
      <PageHeader
        title={t('pages.weekly_review.title')}
        actions={
          <Button
            variant="primary"
            onClick={() => setGenerateOpen(true)}
            disabled={archived}
          >
            {t('pages.weekly_review.btn_generate')}
          </Button>
        }
      />

      {archived && (
        <Banner tone="warning" title={t('pages.weekly_review.archived_title')}>
          {t('pages.weekly_review.archived_message')}
        </Banner>
      )}

      <Toolbar label={t('pages.weekly_review.title')}>
        <SelectField
          label={t('pages.weekly_review.field_status')}
          value={status}
          onChange={(value) =>
            applyFilter(() => setStatus(value === 'draft' || value === 'confirmed' ? value : 'all'))
          }
          options={[
            { value: 'all', label: t('pages.weekly_review.status_all') },
            { value: 'draft', label: t('pages.weekly_review.status_draft') },
            { value: 'confirmed', label: t('pages.weekly_review.status_confirmed') }
          ]}
        />
        <TextInput
          label={t('pages.weekly_review.field_search')}
          value={search}
          onChange={(value) => applyFilter(() => setSearch(value))}
          hint={t('pages.weekly_review.field_search_hint')}
        />
        <TextInput
          label={t('pages.weekly_review.field_from')}
          type="date"
          value={from}
          onChange={(value) => applyFilter(() => setFrom(value))}
          hint={t('pages.weekly_review.field_from_hint')}
        />
        <TextInput
          label={t('pages.weekly_review.field_to')}
          type="date"
          value={to}
          onChange={(value) => applyFilter(() => setTo(value))}
          hint={t('pages.weekly_review.field_to_hint')}
        />
        <SwitchField
          label={t('pages.weekly_review.field_deleted')}
          checked={showDeleted}
          onChange={(checked) => applyFilter(() => setShowDeleted(checked))}
        />
        <Button variant="ghost" icon={RefreshCw} onClick={() => void reload()}>
          {t('pages.weekly_review.btn_reload')}
        </Button>
      </Toolbar>

      {reportsQuery.isPending && <Skeleton rows={6} />}

      {reportsQuery.isError && (
        <ErrorState
          title={t('pages.weekly_review.banner_problem')}
          message={reportsQuery.error.message}
          retryLabel={t('pages.weekly_review.btn_reload')}
          onRetry={() => void reportsQuery.refetch()}
        />
      )}

      {!reportsQuery.isPending && !reportsQuery.isError && (
        <>
          <Table
            label={t('pages.weekly_review.title')}
            empty={
              <EmptyState
                title={t('pages.weekly_review.empty_title')}
                description={t('pages.weekly_review.empty_reason')}
                action={
                  <Button
                    variant="primary"
                    onClick={() => setGenerateOpen(true)}
                    disabled={archived}
                  >
                    {t('pages.weekly_review.btn_generate')}
                  </Button>
                }
              />
            }
            columns={[
              t('pages.weekly_review.col_period'),
              t('pages.weekly_review.col_title'),
              t('pages.weekly_review.col_status'),
              t('pages.weekly_review.col_updated'),
              t('pages.weekly_review.col_confirmed'),
              t('pages.weekly_review.col_actions')
            ]}
          >
            {visible.map((item) => (
              <tr key={item.reportId}>
                <td>
                  <div className={styles.periodCell}>
                    <Mono>{periodLabel(item.periodStart, weekStartsOn)}</Mono>
                    <span className={styles.periodRange}>
                      {formatDay(item.periodStart, language)} – {formatDay(item.periodEnd, language)}
                    </span>
                  </div>
                </td>
                <td>
                  <div className={styles.titleCell}>
                    <button
                      type="button"
                      className={styles.linkButton}
                      onClick={() => openReport(item.reportId)}
                    >
                      {item.title}
                    </button>
                    {item.excerpt !== '' && <span className={styles.excerpt}>{item.excerpt}</span>}
                  </div>
                </td>
                <td>
                  <div className={styles.row}>
                    <Badge tone={item.status === 'confirmed' ? 'success' : 'warning'}>
                      {t(
                        item.status === 'confirmed'
                          ? 'pages.weekly_review.status_confirmed'
                          : 'pages.weekly_review.status_draft'
                      )}
                    </Badge>
                    {item.deletedAt !== null && (
                      <Badge tone="danger">{t('pages.weekly_review.field_deleted')}</Badge>
                    )}
                  </div>
                </td>
                <td>
                  <Mono>{formatInstant(item.updatedAt, language)}</Mono>
                </td>
                <td>
                  <Mono>
                    {item.confirmedAt === null ? '—' : formatInstant(item.confirmedAt, language)}
                  </Mono>
                </td>
                <td>
                  <Button variant="ghost" size="sm" onClick={() => openReport(item.reportId)}>
                    {t('pages.weekly_review.btn_open')}
                  </Button>
                </td>
              </tr>
            ))}
          </Table>

          <div className={styles.row}>
            <span className={styles.mono}>
              {t('pages.weekly_review.page_of', { page, pages, total })}
            </span>
            <div className={styles.grow} />
            <Button
              variant="secondary"
              size="sm"
              disabled={page <= 1}
              onClick={() => setPage((current) => Math.max(1, current - 1))}
            >
              {t('pages.weekly_review.btn_prev')}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              disabled={page >= pages}
              onClick={() => setPage((current) => Math.min(pages, current + 1))}
            >
              {t('pages.weekly_review.btn_next')}
            </Button>
          </div>
        </>
      )}

      <GenerateDialog
        open={generateOpen}
        projectId={projectId}
        onClose={() => setGenerateOpen(false)}
        onGenerated={(reportId) => {
          setGenerateOpen(false)
          void queryClient.invalidateQueries({ queryKey: weeklyKeys.reportsRoot(projectId) })
          openReport(reportId)
        }}
      />
    </Page>
  )
}
