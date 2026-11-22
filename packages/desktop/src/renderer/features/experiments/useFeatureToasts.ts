import { useCallback, useRef, useState } from 'react'

import type { ToastMessage } from '../../app/Toasts'

export type ToastInput = Omit<ToastMessage, 'id'>

export interface FeatureToasts {
  readonly toasts: readonly ToastMessage[]
  readonly notify: (toast: ToastInput) => void
  readonly dismiss: (id: string) => void
}

/**
 * Module-level feedback (scan finished, save failed). The shell's Toasts
 * component renders the queue the page passes it; the feature owns this
 * instance instead of reaching into shell state it does not own.
 */
export function useFeatureToasts(): FeatureToasts {
  const [toasts, setToasts] = useState<readonly ToastMessage[]>([])
  const nextId = useRef(0)

  const notify = useCallback((toast: ToastInput) => {
    nextId.current += 1
    const id = `experiments-${nextId.current}`
    setToasts((list) => [...list, { id, ...toast }])
  }, [])

  const dismiss = useCallback((id: string) => {
    setToasts((list) => list.filter((toast) => toast.id !== id))
  }, [])

  return { toasts, notify, dismiss }
}