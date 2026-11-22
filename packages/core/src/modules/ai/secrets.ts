import type { SecretStatus } from '@researchos/shared'

import { AiError } from './errors'

/** OS secret-store contract. There is deliberately no list. */
export interface SecretStore {
  set(ref: string, secret: string): Promise<void>
  get(ref: string): Promise<string | null>
  delete(ref: string): Promise<void>
}

export class SecretStoreUnavailableError extends Error {
  constructor(cause?: unknown) {
    super('os secret store unavailable', cause === undefined ? undefined : { cause })
    this.name = 'SecretStoreUnavailableError'
  }
}

/** The fixed, opaque reference for one provider key. */
export function providerKeyRef(providerId: string): string {
  return `researchos/provider/${providerId}/api-key`
}

interface KeyringEntry {
  setPassword(password: string): void
  getPassword(): string | null
  deletePassword(): boolean
}

type KeyringCtor = new (service: string, username: string) => KeyringEntry

let keyringCtor: Promise<KeyringCtor | null> | null = null

async function loadKeyring(): Promise<KeyringCtor | null> {
  keyringCtor ??= import('@napi-rs/keyring')
    .then((module) => module.Entry as unknown as KeyringCtor)
    .catch(() => null)
  return keyringCtor
}

/**
 * `@napi-rs/keyring` backed store. The target name is the reference itself and
 * the user is `researchos`, matching the Go Windows Credential Manager target
 * so an existing workspace keeps its keys.
 */
export class KeyringSecretStore implements SecretStore {
  private readonly service: string

  constructor(service = 'researchos') {
    this.service = service
  }

  private async entry(ref: string): Promise<KeyringEntry> {
    const Entry = await loadKeyring()
    if (Entry === null) throw new SecretStoreUnavailableError()
    try {
      return new Entry(ref, this.service)
    } catch (cause) {
      throw new SecretStoreUnavailableError(cause)
    }
  }

  async set(ref: string, secret: string): Promise<void> {
    try {
      ;(await this.entry(ref)).setPassword(secret)
    } catch (cause) {
      throw new SecretStoreUnavailableError(cause)
    }
  }

  async get(ref: string): Promise<string | null> {
    try {
      const value = (await this.entry(ref)).getPassword()
      return value === '' || value === null ? null : value
    } catch (cause) {
      throw new SecretStoreUnavailableError(cause)
    }
  }

  async delete(ref: string): Promise<void> {
    try {
      ;(await this.entry(ref)).deletePassword()
    } catch (cause) {
      throw new SecretStoreUnavailableError(cause)
    }
  }
}

/** Test-only store; release wiring must use KeyringSecretStore. */
export class MemorySecretStore implements SecretStore {
  private readonly entries = new Map<string, string>()
  unavailable = false

  async set(ref: string, secret: string): Promise<void> {
    if (this.unavailable) throw new SecretStoreUnavailableError()
    this.entries.set(ref, secret)
  }

  async get(ref: string): Promise<string | null> {
    if (this.unavailable) throw new SecretStoreUnavailableError()
    return this.entries.get(ref) ?? null
  }

  async delete(ref: string): Promise<void> {
    if (this.unavailable) throw new SecretStoreUnavailableError()
    this.entries.delete(ref)
  }

  has(ref: string): boolean {
    return this.entries.has(ref)
  }

  size(): number {
    return this.entries.size
  }
}

export interface SecretService {
  set(providerId: string, apiKey: string): Promise<SecretStatus>
  delete(providerId: string): Promise<SecretStatus>
  status(providerId: string, apiKeyRef?: string): Promise<SecretStatus>
  /** Throws `secret_unavailable` when the ref exists but cannot be read. */
  read(apiKeyRef: string): Promise<string | null>
}

export class SecretServiceImpl implements SecretService {
  private readonly store: SecretStore

  constructor(store: SecretStore) {
    this.store = store
  }

  async set(providerId: string, apiKey: string): Promise<SecretStatus> {
    if (apiKey.trim() === '') {
      throw new AiError('invalid_request', 'API key must not be empty', {
        details: { field: 'apiKey' }
      })
    }
    const ref = providerKeyRef(providerId)
    try {
      await this.store.set(ref, apiKey)
    } catch (cause) {
      throw new AiError('secret_unavailable', sanitizeSecretError(cause), { cause })
    }
    return { ref, available: true, store: 'keyring' }
  }

  async delete(providerId: string): Promise<SecretStatus> {
    const ref = providerKeyRef(providerId)
    try {
      await this.store.delete(ref)
    } catch (cause) {
      throw new AiError('secret_unavailable', sanitizeSecretError(cause), { cause })
    }
    return { ref, available: false, store: 'keyring' }
  }

  async status(providerId: string, apiKeyRef?: string): Promise<SecretStatus> {
    const ref = apiKeyRef === undefined || apiKeyRef === '' ? providerKeyRef(providerId) : apiKeyRef
    try {
      return { ref, available: (await this.store.get(ref)) !== null, store: 'keyring' }
    } catch {
      return { ref, available: false, store: 'unavailable' }
    }
  }

  async read(apiKeyRef: string): Promise<string | null> {
    if (apiKeyRef === '') return null
    try {
      return await this.store.get(apiKeyRef)
    } catch (cause) {
      throw new AiError('secret_unavailable', sanitizeSecretError(cause), { cause })
    }
  }
}

function sanitizeSecretError(cause: unknown): string {
  const message = cause instanceof Error ? cause.message : String(cause)
  return message.includes('unavailable') ? message : `secret store failure: ${message}`
}