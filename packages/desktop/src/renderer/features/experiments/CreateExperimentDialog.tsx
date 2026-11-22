import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'

import type { ExperimentView } from '@researchos/shared'

import { Button, Modal, TextInput } from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { experimentsApi } from './api'
import styles from './experiments.module.css'

export interface CreateExperimentDialogProps {
  readonly open: boolean
  readonly projectId: string
  readonly onClose: () => void
  readonly onCreated: (experiment: ExperimentView) => void
}

/**
 * New experiment: title is required; purpose and a directory are optional.
 * The directory comes from the native picker and is only referenced — the
 * service validates that it is absolute and outside the workspace.
 */
export function CreateExperimentDialog({
  open,
  projectId,
  onClose,
  onCreated
}: CreateExperimentDialogProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [title, setTitle] = useState('')
  const [purpose, setPurpose] = useState('')
  const [dirRef, setDirRef] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const close = (): void => {
    if (busy) return
    setTitle('')
    setPurpose('')
    setDirRef('')
    setError(null)
    onClose()
  }

  const chooseFolder = async (): Promise<void> => {
    try {
      const result = await ipc.invoke('app.pickFolder', {
        title: t('pages.experiments.bind_title')
      })
      if (!result.canceled && result.paths[0] !== undefined) setDirRef(result.paths[0])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('pages.experiments.err_dir_guard'))
    }
  }

  const submit = async (): Promise<void> => {
    const trimmed = title.trim()
    if (trimmed === '') {
      setError(t('pages.experiments.title_required'))
      return
    }
    setBusy(true)
    setError(null)
    try {
      const experiment = await experimentsApi.create(projectId, {
        title: trimmed,
        ...(purpose.trim() === '' ? {} : { purpose: purpose.trim() }),
        ...(dirRef.trim() === '' ? {} : { dirRef: dirRef.trim() })
      })
      await queryClient.invalidateQueries({ queryKey: queryKeys.experiments.all(projectId) })
      setTitle('')
      setPurpose('')
      setDirRef('')
      onCreated(experiment)
    } catch (cause) {
      const code = cause instanceof Error ? cause.message : t('pages.experiments.banner_create_failed')
      setError(code)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      title={t('pages.experiments.create_title')}
      onClose={close}
      footer={
        <>
          <Button variant="secondary" onClick={close} disabled={busy}>
            {t('pages.experiments.btn_back')}
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy} aria-busy={busy}>
            {t('pages.experiments.create_confirm')}
          </Button>
        </>
      }
    >
      <div className={styles.stack}>
        <TextInput
          label={t('pages.experiments.field_title')}
          hint={t('pages.experiments.field_title_hint')}
          value={title}
          onChange={setTitle}
          autoFocus
        />
        <TextInput
          label={t('pages.experiments.field_purpose')}
          hint={t('pages.experiments.field_purpose_hint')}
          value={purpose}
          onChange={setPurpose}
        />
        <div className={styles.dirRow}>
          <TextInput
            label={t('pages.experiments.field_dir')}
            hint={t('pages.experiments.field_dir_hint')}
            value={dirRef}
            onChange={setDirRef}
          />
          <Button variant="secondary" onClick={() => void chooseFolder()}>
            {t('pages.experiments.btn_choose_folder')}
          </Button>
        </div>
        <p className={styles.muted}>{t('pages.experiments.create_defaults')}</p>
        {error !== null && (
          <p className={styles.errorText} role="alert">
            {error}
          </p>
        )}
      </div>
    </Modal>
  )
}