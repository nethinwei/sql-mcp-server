package introspect

import (
	"fmt"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// benchCatalog has tables tables over 10 schemas and configures entities of
// them, unqualified names reading the default schema.
func benchCatalog(tables, entities int) (Catalog, []entity.Entity) {
	cat := Catalog{Default: "s0", FoldCase: true}
	for i := range tables {
		name := fmt.Sprintf("table_%d", i/10)
		cat.Tables = append(cat.Tables, entity.Entity{
			Name: name, Source: name, Schema: fmt.Sprintf("s%d", i%10),
			Attributes: []entity.Attribute{{Name: "id"}, {Name: "name"}, {Name: "created_at"}},
		})
	}
	configured := make([]entity.Entity, entities)
	for i := range configured {
		configured[i] = entity.Entity{
			Name: fmt.Sprintf("e%d", i), Source: fmt.Sprintf("table_%d", i/2), Schema: fmt.Sprintf("s%d", i%10),
			Attributes: []entity.Attribute{{Name: "id"}, {Name: "name"}},
		}
	}
	return cat, configured
}

// BenchmarkReconcile reconciles 1000 entities with 5000 tables, with the name
// index LoadCatalog builds and without it.
func BenchmarkReconcile(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		b.Run(fmt.Sprintf("indexed=%v", indexed), func(b *testing.B) {
			cat, configured := benchCatalog(5000, 1000)
			if indexed {
				cat.Index()
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, drift := Reconcile(configured, cat); len(drift.Missing) != 0 {
					b.Fatal(drift.Missing)
				}
			}
		})
	}
}
