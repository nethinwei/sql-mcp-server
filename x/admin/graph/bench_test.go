package graph

import (
	"context"
	"fmt"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
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
