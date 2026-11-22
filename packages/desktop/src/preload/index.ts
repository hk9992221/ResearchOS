import { contextBridge, ipcRenderer, type IpcRendererEvent } from 'electron'

import {
  APP_EVENT_CHANNEL_PREFIX,
  type AppEventPayload,
  type AppEventType,
  type CommandChannel,
  type CommandMap,
  type IpcResult,
  type ResearchOsBridge
} from '@researchos/shared'

interface Subscription {
  channel: string
  handler: unknown
  listener: (event: IpcRendererEvent, payload: unknown) => void
}

const subscriptions: Subscription[] = []

function removeSubscription(entry: Subscription): void {
  const index = subscriptions.indexOf(entry)
  if (index === -1) return
  subscriptions.splice(index, 1)
  ipcRenderer.removeListener(entry.channel, entry.listener)
}

const bridge: ResearchOsBridge = {
  invoke<C extends CommandChannel>(
    channel: C,
    payload: CommandMap[C]['req']
  ): Promise<IpcResult<CommandMap[C]['res']>> {
    return ipcRenderer.invoke(channel, payload)
  },

  on<E extends AppEventType>(event: E, handler: (payload: AppEventPayload<E>) => void): () => void {
    const channel = `${APP_EVENT_CHANNEL_PREFIX}${event}`
    const listener = (_event: IpcRendererEvent, payload: unknown): void => {
      // Payloads are produced by this app's main process for this event type.
      handler(payload as AppEventPayload<E>)
    }
    const entry: Subscription = { channel, handler, listener }
    subscriptions.push(entry)
    ipcRenderer.on(channel, listener)
    return () => removeSubscription(entry)
  },

  off<E extends AppEventType>(event: E, handler: (payload: AppEventPayload<E>) => void): void {
    const channel = `${APP_EVENT_CHANNEL_PREFIX}${event}`
    const entry = subscriptions.find((item) => item.channel === channel && item.handler === handler)
    if (entry !== undefined) removeSubscription(entry)
  },

  platform: process.platform,
  versions: {
    electron: process.versions.electron,
    chrome: process.versions.chrome
  }
}

contextBridge.exposeInMainWorld('researchos', bridge)