import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import type { AiRole, ProviderView, RoleView } from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  Card,
  ConfirmDialog,
  ErrorState,
  SelectField,
  Skeleton,
  Table
} from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { ProviderForm } from './ProviderForm'
import type { ProviderFormValues } from './provider-form'

interface LastAction {
  readonly tone: 'success' | 'warning' | 'danger'
  readonly text: string
}

const ROLE_LABEL: Record<AiRole, string> = {
  chat_main: 'pages.settings.ai.role.chat_main',
  embedding: 'pages.settings.ai.role.embedding',
  rerank: 'pages.settings.ai.role.rerank'
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

/** AI section: availability, providers, model cache, role assignments, skills. */
export function AiTab(): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()

  const providersQuery = useQuery({
    queryKey: queryKeys.ai.providers,
    queryFn: () => ipc.invoke('ai.providers.list')
  })
  const rolesQuery = useQuery({
    queryKey: queryKeys.ai.roles,
    queryFn: () => ipc.invoke('ai.roles.list')
  })
  const availabilityQuery = useQuery({
    queryKey: queryKeys.ai.availability,
    queryFn: () => ipc.invoke('ai.availability', { refresh: false })
  })
  const skillsQuery = useQuery({
    queryKey: queryKeys.weeklySkills,
    queryFn: () => ipc.invoke('weekly.skills.list')
  })

  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<ProviderView | null>(null)
  const [saving, setSaving] = useState(false)
  const [formError, setFormError] = useState<unknown>(null)
  const [deleting, setDeleting] = useState<ProviderView | null>(null)
  const [lastAction, setLastAction] = useState<LastAction | null>(null)
  const [selectedProviderId, setSelectedProviderId] = useState<string>('')

  const providers = providersQuery.data ?? []
  const roles = rolesQuery.data ?? []
  const providerOptions = providers.map((provider) => ({
    value: provider.id,
    label: provider.name
  }))

  const effectiveProviderId =
    selectedProviderId !== ''
      ? selectedProviderId
      : (providers[0]?.id ?? '')

  const modelsQuery = useQuery({
    queryKey: queryKeys.ai.models(effectiveProviderId),
    queryFn: () => ipc.invoke('ai.models.list', { providerId: effectiveProviderId }),
    enabled: effectiveProviderId !== ''
  })

  const refreshRoles = async (): Promise<void> => {
    await queryClient.invalidateQueries({ queryKey: queryKeys.ai.roles })
    await queryClient.invalidateQueries({ queryKey: queryKeys.ai.availability })
  }

  const saveProvider = async (values: ProviderFormValues, enabled: boolean): Promise<void> => {
    setSaving(true)
    setFormError(null)
    try {
      if (editing === null) {
        await ipc.invoke('ai.providers.create', {
          provider: {
            name: values.name,
            protocol: values.protocol,
            baseUrl: values.baseUrl,
            apiKey: values.apiKey === '' ? undefined : values.apiKey,
            enabled
          }
        })
      } else {
        await ipc.invoke('ai.providers.update', {
          providerId: editing.id,
          patch: {
            name: values.name,
            protocol: values.protocol,
            baseUrl: values.baseUrl,
            apiKey: values.apiKey === '' ? undefined : values.apiKey,
            enabled
          }
        })
      }
      await queryClient.invalidateQueries({ queryKey: queryKeys.ai.all })
      setFormOpen(false)
      setEditing(null)
      setLastAction({ tone: 'success', text: t('pages.settings.ai.action_done') })
    } catch (cause) {
      setFormError(cause)
    } finally {
      setSaving(false)
    }
  }

  const testProvider = async (provider: ProviderView): Promise<void> => {
    setLastAction(null)
    try {
      const result = await ipc.invoke('ai.providers.test', { providerId: provider.id })
      setLastAction(
        result.ok
          ? {
              tone: 'success',
              text: t('pages.settings.ai.test_ok', {
                ms: result.latencyMs,
                n: result.modelCount
              })
            }
          : { tone: 'danger', text: result.error ?? t('pages.settings.ai.test_failed') }
      )
      await queryClient.invalidateQueries({ queryKey: queryKeys.ai.providers })
    } catch (cause) {
      setLastAction({ tone: 'danger', text: `${t('pages.settings.ai.test_failed')} ${errorText(cause)}` })
    }
  }

  const refreshModels = async (provider: ProviderView): Promise<void> => {
    setLastAction(null)
    try {
      const result = await ipc.invoke('ai.models.refresh', { providerId: provider.id })
      await queryClient.invalidateQueries({ queryKey: queryKeys.ai.all })
      setLastAction(
        result.total === 0
          ? {
              tone: 'warning',
              text: t('pages.settings.ai.sync_warning', { total: result.total })
            }
          : {
              tone: 'success',
              text: t('pages.settings.ai.sync_ok', {
                added: result.added,
                unavailable: result.markedUnavailable,
                total: result.total
              })
            }
      )
    } catch (cause) {
      setLastAction({ tone: 'danger', text: errorText(cause) })
    }
  }

  const deleteProvider = async (): Promise<void> => {
    if (deleting === null) return
    try {
      await ipc.invoke('ai.providers.delete', { providerId: deleting.id })
      await queryClient.invalidateQueries({ queryKey: queryKeys.ai.all })
      setDeleting(null)
      setLastAction({ tone: 'success', text: t('pages.settings.ai.action_done') })
    } catch (cause) {
      setDeleting(null)
      setLastAction({ tone: 'danger', text: errorText(cause) })
    }
  }

  if (providersQuery.isPending) return <Skeleton rows={5} />
  if (providersQuery.isError) {
    return (
      <ErrorState
        title={t('pages.settings.ai.unavailable_title')}
        message={providersQuery.error.message}
        retryLabel={t('shell.overview.error_retry')}
        onRetry={() => void providersQuery.refetch()}
      />
    )
  }

  const availability = availabilityQuery.data

  return (
    <>
      {availability !== undefined && availability.available && (
        <Banner tone="success" title={t('pages.settings.ai.ready_title')}>
          {availability.chatProfile === null
            ? t('pages.settings.ai.ready_generic')
            : t('pages.settings.ai.ready_message', {
                target: `${availability.chatProfile.providerName} / ${availability.chatProfile.modelId}`
              })}
        </Banner>
      )}
      {availability !== undefined && !availability.available && (
        <Banner tone="warning" title={t('pages.settings.ai.not_ready_title')}>
          {t('pages.settings.ai.not_ready_message')}
        </Banner>
      )}
      {lastAction !== null && (
        <Banner tone={lastAction.tone}>{lastAction.text}</Banner>
      )}

      <Card
        title={t('pages.settings.ai.card_providers')}
        actions={
          <Button
            variant="secondary"
            size="sm"
            onClick={() => {
              setEditing(null)
              setFormError(null)
              setFormOpen(true)
            }}
          >
            {t('pages.settings.ai.btn_add_provider')}
          </Button>
        }
      >
        {providers.length === 0 ? (
          <p>{t('pages.settings.ai.empty_providers')}</p>
        ) : (
          <Table
            label={t('pages.settings.ai.card_providers')}
            columns={[
              t('pages.settings.ai.col_name'),
              t('pages.settings.ai.col_protocol'),
              t('pages.settings.ai.col_base_url'),
              t('pages.settings.ai.col_key'),
              t('pages.settings.ai.col_enabled'),
              t('pages.settings.ai.col_actions')
            ]}
          >
            {providers.map((provider) => (
              <tr key={provider.id}>
                <td>{provider.name}</td>
                <td>{t(`pages.settings.ai.protocol.${provider.protocol}`)}</td>
                <td>{provider.baseUrl}</td>
                <td>
                  {provider.hasKey
                    ? t('pages.settings.ai.key_saved')
                    : t('pages.settings.ai.key_missing')}
                </td>
                <td>
                  {provider.enabled
                    ? t('pages.settings.ai.enabled')
                    : t('pages.settings.ai.disabled')}
                </td>
                <td>
                  <Button variant="ghost" size="sm" onClick={() => void testProvider(provider)}>
                    {t('pages.settings.ai.btn_test')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => void refreshModels(provider)}
                  >
                    {t('pages.settings.ai.btn_sync')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => {
                      setEditing(provider)
                      setFormError(null)
                      setFormOpen(true)
                    }}
                  >
                    {t('pages.settings.ai.btn_edit')}
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => setDeleting(provider)}>
                    {t('pages.settings.ai.btn_delete')}
                  </Button>
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Card>

      <Card title={t('pages.settings.ai.card_models')}>
        {providers.length === 0 ? (
          <p>{t('pages.settings.ai.empty_no_provider')}</p>
        ) : (
          <>
            <SelectField
              label={t('pages.settings.ai.field_provider')}
              value={effectiveProviderId}
              options={providerOptions}
              onChange={setSelectedProviderId}
            />
            {modelsQuery.isPending && <Skeleton rows={2} />}
            {modelsQuery.isError && <Banner tone="danger">{modelsQuery.error.message}</Banner>}
            {modelsQuery.data !== undefined && modelsQuery.data.length === 0 && (
              <p>{t('pages.settings.ai.empty_models')}</p>
            )}
            {modelsQuery.data !== undefined && modelsQuery.data.length > 0 && (
              <Table
                label={t('pages.settings.ai.card_models')}
                columns={[
                  t('pages.settings.ai.col_model_id'),
                  t('pages.settings.ai.col_model_name'),
                  t('pages.settings.ai.col_capability'),
                  t('pages.settings.ai.col_source'),
                  t('pages.settings.ai.col_available')
                ]}
              >
                {modelsQuery.data.map((model) => (
                  <tr key={model.modelId}>
                    <td>{model.modelId}</td>
                    <td>{model.displayName}</td>
                    <td>
                      {model.capabilities
                        .map((capability) => t(`pages.settings.ai.capability.${capability}`))
                        .join(', ')}
                    </td>
                    <td>
                      {model.source === 'manual'
                        ? t('pages.settings.ai.source_manual')
                        : t('pages.settings.ai.source_listed')}
                    </td>
                    <td>
                      {model.available
                        ? t('pages.settings.ai.available')
                        : t('pages.settings.ai.unavailable')}
                    </td>
                  </tr>
                ))}
              </Table>
            )}
            <p>{t('pages.settings.ai.models_manual_missing')}</p>
          </>
        )}
      </Card>

      <Card title={t('pages.settings.ai.card_roles')}>
        {rolesQuery.isError && <Banner tone="danger">{rolesQuery.error.message}</Banner>}
        {rolesQuery.data?.map((view) => (
          <RoleAssignmentRow
            key={`${view.role}:${view.assignment?.providerId ?? ''}:${view.assignment?.modelId ?? ''}`}
            view={view}
            providers={providerOptions}
            onSaved={() => void refreshRoles()}
            onError={(cause) => setLastAction({ tone: 'danger', text: errorText(cause) })}
          />
        ))}
      </Card>

      <Card title={t('pages.settings.ai.card_skills')}>
        {skillsQuery.isPending && <Skeleton rows={2} />}
        {skillsQuery.isError && <Banner tone="danger">{skillsQuery.error.message}</Banner>}
        <ul>
          {skillsQuery.data?.skills.map((skill) => (
            <li key={skill.id}>
              {skill.name} {skill.builtin === true && <Badge>{t('pages.settings.ai.source_listed')}</Badge>}
            </li>
          ))}
        </ul>
      </Card>

      <ProviderForm
        key={editing?.id ?? 'new-provider'}
        open={formOpen}
        provider={editing}
        busy={saving}
        error={formError}
        onSubmit={(values, enabled) => void saveProvider(values, enabled)}
        onClose={() => {
          setFormOpen(false)
          setEditing(null)
        }}
      />

      <ConfirmDialog
        open={deleting !== null}
        title={t('pages.settings.ai.delete_provider_title')}
        description={
          deleting === null
            ? undefined
            : t('pages.settings.ai.delete_provider_message', { name: deleting.name })
        }
        confirmLabel={t('pages.settings.ai.btn_delete')}
        cancelLabel={t('common.cancel')}
        tone="danger"
        onConfirm={() => void deleteProvider()}
        onCancel={() => setDeleting(null)}
      />
    </>
  )
}

interface RoleAssignmentRowProps {
  readonly view: RoleView
  readonly providers: readonly { value: string; label: string }[]
  readonly onSaved: () => void
  readonly onError: (error: unknown) => void
}

function RoleAssignmentRow({ view, providers, onSaved, onError }: RoleAssignmentRowProps): JSX.Element {
  const t = useT()
  const [providerId, setProviderId] = useState(view.assignment?.providerId ?? '')
  const [modelId, setModelId] = useState(view.assignment?.modelId ?? '')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const modelsQuery = useQuery({
    queryKey: queryKeys.ai.models(providerId),
    queryFn: () => ipc.invoke('ai.models.list', { providerId }),
    enabled: providerId !== ''
  })

  const save = async (): Promise<void> => {
    setError(null)
    if (providerId === '') {
      setError(t('pages.settings.ai.error.role_provider'))
      return
    }
    if (modelId === '') {
      setError(t('pages.settings.ai.error.role_model'))
      return
    }
    setBusy(true)
    try {
      await ipc.invoke('ai.roles.set', { role: view.role, providerId, modelId })
      onSaved()
    } catch (cause) {
      onError(cause)
    } finally {
      setBusy(false)
    }
  }

  const clear = async (): Promise<void> => {
    setBusy(true)
    try {
      await ipc.invoke('ai.roles.clear', { role: view.role })
      setProviderId('')
      setModelId('')
      onSaved()
    } catch (cause) {
      onError(cause)
    } finally {
      setBusy(false)
    }
  }

  const current = view.assignment === null
    ? t('pages.settings.ai.role_unassigned')
    : t('pages.settings.ai.role_current', {
        target: `${view.providerName ?? ''} / ${view.assignment.modelId}`
      })

  return (
    <div>
      <p>
        {t(ROLE_LABEL[view.role])} — {current}
      </p>
      <SelectField
        label={t('pages.settings.ai.role_provider_label', { role: t(ROLE_LABEL[view.role]) })}
        value={providerId}
        options={[
          { value: '', label: t('pages.settings.ai.provider_placeholder') },
          ...providers
        ]}
        onChange={(value) => {
          setProviderId(value)
          setModelId('')
        }}
      />
      <SelectField
        label={t('pages.settings.ai.role_model_label', { role: t(ROLE_LABEL[view.role]) })}
        value={modelId}
        options={[
          { value: '', label: t('pages.settings.ai.model_placeholder') },
          ...(modelsQuery.data ?? []).map((model) => ({
            value: model.modelId,
            label: model.displayName
          }))
        ]}
        onChange={setModelId}
        disabled={providerId === '' || modelsQuery.isPending}
      />
      {error !== null && <Banner tone="danger">{error}</Banner>}
      <Button variant="primary" size="sm" onClick={() => void save()} disabled={busy}>
        {t('pages.settings.ai.btn_save_role')}
      </Button>{' '}
      <Button variant="ghost" size="sm" onClick={() => void clear()} disabled={busy}>
        {t('pages.settings.ai.btn_clear_role')}
      </Button>
    </div>
  )
}