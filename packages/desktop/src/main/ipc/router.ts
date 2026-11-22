import { randomUUID } from 'node:crypto'

import {
  AppError,
  AppErrorCode,
  COMMAND_CHANNELS,
  isAppError,
  type CommandChannel,
  type CommandHandler,
  type CommandMap,
  type CommandValidator,
  type IpcResult,
  type SenderContext
} from '@researchos/shared'

export interface RouterLogger {
  error(message: string, fields?: Record<string, unknown>): void
}

export interface CommandRouterOptions {
  logger?: RouterLogger
  generateTraceId?: () => string
}

interface Registration {
  validator: CommandValidator<unknown>
  handler: (request: unknown, sender: SenderContext) => unknown | Promise<unknown>
}

const KNOWN_CHANNELS: Readonly<Record<string, true>> = Object.fromEntries(
  COMMAND_CHANNELS.map((channel) => [channel, true])
)

export class CommandRouter {
  private readonly handlers = new Map<CommandChannel, Registration>()
  private readonly logger: RouterLogger | undefined
  private readonly generateTraceId: () => string

  constructor(options: CommandRouterOptions = {}) {
    this.logger = options.logger
    this.generateTraceId = options.generateTraceId ?? randomUUID
  }

  registerHandler<C extends CommandChannel>(
    channel: C,
    validator: CommandValidator<CommandMap[C]['req']>,
    handler: CommandHandler<C>
  ): void {
    if (this.handlers.has(channel)) {
      throw new Error(`duplicate command handler: ${channel}`)
    }
    // The map is intentionally erased; each registration is typed by the
    // generic above and only re-entered through invoke() for that channel.
    const erasedValidator: CommandValidator<unknown> = validator
    const erasedHandler = handler as (request: unknown, sender: SenderContext) => unknown | Promise<unknown>
    this.handlers.set(channel, { validator: erasedValidator, handler: erasedHandler })
  }

  channels(): CommandChannel[] {
    return [...this.handlers.keys()]
  }

  /** Removes one handler so a workspace switch can re-register fresh services. */
  unregisterHandler(channel: CommandChannel): void {
    this.handlers.delete(channel)
  }

  isKnownChannel(channel: string): channel is CommandChannel {
    return KNOWN_CHANNELS[channel] === true
  }

  async invoke(channel: string, payload: unknown, sender: SenderContext): Promise<IpcResult<unknown>> {
    if (!this.isKnownChannel(channel)) {
      return {
        ok: false,
        error: {
          code: AppErrorCode.NotFound,
          message: `Unknown command channel: ${channel}`,
          details: { channel }
        }
      }
    }

    if (!sender.isMainWindow) {
      return {
        ok: false,
        error: {
          code: AppErrorCode.Forbidden,
          message: 'Command rejected: sender is not the main window',
          details: { channel }
        }
      }
    }

    const registration = this.handlers.get(channel)
    if (registration === undefined) {
      return {
        ok: false,
        error: {
          code: AppErrorCode.Internal,
          message: `No handler registered for channel: ${channel}`,
          details: { channel }
        }
      }
    }

    let request: unknown
    try {
      request = registration.validator.parse(payload)
    } catch {
      return {
        ok: false,
        error: {
          code: AppErrorCode.InvalidArgument,
          message: 'Invalid command payload',
          details: { channel }
        }
      }
    }

    try {
      const data = await registration.handler(request, sender)
      return { ok: true, data }
    } catch (error) {
      if (isAppError(error)) return { ok: false, error: error.toShape() }

      const traceId = this.generateTraceId()
      this.logger?.error('unhandled command error', { channel, traceId, error })
      const internal = new AppError(AppErrorCode.Internal, 'Internal error', { traceId })
      return { ok: false, error: internal.toShape() }
    }
  }
}

/** Validator for commands whose request is `void`. */
export const noPayloadValidator: CommandValidator<undefined> = {
  parse: () => undefined
}

/** Validator used where the core validation layer has not landed yet. */
export const passthroughValidator: CommandValidator<unknown> = {
  parse: (input) => input
}