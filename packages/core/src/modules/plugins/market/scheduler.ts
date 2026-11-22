import type { PluginUpdateCheckResult } from '@researchos/shared'
import { PLUGIN_MARKET_STARTUP_DELAY_MS } from '@researchos/shared'

import type { Logger } from '../../../logger'

/**
 * Background update polling. One chained timer, never a
 * standing interval: the first check waits out the cold-start delay, each
 * following check is scheduled from the interval setting read at that moment,
 * so changing 6h/12h/24h/off needs no restart and two checks can never
 * overlap. `off` stops the chain; manual checks keep working.
 */

/** What the scheduler needs from the market, kept narrow for tests. */
export interface UpdateChecker {
  checkUpdates(): Promise<PluginUpdateCheckResult[]>
  pollIntervalMs(): number | null
}

export interface SchedulerTimers {
  setTimeout(handler: () => void, delayMs: number): unknown
  clearTimeout(handle: unknown): void
}

const realTimers: SchedulerTimers = {
  setTimeout: (handler, delayMs) => setTimeout(handler, delayMs),
  clearTimeout: (handle) => {
    clearTimeout(handle as ReturnType<typeof setTimeout>)
  }
}

export interface PluginUpdateSchedulerOptions {
  market: UpdateChecker
  timers?: SchedulerTimers
  startupDelayMs?: number
  logger?: Logger
}

export class PluginUpdateScheduler {
  private readonly market: UpdateChecker
  private readonly timers: SchedulerTimers
  private readonly startupDelayMs: number
  private readonly logger: Logger | undefined
  private handle: unknown = null
  private running = false
  private checking: Promise<PluginUpdateCheckResult[]> | null = null

  constructor(options: PluginUpdateSchedulerOptions) {
    this.market = options.market
    this.timers = options.timers ?? realTimers
    this.startupDelayMs = options.startupDelayMs ?? PLUGIN_MARKET_STARTUP_DELAY_MS
    this.logger = options.logger
  }

  /** Arms the startup check; safe to call again after a workspace switch. */
  start(): void {
    if (this.running) return
    this.running = true
    this.arm(this.startupDelayMs)
  }

  stop(): void {
    this.running = false
    if (this.handle !== null) {
      this.timers.clearTimeout(this.handle)
      this.handle = null
    }
  }

  /** Re-reads the interval after a settings change. */
  reschedule(): void {
    if (this.handle !== null) {
      this.timers.clearTimeout(this.handle)
      this.handle = null
    }
    this.armNext()
  }

  /** Manual trigger: one check, without disturbing the existing chain. */
  async runNow(): Promise<PluginUpdateCheckResult[]> {
    return await this.check()
  }

  /** Resolves when the current check (if any) settles; used by shutdown. */
  async settled(): Promise<void> {
    await this.checking
  }

  private arm(delayMs: number): void {
    if (!this.running) return
    this.handle = this.timers.setTimeout(() => {
      this.handle = null
      void this.check().then(() => this.armNext())
    }, delayMs)
  }

  /** One chained timer: the next delay is read after every check settles. */
  private armNext(): void {
    if (!this.running || this.handle !== null) return
    const interval = this.market.pollIntervalMs()
    if (interval === null) return
    this.arm(interval)
  }

  /** Concurrent triggers share one in-flight check. */
  private check(): Promise<PluginUpdateCheckResult[]> {
    if (this.checking !== null) return this.checking
    const task = (async (): Promise<PluginUpdateCheckResult[]> => {
      try {
        const results = await this.market.checkUpdates()
        const updated = results.filter((result) => result.state === 'updated').length
        if (updated > 0 || results.some((result) => result.state !== 'up-to-date')) {
          this.logger?.info('plugin update check finished', {
            checked: results.length,
            updated,
            pending: results.filter((result) => result.state === 'pending-approval').length,
            failed: results.filter((result) => result.state === 'failed').length
          })
        }
        return results
      } catch (error) {
        this.logger?.warn('plugin update check failed', {
          error: error instanceof Error ? error.message : String(error)
        })
        return []
      } finally {
        this.checking = null
      }
    })()
    this.checking = task
    return task
  }
}
