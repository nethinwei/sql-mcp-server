package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// benchProvider answers every query at once with fixed rows and introspects
// fixed tables, so the benchmarks measure the assembled governance path.
type benchProvider struct {
	*fakeProvider
	rows   [][]any
	tables fakeIntrospector
}

func (p benchProvider) QueryContext(context.Context, string, ...any) (store.Rows, error) {
	return store.NewFakeRows([]string{"id", "email"}, p.rows...), nil
}

func (p benchProvider) Introspector() introspect.Introspector { return p.tables }

func benchConfig(entities int) (*config.Config, fakeIntrospector) {
	cfg := &config.Config{Databases: map[string]config.DatabaseConfig{"main": {Driver: "postgres", DSN: "x"}}}
	tables := make(fakeIntrospector, 0, entities)
	for i := range entities {
		name := fmt.Sprintf("t%d", i)
		if i == 0 {
			name = "users"
		}
		cfg.Entities = append(cfg.Entities, config.EntityConfig{
			Name: name, DataSource: "main", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "email", Mask: "email"}},
			Roles:  config.RoleConfig{Read: []string{"reader"}},
		})
		tables = append(tables, entity.Entity{
			Name: name, Source: name, Schema: "public",
			Attributes: []entity.Attribute{{Name: "id"}, {Name: "email"}},
			Keys:       []entity.Key{{Name: name + "_pkey", Columns: []string{"id"}, Primary: true}},
		})
	}
	cfg.ApplyDefaults()
	return cfg, tables
}

// BenchmarkAssemble300Entities measures a startup or reload of a
// configuration with 300 entities, the databases answering at once.
func BenchmarkAssemble300Entities(b *testing.B) {
	cfg, tables := benchConfig(300)
	prov := benchProvider{fakeProvider: &fakeProvider{dialect: postgres.Dialect{}}, tables: tables}
	b.ReportAllocs()
	for b.Loop() {
		app, err := AssembleWithProviders(cfg, map[string]Provider{"main": prov})
		if err != nil {
			b.Fatal(err)
		}
		_ = app.Close()
	}
}

func benchToolApp(b *testing.B) *App {
	b.Helper()
	cfg, tables := benchConfig(1)
	prov := benchProvider{
		fakeProvider: &fakeProvider{dialect: postgres.Dialect{}}, tables: tables,
		rows: [][]any{{int64(1), "alice@x.com"}},
	}
	app, err := AssembleWithProviders(cfg, map[string]Provider{"main": prov})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = app.Close() })
	return app
}

var benchPointRead = json.RawMessage(`{"entity":"users","filter":[{"field":"id","op":"eq","value":1}]}`)

// BenchmarkToolPathPointRead measures one read through the assembled App:
// budget, engine, authorization, cost gate, codegen, masking and audit.
func BenchmarkToolPathPointRead(b *testing.B) {
	tc := benchToolApp(b).ToolContext("reader")
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := tool.RunTool(ctx, tool.ReadTool{}, benchPointRead, tc); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkToolPathPointReadParallel runs the same reads from concurrent
// callers, as an MCP server under load does.
func BenchmarkToolPathPointReadParallel(b *testing.B) {
	tc := benchToolApp(b).ToolContext("reader")
	ctx := context.Background()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := tool.RunTool(ctx, tool.ReadTool{}, benchPointRead, tc); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
