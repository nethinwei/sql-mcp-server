package graph

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/scanjobs"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

// BenchmarkBuildSchemaImport builds the import page of a scan of 2000 tables
// (each referencing the previous one) with 500 of them configured.
func BenchmarkBuildSchemaImport(b *testing.B) {
	tables := make([]entity.Entity, 2000)
	cfg := &config.Config{}
	for i := range tables {
		name := fmt.Sprintf("t%d", i)
		tables[i] = entity.Entity{
			Name: name, Source: name, Schema: "public",
			Attributes: []entity.Attribute{{Name: "id"}, {Name: "parent_id"}, {Name: "name"}},
			Keys:       []entity.Key{{Name: name + "_pkey", Columns: []string{"id"}, Primary: true}},
		}
		if i > 0 {
			tables[i].ForeignKeys = []entity.ForeignKey{{
				Name: name + "_fk", Columns: []string{"parent_id"}, RefSchema: "public",
				RefRelation: fmt.Sprintf("t%d", i-1), RefColumns: []string{"id"},
			}}
		}
		if i < 500 {
			cfg.Entities = append(cfg.Entities, config.EntityConfig{Name: name, Fields: []config.FieldConfig{{Name: "id"}}})
		}
	}
	cat, err := introspect.LoadCatalog(context.Background(),
		&fakeIntrospector{tables: tables, all: []string{"public"}}, []string{"public"}, false)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if got := buildSchemaImport("default", cat, cfg); len(got.Tables) != len(tables) {
			b.Fatalf("tables = %d", len(got.Tables))
		}
	}
}

// catalogBench is a resolver over a SQLite store holding 500 schemas of 100
// tables each.
func catalogBench(b *testing.B) *Resolver {
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
	d, _ := revs.Create(ctx, revision.Draft{Payload: []byte(baseYAML)})
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
	return &Resolver{Store: revs, Scans: scans}
}

func catalogPage(b *testing.B, r *Resolver, search string, offset int, schemas ...string) {
	out, err := r.catalog(context.Background(), CatalogFilter{Search: &search, Schemas: schemas}, nil, offset, 50)
	if err != nil || out.Total != 5000 {
		b.Fatalf("total = %v, %v", out, err)
	}
}

// BenchmarkCatalogPage reads pages of a catalog of 50,000 tables kept in a
// SQLite store: the first read decodes every schema, later ones read only the
// versions and the cached rows of the filter.
func BenchmarkCatalogPage(b *testing.B) {
	r := catalogBench(b)
	start := time.Now()
	catalogPage(b, r, "t_05", 0)
	b.Logf("first page, decoding 50,000 tables: %v", time.Since(start))
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		catalogPage(b, r, "t_05", (i%100)*50)
		i++
	}
}

// BenchmarkCatalogPageParallel pages through the catalog from many
// goroutines at once (GOMAXPROCS of them): every other request uses a filter
// no request used before, so its rows are not cached. ns/op is throughput
// (wall time over requests); p50-ms and p95-ms are the latencies of the
// requests, timed one by one.
func BenchmarkCatalogPageParallel(b *testing.B) {
	r := catalogBench(b)
	catalogPage(b, r, "t_05", 0)
	var fresh atomic.Int64
	var mu sync.Mutex
	var latencies []time.Duration
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		var mine []time.Duration
		for i := 0; pb.Next(); i++ {
			start := time.Now()
			if i%2 == 1 { // a filter never seen: the same rows under a new cache key
				catalogPage(b, r, "t_05", (i%100)*50, fmt.Sprintf("db_*%d", fresh.Add(1)), "db_*")
			} else {
				catalogPage(b, r, "t_05", (i%100)*50)
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
