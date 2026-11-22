import { useState } from 'react'

import type { WeeklyExportResult, WeeklyReportStatus } from '@researchos/shared'

import {
  Banner,
  Button,
  Modal,
  Mono,
  SelectField,
  SwitchField,
  TextInput
} from '../../design/components'
import { useT } from '../../i18n'
import { weeklyApi } from './weekly-api'
import { weeklyErrorView } from './weekly-errors'
import styles from './weekly.module.css'

export interface ExportDialogProps {
  readonly open: boolean
  readonly projectId: string
  readonly reportId: string
  readonly status: WeeklyReportStatus
  readonly onClose: () => void
}

function baseName(path: string): string {
  const parts = path.split(/[\\/]/)
  return parts[parts.length - 1] ?? path
}

/**
 * ExportDialog writes the report to an absolute folder the user picks (outside
 * the workspace is allowed). The service refuses a relative path, a missing
 * parent, the project directory itself, an existing file without overwrite and
 * an unwritable folder; each refusal keeps its code and reason, so the dialog
 * shows the mapped sentence plus the server message instead of a generic
 * failure.
 */
export function ExportDialog({
  open,
  projectId,
  reportId,
  status,
  onClose
}: ExportDialogProps): JSX.Element {
  const t = useT()
  const [format, setFormat] = useState<'md' | 'html'>('md')
  const [destDir, setDestDir] = useState('')
  const [overwrite, setOverwrite] = useState(false)
  const [frontMatter, setFrontMatter] = useState(true)
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<WeeklyExportResult | null>(null)
  const [error, setError] = useState<unknown>(null)

  const close = (): void => {
    setBusy(false)
    setError(null)
    setResult(null)
    onClose()
  }

  const chooseFolder = async (): Promise<void> => {
    try {
      const picked = await weeklyApi.pickFolder(
        t('pages.weekly_review.field_dir'),
        destDir.trim() === '' ? undefined : destDir.trim()
      )
      const first = picked.paths[0]
      if (!picked.canceled && first !== undefined) setDestDir(first)
    } catch (cause) {
      setError(cause)
    }
  }

  const submit = async (): Promise<void> => {
    setBusy(true)
    setError(null)
    setResult(null)
    try {
      const written = await weeklyApi.export({
        projectId,
        reportId,
        format,
        overwrite,
        frontMatter,
        ...(destDir.trim() === '' ? {} : { destDir: destDir.trim() })
      })
      setResult(written)
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const errorView = error === null ? null : weeklyErrorView(error)

  return (
    <Modal
      open={open}
      title={t('pages.weekly_review.export_title')}
      onClose={close}
      width={560}
      footer={
        <>
          <Button variant="secondary" onClick={close} disabled={busy}>
            {t('pages.weekly_review.gen_close')}
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy}>
            {t('pages.weekly_review.btn_export_run')}
          </Button>
        </>
      }
    >
      <div className={styles.dialogGrid}>
        <p className={styles.emptyLine}>{t('pages.weekly_review.export_desc')}</p>
        <SelectField
          label={t('pages.weekly_review.field_format')}
          value={format}
          onChange={(value) => setFormat(value === 'html' ? 'html' : 'md')}
          options={[
            { value: 'md', label: t('pages.weekly_review.export_format_md') },
            { value: 'html', label: t('pages.weekly_review.export_format_html') }
          ]}
        />
        <div className={styles.row}>
          <div className={styles.grow}>
            <TextInput
              label={t('pages.weekly_review.field_dir')}
              value={destDir}
              onChange={setDestDir}
              hint={t('pages.weekly_review.field_dir_hint')}
            />
          </div>
          <Button variant="secondary" onClick={() => void chooseFolder()}>
            {t('pages.weekly_review.btn_choose_dir')}
          </Button>
        </div>
        <SwitchField
          label={t('pages.weekly_review.field_overwrite')}
          checked={overwrite}
          onChange={setOverwrite}
          description={t('pages.weekly_review.err_export_exists')}
        />
        <SwitchField
          label={t('pages.weekly_review.field_front_matter')}
          checked={frontMatter}
          onChange={setFrontMatter}
        />
        {status === 'draft' && (
          <Banner tone="warning" title={t('pages.weekly_review.export_draft_title')}>
            {t('pages.weekly_review.export_draft_message')}
          </Banner>
        )}
        {errorView !== null && (
          <Banner tone="danger" title={t(errorView.labelKey)}>
            <div className={styles.errorBox}>
              <span className={styles.errorMessage}>{errorView.message}</span>
              <Mono>{errorView.code}</Mono>
            </div>
          </Banner>
        )}
        {result !== null && (
          <div className={styles.result}>
            <span>{t('pages.weekly_review.export_done', { path: result.path })}</span>
            <span className={styles.mono}>
              {t('pages.weekly_review.export_filename', { name: baseName(result.path) })}
            </span>
          </div>
        )}
      </div>
    </Modal>
  )
}
