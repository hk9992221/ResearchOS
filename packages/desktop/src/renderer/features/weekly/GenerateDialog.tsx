import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import { AppError, AppErrorCode, type Period } from '@researchos/shared'

import {
  Banner,
  Button,
  Modal,
  ProgressBar,
  SelectField,
  SwitchField,
  TextInput
} from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { SkillPicker } from './SkillPicker'
import { weeklyApi, weeklyKeys } from './weekly-api'
import { weeklyErrorView } from './weekly-errors'
import {
  isCalendarDate,
  periodAround,
  periodDays,
  periodLabel,
  previousPeriod,
  todayInZone
} from './weekly-format'
import styles from './weekly.module.css'

type Preset = 'current' | 'previous' | 'custom'

interface JobView {
  readonly jobId: string
  readonly percent: number
  readonly stage: string
  readonly failed: boolean
  readonly error?: string
}

export interface GenerateDialogProps {
  readonly open: boolean
  readonly projectId: string
  readonly onClose: () => void
  readonly onGenerated: (reportId: string) => void
}

/**
 * GenerateDialog creates one period's report: the week comes from the shared
 * preferences (week start + timezone), an existing report is reported before
 * anything is written, and the AI path runs as a job whose progress is shown
 * here while the shell's task strip shows the same job.
 */
