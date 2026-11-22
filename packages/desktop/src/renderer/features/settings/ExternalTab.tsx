import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import {
  Banner,
  Button,
  Card,
  ErrorState,
  Skeleton,
  TextInput
} from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'

/** External data section: the Zotero storage directory. */
export function ExternalTab(): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [edited, setEdited] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [pickerError, setPickerError] = useState<string | null>(null)

  const configQuery = useQuery({
    queryKey: queryKeys.config,
    queryFn: () => ipc.invoke('app.config.get')
  })

  if (configQuery.isPending) return <Skeleton rows={3} />
  if (configQuery.isError) {
    return (
      <ErrorState
        title={t('pages.settings.banner_load_fail')}
        message={configQuery.error.message}
        retryLabel={t('shell.overview.error_retry')}
        onRetry={() => void configQuery.refetch()}
      />
    )
  }

  const configured = configQuery.data.zoteroDataDir
  const value = edited ?? configured ?? ''

  const browse = async (): Promise<void> => {
    setPickerError(null)
    try {
      const result = await ipc.invoke('app.pickFolder', {
        title: t('boot.dialog.select_workspace'),
        defaultPath: value === '' ? undefined : value
      })
      const picked = result.paths[0]
      if (!result.canceled && picked !== undefined) {
        setEdited(picked)
        setSaved(false)
      }
    } catch (cause) {
      setPickerError(t('pages.wizard.err_chooser_failed', { error: String(cause) }))
    }
  }

  const save = async (): Promise<void> => {
    setError(null)
    setSaved(false)
    try {
      await ipc.invoke('app.config.update', {
        patch: { zoteroDataDir: value.trim() === '' ? null : value.trim() }
      })
      await queryClient.invalidateQueries({ queryKey: queryKeys.config })
      setEdited(null)
      setSaved(true)
    } catch (cause) {
      setError(cause)
    }
  }

  return (
    <>
      <Card title={t('pages.settings.tab_external')}>
        <TextInput
          label={t('pages.settings.zotero_label')}
          value={value}
          onChange={(next) => {
            setEdited(next)
            setSaved(false)
          }}
          hint={t('pages.settings.zotero_desc')}
          placeholder="C:/Users/me/Zotero"
        />
        <p>
          {configured === null
            ? t('pages.settings.status_not_configured')
            : t('pages.settings.status_configured', { path: configured })}
        </p>
        {pickerError !== null && <Banner tone="warning">{pickerError}</Banner>}
        {saved && <Banner tone="success">{t('pages.settings.toast_saved_title')}</Banner>}
        {error !== null && (
          <Banner tone="danger" title={t('pages.settings.external_save_failed')}>
            {error instanceof Error ? error.message : String(error)}
          </Banner>
        )}
        <div>
          <Button variant="secondary" size="sm" onClick={() => void browse()}>
            {t('pages.common.btn_browse')}
          </Button>{' '}
          <Button variant="primary" size="sm" onClick={() => void save()}>
            {t('pages.settings.task_save_zotero')}
          </Button>
        </div>
      </Card>
      <Banner tone="info">{t('pages.settings.external_check_missing')}</Banner>
    </>
  )
}