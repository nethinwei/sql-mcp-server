package otel

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/nethinwei/sql-mcp-server/version"
)

// Setup initializes the global TracerProvider from the standard OTLP
// environment variables so the spans emitted by NewHooks are actually
// exported. When neither OTEL_EXPORTER_OTLP_ENDPOINT nor
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is set, it leaves the no-op global
// provider untouched and returns a no-op shutdown. The exporter speaks OTLP
// over HTTP/protobuf; endpoint, headers, and TLS come from the standard
// OTEL_EXPORTER_OTLP_* variables read by the exporter itself.
func Setup(ctx context.Context) (shutdown func(context.Context) error, err error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" &&
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", "sql-mcp-server"),
		attribute.String("service.version", version.String()),
	))
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}
