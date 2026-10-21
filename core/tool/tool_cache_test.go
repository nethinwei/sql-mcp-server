package tool

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

func TestWriteInvalidatesByRelation(t *testing.T) {
	t.Parallel()
	orders := entity.Entity{Name: "orders", Source: "orders"}
	items := entity.Entity{Name: "items", Source: "items"}
	daily := entity.Entity{Name: "daily", Source: "daily", Kind: entity.KindView, Derived: true}
	other := entity.Entity{Name: "remote", Source: "orders", DataSource: "archive"}
	refreshAll := entity.Entity{Name: "refresh_all", Kind: entity.KindProcedure}
	refreshOrders := entity.Entity{Name: "refresh_orders", Kind: entity.KindProcedure, Affects: []string{"orders"}}
	reg, err := entity.NewRegistry([]entity.Entity{orders, items, daily, other, refreshAll, refreshOrders})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		writer entity.Entity
		gone   []entity.Entity
		kept   []entity.Entity
	}{
		"table write":                  {orders, []entity.Entity{orders, daily}, []entity.Entity{items, other}},
		"view write":                   {daily, []entity.Entity{orders, items, daily}, []entity.Entity{other}},
		"procedure without affects":    {refreshAll, []entity.Entity{orders, items, daily}, []entity.Entity{other}},
		"procedure with known affects": {refreshOrders, []entity.Entity{orders, daily}, []entity.Entity{items, other}},
	} {
		t.Run(name, func(t *testing.T) {
			c := cache.NewTTLCache[[]map[string]any](time.Minute, 0)
			key := func(e entity.Entity) cache.Key {
				return cache.Key{Database: physicalDatabase(e), Relation: cacheRelation(e), SQL: e.Name}
			}
			for _, e := range []entity.Entity{orders, items, daily, other} {
				_ = c.Set(context.Background(), key(e), []map[string]any{{}}, 0)
			}
			// Through the targets worked out once per configuration.
			tcx := Context{Services: &Services{Registry: reg, Cache: c, WriteTargets: BuildWriteTargets(reg)}}
			if err := afterWrite(bg, tcx, tc.writer, ""); err != nil {
				t.Fatal(err)
			}
			for _, e := range tc.gone {
				if _, ok := c.Get(context.Background(), key(e)); ok {
					t.Errorf("cached read of %s survived the write", e.Name)
				}
			}
			for _, e := range tc.kept {
				if _, ok := c.Get(context.Background(), key(e)); !ok {
					t.Errorf("cached read of %s was dropped", e.Name)
				}
			}
		})
	}
}

func TestWriteInvalidatesEveryCascadeLevel(t *testing.T) {
	t.Parallel()
	shipments := entity.Entity{Name: "shipments", Source: "shipments"}
	orders := entity.Entity{Name: "orders", Source: "orders",
		Cascades: []entity.Cascade{{Table: "shipments", Entity: "shipments", OnDelete: entity.FKCascade}}}
	customers := entity.Entity{Name: "customers", Source: "customers",
		Cascades: []entity.Cascade{{Table: "orders", Entity: "orders", OnDelete: entity.FKCascade}}}
	hidden := entity.Entity{Name: "accounts", Source: "accounts",
		Cascades: []entity.Cascade{{Table: "ledger", OnDelete: entity.FKCascade}}}
	items := entity.Entity{Name: "items", Source: "items"}
	reg, err := entity.NewRegistry([]entity.Entity{shipments, orders, customers, hidden, items})
	if err != nil {
		t.Fatal(err)
	}
	relations := func(targets []CacheTarget) []string {
		var out []string
		for _, target := range targets {
			out = append(out, target.Relation)
		}
		return out
	}
	got := relations(BuildWriteTargets(reg).byEntity["customers"])
	want := []string{customers.RelationKey(), orders.RelationKey(), shipments.RelationKey()}
	if !slices.Equal(got, want) {
		t.Errorf("customers targets = %q, want %q", got, want)
	}
	// What a cascade into an unexposed table cascades to is unknown.
	if got := BuildWriteTargets(reg).byEntity["accounts"]; len(got) != 1 || got[0].Relation != "" {
		t.Errorf("accounts targets = %+v, want the whole database", got)
	}
}

