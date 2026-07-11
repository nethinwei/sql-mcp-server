// Package cost implements the defense-in-depth cost & resource gate.
//
// EXPLAIN is one optional layer, not a leash. The gate chains layers in order:
// StaticRule (PK-point whitelist, no EXPLAIN) -> Estimate (optional EXPLAIN
// pre-filter, only when the dialect's estimates are trustworthy) -> EnforceCap
// (deterministic LIMIT injection, independent of estimate correctness). Any
// layer may reject; a single layer's failure is not fatal.
//
// Runtime/DB-native guards sit beneath this synchronous gate:
//   - context.WithTimeout plus QueryContext/ExecContext is the portable
//     cancellation contract. Drivers are responsible for propagating it.
//   - native statement/scan timeouts are not SET per request: *sql.DB may run
//     the SET and query on different pooled connections. Configure those at the
//     database/user level. MySQL-compatible providers do set sql_safe_updates
//     through the DSN, which applies when each connection is established.
//
// Estimate failure (bad EXPLAIN, plan drift) degrades to Plan{ScanUnknown,
// !StatsFresh} rather than panicking (Murphy); RequireKnownScan/RequireFreshStats
// decide whether that degrades to a hard reject.
package cost
