import type { CommandChannel, CommandMap, CommandValidator } from '@researchos/shared'

import type { NotesModule } from '@researchos/core/modules/notes/index'
import * as notesSchemas from '../../../../../core/src/validation/notes'
import type { CommandRouter } from '../router'

/** Minimal schema surface; the concrete schemas are zod (core-owned). */
interface RequestSchema {
  parse(input: unknown): unknown
}

/**
 * Notes IPC handlers. Every request is validated with the core zod schemas;
 * archived projects reject writes before dispatch. The router converts thrown
 * AppErrors into the IpcResult envelope, so handlers return plain data.
 */
export interface NotesHandlerDeps {
  notes(projectId: string): NotesModule
  /** Throws ARCHIVED_READ_ONLY for a write on an archived project. */
  assertWritable?(projectId: string): void
}

type HandlerFor<C extends CommandChannel> = (
  request: CommandMap[C]['req']
) => CommandMap[C]['res'] | Promise<CommandMap[C]['res']>

function register<C extends CommandChannel>(
  router: CommandRouter,
  channel: C,
  schema: RequestSchema,
  handler: HandlerFor<C>
): void {
  const validator: CommandValidator<CommandMap[C]['req']> = {
    parse: (input: unknown) => schema.parse(input) as CommandMap[C]['req']
  }
  router.registerHandler(channel, validator, handler)
}

function requireWritable(deps: NotesHandlerDeps, projectId: string): void {
  deps.assertWritable?.(projectId)
}