func TestWriteInvalidatesDatasourcesReachingTheSameDatabase(t *testing.T) {
	t.Parallel()
	// ro and rw reach the same database; archive another one.
	rel := func(server, table string) string { return server + "\x00shop\x00public\x00" + table }
	customers := entity.Entity{Name: "customers", Source: "customers", DataSource: "rw", Relation: rel("pg1", "customers"),
		Cascades: []entity.Cascade{{Table: "orders", Entity: "orders", OnDelete: entity.FKCascade}}}
	orders := entity.Entity{Name: "orders", Source: "orders", DataSource: "ro", Relation: rel("pg1", "orders")}
	daily := entity.Entity{Name: "daily", Source: "daily", DataSource: "ro", Relation: rel("pg1", "daily"),
		Kind: entity.KindView, Derived: true}
	archived := entity.Entity{Name: "archived", Source: "orders", DataSource: "archive", Relation: rel("pg2", "orders")}
	audited := entity.Entity{Name: "audited", Source: "audited", DataSource: "rw", Relation: rel("pg1", "audited"),
		SideEffects: true}
	all := []entity.Entity{customers, orders, daily, archived, audited}
	reg, err := entity.NewRegistry(all)
	if err != nil {
		t.Fatal(err)
	}
	for writer, gone := range map[string][]string{
		"customers": {"customers", "orders", "daily"},
		"audited":   {"customers", "orders", "daily", "audited"},
	} {
		c := cache.NewTTLCache[[]map[string]any](time.Minute, 0)
		key := func(e entity.Entity) cache.Key {
			return cache.Key{Database: physicalDatabase(e), Relation: cacheRelation(e), SQL: e.Name}
		}
		byName := map[string]entity.Entity{}
		for _, e := range all {
			byName[e.Name] = e
			_ = c.Set(context.Background(), key(e), []map[string]any{{}}, 0)
		}
		writes := NewWriteTracker()
		services := &Services{Registry: reg, Cache: c, Writes: writes, Sources: followingWrites("rw", "ro", "archive")}
		tcx := Context{Services: services, Session: "s"}
		if err := afterWrite(bg, tcx, byName[writer], ""); err != nil {
			t.Fatal(err)
		}
		// The session's reads through every datasource reaching pg1 follow the write.
		for datasource, follows := range map[string]bool{"rw": true, "ro": true, "archive": false} {
			if writes.Recent("s", datasource) != follows {
				t.Errorf("write to %s: reads through %s follow it = %v", writer, datasource, !follows)
			}
		}
		for _, e := range all {
			_, cached := c.Get(context.Background(), key(e))
			if cached == slices.Contains(gone, e.Name) {
				t.Errorf("write to %s: cached read of %s kept = %v", writer, e.Name, cached)
			}
		}
	}
}

func TestWriteTargetsAreCompact(t *testing.T) {
	t.Parallel()
	rel := func(table string) string { return "pg1\x00shop\x00public\x00" + table }
	orders := entity.Entity{Name: "orders", Source: "orders", DataSource: "ro", Relation: rel("orders")}
	items := entity.Entity{Name: "items", Source: "items", DataSource: "rw", Relation: rel("items"),
		Cascades: []entity.Cascade{{Table: "orders", Entity: "orders"}, {Table: "orders", Entity: "orders"}}}
	audited := entity.Entity{Name: "audited", Source: "audited", DataSource: "rw", Relation: rel("audited"),
		SideEffects: true}
	refresh := entity.Entity{Name: "refresh", Kind: entity.KindProcedure, DataSource: "rw",
		Affects: []string{"orders", "audited", "orders"}}
	reg, err := entity.NewRegistry([]entity.Entity{orders, items, audited, refresh})
	if err != nil {
		t.Fatal(err)
	}
	w := BuildWriteTargets(reg)
	database := physicalDatabase(orders)
	for name, want := range map[string][]CacheTarget{
		"items": {
			{Physical: database, Relation: items.RelationKey()}, {Physical: database, Relation: orders.RelationKey()},
		},
		"refresh": {{Physical: database}},
	} {
		if got := w.byEntity[name]; !slices.Equal(got, want) {
			t.Errorf("%s targets = %+v, want %+v", name, got, want)
		}
	}
	if got := w.datasources[database]; !slices.Equal(got, []string{"ro", "rw"}) {
		t.Errorf("datasources of the database = %q", got)
	}
}

