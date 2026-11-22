import { useEffect, useState } from 'react'

import type { MigratePlan, MigrateProgress } from '@researchos/shared'

import { Banner, Button, Card, Page, PageHeader, Skeleton, TextInput } from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'

function bytes(value: number): string {
  if (value < 1024) return `${value} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let size = value / 1024
  let index = 0
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024
    index += 1
  }
  return `${size.toFixed(1)} ${units[index]}`
}

/** Workspace copy-migration: plan → copy/verify/commit progress → cancel. */
export default function MigrationPage(): JSX.Element {
  const t = useT()
  const [target, setTarget] = useState('')
  const [plan, setPlan] = useState<MigratePlan | null>(null)
  const [jobId, setJobId] = useState<string | null>(null)
  const [progress, setProgress] = useState<MigrateProgress | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const choose = async (): Promise<void> => {
    setError(null)
    const picked = await ipc.invoke('app.pickFolder', {
      title: t('pages.migration.field_target_label')
    })
    if (!picked.canceled && picked.paths[0] !== undefined) {
      setTarget(picked.paths[0])
      setPlan(null)
    }
  }

  const makePlan = async (): Promise<void> => {
    setError(null)
    setBusy(true)
    try {
      setPlan(await ipc.invoke('workspace.migrate.plan', { targetRoot: target }))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy(false)
    }
  }

  const start = async (): Promise<void> => {
    setError(null)
    setBusy(true)
    try {
      const job = await ipc.invoke('workspace.migrate.start', { targetRoot: target })
      setJobId(job.jobId)
      setProgress(null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy(false)
    }
  }

  const cancel = async (): Promise<void> => {
    if (jobId === null) return
    try {
      await ipc.invoke('workspace.migrate.cancel', { jobId })
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    }
  }

  useEffect(() => {
    if (jobId === null) return
    let cancelled = false
    const tick = async (): Promise<void> => {
      try {
        const next = await ipc.invoke('workspace.migrate.progress', { jobId })
        if (cancelled) return
        setProgress(next)
        if (next.state === 'succeeded' || next.state === 'failed' || next.state === 'cancelled') {
          setJobId(null)
        }
      } catch (cause) {
        if (!cancelled) setError(cause instanceof Error ? cause.message : String(cause))
        setJobId(null)
      }
    }
    void tick()
    const timer = setInterval(() => void tick(), 500)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [jobId])

  const running = jobId !== null

  return (
    <Page>
      <PageHeader title={t('pages.migration.title')} />
      {error !== null && <Banner tone="danger">{error}</Banner>}
      {progress?.state === 'succeeded' && (
        <Banner tone="success">
          {t('pages.migration.done_message', { path: target })}
        </Banner>
      )}
      <Card title={t('pages.migration.card_new_location')}>
        <div style={{ display: 'flex', gap: 12, alignItems: 'flex-end' }}>
          <TextInput
            label={t('pages.migration.field_target_label')}
            value={target}
            onChange={setTarget}
            hint={t('pages.migration.field_target_desc')}
          />
          <Button variant="secondary" onClick={() => void choose()} disabled={running}>
            {t('pages.migration.btn_browse')}
          </Button>
        </div>
      </Card>

      {running && (
        <Card title={t('pages.migration.card_migrating')}>
          {progress === null ? (
            <Skeleton />
          ) : (
            <div>
              <div>{progress.currentStep}</div>
              <div>
                {t('pages.migration.progress_bytes', {
                  done: bytes(progress.processedBytes),
                  total: bytes(progress.totalBytes)
                })}
              </div>
            </div>
          )}
          <Button variant="danger" onClick={() => void cancel()}>
            {t('pages.migration.btn_cancel')}
          </Button>
        </Card>
      )}

      {plan !== null && (
        <Card title={t('pages.migration.card_plan')}>
          <div>{t('pages.migration.plan_paths', { source: plan.sourceRoot, target: plan.targetRoot })}</div>
          <div>
            {t('pages.migration.row_required')}: {bytes(plan.requiredBytes)}
          </div>
          <div>
            {t('pages.migration.row_free')}: {bytes(plan.freeBytes)}
          </div>
          <div>{t('pages.migration.row_items', { count: plan.items.length })}</div>
          {plan.warnings.map((warning) => (
            <Banner key={warning} tone="warning">
              {warning}
            </Banner>
          ))}
          {!plan.canProceed && <Banner tone="danger">{t('pages.migration.banner_target_blocked')}</Banner>}
        </Card>
      )}

      <div style={{ display: 'flex', gap: 12 }}>
        <Button
          variant="secondary"
          onClick={() => void makePlan()}
          disabled={target === '' || busy || running}
        >
          {t('pages.migration.btn_plan')}
        </Button>
        <Button
          variant="primary"
          onClick={() => void start()}
          disabled={plan === null || !plan.canProceed || busy || running}
        >
          {t('pages.migration.btn_start')}
        </Button>
      </div>
    </Page>
  )
}
