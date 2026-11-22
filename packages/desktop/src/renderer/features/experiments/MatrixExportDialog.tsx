import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import type { MetricDirection, TableFormat } from '@researchos/shared'

import {
  Button,
  Modal,
  SelectField,
  Skeleton,
  SwitchField,
  TextInput
} from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { experimentsApi } from './api'
import styles from './experiments.module.css'

export interface MatrixExportDialogProps {
  readonly open: boolean
  readonly projectId: string
  readonly experimentId: string
  readonly baselineId: string | null
  readonly directions: Record<string, MetricDirection>
  readonly runIds: readonly string[]
  readonly diffParams: boolean
  readonly precision: number
  readonly onClose: () => void
  readonly notify: (input: {
    tone: 'success' | 'error' | 'warning' | 'info'
    title: string
    message?: string
  }) => void
}

const FORMATS: readonly TableFormat[] = ['latex_booktabs', 'markdown', 'csv']

/**
 * Paper-ready table export. The service renders the text; this
 * dialog only chooses the format/options and shows the exact bytes that go to
 * the clipboard or into a Research Note.
 *
 * Destinations: clipboard, a Research Note, or a user-chosen file written
 * through `app.saveTextFile`.
 */
export function MatrixExportDialog({
  open,
  projectId,
  experimentId,
  baselineId,
  directions,
  runIds,
  diffParams,
  precision,
  onClose,
  notify
}: MatrixExportDialogProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [format, setFormat] = useState<TableFormat>('latex_booktabs')
  const [caption, setCaption] = useState('')
  const [label, setLabel] = useState('')
  const [showDelta, setShowDelta] = useState(true)
  const [highlightBest, setHighlightBest] = useState(true)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (open) return
    setCaption('')
    setLabel('')
  }, [open])

  const previewQuery = useQuery({
    queryKey: [
      'experiments',
      projectId,
      'export-table',
      experimentId,
      format,
      caption,
      label,
      showDelta,
      highlightBest,
      precision,
      baselineId,
      runIds
    ],
    queryFn: () =>
      experimentsApi.compare({
        projectId,
        experimentId,
        ...(baselineId === null ? {} : { baselineId }),
        ...(Object.keys(directions).length === 0 ? {} : { directions }),
        ...(runIds.length === 0 ? {} : { runIds: [...runIds] }),
        diffParams,
        table: {
          format,
          precision,
          showDelta,
          highlightBest,
          ...(caption.trim() === '' ? {} : { caption: caption.trim() }),
          ...(label.trim() === '' ? {} : { label: label.trim() })
        }
      }),
    enabled: open
  })

  const table = previewQuery.data

  const copy = async (): Promise<void> => {
    if (table === undefined) return
    try {
      await navigator.clipboard.writeText(table.table)
      notify({ tone: 'success', title: t('pages.experiments.matrix_copied') })
    } catch (cause) {
      notify({
        tone: 'error',
        title: t('pages.experiments.matrix_export_failed'),
        message: cause instanceof Error ? cause.message : undefined
      })
    }
  }

  const saveFile = async (): Promise<void> => {
    if (table === undefined) return
    const extension = format === 'latex_booktabs' ? 'tex' : format === 'markdown' ? 'md' : 'csv'
    setBusy(true)
    try {
      const result = await ipc.invoke('app.saveTextFile', {
        defaultPath: `matrix.${extension}`,
        filters: [{ name: format, extensions: [extension] }],
        content: table.table
      })
      if (!result.canceled) notify({ tone: 'success', title: t('pages.experiments.matrix_saved') })
    } catch (cause) {
      notify({
        tone: 'error',
        title: t('pages.experiments.matrix_save_failed'),
        message: cause instanceof Error ? cause.message : undefined
      })
    } finally {
      setBusy(false)
    }
  }

  const insertNote = async (): Promise<void> => {
    if (table === undefined) return
    setBusy(true)
    try {
      await ipc.invoke('notes.create', {
        projectId,
        title: caption.trim() === '' ? t('pages.experiments.matrix_export_title') : caption.trim(),
        kind: 'scratchpad',
        bodyMd: table.table
      })
      await queryClient.invalidateQueries({ queryKey: queryKeys.notes.all(projectId) })
      notify({ tone: 'success', title: t('pages.experiments.matrix_insert_done') })
      onClose()
    } catch (cause) {
      notify({
        tone: 'error',
        title: t('pages.experiments.matrix_insert_failed'),
        message: cause instanceof Error ? cause.message : undefined
      })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      title={t('pages.experiments.matrix_export_title')}
      onClose={onClose}
      width={860}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            {t('pages.experiments.btn_back')}
          </Button>
          <Button
            variant="secondary"
            onClick={() => void copy()}
            disabled={table === undefined || busy}
          >
            {t('pages.experiments.matrix_copy')}
          </Button>
          <Button
            variant="secondary"
            onClick={() => void saveFile()}
            disabled={table === undefined || busy}
          >
            {t('pages.experiments.matrix_save_file')}
          </Button>
          <Button
            variant="primary"
            onClick={() => void insertNote()}
            disabled={table === undefined || busy}
            aria-busy={busy}
          >
            {t('pages.experiments.matrix_insert_note')}
          </Button>
        </>
      }
    >
      <div className={styles.stack}>
        <div className={styles.formGrid}>
          <SelectField
            label={t('pages.experiments.matrix_format')}
            value={format}
            onChange={(value) => setFormat(value as TableFormat)}
            options={FORMATS.map((value) => ({
              value,
              label:
                value === 'latex_booktabs'
                  ? 'LaTeX booktabs'
                  : value === 'markdown'
                    ? 'Markdown'
                    : 'CSV'
            }))}
          />
          <TextInput
            label={t('pages.experiments.matrix_caption')}
            value={caption}
            onChange={setCaption}
          />
          <TextInput
            label={t('pages.experiments.matrix_label')}
            value={label}
            onChange={setLabel}
          />
        </div>
        <div className={styles.filters}>
          <SwitchField
            label={t('pages.experiments.matrix_show_delta')}
            checked={showDelta}
            onChange={setShowDelta}
          />
          <SwitchField
            label={t('pages.experiments.matrix_highlight')}
            checked={highlightBest}
            onChange={setHighlightBest}
          />
        </div>

        {previewQuery.isPending ? (
          <Skeleton rows={6} />
        ) : previewQuery.isError ? (
          <p className={styles.errorText} role="alert">
            {previewQuery.error instanceof Error
              ? previewQuery.error.message
              : t('pages.experiments.matrix_failed')}
          </p>
        ) : (
          <pre className={styles.previewBox} aria-label={t('pages.experiments.matrix_preview')}>
            {table?.table ?? ''}
          </pre>
        )}
      </div>
    </Modal>
  )
}