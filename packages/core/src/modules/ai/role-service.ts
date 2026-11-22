import type {
  AiCapability,
  AiProtocol,
  AiRole,
  AssignResult,
  ModelInfo,
  RoleAssignment,
  RoleProblem,
  RoleView
} from '@researchos/shared'
import { AppErrorCode } from '@researchos/shared'

import { formatTimestamp, SystemClock, type Clock } from '../../clock'
import type { Db } from '../../db/open'
import { appError } from '../../errors'
import { protocolSupports } from './capabilities'
import { AiError } from './errors'
import type { ProviderConfig } from './protocol/types'

export const AI_ROLES: readonly AiRole[] = ['chat_main', 'embedding', 'rerank']

const ROLE_CAPABILITY: Readonly<Record<AiRole, AiCapability>> = {
  chat_main: 'chat',
  embedding: 'embedding',
  rerank: 'rerank'
}

export function roleCapability(role: AiRole): AiCapability {
  return ROLE_CAPABILITY[role]
}

export function isAiRole(value: unknown): value is AiRole {
  return typeof value === 'string' && (AI_ROLES as readonly string[]).includes(value)
}

export interface ResolvedRole {
  role: AiRole
  capability: AiCapability
  provider: ProviderConfig
  model: ModelInfo
}

export interface RoleService {
  list(): RoleView[]
  assign(role: AiRole, providerId: string, modelId: string): AssignResult
  clear(role: AiRole): void
  resolve(role: AiRole): ResolvedRole
}

interface AssignmentRow {
  role: AiRole
  provider_id: string
  model_id: string
  updated_at: string
}

export class SqliteRoleService implements RoleService {
  private readonly db: Db
  private readonly clock: Clock

  constructor(db: Db, clock: Clock = new SystemClock()) {
    this.db = db
    this.clock = clock
  }

  list(): RoleView[] {
    return AI_ROLES.map((role) => {
      const assignment = this.assignment(role)
      if (assignment === null) {
        return {
          role,
          requiredCapability: ROLE_CAPABILITY[role],
          assignment: null,
          providerName: null,
          resolved: false,
          problem: 'role_unassigned'
        }
      }
      try {
        const resolved = this.resolve(role)
        return {
          role,
          requiredCapability: ROLE_CAPABILITY[role],
          assignment,
          providerName: resolved.provider.name,
          resolved: true,
          problem: null
        }
      } catch (error) {
        const problem = problemOf(error)
        return {
          role,
          requiredCapability: ROLE_CAPABILITY[role],
          assignment,
          providerName: problem === 'provider_disabled' ? this.providerName(assignment.providerId) : null,
          resolved: false,
          problem
        }
      }
    })
  }

  assign(role: AiRole, providerId: string, modelId: string): AssignResult {
    if (!isAiRole(role)) {
      throw appError(AppErrorCode.InvalidArgument, `unknown AI role: ${String(role)}`, {
        field: 'role'
      })
    }
    const capability = ROLE_CAPABILITY[role]
    const provider = this.provider(providerId)
    if (!provider.enabled) {
      throw new AiError('provider_disabled', `provider ${provider.name} is disabled`, {
        providerId: provider.id,
        providerName: provider.name,
        protocol: provider.protocol,
        modelId
      })
    }
    if (!protocolSupports(provider.protocol, capability)) {
      throw new AiError(
        'unsupported_capability',
        `protocol ${provider.protocol} does not support ${capability}`,
        {
          providerId: provider.id,
          providerName: provider.name,
          protocol: provider.protocol,
          modelId,
          details: { problem: 'capability_missing' }
        }
      )
    }
    this.loadModel(provider, modelId, capability)
    const previous = this.assignment(role)
    const now = formatTimestamp(this.clock.now())
    this.db
      .prepare(
        `INSERT INTO role_assignments (role, provider_id, model_id, updated_at)
         VALUES (?, ?, ?, ?)
         ON CONFLICT(role) DO UPDATE SET provider_id = excluded.provider_id,
           model_id = excluded.model_id, updated_at = excluded.updated_at`
      )
      .run(role, providerId, modelId, now)
    const reindexRequired =
      role === 'embedding' &&
      (previous === null ||
        previous.providerId !== providerId ||
        previous.modelId !== modelId)
    return {
      assignment: { role, providerId, modelId, updatedAt: now },
      reindexRequired
    }
  }

  clear(role: AiRole): void {
    if (!isAiRole(role)) {
      throw appError(AppErrorCode.InvalidArgument, `unknown AI role: ${String(role)}`, {
        field: 'role'
      })
    }
    this.db.prepare(`DELETE FROM role_assignments WHERE role = ?`).run(role)
  }

