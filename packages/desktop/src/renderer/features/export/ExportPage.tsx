import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import type { ExportFormat, ExportPlan } from '@researchos/shared'

import {
  Banner,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Mono,
  Page,
  PageHeader,
  ProgressBar,
  Row,
  Rows,
  SelectField,
  Skeleton,
  TextInput
} from '../../design/components'
import { useCurrentProject } from '../../app/useCurrentProject'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { formatBytes } from '../../lib/bytes'
import { ImportProjectDialog } from '../import/ImportProjectDialog'

const FORMATS: readonly { value: ExportFormat; key: string }[] = [
  { value: 'folder', key: 'pages.export.format_folder' },
  { value: 'zip', key: 'pages.export.format_zip' },
  { value: 'tar.gz', key: 'pages.export.format_targz' }
]

/**
 * Export dialog (Go pages/export.go): destination and format, preflight plan,
 * then a background job. The import side lives beside it because both operate
 * on the same project bundle. Cancelling an export has no channel yet and is
 * omitted rather than faked (reported gap).
 */
export default function ExportPage(): JSX.Element {
  const t = useT()
  const { projectId, project, archived } = useCurrentProject()

  const [format, setFormat] = useState<ExportFormat>('zip')
  const [dest, setDest] = useState('')
  const [plan, setPlan] = useState<ExportPlan | null>(null)
  const [planError, setPlanError] = useState<unknown>(null)
  const [validation, setValidation] = useState<string | null>(null)
  const [jobId, setJobId] = useState<string | null>(null)
  const [startError, setStartError] = useState<unknown>(null)
  const [importOpen, setImportOpen] = useState(false)
  const [busy, setBusy] = useState(false)

  const jobQuery = useQuery({
    queryKey: queryKeys.exportJob(jobId ?? 'none'),
    queryFn: () => ipc.invoke('export.project.status', { jobId: jobId as string }),
    enabled: jobId !== null,
    refetchInterval: (query) => {
      const state = query.state.data?.state
      return state === 'queued' || state === 'running' ? 1000 : false
    }
  })

  if (projectId === null) {
    return (
      <Page>
        <PageHeader title={t('pages.export.title')} />
        <EmptyState
          title={t('pages.common.empty_no_project_title')}
          description={t('pages.export.disabled_reason')}
        />
      </Page>
    )
  }

  if (project === null) {
    return (
      <Page>
        <PageHeader title={t('pages.export.title')} />
        <Skeleton rows={4} />
      </Page>
    )
  }

  const browse = async (): Promise<void> => {
    try {
      const result = await ipc.invoke('app.pickFolder', {
        title: t('pages.export.btn_choose'),
        defaultPath: dest === '' ? undefined : dest
      })
      const picked = result.paths[0]
      if (!result.canceled && picked !== undefined) {
        setDest(picked)
        setPlan(null)
        setValidation(null)
      }
    } catch (cause) {
      setPlanError(cause)
    }
  }

  const preflight = async (): Promise<void> => {
    setValidation(null)
    setPlanError(null)
    setPlan(null)
    if (dest.trim() === '') {
      setValidation(t('pages.export.dest_missing'))
      return
    }
    setBusy(true)
    try {
      const result = await ipc.invoke('export.project.plan', {
        projectId,
        format,
        destDir: dest.trim()
      })
      setPlan(result)
    } catch (cause) {
      setPlanError(cause)
    } finally {
      setBusy(false)
    }
  }

  const start = async (): Promise<void> => {
    setStartError(null)
    setBusy(true)
    try {
      const job = await ipc.invoke('export.project.start', {
        projectId,
        format,
        destDir: dest.trim()
      })
      setJobId(job.jobId)
    } catch (cause) {
      setStartError(cause)
    } finally {
      setBusy(false)
    }
  }

  const insufficient = plan !== null && plan.requiredBytes > plan.freeBytes
  const job = jobQuery.data
  const running = job?.state === 'queued' || job?.state === 'running'

  return (
    <Page>
      <PageHeader
        title={t('pages.export.title')}
        actions={
          <Button variant="secondary" size="sm" onClick={() => setImportOpen(true)}>
            {t('pages.export.import_title')}
          </Button>
        }
      />
      {archived && (
        <Banner tone="warning" title={t('shell.overview.readonly_title')}>
          {t('shell.overview.readonly_message')}
        </Banner>
      )}

      <Card title={project.name}>
        <SelectField
          label={t('pages.export.field_format')}
          value={format}
          options={FORMATS.map((item) => ({ value: item.value, label: t(item.key) }))}
          onChange={(value) => {
            const next = FORMATS.find((item) => item.value === value)
            if (next !== undefined) {
              setFormat(next.value)
              setPlan(null)
            }
          }}
          disabled={running}
        />
        <TextInput
          label={t('pages.export.field_dest')}
          value={dest}
          onChange={(value) => {
            setDest(value)
            setPlan(null)
            setValidation(null)
          }}
          hint={t('pages.export.field_dest_hint')}
          error={validation ?? undefined}
          disabled={running}
        />
        <div>
          <Button variant="secondary" size="sm" onClick={() => void browse()} disabled={running}>
            {t('pages.export.btn_choose')}
          </Button>{' '}
          <Button
            variant="primary"
            size="sm"
            onClick={() => void preflight()}
            disabled={busy || running || archived}
          >
            {t('pages.export.btn_preflight')}
          </Button>
        </div>
      </Card>

      {planError !== null && (
        <ErrorState
          title={t('pages.export.error_plan')}
          message={planError instanceof Error ? planError.message : String(planError)}
          retryLabel={t('shell.overview.error_retry')}
          onRetry={() => void preflight()}
        />
      )}

      {plan !== null && (
        <Card
          title={t('pages.export.row_output')}
          actions={
            <Button
              variant="primary"
              size="sm"
              disabled={busy || running || insufficient || archived}
              onClick={() => void start()}
            >
              {t('pages.export.btn_start')}
            </Button>
          }
        >
          <Rows>
            <Row label={t('pages.export.row_output')} value={plan.destination} mono />
            <Row
              label={t('pages.export.row_files')}
              value={t('pages.export.plan_files', { count: plan.files.length })}
            />
            <Row label={t('pages.export.row_size')} value={formatBytes(plan.requiredBytes, t)} />
            <Row label={t('pages.export.row_free')} value={formatBytes(plan.freeBytes, t)} />
          </Rows>
          {insufficient && (
            <Banner tone="danger">
              {t('pages.export.free_insufficient', {
                need: formatBytes(plan.requiredBytes, t),
                free: formatBytes(plan.freeBytes, t)
              })}
            </Banner>
          )}
          {plan.warnings.length > 0 && (
            <Banner tone="warning" title={t('pages.export.warning_title')}>
              <ul>
                {plan.warnings.map((warning) => (
                  <li key={warning}>{warning}</li>
                ))}
              </ul>
            </Banner>
          )}
        </Card>
      )}

      {startError !== null && (
        <Banner tone="danger" title={t('pages.export.error_start')}>
          {startError instanceof Error ? startError.message : String(startError)}
        </Banner>
      )}

      {job !== undefined && (
        <Card title={t('pages.export.job_running')}>
          <ProgressBar
            value={job.percent}
            label={job.stage === '' ? t('pages.export.job_running') : job.stage}
          />
          <p>
            <Mono>{job.stage}</Mono>
          </p>
          {job.state === 'succeeded' && job.artifact !== null && (
            <Banner tone="success" title={t('pages.export.state_done')}>
              <Mono>{job.artifact.path}</Mono>
            </Banner>
          )}
          {job.state === 'failed' && (
            <Banner tone="danger" title={t('pages.export.toast_failed')}>
              {job.error ?? ''}
            </Banner>
          )}
        </Card>
      )}

      <ImportProjectDialog open={importOpen} onClose={() => setImportOpen(false)} />
    </Page>
  )
}