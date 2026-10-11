package otel

import (
	"context"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/nethinwei/sql-mcp-server/core/bus"
)

func TestRegisterBusMetricsObservesLanes(t *testing.T) {
	t.Parallel()
	b := bus.New(4, map[bus.LaneName]bus.Limits{bus.Scan: {MaxConcurrent: 3}})
	defer func() { _ = b.Close(context.Background()) }()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	stop, err := RegisterBusMetrics(mp, b)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stop() }()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	limits := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "smcp.bus.lane.limit" {
				continue
			}
			for _, p := range m.Data.(metricdata.Gauge[int64]).DataPoints {
				lane, _ := p.Attributes.Value("lane")
				limits[lane.AsString()] = p.Value
			}
		}
	}
	if limits["scan"] != 3 || limits["admin"] != 4 || len(limits) != len(bus.Lanes) {
		t.Fatalf("lane limits = %v", limits)
	}
}
