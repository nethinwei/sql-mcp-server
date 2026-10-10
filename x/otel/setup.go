package otel

import (
	"context"
	"errors"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/nethinwei/sql-mcp-server/version"
)

// Setup initializes the global TracerProvider and MeterProvider from the
// standard OTLP environment variables so the spans emitted by NewHooks and
// the metrics RegisterBusMetrics observes on the global MeterProvider are actually exported. Traces are
// exported when OTEL_EXPORTER_OTLP_ENDPOINT or
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is set, metrics when
// OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_METRICS_ENDPOINT is;
// otherwise the no-op global providers stay. The exporters speak OTLP over
// HTTP/protobuf; endpoint, headers, and TLS come from the standard
// OTEL_EXPORTER_OTLP_* variables read by the exporters themselves.
func Setup(ctx context.Context) (shutdown func(context.Context) error, err error) {
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", "sql-mcp-server"),
		attribute.String("service.version", version.String()),
	))
	if err != nil {
		return nil, err
	}
	stopTraces, err := setupTraces(ctx, res)
	if err != nil {
		return nil, err
	}
	stopMetrics, err := setupMetrics(ctx, res)
	if err != nil {
		_ = stopTraces(ctx)
		return nil, err
	}
	return func(ctx context.Context) error { return errors.Join(stopMetrics(ctx), stopTraces(ctx)) }, nil
}

func noShutdown(context.Context) error { return nil }

func setupTraces(ctx context.Context, res *resource.Resource) (func(context.Context) error, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return noShutdown, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

func setupMetrics(ctx context.Context, res *resource.Resource) (func(context.Context) error, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") == "" {
		return noShutdown, nil
	}
	exporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, err
	}
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)), sdkmetric.WithResource(res))
	otel.SetMeterProvider(provider)
	return provider.Shutdown, nil
}
