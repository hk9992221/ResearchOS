import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import type {
  EvidenceExtractionSourceRef,
  EvidenceExtractionSourceType,
  ProjectResources,
  RunSummary
} from '@researchos/shared'

import { Banner, Button, Modal, SelectField } from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { ideasApi } from './api'
import { EXTRACT_INSTRUCTION_MAX } from './model'
import styles from './ideas.module.css'

const MAX_SOURCES = 8

export interface ExtractDialogProps {
  readonly open: boolean
  readonly projectId: string
  readonly ideaId: string
  readonly onClose: () => void
  readonly onStarted: (jobId: string) => void
}

interface SourceChoice {
  readonly type: EvidenceExtractionSourceType
  readonly id: string
  readonly title: string
}

const keyOf = (ref: { type: string; id: string }): string => `${ref.type}:${ref.id}`

/**
 * AI evidence extraction (Go ideas_ai.go): choose 1–8 experiment/run/note
 * sources, optionally focus the instruction, start a job. Nothing is
 * confirmed here; drafts land in the panel for review.
 */
export function ExtractDialog({
  open,
  projectId,
  ideaId,
  onClose,
  onStarted
}: ExtractDialogProps): JSX.Element {
  const t = useT()
  const [selected, setSelected] = useState<readonly SourceChoice[]>([])
  const [instruction, setInstruction] = useState('')
  const [runExperiment, setRunExperiment] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const resources = useQuery({
    queryKey: ['project', projectId, 'resources'],
    queryFn: () => ipc.invoke('project.resources', { id: projectId }),
    enabled: open
  })
  const availability = useQuery({
    queryKey: ['ai', 'availability'],
    queryFn: () => ipc.invoke('ai.availability', { refresh: false }),
    enabled: open
  })
  const runs = useQuery({
    queryKey: ['experiments', projectId, 'runs', runExperiment],
    queryFn: () => ideasApi.runs(projectId, runExperiment),
    enabled: open && runExperiment !== ''
  })

  const data: ProjectResources | undefined = resources.data
  const available = availability.data?.available === true

  const toggle = (choice: SourceChoice): void => {
    setError(null)
    setSelected((current) => {
      if (current.some((item) => keyOf(item) === keyOf(choice))) {
        return current.filter((item) => keyOf(item) !== keyOf(choice))
      }
      if (current.length >= MAX_SOURCES) {
        setError(t('pages.ideas.ai_max_sources', { max: MAX_SOURCES }))
        return current
      }
      return [...current, choice]
    })
  }

  const close = (): void => {
    if (busy) return
    setSelected([])
    setInstruction('')
    setRunExperiment('')
    setError(null)
    onClose()
  }

  const submit = async (): Promise<void> => {
    if (selected.length === 0) {
      setError(t('pages.ideas.ai_no_sources'))
      return
    }
    if (Array.from(instruction).length > EXTRACT_INSTRUCTION_MAX) {
      setError(t('pages.ideas.ai_instruction_long', { max: EXTRACT_INSTRUCTION_MAX }))
      return
    }
    setBusy(true)
    setError(null)
    const sourceRefs: EvidenceExtractionSourceRef[] = selected.map((item) => ({
      type: item.type,
      id: item.id
    }))
    try {
      const { jobId } = await ideasApi.evidence.extract({
        projectId,
        ideaId,
        sourceRefs,
        ...(instruction.trim() === '' ? {} : { instruction: instruction.trim() })
      })
      setBusy(false)
      setSelected([])
      setInstruction('')
      onStarted(jobId)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('pages.ideas.ai_failed'))
      setBusy(false)
    }
  }

  const group = (
    type: EvidenceExtractionSourceType,
    titleKey: string,
    items: readonly { id: string; title: string }[]
  ): JSX.Element => (
    <fieldset className={styles.sourceGroup}>
      <legend className={styles.groupTitle}>{t(titleKey)}</legend>
      {items.length === 0 ? (
        <p className={styles.muted}>{t('pages.ideas.ai_group_empty')}</p>
      ) : (
        <ul className={styles.sourceList}>
          {items.map((item) => (
            <li key={`${type}:${item.id}`}>
              <label className={styles.checkRow}>
                <input
                  type="checkbox"
                  checked={selected.some((choice) => keyOf(choice) === keyOf({ type, id: item.id }))}
                  onChange={() => toggle({ type, id: item.id, title: item.title })}
                />
                <span>{item.title}</span>
              </label>
            </li>
          ))}
        </ul>
      )}
    </fieldset>
  )

  return (
    <Modal
      open={open}
      title={t('pages.ideas.ai_title')}
      onClose={close}
      width={620}
      footer={
        <>
          <Button variant="secondary" onClick={close} disabled={busy}>
            {t('pages.ideas.ai_cancel')}
          </Button>
          <Button
            variant="primary"
            onClick={() => void submit()}
            disabled={busy || !available || selected.length === 0}
            aria-busy={busy}
          >
            {t('pages.ideas.ai_generate')}
          </Button>
        </>
      }
    >
      <p className={styles.dialogText}>{t('pages.ideas.ai_desc')}</p>
      {!available && (
        <Banner tone="warning" title={t('pages.ideas.error.ai_disabled')} />
      )}
      {group(
        'experiment',
        'pages.ideas.ai_group_experiments',
        (data?.experiments ?? []).map((experiment) => ({
          id: experiment.id,
          title: experiment.title
        }))
      )}
      {group(
        'note',
        'pages.ideas.ai_group_notes',
        (data?.notes ?? []).map((note) => ({ id: note.id, title: note.title }))
      )}
      <fieldset className={styles.sourceGroup}>
        <legend className={styles.groupTitle}>{t('pages.ideas.ai_group_runs')}</legend>
        <SelectField
          label={t('pages.ideas.source_experiment')}
          value={runExperiment}
          options={[
            { value: '', label: t('pages.ideas.link_empty') },
            ...(data?.experiments ?? []).map((experiment) => ({
              value: experiment.id,
              label: experiment.title
            }))
          ]}
          onChange={(next) => {
            setRunExperiment(next)
            setSelected((current) => current.filter((item) => item.type !== 'run'))
          }}
        />
        {runExperiment === '' ? (
          <p className={styles.muted}>{t('pages.ideas.ai_group_empty')}</p>
        ) : runs.isPending ? (
          <p className={styles.muted}>{t('pages.ideas.task_candidates')}</p>
        ) : (
          <ul className={styles.sourceList}>
            {(runs.data ?? []).map((run: RunSummary) => (
              <li key={run.id}>
                <label className={styles.checkRow}>
                  <input
                    type="checkbox"
                    checked={selected.some((choice) => keyOf(choice) === keyOf({ type: 'run', id: run.id }))}
                    onChange={() =>
                      toggle({
                        type: 'run',
                        id: run.id,
                        title: run.displayName.trim() === '' ? run.title : run.displayName
                      })
                    }
                  />
                  <span>{run.displayName.trim() === '' ? run.title : run.displayName}</span>
                </label>
              </li>
            ))}
          </ul>
        )}
      </fieldset>
      <div className={styles.formBlock}>
        <label className={styles.fieldLabel} htmlFor="idea-extract-instruction">
          {t('pages.ideas.ai_instruction')}
          <span className={styles.muted}>{t('pages.ideas.ai_instruction_hint')}</span>
        </label>
        <textarea
          id="idea-extract-instruction"
          className={styles.textarea}
          rows={3}
          value={instruction}
          onChange={(event) => {
            setInstruction(event.target.value)
            setError(null)
          }}
        />
        <span className={styles.muted}>
          {Array.from(instruction).length} / {EXTRACT_INSTRUCTION_MAX}
        </span>
      </div>
      {selected.length > 0 && (
        <p className={styles.muted}>{t('pages.ideas.ai_selected_n', { n: selected.length })}</p>
      )}
      {error !== null && (
        <p className={styles.fieldError} role="alert">
          {error}
        </p>
      )}
    </Modal>
  )
}