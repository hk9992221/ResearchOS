import { useEffect, useId, useState, type ReactNode } from 'react'

import type { DeleteProjectPlan, Project } from '@researchos/shared'

import { Banner, Button, Modal, Row, Rows, Skeleton } from '../../design/components'
import { useT } from '../../i18n'
import { formatBytes } from '../../lib/bytes'
import { projectsApi } from './api'
import {
  PROJECT_LIMITS,
  fieldError,
  hasErrors,
  runeCount,
  validateProjectForm,
  type ProjectFormValues
} from './limits'
import styles from './projects.module.css'

/* --------------------------------------------------------------- fields */

interface ProjectFieldProps {
  readonly label: string
  readonly value: string
  readonly onChange: (value: string) => void
  readonly max: number
  readonly required?: boolean
  readonly multiline?: boolean
  readonly rows?: number
  readonly autoFocus?: boolean
  readonly disabled?: boolean
  readonly hint?: string
}

/**
 * ProjectField is a labelled text control with a live rune counter. The
 * counter is always visible; the error replaces nothing, so the user sees
 * both how far over the bound they are and what the bound is.
 */
export function ProjectField({
  label,
  value,
  onChange,
  max,
  required = false,
  multiline = false,
  rows = 4,
  autoFocus = false,
  disabled = false,
  hint
}: ProjectFieldProps): JSX.Element {
  const t = useT()
  const id = useId()
  const error = fieldError(value, max, required)
  const count = runeCount(value)
  const errorText =
    error === 'required'
      ? t('shell.projects.err_required')
      : error === 'too_long'
        ? t('shell.projects.err_too_long', { max })
        : undefined
  return (
    <div className={styles.field}>
      <label className={styles.label} htmlFor={id}>
        {label}
      </label>
      {multiline ? (
        <textarea
          id={id}
          className={[styles.input, styles.textarea, error !== null ? styles.inputError : ''].join(
            ' '
          )}
          rows={rows}
          value={value}
          disabled={disabled}
          autoFocus={autoFocus}
          aria-invalid={error !== null}
          aria-describedby={`${id}-meta`}
          onChange={(event) => onChange(event.target.value)}
        />
      ) : (
        <input
          id={id}
          className={[styles.input, error !== null ? styles.inputError : ''].join(' ')}
          value={value}
          disabled={disabled}
          autoFocus={autoFocus}
          aria-invalid={error !== null}
          aria-describedby={`${id}-meta`}
          onChange={(event) => onChange(event.target.value)}
        />
      )}
      <p id={`${id}-meta`} className={styles.meta}>
        <span className={styles.counter}>
          {t('shell.projects.counter', { used: count, max })}
        </span>
        {hint !== undefined && <span>{hint}</span>}
      </p>
      {errorText !== undefined && (
        <p className={styles.error} role="alert">
          {errorText}
        </p>
      )}
    </div>
  )
}

interface FormFieldsProps {
  readonly values: ProjectFormValues
  readonly onChange: (patch: Partial<ProjectFormValues>) => void
  readonly withFocus: boolean
  readonly disabled: boolean
}

function FormFields({ values, onChange, withFocus, disabled }: FormFieldsProps): JSX.Element {
  const t = useT()
  return (
    <div className={styles.fields}>
      <ProjectField
        label={t('shell.projects.field_name')}
        value={values.name}
        onChange={(name) => onChange({ name })}
        max={PROJECT_LIMITS.name}
        required
        autoFocus
        disabled={disabled}
      />
      <ProjectField
        label={t('shell.projects.field_summary')}
        value={values.summary}
        onChange={(summary) => onChange({ summary })}
        max={PROJECT_LIMITS.summary}
        multiline
        rows={3}
        disabled={disabled}
      />
      <ProjectField
        label={t('shell.projects.field_goals')}
        value={values.goals}
        onChange={(goals) => onChange({ goals })}
        max={PROJECT_LIMITS.goals}
        multiline
        rows={5}
        disabled={disabled}
      />
      {withFocus && (
        <ProjectField
          label={t('shell.projects.field_focus')}
          value={values.focus}
          onChange={(focus) => onChange({ focus })}
          max={PROJECT_LIMITS.focus}
          multiline
          rows={5}
          disabled={disabled}
        />
      )}
    </div>
  )
}

