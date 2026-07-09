// Package otel adapts the core hook.Hooks callbacks to OpenTelemetry spans and
// attributes. It is the only place that imports the otel packages; core stays
// free of any observability backend. Initialize a tracer provider in main for
// spans to be exported; with no provider, otel uses a no-op tracer.
package otel
