import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import {
  AppError,
  AppErrorCode,
  PLUGIN_MARKET_INTERVALS,
  type PluginMarketInterval
} from '@researchos/shared'

import {
  Banner,
  Button,
  ErrorState,
  Mono,
  Modal,
  SelectField,
  Table,
  TextInput
} from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { pushToast } from '../../state/toasts'
import { shortDate } from './marketFormat'
import styles from './PluginManagerPage.module.css'

const INTERVAL_KEYS: Readonly<Record<PluginMarketInterval, string>> = {
  off: 'pages.plugins.interval_off',
  '6h': 'pages.plugins.interval_6h',
  '12h': 'pages.plugins.interval_12h',
  '24h': 'pages.plugins.interval_24h'
}

export interface RegistrySettingsDialogProps {
  readonly open: boolean
  readonly onClose: () => void
}

/** Plugin sources plus the market client settings, in one dialog. */
export function RegistrySettingsDialog({
  open,
  onClose
}: RegistrySettingsDialogProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [addError, setAddError] = useState<string | null>(null)
  const [working, setWorking] = useState(false)
  const [registryUrl, setRegistryUrl] = useState('')
  const [mirrorPrefix, setMirrorPrefix] = useState('')
  const [interval, setInterval] = useState<PluginMarketInterval>('24h')

  const registries = useQuery({
    queryKey: queryKeys.plugins.registries,
    queryFn: () => ipc.invoke('plugins.registries.list'),
    enabled: open
  })

  const settings = useQuery({
    queryKey: queryKeys.plugins.settings,
    queryFn: () => ipc.invoke('plugins.market.settings.get'),
    enabled: open
  })

  useEffect(() => {
    if (settings.data === undefined) return
    setRegistryUrl(settings.data.registryUrl)
    setMirrorPrefix(settings.data.mirrorPrefix)
    setInterval(settings.data.interval)
  }, [settings.data])

  const refreshMarket = (): void => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.plugins.all })
  }

  const addRegistry = async (): Promise<void> => {
    setWorking(true)
    setAddError(null)
    try {
      await ipc.invoke('plugins.registries.add', { name: name.trim(), url: url.trim() })
      setName('')
      setUrl('')
      await queryClient.invalidateQueries({ queryKey: queryKeys.plugins.registries })
      refreshMarket()
    } catch (cause) {
      const code = cause instanceof AppError ? cause.code : null
      setAddError(
        code === AppErrorCode.InvalidArgument
          ? t('pages.plugins.registry_add_invalid')
          : code === AppErrorCode.NotFound
            ? t('pages.plugins.registry_add_not_found')
            : t('pages.plugins.registry_add_failed')
      )
    } finally {
      setWorking(false)
    }
  }

  const removeRegistry = async (id: string): Promise<void> => {
    setWorking(true)
    setAddError(null)
    try {
      await ipc.invoke('plugins.registries.remove', { id })
      await queryClient.invalidateQueries({ queryKey: queryKeys.plugins.registries })
      refreshMarket()
    } catch {
      setAddError(t('pages.plugins.registry_remove_failed'))
    } finally {
      setWorking(false)
    }
  }

  const saveSettings = async (): Promise<void> => {
    setWorking(true)
    try {
      const next = await ipc.invoke('plugins.market.settings.update', {
        registryUrl: registryUrl.trim(),
        mirrorPrefix: mirrorPrefix.trim(),
        interval
      })
      queryClient.setQueryData(queryKeys.plugins.settings, next)
      refreshMarket()
      pushToast({ tone: 'success', title: t('pages.plugins.settings_saved') })
    } catch {
      pushToast({ tone: 'error', title: t('pages.plugins.action_failed') })
    } finally {
      setWorking(false)
    }
  }

  return (
    <Modal
      open={open}
      title={t('pages.plugins.source_settings')}
      onClose={onClose}
      width={760}
      footer={
        <Button variant="secondary" onClick={onClose} disabled={working}>
          {t('pages.plugins.close')}
        </Button>
      }
    >
      {registries.isError ? (
        <ErrorState
          title={t('pages.plugins.load_failed')}
          message={
            registries.error instanceof Error ? registries.error.message : String(registries.error)
          }
          onRetry={() => void registries.refetch()}
          retryLabel={t('pages.plugins.retry')}
        />
      ) : (
        <Table
          label={t('pages.plugins.source_settings')}
          columns={[
            t('pages.plugins.sources_name'),
            t('pages.plugins.sources_url'),
            t('pages.plugins.sources_plugins'),
            t('pages.plugins.sources_fetched'),
            t('pages.plugins.col_actions')
          ]}
        >
          {(registries.data ?? []).map((registry) => (
            <tr key={registry.id}>
              <td>{registry.name}</td>
              <td>
                <Mono>{registry.url}</Mono>
              </td>
              <td>{registry.pluginCount}</td>
              <td>{shortDate(registry.fetchedAt)}</td>
              <td>
                {!registry.official && (
                  <Button
                    variant="danger"
                    size="sm"
                    disabled={working}
                    onClick={() => void removeRegistry(registry.id)}
                  >
                    {t('pages.plugins.sources_remove')}
                  </Button>
                )}
              </td>
            </tr>
          ))}
        </Table>
      )}

      <div className={styles.addRow}>
        <div className={styles.nameField}>
          <TextInput
            label={t('pages.plugins.sources_name')}
            value={name}
            onChange={setName}
          />
        </div>
        <div className={styles.searchField}>
          <TextInput label={t('pages.plugins.sources_url')} value={url} onChange={setUrl} />
        </div>
        <Button
          variant="secondary"
          disabled={working || name.trim() === '' || url.trim() === ''}
          onClick={() => void addRegistry()}
        >
          {t('pages.plugins.sources_add')}
        </Button>
      </div>
      {addError !== null && (
        <Banner tone="danger" title={addError} />
      )}

      <div className={styles.settingsForm}>
        <TextInput
          label={t('pages.plugins.market_registry_url')}
          value={registryUrl}
          onChange={setRegistryUrl}
          placeholder="https://"
        />
        <TextInput
          label={t('pages.plugins.market_mirror_prefix')}
          value={mirrorPrefix}
          onChange={setMirrorPrefix}
          placeholder="https://"
        />
        <div className={styles.intervalRow}>
          <SelectField
            label={t('pages.plugins.market_interval')}
            value={interval}
            options={PLUGIN_MARKET_INTERVALS.map((value) => ({
              value,
              label: t(INTERVAL_KEYS[value])
            }))}
            onChange={(value) => setInterval(value as PluginMarketInterval)}
          />
          <Button
            variant="primary"
            disabled={working || settings.isPending || settings.isError}
            onClick={() => void saveSettings()}
          >
            {t('pages.plugins.settings_save')}
          </Button>
        </div>
      </div>
    </Modal>
  )
}