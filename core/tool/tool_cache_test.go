package tool

import (
	"context"
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
				return cache.Key{Database: e.DatasourceName(), Relation: cacheRelation(e), SQL: e.Name}
			}
			for _, e := range []entity.Entity{orders, items, daily, other} {
				_ = c.Set(context.Background(), key(e), []map[string]any{{}})
			}
			if err := afterWrite(Context{Registry: reg, Cache: c}, tc.writer, ""); err != nil {
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
	got := relations(writeTargets(reg, customers))
	want := []string{customers.RelationKey(), orders.RelationKey(), shipments.RelationKey()}
	if !slices.Equal(got, want) {
		t.Errorf("customers targets = %q, want %q", got, want)
	}
	// What a cascade into an unexposed table cascades to is unknown.
	if got := writeTargets(reg, hidden); len(got) != 1 || got[0].Relation != "" {
		t.Errorf("accounts targets = %+v, want the whole database", got)
	}
}
