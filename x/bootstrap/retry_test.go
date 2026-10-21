package bootstrap

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/tool"
)

// A revision that keeps failing is tried after a doubling delay, up to
// maxRetryDelay; another revision is tried at once.
func TestRetryBackoffSpacesAttempts(t *testing.T) {
	t.Parallel()
	b := retryBackoff[int64]{base: time.Second}
	now := time.Unix(0, 0)
	if !b.due(7, now) {
		t.Fatal("a revision never tried is due")
	}
	for _, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		b.failed(7, now)
		if b.due(7, now.Add(wait-time.Millisecond)) || !b.due(7, now.Add(wait)) {
			t.Fatalf("after a failure the next attempt is due in %v", wait)
		}
		if !b.due(8, now) {
			t.Fatal("a newly published revision is due at once")
		}
		now = now.Add(wait)
	}
	for range 20 {
		b.failed(7, now)
	}
	if b.delay != maxRetryDelay {
		t.Fatalf("delay = %v, want it capped at %v", b.delay, maxRetryDelay)
	}
}

// Polling a revision that keeps failing does not rebuild it on every poll,
// and a fix published meanwhile applies on the next one.
func TestWatchStoreBacksOffAFailingRevision(t *testing.T) {
	t.Parallel()
	store := revision.NewMemoryStore(nil)
	first := publishPayload(t, store, "ok-1")
	runtime := NewRuntimeWithBuilder(&App{DefaultRole: "ok-1"}, nil)
	defer runtime.Close()
	var attempts atomic.Int32
	build := func(rev revision.Revision) (*App, error) {
		if string(rev.Payload) == "broken" {
			attempts.Add(1)
			return nil, errors.New("assemble failed")
		}
		return &App{DefaultRole: string(rev.Payload)}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runtime.WatchStore(ctx, store, first.ID, time.Millisecond, build, nil) }()

	publishPayload(t, store, "broken")
	waitFor(t, func() bool { return attempts.Load() > 0 })
	time.Sleep(100 * time.Millisecond) // about 100 polls
	if n := attempts.Load(); n > 10 {
		t.Fatalf("a failing revision was rebuilt %d times in 100 polls", n)
	}
	publishPayload(t, store, "ok-2")
	waitFor(t, func() bool { return runtime.Current().DefaultRole == "ok-2" })
}

// While a datasource of the served revision is unreachable, the revision is
// assembled again, so its entities come back without a new publish.
func TestWatchStoreReassemblesWhileADatasourceIsUnreachable(t *testing.T) {
	t.Parallel()
	store := revision.NewMemoryStore(nil)
	first := publishPayload(t, store, "ok-1")
	runtime := NewRuntimeWithBuilder(&App{DefaultRole: "ok-1"}, nil)
	defer runtime.Close()
	var builds atomic.Int32
	build := func(revision.Revision) (*App, error) {
		if builds.Add(1) <= 3 {
			return &App{DefaultRole: "down", Unreachable: map[string]string{"legacy": "connection refused"}}, nil
		}
		return &App{DefaultRole: "up"}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runtime.WatchStore(ctx, store, first.ID, time.Millisecond, build, nil) }()

	second := publishPayload(t, store, "ok-2")
	waitFor(t, func() bool { return runtime.Current().DefaultRole == "up" })
	if runtime.AppliedRevision() != second.ID {
		t.Fatalf("applied = %d, want %d", runtime.AppliedRevision(), second.ID)
	}
	n := builds.Load()
	time.Sleep(20 * time.Millisecond)
	if builds.Load() != n {
		t.Fatal("a revision served in full was assembled again")
	}
}

// A call that finds the schema changed has the served revision assembled
// again, without a new publish, so the entity it broke is reported
// unavailable; nothing is assembled again until another call finds it.
func TestWatchStoreReassemblesOnSchemaDrift(t *testing.T) {
	t.Parallel()
	store := revision.NewMemoryStore(nil)
	first := publishPayload(t, store, "ok-1")
	drift := &tool.SchemaDrift{}
	runtime := NewRuntimeWithBuilder(&App{DefaultRole: "ok-1", Drift: drift}, nil)
	defer runtime.Close()
	var builds atomic.Int32
	build := func(revision.Revision) (*App, error) {
		builds.Add(1)
		return &App{DefaultRole: "rebuilt", Drift: drift,
			Unavailable: map[string]string{"shop.orders": TableMissing}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runtime.WatchStore(ctx, store, first.ID, time.Millisecond, build, nil) }()

	time.Sleep(20 * time.Millisecond)
	if builds.Load() != 0 {
		t.Fatal("a revision served in full was assembled again")
	}
	drift.Report()
	waitFor(t, func() bool { return runtime.Current().DefaultRole == "rebuilt" })
	if runtime.AppliedRevision() != first.ID || drift.Pending() {
		t.Fatalf("applied %d, drift pending %v", runtime.AppliedRevision(), drift.Pending())
	}
	n := builds.Load()
	time.Sleep(20 * time.Millisecond)
	if builds.Load() != n {
		t.Fatal("assembled again without another drift")
	}
}
