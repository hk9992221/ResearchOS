import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Copy } from 'lucide-react'

import type {
  JsonObject,
  McpClient,
  McpConfirmation,
  McpConfirmationState,
  McpConnectionReport
} from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  Card,
  ConfirmDialog,
  CopyChip,
  ErrorState,
  IconButton,
  Modal,
  Row,
  Rows,
  Segmented,
  SelectField,
  Skeleton,
  SwitchField,
  Table,
  TextInput
} from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import styles from './McpTab.module.css'

interface TokenOnce {
  readonly name: string
  readonly token: string
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

/** Selectable code sample with an inline copy button. */
function CodeBlock({
  value,
  copyLabel,
  copiedLabel
}: {
  readonly value: string
  readonly copyLabel: string
  readonly copiedLabel: string
}): JSX.Element {
  const [copied, setCopied] = useState(false)
  const copy = async (): Promise<void> => {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard unavailable: the text stays selectable.
    }
  }
  return (
    <div className={styles.codeBlock}>
      <pre>{value}</pre>
      <IconButton
        icon={copied ? Check : Copy}
        label={copied ? copiedLabel : copyLabel}
        size="sm"
        variant="ghost"
        onClick={() => void copy()}
      />
    </div>
  )
}

/** Sample Bearer value: loopback accepts any key, so examples ship a fixed one. */
const EXAMPLE_TOKEN = 'researchos'

/** Copy-ready snippets for the 接入 section, rendered per selected tab. */
function buildExample(tab: string, endpoint: string): string {
  const auth = `Bearer ${EXAMPLE_TOKEN}`
  const body =
    '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"example","version":"1.0"}}}'
  if (tab === 'powershell') {
    return [
      `Invoke-RestMethod -Method Post -Uri "${endpoint}" \``,
      `  -Headers @{ Authorization = "${auth}"; Accept = "application/json, text/event-stream" } \``,
      `  -ContentType "application/json" \``,
      `  -Body '${body}'`
    ].join('\n')
  }
  if (tab === 'config') {
    return [
      '{',
      '  "mcpServers": {',
      '    "researchos": {',
      '      "type": "http",',
      `      "url": "${endpoint}",`,
      `      "headers": { "Authorization": "${auth}" }`,
      '    }',
      '  }',
      '}'
    ].join('\n')
  }
  return [
    `curl -X POST "${endpoint}" \\`,
    `  -H "${auth}" \\`,
    '  -H "Content-Type: application/json" \\',
    '  -H "Accept: application/json, text/event-stream" \\',
    `  -d '${body}'`
  ].join('\n')
}

function stateTone(state: McpConfirmationState): 'success' | 'danger' | 'warning' | 'neutral' {
  switch (state) {
    case 'executed':
      return 'success'
    case 'failed':
      return 'danger'
    case 'requested':
    case 'approved':
    case 'executing':
      return 'warning'
    default:
      return 'neutral'
  }
}

function remainingMinutes(expiresAt: string, now: number): number {
  return Math.max(0, Math.ceil((Date.parse(expiresAt) - now) / 60_000))
}

