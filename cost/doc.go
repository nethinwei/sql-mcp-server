// Package cost implements the defense-in-depth cost & resource gate.
//
// EXPLAIN is one optional layer, not a leash. The gate chains layers in order:
// StaticRule (PK-point whitelist, no EXPLAIN) -> Estimate (optional EXPLAIN
// pre-filter, only when the dialect's estimates are trustworthy) -> EnforceCap
// (deterministic LIMIT injection, independent of estimate correctness). Any
// layer may reject; a single layer's failure is not fatal.
//
// Runtime/DB-native guards sit beneath this synchronous gate:
//   - statement_timeout is enforced by context.WithTimeout around each DB call
//     (the driver cancels the query when the deadline fires).
//   - max_read_size / sql_safe_updates are DB session variables; *sql.DB pools
//     have no per-connection init hook, so these are not auto-set here. Set them
//     at the database/user level for production hardening.
//
// Estimate failure (bad EXPLAIN, plan drift) degrades to Plan{ScanUnknown,
// !StatsFresh} rather than panicking (Murphy); RequireKnownScan/RequireFreshStats
// decide whether that degrades to a hard reject.
package cost
