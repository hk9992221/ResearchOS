/**
 * Close-time flush handshake: the note editor debounces saves by
 * 600 ms, so the window must not disappear before the renderer reports that the
 * pending body write is on disk. The coordinator is transport-free; main sends
 * the `app.flush` event and the renderer acknowledges through the `app.flush`
 * command.
 */
export type FlushReason = 'close' | 'switch-workspace'
export type FlushResult = 'ack' | 'timeout' | 'superseded'

export const DEFAULT_FLUSH_TIMEOUT_MS = 1000

interface PendingFlush {
  reason: FlushReason
  resolve: (result: FlushResult) => void
  timer: NodeJS.Timeout
}

export class FlushCoordinator {
  private pending: PendingFlush | null = null

  get isPending(): boolean {
    return this.pending !== null
  }

  request(reason: FlushReason, timeoutMs = DEFAULT_FLUSH_TIMEOUT_MS): Promise<FlushResult> {
    this.settle('superseded')

    const { promise, resolve } = Promise.withResolvers<FlushResult>()
    const timer = setTimeout(() => {
      if (this.pending?.timer !== timer) return
      this.pending = null
      resolve('timeout')
    }, timeoutMs)

    this.pending = { reason, resolve, timer }
    return promise
  }

  /** Returns false when no flush was pending (renderer ack after a timeout). */
  acknowledge(): boolean {
    if (this.pending === null) return false
    const pending = this.pending
    this.pending = null
    clearTimeout(pending.timer)
    pending.resolve('ack')
    return true
  }

  cancel(): void {
    this.settle('superseded')
  }

  private settle(result: FlushResult): void {
    if (this.pending === null) return
    const pending = this.pending
    this.pending = null
    clearTimeout(pending.timer)
    pending.resolve(result)
  }
}