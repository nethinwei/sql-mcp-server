// Package cost implements the defense-in-depth cost & resource gate.
//
// EXPLAIN is one optional layer, not a leash. The gate chains layers in order:
// StaticRule (PK-point whitelist, no EXPLAIN) -> Estimate (optional EXPLAIN
// pre-filter, only when the dialect's estimates are trustworthy) -> EnforceCap
// (deterministic LIMIT injection, independent of estimate correctness). Any
// layer may reject; a single layer's failure is not fatal. Runtime guards
// (statement_timeout, max_read_size) and DB-native limits are applied at
// execution time by providers, beneath this synchronous gate.
//
// Estimate failure (bad EXPLAIN, plan drift) degrades to Plan{ScanUnknown,
// !StatsFresh} rather than panicking (Murphy); RequireKnownScan/RequireFreshStats
// decide whether that degrades to a hard reject.
package cost
