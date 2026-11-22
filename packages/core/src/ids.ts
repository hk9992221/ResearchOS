import { randomFillSync } from 'node:crypto'

import { type Clock, SystemClock } from './clock'

/**
 * The only UUIDv7 generator in the application.
 *
 * Layout: 48-bit big-endian Unix millisecond timestamp, version nibble 7,
 * RFC 4122 variant `10xx`, 74 random bits. The output is a lowercase
 * 36-character UUID, comparable in timestamp order by its first 13 characters.
 */
export function newId(clock: Clock = new SystemClock()): string {
  const bytes = new Uint8Array(16)
  randomFillSync(bytes)

  const milliseconds = BigInt(Math.trunc(clock.now().getTime()))
  if (milliseconds < 0n || milliseconds > 0xffffffffffffn) {
    throw new RangeError('newId: timestamp does not fit in 48 bits')
  }
  for (let index = 0; index < 6; index += 1) {
    bytes[index] = Number((milliseconds >> BigInt(8 * (5 - index))) & 0xffn)
  }
  bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x70 // version 7
  bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80 // RFC 4122 variant 10xx

  const hex = Buffer.from(bytes).toString('hex')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

const ID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

/** True only for a 36-character lowercase UUIDv7 (version 7, variant 10xx). */
export function isValidId(value: unknown): value is string {
  return typeof value === 'string' && ID_PATTERN.test(value)
}

export function assertValidId(value: unknown, label = 'id'): string {
  if (!isValidId(value)) throw new TypeError(`${label} is not a lowercase UUIDv7: ${String(value)}`)
  return value
}

/** The 13 leading characters: the 48-bit timestamp with its separator. */
export function timestampPrefix(id: string): string {
  return id.length < 13 ? id : id.slice(0, 13).toLowerCase()
}

/** Extracts the embedded timestamp. Throws for anything that is not a UUIDv7. */
export function idTimestamp(id: string): Date {
  assertValidId(id, 'id')
  const hex = id.replaceAll('-', '').slice(0, 12)
  return new Date(Number(BigInt(`0x${hex}`)))
}