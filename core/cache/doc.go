// Package cache defines a generic read-result cache with per-relation
// invalidation. A write invalidates the relation it changed (and the
// database's derived entries, such as views) precisely, rather than relying on
// blind TTL expiry (Oracle result-cache semantics). Entries are keyed by the
// physical relation, not the entity, so every path to a table sees its writes.
package cache
