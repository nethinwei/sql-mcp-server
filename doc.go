// Package sqlmcp provides a SQL MCP server: a relationally complete
// data access surface for AI agents over PostgreSQL, MySQL, and OceanBase,
// modeled on Microsoft's Data API Builder SQL MCP Server.
//
// Design pillars:
//   - Relational algebra IR (core/relalg + core/codegen) — dialect-neutral,
//     mathematically complete; new databases plug in through core/provider.
//   - Defense-in-depth cost & resource gate — EXPLAIN is one optional layer,
//     not a leash; deterministic LIMIT/timeout and DB-native resource limits
//     backstop unreliable estimates.
//   - Enterprise safety — RBAC with row-level security, non-blocking audit,
//     and field masking.
//   - Bounded concurrency with backpressure — no unbounded goroutines.
//
// The core packages depend only on the standard library. External dependencies
// (the MCP SDK, database drivers, OpenTelemetry, YAML) live under x/ and depend
// on core, never the reverse. See CONTRIBUTING.md for the coding standard.
package sqlmcp
