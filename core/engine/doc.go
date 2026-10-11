// Package engine enforces bounded concurrency with backpressure, request
// deduplication (singleflight), and integration with adaptive limiting and the
// circuit breaker. It uses only the standard library and core/bus: admission
// is a bus lane (the Serve lane of the process's bus, or one of the engine's
// own), IO slots a bus semaphore, singleflight a mutex-guarded call table. No
// unbounded goroutines (invariant I13); panics in submitted functions are recovered and returned as
// errors so they never crash the process.
package engine
