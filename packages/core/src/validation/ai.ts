import { z } from 'zod'

import type { CommandValidator } from '@researchos/shared'
import type { AiCommands } from '@researchos/shared'

import { AI_PROTOCOLS } from '../modules/ai/capabilities'

const protocol = z.enum(AI_PROTOCOLS)
const role = z.enum(['chat_main', 'embedding', 'rerank'])
const capability = z.enum(['chat', 'embedding', 'rerank'])
const id = z.string().min(1).max(64)
const message = z
  .object({
    role: z.enum(['system', 'user', 'assistant']),
    content: z.string()
  })
  .strict()

const providerCreate = z
  .object({
    provider: z
      .object({
        name: z.string().trim().min(1).max(200),
        protocol,
        baseUrl: z.string().min(1).max(2048),
        apiKey: z.string().optional(),
        enabled: z.boolean().optional()
      })
      .strict()
  })
  .strict()

const providerUpdate = z
  .object({
    providerId: id,
    patch: z
      .object({
        name: z.string().trim().min(1).max(200).optional(),
        protocol: protocol.optional(),
        baseUrl: z.string().min(1).max(2048).optional(),
        apiKey: z.string().optional(),
        clearKey: z.boolean().optional(),
        enabled: z.boolean().optional()
      })
      .strict()
      .refine((value) => !(value.apiKey !== undefined && value.clearKey === true), {
        message: 'apiKey and clearKey are mutually exclusive',
        path: ['apiKey']
      })
  })
  .strict()

const chat = z
  .object({
    role: role.optional(),
    messages: z.array(message).min(1),
    temperature: z.number().min(0).max(2).optional(),
    maxOutputTokens: z.number().int().min(0).max(200_000).optional()
  })
  .strict()

const outputList = z
  .object({
    projectId: id,
    entityType: z.string().min(1).max(64).optional(),
    entityId: id.optional(),
    taskKind: z.string().min(1).max(64).optional(),
    status: z.enum(['draft', 'confirmed']).optional(),
    limit: z.number().int().min(1).max(200).optional(),
    offset: z.number().int().min(0).optional()
  })
  .strict()

const outputGet = z.object({ projectId: id, outputId: id }).strict()
const outputUpdate = z
  .object({ projectId: id, outputId: id, contentMd: z.string().max(2_000_000) })
  .strict()
const outputConfirm = z
  .object({
    projectId: id,
    outputId: id,
    contentMd: z.string().max(2_000_000).optional()
  })
  .strict()

const noPayload: CommandValidator<undefined> = { parse: () => undefined }

function validator<T>(schema: z.ZodType<T>): CommandValidator<T> {
  return { parse: (input) => schema.parse(input) }
}

/** Typed validators for every AI channel; the handler registers these. */
export const aiValidators: {
  [C in keyof AiCommands]: CommandValidator<AiCommands[C]['req']>
} = {
  'ai.providers.list': noPayload,
  'ai.providers.create': validator(providerCreate),
  'ai.providers.update': validator(providerUpdate),
  'ai.providers.delete': validator(z.object({ providerId: id }).strict()),
  'ai.providers.test': validator(z.object({ providerId: id }).strict()),

  'ai.models.list': validator(
    z
      .object({
        providerId: id,
        capability: capability.optional(),
        includeUnavailable: z.boolean().optional()
      })
      .strict()
  ),
  'ai.models.refresh': validator(z.object({ providerId: id }).strict()),
  'ai.models.addManual': validator(
    z
      .object({
        providerId: id,
        modelId: z.string().min(1).max(200),
        displayName: z.string().max(200).optional()
      })
      .strict()
  ),
  'ai.models.remove': validator(z.object({ providerId: id, modelId: z.string().min(1).max(200) }).strict()),

  'ai.roles.list': noPayload,
  'ai.roles.set': validator(
    z.object({ role, providerId: id, modelId: z.string().min(1).max(200) }).strict()
  ),
  'ai.roles.clear': validator(z.object({ role }).strict()),

  'ai.secrets.set': validator(
    z.object({ providerId: id, apiKey: z.string().min(1).max(8192) }).strict()
  ),
  'ai.secrets.delete': validator(z.object({ providerId: id }).strict()),
  'ai.secrets.status': validator(z.object({ providerId: id }).strict()),

  'ai.probe': validator(z.object({ role: role.optional() }).strict()),
  'ai.chat': validator(chat),
  'ai.availability': validator(
    z.object({ refresh: z.boolean().optional() }).strict().optional()
  ),

  'ai.outputs.list': validator(outputList),
  'ai.outputs.get': validator(outputGet),
  'ai.outputs.confirm': validator(outputConfirm),
  'ai.outputs.update': validator(outputUpdate),
  'ai.outputs.discard': validator(z.object({ projectId: id, outputId: id }).strict())
}

export const aiSchemas = {
  protocol,
  role,
  capability,
  providerCreate,
  providerUpdate,
  chat,
  outputList,
  outputGet,
  outputUpdate,
  outputConfirm
}