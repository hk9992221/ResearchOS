import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'

import type { WorkspaceScanReport } from '@researchos/shared'

import {
  Banner,
  Button,
  Card,
  ErrorState,
  Page,
  PageHeader,
  SelectField,
  TextInput,
  Mono
} from '../../design/components'
import { useI18n, useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { classifyWorkspace, wizardAction, type WizardDirectoryState } from './classify'
import styles from './WorkspaceWizard.module.css'

type Step = 'welcome' | 'directory' | 'review' | 'running' | 'done'

const STATE_LABEL: Record<WizardDirectoryState, string> = {
  empty: 'pages.wizard.state_empty',
  existing: 'pages.wizard.state_existing',
  foreign: 'pages.wizard.state_foreign',
  marker_missing: 'pages.wizard.state_marker_missing',
  marker_corrupt: 'pages.wizard.state_marker_corrupt'
}

const STATE_MESSAGE: Record<WizardDirectoryState, string> = {
  empty: 'boot.inspect.empty',
  existing: 'boot.inspect.existing',
  foreign: 'boot.inspect.foreign',
  marker_missing: 'boot.inspect.marker_missing',
  marker_corrupt: 'boot.inspect.marker_corrupt'
}

const STATE_GUIDANCE: Partial<Record<WizardDirectoryState, string>> = {
  foreign: 'pages.wizard.guidance_foreign',
  marker_missing: 'pages.wizard.guidance_marker_missing',
  marker_corrupt: 'pages.wizard.guidance_marker_corrupt'
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

/**
 * WorkspaceWizard is the pre-shell first-run flow (Go pages/wizard.go):
 * language first, then the folder, then the review — initialize an empty
 * folder, open an existing workspace, and refuse a foreign one. It is not a
 * shell destination: it lives at /welcome, outside the sidebar.
 */
export default function WorkspaceWizard(): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { language, preference, available, setLanguage } = useI18n()

  const [step, setStep] = useState<Step>('welcome')
  const [root, setRoot] = useState('')
  const [report, setReport] = useState<WorkspaceScanReport | null>(null)
  const [directoryState, setDirectoryState] = useState<WizardDirectoryState | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [pickerError, setPickerError] = useState<string | null>(null)

  const statusQuery = useQuery({
    queryKey: queryKeys.workspace.status(),
    queryFn: () => ipc.invoke('workspace.status')
  })

  const languageOptions = [
    { value: 'system', label: t('settings.language.system_hint', { language }) },
    ...available.map((option) => ({ value: option.tag, label: option.nativeName }))
  ]

  const browse = async (): Promise<void> => {
    setPickerError(null)
    try {
      const result = await ipc.invoke('app.pickFolder', {
        title: t('boot.dialog.select_workspace'),
        defaultPath: root === '' ? undefined : root
      })
      const picked = result.paths[0]
      if (!result.canceled && picked !== undefined) {
        setRoot(picked)
        setReport(null)
        setDirectoryState(null)
      }
    } catch (cause) {
      setPickerError(t('pages.wizard.err_chooser_failed', { error: errorText(cause) }))
    }
  }

  const validate = async (): Promise<void> => {
    if (root.trim() === '') return
    setBusy(true)
    setError(null)
    try {
      const scan = await ipc.invoke('workspace.scan', { root: root.trim() })
      setReport(scan)
      setDirectoryState(classifyWorkspace(scan))
      setStep('review')
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const activate = async (state: WizardDirectoryState): Promise<void> => {
    const action = wizardAction(state)
    if (action === null) return
    setStep('running')
    setError(null)
    try {
      if (action === 'initialize') {
        await ipc.invoke('workspace.create', { root: root.trim() })
      } else {
        await ipc.invoke('workspace.open', { root: root.trim() })
      }
      // Gap #4 from phase 2: there is no workspace.changed event, so the
      // wizard invalidates the workspace and project caches itself.
      await queryClient.invalidateQueries({ queryKey: queryKeys.workspace.all })
      await queryClient.invalidateQueries({ queryKey: queryKeys.projects.all })
      setStep('done')
    } catch (cause) {
      setError(cause)
      setStep('review')
    }
  }

  const chooseOther = (): void => {
    setReport(null)
    setDirectoryState(null)
    setError(null)
    setStep('directory')
  }

  const guidanceKey = directoryState === null ? undefined : STATE_GUIDANCE[directoryState]

  return (
    <Page>
      <PageHeader title={t('pages.wizard.title')} />

      {step === 'welcome' && (
        <>
          <Card title={t('settings.language.title')}>
            <SelectField
              label={t('settings.language.label')}
              value={preference}
              options={languageOptions}
              onChange={(value) => setLanguage(value)}
              hint={t('settings.language.description')}
            />
          </Card>
          <Card title={t('pages.wizard.card_next')}>
            <ul className={styles.bullets}>
              {['pages.wizard.bullet_1', 'pages.wizard.bullet_2', 'pages.wizard.bullet_3'].map(
                (key) => (
                  <li key={key} className={styles.bullet}>
                    <span aria-hidden="true" className={styles.glyph}>
                      {t('pages.wizard.bullet_glyph')}
                    </span>
                    <span>{t(key)}</span>
                  </li>
                )
              )}
            </ul>
          </Card>
          {statusQuery.data?.open === true && (
            <Banner
              tone="info"
              title={t('pages.wizard.current_workspace')}
              action={
                <Button variant="secondary" onClick={() => navigate('/')}>
                  {t('pages.wizard.btn_use_current')}
                </Button>
              }
            >
              <Mono>{statusQuery.data.root ?? ''}</Mono>
            </Banner>
          )}
          <div className={styles.footer}>
            <Button variant="primary" onClick={() => setStep('directory')}>
              {t('pages.wizard.btn_start')}
            </Button>
          </div>
        </>
      )}

      {step === 'directory' && (
        <>
          <Card title={t('pages.wizard.card_folder')}>
            <TextInput
              label={t('pages.wizard.field_path_label')}
              value={root}
              onChange={(value) => {
                setRoot(value)
                setReport(null)
                setDirectoryState(null)
                setError(null)
              }}
              hint={t('pages.wizard.field_path_desc')}
              placeholder="F:/ResearchOS-Workspace"
              disabled={busy}
            />
            {pickerError !== null && <Banner tone="warning">{pickerError}</Banner>}
            {error !== null && (
              <ErrorState
                title={t('pages.wizard.error_validate')}
                message={errorText(error)}
                retryLabel={t('shell.overview.error_retry')}
                onRetry={() => {
                  void validate()
                }}
              />
            )}
            <div className={styles.footer}>
              <Button variant="secondary" onClick={() => void browse()} disabled={busy}>
                {t('pages.common.btn_browse')}
              </Button>
              <Button
                variant="primary"
                onClick={() => void validate()}
                disabled={busy || root.trim() === ''}
              >
                {t('pages.wizard.btn_validate')}
              </Button>
            </div>
          </Card>
        </>
      )}

      {step === 'review' && directoryState !== null && (
        <Card title={t('pages.wizard.card_check')}>
          <div className={styles.review}>
            <Mono>{report?.root ?? root}</Mono>
            <p className={styles.stateLabel}>{t(STATE_LABEL[directoryState])}</p>
            <p className={styles.stateMessage}>{t(STATE_MESSAGE[directoryState])}</p>
            {guidanceKey !== undefined && <Banner tone="warning">{t(guidanceKey)}</Banner>}
            {error !== null && (
              <ErrorState
                title={
                  wizardAction(directoryState) === 'initialize'
                    ? t('pages.wizard.error_create')
                    : t('pages.wizard.error_open')
                }
                message={errorText(error)}
                retryLabel={t('shell.overview.error_retry')}
                onRetry={() => void activate(directoryState)}
              />
            )}
          </div>
          <div className={styles.footer}>
            <Button variant="secondary" onClick={chooseOther}>
              {t('pages.wizard.btn_choose_other')}
            </Button>
            {wizardAction(directoryState) === 'initialize' && (
              <Button variant="primary" onClick={() => void activate(directoryState)}>
                {t('pages.wizard.btn_initialize')}
              </Button>
            )}
            {wizardAction(directoryState) === 'open' && (
              <Button variant="primary" onClick={() => void activate(directoryState)}>
                {t('pages.wizard.btn_open')}
              </Button>
            )}
          </div>
        </Card>
      )}

      {step === 'running' && (
        <Card title={t('pages.wizard.card_running')}>
          <Banner tone="info">{t('pages.wizard.stage_preparing')}</Banner>
        </Card>
      )}

      {step === 'done' && (
        <>
          <Banner tone="success" title={t('pages.wizard.banner_done')}>
            {t('pages.wizard.banner_done_message')}
          </Banner>
          <div className={styles.footer}>
            <Button variant="secondary" onClick={chooseOther}>
              {t('pages.wizard.btn_choose_other')}
            </Button>
            <Button variant="primary" onClick={() => navigate('/')}>
              {t('pages.wizard.btn_continue')}
            </Button>
          </div>
        </>
      )}
    </Page>
  )
}