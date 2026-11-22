import { useEffect, useState } from 'react'

import type { AiActivity } from '@researchos/shared'

import { useT } from '../i18n'
import { ipc } from '../lib/ipc'
import styles from './AiActivityPet.module.css'

const STALE_AFTER_MS = 4000
const DROP_AFTER_MS = 10 * 60 * 1000

interface ActiveCall extends AiActivity {
  lastAt: number
}

const KIND_KEY: Readonly<Record<AiActivity['kind'], string>> = {
  chat: 'shell.ai_pet.kind_chat',
  structured: 'shell.ai_pet.kind_structured',
  embedding: 'shell.ai_pet.kind_embedding'
}

/**
 * A small mascot for every main-process AI call. `started`/`tick` open and
 * refresh it, `finished`/`failed` close it; a missing heartbeat turns the
 * bubble into an explicit "no response" state instead of an endless spinner.
 */
export function AiActivityPet(): JSX.Element | null {
  const t = useT()
  const [calls, setCalls] = useState<Record<string, ActiveCall>>({})
  const [now, setNow] = useState(() => Date.now())

  useEffect(
    () =>
      ipc.on('ai.activity', (activity) => {
        setCalls((current) => {
          const next = { ...current }
          if (activity.phase === 'finished' || activity.phase === 'failed') {
            delete next[activity.callId]
          } else {
            next[activity.callId] = { ...activity, lastAt: Date.now() }
          }
          return next
        })
      }),
    []
  )

  const active = Object.values(calls)
  useEffect(() => {
    if (active.length === 0) return undefined
    const timer = window.setInterval(() => {
      const at = Date.now()
      setNow(at)
      setCalls((current) => {
        const next: Record<string, ActiveCall> = {}
        let changed = false
        for (const [id, call] of Object.entries(current)) {
          if (at - call.lastAt > DROP_AFTER_MS) {
            changed = true
            continue
          }
          next[id] = call
        }
        return changed ? next : current
      })
    }, 500)
    return () => window.clearInterval(timer)
  }, [active.length])

  if (active.length === 0) return null

  const longest = active.reduce(
    (best, call) => (call.elapsedMs > best.elapsedMs ? call : best),
    active[0]!
  )
  const stale = now - longest.lastAt > STALE_AFTER_MS
  const elapsed = Math.max(longest.elapsedMs, now - Date.parse(longest.startedAt))
  const seconds = Math.max(0, Math.round(elapsed / 1000))
  const model = longest.model ?? longest.role

  return (
    <div
      className={styles.pet}
      data-stale={stale ? 'true' : 'false'}
      role="status"
      aria-label={t('shell.ai_pet.aria', { model })}
    >
      <div className={styles.avatar} aria-hidden>
        <span className={styles.eye} />
        <span className={styles.eye} />
      </div>
      <div className={styles.bubble}>
        <span className={styles.title}>
          {active.length > 1
            ? t('shell.ai_pet.multiple', { count: active.length })
            : t('shell.ai_pet.title', { model })}
        </span>
        <span className={styles.detail}>
          {stale
            ? t('shell.ai_pet.stale', { seconds })
            : t('shell.ai_pet.running', {
                kind: t(KIND_KEY[longest.kind]),
                role: longest.role,
                seconds
              })}
        </span>
      </div>
    </div>
  )
}
