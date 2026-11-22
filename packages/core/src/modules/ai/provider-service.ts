import type {
  AiProtocol,
  CreateProviderInput,
  ProviderTestResult,
  ProviderView,
  UpdateProviderInput
} from '@researchos/shared'
import { AppErrorCode } from '@researchos/shared'

import { formatTimestamp, SystemClock, type Clock } from '../../clock'
import type { Db } from '../../db/open'
import { appError } from '../../errors'
import { newId } from '../../ids'
import {
  assertEnablePolicy,
  assertProtocol,
  isLoopbackUrl,
  normalizeBaseUrl,
  protocolCapabilities,
  protocolSupports
} from './capabilities'
import { AiError, asAiError, sanitize } from './errors'
import type { ProviderConfig } from './protocol/types'
import { providerKeyRef, type SecretStore } from './secrets'

export interface ModelLister {
  listModels(
    config: ProviderConfig,
    apiKey: string | null,
    signal?: AbortSignal
  ): Promise<Array<{ modelId: string; displayName: string; capabilities: string[] }>>
}

export interface ProviderServiceOptions {
  clock?: Clock
  secrets: SecretStore
  lister?: ModelLister
  /** Bound for the low-cost model-list test; default 30 s. */
  testTimeoutMs?: number
}

export interface ProviderService {
  list(): ProviderView[]
  get(providerId: string): ProviderView
  create(input: CreateProviderInput): Promise<ProviderView>
  update(providerId: string, patch: UpdateProviderInput): Promise<ProviderView>
  delete(providerId: string): Promise<void>
  setEnabled(providerId: string, enabled: boolean): Promise<void>
  test(providerId: string): Promise<ProviderTestResult>
  /** Internal: provider row with the secret reference for gateway resolution. */
  config(providerId: string): ProviderConfig
}

export interface ProviderRow {
  id: string
  name: string
  protocol: AiProtocol
  base_url: string
  api_key_ref: string
  enabled: number
  created_at: string
  updated_at: string
  last_tested_at: string | null
  last_test_ok: number | null
  last_test_latency_ms: number | null
  last_test_model_count: number | null
  last_test_error: string | null
  models_fetched_at: string | null
  models_fetch_error: string | null
}

const PROVIDER_COLUMNS = `id, name, protocol, base_url, api_key_ref, enabled,
  created_at, updated_at, last_tested_at, last_test_ok, last_test_latency_ms,
  last_test_model_count, last_test_error, models_fetched_at, models_fetch_error`

export class SqliteProviderService implements ProviderService {
  private readonly db: Db
  private readonly clock: Clock
  private readonly secrets: SecretStore
  private readonly lister: ModelLister | undefined
  private readonly testTimeoutMs: number

  constructor(db: Db, options: ProviderServiceOptions) {
    this.db = db
    this.clock = options.clock ?? new SystemClock()
    this.secrets = options.secrets
    this.lister = options.lister
    this.testTimeoutMs = options.testTimeoutMs ?? 30_000
  }

  list(): ProviderView[] {
    const rows = this.db
      .prepare(`SELECT ${PROVIDER_COLUMNS} FROM providers ORDER BY created_at, id`)
      .all() as ProviderRow[]
    return rows.map(viewOf)
  }

  get(providerId: string): ProviderView {
    return viewOf(this.row(providerId))
  }

  config(providerId: string): ProviderConfig {
    const row = this.row(providerId)
    return {
      id: row.id,
      name: row.name,
      protocol: row.protocol,
      baseUrl: row.base_url,
      apiKeyRef: row.api_key_ref
    }
  }

