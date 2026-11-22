/**
 * Global toast queue.
 *
 * One host (`<Toasts>` in AppShell) renders this store. Feature modules never
 * render their own toast stack: `pushToast` is the single entry point, and
 * `pushUndoToast` is the common reversible-action shape (restore, undo a
 * delete). The store keeps no timers — the host dismisses each toast after its
 * lifetime, so a queued toast survives a page unmount.
 */

import { create } from 'zustand'

export type ToastTone = 'success' | 'error' | 'warning' | 'info'

export interface ToastAction {
  readonly label: string
  readonly onClick: () => void
}

export interface ToastInput {
  readonly tone?: ToastTone
  readonly title: string
  /** A detail from the command; only when it adds a fact. */
  readonly message?: string
  /** Milliseconds before auto-dismiss; 0 keeps it until closed. */
  readonly durationMs?: number
  readonly action?: ToastAction
}

export interface GlobalToast {
  readonly id: string
  readonly tone: ToastTone
  readonly title: string
  readonly message?: string
  readonly durationMs?: number
  readonly action?: ToastAction
}

export interface UndoToastInput {
  readonly tone?: ToastTone
  readonly title: string
  readonly message?: string
  readonly undoLabel: string
  readonly onUndo: () => void
}

export interface ToastStore {
  readonly toasts: readonly GlobalToast[]
  readonly push: (input: ToastInput) => string
  readonly dismiss: (id: string) => void
  readonly clear: () => void
}

let sequence = 0

function nextId(): string {
  sequence += 1
  return `toast-${sequence}`
}

export const useToastStore = create<ToastStore>((set) => ({
  toasts: [],
  push: (input) => {
    const id = nextId()
    const toast: GlobalToast = {
      id,
      tone: input.tone ?? 'info',
      title: input.title,
      ...(input.message === undefined ? {} : { message: input.message }),
      ...(input.durationMs === undefined ? {} : { durationMs: input.durationMs }),
      ...(input.action === undefined ? {} : { action: input.action })
    }
    set((state) => ({ toasts: [...state.toasts, toast] }))
    return id
  },
  dismiss: (id) => set((state) => ({ toasts: state.toasts.filter((toast) => toast.id !== id) })),
  clear: () => set({ toasts: [] })
}))

/** pushToast queues one toast and returns its id (features call this). */
export function pushToast(input: ToastInput): string {
  return useToastStore.getState().push(input)
}

export function dismissToast(id: string): void {
  useToastStore.getState().dismiss(id)
}

export function clearToasts(): void {
  useToastStore.getState().clear()
}

/**
 * pushUndoToast queues a reversible action. Undo runs the callback and closes
 * the toast; dismissing without undo keeps the change.
 */
export function pushUndoToast(input: UndoToastInput): string {
  const id = useToastStore.getState().push({
    tone: input.tone ?? 'success',
    title: input.title,
    ...(input.message === undefined ? {} : { message: input.message }),
    action: {
      label: input.undoLabel,
      onClick: () => {
        input.onUndo()
        dismissToast(id)
      }
    }
  })
  return id
}