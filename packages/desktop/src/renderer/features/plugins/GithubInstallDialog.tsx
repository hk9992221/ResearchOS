import { useEffect, useState } from 'react'

import type { GithubReleasePreview } from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  Modal,
  Mono,
  Rows,
  Row,
  Table,
  TextInput
} from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { mebibytes, shortDate, shortSha } from './marketFormat'
import styles from './PluginManagerPage.module.css'

export interface GithubInstallDialogProps {
  readonly open: boolean
  readonly busy: boolean
  readonly installError: string | null
  readonly onClose: () => void
  readonly onInstall: (repo: string, tag: string, confirmUnverified: boolean) => void
}

/** Direct release install: preview the asset, then confirm the grant round. */
export function GithubInstallDialog({
  open,
  busy,
  installError,
  onClose,
  onInstall
}: GithubInstallDialogProps): JSX.Element {
  const t = useT()
  const [repo, setRepo] = useState('')
  const [tag, setTag] = useState('')
  const [preview, setPreview] = useState<GithubReleasePreview | null>(null)
  const [previewing, setPreviewing] = useState(false)
  const [previewError, setPreviewError] = useState<string | null>(null)
  const [confirmUnverified, setConfirmUnverified] = useState(false)

  useEffect(() => {
    if (open) return
    setPreview(null)
    setPreviewError(null)
    setConfirmUnverified(false)
  }, [open])

  const lookUp = async (): Promise<void> => {
    setPreviewing(true)
    setPreviewError(null)
    setPreview(null)
    setConfirmUnverified(false)
    try {
      const result = await ipc.invoke('plugins.market.previewGithub', {
        repo: repo.trim(),
        tag: tag.trim() === '' ? undefined : tag.trim()
      })
      setPreview(result)
    } catch (cause) {
      setPreviewError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setPreviewing(false)
    }
  }

  const asset = preview?.assets[0]
  const unverified = asset !== undefined && asset.sha256 === null

  return (
    <Modal
      open={open}
      title={t('pages.plugins.github_title')}
      onClose={onClose}
      width={640}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            {t('pages.plugins.cancel')}
          </Button>
          <Button
            variant="primary"
            disabled={busy || preview === null || asset === undefined}
            onClick={() => {
              if (preview === null) return
              if (unverified && !confirmUnverified) {
                setConfirmUnverified(true)
                return
              }
              onInstall(preview.repo, preview.tag, unverified)
            }}
          >
            {unverified && confirmUnverified
              ? t('pages.plugins.github_unverified_confirm')
              : t('pages.plugins.install')}
          </Button>
        </>
      }
    >
      <div className={styles.lookupRow}>
        <div className={styles.searchField}>
          <TextInput
            label={t('pages.plugins.field_repo')}
            value={repo}
            placeholder="owner/name"
            onChange={setRepo}
          />
        </div>
        <div className={styles.tagField}>
          <TextInput
            label={t('pages.plugins.field_tag')}
            value={tag}
            placeholder="v1.0.0"
            onChange={setTag}
          />
        </div>
        <Button
          variant="secondary"
          disabled={previewing || repo.trim() === '' || busy}
          onClick={() => void lookUp()}
        >
          {t('pages.plugins.github_query')}
        </Button>
      </div>

      {installError !== null && (
        <Banner tone="danger" title={t('pages.plugins.action_failed')}>
          {installError}
        </Banner>
      )}

      {previewError !== null && (
        <Banner tone="danger" title={t('pages.plugins.github_preview_failed')}>
          {previewError}
        </Banner>
      )}

      {preview !== null && (
        <>
          <Rows>
            <Row label={t('pages.plugins.field_tag')} value={<Mono>{preview.tag}</Mono>} />
            <Row label={t('pages.plugins.field_published')} value={shortDate(preview.publishedAt)} />
          </Rows>
          <Table
            label={t('pages.plugins.github_assets')}
            columns={[
              t('pages.plugins.col_asset'),
              t('pages.plugins.col_size'),
              t('pages.plugins.sha256')
            ]}
          >
            {preview.assets.map((item) => (
              <tr key={item.name}>
                <td>
                  <Mono>{item.name}</Mono>
                </td>
                <td>
                  {mebibytes(item.size)} MiB
                </td>
                <td>
                  {item.sha256 === null ? (
                    <Badge tone="warning">{t('pages.plugins.github_missing_sha')}</Badge>
                  ) : (
                    <Mono>{shortSha(item.sha256)}</Mono>
                  )}
                </td>
              </tr>
            ))}
          </Table>
          {preview.warnings.length > 0 && (
            <ul className={styles.warnings}>
              {preview.warnings.map((warning) => (
                <li key={warning}>{warning}</li>
              ))}
            </ul>
          )}
        </>
      )}
    </Modal>
  )
}