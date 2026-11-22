/**
 * MCP host entry point for the Electron main process.
 *
 * The implementation lives in `@researchos/core/modules/mcp/sdk-server`: core
 * owns the `@modelcontextprotocol/sdk` and `zod` dependencies, and the main
 * process must not pull raw SDK types through its externalized dependency
 * graph. Wiring code imports this module exactly as before.
 */
export {
  MCP_HOST,
  MCP_PATH,
  MCP_PORT,
  McpHttpServer,
  prepareArguments,
  type McpServerOptions
} from '@researchos/core/modules/mcp/sdk-server'