package otel

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/nethinwei/sql-mcp-server/core/bus"
)

// busInstruments are what RegisterBusMetrics observes.
type busInstruments struct {
	inflight, queued, limit, ioUsed, ioSize, tasks metric.Int64ObservableGauge
	rejected, completed                            metric.Int64ObservableCounter
}

// RegisterBusMetrics observes b through mp, such as the global
// MeterProvider (a no-op one unless Setup exported metrics): per lane smcp.bus.lane.inflight,
// .queued, .limit and the cumulative .rejected and .completed (attribute
// lane); smcp.bus.io.used and .size (attribute pool: io or aux); and
// smcp.bus.tasks per background task name (attribute task). The returned
// function stops observing.
func RegisterBusMetrics(mp metric.MeterProvider, b *bus.Bus) (func() error, error) {
	meter := mp.Meter("sql-mcp-server")
	var in busInstruments
	var errs []error
	gauge := func(name, desc string) metric.Int64ObservableGauge {
		g, err := meter.Int64ObservableGauge(name, metric.WithDescription(desc))
		errs = append(errs, err)
		return g
	}
	counter := func(name, desc string) metric.Int64ObservableCounter {
		c, err := meter.Int64ObservableCounter(name, metric.WithDescription(desc))
		errs = append(errs, err)
		return c
	}
	in.inflight = gauge("smcp.bus.lane.inflight", "Work running on a lane.")
	in.queued = gauge("smcp.bus.lane.queued", "Work waiting for a lane.")
	in.limit = gauge("smcp.bus.lane.limit", "Work a lane runs at once.")
	in.rejected = counter("smcp.bus.lane.rejected", "Work a full lane turned away.")
	in.completed = counter("smcp.bus.lane.completed", "Work a lane ran.")
	in.ioUsed = gauge("smcp.bus.io.used", "IO slots in use.")
	in.ioSize = gauge("smcp.bus.io.size", "IO slots.")
	in.tasks = gauge("smcp.bus.tasks", "Background tasks running.")
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	reg, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		observeBus(o, in, b.Stats())
		return nil
	}, in.inflight, in.queued, in.limit, in.rejected, in.completed, in.ioUsed, in.ioSize, in.tasks)
	if err != nil {
		return nil, err
	}
	return reg.Unregister, nil
}

func observeBus(o metric.Observer, in busInstruments, st bus.Stats) {
	for _, lane := range st.Lanes {
		attrs := metric.WithAttributes(attribute.String("lane", string(lane.Name)))
		o.ObserveInt64(in.inflight, int64(lane.Inflight), attrs)
		o.ObserveInt64(in.queued, int64(lane.Queued), attrs)
		o.ObserveInt64(in.limit, int64(lane.MaxConcurrent), attrs)
		o.ObserveInt64(in.rejected, lane.Rejected, attrs)
		o.ObserveInt64(in.completed, lane.Completed, attrs)
	}
	for pool, slots := range map[string]bus.SlotStats{"io": st.IO, "aux": st.Aux} {
		attrs := metric.WithAttributes(attribute.String("pool", pool))
		o.ObserveInt64(in.ioUsed, int64(slots.Used), attrs)
		o.ObserveInt64(in.ioSize, int64(slots.Size), attrs)
	}
	for _, task := range st.Tasks {
		o.ObserveInt64(in.tasks, int64(task.Running), metric.WithAttributes(attribute.String("task", task.Name)))
	}
}
