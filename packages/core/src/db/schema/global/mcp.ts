import { sql } from 'drizzle-orm'
import { check, index, integer, primaryKey, sqliteTable, text } from 'drizzle-orm/sqlite-core'

/** global.db 0350_mcp_runtime: confirmations, jobs, clients and idempotency keys. */
export const mcpConfirmation = sqliteTable(
  'mcp_confirmation',
  {
    id: text('id').primaryKey(),
    tool: text('tool').notNull(),
    argsJson: text('args_json').notNull().default('{}'),
    argsHash: text('args_hash').notNull(),
    title: text('title').notNull().default(''),
    summary: text('summary').notNull().default(''),
    note: text('note').notNull().default(''),
    impactJson: text('impact_json'),
    projectId: text('project_id'),
    clientId: text('client_id').notNull(),
    state: text('state').notNull(),
    createdAt: text('created_at').notNull(),
    expiresAt: text('expires_at').notNull(),
    resolvedAt: text('resolved_at'),
    resolvedBy: text('resolved_by'),
    resultJson: text('result_json'),
    errorCode: text('error_code'),
    traceId: text('trace_id')
  },
  (table) => [
    check(
      'mcp_confirmation_state_valid',
      sql`state IN ('requested','approved','executing','executed','failed','denied','expired','cancelled')`
    ),
    index('idx_mcp_confirmation_state_created').on(table.state, table.createdAt),
    index('idx_mcp_confirmation_client_created').on(table.clientId, table.createdAt),
    index('idx_mcp_confirmation_project_created').on(table.projectId, table.createdAt)
  ]
)

export const mcpJob = sqliteTable(
  'mcp_job',
  {
    id: text('id').primaryKey(),
    tool: text('tool').notNull(),
    projectId: text('project_id'),
    clientId: text('client_id').notNull(),
    state: text('state').notNull(),
    progress: integer('progress').notNull().default(0),
    stage: text('stage').notNull().default(''),
    readOnly: integer('read_only').notNull().default(0),
    paramsJson: text('params_json'),
    resultJson: text('result_json'),
    errorJson: text('error_json'),
    createdAt: text('created_at').notNull(),
    startedAt: text('started_at'),
    finishedAt: text('finished_at'),
    traceId: text('trace_id')
  },
  (table) => [
    check('mcp_job_state_valid', sql`state IN ('queued','running','succeeded','failed','cancelled')`),
    check('mcp_job_progress_range', sql`progress BETWEEN 0 AND 100`),
    check('mcp_job_read_only_boolean', sql`read_only IN (0,1)`),
    index('idx_mcp_job_state_created').on(table.state, table.createdAt),
    index('idx_mcp_job_project_created').on(table.projectId, table.createdAt),
    index('idx_mcp_job_client_created').on(table.clientId, table.createdAt)
  ]
)

export const mcpClient = sqliteTable(
  'mcp_client',
  {
    id: text('id').primaryKey(),
    name: text('name').notNull(),
    tokenHash: text('token_hash').notNull().unique(),
    transport: text('transport').notNull(),
    createdAt: text('created_at').notNull(),
    lastSeenAt: text('last_seen_at'),
    enabled: integer('enabled').notNull().default(1)
  },
  (table) => [
    check('mcp_client_transport_valid', sql`transport IN ('http','stdio')`),
    check('mcp_client_enabled_boolean', sql`enabled IN (0,1)`)
  ]
)

export const mcpIdempotency = sqliteTable(
  'mcp_idempotency',
  {
    clientId: text('client_id').notNull(),
    key: text('key').notNull(),
    tool: text('tool').notNull(),
    argsHash: text('args_hash').notNull(),
    state: text('state').notNull(),
    resultJson: text('result_json'),
    createdAt: text('created_at').notNull(),
    expiresAt: text('expires_at').notNull()
  },
  (table) => [
    primaryKey({ columns: [table.clientId, table.key] }),
    check('mcp_idempotency_state_valid', sql`state IN ('pending','completed')`),
    index('idx_mcp_idempotency_expires').on(table.expiresAt)
  ]
)