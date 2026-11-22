import { useCallback, useRef, useState } from 'react'

import type { ToastMessage } from '../../app/Toasts'
import { useT } from '../../i18n'

export type ToastInput = Omit<ToastMessage, 'id'>

export interface FeatureToasts {
  readonly toasts: readonly ToastMessage[]
  readonly notify: (toast: ToastInput) => void
  readonly dismiss: (id: string) => void
  readonly undoable: (title: string, onUndo: () => Promise<void>) => void
}

/**
 * A small queue for module-level feedback (save failures, archive undo). The
 * shell's Toasts component renders it; the shell only feeds its own queue from
 * host props, so the module keeps its instance instead of reaching into app
 * state it does not own.
 */
export function useFeatureToasts(): FeatureToasts {
  const t = useT()
  const [toasts, setToasts] = useState<readonly ToastMessage[]>([])
  const nextId = useRef(0)

  const notify = useCallback((toast: ToastInput) => {
    nextId.current += 1
    const id = `kanban-${nextId.current}`
    setToasts((list) => [...list, { id, ...toast }])
  }, [])

  const dismiss = useCallback((id: string) => {
    setToasts((list) => list.filter((toast) => toast.id !== id))
  }, [])

  const undoable = useCallback(
    (title: string, onUndo: () => Promise<void>) => {
      notify({
        tone: 'success',
        title,
        durationMs: 8000,
        action: {
          label: t('common.undo'),
          onClick: () => {
            void onUndo()
          }
        }
      })
    },
    [notify, t]
  )

  return { toasts, notify, dismiss, undoable }
}