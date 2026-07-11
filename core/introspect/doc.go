// Package introspect defines schema introspection: discovering entity metadata
// from a live database, and detecting drift between configured and discovered
// schemas. The Introspector interface lives here; providers in x/ implement it.
// DetectDrift is pure logic and runs at startup to fail fast on mismatched
// configuration (a referenced column that no longer exists in the DB).
package introspect
