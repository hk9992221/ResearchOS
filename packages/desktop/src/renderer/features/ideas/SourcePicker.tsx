import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import type { EvidenceSourceType, ProjectResources, RunSummary } from '@researchos/shared'

import { SelectField } from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { ideasApi } from './api'
import { SOURCE_KEY } from './model'

export const SOURCE_TYPES: readonly EvidenceSourceType[] = ['paper', 'note', 'experiment', 'run']

export interface SourceRefValue {
  readonly type: EvidenceSourceType
  readonly id: string
}

export interface SourcePickerProps {
  readonly projectId: string
  readonly value: SourceRefValue | null
  readonly onChange: (value: SourceRefValue | null) => void
  readonly types?: readonly EvidenceSourceType[]
  readonly label?: string
}

interface Candidate {
  readonly id: string
  readonly title: string
  readonly detail: string
}

/**
 * Source picker for evidence and extraction: paper/note/experiment candidates
 * come from project resources; a run is reached through its experiment. The
 * chosen value is the polymorphic `{type, id}` the domain stores.
 */
export function SourcePicker({
  projectId,
  value,
  onChange,
  types = SOURCE_TYPES,
  label
}: SourcePickerProps): JSX.Element {
  const t = useT()
  const [runExperiment, setRunExperiment] = useState('')

  const resources = useQuery({
    queryKey: ['project', projectId, 'resources'],
    queryFn: () => ipc.invoke('project.resources', { id: projectId })
  })

  const runs = useQuery({
    queryKey: ['experiments', projectId, 'runs', runExperiment],
    queryFn: () => ideasApi.runs(projectId, runExperiment),
    enabled: runExperiment !== ''
  })

  const type = value?.type ?? types[0] ?? 'experiment'

  const candidates: readonly Candidate[] = (() => {
    const data: ProjectResources | undefined = resources.data
    if (data === undefined) return []
    if (type === 'paper') {
      return data.papers.map((paper) => ({
        id: paper.id,
        title: paper.title,
        detail: [paper.authors, paper.year === 0 ? '' : String(paper.year)].filter(Boolean).join(' · ')
      }))
    }
    if (type === 'note') {
      return data.notes.map((note) => ({ id: note.id, title: note.title, detail: note.kind }))
    }
    if (type === 'experiment') {
      return data.experiments.map((experiment) => ({
        id: experiment.id,
        title: experiment.title,
        detail: experiment.lastRunStatus
      }))
    }
    return []
  })()

  const runsForExperiment: readonly Candidate[] =
    runs.data === undefined
      ? []
      : (runs.data as RunSummary[]).map((run) => ({
          id: run.id,
          title: run.displayName.trim() === '' ? run.title : run.displayName,
          detail: run.status
        }))

  const pickType = (next: EvidenceSourceType): void => {
    onChange(null)
    if (next !== 'run') setRunExperiment('')
  }

  return (
    <div>
      <SelectField
        label={label ?? t('pages.ideas.field_source')}
        value={type}
        options={types.map((item) => ({ value: item, label: t(SOURCE_KEY[item]) }))}
        onChange={(next) => pickType(next as EvidenceSourceType)}
      />
      {type === 'run' ? (
        <>
          <SelectField
            label={t('pages.ideas.source_experiment')}
            value={runExperiment}
            options={[
              { value: '', label: t('pages.ideas.link_empty') },
              ...candidatesForExperiments(resources.data)
            ]}
            onChange={(next) => {
              setRunExperiment(next)
              onChange(null)
            }}
          />
          <SelectField
            label={t('pages.ideas.source_pick')}
            value={value?.type === 'run' ? value.id : ''}
            options={[
              {
                value: '',
                label:
                  runExperiment === ''
                    ? t('pages.ideas.link_empty')
                    : runs.isPending
                      ? t('pages.ideas.task_candidates')
                      : t('pages.ideas.link_empty')
              },
              ...runsForExperiment.map((item) => ({
                value: item.id,
                label: item.detail === '' ? item.title : `${item.title} — ${item.detail}`
              }))
            ]}
            onChange={(next) => onChange(next === '' ? null : { type: 'run', id: next })}
          />
        </>
      ) : (
        <SelectField
          label={t('pages.ideas.source_pick')}
          value={value?.type === type ? value.id : ''}
          options={[
            {
              value: '',
              label: resources.isPending
                ? t('pages.ideas.task_candidates')
                : t('pages.ideas.link_empty')
            },
            ...candidates.map((item) => ({
              value: item.id,
              label: item.detail === '' ? item.title : `${item.title} — ${item.detail}`
            }))
          ]}
          onChange={(next) => onChange(next === '' ? null : { type, id: next })}
        />
      )}
    </div>
  )
}

function candidatesForExperiments(data: ProjectResources | undefined): Array<{
  value: string
  label: string
}> {
  if (data === undefined) return []
  return data.experiments.map((experiment) => ({
    value: experiment.id,
    label: experiment.title
  }))
}