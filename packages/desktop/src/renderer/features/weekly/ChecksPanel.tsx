import type { WeeklyValidationReport, WeeklyViolation, WeeklyTemplate } from '@researchos/shared'

import { Badge, Button, EmptyState, Mono, Skeleton, TextInput, Banner } from '../../design/components'
import { useT, type Translations } from '../../i18n'
import { unresolvedCount } from './weekly-sections'
import styles from './weekly.module.css'

export interface ChecksPanelProps {
  readonly template: WeeklyTemplate | undefined
  readonly validation: WeeklyValidationReport | undefined
  readonly loading: boolean
  readonly error: unknown
  /** Fingerprints acknowledged in this session, not yet stored. */
  readonly acks: ReadonlySet<string>
  readonly notes: ReadonlyMap<string, string>
  readonly onToggleAck: (violation: WeeklyViolation) => void
  readonly onNoteChange: (fingerprint: string, note: string) => void
}

function severityLabel(t: Translations, severity: WeeklyViolation['severity']): string {
  return t(
    severity === 'error'
      ? 'pages.weekly_review.severity_error'
      : 'pages.weekly_review.severity_warning'
  )
}

/**
 * ChecksPanel is the validation view of one report: every finding with its
 * rule, section and fix hint, an acknowledgement per finding (a note may say
 * what was checked), and the unresolved counter that blocks confirmation.
 * Acknowledgement is local until Confirm sends the fingerprints; the service
 * re-validates inside the confirm transaction, so nothing here can fake a
 * pass.
 */
export function ChecksPanel({
  template,
  validation,
  loading,
  error,
  acks,
  notes,
  onToggleAck,
  onNoteChange
}: ChecksPanelProps): JSX.Element {
  const t = useT()
  if (error !== null) {
    return (
      <Banner tone="danger" title={t('pages.weekly_review.banner_problem')}>
        <span className={styles.errorMessage}>
          {error instanceof Error ? error.message : String(error)}
        </span>
      </Banner>
    )
  }
  if (loading) return <Skeleton rows={4} />

  const violations = validation?.violations ?? []
  const errors = violations.filter((violation) => violation.severity === 'error').length
  const warnings = violations.length - errors
  const unresolved = unresolvedCount(violations, acks)
  const sectionTitle = (key: string): string =>
    key === '' ? '—' : (template?.sections.find((section) => section.key === key)?.title ?? key)

  if (violations.length === 0) {
    return (
      <EmptyState
        title={t('pages.weekly_review.checks_clean_title')}
        description={t('pages.weekly_review.checks_clean_message')}
      />
    )
  }

  const ordered = [...violations].sort((left, right) => {
    if (left.severity !== right.severity) return left.severity === 'error' ? -1 : 1
    return left.section.localeCompare(right.section)
  })

  return (
    <div className={styles.dialogGrid}>
      <div className={styles.row}>
        <Badge tone="danger">{t('pages.weekly_review.checks_errors', { n: errors })}</Badge>
        <Badge tone="warning">{t('pages.weekly_review.checks_warnings', { n: warnings })}</Badge>
        <Badge tone={unresolved === 0 ? 'success' : 'warning'}>
          {t('pages.weekly_review.checks_unresolved', { n: unresolved })}
        </Badge>
      </div>
      {unresolved > 0 && (
        <Banner tone="warning" title={t('pages.weekly_review.confirm_blocked', { n: unresolved })}>
          {t('pages.weekly_review.confirm_blocked_hint')}
        </Banner>
      )}
      <ul className={styles.checkList} aria-label={t('pages.weekly_review.tab_checks')}>
        {ordered.map((violation) => {
          const acknowledged = violation.ack !== undefined || acks.has(violation.fingerprint)
          const note = notes.get(violation.fingerprint) ?? violation.ack?.note ?? ''
          return (
            <li key={violation.fingerprint} className={styles.checkRow}>
              <Badge tone={violation.severity === 'error' ? 'danger' : 'warning'}>
                {severityLabel(t, violation.severity)}
              </Badge>
              <div className={styles.checkMain}>
                <span className={styles.checkMessage}>{violation.message}</span>
                <div className={styles.checkMeta}>
                  <Mono>{violation.rule}</Mono>
                  <span>{sectionTitle(violation.section)}</span>
                  {violation.ref !== undefined && (
                    <Mono>
                      {violation.ref.type} {violation.ref.id}
                    </Mono>
                  )}
                  <Mono>{violation.fingerprint}</Mono>
                </div>
                {acknowledged ? (
                  <div className={styles.ackDone}>
                    <Badge tone="success">{t('pages.weekly_review.ack_done')}</Badge>
                    <TextInput
                      label={t('pages.weekly_review.ack_note')}
                      value={note}
                      onChange={(value) => onNoteChange(violation.fingerprint, value)}
                      hint={t('pages.weekly_review.ack_note_hint')}
                      disabled={violation.ack !== undefined}
                    />
                    <Button
                      variant="ghost"
                      onClick={() => onToggleAck(violation)}
                      disabled={violation.ack !== undefined}
                    >
                      {t('pages.weekly_review.btn_unack')}
                    </Button>
                  </div>
                ) : (
                  <div className={styles.ackBox}>
                    <span className={styles.ackNote}>{t('pages.weekly_review.ack_desc')}</span>
                    <Button variant="secondary" onClick={() => onToggleAck(violation)}>
                      {t('pages.weekly_review.btn_ack')}
                    </Button>
                  </div>
                )}
              </div>
            </li>
          )
        })}
      </ul>
    </div>
  )
}
