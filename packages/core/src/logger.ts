/**
 * The foundation logging surface. Key/value arguments are
 * structured fields, never string interpolation, so logs stay queryable and a
 * ported Go call site keeps its shape.
 */
export type LogFields = Record<string, unknown>

export interface Logger {
  debug(message: string, fields?: LogFields): void
  info(message: string, fields?: LogFields): void
  warn(message: string, fields?: LogFields): void
  error(message: string, fields?: LogFields): void
}

export type LogLevel = 'debug' | 'info' | 'warn' | 'error'

const LEVEL_ORDER: Readonly<Record<LogLevel, number>> = { debug: 10, info: 20, warn: 30, error: 40 }

export class ConsoleLogger implements Logger {
  private readonly threshold: number
  private readonly sink: Pick<Console, 'debug' | 'info' | 'warn' | 'error'>

  constructor(level: LogLevel = 'info', sink: Pick<Console, 'debug' | 'info' | 'warn' | 'error'> = console) {
    this.threshold = LEVEL_ORDER[level]
    this.sink = sink
  }

  private write(level: LogLevel, message: string, fields?: LogFields): void {
    if (LEVEL_ORDER[level] < this.threshold) return
    this.sink[level](`[researchos] ${message}`, fields ?? {})
  }

  debug(message: string, fields?: LogFields): void {
    this.write('debug', message, fields)
  }

  info(message: string, fields?: LogFields): void {
    this.write('info', message, fields)
  }

  warn(message: string, fields?: LogFields): void {
    this.write('warn', message, fields)
  }

  error(message: string, fields?: LogFields): void {
    this.write('error', message, fields)
  }
}

/** A logger that discards everything; the default for library calls. */
export class NullLogger implements Logger {
  debug(): void {}
  info(): void {}
  warn(): void {}
  error(): void {}
}