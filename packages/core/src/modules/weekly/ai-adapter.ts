import { AppError, AppErrorCode, type AiChatCallResult, type AiRole } from '@researchos/shared'

import type { AiGateway } from '../ai/gateway'
import type { AiOutputService } from '../ai/output-service'
import { isTruncatedFinish } from '../ai/structured'
import type { WeeklyAiPort, WeeklyAiRequest, WeeklyAiResult } from './context'

/**
 * Adapter from the AI module to the weekly generation port.
 *
 * One weekly section is one chat call: the gateway resolves the chat role,
 * owns retries/timeouts/stream deltas, and `AiOutputService.createDraft`
 * records the provenance row the report points at. `confirmOutput` flips the
 * same rows inside the report confirmation transaction.
 */
export interface WeeklyAiAdapterDeps {
  gateway: Pick<AiGateway, 'chat'>
  outputs: Pick<AiOutputService, 'createDraft' | 'confirmTx'>
  projectId: string
  /** Chat role; default `chat_main`. */
  role?: AiRole
}

export function createWeeklyAiPort(deps: WeeklyAiAdapterDeps): WeeklyAiPort {
  const role: AiRole = deps.role ?? 'chat_main'
  return {
    async generate(request: WeeklyAiRequest): Promise<WeeklyAiResult> {
      const result: AiChatCallResult = await deps.gateway.chat(role, {
        messages: request.messages,
        maxOutputTokens: request.maxOutputTokens,
        onDelta: request.onDelta,
        signal: request.signal
      })
      if (isTruncatedFinish(result.finishReason)) {
        throw new AppError(
          AppErrorCode.AiOutputTruncated,
          'AI 输出被截断（finish_reason=length）；请缩短本节事实或提高输出上限',
          { finishReason: result.finishReason, maxOutputTokens: request.maxOutputTokens }
        )
      }
      if (result.content.trim() === '') {
        throw new AppError(AppErrorCode.AiOutputInvalid, 'AI 返回空内容', {
          finishReason: result.finishReason
        })
      }
      const draft = deps.outputs.createDraft({
        projectId: deps.projectId,
        taskKind: request.taskKind,
        entity: request.entity,
        contentMd: result.content,
        provenance: {
          providerId: result.providerId,
          providerName: result.providerName,
          protocol: result.protocol,
          modelId: result.modelId,
          promptVersion: request.promptVersion,
          sourceRefs: request.sourceRefs.map((ref) => ({
            entityType: ref.entityType,
            entityId: ref.entityId
          })),
          parameters: {
            max_output_tokens: request.maxOutputTokens,
            finish_reason: result.finishReason,
            streamed: result.streamed
          }
        },
        usage: result.usage
      })
      return {
        outputId: draft.id,
        contentMd: result.content,
        attempts: Math.max(result.attempts, 1),
        provenance: {
          providerId: result.providerId,
          modelId: result.modelId,
          promptVersion: request.promptVersion,
          generatedAt: draft.createdAt
        }
      }
    },

    confirmOutput(outputId: string, actor: 'user'): void {
      deps.outputs.confirmTx(outputId, actor)
    }
  }
}