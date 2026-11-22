/**
 * Task store: the job strip of the shell (§17).
 *
 * `job.progress` opens a job, `job.done` closes it. A job that finishes inside
 * TASK_STRIP_DELAY_MS never appears — the strip is for work long enough to
 * need a presence, not for a flash on every click. Failures stay until the
 * user clears them, and a running job only offers Cancel when a producer
 * registered a canceller for it (the command map has no generic jobs.cancel:
 * each long operation owns its own cancel channel).
 */

import { create } from 'zustand'

export const TASK_STRIP_DELAY_MS = 400

export interface JobTask {
  readonly id: string
  /** The latest stage text from the main process; also the strip's label. */
  readonly name: string
  /** 0–100, as `job.progress` reports it; null before the first tick. */
  readonly percent: number | null
  readonly startedAt: number
  readonly visible: boolean
  readonly canceling: boolean
  readonly failed: boolean
  readonly error?: string
  readonly cancellable: boolean
}

export interface TasksStore {
  readonly tasks: readonly JobTask[]
  /** job.progress: create on first tick, then update stage and percent. */
  readonly progress: (jobId: string, percent: number, stage: string) => void
  /** job.done: remove a success, keep a failure until cleared. */
  readonly finish: (
    jobId: string,
    state: 'succeeded' | 'failed' | 'cancelled',
    error?: string
  ) => void
  /** Ask a job to stop; producers register how (see registerJobCanceller). */
  readonly cancel: (jobId: string) => void
  /** Remove the failures from the strip; running jobs stay. */
  readonly clearFailures: () => void
  readonly clear: () => void
}

const visibilityTimers = new Map<string, number>()
const cancellers = new Map<string, () => void>()

function disarm(jobId: string): void {
  const timer = visibilityTimers.get(jobId)
  if (timer !== undefined) {
    window.clearTimeout(timer)
    visibilityTimers.delete(jobId)
  }
}

/**
 * registerJobCanceller lets a producer say how its job stops. The strip shows
 * Cancel for that job only; unregistering hides it again.
 */
export function registerJobCanceller(jobId: string, cancel: () => void): () => void {
  cancellers.set(jobId, cancel)
  useTasksStore.setState((state) => ({
    tasks: state.tasks.map((task) => (task.id === jobId ? { ...task, cancellable: true } : task))
  }))
  return () => {
    cancellers.delete(jobId)
    useTasksStore.setState((state) => ({
      tasks: state.tasks.map((task) =>
        task.id === jobId ? { ...task, cancellable: false } : task
      )
    }))
  }
}

export const useTasksStore = create<TasksStore>((set, get) => ({
  tasks: [],

  progress: (jobId, percent, stage) => {
    const existing = get().tasks.find((task) => task.id === jobId)
    if (existing === undefined) {
      // The job becomes visible only after the grace period, so a fast
      // operation never flickers into the strip.
      visibilityTimers.set(
        jobId,
        window.setTimeout(() => {
          visibilityTimers.delete(jobId)
          useTasksStore.setState((state) => ({
            tasks: state.tasks.map((task) =>
              task.id === jobId ? { ...task, visible: true } : task
            )
          }))
        }, TASK_STRIP_DELAY_MS)
      )
      set((state) => ({
        tasks: [
          ...state.tasks,
          {
            id: jobId,
            name: stage,
            percent,
            startedAt: Date.now(),
            visible: false,
            canceling: false,
            failed: false,
            cancellable: cancellers.has(jobId)
          }
        ]
      }))
      return
    }
    set((state) => ({
      tasks: state.tasks.map((task) =>
        task.id === jobId ? { ...task, name: stage, percent } : task
      )
    }))
  },

  finish: (jobId, state, error) => {
    disarm(jobId)
    set((current) => {
      if (!current.tasks.some((task) => task.id === jobId)) return {}
      if (state === 'failed') {
        return {
          tasks: current.tasks.map((task) =>
            task.id === jobId
              ? { ...task, failed: true, visible: true, canceling: false, error }
              : task
          )
        }
      }
      return { tasks: current.tasks.filter((task) => task.id !== jobId) }
    })
  },

  cancel: (jobId) => {
    const task = get().tasks.find((candidate) => candidate.id === jobId)
    if (task === undefined || task.failed) return
    set((state) => ({
      tasks: state.tasks.map((candidate) =>
        candidate.id === jobId ? { ...candidate, canceling: true } : candidate
      )
    }))
    cancellers.get(jobId)?.()
  },

  clearFailures: () =>
    set((state) => ({ tasks: state.tasks.filter((task) => !task.failed) })),

  clear: () => {
    for (const timer of visibilityTimers.values()) window.clearTimeout(timer)
    visibilityTimers.clear()
    set({ tasks: [] })
  }
}))