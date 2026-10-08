package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// catalogProvider is a provider whose introspector reports fixed relations
// in the "public" default schema of server.
type catalogProvider struct {
	fakeProvider
	relations     []entity.Entity
	physical      introspect.Physical
	defaultSchema string
}

func (p *catalogProvider) Introspector() introspect.Introspector { return catalogIntrospector{p} }

type catalogIntrospector struct{ p *catalogProvider }

func (c catalogIntrospector) Discover(context.Context, []string) ([]entity.Entity, error) {
	return c.p.relations, nil
}
func (c catalogIntrospector) Schemas(context.Context) ([]string, string, error) {
	if c.p.defaultSchema != "" {
		return []string{"public", c.p.defaultSchema}, c.p.defaultSchema, nil
	}
	return []string{"public"}, "public", nil
}
func (c catalogIntrospector) Physical(context.Context) (introspect.Physical, error) {
	return c.p.physical, nil
}

func relationProvider(server string, foldCase bool, relations ...entity.Entity) *catalogProvider {
	for i := range relations {
		relations[i].Schema = "public"
		relations[i].Attributes = []entity.Attribute{{Name: "id"}}
	}
	return &catalogProvider{
		fakeProvider: fakeProvider{dialect: postgres.Dialect{}},
		relations:    relations,
		physical:     introspect.Physical{Server: server, FoldCase: foldCase},
	}
}

func relationAssembly(t *testing.T, providers map[string]Provider, entities ...config.EntityConfig) (*App, error) {
	t.Helper()
	databases := map[string]config.DatabaseConfig{}
	for name := range providers {
		databases[name] = config.DatabaseConfig{Driver: "postgres", DSN: "x"}
	}
	for i := range entities {
		entities[i].Fields = []config.FieldConfig{{Name: "id"}}
	}
	cfg := &config.Config{Databases: databases, Entities: entities}
	cfg.ApplyDefaults()
	return AssembleWithProviders(cfg, providers)
}

func TestAssembleRejectsTwoEntitiesOnOneRelation(t *testing.T) {
	t.Parallel()
	orders := entity.Entity{Name: "orders", Source: "orders"}
	for name, tc := range map[string]struct {
		providers map[string]Provider
		entities  []config.EntityConfig
	}{
		"two datasources on one server": {
			map[string]Provider{"ro": relationProvider("srv", false, orders), "rw": relationProvider("srv", false, orders)},
			[]config.EntityConfig{
				{Name: "orders", DataSource: "ro"}, {Name: "orders_rw", DataSource: "rw", Source: "orders"},
			},
		},
		"default schema and explicit schema": {
			map[string]Provider{"main": relationProvider("srv", false, orders)},
			[]config.EntityConfig{
				{Name: "orders", DataSource: "main"},
				{Name: "orders_public", DataSource: "main", Schema: "public", Source: "orders"},
			},
		},
		"names that fold to one": {
			map[string]Provider{"main": relationProvider("srv", true, orders)},
			[]config.EntityConfig{
				{Name: "orders", DataSource: "main"}, {Name: "orders_upper", DataSource: "main", Source: "ORDERS"},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := relationAssembly(t, tc.providers, tc.entities...)
			if err == nil || !strings.Contains(err.Error(), "same relation") {
				t.Fatalf("error = %v, want a duplicate relation error", err)
			}
		})
	}
}

func TestAssembleIdentifiesRelations(t *testing.T) {
	t.Parallel()
	view := entity.Entity{Name: "daily", Source: "daily", Kind: entity.KindView, Derived: true}
	app, err := relationAssembly(t,
		map[string]Provider{
			"a": relationProvider("srv-a", false, entity.Entity{Name: "orders", Source: "orders"}, view),
			"b": relationProvider("srv-b", false, entity.Entity{Name: "orders", Source: "orders"}),
		},
		config.EntityConfig{Name: "orders", DataSource: "a"},
		config.EntityConfig{Name: "daily", DataSource: "a"},
		config.EntityConfig{Name: "orders_b", DataSource: "b", Source: "orders"},
	)
	if err != nil {
		t.Fatalf("same table name on other servers must assemble: %v", err)
	}
	daily, _ := app.Registry.Resolve("daily")
	if daily.Entity.Kind != entity.KindView || !daily.Entity.Derived {
		t.Fatalf("a configured table that is a view must take the database's kind: %+v", daily.Entity)
	}
	a, _ := app.Registry.Resolve("orders")
	b, _ := app.Registry.Resolve("orders_b")
	if a.Entity.Relation == "" || a.Entity.Relation == b.Entity.Relation {
		t.Fatalf("relations: %q vs %q", a.Entity.Relation, b.Entity.Relation)
	}
}
