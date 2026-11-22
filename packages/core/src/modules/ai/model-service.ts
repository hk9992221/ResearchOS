import type { AiCapability, AiProtocol, ModelInfo, ModelSyncResult } from '@researchos/shared'
import { AppErrorCode } from '@researchos/shared'

import { formatTimestamp, SystemClock, type Clock } from '../../clock'
import type { Db } from '../../db/open'
import { appError } from '../../errors'
import { asAiError, isAiError, sanitize } from './errors'
import { resolveModelCapabilities } from './protocol/factory'
import type { ModelLister } from './provider-service'
import type { ProviderConfig } from './protocol/types'
import { protocolCapabilities, protocolSupports } from './capabilities'
import type { SecretStore } from './secrets'

export interface ModelServiceOptions {
  clock?: Clock
  secrets: SecretStore
  lister?: ModelLister
  listTimeoutMs?: number
}

export interface ModelQuery {
  providerId: string
  capability?: AiCapability
  includeUnavailable?: boolean
}

export interface ModelService {
  sync(providerId: string): Promise<ModelSyncResult>
  list(query: ModelQuery): ModelInfo[]
  addManual(providerId: string, modelId: string, displayName?: string): void
  removeManual(providerId: string, modelId: string): void
}

interface ModelRow {
  provider_id: string
  model_id: string
  display_name: string
  capabilities: string
  source: 'listed' | 'manual'
  available: number
  first_seen_at: string
  last_seen_at: string
}

export class SqliteModelService implements ModelService {
  private readonly db: Db
  private readonly clock: Clock
  private readonly secrets: SecretStore
  private readonly lister: ModelLister | undefined
  private readonly listTimeoutMs: number

  constructor(db: Db, options: ModelServiceOptions) {
    this.db = db
    this.clock = options.clock ?? new SystemClock()
    this.secrets = options.secrets
    this.lister = options.lister
    this.listTimeoutMs = options.listTimeoutMs ?? 30_000
  }

  async sync(providerId: string): Promise<ModelSyncResult> {
    if (this.lister === undefined) {
      throw appError(AppErrorCode.InvalidArgument, 'model listing is not configured', {
        reason: 'lister_unavailable'
      })
    }
    const provider = this.providerRow(providerId)
    let apiKey: string | null = null
    if (provider.apiKeyRef !== '') {
      try {
        apiKey = await this.secrets.get(provider.apiKeyRef)
      } catch (cause) {
        this.recordFetchError(providerId, 'secret_unavailable', cause)
        throw asAiError(cause, 'secret_unavailable')
      }
      if (apiKey === null) {
        this.recordFetchError(providerId, 'secret_unavailable', 'provider key is missing')
        throw appError(AppErrorCode.SecretUnavailable, 'provider key is missing')
      }
    }

    let upstream: Array<{ modelId: string; displayName: string; capabilities: string[] }>
    try {
      upstream = await this.lister.listModels(provider, apiKey, AbortSignal.timeout(this.listTimeoutMs))
    } catch (cause) {
      const error = asAiError(cause)
      const kind = isAiError(cause) ? cause.kind : 'network'
      this.recordFetchError(providerId, kind, error.message)
      throw error
    }

    const now = formatTimestamp(this.clock.now())
    let added = 0
    let markedUnavailable = 0
    let total = 0
    const seen = new Set<string>()
    this.db.transaction(() => {
      const existing = new Map(
        (
          this.db
            .prepare(`SELECT model_id, source, available FROM provider_models WHERE provider_id = ?`)
            .all(providerId) as Array<{ model_id: string; source: 'listed' | 'manual'; available: number }>
        ).map((row) => [row.model_id, row])
      )
      for (const model of upstream) {
        if (model.modelId === '' || seen.has(model.modelId)) continue
        seen.add(model.modelId)
        total += 1
        const capabilities = resolveModelCapabilities(
          provider.protocol as AiProtocol,
          model.capabilities as AiCapability[]
        )
        const previous = existing.get(model.modelId)
        if (previous === undefined) {
          this.db
            .prepare(
              `INSERT INTO provider_models
                 (provider_id, model_id, display_name, capabilities, source, available, first_seen_at, last_seen_at)
               VALUES (?, ?, ?, ?, 'listed', 1, ?, ?)`
            )
            .run(
              providerId,
              model.modelId,
              model.displayName === '' ? model.modelId : model.displayName,
              JSON.stringify(capabilities),
              now,
              now
            )
          added += 1
        } else if (previous.source === 'manual') {
          this.db
            .prepare(
              `UPDATE provider_models SET available = 1, last_seen_at = ?
               WHERE provider_id = ? AND model_id = ?`
            )
            .run(now, providerId, model.modelId)
        } else {
          this.db
            .prepare(
              `UPDATE provider_models SET display_name = ?, capabilities = ?, available = 1, last_seen_at = ?
               WHERE provider_id = ? AND model_id = ?`
            )
            .run(
              model.displayName === '' ? model.modelId : model.displayName,
              JSON.stringify(capabilities),
              now,
              providerId,
              model.modelId
            )
        }
      }
      for (const [modelId, previous] of existing) {
        if (previous.source === 'listed' && previous.available === 1 && !seen.has(modelId)) {
          this.db
            .prepare(
              `UPDATE provider_models SET available = 0 WHERE provider_id = ? AND model_id = ?`
            )
            .run(providerId, modelId)
          markedUnavailable += 1
        }
      }
      this.db
        .prepare(`UPDATE providers SET models_fetched_at = ?, models_fetch_error = NULL WHERE id = ?`)
        .run(now, providerId)
    }).immediate()

    return {
      providerId,
      added,
      markedUnavailable,
      total,
      fetchedAt: now,
      warning: total === 0 ? 'upstream returned an empty model list' : null
    }
  }

