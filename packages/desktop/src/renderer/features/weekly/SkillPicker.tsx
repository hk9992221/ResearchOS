import type { WeeklySkillSet } from '@researchos/shared'

import { SelectField } from '../../design/components'
import { useT } from '../../i18n'

export interface SkillPickerProps {
  readonly skills: WeeklySkillSet | undefined
  /** Empty string means the built-in skill. */
  readonly value: string
  readonly onChange: (skillId: string) => void
  readonly disabled?: boolean
  readonly hint?: string
}

/**
 * SkillPicker chooses the writing skill of one generation run. It only lists
 * what `weekly.skills.list` returns; the built-in default is the empty id, so
 * an untouched picker keeps the service default. Editing skills lives in
 * Settings — this page never saves one.
 */
export function SkillPicker({
  skills,
  value,
  onChange,
  disabled = false,
  hint
}: SkillPickerProps): JSX.Element {
  const t = useT()
  const defaultId = skills?.default ?? ''
  const options = [
    {
      value: '',
      label:
        defaultId === ''
          ? `${t('pages.weekly_review.skill_builtin')} ${t('pages.weekly_review.skill_default_suffix')}`
          : t('pages.weekly_review.skill_builtin')
    },
    ...(skills?.skills ?? []).map((skill) => ({
      value: skill.id,
      label:
        skill.id === defaultId
          ? `${skill.name} ${t('pages.weekly_review.skill_default_suffix')}`
          : skill.name
    }))
  ]
  return (
    <SelectField
      label={t('pages.weekly_review.field_skill')}
      value={value}
      options={options}
      onChange={onChange}
      disabled={disabled}
      hint={hint}
    />
  )
}
