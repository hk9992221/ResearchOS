import { useEffect, useRef } from 'react'

import type { AppErrorShape } from '@researchos/shared'

import { ipc } from '../../lib/ipc'

export interface JobOutcome {
  readonly state: 'succeeded' | 'failed' | 'cancelled'
  readonly error?: AppErrorShape
}

/**
 * useJobOutcome waits for one job the page started. Progress is not handled
 * here: `wireAppEvents` already feeds `job.progress` to the shell's task strip.
 *
 * The job result is deliberately not read from the event (it carries none): a
 * successful scan is re-read through the deterministic preview command, and a
 * successful analysis appears in the snapshot list. The failure error keeps its
 * typed shape, so the page can surface `details.cause` instead of an opaque
 * "job failed".
 */
export function useJobOutcome(
  jobId: string | null,
  onDone: (outcome: JobOutcome) => void
): void {
  const handler = useRef(onDone)
  handler.current = onDone

  useEffect(() => {
    if (jobId === null) return undefined
    return ipc.on('job.done', (event) => {
      if (event.jobId !== jobId) return
      handler.current({
        state: event.state,
        ...(event.error === undefined ? {} : { error: event.error })
      })
    })
  }, [jobId])
}