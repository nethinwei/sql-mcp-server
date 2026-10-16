package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// A table or column gone from the database makes its entity unavailable;
// the configuration still loads and the other entities stay served.
func TestAssembleMarksDriftedEntitiesUnavailable(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Databases: map[string]config.DatabaseConfig{"main": {Driver: "postgres", DSN: "x"}},
		Entities: []config.EntityConfig{
			{Name: "users", DataSource: "main", Fields: []config.FieldConfig{{Name: "id"}}},
			{Name: "orders", DataSource: "main", Fields: []config.FieldConfig{{Name: "id"}, {Name: "total"}}},
			{Name: "snapshot_2026", DataSource: "main", Fields: []config.FieldConfig{{Name: "id"}}},
		},
	}
	cfg.ApplyDefaults()
	prov := commentedProvider{&fakeProvider{dialect: postgres.Dialect{}}, fakeIntrospector{
		{Name: "users", Source: "users", Schema: "public", Attributes: []entity.Attribute{{Name: "id"}}},
		{Name: "orders", Source: "orders", Schema: "public", Attributes: []entity.Attribute{{Name: "id"}}},
	}}
	app, err := AssembleWithProviders(cfg, map[string]Provider{"main": prov})
	if err != nil {
		t.Fatalf("drift failed the configuration: %v", err)
	}
	defer app.Close()
	want := map[string]string{
		"users":         "",
		"orders":        "columns missing from the database: total",
		"snapshot_2026": "its table is missing from the database",
	}
	for name, reason := range want {
		res, ok := app.Registry.Resolve(name)
		if !ok || res.Entity.Unavailable != reason {
			t.Errorf("%s: unavailable = %q (found %v), want %q", name, res.Entity.Unavailable, ok, reason)
		}
	}
}

// failingIntrospector stands for a database that does not answer.
type failingIntrospector struct{}

func (failingIntrospector) Discover(context.Context, []string) ([]entity.Entity, error) {
	return nil, errors.New("dial tcp 10.0.0.9:5432: connection refused")
}

type unreachableProvider struct{ *fakeProvider }

func (unreachableProvider) Introspector() introspect.Introspector { return failingIntrospector{} }

// A datasource whose schema cannot be read leaves the configuration loading:
// its entities are unavailable without naming the error to agents, and it is
// reported apart from the entities whose tables are missing.
func TestAssembleDegradesAnUnreachableDatasource(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Databases: map[string]config.DatabaseConfig{
			"main": {Driver: "postgres", DSN: "x"}, "legacy": {Driver: "postgres", DSN: "y"},
		},
		Entities: []config.EntityConfig{
			{Name: "users", DataSource: "main", Fields: []config.FieldConfig{{Name: "id"}}},
			{Name: "orders", DataSource: "legacy", Fields: []config.FieldConfig{{Name: "id"}}},
		},
	}
	cfg.ApplyDefaults()
	main := commentedProvider{&fakeProvider{dialect: postgres.Dialect{}}, fakeIntrospector{
		{Name: "users", Source: "users", Schema: "public", Attributes: []entity.Attribute{{Name: "id"}}},
	}}
	legacy := unreachableProvider{&fakeProvider{dialect: postgres.Dialect{}}}
	app, err := AssembleWithProviders(cfg, map[string]Provider{"main": main, "legacy": legacy})
	if err != nil {
		t.Fatalf("an unreachable datasource failed the configuration: %v", err)
	}
	defer app.Close()
	if res, _ := app.Registry.Resolve("users"); res.Entity.Unavailable != "" {
		t.Fatalf("users = %q, want available", res.Entity.Unavailable)
	}
	if res, _ := app.Registry.Resolve("orders"); res.Entity.Unavailable != "its datasource is unreachable" {
		t.Fatalf("orders = %q", res.Entity.Unavailable)
	}
	if !strings.Contains(app.Unreachable["legacy"], "connection refused") || len(app.Unavailable) != 0 {
		t.Fatalf("unreachable = %v, unavailable = %v", app.Unreachable, app.Unavailable)
	}
}

// A database down when the process assembles does not keep it from serving:
// its connection opens without reaching it, and its datasource is
// unreachable.
func TestSharedAssemblyServesWithADatabaseDown(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Databases: map[string]config.DatabaseConfig{
			"down": {Driver: "postgres", DSN: "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=2"},
		},
		Entities: []config.EntityConfig{{Name: "orders", DataSource: "down", Fields: []config.FieldConfig{{Name: "id"}}}},
	}
	cfg.ApplyDefaults()
	shared := NewShared()
	defer shared.Close()
	app, err := shared.Assemble(cfg)
	if err != nil {
		t.Fatalf("a database down failed the process: %v", err)
	}
	defer app.Close()
	if app.Unreachable["down"] == "" {
		t.Fatalf("unreachable = %v", app.Unreachable)
	}
	if res, _ := app.Registry.Resolve("orders"); res.Entity.Unavailable != "its datasource is unreachable" {
		t.Fatalf("orders = %q", res.Entity.Unavailable)
	}
}