// A read cached is counted on a hit as it was when read, by the size stored
// with it, against the same limits.
func TestCacheHitCountsAsTheRead(t *testing.T) {
	t.Parallel()
	rows := []map[string]any{{"id": int64(1), "name": "a"}, {"id": int64(2), "name": "bé"}}
	read := &resultMeter{}
	for _, row := range rows {
		if err := read.add(row); err != nil {
			t.Fatal(err)
		}
	}
	c := cache.NewTTLCache[[]map[string]any](time.Minute, 0)
	key := cache.Key{SQL: "q"}
	storeReadCache(bg, Context{Services: &Services{Cache: c}, results: read}, readInput{}, key, 0, rows)
	got, size, ok := c.GetSized(bg, key)
	if _, bytes := read.counted(); !ok || size != bytes || len(got) != 2 {
		t.Fatalf("stored %d bytes, read %d", size, bytes)
	}
	hit := &resultMeter{}
	if err := countCached(Context{results: hit}, got, size); err != nil {
		t.Fatal(err)
	}
	if fmtCounted(hit) != fmtCounted(read) {
		t.Fatalf("a hit counts %v, the read %v", fmtCounted(hit), fmtCounted(read))
	}
	for name, m := range map[string]*resultMeter{"rows": {maxRows: 1}, "bytes": {maxBytes: size - 1}} {
		if err := countCached(Context{results: m}, got, size); err == nil {
			t.Fatalf("a hit past the call's %s passed", name)
		}
	}
}

func fmtCounted(m *resultMeter) [2]int64 {
	rows, bytes := m.counted()
	return [2]int64{rows, bytes}
}

// A row of the escape hatch is counted as it is returned, a list of values:
// columns sharing a name count each.
func TestRawRowsCountEveryColumn(t *testing.T) {
	t.Parallel()
	m := &resultMeter{}
	if err := m.addValues([]any{int64(1), "a long value"}); err != nil {
		t.Fatal(err)
	}
	if _, bytes := m.counted(); bytes != int64(len(`[1,"a long value"]`))+1 {
		t.Fatalf("counted %d bytes", bytes)
	}
}

// The rows a read stores are those later calls are served: neither the read
// that stored them nor one served from them edits them after.
func TestCachedRowsStayAsStored(t *testing.T) {
	t.Parallel()
	tc, db := orderContext(t, []any{int64(7), int64(500), "2026-10-01"})
	c := cache.NewTTLCache[[]map[string]any](time.Minute, 0)
	tc.Cache = c
	input := json.RawMessage(`{"entity":"orders","fields":["amount","created_at"],"limit":5}`)
	first, err := RunTool(context.Background(), ReadTool{}, input, tc)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := json.Marshal(first.Content)
	second, err := RunTool(context.Background(), ReadTool{}, input, tc)
	if err != nil || len(db.Queries) != 1 {
		t.Fatalf("not served from the cache: %v, %d queries", err, len(db.Queries))
	}
	if now, _ := json.Marshal(second.Content); string(now) != string(stored) {
		t.Fatalf("served %s, stored %s", now, stored)
	}
	if now, _ := json.Marshal(first.Content); string(now) != string(stored) {
		t.Fatalf("the stored rows changed: %s, were %s", now, stored)
	}
}
