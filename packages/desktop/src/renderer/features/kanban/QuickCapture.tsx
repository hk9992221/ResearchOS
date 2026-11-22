import { useState } from 'react'
import { Plus } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'

import { Button, TextInput } from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { kanbanKeys } from './keys'
import styles from './kanban.module.css'

export interface QuickCaptureProps {
  readonly projectId: string
  readonly disabled: boolean
}

/**
 * Quick capture: Enter creates a top-level task in the default (leftmost
 * non-terminal) column. The board's inbox column is that same projection.
 */
export function QuickCapture({ projectId, disabled }: QuickCaptureProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [title, setTitle] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async (): Promise<void> => {
    const value = title.trim()
    if (value === '') {
      setError(t('pages.kanban.title_required'))
      return
    }
    setBusy(true)
    setError(null)
    try {
      await ipc.invoke('kanban.tasks.create', { projectId, title: value })
      setTitle('')
      await queryClient.invalidateQueries({ queryKey: kanbanKeys.all(projectId) })
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form
      className={[styles.quickAdd, styles.quickCapture].join(' ')}
      onSubmit={(event) => {
        event.preventDefault()
        void submit()
      }}
    >
      <div className={styles.quickAddRow}>
        <div className={styles.filterField}>
          <TextInput
            label={t('pages.kanban.quick_label')}
            hideLabel
            placeholder={t('pages.kanban.quick_hint')}
            value={title}
            disabled={disabled || busy}
            onChange={(value) => {
              setTitle(value)
              if (error !== null) setError(null)
            }}
          />
        </div>
        <Button
          type="submit"
          variant="secondary"
          size="sm"
          icon={Plus}
          disabled={disabled || busy}
        >
          {t('pages.kanban.btn_add_card')}
        </Button>
      </div>
      {error !== null && <p className={styles.errorLine}>{error}</p>}
    </form>
  )
}