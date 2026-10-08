// Package introspect defines schema introspection: discovering entity metadata
// from a live database and matching configured entities to it. The
// Introspector interface lives here; providers in x/ implement it. Catalog
// resolves the table an entity reads, and Reconcile, pure logic run at
// startup, fails fast on mismatched configuration (a referenced column that no
// longer exists in the DB) and fills descriptions from database comments.
package introspect
