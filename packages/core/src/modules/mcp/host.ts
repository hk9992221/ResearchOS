/**
 * The tool-call pipeline shared by HTTP and any future transport:
 * mode check → archived gate → rate limit → idempotency → handler.
 *
 * The host never re-implements a domain rule; it applies the cross-cutting
 * policy of the MCP spec and hands a fully-resolved HandlerContext to the
 * tool. Errors are returned as `isError` results with the domain code intact.
 */
import { AppErrorCode as Codes } from '@researchos/shared'

import { type Clock, SystemClock } from '../../clock'
import type { ConfirmService } from '../../confirm'
import type { EventBus } from '../../events'
import { appError } from '../../errors'
import { newId } from '../../ids'
import { RelationService } from '../../relations'
import { TrashService } from '../../trash'
import type { WorkspaceManager } from '../../workspace'
import type { ProjectHandle } from '../../workspace/handle'
import type { Confirmations } from './confirmations'
import { IdempotencyStore, argsHash, validateIdempotencyKey } from './idempotency'
import type { Jobs } from './jobs'
import { RateLimiter } from './limit'
import { assertModeAllows, assertProjectWritable, isMutating } from './policy'
import { fail, resolveIdempotencyKey, stripIdempotencyKey } from './tools'
import type { AccessMode, HandlerContext, Principal, ToolDefinition, ToolResult } from './types'

export interface ToolHostOptions {
  tools: readonly ToolDefinition[]
  accessMode: () => AccessMode
  confirmations: Confirmations
  jobs: Jobs
  idempotency: IdempotencyStore
  limiter: RateLimiter
  workspace: WorkspaceManager
  events: EventBus
  confirm: ConfirmService
  clock?: Clock
  /** Composition-root services merged into every tool context (module runtimes). */
  servicesFor?: (projectId: string | null) => Record<string, unknown>
}

export interface ToolCall {
  name: string
  args: Record<string, unknown>
  meta?: Record<string, unknown>
  signal?: AbortSignal
  /** Test seam; production generates a fresh id. */
  traceId?: string
}

interface ProjectContext {
  id: string
  status: 'active' | 'archived'
  handle: ProjectHandle
}

export class ToolHost {
  private readonly byName: ReadonlyMap<string, ToolDefinition>
  private readonly options: ToolHostOptions
  private readonly clock: Clock

  constructor(options: ToolHostOptions) {
    this.options = options
    this.byName = new Map(options.tools.map((tool) => [tool.name, tool]))
    this.clock = options.clock ?? new SystemClock()
  }

  async call(principal: Principal, call: ToolCall): Promise<ToolResult> {
    const traceId = call.traceId ?? newId(this.clock)
    let claimedKey: string | null = null
    try {
      const definition = this.byName.get(call.name)
      if (definition === undefined) {
        throw appError(Codes.NotFound, `工具 ${call.name} 不存在`, { details: { tool: call.name } })
      }
      const mode = this.options.accessMode()
      assertModeAllows(definition, mode)

      const projectId = typeof call.args['project_id'] === 'string' ? call.args['project_id'] : null
      const mutating = isMutating(definition)
      const project =
        projectId === null || projectId === '' ? null : this.openProject(projectId)
      if (mutating && project !== null) {
        assertProjectWritable(definition.name, project.status, true)
      }

      this.options.limiter.enforce(principal.clientId, definition.name, mutating ? 'write' : 'read')

      if (mutating) {
        const key = resolveIdempotencyKey(call.args, call.meta)
        if (key === null) {
          throw appError(Codes.InvalidArgument, '写工具必须提供 idempotency_key', {
            details: { field: 'idempotency_key' }
          })
        }
        validateIdempotencyKey(key)
        const hash = argsHash(call.args)
        const claim = this.options.idempotency.claim(principal.clientId, key, definition.name, hash)
        if (claim.kind === 'replay') {
          return {
            content: [{ type: 'text', text: JSON.stringify(claim.result) }],
            structuredContent: claim.result,
            _meta: { replayed: true }
          }
        }
        claimedKey = key
      }

      const ctx = this.context(principal, projectId, project, call, traceId, claimedKey)
      const input = stripIdempotencyKey(call.args)
      const result = await definition.handler(ctx, input as never)
      if (claimedKey !== null) {
        this.options.idempotency.complete(principal.clientId, claimedKey, result.structuredContent)
      }
      return result
    } catch (error) {
      if (claimedKey !== null) {
        this.options.idempotency.release(principal.clientId, claimedKey)
      }
      return fail(error, traceId)
    }
  }

  private context(
    principal: Principal,
    projectId: string | null,
    project: ProjectContext | null,
    call: ToolCall,
    traceId: string,
    idempotencyKey: string | null
  ): HandlerContext {
    const handle = project?.handle ?? null
    const services: Record<string, unknown> = {
      confirmations: this.options.confirmations,
      jobs: this.options.jobs,
      events: this.options.events,
      confirm: this.options.confirm,
      workspace: this.options.workspace
    }
    if (handle !== null) {
      services['relations'] = new RelationService(handle, this.options.events, {
        global: { db: this.options.workspace.global.db },
        clock: this.clock
      })
      services['trash'] = new TrashService(handle, this.options.events, this.options.confirm, {
        clock: this.clock
      })
    }
    Object.assign(services, this.options.servicesFor?.(projectId) ?? {})
    return {
      principal,
      projectId,
      idempotencyKey,
      signal: call.signal ?? new AbortController().signal,
      progress: () => undefined,
      traceId,
      services,
      project: handle,
      global: this.options.workspace.global,
      clock: this.clock,
      db: this.options.workspace.global.db
    }
  }

  /** Resolves the project status and handle together; missing is NOT_FOUND. */
  private openProject(projectId: string): ProjectContext {
    const row = this.options.workspace.global.db
      .prepare('SELECT status FROM projects WHERE id = ?')
      .get(projectId) as { status: string } | undefined
    if (row === undefined) {
      throw appError(Codes.NotFound, `项目 ${projectId} 不存在`, { details: { project_id: projectId } })
    }
    let handle: ProjectHandle
    try {
      handle = this.options.workspace.project(projectId)
    } catch (cause) {
      throw appError(Codes.NotFound, `项目 ${projectId} 不存在`, {
        cause,
        details: { project_id: projectId }
      })
    }
    return { id: projectId, status: row.status === 'archived' ? 'archived' : 'active', handle }
  }
}