  list(query: ModelQuery): ModelInfo[] {
    const protocol = this.providerRow(query.providerId).protocol as AiProtocol
    const rows = this.db
      .prepare(
        `SELECT provider_id, model_id, display_name, capabilities, source, available,
                first_seen_at, last_seen_at
         FROM provider_models WHERE provider_id = ? ORDER BY model_id`
      )
      .all(query.providerId) as ModelRow[]
    const items: ModelInfo[] = []
    for (const row of rows) {
      const capabilities = JSON.parse(row.capabilities) as AiCapability[]
      const available = row.available === 1
      if (!query.includeUnavailable && !available) continue
      if (
        query.capability !== undefined &&
        (!capabilities.includes(query.capability) || !protocolSupports(protocol, query.capability))
      ) {
        continue
      }
      items.push({
        providerId: row.provider_id,
        modelId: row.model_id,
        displayName: row.display_name,
        capabilities,
        source: row.source === 'manual' ? 'manual' : 'upstream',
        available,
        lastSeenAt: row.last_seen_at
      })
    }
    return items
  }

  addManual(providerId: string, modelId: string, displayName?: string): void {
    const id = modelId.trim()
    if (id === '') {
      throw appError(AppErrorCode.InvalidArgument, 'model id is required', { field: 'modelId' })
    }
    const provider = this.providerRow(providerId)
    const existing = this.db
      .prepare(`SELECT 1 AS present FROM provider_models WHERE provider_id = ? AND model_id = ?`)
      .get(providerId, id)
    if (existing !== undefined) {
      throw appError(AppErrorCode.Conflict, `model already cached: ${id}`, {
        reason: 'model_exists',
        modelId: id
      })
    }
    const now = formatTimestamp(this.clock.now())
    this.db
      .prepare(
        `INSERT INTO provider_models
           (provider_id, model_id, display_name, capabilities, source, available, first_seen_at, last_seen_at)
         VALUES (?, ?, ?, ?, 'manual', 1, ?, ?)`
      )
      .run(
        providerId,
        id,
        displayName === undefined || displayName.trim() === '' ? id : displayName.trim(),
        JSON.stringify(protocolCapabilities(provider.protocol as AiProtocol)),
        now,
        now
      )
  }

  removeManual(providerId: string, modelId: string): void {
    const row = this.db
      .prepare(`SELECT source FROM provider_models WHERE provider_id = ? AND model_id = ?`)
      .get(providerId, modelId) as { source: string } | undefined
    if (row === undefined) {
      throw appError(AppErrorCode.NotFound, `model not found in cache: ${modelId}`, { modelId })
    }
    if (row.source !== 'manual') {
      throw appError(AppErrorCode.InvalidArgument, 'only manual models can be removed', {
        reason: 'not_manual',
        modelId
      })
    }
    const roles = this.db
      .prepare(`SELECT role FROM role_assignments WHERE provider_id = ? AND model_id = ? ORDER BY role`)
      .all(providerId, modelId) as Array<{ role: string }>
    if (roles.length > 0) {
      throw appError(AppErrorCode.Conflict, 'model is in use by roles', {
        reason: 'model_in_use',
        roles: roles.map((entry) => entry.role)
      })
    }
    this.db
      .prepare(`DELETE FROM provider_models WHERE provider_id = ? AND model_id = ?`)
      .run(providerId, modelId)
  }

  private providerRow(providerId: string): ProviderConfig {
    const row = this.db
      .prepare(`SELECT id, name, protocol, base_url, api_key_ref FROM providers WHERE id = ?`)
      .get(providerId) as
      | { id: string; name: string; protocol: string; base_url: string; api_key_ref: string }
      | undefined
    if (row === undefined) {
      throw appError(AppErrorCode.NotFound, `provider not found: ${providerId}`, { providerId })
    }
    return {
      id: row.id,
      name: row.name,
      protocol: row.protocol as AiProtocol,
      baseUrl: row.base_url,
      apiKeyRef: row.api_key_ref
    }
  }

  private recordFetchError(providerId: string, kind: string, cause: unknown): void {
    const message = cause instanceof Error ? cause.message : String(cause)
    this.db
      .prepare(`UPDATE providers SET models_fetch_error = ? WHERE id = ?`)
      .run(sanitize(`${kind}|${message}`), providerId)
  }
}