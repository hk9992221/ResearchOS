import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'

import type { EvidenceDirection, EvidenceView, IdeaDetail } from '@researchos/shared'

import { Button, Modal, SelectField } from '../../design/components'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { ideasApi } from './api'
import { SourcePicker, type SourceRefValue } from './SourcePicker'
import styles from './ideas.module.css'

export interface EvidenceCreateDialogProps {
  readonly open: boolean
  readonly projectId: string
  readonly idea: IdeaDetail
  readonly onClose: () => void
  readonly onCreated: (evidence: EvidenceView) => void
}

/** New evidence: direction, Markdown content and the exact source row. */
export function EvidenceCreateDialog({
  open,
  projectId,
  idea,
  onClose,
  onCreated
}: EvidenceCreateDialogProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [direction, setDirection] = useState<EvidenceDirection>('support')
  const [content, setContent] = useState('')
  const [source, setSource] = useState<SourceRefValue | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const close = (): void => {
    if (busy) return
    setContent('')
    setSource(null)
    setError(null)
    onClose()
  }

  const submit = async (): Promise<void> => {
    if (content.trim() === '') {
      setError(t('pages.ideas.evidence_content_required'))
      return
    }
    if (source === null) {
      setError(t('pages.ideas.source_required'))
      return
    }
    setBusy(true)
    setError(null)
    try {
      const created = await ideasApi.evidence.create({
        projectId,
        ideaId: idea.id,
        direction,
        content,
        source: { type: source.type, id: source.id }
      })
      await queryClient.invalidateQueries({ queryKey: queryKeys.ideas.all(projectId) })
      setContent('')
      setSource(null)
      onCreated(created)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('pages.ideas.toast_evidence_fail'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      title={t('pages.ideas.add_title')}
      onClose={close}
      footer={
        <>
          <Button variant="secondary" onClick={close} disabled={busy}>
            {t('pages.ideas.ai_cancel')}
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy} aria-busy={busy}>
            {t('pages.ideas.add_confirm')}
          </Button>
        </>
      }
    >
      <SelectField
        label={t('pages.ideas.field_direction')}
        value={direction}
        options={[
          { value: 'support', label: t('pages.ideas.direction_support') },
          { value: 'oppose', label: t('pages.ideas.direction_oppose') }
        ]}
        onChange={(next) => setDirection(next as EvidenceDirection)}
      />
      <div className={styles.formBlock}>
        <label className={styles.fieldLabel} htmlFor="idea-evidence-content">
          {t('pages.ideas.field_evidence_content')}
          <span className={styles.muted}>{t('pages.ideas.field_evidence_hint')}</span>
        </label>
        <textarea
          id="idea-evidence-content"
          className={styles.textarea}
          rows={5}
          value={content}
          onChange={(event) => {
            setContent(event.target.value)
            setError(null)
          }}
        />
      </div>
      <SourcePicker projectId={projectId} value={source} onChange={setSource} />
      {error !== null && (
        <p className={styles.fieldError} role="alert">
          {error}
        </p>
      )}
    </Modal>
  )
}