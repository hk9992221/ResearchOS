/**
 * The tool-definition contract every domain module imports
 * (rewrite plan §3.6). One `mcp.ts` per module exports `ToolDefinition[]`; the
 * aggregator checks names and the host registers them with the official SDK.
 *
 * Categories are the MCP classes:
 *   R read   W write   C confirm (destructive, desktop approval)   J job
 *
 * A handler never returns a protocol envelope it built by hand: `tools.ts`
 * owns `ok`/`pending`/`job`/`fail` and the single AppError-to-tool mapping.
 */
import { z } from 'zod'
import type { ZodObject, ZodRawShape } from 'zod'

import type { McpAccessMode, McpTransport } from '@researchos/shared'

import type { Clock } from '../../clock'
import type { Db } from '../../db/open'
import type { GlobalHandle, ProjectHandle } from '../../workspace/handle'

export type ToolCategory = 'R' | 'W' | 'C' | 'J'

/** Machine name used by `mcp.access_mode`; mirrors the settings value. */
export type AccessMode = McpAccessMode

/**
 * Who is calling. `clientId` is the `mcp_client.id` behind the bearer token;
 * it scopes confirmations, jobs and idempotency records.
 */
export interface Principal {
  readonly clientId: string
  readonly clientName: string
  readonly transport: McpTransport
}

/** Monotonic progress reporter handed to job bodies (0–100). */
export interface ProgressReporter {
  (percent: number, stage?: string): void
}

/**
 * Collaborators resolved by the composition root for one request. It is a
 * string-keyed bag on purpose: each module narrows it to the ports it owns
 * without the MCP layer importing every domain service. Missing ports make the
 * affected tool fail with `INTERNAL`, never a silent success.
 */
export type ToolServices = Readonly<Record<string, unknown>>

/**
 * The one handler context shape. `projectId` is the request's
 * project when the tool takes one; `services` carries the resolved ports.
 *
 * `project`/`global` are already-open handles: the host applies the archived
 * gate before the handler runs, so a handler never re-reads project.status to
 * decide whether it may write. `db` is exposed for the rare tool that has no
 * service (status counters); business tools go through their module service.
 */
export interface HandlerContext {
  readonly principal: Principal
  readonly projectId: string | null
  readonly idempotencyKey: string | null
  readonly signal: AbortSignal
  readonly progress: ProgressReporter
  readonly traceId: string
  readonly services: ToolServices
  readonly project: ProjectHandle | null
  readonly global: GlobalHandle
  readonly clock: Clock
  /** Raw global connection, for runtime helpers (jobs/confirmations stores). */
  readonly db: Db
}

/** MCP content block the host always emits in addition to structuredContent. */
export interface TextContent {
  readonly type: 'text'
  readonly text: string
}

/**
 * The normalized handler result. The host adds `content` when a handler only
 * produces structured data; `pending` and `job` results are NOT errors.
 */
export interface ToolResult {
  readonly content: readonly TextContent[]
  readonly structuredContent: Record<string, unknown>
  readonly isError?: boolean
  /** MCP `_meta`; replay sets `replayed: true`. */
  readonly _meta?: Record<string, unknown>
}

/** SDK tool annotations; `readOnlyHint` also drives read-only `tools/list`. */
export interface ToolAnnotations {
  readonly title?: string
  readonly readOnlyHint?: boolean
  readonly destructiveHint?: boolean
  readonly idempotentHint?: boolean
  readonly openWorldHint?: boolean
}

/**
 * One catalog entry. `inputSchema` is a Zod raw shape so modules can use plain
 * `z.string()` fields; the host wraps it with `z.strictObject()` at
 * registration, which is what emits `additionalProperties:false`.
 */
export interface ToolDefinition<TInput = never> {
  readonly name: string
  readonly title: string
  readonly description: string
  readonly category: ToolCategory
  readonly inputSchema: ZodRawShape
  readonly outputSchema?: ZodRawShape
  readonly annotations: ToolAnnotations
  readonly handler: (ctx: HandlerContext, args: TInput) => Promise<ToolResult> | ToolResult
}

/** Builds the strict object the SDK validates arguments against. */
export function inputObject(definition: ToolDefinition<never> | ToolDefinition): ZodObject {
  return z.strictObject(definition.inputSchema) as ZodObject
}

/** True when the category writes application state (archived gate applies). */
export function isWriteCategory(category: ToolCategory): boolean {
  return category === 'W' || category === 'C'
}

/** True for a writing job (`J` without readOnlyHint). */
export function isWritingJob(definition: ToolDefinition<never> | ToolDefinition): boolean {
  return definition.category === 'J' && definition.annotations.readOnlyHint !== true
}