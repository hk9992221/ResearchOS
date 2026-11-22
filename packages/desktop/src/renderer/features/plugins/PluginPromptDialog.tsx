import { useEffect, useState } from 'react'

import type {
  AppEventPayload,
  PluginPromptParams,
  PluginUiConfirmParams
} from '@researchos/shared'

import { Button, Modal } from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import styles from './PluginManagerPage.module.css'

type PromptRequest = AppEventPayload<'plugins.prompt.request'>

/**
 * Global `ui.prompt` / `ui.confirm` dialog. The header badge names
 * the plugin from host-owned data; the request payload cannot change it. Only
 * one request is shown at a time because the main process already serializes
 * dialogs per plugin.
 */
export function PluginPromptDialog(): JSX.Element | null {
  const t = useT()
  const [request, setRequest] = useState<PromptRequest | null>(null)
  const [value, setValue] = useState('')

  useEffect(
    () =>
      ipc.on('plugins.prompt.request', (payload) => {
        setRequest(payload)
        setValue(
          payload.kind === 'prompt' && 'defaultValue' in payload.params
            ? (payload.params as PluginPromptParams).defaultValue ?? ''
            : ''
        )
      }),
    []
  )

  useEffect(
    () =>
      ipc.on('plugins.prompt.cancel', (payload) => {
        setRequest((current) => (current?.requestId === payload.requestId ? null : current))
      }),
    []
  )

  if (request === null) return null

  const respond = (answer: { submitted?: boolean; confirmed?: boolean; value?: string }): void => {
    const requestId = request.requestId
    setRequest(null)
    void ipc
      .invoke('plugins.prompt.respond', { requestId, ...answer })
      .catch(() => undefined)
  }

  if (request.kind === 'confirm') {
    const params = request.params as PluginUiConfirmParams
    return (
      <Modal
        open
        title={params.title}
        width={460}
        escapeCloses={false}
        onClose={() => respond({ confirmed: false })}
        footer={
          <>
            <Button variant="secondary" onClick={() => respond({ confirmed: false })}>
              {params.cancelLabel ?? t('pages.plugins.prompt.cancel')}
            </Button>
            <Button
              variant={params.destructive === true ? 'danger' : 'primary'}
              onClick={() => respond({ confirmed: true })}
            >
              {params.confirmLabel ?? t('pages.plugins.prompt.confirm')}
            </Button>
          </>
        }
      >
        <p className={styles.pluginBadge}>
          {t('pages.plugins.prompt.badge', { name: request.pluginName })}
        </p>
        <p className={styles.dialogText}>{params.message}</p>
      </Modal>
    )
  }

  const params = request.params as PluginPromptParams
  return (
    <Modal
      open
      title={params.title}
      width={480}
      escapeCloses={false}
      onClose={() => respond({ submitted: false })}
      footer={
        <>
          <Button variant="secondary" onClick={() => respond({ submitted: false })}>
            {params.cancelLabel ?? t('pages.plugins.prompt.cancel')}
          </Button>
          <Button variant="primary" onClick={() => respond({ submitted: true, value })}>
            {params.confirmLabel ?? t('pages.plugins.prompt.confirm')}
          </Button>
        </>
      }
    >
      <p className={styles.pluginBadge}>
        {t('pages.plugins.prompt.badge', { name: request.pluginName })}
      </p>
      {params.message !== undefined && <p className={styles.dialogText}>{params.message}</p>}
      {params.multiline === true ? (
        <textarea
          className={styles.promptInput}
          value={value}
          placeholder={params.placeholder}
          onChange={(event) => setValue(event.target.value)}
        />
      ) : (
        <input
          className={styles.promptInput}
          value={value}
          placeholder={params.placeholder}
          onChange={(event) => setValue(event.target.value)}
        />
      )}
    </Modal>
  )
}

