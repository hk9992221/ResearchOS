import Ajv from 'ajv'

import { AiError } from './errors'

const ajv = new Ajv({ allErrors: true, strict: false, allowUnionTypes: true, validateFormats: false })

/** finish_reason values that mean the model ran out of output budget. */
export function isTruncatedFinish(reason: string | null): boolean {
  if (reason === null) return false
  const normalized = reason.toLowerCase().replace(/-/g, '_')
  return normalized === 'length' || normalized === 'max_tokens' || normalized === 'max_output_tokens'
}

/**
 * Finds the first complete JSON object in a model reply, tolerating code
 * fences and surrounding prose (port of the Go behavior).
 */
export function extractJsonObject(content: string): { value: unknown; json: string } {
  let text = content.trim()
  if (text.startsWith('```')) {
    const firstNewline = text.indexOf('\n')
    if (firstNewline >= 0) text = text.slice(firstNewline + 1)
    const closing = text.lastIndexOf('```')
    if (closing >= 0) text = text.slice(0, closing)
    text = text.trim()
  }
  const start = text.indexOf('{')
  if (start < 0) {
    throw new AiError('output_invalid', 'model reply contained no JSON object')
  }
  let depth = 0
  let inString = false
  let escaped = false
  for (let index = start; index < text.length; index += 1) {
    const char = text[index]
    if (escaped) {
      escaped = false
    } else if (char === '\\' && inString) {
      escaped = true
    } else if (char === '"') {
      inString = !inString
    } else if (!inString && char === '{') {
      depth += 1
    } else if (!inString && char === '}') {
      depth -= 1
      if (depth === 0) {
        const json = text.slice(start, index + 1)
        try {
          return { value: JSON.parse(json) as unknown, json }
        } catch {
          throw new AiError('output_invalid', 'model reply contains malformed JSON')
        }
      }
    }
  }
  throw new AiError('output_invalid', 'model reply contains an unbalanced JSON object')
}

/** Validates a parsed value against the caller's JSON Schema. */
export function validateJsonSchema(schema: Record<string, unknown>, value: unknown): void {
  let validate
  try {
    validate = ajv.compile(schema)
  } catch (cause) {
    throw new AiError('invalid_request', 'the supplied JSON schema is invalid', { cause })
  }
  if (validate(value)) return
  const detail = (validate.errors ?? [])
    .slice(0, 5)
    .map((error) => `${error.instancePath === '' ? '/' : error.instancePath} ${error.message ?? 'invalid'}`)
    .join('; ')
  throw new AiError('output_invalid', `model JSON does not match the schema: ${detail}`, {
    details: { errors: validate.errors?.slice(0, 5) }
  })
}

/** System prompt used when the protocol has no native structured output. */
export function jsonConstraintPrompt(name: string | undefined, schema: Record<string, unknown>): string {
  const label = name === undefined || name === '' ? 'response' : name
  return (
    `Respond with a single valid JSON object for schema "${label}" matching this JSON Schema, ` +
    `and nothing else (no prose, no code fences):\n${JSON.stringify(schema)}`
  )
}

export function repairPrompt(detail: string): string {
  return (
    `Your previous reply failed validation: ${detail}. ` +
    'Reply again with ONLY the corrected JSON object; no prose, no code fences.'
  )
}