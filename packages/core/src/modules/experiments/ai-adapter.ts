import type { AiChatCallRequest, AiRole, AiStructuredCallResult } from '@researchos/shared'
import { AppError, AppErrorCode, isAppError } from '@researchos/shared'

import type { AiGateway } from '../ai/gateway'
import type { AiOutputService } from '../ai/output-service'
import { isTruncatedFinish } from '../ai/structured'
import type { AiOutputRecord, ExperimentsAiPort } from './ai'

export interface ExperimentsAiAdapterDeps {
  gateway: Pick<AiGateway, 'chatStructured'>
  outputs: Pick<AiOutputService, 'createDraft'>
  /** Project that owns the `ai_outputs` rows; request.projectId wins when set. */
  projectId: string
  /** Chat role; defaults to `chat_main`. */
  role?: AiRole
}

/**
 * Adapter from the W3 `AiGateway` (`modules/ai/index.ts`) to the experiments AI
 * port. One analysis is one `chatStructured` call; the gateway owns the role
 * resolution, retries, timeouts and protocol-specific schema mode, while
 * `AiOutputService.createDraft` records the provenance row the snapshot points
 * at. Truncation is rejected before the draft is written (defect 1).
 */
export function createExperimentsAiPort(deps: ExperimentsAiAdapterDeps): ExperimentsAiPort {
  const role: AiRole = deps.role ?? 'chat_main'
  return {
    async completeStructured(request) {
      const call: AiChatCallRequest = {
        messages: request.messages,
        maxOutputTokens: request.maxOutputTokens,
        ...(request.temperature === undefined ? {} : { temperature: request.temperature }),
        ...(request.signal === undefined ? {} : { signal: request.signal })
      }
      let result: AiStructuredCallResult
      try {
        result = await deps.gateway.chatStructured(role, call, request.jsonSchema, {
          name: request.schemaName,
          repairAttempts: 1,
          ...(request.validate === undefined ? {} : { validate: request.validate })
        })
      } catch (error) {
        if (isAppError(error)) throw error
        throw new AppError(
          AppErrorCode.AiUnavailable,
          error instanceof Error ? error.message : String(error),
          { reason: 'ai_generation_failed' },
          { cause: error }
        )
      }
      if (isTruncatedFinish(result.finishReason)) {
        throw new AppError(
          AppErrorCode.AiOutputTruncated,
          'AI 输出被截断（finish_reason=length）',
          { finish_reason: result.finishReason, max_output_tokens: request.maxOutputTokens }
        )
      }
      const output = deps.outputs.createDraft({
        projectId: request.projectId === '' ? deps.projectId : request.projectId,
        taskKind: request.taskKind,
        entity: request.entity ?? null,
        contentMd: result.content,
        provenance: {
          providerId: result.providerId,
          providerName: result.providerName,
          protocol: result.protocol,
          modelId: result.modelId,
          promptVersion: request.promptVersion,
          sourceRefs: request.sourceRefs.map((ref) => ({
            entityType: ref.entityType,
            entityId: ref.entityId,
            locator: ref.locator,
            title: ref.title
          })),
          parameters: {
            max_output_tokens: request.maxOutputTokens,
            temperature: request.temperature ?? null,
            finish_reason: result.finishReason,
            attempts: result.attempts,
            repaired: result.repaired
          }
        },
        usage: result.usage
      })
      const record: AiOutputRecord = {
        id: output.id,
        contentMd: output.contentMd,
        createdAt: output.createdAt,
        provenance: {
          providerId: output.providerId ?? result.providerId,
          providerName: output.providerName ?? result.providerName,
          protocol: output.protocol ?? result.protocol,
          modelId: output.modelId ?? result.modelId,
          promptVersion: output.promptVersion ?? request.promptVersion,
          parameters: output.parameters ?? {},
          sourceRefs: request.sourceRefs,
          generatedAt: output.createdAt
        }
      }
      return {
        json: result.value,
        output: record,
        ...(result.finishReason === null ? {} : { finishReason: result.finishReason })
      }
    }
  }
}
