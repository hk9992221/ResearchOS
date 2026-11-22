/**
 * Tool construction and result shaping (rewrite plan §3.6,
 * defect 5). Every W/C/writing-J schema declares `idempotency_key` because
 * `writeTool`/`jobTool` inject it here — schema and policy cannot drift again.
 */
import { z } from 'zod'

import type { AppErrorCode } from '@researchos/shared'
import { AppError, AppErrorCode as Codes, isAppError } from '@researchos/shared'

import { appError } from '../../errors'
import type {
  HandlerContext,
  ProgressReporter,
  TextContent,
  ToolAnnotations,
  ToolCategory,
  ToolDefinition,
  ToolResult
} from './types'

/** Argument name required by every mutating tool (1–128 characters). */
export const IDEMPOTENCY_KEY_FIELD = 'idempotency_key'

/** Injected schema field; the description tells an agent what to send. */
export const idempotencyKeySchema = z
  .string()
  .min(1)
  .max(128)
  .describe('幂等键：同一逻辑写入重试时必须复用（1–128 字符）')

/** Confirmation poll interval. */
export const CONFIRMATION_POLL_MS = 2000

function withIdempotencyKey(shape: z.ZodRawShape): z.ZodRawShape {
  return { ...shape, [IDEMPOTENCY_KEY_FIELD]: idempotencyKeySchema }
}

function annotate(
  base: { title?: string; readOnlyHint?: boolean; destructiveHint?: boolean },
  overrides: ToolAnnotations | undefined
): ToolAnnotations {
  return {
    ...(base.title === undefined ? {} : { title: base.title }),
    ...(base.readOnlyHint === undefined ? {} : { readOnlyHint: base.readOnlyHint }),
    ...(base.destructiveHint === undefined ? {} : { destructiveHint: base.destructiveHint }),
    ...overrides,
    // A tool name is always a human title too.
    title: overrides?.title ?? base.title
  }
}

/** Handler argument type derived from a module's raw Zod shape. */
export type ShapeArgs<TShape extends z.ZodRawShape> = z.infer<z.ZodObject<TShape>>

export interface ReadToolSpec<TShape extends z.ZodRawShape = z.ZodRawShape> {
  name: string
  title: string
  description: string
  inputSchema: TShape
  outputSchema?: z.ZodRawShape
  annotations?: ToolAnnotations
  handler: (ctx: HandlerContext, args: ShapeArgs<TShape>) => Promise<ToolResult> | ToolResult
}

/** Registers a read-only `R` tool. */
export function readTool<TShape extends z.ZodRawShape>(
  spec: ReadToolSpec<TShape>
): ToolDefinition<ShapeArgs<TShape>> {
  return {
    name: spec.name,
    title: spec.title,
    description: spec.description,
    category: 'R',
    inputSchema: spec.inputSchema,
    ...(spec.outputSchema === undefined ? {} : { outputSchema: spec.outputSchema }),
    annotations: annotate({ title: spec.title, readOnlyHint: true }, spec.annotations),
    handler: spec.handler
  }
}

export interface WriteToolSpec<TShape extends z.ZodRawShape = z.ZodRawShape>
  extends Omit<ReadToolSpec<TShape>, 'annotations'> {
  annotations?: ToolAnnotations
}

/**
 * Registers a `W` tool: `idempotency_key` is mandatory and is declared in the
 * schema (defect 5). The host also validates against `z.strictObject(shape)`.
 */
export function writeTool<TShape extends z.ZodRawShape>(
  spec: WriteToolSpec<TShape>
): ToolDefinition<ShapeArgs<TShape>> {
  return {
    name: spec.name,
    title: spec.title,
    description: spec.description,
    category: 'W',
    inputSchema: withIdempotencyKey(spec.inputSchema),
    ...(spec.outputSchema === undefined ? {} : { outputSchema: spec.outputSchema }),
    annotations: annotate({ title: spec.title, readOnlyHint: false }, spec.annotations),
    handler: spec.handler
  }
}

export interface ConfirmToolSpec<TShape extends z.ZodRawShape = z.ZodRawShape>
  extends Omit<ReadToolSpec<TShape>, 'annotations'> {
  annotations?: ToolAnnotations
}

/**
 * Registers a `C` tool. It enqueues a desktop confirmation and returns
 * `status:'pending'`; it never performs the destructive write itself.
 */
export function confirmTool<TShape extends z.ZodRawShape>(
  spec: ConfirmToolSpec<TShape>
): ToolDefinition<ShapeArgs<TShape>> {
  return {
    name: spec.name,
    title: spec.title,
    description: spec.description,
    category: 'C',
    inputSchema: withIdempotencyKey(spec.inputSchema),
    ...(spec.outputSchema === undefined ? {} : { outputSchema: spec.outputSchema }),
    annotations: annotate(
      { title: spec.title, readOnlyHint: false, destructiveHint: true },
      spec.annotations
    ),
    handler: spec.handler
  }
}

export interface JobToolSpec<TShape extends z.ZodRawShape = z.ZodRawShape>
  extends Omit<ReadToolSpec<TShape>, 'annotations'> {
  /** Read-only jobs are visible in read-only mode and need no key. */
  readOnly: boolean
  annotations?: ToolAnnotations
}

/**
 * Registers a `J` tool. `readOnlyHint` is derived from the `readOnly` flag so
 * read-only mode can list read-only jobs and hide writing jobs.
 */
