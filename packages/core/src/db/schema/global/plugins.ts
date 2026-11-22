import { sql } from 'drizzle-orm'
import { check, index, integer, primaryKey, sqliteTable, text, uniqueIndex } from 'drizzle-orm/sqlite-core'

/**
 * global.db 0500_plugins: installed plugin registry (plan §3.7). The manifest
 * snapshot, approved grants and derived contributions are stored as JSON text;
 * the service boundary parses them explicitly to preserve tolerance.
 */
export const plugins = sqliteTable(
  'plugins',
  {
    id: text('id').primaryKey(),
    name: text('name').notNull(),
    version: text('version').notNull(),
    apiVersion: integer('api_version').notNull(),
    description: text('description').notNull().default(''),
    path: text('path').notNull(),
    enabled: integer('enabled').notNull().default(0),
    permissionsJson: text('permissions_json').notNull().default('[]'),
    grantedJson: text('granted_json').notNull().default('[]'),
    networkDomainsJson: text('network_domains_json').notNull().default('[]'),
    contributionsJson: text('contributions_json').notNull().default('{}'),
    renderer: text('renderer'),
    warningsJson: text('warnings_json').notNull().default('[]'),
    lastError: text('last_error'),
    installedAt: text('installed_at').notNull(),
    updatedAt: text('updated_at').notNull(),
    sourceType: text('source_type').notNull().default('local'),
    sourceUrl: text('source_url').notNull().default(''),
    registryId: text('registry_id').notNull().default(''),
    autoUpdate: integer('auto_update').notNull().default(1),
    availableVersion: text('available_version'),
    updateRequiresApproval: integer('update_requires_approval').notNull().default(0),
    lastCheckedAt: text('last_checked_at')
  },
  (table) => [
    check('plugins_api_version', sql`api_version = 1`),
    check('plugins_name_length', sql`length(name) > 0`),
    check('plugins_enabled_boolean', sql`enabled IN (0,1)`),
    check('plugins_source_type', sql`source_type IN ('local', 'registry', 'github')`),
    check('plugins_auto_update_boolean', sql`auto_update IN (0, 1)`),
    check('plugins_update_requires_approval_boolean', sql`update_requires_approval IN (0, 1)`),
    uniqueIndex('idx_plugins_path').on(table.path),
    index('idx_plugins_enabled').on(table.enabled)
  ]
)

/**
 * Global.db 0502_plugin_market: registry index sources. The
 * document body is cached verbatim with its validator so refresh stays
 * conditional (`If-None-Match`) and browsing works offline.
 */
export const pluginRegistries = sqliteTable('plugin_registries', {
  id: text('id').primaryKey(),
  name: text('name').notNull(),
  url: text('url').notNull().unique(),
  enabled: integer('enabled').notNull().default(1),
  etag: text('etag'),
  cachedJson: text('cached_json'),
  fetchedAt: text('fetched_at')
}, (table) => [check('plugin_registries_enabled_boolean', sql`enabled IN (0, 1)`)])

/**
 * Global.db 0501_plugin_storage: plugin private KV storage.
 * `scope_key` is 'global' or 'project:<projectId>'; the row's plugin_id is
 * host-injected, so namespaces cannot overlap. Quotas are enforced in the
 * store layer, not by SQL constraints beyond the composite primary key.
 */
export const pluginKv = sqliteTable(
  'plugin_kv',
  {
    pluginId: text('plugin_id')
      .notNull()
      .references(() => plugins.id, { onDelete: 'cascade' }),
    scopeKey: text('scope_key').notNull(),
    key: text('key').notNull(),
    valueJson: text('value_json').notNull(),
    byteSize: integer('byte_size').notNull(),
    updatedAt: text('updated_at').notNull()
  },
  (table) => [
    primaryKey({ columns: [table.pluginId, table.scopeKey, table.key] }),
    check('plugin_kv_byte_size_nonnegative', sql`byte_size >= 0`),
    index('idx_plugin_kv_plugin_scope').on(table.pluginId, table.scopeKey)
  ]
)
/**
 * global.db 0504_plugin_watched_dirs: explicit user grants for plugin folder
 * access. `id` is the opaque `dirId`; the plugin never reads
 * `dir_path` directly.
 */
export const pluginWatchedDirs = sqliteTable(
  'plugin_watched_dirs',
  {
    id: text('id').primaryKey(),
    pluginId: text('plugin_id')
      .notNull()
      .references(() => plugins.id, { onDelete: 'cascade' }),
    projectId: text('project_id'),
    dirPath: text('dir_path').notNull(),
    displayName: text('display_name').notNull(),
    createdAt: text('created_at').notNull()
  },
  (table) => [
    index('idx_plugin_watched_dirs_plugin').on(table.pluginId, table.projectId)
  ]
)
