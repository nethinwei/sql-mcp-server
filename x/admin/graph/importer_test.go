package graph

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

func table(schema, name string, fks ...entity.ForeignKey) entity.Entity {
	return entity.Entity{
		Name: name, Source: name, Schema: schema, ForeignKeys: fks,
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "user_id"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
	}
}

func TestSchemaImportKeepsSameNamedTablesApart(t *testing.T) {
	t.Parallel()
	discovered := []entity.Entity{
		table("public", "users"),
		table("archive", "users"),
		table("archive", "orders", entity.ForeignKey{
			Columns: []string{"user_id"}, RefSchema: "archive", RefRelation: "users", RefColumns: []string{"id"},
		}),
	}
	cfg := &config.Config{Entities: []config.EntityConfig{
		{Name: "users", Source: "users", Schema: "public", DataSource: "shop", Fields: []config.FieldConfig{{Name: "id"}}},
	}}
	got := map[string]ImportTable{}
	for _, tb := range buildSchemaImport("shop", discovered, cfg).Tables {
		got[tb.Schema+"."+tb.Table] = tb
	}
	if pub := got["public.users"]; pub.Status != ImportStatusConfigured || *pub.ConfiguredAs != "users" {
		t.Fatalf("public.users = %+v", pub)
	}
	arch := got["archive.users"]
	if arch.Status != ImportStatusNew || arch.Candidate.Name != "archive_users" || *arch.Candidate.Schema != "archive" {
		t.Fatalf("archive.users = %+v / %+v", arch, arch.Candidate)
	}
	rels := got["archive.orders"].Candidate.Relationships
	if len(rels) != 1 || rels[0].Target != "archive_users" {
		t.Fatalf("archive.orders relationships = %+v", rels)
	}
}

func TestSchemaImportUnscopedEntityNeedsUniqueTable(t *testing.T) {
	t.Parallel()
	discovered := []entity.Entity{table("public", "users"), table("archive", "users")}
	cfg := &config.Config{Entities: []config.EntityConfig{{Name: "users", DataSource: "shop"}}}
	for _, tb := range buildSchemaImport("shop", discovered, cfg).Tables {
		if tb.Status != ImportStatusNew {
			t.Fatalf("%s.%s matched an entity without a schema although the name is ambiguous", tb.Schema, tb.Table)
		}
		if tb.Candidate.Name == "users" {
			t.Fatalf("%s.%s reuses the configured name users", tb.Schema, tb.Table)
		}
	}
	single := buildSchemaImport("shop", discovered[:1], cfg).Tables[0]
	if single.Status != ImportStatusConfigured {
		t.Fatalf("a unique table must match the entity without a schema: %+v", single)
	}
}
