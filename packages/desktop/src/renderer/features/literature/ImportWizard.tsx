/**
 * Zotero import wizard: connection → library/collection → items → preview →
 * serial job → per-entry outcomes. Preview reads Zotero only; nothing is
 * written until the user confirms on the preview step. The job is polled
 * through `literature.import.report`, so the dialog shows the same per-entry
 * result the import service recorded (never a guessed one).
 */

import { Fragment, useEffect, useMemo, useState } from 'react'

import type {
  ImportOutcomeStatus,
  ImportPreview,
  ImportReport,
  ZoteroCollection,
  ZoteroItem,
  ZoteroLibrary,
  ZoteroStatus
} from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  IconButton,
  ProgressBar,
  Modal,
  Skeleton,
  TextInput
} from '../../design/components'
import { iconFor } from '../../app/icons'
import { useT } from '../../i18n'
import { literatureApi } from './api'
import styles from './literature.module.css'

type Step = 'connection' | 'choose' | 'items' | 'preview' | 'running' | 'result'

interface Source {
  readonly libraryId: string
  readonly libraryName: string
  readonly collectionKey: string | null
  readonly collectionName: string
}

const STEP_ORDER = ['connection', 'choose', 'items', 'preview'] as const
const PAGE_SIZE = 50
const REPORT_INTERVAL_MS = 350
const FolderIcon = iconFor('folderOpen')

const OUTCOME_KEY: Readonly<Record<ImportOutcomeStatus, string>> = {
  imported: 'pages.literature.status_imported',
  reused: 'pages.literature.outcome_reused',
  skipped: 'pages.literature.outcome_skipped',
  needs_attachment: 'pages.literature.status_needs_choice',
  failed: 'pages.literature.status_failed'
}

function sleep(ms: number): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>()
  setTimeout(resolve, ms)
  return promise
}

export interface ImportWizardProps {
  readonly open: boolean
  readonly projectId: string
  readonly onClose: () => void
  readonly onImported: () => void
}

