// Package cache defines a generic read-result cache with per-table invalidation.
// Writes (create/update/delete) call Invalidate(table) to drop entries for that
// table precisely, rather than relying on blind TTL expiry (Oracle result-cache
// semantics). TTLCache uses sync.Map and lazy expiry; weak-pointer soft caching
// is a future refinement.
package cache
