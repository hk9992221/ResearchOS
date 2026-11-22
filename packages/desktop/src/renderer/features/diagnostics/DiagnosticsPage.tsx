import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import {
  Banner,
  Button,
  Card,
  CardGrid,
  EmptyState,
  ErrorState,
  Mono,
  Page,
  PageHeader,
  Row,
  Rows,
  Skeleton,
  Table
} from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'

/**
 * Diagnostics: the self-check report. The main process exposes the
 * `app.diagnostics` findings list, the native-stack smoke result, MCP status
 * and workspace identity; database integrity/foreign-key counters and sizes
 * have no channel yet (reported as a gap) and are shown as unavailable rather
 * than invented.
 */
export default function DiagnosticsPage(): JSX.Element {
  const t = useT()
  const [requested, setRequested] = useState(false)
  const [copied, setCopied] = useState<string | null>(null)
  const [copyError, setCopyError] = useState<string | null>(null)

  const diagnosticsQuery = useQuery({
    queryKey: queryKeys.diagnostics.report,
    queryFn: () => ipc.invoke('app.diagnostics'),
    enabled: requested
  })
  const nativeQuery = useQuery({
    queryKey: queryKeys.diagnostics.nativeSmoke,
    queryFn: () => ipc.invoke('app.nativeSmoke'),
    enabled: requested
  })
  const infoQuery = useQuery({
    queryKey: ['app', 'info'],
    queryFn: () => ipc.invoke('app.info'),
    enabled: requested
  })
  const configQuery = useQuery({
    queryKey: queryKeys.config,
    queryFn: () => ipc.invoke('app.config.get'),
    enabled: requested
  })
  const projectsQuery = useQuery({
    queryKey: queryKeys.projects.list({ includeArchived: true }),
    queryFn: () => ipc.invoke('project.list', { includeArchived: true }),
    enabled: requested
  })
  const mcpQuery = useQuery({
    queryKey: queryKeys.mcp.status,
    queryFn: () => ipc.invoke('mcp.status'),
    enabled: requested
  })

  const pending =
    diagnosticsQuery.isPending ||
    nativeQuery.isPending ||
    infoQuery.isPending ||
    configQuery.isPending ||
    projectsQuery.isPending ||
    mcpQuery.isPending

  const failure = [
    diagnosticsQuery.error,
    nativeQuery.error,
    infoQuery.error,
    configQuery.error,
    projectsQuery.error,
    mcpQuery.error
  ].find((error) => error !== null && error !== undefined)

  const copy = async (): Promise<void> => {
    setCopyError(null)
    try {
      const lines: string[] = [
        t('pages.diagnostics.clip_header', {
          time: diagnosticsQuery.data?.generatedAt ?? ''
        })
      ]
      lines.push(
        t('pages.diagnostics.clip_global', {
          path: nativeQuery.data?.databasePath ?? '',
          schema: String(infoQuery.data?.schemaVersion ?? ''),
          integrity: nativeQuery.data?.ok === true ? t('pages.diagnostics.val_ok') : t('pages.diagnostics.val_failed'),
          keys: t('pages.common.status_unavailable'),
          size: t('pages.common.status_unavailable')
        })
      )
      lines.push(t('pages.diagnostics.clip_projects'))
      for (const project of projectsQuery.data ?? []) {
        lines.push(`  ${project.name}`)
      }
      lines.push(
        `${t('pages.diagnostics.card_config')}: ${infoQuery.data?.workspaceRoot ?? ''} / ${
          configQuery.data?.zoteroDataDir ?? t('pages.settings.status_not_configured')
        }`
      )
      lines.push(t('pages.diagnostics.clip_warnings'))
      const entries = diagnosticsQuery.data?.entries ?? []
      for (const entry of entries) {
        lines.push(t('pages.diagnostics.clip_warning_item', { warning: entry.message }))
      }
      if (entries.length === 0) lines.push(t('pages.diagnostics.clip_warnings_none'))
      await navigator.clipboard.writeText(lines.join('\n'))
      setCopied(t('pages.diagnostics.toast_copied_message'))
    } catch (cause) {
      setCopyError(`${t('pages.diagnostics.copy_failed')}: ${String(cause)}`)
    }
  }

  return (
    <Page>
      <PageHeader
        title={t('pages.diagnostics.title')}
        actions={
          <Button
            variant={requested ? 'secondary' : 'primary'}
            onClick={() => {
              setRequested(true)
              if (requested) {
                void Promise.all([
                  diagnosticsQuery.refetch(),
                  nativeQuery.refetch(),
                  infoQuery.refetch(),
                  configQuery.refetch(),
                  projectsQuery.refetch(),
                  mcpQuery.refetch()
                ])
              }
            }}
          >
            {requested ? t('pages.diagnostics.btn_run_again') : t('pages.diagnostics.btn_run')}
          </Button>
        }
      />

      {!requested && (
        <EmptyState
          title={t('pages.diagnostics.empty_title')}
          description={t('pages.diagnostics.empty_reason')}
        />
      )}
      {requested && pending && <Skeleton rows={6} />}
      {requested && !pending && failure !== undefined && failure !== null && (
        <ErrorState
          title={t('pages.diagnostics.banner_failed')}
          message={failure.message}
          retryLabel={t('shell.overview.error_retry')}
          onRetry={() => {
            void diagnosticsQuery.refetch()
            void nativeQuery.refetch()
            void infoQuery.refetch()
          }}
        />
      )}
      {copied !== null && <Banner tone="success">{copied}</Banner>}
      {copyError !== null && <Banner tone="danger">{copyError}</Banner>}

      {requested && !pending && (
        <>
          <CardGrid>
            <Card title={t('pages.diagnostics.card_global')}>
              <Rows>
                <Row label={t('pages.diagnostics.row_file')} value={nativeQuery.data?.databasePath ?? ''} mono />
                <Row
                  label={t('pages.diagnostics.row_schema')}
                  value={String(infoQuery.data?.schemaVersion ?? '')}
                />
                <Row
                  label={t('pages.diagnostics.row_integrity')}
                  value={
                    nativeQuery.data?.ok === true
                      ? t('pages.diagnostics.val_ok')
                      : t('pages.diagnostics.val_failed')
                  }
                />
                <Row
                  label={t('pages.diagnostics.row_foreign_keys')}
                  value={t('pages.common.status_unavailable')}
                />
                <Row
                  label={t('pages.diagnostics.row_size')}
                  value={t('pages.common.status_unavailable')}
                />
              </Rows>
            </Card>

            <Card title={t('pages.diagnostics.card_stack')}>
              <Rows>
                <Row label="FTS5" value={nativeQuery.data?.fts5Match ?? ''} />
                <Row label="sqlite-vec" value={nativeQuery.data?.vecVersion ?? ''} />
                <Row
                  label={t('pages.diagnostics.row_duration')}
                  value={
                    nativeQuery.data === undefined
                      ? t('pages.common.status_unavailable')
                      : `${nativeQuery.data.durationMs} ms`
                  }
                />
                <Row label="drizzle" value={String(nativeQuery.data?.drizzleRows ?? 0)} />
              </Rows>
            </Card>

            <Card title={t('pages.diagnostics.card_projects')}>
              <p>
                {t('pages.diagnostics.projects_registered', {
                  count: projectsQuery.data?.length ?? 0
                })}
              </p>
              {(projectsQuery.data?.length ?? 0) === 0 ? (
                <p>{t('pages.diagnostics.projects_none')}</p>
              ) : (
                <ul>
                  {projectsQuery.data?.map((project) => <li key={project.id}>{project.name}</li>)}
                </ul>
              )}
            </Card>

            <Card title={t('pages.diagnostics.card_config')}>
              <Rows>
                <Row
                  label={t('pages.diagnostics.row_workspace_root')}
                  value={infoQuery.data?.workspaceRoot ?? ''}
                  mono
                />
                <Row label={t('pages.diagnostics.row_file')} value={infoQuery.data?.platform ?? ''} />
                <Row
                  label={t('pages.diagnostics.row_zotero')}
                  value={configQuery.data?.zoteroDataDir ?? t('pages.settings.status_not_configured')}
                  mono
                />
              </Rows>
            </Card>

            <Card title={t('pages.diagnostics.card_mcp')}>
              <Rows>
                <Row
                  label={t('pages.diagnostics.row_mcp_state')}
                  value={
                    mcpQuery.data?.running === true
                      ? t('pages.diagnostics.val_mcp_running')
                      : t('pages.diagnostics.val_mcp_stopped')
                  }
                />
                <Row
                  label={t('pages.diagnostics.row_mcp_socket')}
                  value={
                    mcpQuery.data === undefined
                      ? t('pages.common.status_unavailable')
                      : `http://${mcpQuery.data.host}:${mcpQuery.data.port}${mcpQuery.data.path}`
                  }
                  mono
                />
                <Row label="tools" value={String(mcpQuery.data?.toolsCount ?? 0)} />
              </Rows>
            </Card>
          </CardGrid>

          <Card
            title={t('pages.diagnostics.entries_title')}
            actions={
              <Button variant="secondary" size="sm" onClick={() => void copy()}>
                {t('pages.diagnostics.btn_copy')}
              </Button>
            }
          >
            {(diagnosticsQuery.data?.entries.length ?? 0) === 0 ? (
              <p>{t('pages.diagnostics.entries_empty')}</p>
            ) : (
              <Table
                label={t('pages.diagnostics.entries_title')}
                columns={[
                  t('pages.diagnostics.row_area'),
                  t('pages.diagnostics.row_status'),
                  t('pages.diagnostics.row_message'),
                  t('pages.diagnostics.row_time')
                ]}
              >
                {diagnosticsQuery.data?.entries.map((entry) => (
                  <tr key={entry.id}>
                    <td>{entry.area}</td>
                    <td>
                      <Mono>{entry.status}</Mono>
                    </td>
                    <td>{entry.message}</td>
                    <td>
                      <Mono>{entry.at}</Mono>
                    </td>
                  </tr>
                ))}
              </Table>
            )}
          </Card>

          </>
      )}
    </Page>
  )
}