export function jobTool<TShape extends z.ZodRawShape>(
  spec: JobToolSpec<TShape>
): ToolDefinition<ShapeArgs<TShape>> {
  return {
    name: spec.name,
    title: spec.title,
    description: spec.description,
    category: 'J',
    inputSchema: spec.readOnly ? spec.inputSchema : withIdempotencyKey(spec.inputSchema),
    ...(spec.outputSchema === undefined ? {} : { outputSchema: spec.outputSchema }),
    annotations: annotate(
      {
        title: spec.title,
        readOnlyHint: spec.readOnly,
        destructiveHint: spec.readOnly ? false : undefined
      },
      spec.annotations
    ),
    handler: spec.handler
  }
}

function textOf(payload: Record<string, unknown>): TextContent[] {
  return [{ type: 'text', text: JSON.stringify(payload) }]
}

/** A normal (non-error) tool result. */
export function ok(structuredContent: Record<string, unknown>, text?: string): ToolResult {
  return {
    content: text === undefined ? textOf(structuredContent) : [{ type: 'text', text }],
    structuredContent
  }
}

/** A `pending` confirmation result: non-error, `structuredContent.status`. */
export function pending(structuredContent: Record<string, unknown>): ToolResult {
  return ok({ status: 'pending', poll_after_ms: CONFIRMATION_POLL_MS, requires_in_app: true, ...structuredContent })
}

/** A `job` result: non-error, `structuredContent.status`. */
export function jobQueued(structuredContent: Record<string, unknown>): ToolResult {
  return ok({ status: 'job', ...structuredContent })
}

function detailsOf(error: AppError): Record<string, unknown> {
  return { ...(error.details ?? {}) }
}

/**
 * The single AppError → tool-result mapping. A known domain error preserves its
 * code and details; only a genuinely unexpected error becomes `INTERNAL`, and
 * that one carries the trace id so the log can be found (defect 1).
 */
export function fail(error: unknown, traceId: string): ToolResult {
  const appErrorValue = isAppError(error) ? error : unexpected(error, traceId)
  const details = detailsOf(appErrorValue)
  const body: Record<string, unknown> = {
    code: appErrorValue.code,
    message: appErrorValue.message,
    ...(Object.keys(details).length === 0 ? {} : { details })
  }
  return {
    content: [{ type: 'text', text: `${appErrorValue.code}: ${appErrorValue.message}` }],
    structuredContent: { ...body, error: body },
    isError: true
  }
}

function unexpected(error: unknown, traceId: string): AppError {
  const message = error instanceof Error ? error.message : String(error)
  return new AppError(Codes.Internal, message, { traceId })
}

/** Convenience for handlers that fail without an underlying exception. */
export function toolError(code: AppErrorCode, message: string, details?: Record<string, unknown>): AppError {
  return appError(code, message, details === undefined ? {} : { details })
}

/** Turns a thrown value into a ToolResult, mapping AppError precisely. */
export async function runHandler(
  handler: () => Promise<ToolResult> | ToolResult,
  traceId: string
): Promise<ToolResult> {
  try {
    return await handler()
  } catch (error) {
    return fail(error, traceId)
  }
}

/** Monotonic progress reporter that ignores regressions and clamps to 0–100. */
export function progressReporter(sink: (percent: number, stage: string) => void): ProgressReporter {
  let last = 0
  return (percent, stage = '') => {
    const next = Math.max(last, Math.min(100, Math.max(0, Math.trunc(percent))))
    last = next
    sink(next, stage)
  }
}

/** Reads the idempotency key from arguments or `_meta.idempotencyKey`. */
export function resolveIdempotencyKey(
  args: Record<string, unknown>,
  meta?: Record<string, unknown>
): string | null {
  const fromArgs = args[IDEMPOTENCY_KEY_FIELD]
  const fromMeta = meta?.['idempotencyKey']
  const argsKey = typeof fromArgs === 'string' ? fromArgs : null
  const metaKey = typeof fromMeta === 'string' ? fromMeta : null
  if (argsKey !== null && argsKey.length > 128) {
    throw appError(Codes.InvalidArgument, 'idempotency_key 长度必须在 1–128 之间', {
      details: { field: IDEMPOTENCY_KEY_FIELD }
    })
  }
  if (metaKey !== null && metaKey.length > 128) {
    throw appError(Codes.InvalidArgument, 'idempotencyKey 长度必须在 1–128 之间', {
      details: { field: '_meta.idempotencyKey' }
    })
  }
  if (argsKey !== null && metaKey !== null && argsKey !== metaKey) {
    throw appError(Codes.InvalidArgument, 'idempotency_key 与 _meta.idempotencyKey 不一致', {
      details: { field: IDEMPOTENCY_KEY_FIELD }
    })
  }
  return argsKey ?? metaKey
}

/** Strips the idempotency key before canonical hashing and service calls. */
export function stripIdempotencyKey(args: Record<string, unknown>): Record<string, unknown> {
  if (!(IDEMPOTENCY_KEY_FIELD in args)) return args
  const { [IDEMPOTENCY_KEY_FIELD]: _ignored, ...rest } = args
  return rest
}

const categoryOrder: Readonly<Record<ToolCategory, number>> = { R: 0, J: 1, W: 2, C: 3 }

/** Deterministic catalog order: category, then name. */
export function compareTools(left: ToolDefinition, right: ToolDefinition): number {
  const byCategory = categoryOrder[left.category] - categoryOrder[right.category]
  return byCategory !== 0 ? byCategory : left.name.localeCompare(right.name)
}