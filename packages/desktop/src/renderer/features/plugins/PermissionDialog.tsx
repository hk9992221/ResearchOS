import { useState } from 'react'

import type { PluginPermission } from '@researchos/shared'
import { isPluginWritePermission } from '@researchos/shared'

import { Button, Modal } from '../../design/components'
import { useT } from '../../i18n'
import styles from './PluginManagerPage.module.css'

/** What the grant dialog needs before install, enable or an escalated update. */
export interface GrantTarget {
  readonly name: string
  readonly permissions: readonly PluginPermission[]
  readonly networkDomains: readonly string[]
}

/**
 * PermissionDialog is the one grant screen: the manifest's exact scopes,
 * write scopes flagged, and the domain allow-list when network is selected.
 */
export function PermissionDialog({
  target,
  busy,
  onCancel,
  onConfirm
}: {
  readonly target: GrantTarget | null
  readonly busy: boolean
  readonly onCancel: () => void
  readonly onConfirm: (permissions: PluginPermission[], networkDomains: string[]) => void
}): JSX.Element {
  const t = useT()
  const [selected, setSelected] = useState<Set<PluginPermission>>(
    () => new Set(target?.permissions ?? [])
  )
  const [domains, setDomains] = useState((target?.networkDomains ?? []).join('\n'))

  const toggle = (permission: PluginPermission): void => {
    setSelected((current) => {
      const next = new Set(current)
      if (next.has(permission)) next.delete(permission)
      else next.add(permission)
      return next
    })
  }

  return (
    <Modal
      open={target !== null}
      title={t('pages.plugins.permissions_title', { name: target?.name ?? '' })}
      onClose={onCancel}
      width={520}
      footer={
        <>
          <Button variant="secondary" onClick={onCancel} disabled={busy}>
            {t('pages.plugins.cancel')}
          </Button>
          <Button
            variant="primary"
            disabled={busy}
            onClick={() => {
              if (target === null) return
              onConfirm(
                target.permissions.filter((permission) => selected.has(permission)),
                domains
                  .split(/\r?\n/)
                  .map((domain) => domain.trim())
                  .filter((domain) => domain !== '')
              )
            }}
          >
            {t('pages.plugins.permissions_confirm')}
          </Button>
        </>
      }
    >
      <p className={styles.dialogText}>{t('pages.plugins.permissions_description')}</p>
      <div className={styles.grantList}>
        {(target?.permissions ?? []).map((permission) => (
          <label key={permission} className={styles.grantRow}>
            <input
              type="checkbox"
              checked={selected.has(permission)}
              onChange={() => toggle(permission)}
            />
            <span className={styles.mono}>{permission}</span>
            {isPluginWritePermission(permission) && (
              <span className={styles.writeFlag}>{t('pages.plugins.write_scope')}</span>
            )}
          </label>
        ))}
      </div>
      {selected.has('network') && (
        <label className={styles.domains}>
          <span>{t('pages.plugins.network_domains')}</span>
          <textarea
            value={domains}
            placeholder="example.com"
            onChange={(event) => setDomains(event.target.value)}
          />
        </label>
      )}
    </Modal>
  )
}