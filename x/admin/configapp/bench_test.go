package configapp

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/scanjobs"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
	_ "github.com/nethinwei/sql-mcp-server/x/providers/postgres" // the benchmark's datasource
)

// catalogBench is the service over a SQLite store holding 500 schemas of 100
// tables each, with an administrator signed in.
func catalogBench(b *testing.B) (*Service, context.Context) {
	b.Helper()
	b.Setenv("SHOP_DSN", "postgres://localhost/shop")
	ctx := context.Background()
	spec := configstore.Spec{Driver: "sqlite", DSN: filepath.Join(b.TempDir(), "store.db")}
	if err := configstore.Init(ctx, spec, nil); err != nil {
		b.Fatal(err)
	}
	store, err := configstore.Open(ctx, spec, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	revs := revision.NewMemoryStore(nil)
	payload := "databases:\n  shop:\n    driver: postgres\n    dsn: ${SHOP_DSN}\n"
	d, _ := revs.Create(ctx, revision.Draft{Payload: []byte(payload)})
	if _, err := revs.Publish(ctx, d.ID, 0, revision.Meta{}); err != nil {
		b.Fatal(err)
	}
	cfg, err := bootstrap.LoadRevision(d)
	if err != nil {
		b.Fatal(err)
	}
	if err := store.PutCatalogSchemas(ctx, sourceOf(cfg, "shop").ID(), "shop", largeSchemas()); err != nil {
		b.Fatal(err)
	}
	scans := NewScans(scanjobs.Options{}, store)
	b.Cleanup(scans.Close)
	return &Service{Store: revs, Scans: scans},
		auth.WithPrincipal(ctx, auth.Principal{Username: "root", Permissions: []string{auth.PermAll}})
}

func catalogPage(ctx context.Context, b *testing.B, r *Service, search string, offset int, schemas ...string) {
	out, err := r.Catalog(ctx, CatalogFilter{Search: &search, Schemas: schemas}, Against{}, offset, 50)
	if err != nil || out.Total != 5000 {
		b.Fatalf("total = %v, %v", out, err)
	}
}

// BenchmarkCatalogPage reads pages of a catalog of 50,000 tables kept in a
// SQLite store: the first read decodes every schema, later ones read only the
// versions and the cached rows of the filter.
func BenchmarkCatalogPage(b *testing.B) {
	r, ctx := catalogBench(b)
	start := time.Now()
	catalogPage(ctx, b, r, "t_05", 0)
	b.Logf("first page, decoding 50,000 tables: %v", time.Since(start))
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		catalogPage(ctx, b, r, "t_05", (i%100)*50)
		i++
	}
}

// BenchmarkCatalogPageParallel pages through the catalog from many
// goroutines at once (GOMAXPROCS of them): every other request uses a filter
// no request used before, so its rows are not cached. ns/op is throughput
// (wall time over requests); p50-ms and p95-ms are the latencies of the
// requests, timed one by one.
func BenchmarkCatalogPageParallel(b *testing.B) {
	r, ctx := catalogBench(b)
	catalogPage(ctx, b, r, "t_05", 0)
	var fresh atomic.Int64
	var mu sync.Mutex
	var latencies []time.Duration
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		var mine []time.Duration
		for i := 0; pb.Next(); i++ {
			start := time.Now()
			if i%2 == 1 { // a filter never seen: the same rows under a new cache key
				catalogPage(ctx, b, r, "t_05", (i%100)*50, fmt.Sprintf("db_*%d", fresh.Add(1)), "db_*")
			} else {
				catalogPage(ctx, b, r, "t_05", (i%100)*50)
			}
			mine = append(mine, time.Since(start))
		}
		mu.Lock()
		latencies = append(latencies, mine...)
		mu.Unlock()
	})
	slices.Sort(latencies)
	if n := len(latencies); n > 0 {
		b.ReportMetric(float64(latencies[n/2].Microseconds())/1000, "p50-ms")
		b.ReportMetric(float64(latencies[n*95/100].Microseconds())/1000, "p95-ms")
	}
}

// largeSchemas is 500 scanned schemas of 100 tables each.
func largeSchemas() []catalog.Schema {
	var schemas []catalog.Schema
	for s := range 500 {
		sc := catalog.Schema{Name: fmt.Sprintf("db_%03d", s), ScannedAt: time.Now()}
		for t := range 100 {
			sc.Tables = append(sc.Tables, catalog.Table{Schema: sc.Name, Name: fmt.Sprintf("t_%03d", t),
				Columns: []catalog.Column{{Name: "id", Type: "bigint"}, {Name: "phone", Type: "text"}},
				Keys:    []catalog.Key{{Name: "pk", Columns: []string{"id"}, Primary: true}}})
		}
		schemas = append(schemas, sc)
	}
	return schemas
}
