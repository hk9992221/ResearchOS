import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'

import type { Idea } from '@researchos/shared'

import { Button, Modal, TextInput } from '../../design/components'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { ideasApi } from './api'
import { IDEA_TITLE_MAX } from './model'
import styles from './ideas.module.css'

export interface CreateIdeaDialogProps {
  readonly open: boolean
  readonly projectId: string
  readonly onClose: () => void
  readonly onCreated: (idea: Idea) => void
}

/** New idea (Go pages/ideas.go dialog): title required, description optional. */
export function CreateIdeaDialog({
  open,
  projectId,
  onClose,
  onCreated
}: CreateIdeaDialogProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const close = (): void => {
    if (busy) return
    setTitle('')
    setDescription('')
    setError(null)
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
      const idea = await ideasApi.create(
        projectId,
        trimmed,
        description.trim() === '' ? undefined : description
      )
      await queryClient.invalidateQueries({ queryKey: queryKeys.ideas.all(projectId) })
      setTitle('')
      setDescription('')
      onCreated(idea)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('pages.ideas.toast_create_fail'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      title={t('pages.ideas.dialog_title')}
      onClose={close}
      footer={
        <>
          <Button variant="secondary" onClick={close} disabled={busy}>
            {t('pages.ideas.ai_cancel')}
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy} aria-busy={busy}>
            {t('pages.ideas.dialog_create')}
          </Button>
        </>
      }
    >
      <p className={styles.dialogText}>{t('pages.ideas.dialog_desc')}</p>
      <TextInput
        label={t('pages.ideas.field_idea_title')}
        hint={t('pages.ideas.field_title_hint')}
        value={title}
        error={error ?? undefined}
        autoFocus
        onChange={(value) => {
          setTitle(value)
          setError(null)
        }}
        onKeyDown={(event) => {
          if (event.key === 'Enter') {
            event.preventDefault()
            void submit()
          }
        }}
      />
      <label className={styles.fieldLabel} htmlFor="idea-create-description">
        {t('pages.ideas.field_description')}
        <span className={styles.muted}>{t('pages.ideas.field_description_hint')}</span>
      </label>
      <textarea
        id="idea-create-description"
        className={styles.textarea}
        rows={4}
        value={description}
        onChange={(event) => setDescription(event.target.value)}
      />
    </Modal>
  )
}