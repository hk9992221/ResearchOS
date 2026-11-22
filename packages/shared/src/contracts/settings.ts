import type { IsoDateTime, JsonObject } from '../types/common'

export interface SettingEntry {
  key: string
  value: unknown
  updatedAt: IsoDateTime
}

export interface SettingValueSource {
  value: unknown
  source: 'project' | 'workspace' | 'default'
}

export interface SettingsCommands {
  'settings.workspace.list': { req: { prefix?: string }; res: SettingEntry[] }
  'settings.workspace.get': { req: { key: string }; res: SettingEntry | null }
  'settings.workspace.set': { req: { key: string; value: unknown }; res: SettingEntry }
  'settings.workspace.delete': { req: { key: string }; res: { deleted: boolean } }

  'settings.project.list': { req: { projectId: string; prefix?: string }; res: SettingEntry[] }
  'settings.project.get': { req: { projectId: string; key: string }; res: SettingEntry | null }
  'settings.project.set': { req: { projectId: string; key: string; value: unknown }; res: SettingEntry }
  'settings.project.delete': { req: { projectId: string; key: string }; res: { deleted: boolean } }

  'settings.priority.resolve': { req: { projectId: string; key: string }; res: SettingValueSource }
}

export type SettingsJson = JsonObject