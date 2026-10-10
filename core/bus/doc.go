// Package bus is the process's execution bus: every piece of work that
// reaches a database runs on one of its lanes, and every background task is
// started through it, so what the process runs is bounded, visible and
// stopped together.
//
// A lane bounds concurrent work and the work waiting for it. All lanes draw
// on one IO semaphore; the lanes other than Serve share an auxiliary half of
// it, so administration, scans and background work never take the whole IO
// pool from tool calls. Work admitted on a lane carries the admission in its
// context, and Ensure runs nested work under it instead of taking a second
// slot.
//
// Go and AfterFunc start background tasks: each is named, counted, recovers
// from panics, sees its context canceled on Close, and Close waits for it.
// The bus uses only the standard library.
package bus
