import { useSearchParams } from 'react-router-dom'

import { Page, PageHeader, Tabs } from '../../design/components'
import { useT } from '../../i18n'
import type { RouteFeatureProps } from '../../routes'
import { AiTab } from './AiTab'
import { ExternalTab } from './ExternalTab'
import { McpTab } from './McpTab'
import { SearchTab } from './SearchTab'
import { StorageTab } from './StorageTab'
import { WorkspaceTab } from './WorkspaceTab'

const TABS = [
  { id: 'workspace', key: 'pages.settings.tab_workspace' },
  { id: 'external', key: 'pages.settings.tab_external' },
  { id: 'ai', key: 'pages.settings.tab_ai' },
  { id: 'mcp', key: 'pages.settings.tab_mcp' },
  { id: 'search', key: 'pages.settings.tab_search' },
  { id: 'storage', key: 'pages.settings.tab_storage' }
] as const

type TabId = (typeof TABS)[number]['id']

function isTabId(value: string | null): value is TabId {
  return TABS.some((tab) => tab.id === value)
}

/** Settings: six deep-linkable sections (Go pages/settings*.go). */
export default function SettingsPage(_props: RouteFeatureProps): JSX.Element {
  const t = useT()
  const [params, setParams] = useSearchParams()
  const requested = params.get('tab')
  const active: TabId = isTabId(requested) ? requested : 'workspace'

  const change = (id: string): void => {
    setParams({ tab: id }, { replace: true })
  }

  return (
    <Page>
      <PageHeader title={t('pages.settings.title')} />
      <Tabs
        label={t('pages.settings.title')}
        items={TABS.map((tab) => ({ id: tab.id, label: t(tab.key) }))}
        value={active}
        onChange={change}
      />
      {active === 'workspace' && <WorkspaceTab />}
      {active === 'external' && <ExternalTab />}
      {active === 'ai' && <AiTab />}
      {active === 'mcp' && <McpTab />}
      {active === 'search' && <SearchTab />}
      {active === 'storage' && <StorageTab />}
    </Page>
  )
}