import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'

import { Banner, Button, Card, ErrorState, Row, Rows, Skeleton } from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { routeHref } from '../../routes'
import { useShellStore } from '../../state/shell'
import { formatBytes } from '../../lib/bytes'

/** Storage section: workspace footprint and the maintenance destinations. */
export function StorageTab(): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const projectId = useShellStore((state) => state.currentProjectId)
  const [refreshedAt, setRefreshedAt] = useState<string | null>(null)

  const scanQuery = useQuery({
    queryKey: ['workspace', 'scan'],
    queryFn: () => ipc.invoke('workspace.scan', {})
  })

  if (scanQuery.isPending) return <Skeleton rows={3} />
  if (scanQuery.isError) {
    return (
      <ErrorState
        title={t('pages.settings.workspace_size_missing')}
        message={scanQuery.error.message}
        retryLabel={t('shell.overview.error_retry')}
        onRetry={() => void scanQuery.refetch()}
      />
    )
  }

  const trashHref = projectId === null ? undefined : routeHref('trash', { projectId })
  const consistencyHref =
    projectId === null ? undefined : routeHref('consistency', { projectId })

  return (
    <>
      <Card title={t('pages.settings.tab_storage')}>
        <Rows>
          <Row
            label={t('pages.settings.row_workspace_size')}
            value={formatBytes(scanQuery.data.totalBytes, t)}
          />
          <Row
            label={t('pages.settings.row_free_space')}
            value={
              scanQuery.data.freeBytes === null
                ? t('pages.common.status_unavailable')
                : formatBytes(scanQuery.data.freeBytes, t)
            }
          />
          <Row label={t('pages.settings.row_location')} value={scanQuery.data.root} mono />
        </Rows>
        {refreshedAt !== null && <Banner tone="success">{refreshedAt}</Banner>}
        <div>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => {
              void scanQuery.refetch().then(() => {
                setRefreshedAt(t('pages.settings.task_load'))
              })
            }}
          >
            {t('shell.overview.btn_refresh')}
          </Button>
        </div>
      </Card>
      <Card>
        <div>
          <Button
            variant="secondary"
            size="sm"
            disabled={trashHref === undefined}
            onClick={() => trashHref !== undefined && navigate(trashHref)}
          >
            {t('pages.settings.btn_recycle_bin')}
          </Button>{' '}
          <Button
            variant="secondary"
            size="sm"
            disabled={consistencyHref === undefined}
            onClick={() => consistencyHref !== undefined && navigate(consistencyHref)}
          >
            {t('pages.settings.btn_file_consistency')}
          </Button>{' '}
          <Button
            variant="secondary"
            size="sm"
            onClick={() => navigate(routeHref('diagnostics') ?? '/diagnostics')}
          >
            {t('pages.settings.btn_diagnostics')}
          </Button>
        </div>
      </Card>
    </>
  )
}