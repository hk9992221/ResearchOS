import { useCallback, useEffect, useRef, useState } from 'react'

import type { PluginEvent, PluginFrameTheme, PluginHostEvent } from '@researchos/shared'

import styles from './PluginFrame.module.css'
import { createPluginFrameBridge, pluginFrameUrl, type PluginFrameBridge } from './bridge'

export interface PluginFrameProps {
  readonly pluginId: string
  readonly entry: string
  readonly title?: string
  readonly locale?: string
  readonly theme?: PluginFrameTheme
  readonly initialHeight?: number
  /** Requests from the iframe; the caller routes them through `plugins.rpc.call`. */
  readonly invoke: (method: string, params: unknown) => Promise<unknown>
  readonly events?: Array<PluginHostEvent | PluginEvent>
  readonly onError?: (error: unknown) => void
}

/**
 * Sandboxed custom-panel host. `renderer.js` runs inside an opaque-origin iframe
 * with `allow-scripts` only; it cannot read the host DOM, cookies, Node or
 * `window.researchos`. The host renders the frame and owns the bridge.
 */
export function PluginFrame({
  pluginId,
  entry,
  title,
  locale,
  theme,
  initialHeight = 240,
  invoke,
  events = [],
  onError
}: PluginFrameProps): JSX.Element {
  const frameRef = useRef<HTMLIFrameElement>(null)
  const bridgeRef = useRef<PluginFrameBridge | null>(null)
  const invokeRef = useRef(invoke)
  invokeRef.current = invoke
  const eventsRef = useRef(events)
  eventsRef.current = events
  const deliveredRef = useRef(new WeakSet<object>())
  const [height, setHeight] = useState(initialHeight)
  const [failed, setFailed] = useState(false)

  const handleError = useCallback(
    (error: unknown) => {
      setFailed(true)
      onError?.(error)
    },
    [onError]
  )

  // Exactly one bridge per mounted frame; requests use the latest invoke while
  // the channel stays attached across ordinary re-renders.
  const handleLoad = useCallback(() => {
    const frame = frameRef.current
    if (frame === null) return
    bridgeRef.current?.dispose()
    const bridge = createPluginFrameBridge({
      pluginId,
      invoke: (method, params) => invokeRef.current(method, params),
      locale,
      theme,
      onResize: (_width, nextHeight) => {
        setHeight(Math.min(Math.max(nextHeight, 80), 1200))
      },
      onError: handleError
    })
    bridgeRef.current = bridge
    try {
      bridge.attach(frame)
      // The bridge only exists after load; replay the context/files events
      // that were queued while the iframe was booting.
      const delivered = deliveredRef.current
      for (const event of eventsRef.current) {
        if (delivered.has(event)) continue
        delivered.add(event)
        bridge.sendEvent(event)
      }
    } catch (error) {
      handleError(error)
    }
  }, [pluginId, locale, theme, handleError])

  useEffect(
    () => () => {
      bridgeRef.current?.dispose()
      bridgeRef.current = null
    },
    []
  )

  useEffect(() => {
    const current = bridgeRef.current
    if (current === null) return
    const delivered = deliveredRef.current
    for (const event of events) {
      if (delivered.has(event)) continue
      delivered.add(event)
      current.sendEvent(event)
    }
  }, [events])

  return (
    <div className={styles.frameHost} data-plugin={pluginId}>
      {failed && <p className={styles.error}>This plugin panel could not start.</p>}
      <iframe
        ref={frameRef}
        className={styles.frame}
        title={title ?? `Plugin ${pluginId}`}
        src={pluginFrameUrl(pluginId, entry)}
        sandbox="allow-scripts"
        referrerPolicy="no-referrer"
        height={height}
        onLoad={handleLoad}
      />
    </div>
  )
}