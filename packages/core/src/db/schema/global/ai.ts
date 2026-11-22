import { sql } from 'drizzle-orm'
import { check, index, integer, primaryKey, sqliteTable, text } from 'drizzle-orm/sqlite-core'

import { providers } from './core'

/** global.db 0100_ai_provider_models: upstream model-list cache. */
export const providerModels = sqliteTable(
  'provider_models',
  {
    providerId: text('provider_id')
      .notNull()
      .references(() => providers.id, { onDelete: 'cascade' }),
    modelId: text('model_id').notNull(),
    displayName: text('display_name').notNull(),
    capabilities: text('capabilities').notNull(),
    source: text('source').notNull(),
    available: integer('available').notNull().default(1),
    firstSeenAt: text('first_seen_at').notNull(),
    lastSeenAt: text('last_seen_at').notNull()
  },
  (table) => [
    primaryKey({ columns: [table.providerId, table.modelId] }),
    check('provider_models_source_valid', sql`source IN ('listed','manual')`),
    check('provider_models_available_boolean', sql`available IN (0,1)`),
    index('idx_provider_models_provider').on(table.providerId, table.available)
  ]
)