/* --------------------------------------------------------------- create */

export interface CreateProjectDialogProps {
  readonly open: boolean
  readonly onClose: () => void
  readonly onCreated: (project: Project) => void
  readonly onError: (message: string) => void
}

export function CreateProjectDialog({
  open,
  onClose,
  onCreated,
  onError
}: CreateProjectDialogProps): JSX.Element {
  const t = useT()
  const [values, setValues] = useState<ProjectFormValues>({
    name: '',
    summary: '',
    goals: '',
    focus: ''
  })
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (open) {
      setValues({ name: '', summary: '', goals: '', focus: '' })
      setBusy(false)
    }
  }, [open])

  const errors = validateProjectForm(values)
  const invalid = hasErrors(errors)

  const submit = async (): Promise<void> => {
    if (busy || invalid) return
    setBusy(true)
    try {
      const project = await projectsApi.create({
        name: values.name.trim(),
        summary: values.summary,
        goals: values.goals
      })
      onCreated(project)
    } catch (error) {
      onError(error instanceof Error ? error.message : String(error))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      title={t('shell.projects.dialog_title')}
      onClose={onClose}
      width={620}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy || invalid}>
            {t('shell.projects.btn_confirm')}
          </Button>
        </>
      }
    >
      <FormFields
        values={values}
        onChange={(patch) => setValues((current) => ({ ...current, ...patch }))}
        withFocus={false}
        disabled={busy}
      />
    </Modal>
  )
}

/* ----------------------------------------------------------------- edit */

export interface EditProjectDialogProps {
  readonly project: Project | null
  readonly onClose: () => void
  readonly onSaved: (project: Project) => void
  readonly onError: (message: string) => void
}

export function EditProjectDialog({
  project,
  onClose,
  onSaved,
  onError
}: EditProjectDialogProps): JSX.Element {
  const t = useT()
  const [values, setValues] = useState<ProjectFormValues>({
    name: '',
    summary: '',
    goals: '',
    focus: ''
  })
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (project === null) return
    setValues({
      name: project.name,
      summary: project.summary,
      goals: project.goals,
      focus: project.focusMd
    })
    setBusy(false)
  }, [project])

  const errors = validateProjectForm(values)
  const invalid = hasErrors(errors)

  const save = async (): Promise<void> => {
    if (project === null || busy || invalid) return
    setBusy(true)
    try {
      const updated = await projectsApi.update(
        project.id,
        {
          name: values.name.trim(),
          summary: values.summary,
          goals: values.goals,
          focusMd: values.focus
        },
        project.updatedAt
      )
      onSaved(updated)
    } catch (error) {
      onError(error instanceof Error ? error.message : String(error))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={project !== null}
      title={t('shell.projects.edit_dialog_title')}
      onClose={onClose}
      width={620}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button variant="primary" onClick={() => void save()} disabled={busy || invalid}>
            {t('shell.projects.btn_save')}
          </Button>
        </>
      }
    >
      <FormFields
        values={values}
        onChange={(patch) => setValues((current) => ({ ...current, ...patch }))}
        withFocus
        disabled={busy}
      />
    </Modal>
  )
}

/* --------------------------------------------------------------- delete */

export interface DeleteProjectDialogProps {
  readonly project: Project | null
  readonly onClose: () => void
  readonly onDeleted: (projectId: string) => void
}

const COUNT_ORDER = ['note', 'idea', 'experiment', 'run', 'task', 'evidence', 'weekly_report'] as const

/**
 * DeleteProjectDialog is the impact-plan wizard: the plan is read first, its
 * counts and blockers are shown, and only the exact project name plus the
 * one-shot token the plan issued can run the permanent delete.
 */
