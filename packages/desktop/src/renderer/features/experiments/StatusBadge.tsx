import type { RunStatus } from '@researchos/shared'

import { Badge } from '../../design/components'
import { useT } from '../../i18n'
import { dirTone, runStatusKey, runStatusTone } from './model'

export function DirBadge({
  status
}: {
  readonly status: 'ok' | 'broken' | 'unbound'
}): JSX.Element {
  const t = useT()
  const key =
    status === 'ok'
      ? 'pages.experiments.dir_ok'
      : status === 'broken'
        ? 'pages.experiments.dir_broken'
        : 'pages.experiments.dir_unbound'
  return <Badge tone={dirTone(status)}>{t(key)}</Badge>
}

export function RunStatusBadge({ status }: { readonly status: RunStatus }): JSX.Element {
  const t = useT()
  return <Badge tone={runStatusTone(status)}>{t(runStatusKey(status))}</Badge>
}