export function registerNotesHandlers(router: CommandRouter, deps: NotesHandlerDeps): void {
  /* ------------------------------------------------------------- notes */

  register(router, 'notes.list', notesSchemas.noteListSchema, (request) =>
    deps.notes(request.projectId).listNotes({
      kind: request.kind,
      q: request.q,
      tags: request.tags,
      limit: request.limit,
      offset: request.offset
    })
  )

  register(router, 'notes.get', notesSchemas.noteGetSchema, (request) =>
    deps.notes(request.projectId).getNote(request.noteId)
  )

  register(router, 'notes.body.get', notesSchemas.noteBodyGetSchema, (request) =>
    deps.notes(request.projectId).loadBody(request.noteId)
  )

  register(router, 'notes.create', notesSchemas.noteCreateSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).createNote({
      kind: request.kind,
      title: request.title,
      description: request.description,
      bodyMd: request.bodyMd,
      tags: request.tags,
      paperId: request.paperId
    })
  })

  register(router, 'notes.updateMeta', notesSchemas.noteUpdateMetaSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps
      .notes(request.projectId)
      .updateNoteMeta(request.noteId, request.patch, request.expectedUpdatedAt)
  })

  register(router, 'notes.saveBody', notesSchemas.noteSaveBodySchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).saveBody(request.noteId, request.markdown, request.baseHash)
  })

  register(router, 'notes.delete', notesSchemas.noteIdSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).deleteNote(request.noteId)
  })

  register(router, 'notes.restore', notesSchemas.noteIdSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).restoreNote(request.noteId)
  })

  register(router, 'notes.tags.list', notesSchemas.tagListSchema, (request) =>
    deps.notes(request.projectId).listTags()
  )

  register(router, 'notes.tags.add', notesSchemas.noteTagsSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).addNoteTags(request.noteId, request.tags)
  })

  register(router, 'notes.tags.remove', notesSchemas.noteTagsSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).removeNoteTags(request.noteId, request.tags)
  })

  register(router, 'notes.links.outbound', notesSchemas.noteIdSchema, (request) =>
    deps.notes(request.projectId).outboundLinks(request.noteId)
  )

  register(router, 'notes.promote', notesSchemas.notePromoteSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).promote(request.noteId, request.databaseId)
  })

  register(router, 'notes.detach', notesSchemas.noteDetachSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).detach(request.recordId, request.deleteNote === true)
  })

  /* --------------------------------------------------------- databases */

  register(
    router,
    'notes.databases.list',
    notesSchemas.reconcileScanSchema,
    (request) => deps.notes(request.projectId).listDatabases()
  )

  register(router, 'notes.databases.create', notesSchemas.databaseCreateSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).createDatabase({
      name: request.name,
      description: request.description,
      fields: request.fields
    }).database
  })

  register(router, 'notes.databases.get', notesSchemas.databaseIdSchema, (request) =>
    deps.notes(request.projectId).getDatabase(request.databaseId)
  )

  register(router, 'notes.databases.update', notesSchemas.databaseUpdateSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps
      .notes(request.projectId)
      .updateDatabase(request.databaseId, { name: request.name, description: request.description }, request.expectedUpdatedAt)
  })

  register(router, 'notes.databases.delete', notesSchemas.databaseIdSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).deleteDatabase(request.databaseId)
  })

  register(router, 'notes.databases.fields.list', notesSchemas.databaseIdSchema, (request) =>
    deps.notes(request.projectId).listFields(request.databaseId)
  )

  register(router, 'notes.databases.fields.create', notesSchemas.fieldCreateCommandSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).createField(request.databaseId, request.field)
  })

  register(router, 'notes.databases.fields.update', notesSchemas.fieldUpdateSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps
      .notes(request.projectId)
      .updateField(request.fieldId, request.patch, request.expectedUpdatedAt)
  })

  register(router, 'notes.databases.fields.delete', notesSchemas.fieldDeleteSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).deleteField(request.fieldId, request.expectedUpdatedAt)
  })

  register(router, 'notes.databases.fields.reorder', notesSchemas.fieldReorderSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).reorderFields(request.databaseId, request.order.fieldIds)
  })

  /* ----------------------------------------------------------- records */

  register(router, 'notes.databases.records.list', notesSchemas.recordListSchema, (request) =>
    deps.notes(request.projectId).listRecords({
      databaseId: request.databaseId,
      ...(request.q === undefined ? {} : { q: request.q }),
      filter: request.filter,
      sorts: request.sorts,
      limit: request.limit,
      offset: request.offset
    })
  )

  register(router, 'notes.databases.records.get', notesSchemas.recordIdSchema, (request) =>
    deps.notes(request.projectId).getRecord(request.recordId)
  )

  register(router, 'notes.databases.records.create', notesSchemas.recordCreateSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).createRecord({
      databaseId: request.databaseId,
      values: request.values,
      title: request.title,
      noteId: request.noteId
    })
  })

  register(router, 'notes.databases.records.update', notesSchemas.recordUpdateSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).updateRecord({
      recordId: request.recordId,
      values: request.patch.values ?? {},
      title: request.patch.title,
      expectedUpdatedAt: request.expectedUpdatedAt
    })
  })

  register(router, 'notes.databases.records.delete', notesSchemas.recordDeleteSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).deleteRecord(request.recordId, request.deleteNote === true)
  })

  register(router, 'notes.databases.records.reorder', notesSchemas.recordReorderSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).reorderRecords(request.databaseId, request.order.recordIds)
  })

  /* ------------------------------------------------------- attachments */

  register(router, 'notes.attachments.list', notesSchemas.attachmentListSchema, (request) =>
    deps.notes(request.projectId).listAttachments(request.noteId)
  )

  register(router, 'notes.attachments.add', notesSchemas.attachmentAddSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).addAttachment({
      noteId: request.noteId,
      sourcePath: request.sourcePath,
      fileName: request.fileName
    })
  })

  register(router, 'notes.attachments.remove', notesSchemas.attachmentRemoveSchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).removeAttachment(request.noteId, request.attachmentId)
  })

  /* --------------------------------------------------------- reconcile */

  register(router, 'notes.reconcile.scan', notesSchemas.reconcileScanSchema, (request) =>
    deps.notes(request.projectId).scanNotes()
  )

  register(router, 'notes.reconcile.apply', notesSchemas.reconcileApplySchema, (request) => {
    requireWritable(deps, request.projectId)
    return deps.notes(request.projectId).applyReconcile(request.actions)
  })
}