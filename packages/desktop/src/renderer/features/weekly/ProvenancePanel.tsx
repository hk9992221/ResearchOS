import type { WeeklyReportView, WeeklyTemplate } from '@researchos/shared'

import { Badge, Card, Mono, Row, Rows } from '../../design/components'
import { useI18n, useT } from '../../i18n'
import { formatInstant } from './weekly-format'
import { splitReport } from './weekly-sections'
import styles from './weekly.module.css'

export interface ProvenancePanelProps {
  readonly report: WeeklyReportView
  readonly template: WeeklyTemplate | undefined
}

/**
 * ProvenancePanel states where the body came from. Everything shown is stored
 * in `report.meta`: the generation mode and provider/model, the skill that
 * shaped the writing, the template version, the digest the facts came from and
 * the content hash the editor is based on. Per-section rows name the section
 * and the run that wrote it — one run per generation, so a section that was
 * edited by hand is shown as such instead of pretending the model wrote it.
 */
export function ProvenancePanel({ report, template }: ProvenancePanelProps): JSX.Element {
  const t = useT()
  const { language } = useI18n()
  const generation = report.meta.generation
  const coverage = report.meta.coverage
  const source = generation?.mode === 'ai' ? 'AI' : t('pages.weekly_review.badge_human')
  const provider = generation?.provider_id ?? '—'
  const model = generation?.model_id ?? '—'
  const sections = template === undefined ? [] : splitReport(report.contentMd, template)

  return (
    <Card title={t('pages.weekly_review.provenance_title')}>
      <div className={styles.dialogGrid}>
        <Rows>
          <Row
            label={t('pages.weekly_review.prov_status')}
            value={
              <Badge tone={report.status === 'confirmed' ? 'success' : 'warning'}>
                {t(
                  report.status === 'confirmed'
                    ? 'pages.weekly_review.status_confirmed'
                    : 'pages.weekly_review.status_draft'
                )}
              </Badge>
            }
          />
          <Row label={t('pages.weekly_review.prov_mode')} value={generation?.mode ?? '—'} />
          <Row label={t('pages.weekly_review.prov_provider')} value={provider} />
          <Row label={t('pages.weekly_review.prov_model')} value={model} />
          <Row label={t('pages.weekly_review.prov_skill')} value={generation?.skill_name ?? '—'} />
          <Row label={t('pages.weekly_review.prov_prompt')} value={generation?.prompt_version ?? '—'} />
          <Row
            label={t('pages.weekly_review.prov_template')}
            value={
              generation === undefined
                ? '—'
                : `${generation.template_id} v${generation.template_version}`
            }
          />
          <Row label={t('pages.weekly_review.prov_ai_status')} value={generation?.ai_status ?? '—'} />
          <Row label={t('pages.weekly_review.prov_attempts')} value={String(generation?.attempts ?? 0)} />
          <Row label={t('pages.weekly_review.prov_events')} value={String(coverage?.event_count ?? 0)} />
          <Row label={t('pages.weekly_review.prov_timezone')} value={coverage?.timezone ?? '—'} />
          <Row
            label={t('pages.weekly_review.prov_digest_hash')}
            value={<Mono>{coverage?.digest_hash ?? '—'}</Mono>}
          />
          <Row
            label={t('pages.weekly_review.prov_content_hash')}
            value={<Mono>{report.contentHash}</Mono>}
          />
          <Row
            label={t('pages.weekly_review.prov_updated')}
            value={formatInstant(report.updatedAt, language, coverage?.timezone)}
          />
        </Rows>
        {generation?.error !== undefined && (
          <p className={styles.errorMessage}>{generation.error}</p>
        )}
        <ul className={styles.list}>
          {sections.map((section) => (
            <li key={section.key}>
              {section.num}. {section.title} —{' '}
              {generation?.mode === 'ai'
                ? t('pages.weekly_review.section_ai_source', { provider, model })
                : t('pages.weekly_review.section_manual_source')}
            </li>
          ))}
        </ul>
      </div>
    </Card>
  )
}
