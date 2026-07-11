// Package tool defines the DML tool components and their registry. Each tool
// implements the Tool interface; Enabled(flags) decides whether it is registered
// with the MCP server (invariant I8). CostGated marks tools whose execution
// must pass the cost gate (invariant I4). Tools construct relalg.IR, which
// codegen renders; they never build SQL by hand, keeping queries inject-proof.
package tool
