// Package cost implements the defense-in-depth cost & resource gate.
//
// EXPLAIN is one optional layer, not a leash. Layers have Safety, Estimate, and
// Enforcement phases. A static PK/template bypass skips Estimate only; guards
// and deterministic caps still run. CALL is denied unless reviewed explicitly,
// and optional write/aggregate guards remain independent of EXPLAIN support.
//
// Runtime/DB-native guards sit beneath this synchronous gate:
//   - context.WithTimeout plus QueryContext/ExecContext is the portable
//     cancellation contract. Drivers are responsible for propagating it.
//   - native statement/scan timeouts are not SET per request: *sql.DB may run
//     the SET and query on different pooled connections. Configure those at the
//     database/user level. MySQL-compatible providers do set sql_safe_updates
//     through the DSN, which applies when each connection is established.
//
// Estimate failure defaults to Plan{ScanUnknown, !StatsFresh} for compatibility.
// FailClosed, RequireKnownScan, or RequireFreshStats can make it a hard reject.
package cost
