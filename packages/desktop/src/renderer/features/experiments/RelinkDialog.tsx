import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'

import type { ExperimentView } from '@researchos/shared'

import { Button, Modal, TextInput } from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { experimentsApi } from './api'
import styles from './experiments.module.css'

export interface RelinkDialogProps {
  readonly open: boolean
  readonly projectId: string
  readonly experiment: ExperimentView | null
  readonly onClose: () => void
  readonly onSaved: (path: string) => void
}

/**
 * Bind or relocate the referenced folder. `experiments.update` carries the new
 * `dirRef`; that patch is the IPC equivalent of the MCP `experiment_relink_dir`
 * tool (there is no separate renderer channel). Old analyses keep their own
 * recorded paths.
 */
export function RelinkDialog({
  open,
  projectId,
  experiment,
  onClose,
  onSaved
}: RelinkDialogProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [path, setPath] = useState(experiment?.dirRef ?? '')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open) return
    setPath(experiment?.dirRef ?? '')
    setError(null)
  }, [open, experiment?.dirRef])

  const close = (): void => {
    if (busy) return
    setError(null)
    setPath('')
    onClose()
  }

  const choose = async (): Promise<void> => {
    try {
      const result = await ipc.invoke('app.pickFolder', {
        title: t('pages.experiments.relocate_title'),
        ...(experiment?.dirRef === null || experiment?.dirRef === undefined
          ? {}
          : { defaultPath: experiment.dirRef })
      })
      if (!result.canceled && result.paths[0] !== undefined) setPath(result.paths[0])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('pages.experiments.err_dir_guard'))
    }
  }

  const save = async (): Promise<void> => {
    if (experiment === null) return
    const trimmed = path.trim()
    if (trimmed === '') {
      setError(t('pages.experiments.dir_required'))
      return
    }
    setBusy(true)
    setError(null)
    try {
      // relink_dir: the update patch is the relink operation.
      await experimentsApi.update(
        projectId,
        experiment.id,
        { dirRef: trimmed },
        experiment.updatedAt
      )
      await queryClient.invalidateQueries({
        queryKey: queryKeys.experiments.detail(projectId, experiment.id)
      })
      await queryClient.invalidateQueries({ queryKey: queryKeys.experiments.all(projectId) })
      onSaved(trimmed)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('pages.experiments.err_dir_guard'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      title={t('pages.experiments.relocate_title')}
      onClose={close}
      footer={
        <>
          <Button variant="secondary" onClick={close} disabled={busy}>
            {t('pages.experiments.btn_back')}
          </Button>
          <Button variant="primary" onClick={() => void save()} disabled={busy} aria-busy={busy}>
            {t('pages.experiments.dir_save')}
          </Button>
        </>
      }
    >
      <div className={styles.stack}>
        <p className={styles.muted}>{t('pages.experiments.dir_dialog_desc')}</p>
        <div className={styles.dirRow}>
          <TextInput
            label={t('pages.experiments.field_dir')}
            hint={t('pages.experiments.field_dir_hint')}
            value={path}
            onChange={setPath}
            autoFocus
          />
          <Button variant="secondary" onClick={() => void choose()}>
            {t('pages.experiments.btn_choose_folder')}
          </Button>
        </div>
        {error !== null && (
          <p className={styles.errorText} role="alert">
            {error}
          </p>
        )}
      </div>
    </Modal>
  )
}