function resultCount(result: JsonObject, field: string): number {
  const value = result[field]
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

/** Human summary for a settled effect; the raw JSON stays behind `<details>`. */
function resultSummary(
  tool: string,
  result: JsonObject,
  t: (key: string, vars?: Record<string, string | number>) => string
): string {
  switch (tool) {
    case 'trash_empty':
      return t('pages.settings.mcp.result_trash_empty', {
        purged: resultCount(result, 'purged'),
        files: resultCount(result, 'removed_files')
      })
    case 'column_delete':
      return t('pages.settings.mcp.result_column_delete', {
        moved: resultCount(result, 'moved_tasks')
      })
    default:
      return t('pages.settings.mcp.result_generic', { n: Object.keys(result).length })
  }
}

/** MCP section: server, access mode, tokens, confirmations, catalog, probe. */
export function McpTab(): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()

  const statusQuery = useQuery({
    queryKey: queryKeys.mcp.status,
    queryFn: () => ipc.invoke('mcp.status')
  })
  const clientsQuery = useQuery({
    queryKey: ['mcp', 'clients'],
    queryFn: () => ipc.invoke('mcp.clients.list')
  })
  const toolsQuery = useQuery({
    queryKey: ['mcp', 'tools'],
    queryFn: () => ipc.invoke('mcp.tools.list')
  })
  const probeQuery = useQuery({
    queryKey: ['mcp', 'probe'],
    queryFn: () => ipc.invoke('mcp.probe')
  })
  const retentionQuery = useQuery({
    queryKey: ['mcp', 'retention'],
    queryFn: () => ipc.invoke('mcp.retention.get')
  })

  const [accessMode, setAccessMode] = useState<'read-only' | 'read-write'>('read-only')
  const [httpEnabled, setHttpEnabled] = useState(false)
  const [port, setPort] = useState('39217')
  const [exampleTab, setExampleTab] = useState('curl')
  const [accessOpen, setAccessOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [clientDialog, setClientDialog] = useState(false)
  const [clientName, setClientName] = useState('')
  const [clientError, setClientError] = useState<string | null>(null)
  const [tokenOnce, setTokenOnce] = useState<TokenOnce | null>(null)
  const [revoking, setRevoking] = useState<McpClient | null>(null)
  const [showHistory, setShowHistory] = useState(false)
  const [confirming, setConfirming] = useState<McpConfirmation | null>(null)
  const [approveNote, setApproveNote] = useState('')
  const [retentionDays, setRetentionDays] = useState('30')
  const [retentionError, setRetentionError] = useState<string | null>(null)
  const [now, setNow] = useState(() => Date.now())

  const confirmationsQuery = useQuery({
    queryKey: queryKeys.mcp.confirmations(),
    queryFn: () => ipc.invoke('mcp.confirmations.list', {}),
    // External MCP clients enqueue rows without a renderer event; poll the
    // queue so the badge and list stay honest while the tab is open.
    refetchOnMount: 'always',
    refetchInterval: 5000
  })

  const status = statusQuery.data

  useEffect(() => {
    if (status === undefined) return
    setAccessMode(status.accessMode)
    setHttpEnabled(status.httpEnabled)
    setPort(String(status.port))
  }, [status])

  useEffect(() => {
    const data = retentionQuery.data
    if (data !== undefined) setRetentionDays(String(data.days))
  }, [retentionQuery.data])

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [])

  const refresh = async (): Promise<void> => {
    await queryClient.invalidateQueries({ queryKey: queryKeys.mcp.all })
  }

  const act = async (run: () => Promise<unknown>, done: string): Promise<void> => {
    setBusy(true)
    setActionError(null)
    setNotice(null)
    try {
      await run()
      await refresh()
      setNotice(done)
    } catch (cause) {
      setActionError(cause)
    } finally {
      setBusy(false)
    }
  }

  const approve = async (): Promise<void> => {
    if (confirming === null) return
    const target = confirming
    const note = approveNote.trim()
    setBusy(true)
    setActionError(null)
    setNotice(null)
    try {
      await ipc.invoke('mcp.confirmations.approve', {
        id: target.id,
        ...(note === '' ? {} : { note })
      })
      setConfirming(null)
      setApproveNote('')
      await refresh()
      setNotice(t('pages.settings.mcp.action_done'))
    } catch (cause) {
      setActionError(cause)
    } finally {
      setBusy(false)
    }
  }

  const createClient = async (): Promise<void> => {
    setClientError(null)
    if (clientName.trim() === '') {
      setClientError(t('pages.settings.mcp.error.client_name'))
      return
    }
    setBusy(true)
    try {
      const result = await ipc.invoke('mcp.clients.create', { name: clientName.trim() })
      setTokenOnce({ name: result.client.name, token: result.token })
      setClientDialog(false)
      setClientName('')
      await queryClient.invalidateQueries({ queryKey: ['mcp', 'clients'] })
    } catch (cause) {
      setClientError(t('pages.settings.mcp.client_create_failed'))
      setActionError(cause)
    } finally {
      setBusy(false)
    }
  }

  const copyToken = async (): Promise<void> => {
    if (tokenOnce === null) return
    try {
      await navigator.clipboard.writeText(tokenOnce.token)
      setNotice(t('pages.settings.mcp.toast_copied_title'))
    } catch (cause) {
      setActionError(cause)
    }
  }

  const saveRetention = async (): Promise<void> => {
    const parsed = Number(retentionDays)
    if (!Number.isInteger(parsed) || parsed < 1 || parsed > 3650) {
      setRetentionError(t('pages.settings.mcp.error.retention'))
      return
    }
    setRetentionError(null)
    await act(() => ipc.invoke('mcp.retention.set', { days: parsed }), t('pages.settings.mcp.saved'))
  }

  if (statusQuery.isPending) return <Skeleton rows={5} />
  if (statusQuery.isError || status === undefined) {
    return (
      <ErrorState
        title={t('pages.settings.mcp.unavailable_title')}
        message={statusQuery.error?.message ?? t('pages.settings.mcp.unavailable_message')}
        retryLabel={t('shell.overview.error_retry')}
        onRetry={() => void statusQuery.refetch()}
      />
    )
  }

  const clients = clientsQuery.data ?? []
  const tools = toolsQuery.data ?? []
  const confirmations = confirmationsQuery.data ?? []
  const pendingConfirmations = confirmations.filter((row) => row.state === 'requested')
  const visibleConfirmations = showHistory ? confirmations : pendingConfirmations
  const probe: McpConnectionReport | undefined = probeQuery.data
  const statusLabel = !status.enabled
    ? t('pages.settings.mcp.status_disabled')
    : status.running
      ? t('pages.settings.mcp.status_running')
      : status.lastError === null
        ? t('pages.settings.mcp.status_stopped')
        : t('pages.settings.mcp.status_failed')

  const scope = status.host === '0.0.0.0' || status.host === '::' ? 'lan' : 'local'
  const endpointHost =
    scope === 'lan' ? (status.lanAddresses ?? [])[0] ?? '127.0.0.1' : '127.0.0.1'
  const endpointUrl = `http://${endpointHost}:${status.port}${status.path}`

  const applyScope = async (lan: boolean): Promise<void> => {
    const host = lan ? '0.0.0.0' : '127.0.0.1'
    if (host === status.host) return
    await act(() => ipc.invoke('mcp.settings.update', { host }), t('pages.settings.mcp.saved'))
  }

  const commitPort = async (): Promise<void> => {
    const parsed = Number.parseInt(port, 10)
    if (!Number.isInteger(parsed) || parsed < 1 || parsed > 65535) {
      setActionError(new Error(t('pages.settings.mcp.error.port')))
      return
    }
    if (parsed === status.port) return
    await act(
      () => ipc.invoke('mcp.settings.update', { port: parsed }),
      t('pages.settings.mcp.saved')
    )
  }

  return (
    <>
      {notice !== null && <Banner tone="success">{notice}</Banner>}
      {actionError !== null && (
        <Banner tone="danger" title={t('pages.settings.mcp.action_failed')}>
          {errorText(actionError)}
        </Banner>
      )}

      <Card title={t('pages.settings.mcp.card_server')}>
        <Rows>
          <Row label={t('pages.settings.mcp.enable_label')} value={statusLabel} />
          <Row label={t('pages.settings.mcp.row_tools')} value={String(status.toolsCount)} />
          <Row label={t('pages.settings.mcp.row_protocols')} value="Streamable HTTP" />
        </Rows>
        {status.lastError !== null && <Banner tone="danger">{status.lastError}</Banner>}
        <div>
          {status.running ? (
            <Button
              variant="secondary"
              size="sm"
              disabled={busy}
              onClick={() => void act(() => ipc.invoke('mcp.stop'), t('pages.settings.mcp.saved'))}
            >
              {t('pages.settings.mcp.btn_disable')}
            </Button>
          ) : (
            <Button
              variant="primary"
              size="sm"
              disabled={busy}
              onClick={() => void act(() => ipc.invoke('mcp.start'), t('pages.settings.mcp.saved'))}
            >
              {t('pages.settings.mcp.btn_enable')}
            </Button>
          )}
        </div>
        <SelectField
          label={t('pages.settings.mcp.access_label')}
          value={accessMode}
          options={[
            { value: 'read-only', label: t('pages.settings.mcp.access_read_only') },
            { value: 'read-write', label: t('pages.settings.mcp.access_read_write') }
          ]}
          onChange={(value) => setAccessMode(value === 'read-write' ? 'read-write' : 'read-only')}
          hint={t('pages.settings.mcp.access_hint')}
        />
        <div>
          <Button
            variant="secondary"
            size="sm"
            disabled={busy}
            onClick={() =>
              void act(
                () => ipc.invoke('mcp.settings.update', { accessMode }),
                t('pages.settings.mcp.saved')
              )
            }
          >
            {t('pages.settings.mcp.btn_save_access')}
          </Button>
        </div>
      </Card>

      <Card title={t('pages.settings.mcp.card_http')}>
        <SwitchField
          label={t('pages.settings.mcp.http_enable_label')}
          checked={httpEnabled}
          onChange={(value) => {
            setHttpEnabled(value)
            void act(
              () => ipc.invoke('mcp.settings.update', { httpEnabled: value }),
              t('pages.settings.mcp.saved')
            )
          }}
        />
        <div className={styles.scopeRow}>
          <span className={styles.scopeLabel}>{t('pages.settings.mcp.http_scope_label')}</span>
          <Segmented
            label={t('pages.settings.mcp.http_scope_label')}
            value={scope}
            items={[
              { id: 'local', label: t('pages.settings.mcp.scope_local') },
              { id: 'lan', label: t('pages.settings.mcp.scope_lan') }
            ]}
            onChange={(id) => void applyScope(id === 'lan')}
          />
        </div>
        {httpEnabled && (
          <Rows>
            <Row
              label={t('pages.settings.mcp.scope_local')}
              value={
                <CopyChip
                  value={`http://127.0.0.1:${status.port}${status.path}`}
                  copyLabel={t('pages.settings.mcp.copy')}
                  copiedLabel={t('pages.settings.mcp.toast_copied_title')}
                />
              }
            />
            {scope === 'lan' &&
              (status.lanAddresses ?? []).map((address) => (
                <Row
                  key={address}
                  label={address}
                  value={
                    <CopyChip
                      value={`http://${address}:${status.port}${status.path}`}
                      copyLabel={t('pages.settings.mcp.copy')}
                      copiedLabel={t('pages.settings.mcp.toast_copied_title')}
                    />
                  }
                />
              ))}
          </Rows>
        )}
        <TextInput
          label={t('pages.settings.mcp.http_port_label')}
          value={port}
          onChange={setPort}
          inputMode="numeric"
          spellCheck={false}
          onBlur={() => void commitPort()}
          onKeyDown={(event) => {
            if (event.key === 'Enter') void commitPort()
          }}
        />
        <div className={styles.access}>
          <button
            type="button"
            className={styles.accessToggle}
            aria-expanded={accessOpen}
            onClick={() => setAccessOpen((value) => !value)}
          >
            <span className={styles.accessChevron} data-open={accessOpen} aria-hidden />
            <span>{t('pages.settings.mcp.access_title')}</span>
            <span className={styles.accessNote}>
              {scope === 'lan'
                ? t('pages.settings.mcp.access_note_lan')
                : t('pages.settings.mcp.access_note_local')}
            </span>
          </button>
          {accessOpen && (
            <Rows>
            <Row
              label={t('pages.settings.mcp.http_endpoint_label')}
              value={
                <CopyChip
                  value={endpointUrl}
                  copyLabel={t('pages.settings.mcp.copy')}
                  copiedLabel={t('pages.settings.mcp.toast_copied_title')}
                />
              }
            />
            <Row
              label={t('pages.settings.mcp.access_header_label')}
              value={
                <CopyChip
                  value={`Authorization: Bearer ${EXAMPLE_TOKEN}`}
                  copyLabel={t('pages.settings.mcp.copy')}
                  copiedLabel={t('pages.settings.mcp.toast_copied_title')}
                />
              }
            />
            <p className={styles.accessHint}>{t('pages.settings.mcp.access_header_hint')}</p>
            <Row
              label={t('pages.settings.mcp.access_example_label')}
              value={
                <div className={styles.example}>
                  <Segmented
                    label={t('pages.settings.mcp.access_example_label')}
                    value={exampleTab}
                    items={[
                      { id: 'curl', label: 'curl' },
                      { id: 'powershell', label: 'PowerShell' },
                      { id: 'config', label: t('pages.settings.mcp.access_example_config') }
                    ]}
                    onChange={setExampleTab}
                  />
                  <CodeBlock
                    value={buildExample(exampleTab, endpointUrl)}
                    copyLabel={t('pages.settings.mcp.copy')}
                    copiedLabel={t('pages.settings.mcp.toast_copied_title')}
                  />
                </div>
              }
            />
            </Rows>
          )}
        </div>
      </Card>

      <Card
        title={t('pages.settings.mcp.card_confirmations')}
        actions={<Badge>{t('pages.settings.mcp.confirmations_count', { n: pendingConfirmations.length })}</Badge>}
      >
        <SwitchField
          label={t('pages.settings.mcp.confirmations_history')}
          checked={showHistory}
          onChange={setShowHistory}
          description={t('pages.settings.mcp.confirmations_history_hint')}
        />
        {confirmationsQuery.isError && (
          <Banner tone="danger">{confirmationsQuery.error.message}</Banner>
        )}
        <div className={styles.tableScroll}>
        <Table
          label={t('pages.settings.mcp.card_confirmations')}
          columns={[
            t('pages.settings.mcp.col_confirmation_tool'),
            t('pages.settings.mcp.col_confirmation_project'),
            t('pages.settings.mcp.col_confirmation_summary'),
            t('pages.settings.mcp.col_confirmation_timeline'),
            t('pages.settings.mcp.col_confirmation_state'),
            t('pages.settings.mcp.col_confirmation_result'),
            t('pages.settings.mcp.col_confirmation_actions')
          ]}
          empty={
            <p>
              {showHistory
                ? t('pages.settings.mcp.confirmations_empty_history')
                : t('pages.settings.mcp.confirmations_empty')}
            </p>
          }
        >
          {visibleConfirmations.map((confirmation) => {
            const expired =
              confirmation.state === 'expired' ||
              (confirmation.state === 'requested' && Date.parse(confirmation.expiresAt) <= now)
            const minutes = remainingMinutes(confirmation.expiresAt, now)
            const actionable = confirmation.state === 'requested' && !expired
            return (
              <tr key={confirmation.id}>
                <td>
                  <div>{confirmation.title}</div>
                  <code>{confirmation.tool}</code>
                </td>
                <td>{confirmation.projectId ?? t('pages.settings.mcp.confirmation_project_none')}</td>
                <td>{confirmation.summary}</td>
                <td>
                  <div>{confirmation.createdAt}</div>
                  <div>
                    {expired
                      ? t('pages.settings.mcp.confirmation_expired')
                      : t('pages.settings.mcp.confirmation_expires_in', { n: minutes })}
                  </div>
                </td>
                <td>
                  <Badge tone={stateTone(confirmation.state)}>
                    {expired && confirmation.state === 'requested'
                      ? t('pages.settings.mcp.confirmation_expired')
                      : t(`pages.settings.mcp.state_${confirmation.state}`)}
                  </Badge>
                </td>
                <td>
                  {confirmation.note !== '' && (
                    <div className={styles.note}>
                      {t('pages.settings.mcp.confirmation_note_display')}: {confirmation.note}
                    </div>
                  )}
                  {confirmation.errorCode !== null ? (
                    <code>{confirmation.errorCode}</code>
                  ) : confirmation.result !== null ? (
                    <details className={styles.details}>
                      <summary>{resultSummary(confirmation.tool, confirmation.result, t)}</summary>
                      <pre>{JSON.stringify(confirmation.result, null, 2)}</pre>
                    </details>
                  ) : (
                    '—'
                  )}
                </td>
                <td>
                  {actionable ? (
                    <>
                      <Button
                        variant="primary"
                        size="sm"
                        disabled={busy}
                        onClick={() => {
                          setApproveNote('')
                          setConfirming(confirmation)
                        }}
                      >
                        {t('pages.settings.mcp.confirmation_btn_approve')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={busy}
                        onClick={() =>
                          void act(
                            () => ipc.invoke('mcp.confirmations.reject', { id: confirmation.id }),
                            t('pages.settings.mcp.action_done')
                          )
                        }
                      >
                        {t('pages.settings.mcp.confirmation_btn_reject')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={busy}
                        onClick={() =>
                          void act(
                            () => ipc.invoke('mcp.confirmations.cancel', { id: confirmation.id }),
                            t('pages.settings.mcp.action_done')
                          )
                        }
                      >
                        {t('pages.settings.mcp.confirmation_btn_cancel')}
                      </Button>
                    </>
                  ) : (
                    '—'
                  )}
                </td>
              </tr>
            )
          })}
        </Table>
        </div>
      </Card>

      <Card
        title={t('pages.settings.mcp.card_tools')}
        actions={<Badge>{t('pages.settings.mcp.tools_count', { n: status.toolsCount })}</Badge>}
        collapsible
      >
        {toolsQuery.isError && <Banner tone="danger">{toolsQuery.error.message}</Banner>}
        {tools.length === 0 && <p>{t('pages.settings.mcp.tools_empty')}</p>}
        {tools.length > 0 && (
          <Table
            label={t('pages.settings.mcp.card_tools')}
            columns={[
              t('pages.settings.mcp.col_tool_name'),
              t('pages.settings.mcp.col_tool_category'),
              t('pages.settings.mcp.col_tool_desc'),
              t('pages.settings.mcp.col_tool_markers')
            ]}
          >
            {tools.map((tool) => (
              <tr key={tool.name}>
                <td>{tool.title}</td>
                <td>{tool.category}</td>
                <td>{tool.description}</td>
                <td>
                  <Badge>
                    {tool.requiresConfirmation
                      ? t('pages.settings.mcp.tool_destructive')
                      : t('pages.settings.mcp.tool_read_only')}
                  </Badge>
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Card>

      <Card
        title={t('pages.settings.mcp.card_clients')}
        actions={
          <Button variant="secondary" size="sm" onClick={() => setClientDialog(true)}>
            {t('pages.settings.mcp.btn_new_token')}
          </Button>
        }
      >
        {clientsQuery.isError && <Banner tone="danger">{clientsQuery.error.message}</Banner>}
        {clients.length === 0 && <p>{t('pages.settings.mcp.clients_empty')}</p>}
        {clients.length > 0 && (
          <Table
            label={t('pages.settings.mcp.card_clients')}
            columns={[
              t('pages.settings.mcp.col_client_name'),
              t('pages.settings.mcp.col_client_created'),
              t('pages.settings.mcp.col_client_enabled'),
              t('pages.settings.mcp.col_client_actions')
            ]}
          >
            {clients.map((client) => (
              <tr key={client.id}>
                <td>{client.name}</td>
                <td>{client.createdAt}</td>
                <td>
                  {client.enabled
                    ? t('pages.settings.mcp.client_enabled')
                    : t('pages.settings.mcp.client_disabled')}
                </td>
                <td>
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={busy}
                    onClick={() =>
                      void act(
                        () =>
                          ipc.invoke('mcp.clients.update', {
                            clientId: client.id,
                            enabled: !client.enabled
                          }),
                        t('pages.settings.mcp.saved')
                      )
                    }
                  >
                    {client.enabled
                      ? t('pages.settings.mcp.btn_disable')
                      : t('pages.settings.mcp.btn_enable')}
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => setRevoking(client)}>
                    {t('pages.settings.mcp.btn_revoke')}
                  </Button>
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Card>

      <Card
        title={t('pages.settings.mcp.card_probe')}
        actions={
          <Button
            variant="secondary"
            size="sm"
            disabled={probeQuery.isFetching}
            onClick={() => void probeQuery.refetch()}
          >
            {t('pages.settings.mcp.probe_retry')}
          </Button>
        }
      >
        {probeQuery.isError && <Banner tone="danger">{probeQuery.error.message}</Banner>}
        {probe !== undefined && (
          <>
            <Rows>
              <Row
                label={t('pages.settings.mcp.probe_ipc_label')}
                value={
                  probe.ipcOk
                    ? t('pages.settings.mcp.probe_ipc_ok')
                    : t('pages.settings.mcp.probe_ipc_fail', { err: probe.ipcError })
                }
              />
              <Row
                label={t('pages.settings.mcp.probe_http_label')}
                value={
                  probe.httpOk
                    ? t('pages.settings.mcp.probe_http_ok')
                    : probe.httpEnabled
                      ? t('pages.settings.mcp.probe_http_fail', { err: probe.httpError })
                      : t('pages.settings.mcp.probe_http_off')
                }
              />
              <Row label={t('pages.settings.mcp.row_tools')} value={String(probe.toolCount)} />
              <Row label={t('pages.settings.mcp.row_version')} value={probe.version} />
            </Rows>
            <p>{t('pages.settings.mcp.probe_hint')}</p>
          </>
        )}
      </Card>

      <Card title={t('pages.settings.mcp.card_retention')}>
        <TextInput
          label={t('pages.settings.mcp.retention_label')}
          value={retentionDays}
          onChange={(value) => {
            setRetentionDays(value)
            setRetentionError(null)
          }}
          hint={t('pages.settings.mcp.retention_hint')}
          error={retentionError ?? undefined}
          inputMode="numeric"
        />
        <p>{t('pages.settings.mcp.retention_scope')}</p>
        <div>
          <Button variant="secondary" size="sm" disabled={busy} onClick={() => void saveRetention()}>
            {t('pages.settings.mcp.btn_save_retention')}
          </Button>
        </div>
      </Card>

      <Modal
        open={clientDialog}
        title={t('pages.settings.mcp.client_dialog_title')}
        onClose={() => setClientDialog(false)}
        footer={
          <>
            <Button variant="secondary" onClick={() => setClientDialog(false)}>
              {t('pages.settings.mcp.btn_close')}
            </Button>
            <Button variant="primary" onClick={() => void createClient()} disabled={busy}>
              {t('pages.settings.mcp.btn_create_client')}
            </Button>
          </>
        }
      >
        <p>{t('pages.settings.mcp.client_dialog_desc')}</p>
        <TextInput
          label={t('pages.settings.mcp.client_name_label')}
          value={clientName}
          onChange={setClientName}
          hint={t('pages.settings.mcp.client_name_hint')}
          error={clientError ?? undefined}
          autoFocus
        />
      </Modal>

      <Modal
        open={tokenOnce !== null}
        title={t('pages.settings.mcp.token_dialog_title')}
        onClose={() => setTokenOnce(null)}
        footer={
          <>
            <Button variant="secondary" onClick={() => void copyToken()}>
              {t('pages.settings.mcp.btn_copy_token')}
            </Button>
            <Button variant="primary" onClick={() => setTokenOnce(null)}>
              {t('pages.settings.mcp.btn_close')}
            </Button>
          </>
        }
      >
        <p>{t('pages.settings.mcp.token_dialog_desc')}</p>
        {tokenOnce !== null && (
          <>
            <p>{t('pages.settings.mcp.token_dialog_created', { name: tokenOnce.name })}</p>
            <code>{tokenOnce.token}</code>
            <p>{t('pages.settings.mcp.token_dialog_once')}</p>
          </>
        )}
      </Modal>

      <ConfirmDialog
        open={revoking !== null}
        title={t('pages.settings.mcp.revoke_title')}
        description={
          revoking === null
            ? undefined
            : t('pages.settings.mcp.revoke_message', { name: revoking.name })
        }
        confirmLabel={t('pages.settings.mcp.btn_revoke')}
        cancelLabel={t('common.cancel')}
        tone="danger"
        onConfirm={() => {
          if (revoking === null) return
          const target = revoking
          setRevoking(null)
          void act(
            () => ipc.invoke('mcp.clients.delete', { clientId: target.id }),
            t('pages.settings.mcp.client_revoked_ok')
          )
        }}
        onCancel={() => setRevoking(null)}
      />

      <ConfirmDialog
        open={confirming !== null}
        title={t('pages.settings.mcp.confirmation_approve_title')}
        description={
          confirming === null
            ? undefined
            : `${confirming.title} — ${t('pages.settings.mcp.confirmation_approve_prompt')}`
        }
        confirmLabel={t('pages.settings.mcp.confirmation_btn_approve')}
        cancelLabel={t('common.cancel')}
        tone="danger"
        busy={busy}
        onConfirm={() => void approve()}
        onCancel={() => {
          setConfirming(null)
          setApproveNote('')
        }}
      >
        {confirming !== null && (
          <>
            <p>{confirming.summary}</p>
            <h3>{t('pages.settings.mcp.confirmation_impact_label')}</h3>
            <pre style={{ maxHeight: '16rem', overflow: 'auto', whiteSpace: 'pre-wrap' }}>
              {confirming.impact === null ? '—' : JSON.stringify(confirming.impact, null, 2)}
            </pre>
            <TextInput
              label={t('pages.settings.mcp.confirmation_note_label')}
              value={approveNote}
              onChange={setApproveNote}
              hint={t('pages.settings.mcp.confirmation_note_hint')}
            />
          </>
        )}
      </ConfirmDialog>
    </>
  )
}