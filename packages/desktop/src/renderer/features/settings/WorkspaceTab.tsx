import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'

import { Button, Card, ErrorState, Row, Rows, SelectField, Skeleton } from '../../design/components'
import { useI18n, useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { routeHref } from '../../routes'
import { useShellStore } from '../../state/shell'
import { formatBytes } from '../../lib/bytes'

/** Workspace section: location, projects, footprint, marker, language. */
export function WorkspaceTab(): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const { language, preference, available, setLanguage } = useI18n()
  const projectId = useShellStore((state) => state.currentProjectId)

  const statusQuery = useQuery({
    queryKey: queryKeys.workspace.status(),
    queryFn: () => ipc.invoke('workspace.status')
  })
  const projectsQuery = useQuery({
    queryKey: queryKeys.projects.list({ includeArchived: true }),
    queryFn: () => ipc.invoke('project.list', { includeArchived: true })
  })
  const scanQuery = useQuery({
    queryKey: ['workspace', 'scan'],
    queryFn: () => ipc.invoke('workspace.scan', {})
  })

  if (statusQuery.isPending || projectsQuery.isPending) return <Skeleton rows={4} />
  if (statusQuery.isError) {
    return (
      <ErrorState
        title={t('pages.settings.banner_load_fail')}
        message={statusQuery.error.message}
        retryLabel={t('shell.overview.error_retry')}
        onRetry={() => void statusQuery.refetch()}
      />
    )
  }

  const status = statusQuery.data
  const projects = projectsQuery.data ?? []
  const archived = projects.filter((project) => project.status === 'archived').length
  const marker =
    status.markerVersion === null
      ? t('pages.settings.marker_missing')
      : status.schemaVersion === null
        ? t('pages.settings.marker_damaged')
        : t('pages.settings.marker_present')

  const trashHref =
    projectId === null ? undefined : routeHref('trash', { projectId })
  const consistencyHref =
    projectId === null ? undefined : routeHref('consistency', { projectId })

  return (
    <>
      <Card title={t('pages.settings.tab_workspace')}>
        <Rows>
          <Row label={t('pages.settings.row_location')} value={status.root ?? '—'} mono />
          <Row
            label={t('pages.settings.row_projects')}
            value={t('pages.settings.projects_pair', {
              total: projects.length,
              archived
            })}
          />
          <Row
            label={t('pages.settings.row_workspace_size')}
            value={
              scanQuery.data === undefined
                ? t('pages.common.status_unavailable')
                : formatBytes(scanQuery.data.totalBytes, t)
            }
          />
          <Row
            label={t('pages.settings.row_database')}
            value={
              scanQuery.data === undefined
                ? t('pages.common.status_unavailable')
                : formatBytes(scanQuery.data.dbBytes, t)
            }
          />
          <Row
            label={t('pages.settings.row_library')}
            value={
              scanQuery.data === undefined
                ? t('pages.common.status_unavailable')
                : formatBytes(scanQuery.data.libraryBytes, t)
            }
          />
          <Row
            label={t('pages.settings.row_free_space')}
            value={
              scanQuery.data === undefined || scanQuery.data.freeBytes === null
                ? t('pages.common.status_unavailable')
                : formatBytes(scanQuery.data.freeBytes, t)
            }
          />
          <Row label={t('pages.settings.row_marker')} value={marker} />
          <Row
            label={t('pages.settings.row_schema')}
            value={status.schemaVersion === null ? '—' : String(status.schemaVersion)}
          />
        </Rows>
        {status.warnings.length > 0 && (
          <ul>
            {status.warnings.map((warning) => (
              <li key={warning}>{warning}</li>
            ))}
          </ul>
        )}
        <div>
          <Button
            variant="secondary"
            size="sm"
            disabled={projectId === null}
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
          </Button>{' '}
          <Button variant="ghost" size="sm" onClick={() => navigate('/welcome')}>
            {t('pages.settings.btn_change_location')}
          </Button>
        </div>
      </Card>

      <Card title={t('settings.language.title')}>
        <SelectField
          label={t('settings.language.label')}
          value={preference}
          options={[
            {
              value: 'system',
              label: t('settings.language.system_hint', { language })
            },
            ...available.map((option) => ({ value: option.tag, label: option.nativeName }))
          ]}
          onChange={(value) => setLanguage(value)}
          hint={t('settings.language.description')}
        />
      </Card>
    </>
  )
}