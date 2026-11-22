import type { AiGateway } from '../ai/gateway'
import { embeddingProfileId } from '../ai/gateway'
import type { RoleService } from '../ai/role-service'
import type { EmbeddingPort, EmbeddingProfile } from './types'

/**
 * Adapts the AI gateway's `embedding` role to the search embedding port.
 *
 * Keyword-only search stays the default: when the role is unassigned, the
 * provider is disabled, or the secret is missing, `currentProfile()` returns
 * null and the engine reports `vector_unavailable` instead of failing.
 */
export function aiEmbeddingPort(gateway: AiGateway, roles: RoleService): EmbeddingPort {
  return {
    currentProfile: async (): Promise<EmbeddingProfile | null> => {
      try {
        const resolved = roles.resolve('embedding')
        return {
          // The gateway derives the same opaque profile key; keeping it here
          // means vectors stay isolated per provider+model space.
          id: embeddingProfileId(resolved.provider.id, resolved.model.modelId),
          // Dimension is discovered on the first embed call in this process.
          dim: 0
        }
      } catch {
        return null
      }
    },
    embed: async (texts, signal) => {
      const result = await gateway.embed('embedding', [...texts], signal === undefined ? {} : { signal })
      return result.vectors
    }
  }
}
