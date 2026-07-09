// Package engine enforces bounded concurrency with backpressure, request
// deduplication (singleflight), and integration with adaptive limiting and the
// circuit breaker. It uses only the standard library: semaphores are buffered
// channels, singleflight is a mutex-guarded call table. No unbounded goroutines
// (invariant I13); panics in submitted functions are recovered and returned as
// errors so they never crash the process.
package engine
