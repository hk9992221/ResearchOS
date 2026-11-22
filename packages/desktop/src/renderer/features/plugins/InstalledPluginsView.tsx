import type { PluginInfo } from '@researchos/shared'

import { Badge, Button, Mono, SwitchField, Table, Toolbar } from '../../design/components'
import { useT } from '../../i18n'
import { provenanceKey } from './marketFormat'
import styles from './PluginManagerPage.module.css'

export interface InstalledPluginsViewProps {
  readonly plugins: readonly PluginInfo[]
  readonly busy: string | null
  readonly onEnable: (plugin: PluginInfo) => void
  readonly onDisable: (plugin: PluginInfo) => void
  readonly onUninstall: (plugin: PluginInfo) => void
  readonly onUpdate: (plugin: PluginInfo) => void
  readonly onAutoUpdate: (plugin: PluginInfo, enabled: boolean) => void
  readonly onCheckUpdates: () => void
}

/** Installed view: version, provenance, auto-update and the pending upgrade. */
export function InstalledPluginsView({
  plugins,
  busy,
  onEnable,
  onDisable,
  onUninstall,
  onUpdate,
  onAutoUpdate,
  onCheckUpdates
}: InstalledPluginsViewProps): JSX.Element {
  const t = useT()
  return (
    <div className={styles.view}>
      <Toolbar label={t('pages.plugins.toolbar')}>
        <Button
          variant="secondary"
          onClick={onCheckUpdates}
          disabled={busy !== null}
          aria-busy={busy === 'check-updates'}
        >
          {t('pages.plugins.check_updates')}
        </Button>
      </Toolbar>
      <Table
        label={t('pages.plugins.tab_installed')}
        columns={[
          t('pages.plugins.col_plugin'),
          t('pages.plugins.col_version'),
          t('pages.plugins.col_source'),
          t('pages.plugins.auto_update'),
          t('pages.plugins.col_actions')
        ]}
      >
        {plugins.map((plugin) => (
          <tr key={plugin.id}>
            <td>
              <div className={styles.nameCell}>
                <span className={styles.pluginName}>{plugin.name}</span>
                <Mono>{plugin.id}</Mono>
                <Badge tone={plugin.enabled ? 'success' : 'neutral'}>
                  {plugin.enabled
                    ? t('pages.plugins.state_enabled')
                    : t('pages.plugins.state_disabled')}
                </Badge>
              </div>
              {plugin.lastError !== null && (
                <p className={styles.rowError}>
                  {t('pages.plugins.last_error')}: {plugin.lastError}
                </p>
              )}
            </td>
            <td>
              <Mono>{plugin.version}</Mono>
            </td>
            <td>
              <Badge
                tone={
                  plugin.sourceType === 'registry'
                    ? 'success'
                    : plugin.sourceType === 'github'
                      ? 'info'
                      : 'neutral'
                }
              >
                {t(provenanceKey(plugin.sourceType))}
              </Badge>
            </td>
            <td>
              {plugin.sourceType === 'local' ? (
                <span className={styles.muted}>—</span>
              ) : (
                <SwitchField
                  label={t('pages.plugins.auto_update')}
                  checked={plugin.autoUpdate}
                  disabled={busy !== null}
                  onChange={(enabled) => onAutoUpdate(plugin, enabled)}
                />
              )}
            </td>
            <td>
              <div className={styles.actions}>
                {plugin.enabled ? (
                  <Button variant="secondary" size="sm" onClick={() => onDisable(plugin)} disabled={busy !== null}>
                    {t('pages.plugins.disable')}
                  </Button>
                ) : (
                  <Button variant="primary" size="sm" onClick={() => onEnable(plugin)} disabled={busy !== null}>
                    {t('pages.plugins.enable')}
                  </Button>
                )}
                {plugin.availableVersion !== null && (
                  <Button
                    variant="primary"
                    size="sm"
                    onClick={() => onUpdate(plugin)}
                    disabled={busy !== null}
                  >
                    {t('pages.plugins.update')}
                  </Button>
                )}
                {plugin.availableVersion !== null && plugin.updateRequiresApproval && (
                  <Badge tone="warning">{t('pages.plugins.update_requires_approval')}</Badge>
                )}
                <Button variant="danger" size="sm" onClick={() => onUninstall(plugin)} disabled={busy !== null}>
                  {t('pages.plugins.uninstall')}
                </Button>
              </div>
            </td>
          </tr>
        ))}
      </Table>
    </div>
  )
}