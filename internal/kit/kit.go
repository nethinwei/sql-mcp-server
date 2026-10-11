// Package kit holds the small value helpers the x/ packages share: pointer
// and slice conveniences that adapters (GraphQL, MCP, the store) need when
// mapping between optional API fields and Go values.
package kit

import (
	"maps"
	"slices"
)

// Deref is *s, or "" when s is nil.
func Deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// DerefBool is *b, or false when b is nil.
func DerefBool(b *bool) bool { return b != nil && *b }

// DerefInt is *n, or fallback when n is nil.
func DerefInt(n *int, fallback int) int {
	if n == nil {
		return fallback
	}
	return *n
}

// Ptr is a pointer to v.
func Ptr[T any](v T) *T { return &v }

// Optional is a pointer to s, or nil when s is empty: unset text is null.
func Optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// OrEmpty is s, or an empty slice when s is nil: a non-null API list.
func OrEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// SortedKeys are the keys of m, sorted.
func SortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
