import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'

import type { IdeaDetail, IdeaStatus } from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Page,
  PageHeader,
  Skeleton,
  Tabs,
  TextInput
} from '../../design/components'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { routeHref } from '../../routes'
import { useCurrentProject } from '../../app/useCurrentProject'
import { ideasApi } from './api'
import { DeriveDialog } from './DeriveDialog'
import { EvidencePanel } from './EvidencePanel'
import { HistoryTimeline } from './HistoryTimeline'
import { LineageGraph } from './LineageGraph'
import { LinksPanel } from './LinksPanel'
import { MarkdownEditor } from './MarkdownEditor'
import { StatusBadge } from './StatusBadge'
import { StatusDialog } from './StatusDialog'
import { BacklinksPanel } from './BacklinksPanel'
import { ReadinessPanel } from './ReadinessPanel'
import {
  IDEA_TITLE_MAX,
  STATUS_ORDER,
  entityRoute,
  formatTime,
  nextSteps,
  otherStatuses,
  statusKey,
  truncateRunes
} from './model'
import styles from './ideas.module.css'

type TabId = 'overview' | 'evidence' | 'relations' | 'history' | 'lineage' | 'backlinks' | 'readiness'

export default function IdeaDetailPage({
  projectId: routeProjectId,
  entityId
}: {
  readonly projectId?: string
  readonly entityId?: string
}): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { projectId: currentProjectId, archived } = useCurrentProject()
  const projectId = routeProjectId ?? currentProjectId

  const [tab, setTab] = useState<TabId>('overview')
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [dirty, setDirty] = useState(false)
  const [titleBusy, setTitleBusy] = useState(false)
  const [saveBusy, setSaveBusy] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const [statusTarget, setStatusTarget] = useState<IdeaStatus | null>(null)
  const [deriveOpen, setDeriveOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [reasonExpanded, setReasonExpanded] = useState(false)

  const detailQuery = useQuery({
    queryKey: queryKeys.ideas.detail(projectId ?? 'none', entityId ?? 'none'),
    queryFn: () => ideasApi.get(projectId as string, entityId as string),
    enabled: projectId !== null && entityId !== undefined
  })

  const readinessQuery = useQuery({
    queryKey: ['project', projectId ?? 'none', 'readiness', entityId ?? 'none'],
    queryFn: () => ideasApi.readiness(projectId as string, entityId),
    enabled: projectId !== null && entityId !== undefined
  })

  const detail: IdeaDetail | undefined = detailQuery.data

  useEffect(() => {
    setTab('overview')
  }, [entityId])

  // The local title/description track the loaded idea; switching ideas resets
  // the buffer so a save can never write one idea's text onto another.
  useEffect(() => {
    if (detail === undefined) return
    setTitle(detail.title)
    setDescription(detail.description)
    setDirty(false)
    setReasonExpanded(false)
  }, [detail?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const back = async (): Promise<void> => {
    if (dirty && !(await saveDescription())) return
    navigate(routeHref('ideas', { projectId: projectId ?? undefined }) as string)
  }

  if (projectId === null || entityId === undefined) {
    return (
      <Page>
        <EmptyState
          title={t('pages.ideas.needs_project_title')}
          description={t('pages.ideas.needs_project_reason')}
        />
      </Page>
    )
  }

  if (detailQuery.isPending) {
    return (
      <Page>
        <PageHeader title={t('pages.ideas.detail_title')} />
        <Skeleton rows={8} />
      </Page>
    )
  }

  if (detailQuery.isError || detail === undefined) {
    return (
      <Page>
        <ErrorState
          title={t('pages.ideas.banner_problem')}
          message={detailQuery.error instanceof Error ? detailQuery.error.message : undefined}
          onRetry={() => void detailQuery.refetch()}
          retryLabel={t('pages.ideas.retry')}
        />
      </Page>
    )
  }

  if (detail.deletedAt !== null) {
    return (
      <Page>
        <EmptyState
          title={t('pages.ideas.deleted_title')}
          description={t('pages.ideas.deleted_message')}
          action={
            <Button
              variant="secondary"
              onClick={() => navigate(routeHref('ideas', { projectId }) as string)}
            >
              {t('pages.ideas.btn_back')}
            </Button>
          }
        />
      </Page>
    )
  }

  const invalidate = async (): Promise<void> => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: queryKeys.ideas.all(projectId) }),
      queryClient.invalidateQueries({ queryKey: queryKeys.ideas.detail(projectId, entityId) })
    ])
  }

  const saveTitle = async (value: string): Promise<void> => {
    const trimmed = value.trim()
    if (trimmed === '') {
      setTitle(detail.title)
      return
    }
    if (Array.from(trimmed).length > IDEA_TITLE_MAX) {
      setActionError(t('pages.ideas.title_too_long'))
      setTitle(detail.title)
      return
    }
    if (trimmed === detail.title) return
    setTitleBusy(true)
    setActionError(null)
    try {
      await ideasApi.update(projectId, entityId, { title: trimmed })
      await invalidate()
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : t('pages.ideas.banner_save'))
      setTitle(detail.title)
    } finally {
      setTitleBusy(false)
    }
  }

  async function saveDescription(): Promise<boolean> {
    if (!dirty) return true
    setSaveBusy(true)
    setActionError(null)
    try {
      await ideasApi.update(projectId as string, entityId as string, { description })
      setDirty(false)
      await invalidate()
      return true
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : t('pages.ideas.toast_description_fail'))
      return false
    } finally {
      setSaveBusy(false)
    }
  }

  const readiness = readinessQuery.data?.ideas.find((item) => item.ideaId === entityId)

  const tabs = [
    { id: 'overview', label: t('pages.ideas.tab_overview') },
    { id: 'evidence', label: t('pages.ideas.tab_evidence') },
    { id: 'relations', label: t('pages.ideas.tab_relations') },
    { id: 'history', label: t('pages.ideas.tab_history') },
    { id: 'lineage', label: t('pages.ideas.tab_lineage') },
    { id: 'backlinks', label: t('pages.ideas.tab_backlinks') },
    { id: 'readiness', label: t('pages.ideas.tab_readiness') }
  ]

  const rejection = detail.rejectionReason

  return (
    <Page>
      <header className={styles.detailHeader}>
        <div className={styles.detailTopRow}>
          <Button variant="ghost" size="sm" onClick={() => void back()}>
            {t('pages.ideas.btn_back')}
          </Button>
          <div className={styles.titleField}>
            <TextInput
              label={t('pages.ideas.field_idea_title')}
              hideLabel
              value={title}
              maxLength={IDEA_TITLE_MAX}
              readOnly={archived}
              disabled={titleBusy}
              onChange={setTitle}
              onBlur={() => void saveTitle(title)}
              onKeyDown={(event) => {
                if (event.nativeEvent.isComposing) return
                if (event.key === 'Enter') {
                  event.preventDefault()
                  event.currentTarget.blur()
                }
                if (event.key === 'Escape') {
                  setTitle(detail.title)
                  event.currentTarget.blur()
                }
              }}
            />
          </div>
          <select
            className={styles.statusSelect}
            aria-label={t('pages.ideas.status_label')}
            value={detail.status}
            disabled={archived}
            onChange={(event) => {
              const next = event.target.value as IdeaStatus
              if (next !== detail.status) setStatusTarget(next)
            }}
          >
            <optgroup label={t('pages.ideas.status_next')}>
              <option value={detail.status} disabled>
                {t(statusKey(detail.status))}
              </option>
              {nextSteps(detail.status).map((status) => (
                <option key={status} value={status}>
                  {t(statusKey(status))}
                </option>
              ))}
            </optgroup>
            <optgroup label={t('pages.ideas.status_other')}>
              {otherStatuses(detail.status).map((status) => (
                <option key={status} value={status}>
                  {t(statusKey(status))}
                </option>
              ))}
            </optgroup>
          </select>
          {readiness !== undefined && (
            <button
              type="button"
              className={styles.readinessButton}
              onClick={() => setTab('readiness')}
            >
              <Badge tone={readiness.passed === readiness.total ? 'success' : 'warning'}>
                {t('pages.readiness.badge_complete', {
                  passed: readiness.passed,
                  total: readiness.total
                })}
              </Badge>
            </button>
          )}
          <Button variant="secondary" size="sm" disabled={archived} onClick={() => setDeriveOpen(true)}>
            {t('pages.ideas.btn_derive')}
          </Button>
          <Button
            variant="danger"
            size="sm"
            disabled={archived}
            title={archived ? t('pages.ideas.readonly_tooltip') : undefined}
            onClick={() => setDeleteOpen(true)}
          >
            {t('pages.ideas.btn_delete')}
          </Button>
        </div>
        <div className={styles.detailMeta}>
          <StatusBadge status={detail.status} />
          <span className={styles.muted}>
            {t('pages.ideas.updated_at', { time: formatTime(detail.updatedAt) })}
          </span>
          {rejection !== null && rejection.trim() !== '' && (
            <button
              type="button"
              className={styles.rejection}
              aria-expanded={reasonExpanded}
              onClick={() => setReasonExpanded((value) => !value)}
            >
              <span className={styles.muted}>{t('pages.ideas.rejection_label')}:</span>{' '}
              <span>{reasonExpanded ? rejection : truncateRunes(rejection, 60)}</span>
            </button>
          )}
        </div>
      </header>

      {archived && (
        <Banner tone="warning" title={t('pages.ideas.readonly_title')}>
          {t('pages.ideas.readonly_message')}
        </Banner>
      )}
      {actionError !== null && (
        <Banner tone="danger" title={t('pages.ideas.banner_save')}>
          {actionError}
        </Banner>
      )}

      <Tabs
        label={t('pages.ideas.detail_title')}
        items={tabs}
        value={tab}
        onChange={(id) => setTab(id as TabId)}
      />

      {tab === 'overview' && (
        <div className={styles.overview}>
          <div className={styles.overviewMain}>
            <MarkdownEditor
              label={t('pages.ideas.overview_card')}
              value={description}
              readOnly={archived}
              onChange={(value) => {
                setDescription(value)
                setDirty(true)
              }}
            />
            <div className={styles.panelActions}>
              {dirty && <span className={styles.muted}>{t('pages.ideas.desc_draft')}</span>}
              <Button
                variant="primary"
                size="sm"
                disabled={archived || !dirty || saveBusy}
                onClick={() => void saveDescription()}
              >
                {t('pages.ideas.btn_save')}
              </Button>
            </div>
          </div>
          <aside className={styles.overviewSide}>
            <h2 className={styles.subTitle}>{t('pages.ideas.sidebar_title')}</h2>
            <dl className={styles.sideRows}>
              <div className={styles.sideRow}>
                <dt>{t('pages.ideas.col_status')}</dt>
                <dd>
                  <StatusBadge status={detail.status} />
                </dd>
              </div>
              <div className={styles.sideRow}>
                <dt>{t('pages.ideas.col_support')}</dt>
                <dd>
                  {detail.evidence.support} / {detail.evidence.oppose}
                </dd>
              </div>
              <div className={styles.sideRow}>
                <dt>{t('pages.ideas.col_drafts')}</dt>
                <dd>{detail.evidence.drafts}</dd>
              </div>
              <div className={styles.sideRow}>
                <dt>{t('pages.ideas.sidebar_derived_from')}</dt>
                <dd>
                  {detail.relations.derivedFrom.length === 0 ? (
                    <span className={styles.muted}>{t('pages.ideas.rel_none')}</span>
                  ) : (
                    detail.relations.derivedFrom.map((ref) => {
                      const href = entityRoute(ref, projectId)
                      return href === undefined ? (
                        <span key={ref.id}>{ref.title}</span>
                      ) : (
                        <button
                          key={ref.id}
                          type="button"
                          className={styles.linkButton}
                          onClick={() => navigate(href)}
                        >
                          {ref.title}
                        </button>
                      )
                    })
                  )}
                </dd>
              </div>
              <div className={styles.sideRow}>
                <dt>{t('pages.ideas.sidebar_derived')}</dt>
                <dd>{detail.relations.derived.length}</dd>
              </div>
              <div className={styles.sideRow}>
                <dt>{t('pages.ideas.sidebar_created')}</dt>
                <dd>{formatTime(detail.createdAt)}</dd>
              </div>
              <div className={styles.sideRow}>
                <dt>{t('pages.ideas.sidebar_updated')}</dt>
                <dd>{formatTime(detail.updatedAt)}</dd>
              </div>
            </dl>
          </aside>
        </div>
      )}

      {tab === 'evidence' && (
        <EvidencePanel projectId={projectId} idea={detail} archived={archived} />
      )}
      {tab === 'relations' && <LinksPanel projectId={projectId} idea={detail} archived={archived} />}
      {tab === 'history' && <HistoryTimeline projectId={projectId} ideaId={entityId} />}
      {tab === 'lineage' && <LineageGraph projectId={projectId} ideaId={entityId} />}
      {tab === 'backlinks' && <BacklinksPanel projectId={projectId} ideaId={entityId} />}
      {tab === 'readiness' && <ReadinessPanel projectId={projectId} ideaId={entityId} />}

      <StatusDialog
        key={`status-${entityId}`}
        open={statusTarget !== null}
        projectId={projectId}
        idea={detail}
        target={statusTarget}
        onClose={() => setStatusTarget(null)}
        onChanged={() => {
          setStatusTarget(null)
          void invalidate()
        }}
      />
      <DeriveDialog
        key={`derive-${entityId}`}
        open={deriveOpen}
        projectId={projectId}
        parent={detail}
        onClose={() => setDeriveOpen(false)}
        onDerived={(child) => {
          setDeriveOpen(false)
          const href = routeHref('idea', { projectId, entityId: child.id })
          if (href !== undefined) navigate(href)
        }}
      />
      <ConfirmDialog
        open={deleteOpen}
        title={t('pages.ideas.delete_title')}
        description={t('pages.ideas.delete_message', { title: detail.title })}
        confirmLabel={t('pages.ideas.delete_confirm')}
        cancelLabel={t('pages.ideas.ai_cancel')}
        tone="danger"
        onCancel={() => setDeleteOpen(false)}
        onConfirm={() => {
          setDeleteOpen(false)
          ideasApi
            .delete(projectId, entityId)
            .then(async () => {
              await invalidate()
              navigate(routeHref('ideas', { projectId }) as string)
            })
            .catch((cause: unknown) => {
              setActionError(
                cause instanceof Error ? cause.message : t('pages.ideas.toast_delete_fail')
              )
            })
        }}
      />
    </Page>
  )
}