  resolve(role: AiRole): ResolvedRole {
    if (!isAiRole(role)) {
      throw appError(AppErrorCode.InvalidArgument, `unknown AI role: ${String(role)}`, {
        field: 'role'
      })
    }
    const assignment = this.assignment(role)
    if (assignment === null) {
      throw new AiError('role_unassigned', `no provider is assigned to ${role}`, {
        details: { role, problem: 'role_unassigned' }
      })
    }
    const capability = ROLE_CAPABILITY[role]
    const provider = this.provider(assignment.providerId)
    if (!provider.enabled) {
      throw new AiError('provider_disabled', `provider ${provider.name} is disabled`, {
        providerId: provider.id,
        providerName: provider.name,
        protocol: provider.protocol,
        modelId: assignment.modelId,
        details: { problem: 'provider_disabled' }
      })
    }
    if (!protocolSupports(provider.protocol, capability)) {
      throw new AiError(
        'unsupported_capability',
        `protocol ${provider.protocol} does not support ${capability}`,
        {
          providerId: provider.id,
          providerName: provider.name,
          protocol: provider.protocol,
          modelId: assignment.modelId,
          details: { problem: 'capability_missing' }
        }
      )
    }
    const model = this.loadModel(provider, assignment.modelId, capability)
    return { role, capability, provider, model }
  }

  private assignment(role: AiRole): RoleAssignment | null {
    const row = this.db
      .prepare(`SELECT role, provider_id, model_id, updated_at FROM role_assignments WHERE role = ?`)
      .get(role) as AssignmentRow | undefined
    if (row === undefined) return null
    return {
      role: row.role,
      providerId: row.provider_id,
      modelId: row.model_id,
      updatedAt: row.updated_at
    }
  }

  private provider(providerId: string): ProviderConfig & { enabled: boolean } {
    const row = this.db
      .prepare(
        `SELECT id, name, protocol, base_url, api_key_ref, enabled FROM providers WHERE id = ?`
      )
      .get(providerId) as
      | {
          id: string
          name: string
          protocol: string
          base_url: string
          api_key_ref: string
          enabled: number
        }
      | undefined
    if (row === undefined) {
      throw appError(AppErrorCode.NotFound, `provider not found: ${providerId}`, { providerId })
    }
    return {
      id: row.id,
      name: row.name,
      protocol: row.protocol as AiProtocol,
      baseUrl: row.base_url,
      apiKeyRef: row.api_key_ref,
      enabled: row.enabled === 1
    }
  }

  private providerName(providerId: string): string | null {
    const row = this.db.prepare(`SELECT name FROM providers WHERE id = ?`).get(providerId) as
      | { name: string }
      | undefined
    return row?.name ?? null
  }

  private loadModel(
    provider: ProviderConfig,
    modelId: string,
    capability: AiCapability
  ): ModelInfo {
    const row = this.db
      .prepare(
        `SELECT display_name, capabilities, source, available, last_seen_at
         FROM provider_models WHERE provider_id = ? AND model_id = ?`
      )
      .get(provider.id, modelId) as
      | {
          display_name: string
          capabilities: string
          source: 'listed' | 'manual'
          available: number
          last_seen_at: string
        }
      | undefined
    if (row === undefined) {
      throw new AiError('model_unavailable', `model ${modelId} is not in the provider cache`, {
        providerId: provider.id,
        providerName: provider.name,
        protocol: provider.protocol,
        modelId,
        details: { problem: 'model_unavailable' }
      })
    }
    const capabilities = JSON.parse(row.capabilities) as AiCapability[]
    if (row.source === 'listed' && row.available !== 1) {
      throw new AiError('model_unavailable', `model ${modelId} is marked unavailable`, {
        providerId: provider.id,
        providerName: provider.name,
        protocol: provider.protocol,
        modelId,
        details: { problem: 'model_unavailable' }
      })
    }
    if (!capabilities.includes(capability) || !protocolSupports(provider.protocol, capability)) {
      throw new AiError(
        'unsupported_capability',
        `model ${modelId} does not offer ${capability}`,
        {
          providerId: provider.id,
          providerName: provider.name,
          protocol: provider.protocol,
          modelId,
          details: { problem: 'capability_missing' }
        }
      )
    }
    return {
      providerId: provider.id,
      modelId,
      displayName: row.display_name,
      capabilities,
      source: row.source === 'manual' ? 'manual' : 'upstream',
      available: true,
      lastSeenAt: row.last_seen_at
    }
  }
}

function problemOf(error: unknown): RoleProblem {
  if (error instanceof AiError) {
    if (error.kind === 'provider_disabled') return 'provider_disabled'
    if (error.kind === 'model_unavailable' || error.kind === 'model_not_found') {
      return 'model_unavailable'
    }
    if (error.kind === 'unsupported_capability') return 'capability_missing'
  }
  return 'role_unassigned'
}