export function DeleteProjectDialog({
  project,
  onClose,
  onDeleted
}: DeleteProjectDialogProps): JSX.Element {
  const t = useT()
  const [plan, setPlan] = useState<DeleteProjectPlan | null>(null)
  const [planError, setPlanError] = useState<string | null>(null)
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const [confirmError, setConfirmError] = useState<string | null>(null)

  useEffect(() => {
    if (project === null) {
      setPlan(null)
      setPlanError(null)
      setTyped('')
      setBusy(false)
      setConfirmError(null)
      return
    }
    let live = true
    setPlan(null)
    setPlanError(null)
    setTyped('')
    setConfirmError(null)
    setBusy(false)
    projectsApi
      .planDelete(project.id)
      .then((result) => {
        if (live) setPlan(result)
      })
      .catch((error: unknown) => {
        if (live) setPlanError(error instanceof Error ? error.message : String(error))
      })
    return () => {
      live = false
    }
  }, [project])

  const blockers = plan?.blockers ?? []
  const token = plan?.confirmationToken ?? null
  const canConfirm =
    plan !== null &&
    token !== null &&
    blockers.length === 0 &&
    !busy &&
    typed === plan.projectName

  const confirm = async (): Promise<void> => {
    if (project === null || plan === null || token === null || !canConfirm) return
    setBusy(true)
    setConfirmError(null)
    try {
      await projectsApi.confirmDelete(project.id, typed, token)
      onDeleted(project.id)
    } catch (error) {
      setConfirmError(error instanceof Error ? error.message : String(error))
      setBusy(false)
    }
  }

  const counts = plan === null ? [] : COUNT_ORDER.filter((key) => (plan.entityCounts[key] ?? 0) > 0)

  return (
    <Modal
      open={project !== null}
      title={t('shell.projects.delete_dialog_title', { name: project?.name ?? '' })}
      onClose={onClose}
      escapeCloses={false}
      width={620}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button variant="danger" onClick={() => void confirm()} disabled={!canConfirm}>
            {t('shell.projects.btn_delete_confirm')}
          </Button>
        </>
      }
    >
      <p className={styles.muted}>{t('shell.projects.delete_dialog_desc')}</p>
      {planError !== null && (
        <Banner tone="danger" title={t('shell.projects.banner_plan_fail')}>
          {planError}
        </Banner>
      )}
      {plan === null && planError === null && <Skeleton rows={4} />}
      {plan !== null && (
        <>
          <Rows>
            {counts.map((key) => (
              <Row
                key={key}
                label={countLabel(t, key)}
                value={String(plan.entityCounts[key] ?? 0)}
              />
            ))}
            {counts.length === 0 && <Row label={t('shell.projects.plan_empty')} value="—" />}
            <Row label={t('shell.projects.plan_trashed')} value={String(plan.trashedCount)} />
            <Row label={t('shell.projects.plan_size')} value={formatBytes(plan.dirBytes, t)} />
            <Row
              label={t('shell.projects.plan_papers')}
              value={
                plan.paperImpactUnknown
                  ? t('shell.projects.plan_papers_unknown')
                  : t('shell.projects.plan_papers_value', {
                      removed: plan.exclusivePaperIds.length,
                      kept: plan.sharedPaperIds.length
                    })
              }
            />
          </Rows>
          {plan.exportRecommended && (
            <Banner tone="warning" title={t('shell.projects.banner_no_export')}>
              {t('shell.projects.banner_no_export_desc')}
            </Banner>
          )}
          {blockers.map((blocker) => (
            <Banner
              key={blocker.code}
              tone="danger"
              title={t('shell.projects.banner_blocked')}
            >
              {blockerText(t, blocker.code, blocker.message)}
            </Banner>
          ))}
          {token === null && (
            <Banner tone="warning">{t('shell.projects.delete_token_missing')}</Banner>
          )}
          {confirmError !== null && (
            <Banner tone="danger" title={t('shell.projects.banner_confirm_fail')}>
              {confirmError}
            </Banner>
          )}
          <ProjectField
            label={t('shell.projects.field_confirm_name')}
            value={typed}
            onChange={setTyped}
            max={PROJECT_LIMITS.name}
            hint={t('shell.projects.field_confirm_name_desc', { name: plan.projectName })}
            disabled={busy || token === null}
          />
        </>
      )}
    </Modal>
  )
}

function countLabel(t: (key: string) => string, key: string): string {
  const label = t(`shell.projects.entity_${key}`)
  return label === `shell.projects.entity_${key}` ? key : label
}

function blockerText(
  t: (key: string) => string,
  code: string,
  fallback: string
): ReactNode {
  const label = t(`shell.projects.blocker_${code}`)
  return label === `shell.projects.blocker_${code}` ? fallback : label
}