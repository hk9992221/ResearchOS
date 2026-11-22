import { useState } from 'react'
import type { AiProtocol, ProviderView } from '@researchos/shared'

import {
  Banner,
  Button,
  Modal,
  SelectField,
  SwitchField,
  TextInput
} from '../../design/components'
import { useT } from '../../i18n'
import {
  AI_PROTOCOLS,
  hasErrors,
  isAiProtocol,
  validateProviderForm,
  type ProviderFormErrors,
  type ProviderFormValues
} from './provider-form'

export interface ProviderFormProps {
  readonly open: boolean
  /** null creates; a row edits. */
  readonly provider: ProviderView | null
  readonly busy: boolean
  readonly error?: unknown
  readonly onSubmit: (values: ProviderFormValues, enabled: boolean) => void
  readonly onClose: () => void
}

/** The add/edit provider dialog; key material is write-only. */
export function ProviderForm({
  open,
  provider,
  busy,
  error,
  onSubmit,
  onClose
}: ProviderFormProps): JSX.Element {
  const t = useT()
  const [values, setValues] = useState<ProviderFormValues>(() => ({
    name: provider?.name ?? '',
    protocol: provider?.protocol ?? 'openai_chat',
    baseUrl: provider?.baseUrl ?? '',
    apiKey: ''
  }))
  const [errors, setErrors] = useState<ProviderFormErrors>({})
  const [enabled, setEnabled] = useState(provider?.enabled ?? true)

  const submit = (): void => {
    const found = validateProviderForm(values)
    setErrors(found)
    if (hasErrors(found)) return
    onSubmit(
      {
        name: values.name.trim(),
        protocol: values.protocol,
        baseUrl: values.baseUrl.trim(),
        apiKey: values.apiKey
      },
      enabled
    )
  }

  return (
    <Modal
      open={open}
      title={provider === null ? t('pages.settings.ai.dialog_add_title') : t('pages.settings.ai.dialog_edit_title')}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button variant="primary" onClick={submit} disabled={busy}>
            {t('pages.settings.ai.btn_save')}
          </Button>
        </>
      }
    >
      <p>{t('pages.settings.ai.dialog_desc')}</p>
      <TextInput
        label={t('pages.settings.ai.field_name')}
        value={values.name}
        onChange={(name) => setValues((current) => ({ ...current, name }))}
        hint={t('pages.settings.ai.field_name_hint')}
        error={errors.name === undefined ? undefined : t(errors.name)}
        autoFocus
      />
      <SelectField
        label={t('pages.settings.ai.field_protocol')}
        value={values.protocol}
        options={AI_PROTOCOLS.map((protocol) => ({
          value: protocol,
          label: t(`pages.settings.ai.protocol.${protocol}`)
        }))}
        onChange={(protocol) => {
          if (isAiProtocol(protocol)) {
            setValues((current) => ({ ...current, protocol }))
          }
        }}
      />
      <TextInput
        label={t('pages.settings.ai.field_base_url')}
        value={values.baseUrl}
        onChange={(baseUrl) => setValues((current) => ({ ...current, baseUrl }))}
        hint={t('pages.settings.ai.field_base_url_hint')}
        error={errors.baseUrl === undefined ? undefined : t(errors.baseUrl)}
      />
      <TextInput
        label={t('pages.settings.ai.field_api_key')}
        type="password"
        value={values.apiKey}
        onChange={(apiKey) => setValues((current) => ({ ...current, apiKey }))}
        hint={
          provider === null
            ? t('pages.settings.ai.api_key_create_hint')
            : t('pages.settings.ai.api_key_edit_hint')
        }
      />
      <SwitchField
        label={t('pages.settings.ai.field_enabled')}
        checked={enabled}
        onChange={setEnabled}
      />
      {error !== null && error !== undefined && (
        <Banner tone="danger" title={t('pages.settings.ai.provider_save_failed')}>
          {error instanceof Error ? error.message : String(error)}
        </Banner>
      )}
    </Modal>
  )
}