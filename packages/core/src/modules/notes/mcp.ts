import { z } from 'zod'

import type { DbField, DbRecord, FilterNode, Note, ResearchDatabase, SortKey } from '@researchos/shared'
import { DB_FIELD_TYPES, NOTE_KINDS } from '@researchos/shared'

import type { Actor } from '../../schema'
import { parseInput } from '../../validation/notes'
import type { NotesModule } from './index'
import { decodeCursor, encodeCursor, type Json } from './support'
import type { FieldInput } from './database'
import { ok, readTool, writeTool } from '../mcp/tools'
import type { HandlerContext, ToolDefinition } from '../mcp/types'

/**
 * The 20 notes-catalog tools.
 * Writes use the shared `writeTool` helper so `idempotency_key` is declared and
 * required without a second schema convention (defect 5); the MCP host applies
 * the project archived gate before dispatch.
 */
export interface NotesToolDeps {
  /** Resolves the project-scoped module; `ctx.project` carries the open handle. */
  project(projectId: string, ctx: HandlerContext): NotesModule
}

const MCP_ACTOR: Actor = 'mcp'
const projectId = z.string().min(1).max(64)
const entityId = z.string().min(1).max(64)
const scratchpadKind = z.literal('scratchpad')

function moduleOf(deps: NotesToolDeps, ctx: HandlerContext, project: string): NotesModule {
  return deps.project(project, ctx)
}

function parse<T extends z.ZodType>(schema: T, args: unknown): z.infer<T> {
  return parseInput(schema, args)
}

/* ------------------------------------------------------------ projections */

function noteSummaryView(note: Note): Json {
  const view: Json = {
    note_id: note.id,
    kind: note.kind,
    title: note.title,
    description: note.description,
    file_path: note.filePath,
    created_at: note.createdAt,
    updated_at: note.updatedAt
  }
  if (note.deletedAt !== null) view.deleted_at = note.deletedAt
  return view
}

function noteView(note: Note): Json {
  const view = noteSummaryView(note)
  view.body_hash = note.fileHash
  view.body_state = note.fileState
  view.paper_id = note.paperId
  return view
}

function databaseView(database: ResearchDatabase): Json {
  const view: Json = {
    database_id: database.id,
    name: database.name,
    description: database.description,
    created_at: database.createdAt,
    updated_at: database.updatedAt
  }
  if (database.deletedAt !== null) view.deleted_at = database.deletedAt
  return view
}

function fieldView(field: DbField): Json {
  return {
    field_id: field.id,
    database_id: field.databaseId,
    name: field.name,
    type: field.type,
    options: field.config.options ?? null,
    targets: field.config.targets ?? null,
    position: field.sortOrder,
    sort_order: field.sortOrder,
    role: field.role,
    created_at: field.createdAt,
    updated_at: field.updatedAt
  }
}

function recordView(record: DbRecord): Json {
  const view: Json = {
    record_id: record.id,
    database_id: record.databaseId,
    note_id: record.noteId,
    title: record.title,
    values: record.data,
    created_at: record.createdAt,
    updated_at: record.updatedAt
  }
  if (record.deletedAt !== null) view.deleted_at = record.deletedAt
  return view
}

const fieldInputShape = {
  name: z.string().min(1).max(64),
  type: z.enum(DB_FIELD_TYPES),
  options: z.unknown().optional(),
  config: z.unknown().optional(),
  targets: z.array(z.string()).optional(),
  role: z.enum(['title', 'plain']).optional()
}
const fieldInputSchema = z.object(fieldInputShape).loose()

function sortKeys(value: unknown): SortKey[] {
  if (value === undefined) return []
  if (!Array.isArray(value)) throw parseInput(z.array(z.never()), value)
  return value.map((entry, index) => {
    const key = z
      .object({
        field_id: z.string().min(1).optional(),
        field: z.string().min(1).optional(),
        fieldId: z.string().min(1).optional(),
        desc: z.boolean().optional(),
        direction: z.enum(['asc', 'desc']).optional()
      })
      .loose()
      .safeParse(entry)
    if (!key.success) {
      throw parseInput(z.object({ [`sort.${index}`]: z.string() }), { [`sort.${index}`]: 'invalid sort key' })
    }
    const field = key.data.field_id ?? key.data.field ?? key.data.fieldId
    if (field === undefined) {
      throw parseInput(z.object({ [`sort.${index}.field_id`]: z.string() }), {
        [`sort.${index}.field_id`]: 'required'
      })
    }
    const desc = key.data.desc ?? key.data.direction === 'desc'
    return { field, direction: desc ? 'desc' : 'asc' } satisfies SortKey
  })
}