export function ImportWizard({ open, projectId, onClose, onImported }: ImportWizardProps): JSX.Element {
  const t = useT()
  const [step, setStep] = useState<Step>('connection')
  const [status, setStatus] = useState<ZoteroStatus | null>(null)
  const [statusLoading, setStatusLoading] = useState(false)
  const [statusError, setStatusError] = useState<string | null>(null)

  const [libraries, setLibraries] = useState<readonly ZoteroLibrary[]>([])
  const [librariesLoading, setLibrariesLoading] = useState(false)
  const [librariesError, setLibrariesError] = useState<string | null>(null)
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set())
  const [collections, setCollections] = useState<Record<string, readonly ZoteroCollection[]>>({})
  const [source, setSource] = useState<Source | null>(null)

  const [items, setItems] = useState<readonly ZoteroItem[]>([])
  const [itemsTotal, setItemsTotal] = useState(0)
  const [itemsOffset, setItemsOffset] = useState(0)
  const [itemsQuery, setItemsQuery] = useState('')
  const [itemsSearch, setItemsSearch] = useState('')
  const [itemsLoading, setItemsLoading] = useState(false)
  const [itemsError, setItemsError] = useState<string | null>(null)
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set())

  const [preview, setPreview] = useState<ImportPreview | null>(null)
  const [previewLoading, setPreviewLoading] = useState(false)
  const [previewError, setPreviewError] = useState<string | null>(null)

  const [report, setReport] = useState<ImportReport | null>(null)
  const [runError, setRunError] = useState<string | null>(null)
  const [jobId, setJobId] = useState<string | null>(null)
  const [statusToken, setStatusToken] = useState(0)

  // Reset everything when the dialog closes; a reopened wizard never inherits
  // a stale preview or a running job's state.
  useEffect(() => {
    if (open) return
    setStep('connection')
    setStatus(null)
    setStatusError(null)
    setLibraries([])
    setLibrariesError(null)
    setExpanded(new Set())
    setCollections({})
    setSource(null)
    setItems([])
    setItemsTotal(0)
    setItemsOffset(0)
    setItemsQuery('')
    setItemsSearch('')
    setSelected(new Set())
    setPreview(null)
    setPreviewError(null)
    setReport(null)
    setRunError(null)
    setJobId(null)
  }, [open])

  useEffect(() => {
    const timer = window.setTimeout(() => setItemsSearch(itemsQuery), 250)
    return () => window.clearTimeout(timer)
  }, [itemsQuery])

  useEffect(() => {
    if (!open) return
    let cancelled = false
    setStatusLoading(true)
    setStatusError(null)
    literatureApi
      .zoteroStatus(true)
      .then((next) => {
        if (!cancelled) setStatus(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setStatusError(cause instanceof Error ? cause.message : String(cause))
      })
      .finally(() => {
        if (!cancelled) setStatusLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [open, statusToken])

  useEffect(() => {
    if (!open || (step !== 'choose' && step !== 'items')) return
    if (libraries.length > 0) return
    let cancelled = false
    setLibrariesLoading(true)
    setLibrariesError(null)
    literatureApi
      .zoteroLibraries()
      .then(async (next) => {
        // Fetch folders before publishing the libraries so a re-render caused
        // by `setLibraries` cannot cancel the in-flight collection requests.
        const loaded = await Promise.all(
          next.map(async (library) => {
            try {
              return {
                id: library.id,
                list: await literatureApi.zoteroCollections(library.id),
                error: null as string | null
              }
            } catch (cause) {
              return {
                id: library.id,
                list: [] as readonly ZoteroCollection[],
                error: cause instanceof Error ? cause.message : String(cause)
              }
            }
          })
        )
        if (cancelled) return
        setLibraries(next)
        setCollections(Object.fromEntries(loaded.map((entry) => [entry.id, entry.list])))
        const failure = loaded.find((entry) => entry.error !== null)
        if (failure?.error != null) setLibrariesError(failure.error)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setLibrariesError(cause instanceof Error ? cause.message : String(cause))
      })
      .finally(() => {
        if (!cancelled) setLibrariesLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [open, step, libraries.length])

  useEffect(() => {
    if (!open || step !== 'items' || source === null) return
    let cancelled = false
    setItemsLoading(true)
    setItemsError(null)
    literatureApi
      .zoteroItems({
        libraryId: source.libraryId,
        ...(source.collectionKey === null ? {} : { collectionKey: source.collectionKey }),
        ...(itemsSearch === '' ? {} : { query: itemsSearch }),
        limit: PAGE_SIZE,
        offset: itemsOffset
      })
      .then((page) => {
        if (cancelled) return
        setItems(page.items)
        setItemsTotal(page.total)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setItemsError(cause instanceof Error ? cause.message : String(cause))
      })
      .finally(() => {
        if (!cancelled) setItemsLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [open, step, source, itemsSearch, itemsOffset])

  const pick = (next: Source): void => {
    setSource(next)
    setSelected(new Set())
    setItems([])
    setItemsTotal(0)
    setItemsOffset(0)
  }

  const goPreview = async (): Promise<void> => {
    if (source === null || selected.size === 0) return
    setStep('preview')
    setPreviewLoading(true)
    setPreviewError(null)
    setPreview(null)
    try {
      const next = await literatureApi.previewImport(projectId, {
        kind: 'items',
        libraryId: source.libraryId,
        itemKeys: [...selected]
      })
      setPreview(next)
    } catch (cause) {
      setPreviewError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setPreviewLoading(false)
    }
  }

  const startImport = async (): Promise<void> => {
    if (source === null || selected.size === 0) return
    setStep('running')
    setRunError(null)
    setReport(null)
    try {
      const started = await literatureApi.startImport(projectId, {
        kind: 'items',
        libraryId: source.libraryId,
        itemKeys: [...selected]
      })
      setJobId(started.jobId)
      for (;;) {
        const next = await literatureApi.importReport(started.jobId)
        setReport(next)
        if (next.state === 'succeeded' || next.state === 'failed' || next.state === 'cancelled') {
          break
        }
        await sleep(REPORT_INTERVAL_MS)
      }
      onImported()
    } catch (cause) {
      setRunError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setStep('result')
    }
  }

  const selectedCount = selected.size
  const canContinue =
    step === 'connection'
      ? status !== null && status.available
      : step === 'choose'
        ? source !== null
        : step === 'items'
          ? selectedCount > 0 && !itemsLoading
          : step === 'preview'
            ? preview !== null && preview.importable > 0
            : false

  const stepIndex = STEP_ORDER.indexOf(step === 'result' || step === 'running' ? 'preview' : step)
  const failedOutcomes = report?.outcomes.filter((outcome) => outcome.status === 'failed') ?? []
  const totalPages = Math.max(1, Math.ceil(itemsTotal / PAGE_SIZE))
  const currentPage = Math.floor(itemsOffset / PAGE_SIZE) + 1
  const titleByKey = useMemo(() => {
    const map = new Map<string, string>()
    for (const item of items) map.set(item.key, item.title)
    return map
  }, [items])

  const itemRows = useMemo(
    () =>
      items.map((item) => {
        const checked = selected.has(item.key)
        const hasPdf = item.attachments.some((attachment) => attachment.resolvable)
        return (
          <div key={`${item.libraryId}:${item.key}`} className={styles.itemRow}>
            <input
              type="checkbox"
              className={styles.checkbox}
              aria-label={item.title || t('pages.paper.untitled')}
              checked={checked}
              onChange={() =>
                setSelected((current) => {
                  const next = new Set(current)
                  if (next.has(item.key)) next.delete(item.key)
                  else next.add(item.key)
                  return next
                })
              }
            />
            <div className={styles.itemBody}>
              <span className={styles.itemTitle}>{item.title || t('pages.paper.untitled')}</span>
              <span className={styles.subtle}>
                {[item.creators.slice(0, 3).join(', '), item.year ?? '']
                  .filter((part) => part !== '')
                  .join(' · ')}
              </span>
              <span className={styles.badges}>
                <Badge tone={hasPdf ? 'success' : 'warning'}>
                  {t(hasPdf ? 'pages.literature.col_pdf' : 'pages.literature.state_no_pdf')}
                </Badge>
                <span className={styles.subtle}>{item.itemType}</span>
              </span>
            </div>
          </div>
        )
      }),
    [items, selected, t]
  )

  const collectionTrees = useMemo(() => {
    const byLibrary: Record<string, Map<string, ZoteroCollection[]>> = {}
    for (const [libraryId, list] of Object.entries(collections)) {
      const byParent = new Map<string, ZoteroCollection[]>()
      for (const collection of list) {
        const parent = collection.parentKey ?? ''
        const bucket = byParent.get(parent) ?? []
        bucket.push(collection)
        byParent.set(parent, bucket)
      }
      for (const bucket of byParent.values()) {
        bucket.sort((left, right) => left.name.localeCompare(right.name))
      }
      byLibrary[libraryId] = byParent
    }
    return byLibrary
  }, [collections])

  const toggleCollection = (id: string): void => {
    setExpanded((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const renderCollectionNodes = (
    library: ZoteroLibrary,
    parentKey: string,
    depth: number
  ): JSX.Element[] => {
    const nodes = collectionTrees[library.id]?.get(parentKey) ?? []
    return nodes.map((collection) => {
      const nodeId = `${library.id}:${collection.key}`
      const children = collectionTrees[library.id]?.get(collection.key) ?? []
      const open = expanded.has(nodeId)
      const selected =
        source?.libraryId === library.id && source.collectionKey === collection.key
      return (
        <Fragment key={nodeId}>
          <div
            className={styles.collectionRow}
            data-selected={selected ? 'true' : 'false'}
            style={depth === 0 ? undefined : { paddingLeft: `${depth * 18}px` }}
          >
            {children.length > 0 ? (
              <IconButton
                icon={iconFor(open ? 'chevronDown' : 'chevronRight')}
                label={collection.name}
                size="sm"
                active={open}
                onClick={() => toggleCollection(nodeId)}
              />
            ) : (
              <span className={styles.treeIcon} aria-hidden>
                <FolderIcon size={14} />
              </span>
            )}
            <button
              type="button"
              className={styles.pickButton}
              onClick={() =>
                pick({
                  libraryId: library.id,
                  libraryName: library.name,
                  collectionKey: collection.key,
                  collectionName: collection.name
                })
              }
            >
              {collection.name}
            </button>
            <span className={styles.subtle}>{collection.itemCount}</span>
          </div>
          {open && children.length > 0 && (
            <div className={styles.collectionChildren}>
              {renderCollectionNodes(library, collection.key, depth + 1)}
            </div>
          )}
        </Fragment>
      )
    })
  }

  const outcomeRow = (
    key: string,
    title: string,
    status: ImportOutcomeStatus,
    error: string | null,
    warnings: readonly string[]
  ): JSX.Element => (
    <li key={key} className={styles.outcomeRow}>
      <Badge tone={status === 'failed' ? 'danger' : status === 'imported' || status === 'reused' ? 'success' : 'neutral'}>
        {t(OUTCOME_KEY[status])}
      </Badge>
      <span className={styles.outcomeTitle}>{title || t('pages.paper.untitled')}</span>
      {error !== null && <span className={styles.subtle}>{error}</span>}
      {warnings.length > 0 && <span className={styles.subtle}>{warnings.join(' · ')}</span>}
    </li>
  )

  const body = ((): JSX.Element => {
    if (step === 'connection') {
      if (statusLoading && status === null) return <Skeleton rows={5} />
      return (
        <div className={styles.stack}>
          {statusError !== null && (
            <Banner tone="danger" title={t('pages.literature.import_status_fail')}>
              {statusError}
            </Banner>
          )}
          {status !== null && (
            <>
              <div className={styles.badges}>
                <Badge tone={status.available ? 'success' : 'warning'}>
                  {t(status.available ? 'pages.literature.import_available' : 'pages.literature.import_unavailable')}
                </Badge>
                <span className={styles.subtle}>{status.mode}</span>
              </div>
              {status.dataDir !== null && (
                <p className={styles.subtle}>
                  {t('pages.literature.import_data_dir')}: <span className={styles.mono}>{status.dataDir}</span>
                </p>
              )}
              {status.lastError !== null && (
                <Banner tone="warning" title={t('pages.literature.import_last_error')}>
                  {status.lastError}
                </Banner>
              )}
              {!status.available && <p>{t('pages.literature.import_unavailable_reason')}</p>}
              <div>
                <Button
                  variant="secondary"
                  size="sm"
                  icon={iconFor('refresh')}
                  onClick={() => setStatusToken((token) => token + 1)}
                >
                  {t('pages.literature.btn_refresh')}
                </Button>
              </div>
            </>
          )}
        </div>
      )
    }

    if (step === 'choose') {
      if (librariesLoading && libraries.length === 0) return <Skeleton rows={4} />
      return (
        <div className={styles.stack}>
          <p className={styles.subtle}>{t('pages.literature.import_choose_hint')}</p>
          {librariesError !== null && (
            <Banner tone="danger" title={t('pages.literature.import_libraries_fail')}>
              {librariesError}
            </Banner>
          )}
          {libraries.length === 0 && !librariesLoading ? (
            <Banner tone="warning">{t('pages.literature.import_no_libraries')}</Banner>
          ) : (
            libraries.map((library) => {
              const list = collections[library.id]
              const librarySelected =
                source?.libraryId === library.id && source.collectionKey === null
              const nodes = collectionTrees[library.id]?.get('') ?? []
              return (
                <div key={library.id} className={styles.section}>
                  <div
                    className={styles.collectionRow}
                    data-selected={librarySelected ? 'true' : 'false'}
                  >
                    <span className={styles.treeIcon} aria-hidden>
                      <FolderIcon size={16} />
                    </span>
                    <button
                      type="button"
                      className={styles.pickButton}
                      onClick={() =>
                        pick({
                          libraryId: library.id,
                          libraryName: library.name,
                          collectionKey: null,
                          collectionName: t('pages.literature.collection_all')
                        })
                      }
                    >
                      {library.name}
                      <span className={styles.subtle}> · {t('pages.literature.collection_all')}</span>
                    </button>
                    <Badge tone="neutral">{library.type}</Badge>
                  </div>
                  <div className={styles.collections}>
                    {list === undefined ? (
                      <p className={styles.subtle}>{t('pages.literature.import_loading_collections')}</p>
                    ) : nodes.length === 0 ? (
                      <p className={styles.subtle}>{t('pages.literature.import_no_collections')}</p>
                    ) : (
                      renderCollectionNodes(library, '', 0)
                    )}
                  </div>
                </div>
              )
            })
          )}
          {source !== null && (
            <p className={styles.subtle}>
              {t('pages.literature.import_source', {
                library: source.libraryName,
                collection: source.collectionName
              })}
            </p>
          )}
        </div>
      )
    }

    if (step === 'items') {
      return (
        <div className={styles.stack}>
          {source !== null && (
            <p className={styles.subtle}>
              {t('pages.literature.import_source', {
                library: source.libraryName,
                collection: source.collectionName
              })}
            </p>
          )}
          {itemsError !== null && (
            <Banner tone="danger" title={t('pages.literature.import_items_fail')}>
              {itemsError}
            </Banner>
          )}
          <div className={styles.toolbar}>
            <div className={styles.grow}>
              <TextInput
                label={t('pages.literature.field_search')}
                hideLabel
                value={itemsQuery}
                placeholder={t('pages.literature.field_search_hint')}
                onChange={(value) => {
                  setItemsQuery(value)
                  setItemsOffset(0)
                }}
              />
            </div>
            <span className={styles.subtle}>
              {t('pages.literature.import_selected_count', { count: selectedCount })}
            </span>
            <Button
              variant="secondary"
              size="sm"
              onClick={() =>
                setSelected((current) => {
                  const next = new Set(current)
                  for (const item of items) next.add(item.key)
                  return next
                })
              }
            >
              {t('pages.literature.select_all_loaded')}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setSelected(new Set())}>
              {t('pages.literature.btn_clear_selection')}
            </Button>
          </div>
          {itemsLoading && items.length === 0 ? (
            <Skeleton rows={6} />
          ) : items.length === 0 ? (
            <Banner tone="info">{t('pages.literature.items_empty_reason')}</Banner>
          ) : (
            <div className={styles.itemList}>{itemRows}</div>
          )}
          {totalPages > 1 && (
            <div className={styles.footerRow}>
              <Button
                variant="secondary"
                size="sm"
                disabled={itemsOffset === 0 || itemsLoading}
                onClick={() => setItemsOffset(Math.max(0, itemsOffset - PAGE_SIZE))}
              >
                {t('pages.literature.btn_prev')}
              </Button>
              <span className={styles.subtle}>
                {t('pages.literature.import_page_of', { page: currentPage, total: totalPages })}
              </span>
              <Button
                variant="secondary"
                size="sm"
                disabled={currentPage >= totalPages || itemsLoading}
                onClick={() => setItemsOffset(itemsOffset + PAGE_SIZE)}
              >
                {t('pages.literature.btn_next')}
              </Button>
            </div>
          )}
        </div>
      )
    }

    if (step === 'preview') {
      if (previewLoading) return <Skeleton rows={8} />
      if (previewError !== null) {
        return (
          <Banner tone="danger" title={t('pages.literature.import_preview_fail')}>
            {previewError}
          </Banner>
        )
      }
      if (preview === null) return <Skeleton rows={4} />
      return (
        <div className={styles.stack}>
          <p className={styles.subtle}>{t('pages.literature.dialog_desc')}</p>
          <div className={styles.summaryRow}>
            <span>{t('pages.literature.import_preview_summary', {
              importable: preview.importable,
              already: preview.alreadyPresent,
              missing: preview.missingPdf
            })}</span>
          </div>
          {preview.errors.length > 0 && (
            <Banner tone="warning" title={t('pages.literature.preview_errors')}>
              <ul className={styles.outcomeList}>
                {preview.errors.map((error) => (
                  <li key={error.key} className={styles.outcomeRow}>
                    <span className={styles.outcomeTitle}>{error.key}</span>
                    <span className={styles.subtle}>{error.message}</span>
                  </li>
                ))}
              </ul>
            </Banner>
          )}
          {preview.entries.length === 0 ? (
            <Banner tone="info">{t('pages.literature.preview_empty_reason')}</Banner>
          ) : (
            <table className={styles.previewTable} aria-label={t('pages.literature.dialog_title')}>
              <thead>
                <tr>
                  <th>{t('pages.literature.col_title')}</th>
                  <th>{t('pages.literature.col_year')}</th>
                  <th>{t('pages.literature.col_state')}</th>
                  <th>{t('pages.literature.col_pdf')}</th>
                </tr>
              </thead>
              <tbody>
                {preview.entries.map((entry) => (
                  <tr key={`${entry.libraryId}:${entry.key}`}>
                    <td>{entry.title || t('pages.paper.untitled')}</td>
                    <td>{entry.year ?? ''}</td>
                    <td>
                      {t(
                        entry.alreadyImported
                          ? 'pages.literature.state_existing'
                          : 'pages.literature.state_new'
                      )}
                    </td>
                    <td>
                      {t(
                        entry.pdfFound
                          ? 'pages.literature.col_pdf'
                          : 'pages.literature.state_no_pdf'
                      )}
                      {entry.warnings.length > 0 && (
                        <span className={styles.subtle}> · {entry.warnings.join(' · ')}</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )
    }

    if (step === 'running') {
      const progress = report?.progress ?? 0
      return (
        <div className={styles.stack}>
          <p>{t('pages.literature.import_running')}</p>
          <ProgressBar value={progress} label={t('pages.literature.import_progress')} />
          <span className={styles.subtle}>
            {jobId === null ? '' : t('pages.literature.import_progress_count', { percent: Math.round(progress) })}
          </span>
          {report !== null && report.outcomes.length > 0 && (
            <ul className={styles.outcomeList}>
              {report.outcomes.map((outcome) =>
                outcomeRow(
                  `${outcome.libraryId}:${outcome.key}`,
                  titleByKey.get(outcome.key) ?? outcome.key,
                  outcome.status,
                  outcome.error,
                  outcome.warnings
                )
              )}
            </ul>
          )}
        </div>
      )
    }

    const failedCount = failedOutcomes.length
    return (
      <div className={styles.stack}>
        {runError !== null && (
          <Banner tone="danger" title={t('pages.literature.toast_import_fail')}>
            {runError}
          </Banner>
        )}
        {report?.error !== null && report?.error !== undefined && (
          <Banner tone="danger" title={t('pages.literature.toast_import_fail')}>
            {report.error}
          </Banner>
        )}
        <Banner
          tone={failedCount === 0 ? 'success' : 'warning'}
          title={t('pages.literature.result_title')}
        >
          {report === null
            ? ''
            : t('pages.literature.result_counts', {
                imported: report.imported,
                linked: report.reused,
                skipped: report.skipped,
                failed: report.failed
              })}
        </Banner>
        {failedCount > 0 && (
          <h3 className={styles.sectionTitle}>
            {t('pages.literature.result_failed_title', { failed: failedCount })}
          </h3>
        )}
        {report !== null && report.outcomes.length > 0 && (
          <ul className={styles.outcomeList}>
            {report.outcomes.map((outcome) =>
              outcomeRow(
                `${outcome.libraryId}:${outcome.key}`,
                titleByKey.get(outcome.key) ?? outcome.key,
                outcome.status,
                outcome.error,
                outcome.warnings
              )
            )}
          </ul>
        )}
      </div>
    )
  })()

  return (
    <Modal
      open={open}
      title={t('pages.literature.import_title')}
      width={880}
      escapeCloses={step !== 'running'}
      onClose={() => {
        if (step === 'running') return
        onClose()
      }}
      footer={
        step === 'result' ? (
          <div className={styles.footerRow}>
            <Button
              variant="secondary"
              onClick={() => {
                setStep('choose')
                setReport(null)
                setPreview(null)
                setSelected(new Set())
                setJobId(null)
              }}
            >
              {t('pages.literature.import_again')}
            </Button>
            <Button variant="primary" onClick={onClose}>
              {t('pages.literature.btn_close')}
            </Button>
          </div>
        ) : step === 'running' ? (
          <div className={styles.footerRow}>
            <span className={styles.subtle}>{t('pages.literature.import_running')}</span>
          </div>
        ) : (
          <div className={styles.footerRow}>
            <Button
              variant="secondary"
              onClick={() => {
                setStep('connection')
                onClose()
              }}
            >
              {t('pages.literature.btn_cancel')}
            </Button>
            {step !== 'connection' && (
              <Button
                variant="secondary"
                onClick={() => {
                  if (step === 'items') setStep('choose')
                  else if (step === 'preview') setStep('items')
                  else if (step === 'choose') setStep('connection')
                }}
              >
                {t('pages.literature.btn_back')}
              </Button>
            )}
            {step === 'preview' ? (
              <Button
                variant="primary"
                disabled={!canContinue || previewLoading}
                onClick={() => void startImport()}
              >
                {t('pages.literature.btn_import_count', { count: preview?.importable ?? 0 })}
              </Button>
            ) : (
              <Button
                variant="primary"
                disabled={!canContinue}
                onClick={() => {
                  if (step === 'connection') setStep('choose')
                  else if (step === 'choose') {
                    setItemsOffset(0)
                    setItemsQuery('')
                    setItemsSearch('')
                    setStep('items')
                  } else if (step === 'items') void goPreview()
                }}
              >
                {t('pages.literature.btn_next')}
              </Button>
            )}
          </div>
        )
      }
    >
      {step !== 'running' && step !== 'result' && (
        <ol className={styles.steps} aria-label={t('pages.literature.import_steps')}>
          {STEP_ORDER.map((entry, index) => (
            <li
              key={entry}
              className={[
                styles.step,
                entry === step ? styles.stepActive : '',
                index < stepIndex ? styles.stepDone : ''
              ].join(' ')}
            >
              <span className={styles.stepIndex}>{index + 1}</span>
              {t(`pages.literature.import_step_${entry}`)}
            </li>
          ))}
        </ol>
      )}
      {body}
    </Modal>
  )
}