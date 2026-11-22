/**
 * Project field limits.
 *
 * The counters and the disabled submit of the create/edit dialogs use the
 * same rune counts and the same bounds the service enforces, so a long field
 * is refused locally with a visible counter instead of crossing IPC only to
 * fail.
 */

export const PROJECT_LIMITS = {
  name: 200,
  summary: 5000,
  goals: 100_000,
  focus: 20_000
} as const

export type ProjectFieldName = keyof typeof PROJECT_LIMITS

/** Rune count, matching Go's utf8.RuneCountInString (not UTF-16 units). */
export function runeCount(value: string): number {
  let count = 0
  for (const _ of value) count += 1
  return count
}

export type FieldError = 'required' | 'too_long'

export function fieldError(value: string, max: number, required = false): FieldError | null {
  if (required && value.trim() === '') return 'required'
  if (runeCount(value) > max) return 'too_long'
  return null
}

export interface ProjectFormValues {
  readonly name: string
  readonly summary: string
  readonly goals: string
  readonly focus: string
}

export type ProjectFormErrors = { readonly [K in ProjectFieldName]?: FieldError }

export function validateProjectForm(values: ProjectFormValues): ProjectFormErrors {
  const errors: { [K in ProjectFieldName]?: FieldError } = {}
  const name = fieldError(values.name, PROJECT_LIMITS.name, true)
  if (name !== null) errors.name = name
  const summary = fieldError(values.summary, PROJECT_LIMITS.summary)
  if (summary !== null) errors.summary = summary
  const goals = fieldError(values.goals, PROJECT_LIMITS.goals)
  if (goals !== null) errors.goals = goals
  const focus = fieldError(values.focus, PROJECT_LIMITS.focus)
  if (focus !== null) errors.focus = focus
  return errors
}

export function hasErrors(errors: ProjectFormErrors): boolean {
  return Object.values(errors).some((error) => error !== undefined)
}