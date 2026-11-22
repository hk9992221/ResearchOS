import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import type { ImportInspectPlan } from '@researchos/shared'

import {
  Banner,
  Button,
  Modal,
  Mono,
  Row,
  Rows,
  SwitchField,
  TextInput
} from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { formatBytes } from '../../lib/bytes'

export interface ImportProjectDialogProps {
  readonly open: boolean
  readonly onClose: () => void
}

/** Two-phase project import: inspect the bundle, then confirm the plan. */
export function ImportProjectDialog({ open, onClose }: ImportProjectDialogProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [source, setSource] = useState('')
  const [plan, setPlan] = useState<ImportInspectPlan | null>(null)
  const [newIdentity, setNewIdentity] = useState(false)
  const [jobId, setJobId] = useState<string | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [validation, setValidation] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const jobQuery = useQuery({
    queryKey: queryKeys.importJob(jobId ?? 'none'),
    queryFn: () => ipc.invoke('export.import.status', { jobId: jobId as string }),
    enabled: jobId !== null,
    refetchInterval: (query) => {
      const state = query.state.data?.state
      return state === 'queued' || state === 'running' ? 1000 : false
    }
  })

  const choose = async (kind: 'archive' | 'folder'): Promise<void> => {
    setValidation(null)
    try {
      const result =
        kind === 'archive'
          ? await ipc.invoke('app.pickFile', {
              title: t('pages.export.import_choose_archive'),
              filters: [{ name: t('pages.export.import_title'), extensions: ['zip', 'tar.gz'] }]
            })
          : await ipc.invoke('app.pickFolder', { title: t('pages.export.import_choose_folder') })
      const picked = result.paths[0]
      if (!result.canceled && picked !== undefined) {
        setSource(picked)
        setPlan(null)
        setJobId(null)
      }
    } catch (cause) {
      setError(cause)
    }
  }

  const inspect = async (): Promise<void> => {
    setValidation(null)
    setError(null)
    setPlan(null)
    setJobId(null)
    if (source.trim() === '') {
      setValidation(t('pages.export.disabled_reason'))
      return
    }
    setBusy(true)
    try {
      const result = await ipc.invoke('export.import.inspect', { sourcePath: source.trim() })
      setPlan(result)
      setNewIdentity(false)
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const start = async (): Promise<void> => {
    if (plan === null) return
    setError(null)
    setBusy(true)
    try {
      const job = await ipc.invoke('export.import.start', {
        planId: plan.planId,
        newIdentity
      })
      setJobId(job.jobId)
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const close = (): void => {
    if (jobId !== null && jobQuery.data?.state === 'succeeded') {
      void queryClient.invalidateQueries({ queryKey: queryKeys.projects.all })
      void queryClient.invalidateQueries({ queryKey: queryKeys.workspace.all })
    }
    setSource('')
    setPlan(null)
    setJobId(null)
    setError(null)
    onClose()
  }

  const canImport = plan !== null && plan.canImport && (!plan.idConflict || newIdentity)
  const job = jobQuery.data
  const running = job?.state === 'queued' || job?.state === 'running'
  const succeeded = job?.state === 'succeeded'

  return (
    <Modal
      open={open}
      title={t('pages.export.import_title')}
      onClose={close}
      width={560}
      footer={
        <>
          <Button variant="secondary" onClick={close}>
            {t('pages.export.btn_back')}
          </Button>
          {plan === null && (
            <Button variant="primary" onClick={() => void inspect()} disabled={busy}>
              {t('pages.export.import_inspect')}
            </Button>
          )}
          {plan !== null && (
            <Button
              variant="primary"
              onClick={() => void start()}
              disabled={busy || running || !canImport}
            >
              {t('pages.export.import_start')}
            </Button>
          )}
        </>
      }
    >
      <TextInput
        label={t('pages.export.field_dest')}
        value={source}
        onChange={(value) => {
          setSource(value)
          setPlan(null)
          setValidation(null)
        }}
        error={validation ?? undefined}
        disabled={running}
      />
      <div>
        <Button variant="ghost" size="sm" onClick={() => void choose('archive')}>
          {t('pages.export.import_choose_archive')}
        </Button>{' '}
        <Button variant="ghost" size="sm" onClick={() => void choose('folder')}>
          {t('pages.export.import_choose_folder')}
        </Button>
      </div>

      {error !== null && (
        <Banner tone="danger" title={t('pages.export.import_failed')}>
          {error instanceof Error ? error.message : String(error)}
        </Banner>
      )}

      {plan !== null && (
        <>
          <Banner tone={plan.canImport ? 'success' : 'danger'}>
            {plan.canImport ? t('pages.export.import_ok') : t('pages.export.import_blocked')}
          </Banner>
          <Rows>
            <Row label={t('pages.export.field_name')} value={plan.projectName} />
            <Row label={t('pages.export.field_format')} value={plan.container} />
            <Row
              label={t('pages.export.row_files')}
              value={t('pages.export.plan_files', { count: plan.fileCount })}
            />
            <Row label={t('pages.export.row_size')} value={formatBytes(plan.totalBytes, t)} />
            <Row label={t('pages.diagnostics.row_schema')} value={String(plan.schemaVersion)} />
          </Rows>
          {plan.idConflict && (
            <>
              <Banner tone="warning">{t('pages.export.import_id_conflict')}</Banner>
              <SwitchField
                label={t('pages.export.import_new_identity')}
                checked={newIdentity}
                onChange={setNewIdentity}
              />
            </>
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
        </>
      )}

      {job !== undefined && (
        <>
          <p>
            <Mono>{t('pages.export.import_running')}</Mono> — <Mono>{job.stage}</Mono>
          </p>
          {succeeded && (
            <Banner tone="success" title={t('pages.export.import_done')}>
              {job.result?.projectName ?? ''}
            </Banner>
          )}
          {job.state === 'failed' && (
            <Banner tone="danger" title={t('pages.export.import_failed')}>
              {job.error ?? ''}
            </Banner>
          )}
        </>
      )}
    </Modal>
  )
}