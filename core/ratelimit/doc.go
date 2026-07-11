// Package ratelimit provides adaptive concurrency control (AIMD) and a circuit
// breaker. The engine uses these to bound DB load and fail fast when the
// database is unhealthy. Implementations use only the standard library.
package ratelimit
