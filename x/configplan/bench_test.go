package configplan

import (
	"fmt"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// largeCatalog is 500 schemas of 100 tables each, a tenth of them configured.
func largeCatalog() ([]Source, *config.Config) {
	cfg := &config.Config{Roles: map[string]config.RoleDefinition{}}
	var cat introspect.Catalog
	var schemas []string
	for s := range 500 {
		schema := fmt.Sprintf("db_%03d", s)
		schemas = append(schemas, schema)
		for t := range 100 {
			name := fmt.Sprintf("t_%03d", t)
			e := scanned(
				schema,
				name,
				true,
				column("id", "bigint"),
				column("user_phone", "text"),
				column("note", "text"),
			)
			if t > 0 {
				e.ForeignKeys = []entity.ForeignKey{{Columns: []string{"id"}, RefSchema: schema,
					RefRelation: fmt.Sprintf("t_%03d", t-1), RefColumns: []string{"id"}}}
			}
			cat.Tables = append(cat.Tables, e)
			if t%10 == 0 {
				cfg.Entities = append(cfg.Entities, config.EntityConfig{Name: name, DataSource: "shop", Schema: schema,
					PrimaryKey: []string{"id"}, Fields: []config.FieldConfig{{Name: "id"}, {Name: "note"}}})
			}
		}
	}
	cat.Index()
	return []Source{{Datasource: "shop", Catalog: cat, Listed: schemas, Scanned: schemas}}, cfg
}

// BenchmarkRowsLargeCatalog filters 50,000 tables, as a catalog page does.
func BenchmarkRowsLargeCatalog(b *testing.B) {
	sources, cfg := largeCatalog()
	b.ReportAllocs()
	for b.Loop() {
		if rows := Rows(sources, cfg, cfg, Scope{Search: "t_05"}); len(rows) != 5000 {
			b.Fatalf("rows = %d", len(rows))
		}
	}
}

// BenchmarkEvaluateLargePlan imports the new tables of 50 schemas, grants
// and masks them, in a catalog of 50,000 tables.
func BenchmarkEvaluateLargePlan(b *testing.B) {
	sources, cfg := largeCatalog()
	plan := Plan{
		Scope: Scope{Schemas: []string{"db_0[0-4]?"}}, Import: true,
		Grants: []GrantRule{{Principal: "role:analyst", Actions: []string{"read"}}},
		Fields: []FieldRule{{Columns: []string{"*phone*"}, Mask: "phone"}},
	}
	b.ReportAllocs()
	for b.Loop() {
		p, err := Evaluate(Input{Base: cfg, Published: cfg, Sources: sources}, plan)
		if err != nil || len(p.Added) != 4500 {
			b.Fatalf("added = %d, %v", len(p.Added), err)
		}
	}
}
