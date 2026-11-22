import { z } from 'zod'

import type { LiteratureCommands } from '@researchos/shared'

import { isValidId } from '../ids'

/**
 * Zod boundary for every `literature.*` IPC command. The service re-validates
 * semantics (archived state, Zotero source, unicity); these schemas pin the
 * wire shape so unknown keys are INVALID_ARGUMENT, never INTERNAL.
 */

const id = z.string().refine((value) => isValidId(value), 'expected a lowercase UUIDv7')
const projectId = id
const libraryId = z.string().min(1).max(64)
const isoDate = z.string().min(1)

const rect = z
  .strictObject({
    x: z.number().finite(),
    y: z.number().finite(),
    width: z.number().finite().nonnegative(),
    height: z.number().finite().nonnegative()
  })
  .strict()

const locator = z
  .strictObject({
    pageNo: z.number().int().min(1).optional(),
    blockNo: z.number().int().min(0).optional(),
    charStart: z.number().int().min(0).optional(),
    charEnd: z.number().int().min(0).optional(),
    heading: z.string().optional(),
    fieldId: z.string().optional(),
    rects: z.array(rect).optional()
  })
  .strict()

const fields = z
  .strictObject({
    summary: z.string().max(100_000),
    problem: z.string().max(100_000),
    method: z.string().max(100_000),
    contribution: z.string().max(100_000),
    limitations: z.string().max(100_000),
    understanding: z.string().max(100_000)
  })
  .strict()

const importSelection = z.discriminatedUnion('kind', [
  z
    .strictObject({
      kind: z.literal('items'),
      libraryId,
      itemKeys: z.array(z.string().min(1)).min(1)
    })
    .strict(),
  z
    .strictObject({
      kind: z.literal('collection'),
      libraryId,
      collectionKey: z.string().min(1),
      includeSubcollections: z.boolean().optional()
    })
    .strict()
])

const annotationInput = z
  .strictObject({
    pageNo: z.number().int().min(1),
    rects: z.array(rect),
    quote: z.string().max(20_000),
    color: z.string().max(64).nullable().optional(),
    noteId: id.nullable().optional(),
    evidenceId: id.nullable().optional(),
    locator: locator.nullable().optional()
  })
  .strict()

const validators = {
  'literature.zotero.status': z.strictObject({ refresh: z.boolean().optional() }),
  'literature.zotero.libraries': z.undefined(),
  'literature.zotero.collections': z.strictObject({ libraryId }),
  'literature.zotero.items': z.strictObject({
    libraryId,
    collectionKey: z.string().optional(),
    query: z.string().optional(),
    limit: z.number().int().min(1).max(100).optional(),
    offset: z.number().int().min(0).optional()
  }),

  'literature.import.preview': z.strictObject({ projectId, selection: importSelection }),
  'literature.import.start': z.strictObject({ projectId, selection: importSelection }),
  'literature.import.report': z.strictObject({ jobId: z.string().min(1) }),

  'literature.papers.list': z.strictObject({
    projectId,
    q: z.string().optional(),
    fileState: z.enum(['ok', 'missing']).optional(),
    sourceState: z.enum(['ok', 'missing', 'unreadable']).optional(),
    hasNote: z.boolean().optional(),
    limit: z.number().int().min(1).max(200).optional(),
    offset: z.number().int().min(0).optional()
  }),
  'literature.papers.get': z.strictObject({ projectId, paperId: id }),
  'literature.papers.link': z.strictObject({ projectId, paperId: id }),
  'literature.papers.unlink': z.strictObject({ projectId, paperId: id }),

  'literature.paperNote.get': z.strictObject({ projectId, paperId: id }),
  'literature.paperNote.create': z.strictObject({ projectId, paperId: id }),
  'literature.paperNote.saveFields': z.strictObject({
    projectId,
    paperId: id,
    fields,
    title: z.string().min(1).max(200).optional(),
    expectedUpdatedAt: isoDate.optional()
  }),
  'literature.paperNote.saveBody': z.strictObject({
    projectId,
    paperId: id,
    markdown: z.string().max(5 * 1024 * 1024),
    baseHash: z.string()
  }),

  'literature.overview.get': z.strictObject({ projectId }),
  'literature.overview.save': z.strictObject({
    projectId,
    markdown: z.string().max(5 * 1024 * 1024),
    baseHash: z.string().optional()
  }),

  'literature.sync.check': z.strictObject({
    projectId: projectId.optional(),
    paperIds: z.array(id).optional()
  }),
  'literature.sync.apply': z.strictObject({
    projectId,
    decisions: z.array(
      z
        .strictObject({ paperId: id, action: z.enum(['overwrite', 'skip']) })
        .strict()
    )
  }),
  'literature.sync.refreshMetadata': z.strictObject({
    projectId: projectId.optional(),
    paperIds: z.array(id).optional()
  }),

  'literature.annotations.create': z.strictObject({
    projectId,
    paperId: id,
    annotation: annotationInput
  }),
  'literature.annotations.list': z.strictObject({ projectId, paperId: id }),
  'literature.annotations.update': z.strictObject({
    projectId,
    annotationId: id,
    quote: z.string().max(20_000).optional(),
    color: z.string().max(64).nullable().optional(),
    noteId: id.nullable().optional(),
    evidenceId: id.nullable().optional()
  }),
  'literature.annotations.delete': z.strictObject({ projectId, annotationId: id })
} satisfies { [C in keyof LiteratureCommands]: z.ZodType<LiteratureCommands[C]['req']> }

export type LiteratureValidators = typeof validators
export type LiteratureChannel = keyof LiteratureValidators

export const literatureValidators = validators

export function literatureValidator<C extends LiteratureChannel>(
  channel: C
): { parse(input: unknown): LiteratureCommands[C]['req'] } {
  return validators[channel]
}