import { sql } from 'drizzle-orm'
import { check, index, integer, sqliteTable, text } from 'drizzle-orm/sqlite-core'

/** project.db 0100_ai_outputs: one row per generation attempt, immutable content history. */
export const aiOutputs = sqliteTable(
  'ai_outputs',
  {
    id: text('id').primaryKey(),
    taskKind: text('task_kind').notNull(),
    entityType: text('entity_type'),
    entityId: text('entity_id'),
    status: text('status').notNull(),
    contentMd: text('content_md').notNull().default(''),
    providerId: text('provider_id').notNull(),
    providerName: text('provider_name').notNull(),
    protocol: text('protocol').notNull(),
    modelId: text('model_id').notNull(),
    promptVersion: text('prompt_version').notNull(),
    sourceRefs: text('source_refs').notNull().default('[]'),
    parameters: text('parameters').notNull().default('{}'),
    usage: text('usage'),
    humanEdited: integer('human_edited').notNull().default(0),
    createdAt: text('created_at').notNull(),
    confirmedAt: text('confirmed_at')
  },
  (table) => [
    check('ai_outputs_status_valid', sql`status IN ('draft','confirmed')`),
    check(
      'ai_outputs_protocol_valid',
      sql`protocol IN ('openai_chat','openai_responses','gemini','anthropic')`
    ),
    check('ai_outputs_prompt_version_nonempty', sql`length(prompt_version) > 0`),
    check('ai_outputs_human_edited_boolean', sql`human_edited IN (0,1)`),
    index('idx_ai_outputs_entity').on(table.entityType, table.entityId, table.createdAt),
    index('idx_ai_outputs_created').on(table.createdAt)
  ]
)