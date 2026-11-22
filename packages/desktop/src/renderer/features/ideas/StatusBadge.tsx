import type { IdeaStatus } from '@researchos/shared'

import { Badge } from '../../design/components'
import { useT } from '../../i18n'
import { statusKey, statusTone } from './model'

/** The one status badge: list rows, detail header and lineage nodes use it. */
export function StatusBadge({ status }: { readonly status: IdeaStatus }): JSX.Element {
  const t = useT()
  return <Badge tone={statusTone(status)}>{t(statusKey(status))}</Badge>
}