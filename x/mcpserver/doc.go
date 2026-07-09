// Package mcpserver bridges core DML tools to the MCP go-sdk. It registers the
// enabled tools with an mcp.Server, adapts CallToolRequest to tool.Run, and
// maps core errors to MCP results: business errors (unauthorized, cost-exceeded,
// unsafe-write, not-found) become IsError results with actionable text, while
// overload/circuit/internal errors become protocol-level errors.
package mcpserver
