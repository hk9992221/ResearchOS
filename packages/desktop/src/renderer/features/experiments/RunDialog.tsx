import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'

import type { RunStatus, RunSummary } from '@researchos/shared'

import { Button, Modal, SelectField, TextInput } from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { experimentsApi } from './api'
import { paramsToRows, parseParamRows, runStatusKey, type ParamRow } from './model'
import { ParamsEditor } from './ParamsEditor'
import styles from './experiments.module.css'

const STATUSES: readonly RunStatus[] = ['unknown', 'running', 'finished', 'failed']

export interface RunDialogProps {
  readonly open: boolean
  readonly projectId: string
  readonly experimentId: string
  readonly experimentDirRef: string | null
  /** null creates a run; a summary edits it. */
  readonly run: RunSummary | null
  readonly onClose: () => void
  readonly onSaved: () => void
  readonly notify: (input: {
    tone: 'success' | 'error' | 'warning' | 'info'
    title: string
    message?: string
  }) => void
}

/**
 * Create or edit one run. Parameters are a string map; the directory is
 * optional and inherits the experiment's folder when empty. Editing sends the
 * whole patch (`dirRef: null` means inherit) because the service treats an
 * absent field as "unchanged".
 */
export function RunDialog({
  open,
  projectId,
  experimentId,
  experimentDirRef,
  run,
  onClose,
  onSaved,
  notify
}: RunDialogProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [title, setTitle] = useState('')
  const [dir, setDir] = useState('')
  const [status, setStatus] = useState<RunStatus>('unknown')
  const [rows, setRows] = useState<ParamRow[]>([{ key: '', value: '' }])
  const [error, setError] = useState<'key_required' | 'duplicate_key' | 'title_required' | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open) return
    setTitle(run?.title ?? '')
    setDir(run?.dirRef ?? '')
    setStatus(run?.status ?? 'unknown')
    setRows(paramsToRows(run?.params))
    setError(null)
  }, [open, run])

  const close = (): void => {
    if (busy) return
    onClose()
  }

  const chooseFolder = async (): Promise<void> => {
    try {
      const result = await ipc.invoke('app.pickFolder', {
        title: t('pages.experiments.run_dir_label')
      })
      if (!result.canceled && result.paths[0] !== undefined) setDir(result.paths[0])
    } catch {
      // A cancelled or unavailable picker leaves the typed path untouched.
    }
  }

  const submit = async (): Promise<void> => {
    const parsed = parseParamRows(rows)
    if (parsed.error !== undefined) {
      setError(parsed.error)
      return
    }
    const trimmedTitle = title.trim()
    if (run !== null && trimmedTitle === '') {
      setError('title_required')
      return
    }
    setError(null)
    setBusy(true)
    try {
      if (run === null) {
        await experimentsApi.createRun({
          projectId,
          experimentId,
          ...(trimmedTitle === '' ? {} : { title: trimmedTitle }),
          ...(dir.trim() === '' ? {} : { dirRef: dir.trim() }),
          ...(parsed.params === undefined ? {} : { params: parsed.params }),
          status
        })
        notify({ tone: 'success', title: t('pages.experiments.toast_run_created') })
      } else {
        await experimentsApi.updateRun(projectId, run.id, {
          title: trimmedTitle,
          dirRef: dir.trim() === '' ? null : dir.trim(),
          ...(parsed.params === undefined ? {} : { params: parsed.params }),
          status
        })
        notify({ tone: 'success', title: t('pages.experiments.toast_run_saved') })
      }
      await queryClient.invalidateQueries({ queryKey: queryKeys.experiments.all(projectId) })
      onSaved()
    } catch (cause) {
      notify({
        tone: 'error',
        title:
          run === null
            ? t('pages.experiments.toast_run_failed')
            : t('pages.experiments.toast_status_failed'),
        message: cause instanceof Error ? cause.message : undefined
      })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      title={run === null ? t('pages.experiments.run_dialog_title') : t('pages.experiments.edit_title')}
      onClose={close}
      footer={
        <>
          <Button variant="secondary" onClick={close} disabled={busy}>
            {t('pages.experiments.btn_back')}
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy} aria-busy={busy}>
            {run === null ? t('pages.experiments.run_create') : t('pages.experiments.dir_save')}
          </Button>
        </>
      }
    >
      <div className={styles.stack}>
        <TextInput
          label={t('pages.experiments.field_title')}
          value={title}
          onChange={setTitle}
          placeholder={t('pages.experiments.run_unnamed')}
        />
        <div className={styles.dirRow}>
          <TextInput
            label={t('pages.experiments.run_dir_label')}
            hint={t('pages.experiments.run_dir_desc')}
            value={dir}
            onChange={setDir}
            placeholder={experimentDirRef ?? ''}
          />
          <Button variant="secondary" onClick={() => void chooseFolder()}>
            {t('pages.experiments.btn_choose_folder')}
          </Button>
          <Button variant="ghost" onClick={() => setDir('')}>
            {t('pages.experiments.run_dir_clear')}
          </Button>
        </div>
        <SelectField
          label={t('pages.experiments.btn_set_status')}
          value={status}
          onChange={(value) => setStatus(value as RunStatus)}
          options={STATUSES.map((value) => ({ value, label: t(runStatusKey(value)) }))}
        />
        <ParamsEditor rows={rows} onChange={setRows} error={error} />
        <p className={styles.reason}>{t('pages.experiments.run_dialog_desc')}</p>
      </div>
    </Modal>
  )
}