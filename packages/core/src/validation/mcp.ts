/**
 * IPC validators for the MCP settings/confirmation/job channels.
 * The router parses with these before any service sees the input; unknown
 * properties are rejected so a renderer bug cannot smuggle state.
 */
import { z } from 'zod'
import { isIP } from 'node:net'

import type { CommandValidator, McpCommands } from '@researchos/shared'

const id = z.string().min(1).max(64)
const accessMode = z.enum(['read-only', 'read-write'])
const bindHost = z
  .string()
  .trim()
  .min(1)
  .max(64)
  .refine((value) => value === 'localhost' || isIP(value) !== 0, {
    message: 'host must be an IP address or localhost'
  })
const confirmationState = z.enum([
  'requested',
  'approved',
  'executing',
  'executed',
  'failed',
  'denied',
  'expired',
  'cancelled'
])
const jobState = z.enum(['queued', 'running', 'succeeded', 'failed', 'cancelled'])

const clientCreate = z
  .object({
    name: z.string().trim().min(1).max(100),
    enabled: z.boolean().optional()
  })
  .strict()

const clientUpdate = z
  .object({
    clientId: id,
    name: z.string().trim().min(1).max(100).optional(),
    enabled: z.boolean().optional()
  })
  .strict()

const settingsUpdate = z
  .object({
    enabled: z.boolean().optional(),
    httpEnabled: z.boolean().optional(),
    host: bindHost.optional(),
    port: z.number().int().min(1).max(65535).optional(),
    accessMode: accessMode.optional()
  })
  .strict()

const confirmationFilter = z
  .object({
    state: confirmationState.optional(),
    projectId: id.optional(),
    limit: z.number().int().min(1).max(200).optional()
  })
  .strict()

const confirmationDecision = z
  .object({
    id,
    note: z.string().max(2000).optional()
  })
  .strict()

const noPayload: CommandValidator<undefined> = { parse: () => undefined }

function validator<T>(schema: z.ZodType<T>): CommandValidator<T> {
  return { parse: (input) => schema.parse(input) }
}

export const mcpValidators: {
  [C in keyof McpCommands]: CommandValidator<McpCommands[C]['req']>
} = {
  'mcp.status': noPayload,
  'mcp.probe': noPayload,
  'mcp.retention.get': noPayload,
  'mcp.retention.set': validator(z.object({ days: z.number().int().min(1).max(3650) }).strict()),
  'mcp.start': noPayload,
  'mcp.stop': noPayload,
  'mcp.settings.update': validator(settingsUpdate),

  'mcp.clients.list': noPayload,
  'mcp.clients.create': validator(clientCreate),
  'mcp.clients.update': validator(clientUpdate),
  'mcp.clients.delete': validator(z.object({ clientId: id }).strict()),

  'mcp.confirmations.list': validator(confirmationFilter),
  'mcp.confirmations.get': validator(z.object({ id }).strict()),
  'mcp.confirmations.approve': validator(confirmationDecision),
  'mcp.confirmations.reject': validator(confirmationDecision),
  'mcp.confirmations.cancel': validator(z.object({ id }).strict()),

  'mcp.jobs.list': validator(
    z.object({ state: jobState.optional(), projectId: id.optional() }).strict()
  ),
  'mcp.jobs.get': validator(z.object({ id }).strict()),
  'mcp.jobs.cancel': validator(z.object({ id }).strict()),

  'mcp.tools.list': noPayload
}

export const mcpSchemas = {
  accessMode,
  confirmationState,
  jobState,
  clientCreate,
  clientUpdate,
  settingsUpdate,
  confirmationFilter,
  confirmationDecision
}