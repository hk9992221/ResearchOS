import { z } from 'zod'

import type { PluginCommands } from '@researchos/shared'
import { PLUGIN_MARKET_INTERVALS, PLUGIN_PERMISSIONS } from '@researchos/shared'

/**
 * Zod schemas for the plugin IPC channels. The router validates before
 * dispatch, so the service only sees approved types. Confirmation tokens are
 * opaque strings minted by the human prompt.
 */

const pluginId = z
  .string()
  .min(1)
  .max(128)
  .regex(/^[a-z0-9][a-z0-9._-]*$/, 'must be a safe plugin id')
const sourcePath = z.string().min(1).max(4096)
const permission = z.enum(PLUGIN_PERMISSIONS)
const permissions = z.array(permission).max(PLUGIN_PERMISSIONS.length)
const networkDomains = z.array(z.string().trim().min(1).max(253)).max(50)
const confirmationToken = z.string().min(1).max(256).optional()
const confirmWriteScopes = z.boolean().optional()

export const pluginInstallSchema = z
  .object({ sourcePath, enable: z.boolean().optional() })
  .strict()

export const pluginEnableSchema = z
  .object({
    pluginId,
    permissions: permissions.optional(),
    networkDomains: networkDomains.optional(),
    confirmationToken,
    confirmWriteScopes
  })
  .strict()

export const pluginIdSchema = z.object({ pluginId }).strict()

export const pluginUpdateSchema = z.object({ pluginId, sourcePath }).strict()

export const pluginGrantsSchema = z
  .object({ pluginId, permissions, networkDomains: networkDomains.optional(), confirmationToken, confirmWriteScopes })
  .strict()

export const pluginRpcCallSchema = z
  .object({
    pluginId,
    method: z.string().min(1).max(128),
    params: z.unknown(),
    confirmationToken
  })
  .strict()

/* ------------------------------------------------------------------ market */

const registryId = z
  .string()
  .min(1)
  .max(128)
  .regex(/^[a-z0-9][a-z0-9._-]*$/, 'must be a safe registry id')
const remoteUrl = z.string().trim().min(1).max(2048)
const optionalRemoteUrl = z.string().trim().max(2048).optional()
const repo = z.string().trim().min(3).max(200)
const tag = z.string().trim().min(1).max(120)

/** The grant trio every market install/update may carry. */
const grants = {
  permissions: permissions.optional(),
  networkDomains: networkDomains.optional(),
  confirmWriteScopes,
  confirmationToken
}

const marketSearchSchema = z
  .object({
    query: z.string().max(200).optional(),
    tags: z.array(z.string().trim().min(1).max(40)).max(20).optional(),
    verifiedOnly: z.boolean().optional(),
    registryId: registryId.optional()
  })
  .strict()

const marketRefreshSchema = z.object({ force: z.boolean().optional() }).strict()

const marketDetailSchema = z.object({ pluginId, registryId: registryId.optional() }).strict()

const marketInstallSchema = z
  .object({ pluginId, registryId: registryId.optional(), ...grants })
  .strict()

const githubPreviewSchema = z.object({ repo, tag: tag.optional() }).strict()

const githubInstallSchema = z
  .object({ repo, tag: tag.optional(), confirmUnverified: z.boolean().optional(), ...grants })
  .strict()

const marketUpdateSchema = z.object({ pluginId, ...grants }).strict()

const autoUpdateSchema = z.object({ pluginId, enabled: z.boolean() }).strict()

const registryAddSchema = z
  .object({ name: z.string().trim().min(1).max(120), url: remoteUrl })
  .strict()

const registryRemoveSchema = z.object({ id: registryId }).strict()

const marketSettingsUpdateSchema = z
  .object({
    registryUrl: optionalRemoteUrl,
    mirrorPrefix: optionalRemoteUrl,
    interval: z.enum(PLUGIN_MARKET_INTERVALS).optional()
  })
  .strict()

/** Adapts a zod schema to the router's structural validator contract. */
function validatorOf<T extends z.ZodType>(schema: T): { parse(input: unknown): z.infer<T> } {
  return {
    parse: (input: unknown): z.infer<T> => schema.parse(input)
  }
}

const promptRespondSchema = z
  .object({
    requestId: z.string().min(1).max(64),
    submitted: z.boolean().optional(),
    confirmed: z.boolean().optional(),
    value: z.string().max(10_000).optional()
  })
  .strict()

const hostContextSetSchema = z
  .object({
    projectId: z.string().min(1).max(128).nullable().optional(),
    theme: z.enum(['light', 'dark']).optional(),
    locale: z.string().min(1).max(32).optional()
  })
  .strict()

const noPayload = {
  parse: (_input?: unknown): undefined => undefined
}

export const pluginValidators = {
  'plugins.list': noPayload,
  'plugins.install': validatorOf(pluginInstallSchema),
  'plugins.enable': validatorOf(pluginEnableSchema),
  'plugins.disable': validatorOf(pluginIdSchema),
  'plugins.update': validatorOf(pluginUpdateSchema),
  'plugins.uninstall': validatorOf(pluginIdSchema),
  'plugins.grants.set': validatorOf(pluginGrantsSchema),
  'plugins.rpc.call': validatorOf(pluginRpcCallSchema),
  'plugins.commands.list': noPayload,
  'plugins.prompt.respond': validatorOf(promptRespondSchema),
  'plugins.host.context.set': validatorOf(hostContextSetSchema),
  'plugins.market.search': validatorOf(marketSearchSchema),
  'plugins.market.refresh': validatorOf(marketRefreshSchema),
  'plugins.market.detail': validatorOf(marketDetailSchema),
  'plugins.market.install': validatorOf(marketInstallSchema),
  'plugins.market.previewGithub': validatorOf(githubPreviewSchema),
  'plugins.market.installFromGithub': validatorOf(githubInstallSchema),
  'plugins.market.checkUpdates': noPayload,
  'plugins.market.update': validatorOf(marketUpdateSchema),
  'plugins.setAutoUpdate': validatorOf(autoUpdateSchema),
  'plugins.registries.list': noPayload,
  'plugins.registries.add': validatorOf(registryAddSchema),
  'plugins.registries.remove': validatorOf(registryRemoveSchema),
  'plugins.market.settings.get': noPayload,
  'plugins.market.settings.update': validatorOf(marketSettingsUpdateSchema)
} satisfies Record<keyof PluginCommands, { parse(input: unknown): unknown }>

export const pluginSchemaByChannel = pluginValidators