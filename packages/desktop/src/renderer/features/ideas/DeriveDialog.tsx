import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'

import type { Idea, IdeaDetail } from '@researchos/shared'

import { Button, Modal, TextInput } from '../../design/components'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { ideasApi } from './api'
import { IDEA_TITLE_MAX } from './model'
import styles from './ideas.module.css'

export interface DeriveDialogProps {
  readonly open: boolean
  readonly projectId: string
  readonly parent: IdeaDetail
  readonly onClose: () => void
  readonly onDerived: (idea: Idea) => void
}

/** Derive a child: creates an idea plus the bidirectional derived_from link. */
export function DeriveDialog({
  open,
  projectId,
  parent,
  onClose,
  onDerived
}: DeriveDialogProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [title, setTitle] = useState(t('pages.ideas.derive_prefill', { title: parent.title }))
  const [description, setDescription] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const close = (): void => {
    if (busy) return
    onClose()
  }

  const submit = async (): Promise<void> => {
    const trimmed = title.trim()
    if (trimmed === '') {
      setError(t('pages.ideas.title_required'))
      return
    }
    if (Array.from(trimmed).length > IDEA_TITLE_MAX) {
      setError(t('pages.ideas.title_too_long'))
      return
    }
    setBusy(true)
    setError(null)
    try {
      const child = await ideasApi.derive(
        projectId,
        parent.id,
        trimmed,
        description.trim() === '' ? undefined : description
      )
      await queryClient.invalidateQueries({ queryKey: queryKeys.ideas.all(projectId) })
      onDerived(child)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('pages.ideas.task_derive'))
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      title={t('pages.ideas.derive_title')}
      onClose={close}
      footer={
        <>
          <Button variant="secondary" onClick={close} disabled={busy}>
            {t('pages.ideas.ai_cancel')}
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy} aria-busy={busy}>
            {t('pages.ideas.derive_confirm')}
          </Button>
        </>
      }
    >
      <p className={styles.dialogText}>{t('pages.ideas.derive_desc')}</p>
      <p className={styles.dialogText}>{t('pages.ideas.derive_no_evidence')}</p>
      <TextInput
        label={t('pages.ideas.field_idea_title')}
        value={title}
        error={error ?? undefined}
        autoFocus
        onChange={(value) => {
          setTitle(value)
          setError(null)
        }}
      />
      <label className={styles.fieldLabel} htmlFor="idea-derive-description">
        {t('pages.ideas.field_description')}
        <span className={styles.muted}>{t('pages.ideas.field_description_hint')}</span>
      </label>
      <textarea
        id="idea-derive-description"
        className={styles.textarea}
        rows={4}
        value={description}
        onChange={(event) => setDescription(event.target.value)}
      />
    </Modal>
  )
}