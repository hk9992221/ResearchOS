/**
 * Route entry for the literature page. It renders the page header while the
 * feature chunk loads, so the destination never shows a bare content area:
 * the shell's Suspense fallback would otherwise be the whole page.
 */

import { lazy, Suspense } from 'react'

import { Page, PageHeader, Skeleton } from '../../design/components'
import { useT } from '../../i18n'
import type { RouteFeatureProps } from '../../routes'

const LiteraturePage = lazy(() => import('./LiteraturePage'))

export default function LiteratureEntry({ projectId }: RouteFeatureProps): JSX.Element {
  const t = useT()
  return (
    <Suspense
      fallback={
        <Page>
          <PageHeader title={t('pages.literature.title')} />
          <Skeleton rows={7} />
        </Page>
      }
    >
      <LiteraturePage projectId={projectId} />
    </Suspense>
  )
}