export function GenerateDialog({
  open,
  projectId,
  onClose,
  onGenerated
}: GenerateDialogProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()

  const [preset, setPreset] = useState<Preset>('current')
  const [start, setStart] = useState('')
  const [end, setEnd] = useState('')
  const [includeAI, setIncludeAI] = useState(true)
  const [overwrite, setOverwrite] = useState(false)
  const [skillId, setSkillId] = useState('')
  const [busy, setBusy] = useState(false)
  const [job, setJob] = useState<JobView | null>(null)
  const [actionError, setActionError] = useState<unknown>(null)

  const seeded = useRef(false)
  const periodRef = useRef<Period>({ start: '', end: '' })
  const jobIdRef = useRef<string | null>(null)
  const settledRef = useRef(false)

  const preferencesQuery = useQuery({
    queryKey: weeklyKeys.preferences,
    queryFn: weeklyApi.preferences,
    enabled: open
  })
  const skillsQuery = useQuery({
    queryKey: weeklyKeys.skills,
    queryFn: weeklyApi.skills,
    enabled: open
  })
  const periodsQuery = useQuery({
    queryKey: weeklyKeys.periods(projectId),
    queryFn: () => weeklyApi.periods(projectId),
    enabled: open
  })

  const weekStartsOn = preferencesQuery.data?.weekStartsOn ?? 1
  const timeZone = preferencesQuery.data?.timezone ?? 'UTC'

  // A fresh dialog seeds its period from the configured week, never from a
  // hardcoded Monday.
  useEffect(() => {
    if (!open) {
      seeded.current = false
      jobIdRef.current = null
      settledRef.current = false
      setJob(null)
      setBusy(false)
      setActionError(null)
      setOverwrite(false)
      setSkillId('')
      return
    }
    if (seeded.current || preferencesQuery.data === undefined) return
    seeded.current = true
    const current = periodAround(todayInZone(timeZone), weekStartsOn)
    setPreset('current')
    setStart(current.start)
    setEnd(current.end)
  }, [open, preferencesQuery.data, timeZone, weekStartsOn])

  const period: Period = { start, end }
  const periodValid = isCalendarDate(start) && isCalendarDate(end) && end >= start

  useEffect(() => {
    periodRef.current = period
  }, [start, end])

  const occupied = periodsQuery.data?.find(
    (entry) => entry.periodStart === start && entry.periodEnd === end
  )
  const blockedByConfirmation = occupied?.status === 'confirmed'
  const needsOverwrite = occupied !== undefined && !blockedByConfirmation
  const canSubmit =
    periodValid && !busy && !blockedByConfirmation && (!needsOverwrite || overwrite)

  const choosePreset = (value: string): void => {
    const next: Preset = value === 'previous' || value === 'custom' ? value : 'current'
    setPreset(next)
    if (next === 'custom') return
    const current = periodAround(todayInZone(timeZone), weekStartsOn)
    const chosen = next === 'current' ? current : previousPeriod(current)
    setStart(chosen.start)
    setEnd(chosen.end)
  }

  const resolveGenerated = async (target: Period): Promise<void> => {
    try {
      const page = await weeklyApi.reports({
        projectId,
        from: target.start,
        to: target.end,
        pageSize: 50
      })
      const created = page.items.find(
        (item) =>
          item.periodStart === target.start && item.periodEnd === target.end && item.deletedAt === null
      )
      if (created === undefined) throw new AppError(AppErrorCode.NotFound, 'report missing')
      await queryClient.invalidateQueries({ queryKey: queryKeys.weekly.all(projectId) })
      setBusy(false)
      onGenerated(created.reportId)
    } catch (error) {
      setActionError(error)
      setBusy(false)
    }
  }

  // Job events are filtered by the id this dialog started; the shell's own
  // task strip receives the same events and paints them independently.
  useEffect(() => {
    if (!open) return
    const offProgress = ipc.on('job.progress', (event) => {
      if (jobIdRef.current !== event.jobId) return
      setJob((current) =>
        current === null ? current : { ...current, percent: event.percent, stage: event.stage }
      )
    })
    const offDone = ipc.on('job.done', (event) => {
      if (jobIdRef.current !== event.jobId || settledRef.current) return
      if (event.state === 'succeeded') {
        settledRef.current = true
        setJob((current) => (current === null ? current : { ...current, percent: 100 }))
        void resolveGenerated(periodRef.current)
        return
      }
      settledRef.current = true
      setJob((current) =>
        current === null
          ? current
          : {
              ...current,
              failed: true,
              error: event.error?.message ?? t('pages.weekly_review.gen_failed_title')
            }
      )
      setBusy(false)
    })
    return () => {
      offProgress()
      offDone()
    }
  }, [open, projectId, t, queryClient])

  const submit = async (): Promise<void> => {
    setActionError(null)
    if (!periodValid) {
      setActionError(
        new AppError(AppErrorCode.InvalidArgument, t('pages.weekly_review.toast_period_invalid'))
      )
      return
    }
    setBusy(true)
    settledRef.current = false
    try {
      if (occupied === undefined && !includeAI) {
        const view = await weeklyApi.create({
          projectId,
          period,
          skeleton: true
        })
        await queryClient.invalidateQueries({ queryKey: queryKeys.weekly.all(projectId) })
        setBusy(false)
        onGenerated(view.reportId)
        return
      }
      const ref = await weeklyApi.generate({
        projectId,
        period,
        includeAI,
        replace: overwrite,
        ...(skillId === '' ? {} : { skillId })
      })
      jobIdRef.current = ref.jobId
      setJob({
        jobId: ref.jobId,
        percent: 0,
        stage: t('pages.weekly_review.stage_collect'),
        failed: false
      })
      // The job starts in the main process before this reply arrives, so its
      // terminal event may already have been published. Read the snapshot once
      // to close that window; later progress/done events are listened for.
      const snapshot = await weeklyApi.job(ref.jobId).catch(() => null)
      if (snapshot !== null && !settledRef.current) {
        if (snapshot.state === 'succeeded') {
          settledRef.current = true
          setJob((current) => (current === null ? current : { ...current, percent: 100 }))
          await resolveGenerated(periodRef.current)
        } else if (snapshot.state === 'failed' || snapshot.state === 'cancelled') {
          settledRef.current = true
          setJob((current) =>
            current === null
              ? current
              : {
                  ...current,
                  failed: true,
                  error: snapshot.error?.message ?? t('pages.weekly_review.gen_failed_title')
                }
          )
          setBusy(false)
        } else {
          setJob((current) =>
            current === null
              ? current
              : { ...current, percent: snapshot.progress, stage: snapshot.stage }
          )
        }
      }
    } catch (error) {
      setActionError(error)
      setBusy(false)
    }
  }

  const errorView = actionError === null ? null : weeklyErrorView(actionError)
  const def = skillsQuery.data

  return (
    <Modal
      open={open}
      title={t('pages.weekly_review.gen_title')}
      onClose={onClose}
      width={620}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            {t('pages.weekly_review.gen_close')}
          </Button>
          <Button
            variant="primary"
            onClick={() => void submit()}
            disabled={!canSubmit}
            aria-busy={busy ? true : undefined}
          >
            {t('pages.weekly_review.btn_generate_run')}
          </Button>
        </>
      }
    >
      <div className={styles.dialogGrid}>
        <SelectField
          label={t('pages.weekly_review.field_period')}
          value={preset}
          onChange={choosePreset}
          options={[
            { value: 'current', label: t('pages.weekly_review.period_current') },
            { value: 'previous', label: t('pages.weekly_review.period_previous') },
            { value: 'custom', label: t('pages.weekly_review.period_custom') }
          ]}
        />
        <div className={styles.row}>
          <TextInput
            label={t('pages.weekly_review.field_start')}
            type="date"
            value={start}
            onChange={(value) => {
              setPreset('custom')
              setStart(value)
            }}
          />
          <TextInput
            label={t('pages.weekly_review.field_end')}
            type="date"
            value={end}
            onChange={(value) => {
              setPreset('custom')
              setEnd(value)
            }}
          />
        </div>
        <SwitchField
          label={t('pages.weekly_review.gen_mode_ai')}
          checked={includeAI}
          onChange={setIncludeAI}
          description={t('pages.weekly_review.gen_mode_skeleton')}
        />
        {includeAI && (
          <SkillPicker
            skills={def}
            value={skillId}
            onChange={setSkillId}
            disabled={busy}
            hint={t('pages.weekly_review.skill_card_hint')}
          />
        )}
        {needsOverwrite && (
          <SwitchField
            label={t('pages.weekly_review.preview_overwrite')}
            checked={overwrite}
            onChange={setOverwrite}
            description={t('pages.weekly_review.preview_overwrite_desc')}
          />
        )}
        <div className={styles.previewBox}>
          <span className={styles.mono}>
            {periodLabel(isCalendarDate(start) ? start : todayInZone(timeZone), weekStartsOn)}
          </span>
          {periodValid ? (
            <span>
              {t('pages.weekly_review.preview_period', {
                start,
                end,
                days: periodDays(period)
              })}
            </span>
          ) : (
            <span>{t('pages.weekly_review.toast_period_invalid')}</span>
          )}
          <span>
            {t('pages.weekly_review.period_settings', {
              day: t(
                weekStartsOn === 0
                  ? 'pages.weekly_review.week_start_sunday'
                  : 'pages.weekly_review.week_start_monday'
              ),
              timeZone
            })}
          </span>
          <span>
            {blockedByConfirmation
              ? t('pages.weekly_review.preview_confirmed')
              : needsOverwrite
                ? t('pages.weekly_review.preview_exists', {
                    title: periodLabel(start, weekStartsOn)
                  })
                : t('pages.weekly_review.preview_new')}
          </span>
          {!includeAI && <span>{t('pages.weekly_review.preview_no_ai')}</span>}
        </div>
        {job !== null && (
          <div className={styles.previewBox}>
            <ProgressBar value={job.percent} label={t('pages.weekly_review.gen_title')} />
            <span>{job.stage}</span>
            {job.failed && (
              <Banner tone="danger" title={t('pages.weekly_review.gen_failed_title')}>
                {job.error}
              </Banner>
            )}
          </div>
        )}
        {errorView !== null && (
          <Banner tone="danger" title={t(errorView.labelKey)}>
            <span className={styles.errorMessage}>{errorView.message}</span>
          </Banner>
        )}
      </div>
    </Modal>
  )
}
