package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	coreprovider "github.com/nethinwei/sql-mcp-server/core/provider"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// sharedTestDriver opens counting providers on a one-table schema.
const sharedTestDriver = "bootstrap-shared-test"

var sharedOpened, sharedClosed, sharedQueries atomic.Int32

type countingProvider struct{ benchProvider }

func (p countingProvider) QueryContext(ctx context.Context, q string, args ...any) (store.Rows, error) {
	sharedQueries.Add(1)
	return p.benchProvider.QueryContext(ctx, q, args...)
}

func (p countingProvider) Close() error { sharedClosed.Add(1); return nil }

func init() {
	_, tables := benchConfig(1)
	providerregistry.Register(sharedTestDriver, func(string, providerregistry.Options) (coreprovider.Provider, error) {
		sharedOpened.Add(1)
		return countingProvider{benchProvider{
			fakeProvider: &fakeProvider{dialect: postgres.Dialect{}, FakeDB: store.FakeDB{
				ExecFn: func(context.Context, string, ...any) (store.Result, error) {
					return store.Result{RowsAffected: 1}, nil
				},
			}}, tables: tables,
			rows: [][]any{{int64(1), "alice@x.com"}},
		}}, nil
	})
}

// published assembles cfg on shared and applies it as a Runtime publishing it
// does.
func published(t *testing.T, shared *Shared, cfg *config.Config) *App {
	t.Helper()
	app, err := shared.Assemble(cfg)
	if err != nil {
		t.Fatal(err)
	}
	app.publishShared()
	t.Cleanup(func() { _ = app.Close() })
	return app
}

func sharedConfig(dsn string) *config.Config {
	cfg, _ := benchConfig(1)
	cfg.Databases = map[string]config.DatabaseConfig{"main": {Driver: sharedTestDriver, DSN: dsn}}
	cfg.Entities[0].Roles.Update = []string{"writer"}
	cfg.Cache.Enabled, cfg.Cache.TTL = true, time.Minute
	return cfg
}

// Assemblies on shared services reuse the connections whose settings did
// not change and close a connection when no App holds it any more.
func TestSharedReusesConnections(t *testing.T) { //nolint:paralleltest // counts a shared driver's opens
	shared := NewShared()
	opened, closed := sharedOpened.Load(), sharedClosed.Load()
	first, err := shared.Assemble(sharedConfig("a"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := shared.Assemble(sharedConfig("a"))
	if err != nil {
		t.Fatal(err)
	}
	moved, err := shared.Assemble(sharedConfig("b"))
	if err != nil {
		t.Fatal(err)
	}
	if got := sharedOpened.Load() - opened; got != 2 {
		t.Fatalf("opened %d connections, want one per distinct setting", got)
	}
	_ = first.Close()
	if sharedClosed.Load() != closed {
		t.Fatal("a connection another App holds must stay open")
	}
	_ = second.Close()
	_ = moved.Close()
	if got := sharedClosed.Load() - closed; got != 2 {
		t.Fatalf("closed %d connections, want 2", got)
	}
}

// A write through the configuration being replaced invalidates what the new
// configuration cached: both read and invalidate one cache by physical
// database.
func TestWriteOnOldGenerationInvalidatesNewOne(t *testing.T) { //nolint:paralleltest // counts a shared driver's queries
	shared := NewShared()
	old := published(t, shared, sharedConfig("a"))
	next := published(t, shared, sharedConfig("a"))
	ctx := context.Background()
	read := func() {
		if _, err := tool.RunTool(ctx, tool.ReadTool{}, benchPointRead, next.ToolContext("reader")); err != nil {
			t.Fatal(err)
		}
	}
	read()
	before := sharedQueries.Load()
	read()
	if sharedQueries.Load() != before {
		t.Fatal("the second read must be served from the cache")
	}
	update := json.RawMessage(
		`{"entity":"users","filter":[{"field":"id","op":"eq","value":1}],"set":{"email":"bob@x.com"}}`)
	if _, err := tool.RunTool(ctx, tool.UpdateTool{}, update, old.ToolContext("writer")); err != nil {
		t.Fatal(err)
	}
	before = sharedQueries.Load()
	read()
	if sharedQueries.Load() == before {
		t.Fatal("a write through the old configuration must invalidate the new one's cached read")
	}
}

// The configuration being replaced and the new one share the IO pool: while
// the old one's work holds the only slot, the new one's waits.
func TestGenerationsShareTheIOPool(t *testing.T) { //nolint:paralleltest // uses the shared test driver
	shared := NewShared()
	cfg := sharedConfig("io")
	cfg.RateLimit.IOPool = 1
	cfg.RateLimit.Enabled = new(false) // the IO slots alone, without the adaptive limiter
	old := published(t, shared, cfg)
	next := published(t, shared, cfg) // the reload keeps the IO pool at 1
	hold, held := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = old.Engine.Submit(context.Background(), "", func(context.Context) (any, error) {
			close(held)
			<-hold
			return nil, nil
		})
	}()
	<-held
	defer close(hold)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := next.Engine.Submit(ctx, "", func(context.Context) (any, error) { return nil, nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the new configuration ran past the shared IO pool: %v", err)
	}
}

// A configuration with caching off still invalidates what a configuration
// with caching on stored.
// Counts a shared driver's queries, so not parallel.
func TestWriteWithCacheOffInvalidatesSharedCache(t *testing.T) {
	shared := NewShared()
	off := sharedConfig("off")
	off.Cache.Enabled = false
	old := published(t, shared, off)
	next := published(t, shared, sharedConfig("off"))
	ctx := context.Background()
	read := func() {
		if _, err := tool.RunTool(ctx, tool.ReadTool{}, benchPointRead, next.ToolContext("reader")); err != nil {
			t.Fatal(err)
		}
	}
	read()
	update := json.RawMessage(
		`{"entity":"users","filter":[{"field":"id","op":"eq","value":1}],"set":{"email":"bob@x.com"}}`)
	if _, err := tool.RunTool(ctx, tool.UpdateTool{}, update, old.ToolContext("writer")); err != nil {
		t.Fatal(err)
	}
	before := sharedQueries.Load()
	read()
	if sharedQueries.Load() == before {
		t.Fatal("a write through the configuration with caching off must invalidate the shared cache")
	}
}

// A reload that fails after assembling leaves the shared settings as the
// serving configuration set them: here the IO pool stays at 1.
func TestFailedReloadKeepsSharedSettings(t *testing.T) { //nolint:paralleltest // uses the shared test driver
	shared := NewShared()
	one := sharedConfig("failed")
	one.RateLimit.IOPool, one.RateLimit.Enabled = 1, new(false)
	startup, err := shared.Assemble(one)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntimeWithBuilder(startup, func(string) (*App, error) {
		two := sharedConfig("failed")
		two.RateLimit.IOPool, two.RateLimit.Enabled = 2, new(false)
		app, err := shared.Assemble(two)
		if err != nil {
			return nil, err
		}
		_ = app.Close() // a later step of the reload fails
		return nil, errors.New("audit sink unavailable")
	})
	defer runtime.Close()
	if err := runtime.Reload("ignored"); err == nil {
		t.Fatal("the reload must fail")
	}
	hold, held := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = startup.Engine.Submit(context.Background(), "", func(context.Context) (any, error) {
			close(held)
			<-hold
			return nil, nil
		})
	}()
	<-held
	defer close(hold)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := startup.Engine.Submit(ctx, "", func(context.Context) (any, error) { return nil, nil }); err == nil {
		t.Fatal("a failed reload must not raise the serving IO pool")
	}
}
