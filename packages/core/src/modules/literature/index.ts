export { LiteratureModule, type LiteratureModuleOptions } from './module'
export { LiteratureContext, type LiteratureContextOptions, type ProjectInfo } from './context'
export { LiteratureJobStore } from './jobs'
export {
  literatureNotesPort
} from './notes-port'
export type {
  LiteratureIndexHook,
  LiteratureNote,
  LiteratureNoteBody,
  LiteratureNotesPort,
  NoteCreateRequest,
  NoteSaveRequest
} from './deps'
export { createLiteratureTools, LITERATURE_RUNTIME_KEY } from './mcp'
export type { LiteratureMcpRuntime, LiteratureJobPort } from './mcp'
export { createPaperSyncApplyEffect } from './sync-effect'
export { ZoteroConnection, ZOTERO_LOCAL_API_URL } from './zotero/connection'
export { LocalApiReader } from './zotero/localapi'
export { SqliteReader } from './zotero/sqlite'
export { createResolver, containedJoin } from './zotero/resolve'
export { ZoteroSourceService } from './zotero/source'
export type { ZoteroReader } from './types'
export { previewImport, runImport } from './library/import'
export { syncCheck, syncApply, refreshMetadata } from './library/sync'
export {
  createAnnotation,
  listAnnotations,
  updateAnnotation,
  deleteAnnotation
} from './annotations'