// Package mcp is the in-process MCP server runtime (SPEC-11 §5.1, §5.2).
//
// It owns the ports, the ToolHost arbitration pipeline, the JSON-RPC protocol
// layer (2026-07-28 with a 2025-11-25 initialize handshake), the loopback HTTP
// transport and the local IPC transport used by the stdio bridge. It must not
// import a database driver or a SQL package: every persistent concern lives
// behind the ports declared here and is implemented by internal/mcpstore. An
// architecture test enforces that boundary (SPEC-11 §8.8).
package mcp
