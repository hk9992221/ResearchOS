import type { LocaleCode } from '../types/app'

/** Nested JSON message tree; leaves are strings. */
export interface MessageTree {
  [key: string]: string | MessageTree
}

export interface LocaleBundle {
  locale: LocaleCode
  /** Feature area file name, e.g. `pages_notes`. */
  area: string
  messages: MessageTree
}

export type MessageKey = string