  async create(input: CreateProviderInput): Promise<ProviderView> {
    const name = input.name.trim()
    if (name === '') {
      throw appError(AppErrorCode.InvalidArgument, 'provider name is required', { field: 'name' })
    }
    const protocol = assertProtocol(input.protocol)
    const baseUrl = normalizeBaseUrl(input.baseUrl)
    const enabled = input.enabled ?? false
    const apiKey = input.apiKey ?? ''
    if (enabled) assertEnablePolicy(baseUrl, apiKey !== '')
    const id = newId(this.clock)
    const ref = apiKey === '' ? '' : providerKeyRef(id)
    if (ref !== '') {
      try {
        await this.secrets.set(ref, apiKey)
      } catch (cause) {
        throw new AiError('secret_unavailable', 'failed to write the provider key', { cause })
      }
    }
    const now = formatTimestamp(this.clock.now())
    try {
      this.db
        .prepare(
          `INSERT INTO providers (id, name, protocol, base_url, api_key_ref, enabled, created_at, updated_at)
           VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
        )
        .run(id, name, protocol, baseUrl, ref, enabled ? 1 : 0, now, now)
    } catch (cause) {
      if (ref !== '') await this.secrets.delete(ref).catch(() => undefined)
      throw appError(AppErrorCode.Internal, 'failed to create the provider', { cause })
    }
    return this.get(id)
  }

  async update(providerId: string, patch: UpdateProviderInput): Promise<ProviderView> {
    const row = this.row(providerId)
    let name = row.name
    if (patch.name !== undefined) {
      if (typeof patch.name !== 'string' || patch.name.trim() === '') {
        throw appError(AppErrorCode.InvalidArgument, 'provider name is required', { field: 'name' })
      }
      name = patch.name.trim()
    }

    let protocol = row.protocol
    let protocolChanged = false
    if (patch.protocol !== undefined) {
      protocol = assertProtocol(patch.protocol)
      if (protocol !== row.protocol) {
        this.assertProtocolChangeAllowed(providerId, protocol)
        protocolChanged = true
      }
    }

    let baseUrl = row.base_url
    let baseChanged = false
    if (patch.baseUrl !== undefined) {
      if (typeof patch.baseUrl !== 'string') {
        throw appError(AppErrorCode.InvalidArgument, 'base URL must be a string', { field: 'baseUrl' })
      }
      const normalized = normalizeBaseUrl(patch.baseUrl)
      if (normalized !== row.base_url) {
        baseUrl = normalized
        baseChanged = true
      }
    }

    const requestedKey = patch.apiKey
    const clearKey = patch.clearKey === true
    if (requestedKey !== undefined && clearKey) {
      throw appError(
        AppErrorCode.InvalidArgument,
        'apiKey and clearKey are mutually exclusive',
        { field: 'apiKey' }
      )
    }

    const enabled = patch.enabled === undefined ? row.enabled === 1 : patch.enabled === true
    const willHaveKey =
      typeof requestedKey === 'string' ? requestedKey !== '' : row.api_key_ref !== '' && !clearKey
    // Reject before mutating the OS key store: an enabled remote provider must
    // Keep a key.
    if (enabled && !willHaveKey && !isLoopbackUrl(baseUrl)) {
      assertEnablePolicy(baseUrl, false)
    }

    let ref = row.api_key_ref
    if (clearKey && ref !== '') {
      await this.deleteSecret(ref)
      ref = ''
    }
    if (typeof requestedKey === 'string') {
      if (requestedKey === '') {
        if (ref !== '') await this.deleteSecret(ref)
        ref = ''
      } else {
        if (ref === '') ref = providerKeyRef(providerId)
        try {
          await this.secrets.set(ref, requestedKey)
        } catch (cause) {
          throw new AiError('secret_unavailable', 'failed to write the provider key', { cause })
        }
      }
    }

    if (enabled && willHaveKey) {
      await this.assertEnableReadable(ref, baseUrl)
    }

    const now = formatTimestamp(this.clock.now())
    const resetCaches = protocolChanged || baseChanged
    this.db.transaction(() => {
      if (resetCaches) {
        const capabilities = JSON.stringify(protocolCapabilities(protocol))
        this.db
          .prepare(`DELETE FROM provider_models WHERE provider_id = ? AND source = 'listed'`)
          .run(providerId)
        this.db
          .prepare(
            `UPDATE provider_models SET capabilities = ? WHERE provider_id = ? AND source = 'manual'`
          )
          .run(capabilities, providerId)
      }
      this.db
        .prepare(
          resetCaches
            ? `UPDATE providers SET name = ?, protocol = ?, base_url = ?, api_key_ref = ?, enabled = ?,
                 updated_at = ?, last_tested_at = NULL, last_test_ok = NULL, last_test_latency_ms = NULL,
                 last_test_model_count = NULL, last_test_error = NULL,
                 models_fetched_at = NULL, models_fetch_error = NULL
               WHERE id = ?`
            : `UPDATE providers SET name = ?, protocol = ?, base_url = ?, api_key_ref = ?, enabled = ?,
                 updated_at = ? WHERE id = ?`
        )
        .run(name, protocol, baseUrl, ref, enabled ? 1 : 0, now, providerId)
    }).immediate()
    return this.get(providerId)
  }

  async delete(providerId: string): Promise<void> {
    const row = this.row(providerId)
    const roles = this.db
      .prepare(`SELECT role FROM role_assignments WHERE provider_id = ? ORDER BY role`)
      .all(providerId) as Array<{ role: string }>
    if (roles.length > 0) {
      throw appError(
        AppErrorCode.Conflict,
        'provider is assigned to roles; reassign or clear them first',
        { reason: 'provider_in_use', roles: roles.map((entry) => entry.role) }
      )
    }
    if (row.api_key_ref !== '') await this.deleteSecret(row.api_key_ref)
    this.db.transaction(() => {
      this.db.prepare(`DELETE FROM provider_models WHERE provider_id = ?`).run(providerId)
      this.db.prepare(`DELETE FROM providers WHERE id = ?`).run(providerId)
    }).immediate()
  }

  async setEnabled(providerId: string, enabled: boolean): Promise<void> {
    const row = this.row(providerId)
    if (enabled && row.enabled === 0) {
      await this.assertEnableReadable(row.api_key_ref, row.base_url)
    }
    this.db
      .prepare(`UPDATE providers SET enabled = ?, updated_at = ? WHERE id = ?`)
      .run(enabled ? 1 : 0, formatTimestamp(this.clock.now()), providerId)
  }

  async test(providerId: string): Promise<ProviderTestResult> {
    const row = this.row(providerId)
    const started = this.clock.now().getTime()
    const finish = (
      ok: boolean,
      errorKind: ProviderTestResult['errorKind'],
      message: string | null,
      modelCount: number
    ): ProviderTestResult => {
      const latencyMs = Math.max(0, this.clock.now().getTime() - started)
      const testedAt = formatTimestamp(this.clock.now())
      this.db
        .prepare(
          `UPDATE providers SET last_tested_at = ?, last_test_ok = ?, last_test_latency_ms = ?,
             last_test_model_count = ?, last_test_error = ? WHERE id = ?`
        )
        .run(testedAt, ok ? 1 : 0, latencyMs, modelCount, message, providerId)
      return { ok, latencyMs, modelCount, errorKind, error: message, testedAt }
    }

    if (this.lister === undefined) {
      return finish(false, 'unsupported_capability', 'model listing is not configured', 0)
    }
    let apiKey: string | null = null
    if (row.api_key_ref !== '') {
      try {
        apiKey = await this.secrets.get(row.api_key_ref)
      } catch (cause) {
        const aiError = asAiError(cause, 'secret_unavailable')
        return finish(false, aiError.kind, aiError.message, 0)
      }
      if (apiKey === null) {
        return finish(false, 'secret_unavailable', 'provider key is missing', 0)
      }
    }
    const signal = AbortSignal.timeout(this.testTimeoutMs)
    try {
      const models = await this.lister.listModels(
        {
          id: row.id,
          name: row.name,
          protocol: row.protocol,
          baseUrl: row.base_url,
          apiKeyRef: row.api_key_ref
        },
        apiKey,
        signal
      )
      return finish(true, null, null, models.length)
    } catch (cause) {
      const error = asAiError(cause)
      return finish(false, error.kind, sanitize(error.message), 0)
    }
  }

  private row(providerId: string): ProviderRow {
    const row = this.db
      .prepare(`SELECT ${PROVIDER_COLUMNS} FROM providers WHERE id = ?`)
      .get(providerId) as ProviderRow | undefined
    if (row === undefined) {
      throw appError(AppErrorCode.NotFound, `provider not found: ${providerId}`, {
        providerId
      })
    }
    return row
  }

  private assertProtocolChangeAllowed(providerId: string, next: AiProtocol): void {
    const roles = this.db
      .prepare(`SELECT role FROM role_assignments WHERE provider_id = ? ORDER BY role`)
      .all(providerId) as Array<{ role: string }>
    const conflicts = roles
      .map((entry) => entry.role)
      .filter((role) => {
        const capability = role === 'chat_main' ? 'chat' : role === 'embedding' ? 'embedding' : 'rerank'
        return !protocolSupports(next, capability)
      })
    if (conflicts.length > 0) {
      throw appError(
        AppErrorCode.Conflict,
        `protocol ${next} lacks the capability required by assigned roles`,
        { reason: 'protocol_role_conflict', roles: conflicts }
      )
    }
  }

  private async assertEnableReadable(apiKeyRef: string, baseUrl: string): Promise<void> {
    if (apiKeyRef === '') {
      if (!isLoopbackUrl(baseUrl)) {
        assertEnablePolicy(baseUrl, false)
      }
      return
    }
    let key: string | null
    try {
      key = await this.secrets.get(apiKeyRef)
    } catch (cause) {
      throw new AiError('secret_unavailable', 'provider key is unreadable; fix the OS key store', {
        cause
      })
    }
    if (key === null) {
      throw new AiError(
        'secret_unavailable',
        'provider key is missing; set it before enabling the provider'
      )
    }
  }

  private async deleteSecret(ref: string): Promise<void> {
    try {
      await this.secrets.delete(ref)
    } catch (cause) {
      throw new AiError('secret_unavailable', 'failed to delete the provider key', { cause })
    }
  }
}

function viewOf(row: ProviderRow): ProviderView {
  return {
    id: row.id,
    name: row.name,
    protocol: row.protocol,
    baseUrl: row.base_url,
    hasKey: row.api_key_ref !== '',
    enabled: row.enabled === 1,
    lastTestOk: row.last_test_ok === null ? null : row.last_test_ok === 1,
    lastTestedAt: row.last_tested_at,
    lastTestLatencyMs: row.last_test_latency_ms,
    lastTestModelCount: row.last_test_model_count,
    lastTestError: row.last_test_error,
    modelsFetchedAt: row.models_fetched_at,
    modelsError: row.models_fetch_error,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}
