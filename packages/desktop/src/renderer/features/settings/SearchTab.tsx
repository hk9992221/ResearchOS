import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import {
  Badge,
  Banner,
  Button,
  Card,
  ErrorState,
  ProgressBar,
  Row,
  Rows,
  Skeleton
} from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { useShellStore } from '../../state/shell'
import { registerJobCanceller } from '../../state/tasks'

interface IndexCardProps {
  readonly scope: 'global' | 'project'
  readonly projectId?: string
}

function IndexCard({ scope, projectId }: IndexCardProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [jobId, setJobId] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const cancelRegistration = useRef<(() => void) | null>(null)

  useEffect(
    () => () => {
      cancelRegistration.current?.()
    },
    []
  )

  const statusQuery = useQuery({
    queryKey: queryKeys.searchIndex.status(scope, projectId),
    queryFn: () => ipc.invoke('search.index.status', { scope, projectId }),
    refetchInterval: (query) => (query.state.data?.indexing === true ? 1500 : false)
  })

  const status = statusQuery.data
  const activeJob = status?.activeJobs[0]

  const start = async (rebuild: boolean): Promise<void> => {
    setBusy(true)
    setActionError(null)
    setNotice(null)
    try {
      const job = await ipc.invoke('search.index.reindex', {
        scope,
        projectId,
        reparsePdf: rebuild,
        rebuildVector: rebuild
      })
      setJobId(job.jobId)
      // The reindex is a background job: the task strip shows it from
      // job.progress and offers Cancel through this registration.
      cancelRegistration.current?.()
      cancelRegistration.current = registerJobCanceller(job.jobId, () => {
        void ipc.invoke('search.index.cancel', { jobId: job.jobId })
      })
      setNotice(t('pages.settings.search_toast_started'))
      await queryClient.invalidateQueries({
        queryKey: queryKeys.searchIndex.status(scope, projectId)
      })
    } catch (cause) {
      setActionError(cause)
    } finally {
      setBusy(false)
    }
  }

  const cancel = async (): Promise<void> => {
    const target = jobId ?? activeJob?.jobId
    if (target === undefined) return
    setBusy(true)
    setActionError(null)
    try {
      await ipc.invoke('search.index.cancel', { jobId: target })
      setNotice(t('pages.settings.search_toast_canceled'))
      await queryClient.invalidateQueries({
        queryKey: queryKeys.searchIndex.status(scope, projectId)
      })
      setJobId(null)
    } catch (cause) {
      setActionError(cause)
    } finally {
      setBusy(false)
    }
  }

  const title =
    scope === 'global' ? t('pages.settings.search_global') : t('pages.settings.search_project')

  if (statusQuery.isPending) {
    return (
      <Card title={title}>
        <Skeleton rows={3} />
      </Card>
    )
  }
  if (statusQuery.isError || status === undefined) {
    return (
      <Card title={title}>
        <ErrorState
          title={t('pages.settings.search_unavailable_title')}
          message={statusQuery.error?.message ?? t('pages.settings.search_unavailable_reason')}
          retryLabel={t('shell.overview.error_retry')}
          onRetry={() => void statusQuery.refetch()}
        />
      </Card>
    )
  }

  const progress =
    activeJob !== undefined && activeJob.total > 0
      ? (activeJob.processed / activeJob.total) * 100
      : status.progress

  return (
    <Card title={title} actions={status.stale ? <Badge tone="warning">{t('pages.settings.search_stale')}</Badge> : undefined}>
      <Rows>
        <Row label={t('pages.settings.search_docs')} value={String(status.docs)} />
        <Row label={t('pages.settings.search_chunks')} value={String(status.chunks)} />
        <Row label={t('pages.settings.search_dirty')} value={String(status.dirty)} />
        <Row label={t('pages.settings.search_failed')} value={String(status.failed)} />
        <Row label={t('pages.settings.search_model')} value={status.embeddingModel} mono />
        <Row
          label={t('pages.settings.search_vectors')}
          value={
            status.vectorAvailable
              ? t('pages.common.bool_yes')
              : t('pages.common.bool_no')
          }
        />
        <Row
          label={t('pages.settings.search_updated')}
          value={status.lastIndexedAt ?? t('pages.settings.search_never')}
        />
      </Rows>
      {status.lastError !== null && <Banner tone="warning">{status.lastError}</Banner>}
      {!status.vectorAvailable && (
        <Banner tone="warning">{t('shell.search.degraded_vector')}</Banner>
      )}
      {status.indexing && (
        <div>
          <ProgressBar value={progress} label={t('pages.settings.search_task_reindex')} />
        </div>
      )}
      {notice !== null && <Banner tone="success">{notice}</Banner>}
      {actionError !== null && (
        <Banner tone="danger" title={t('pages.settings.search_status_fail')}>
          {actionError instanceof Error ? actionError.message : String(actionError)}
        </Banner>
      )}
      <div>
        <Button variant="secondary" size="sm" disabled={busy} onClick={() => void start(false)}>
          {t('pages.settings.search_incremental')}
        </Button>{' '}
        <Button variant="secondary" size="sm" disabled={busy} onClick={() => void start(true)}>
          {t('pages.settings.search_rebuild')}
        </Button>{' '}
        {(status.indexing || jobId !== null) && (
          <Button variant="ghost" size="sm" disabled={busy} onClick={() => void cancel()}>
            {t('pages.settings.search_cancel')}
          </Button>
        )}
      </div>
    </Card>
  )
}

/** Search index section: one status card per scope, rebuild and cancel. */
export function SearchTab(): JSX.Element {
  const projectId = useShellStore((state) => state.currentProjectId)
  return (
    <>
      <IndexCard scope="global" />
      {projectId !== null && <IndexCard scope="project" projectId={projectId} />}
    </>
  )
}