const listSchema = z
  .object({
    project_id: projectId,
    kind: z.enum(NOTE_KINDS).optional(),
    q: z.string().max(500).optional(),
    include_deleted: z.boolean().optional(),
    limit: z.number().int().min(1).max(200).optional(),
    cursor: z.string().max(1024).optional()
  })
  .strict()

export function createNotesTools(deps: NotesToolDeps): ToolDefinition[] {
  return [
    readTool({
      name: 'note_list',
      title: '笔记列表',
      description: '列出项目笔记，可按类型、标题关键字、标签过滤。',
      inputSchema: {
        project_id: projectId,
        kind: z.enum(NOTE_KINDS).optional(),
        q: z.string().max(500).optional(),
        include_deleted: z.boolean().optional(),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().max(1024).optional()
      },
      handler(ctx, args) {
        const input = parse(listSchema, args)
        const offset = decodeCursor(input.cursor)
        const page = moduleOf(deps, ctx, input.project_id).listNotes({
          kind: input.kind,
          q: input.q,
          limit: input.limit,
          offset
        })
        const next = page.offset + page.items.length
        return ok({
          items: page.items.map(noteSummaryView),
          next_cursor: next < page.total ? encodeCursor(next) : null
        })
      }
    }),

    readTool({
      name: 'note_get',
      title: '笔记详情',
      description: '返回笔记元数据、Markdown 正文与附件列表。',
      inputSchema: { project_id: projectId, note_id: entityId, include_body: z.boolean().optional() },
      handler(ctx, args) {
        const input = parse(
          z.object({ project_id: projectId, note_id: entityId, include_body: z.boolean().optional() }).strict(),
          args
        )
        const module = moduleOf(deps, ctx, input.project_id)
        const note = module.getNote(input.note_id)
        const view: Json = { note: noteView(note) }
        if (input.include_body !== false) {
          const body = module.loadBody(input.note_id)
          view.body_md = body.markdown
          view.body_hash = body.hash
          view.body_state = body.state
        }
        view.attachments = module.listAttachments(input.note_id).map((attachment) => ({
          path: attachment.relativePath,
          markdown_path: `../${attachment.relativePath.split('/').slice(2).join('/')}`,
          mime: attachment.contentType,
          sha256: attachment.sha256,
          bytes: attachment.bytes
        }))
        return ok(view)
      }
    }),

    writeTool({
      name: 'note_create',
      title: '创建自由笔记',
      description: '创建一篇自由笔记（scratchpad）。论文笔记、记录页与文献综述请使用各自专用工具。',
      inputSchema: {
        project_id: projectId,
        title: z.string().min(1).max(200),
        description: z.string().optional(),
        kind: scratchpadKind,
        body_md: z.string().optional()
      },
      handler(ctx, args) {
        const input = parse(
          z
            .object({
              project_id: projectId,
              title: z.string().min(1).max(200),
              description: z.string().optional(),
              kind: scratchpadKind,
              body_md: z.string().optional(),
            })
            .strict(),
          args
        )
        const note = moduleOf(deps, ctx, input.project_id).createNote({
          kind: 'scratchpad',
          title: input.title,
          description: input.description,
          bodyMd: input.body_md,
          actor: MCP_ACTOR
        })
        return ok({ note: noteView(note) })
      }
    }),

    writeTool({
      name: 'note_update_title',
      title: '修改笔记标题',
      description: '修改笔记标题，正文请使用 note_save_body。文件 frontmatter 与数据库同步更新。',
      inputSchema: { project_id: projectId, note_id: entityId, title: z.string().min(1).max(200) },
      handler(ctx, args) {
        const input = parse(
          z
            .object({
              project_id: projectId,
              note_id: entityId,
              title: z.string().min(1).max(200),
            })
            .strict(),
          args
        )
        const note = moduleOf(deps, ctx, input.project_id).updateNoteTitle(input.note_id, input.title, MCP_ACTOR)
        return ok({ note: noteView(note) })
      }
    }),

    writeTool({
      name: 'note_update_meta',
      title: '修改笔记元数据',
      description:
        '修改笔记标题与描述（description 传空串清空）。正文请使用 note_save_body；文件 frontmatter 与数据库在同一事务内更新。',
      inputSchema: {
        project_id: projectId,
        note_id: entityId,
        title: z.string().min(1).max(200),
        description: z.string().optional()
      },
      handler(ctx, args) {
        const input = parse(
          z
            .object({
              project_id: projectId,
              note_id: entityId,
              title: z.string().min(1).max(200),
              description: z.string().optional(),
            })
            .strict(),
          args
        )
        const note = moduleOf(deps, ctx, input.project_id).updateNoteMeta(
          input.note_id,
          { title: input.title, description: input.description },
          undefined,
          MCP_ACTOR
        )
        return ok({ note: noteView(note) })
      }
    }),

    writeTool({
      name: 'note_save_body',
      title: '保存笔记正文',
      description:
        '以乐观并发保存笔记正文。expected_hash 取自 note_get 的 body_hash；不一致返回 CONFLICT，不覆盖。同日多次保存合并为一条 note.updated。',
      inputSchema: {
        project_id: projectId,
        note_id: entityId,
        body_md: z.string().max(5 * 1024 * 1024),
        expected_hash: z.string()
      },
      handler(ctx, args) {
        const input = parse(
          z
            .object({
              project_id: projectId,
              note_id: entityId,
              body_md: z.string().max(5 * 1024 * 1024),
              expected_hash: z.string(),
            })
            .strict(),
          args
        )
        const module = moduleOf(deps, ctx, input.project_id)
        const saved = module.saveBody(input.note_id, input.body_md, input.expected_hash, MCP_ACTOR)
        return ok({ note: noteView(module.getNote(input.note_id)), body_hash: saved.fileHash })
      }
    }),

    writeTool({
      name: 'note_delete',
      title: '删除笔记',
      description: '软删除笔记（进入回收站，可恢复）。',
      inputSchema: { project_id: projectId, note_id: entityId },
      annotations: { destructiveHint: true },
      handler(ctx, args) {
        const input = parse(
          z.object({ project_id: projectId, note_id: entityId }).strict(),
          args
        )
        moduleOf(deps, ctx, input.project_id).deleteNote(input.note_id, MCP_ACTOR)
        return ok({ deleted: true })
      }
    }),

    readTool({
      name: 'database_list',
      title: '数据库列表',
      description: '列出项目的 Research Database。',
      inputSchema: { project_id: projectId, include_deleted: z.boolean().optional() },
      handler(ctx, args) {
        const input = parse(
          z.object({ project_id: projectId, include_deleted: z.boolean().optional() }).strict(),
          args
        )
        const databases = moduleOf(deps, ctx, input.project_id).listDatabases(input.include_deleted === true)
        return ok({ databases: databases.map(databaseView) })
      }
    }),

    readTool({
      name: 'database_get',
      title: '数据库详情',
      description: '返回数据库定义、字段列表与记录数。',
      inputSchema: { project_id: projectId, database_id: entityId },
      handler(ctx, args) {
        const input = parse(z.object({ project_id: projectId, database_id: entityId }).strict(), args)
        const module = moduleOf(deps, ctx, input.project_id)
        return ok({
          database: databaseView(module.getDatabase(input.database_id)),
          fields: module.listFields(input.database_id).map(fieldView),
          record_count: module.listRecords({ databaseId: input.database_id, limit: 1 }).total
        })
      }
    }),

    writeTool({
      name: 'database_create',
      title: '创建数据库',
      description: '创建 Research Database，可同时创建初始字段。',
      inputSchema: {
        project_id: projectId,
        name: z.string().min(1).max(200),
        description: z.string().optional(),
        fields: z.array(fieldInputSchema).optional()
      },
      handler(ctx, args) {
        const input = parse(
          z
            .object({
              project_id: projectId,
              name: z.string().min(1).max(200),
              description: z.string().optional(),
              fields: z.array(fieldInputSchema).optional(),
            })
            .strict(),
          args
        )
        const created = moduleOf(deps, ctx, input.project_id).createDatabase({
          name: input.name,
          description: input.description,
          fields: input.fields as FieldInput[] | undefined,
          actor: MCP_ACTOR
        })
        return ok({ database: databaseView(created.database), fields: created.fields.map(fieldView) })
      }
    }),

    writeTool({
      name: 'database_update',
      title: '重命名数据库',
      description: '重命名 Research Database。',
      inputSchema: { project_id: projectId, database_id: entityId, name: z.string().min(1).max(200) },
      handler(ctx, args) {
        const input = parse(
          z
            .object({
              project_id: projectId,
              database_id: entityId,
              name: z.string().min(1).max(200),
            })
            .strict(),
          args
        )
        const database = moduleOf(deps, ctx, input.project_id).updateDatabase(
          input.database_id,
          { name: input.name },
          undefined,
          MCP_ACTOR
        )
        return ok({ database: databaseView(database) })
      }
    }),

    writeTool({
      name: 'database_field_create',
      title: '新增字段',
      description: '为数据库新增字段。类型创建后不可修改。',
      inputSchema: {
        project_id: projectId,
        database_id: entityId,
        name: z.string().min(1).max(64),
        type: z.enum(DB_FIELD_TYPES),
        options: z.unknown().optional(),
        config: z.unknown().optional(),
        targets: z.array(z.string()).optional()
      },
      handler(ctx, args) {
        const input = parse(
          z
            .object({
              project_id: projectId,
              database_id: entityId,
              name: z.string().min(1).max(64),
              type: z.enum(DB_FIELD_TYPES),
              options: z.unknown().optional(),
              config: z.unknown().optional(),
              targets: z.array(z.string()).optional(),
            })
            .strict(),
          args
        )
        const field = moduleOf(deps, ctx, input.project_id).createField(
          input.database_id,
          {
            name: input.name,
            type: input.type,
            options: input.options,
            config: input.config,
            targets: input.targets
          },
          MCP_ACTOR
        )
        return ok({ field: fieldView(field) })
      }
    }),

    writeTool({
      name: 'database_field_update',
      title: '更新字段',
      description: '更新字段名称或选项。字段类型不可修改。',
      inputSchema: {
        project_id: projectId,
        database_id: entityId,
        field_id: entityId,
        name: z.string().min(1).max(64).optional(),
        options: z.unknown().optional(),
        config: z.unknown().optional(),
        targets: z.array(z.string()).optional(),
        expected_updated_at: z.string().optional()
      },
      handler(ctx, args) {
        const input = parse(
          z
            .object({
              project_id: projectId,
              database_id: entityId,
              field_id: entityId,
              name: z.string().min(1).max(64).optional(),
              options: z.unknown().optional(),
              config: z.unknown().optional(),
              targets: z.array(z.string()).optional(),
              expected_updated_at: z.string().optional(),
            })
            .strict(),
          args
        )
        const field = moduleOf(deps, ctx, input.project_id).updateField(
          input.field_id,
          {
            name: input.name,
            options: input.options,
            config: input.config,
            targets: input.targets
          },
          input.expected_updated_at,
          MCP_ACTOR
        )
        return ok({ field: fieldView(field) })
      }
    }),

    writeTool({
      name: 'database_field_delete',
      title: '删除字段',
      description: '软删除字段，已录入的值保留。',
      inputSchema: {
        project_id: projectId,
        database_id: entityId,
        field_id: entityId,
        expected_updated_at: z.string().optional()
      },
      annotations: { destructiveHint: true },
      handler(ctx, args) {
        const input = parse(
          z
            .object({
              project_id: projectId,
              database_id: entityId,
              field_id: entityId,
              expected_updated_at: z.string().optional(),
            })
            .strict(),
          args
        )
        moduleOf(deps, ctx, input.project_id).deleteField(input.field_id, input.expected_updated_at, MCP_ACTOR)
        return ok({ deleted: true })
      }
    }),

    writeTool({
      name: 'database_delete',
      title: '删除数据库',
      description: '软删除数据库（进入回收站，字段与记录保留）。',
      inputSchema: { project_id: projectId, database_id: entityId },
      annotations: { destructiveHint: true },
      handler(ctx, args) {
        const input = parse(
          z.object({ project_id: projectId, database_id: entityId }).strict(),
          args
        )
        moduleOf(deps, ctx, input.project_id).deleteDatabase(input.database_id, MCP_ACTOR)
        return ok({ deleted: true })
      }
    }),

    readTool({
      name: 'record_list',
      title: '记录列表',
      description: '列出数据库记录，可带过滤树与排序。',
      inputSchema: {
        project_id: projectId,
        database_id: entityId,
        filter: z.unknown().optional(),
        sort: z.array(z.record(z.string(), z.unknown())).optional(),
        include_deleted: z.boolean().optional(),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().max(1024).optional()
      },
      handler(ctx, args) {
        const input = parse(
          z
            .object({
              project_id: projectId,
              database_id: entityId,
              filter: z.unknown().optional(),
              sort: z.array(z.record(z.string(), z.unknown())).optional(),
              include_deleted: z.boolean().optional(),
              limit: z.number().int().min(1).max(200).optional(),
              cursor: z.string().max(1024).optional()
            })
            .strict(),
          args
        )
        const offset = decodeCursor(input.cursor)
        const page = moduleOf(deps, ctx, input.project_id).listRecords({
          databaseId: input.database_id,
          filter: input.filter === undefined ? undefined : (input.filter as FilterNode),
          sorts: sortKeys(input.sort),
          includeDeleted: input.include_deleted,
          limit: input.limit,
          offset
        })
        const next = page.offset + page.items.length
        return ok({
          items: page.items.map(recordView),
          next_cursor: next < page.total ? encodeCursor(next) : null
        })
      }
    }),

    readTool({
      name: 'record_get',
      title: '记录详情',
      description: '返回一条记录及其字段值与关联笔记 ID。',
      inputSchema: { project_id: projectId, record_id: entityId },
      handler(ctx, args) {
        const input = parse(z.object({ project_id: projectId, record_id: entityId }).strict(), args)
        const record = moduleOf(deps, ctx, input.project_id).getRecord(input.record_id)
        return ok({ record: recordView(record), values: record.data, note_id: record.noteId })
      }
    }),

    writeTool({
      name: 'record_create',
      title: '创建记录',
      description: '在数据库中创建一条记录，服务会同时创建记录页笔记。',
      inputSchema: {
        project_id: projectId,
        database_id: entityId,
        values: z.record(z.string(), z.unknown())
      },
      handler(ctx, args) {
        const input = parse(
          z
            .object({
              project_id: projectId,
              database_id: entityId,
              values: z.record(z.string(), z.unknown()),
            })
            .strict(),
          args
        )
        const record = moduleOf(deps, ctx, input.project_id).createRecord({
          databaseId: input.database_id,
          values: input.values,
          actor: MCP_ACTOR
        })
        return ok({ record: recordView(record), note_id: record.noteId })
      }
    }),

    writeTool({
      name: 'record_update',
      title: '更新记录',
      description: '更新记录中提供的字段值（未提供的字段保持不变）。',
      inputSchema: {
        project_id: projectId,
        record_id: entityId,
        values: z.record(z.string(), z.unknown()),
        expected_updated_at: z.string().optional()
      },
      handler(ctx, args) {
        const input = parse(
          z
            .object({
              project_id: projectId,
              record_id: entityId,
              values: z.record(z.string(), z.unknown()),
              expected_updated_at: z.string().optional(),
            })
            .strict(),
          args
        )
        const record = moduleOf(deps, ctx, input.project_id).updateRecord({
          recordId: input.record_id,
          values: input.values,
          expectedUpdatedAt: input.expected_updated_at,
          actor: MCP_ACTOR
        })
        return ok({ record: recordView(record) })
      }
    }),

    writeTool({
      name: 'record_delete',
      title: '删除记录',
      description: '软删除记录，可选择同时删除记录页笔记。',
      inputSchema: { project_id: projectId, record_id: entityId, delete_note: z.boolean().optional() },
      annotations: { destructiveHint: true },
      handler(ctx, args) {
        const input = parse(
          z
            .object({
              project_id: projectId,
              record_id: entityId,
              delete_note: z.boolean().optional(),
            })
            .strict(),
          args
        )
        moduleOf(deps, ctx, input.project_id).deleteRecord(input.record_id, input.delete_note === true, MCP_ACTOR)
        return ok({ deleted: true })
      }
    })
  ]
}

export const NOTES_MCP_TOOL_NAMES = [
  'note_list',
  'note_get',
  'note_create',
  'note_update_title',
  'note_update_meta',
  'note_save_body',
  'note_delete',
  'database_list',
  'database_get',
  'database_create',
  'database_update',
  'database_field_create',
  'database_field_update',
  'database_field_delete',
  'database_delete',
  'record_list',
  'record_get',
  'record_create',
  'record_update',
  'record_delete'
] as const

export type NotesMcpToolName = (typeof NOTES_MCP_TOOL_NAMES)[number]
export type { ToolDefinition }