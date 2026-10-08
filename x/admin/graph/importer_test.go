package graph

import (
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
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
	for _, tb := range buildSchemaImport("shop", introspect.Catalog{Tables: discovered, Default: "public"}, cfg).Tables {
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

// An entity without a schema reads the default schema, as the runtime does:
// a same-named table elsewhere is a new table, and without a default schema
// only a unique name matches.
func TestSchemaImportUnscopedEntityReadsTheDefaultSchema(t *testing.T) {
	t.Parallel()
	discovered := []entity.Entity{table("public", "users"), table("archive", "users")}
	cfg := &config.Config{Entities: []config.EntityConfig{{Name: "users", DataSource: "shop"}}}
	got := map[string]ImportTable{}
	for _, tb := range buildSchemaImport("shop", introspect.Catalog{Tables: discovered, Default: "public"}, cfg).Tables {
		got[tb.Schema] = tb
	}
	if got["public"].Status != ImportStatusConfigured || got["archive"].Status != ImportStatusNew ||
		got["archive"].Candidate.Name == "users" {
		t.Fatalf("public = %+v, archive = %+v", got["public"], got["archive"])
	}
	for _, tb := range buildSchemaImport("shop", introspect.Catalog{Tables: discovered}, cfg).Tables {
		if tb.Status != ImportStatusNew {
			t.Fatalf("%s.%s matched without a known default schema", tb.Schema, tb.Table)
		}
	}
}

// Comments are shown with the scanned columns but not copied into the
// candidate, so they stay the runtime default instead of a frozen override.
func TestSchemaImportLeavesCommentsAsDefaults(t *testing.T) {
	t.Parallel()
	d := table("public", "users")
	d.Description, d.Attributes[0].Description = "用户", "主键"
	tb := buildSchemaImport("shop", introspect.Catalog{Tables: []entity.Entity{d}}, &config.Config{}).Tables[0]
	if tb.Description != "用户" || tb.Columns[0].Description != "主键" {
		t.Fatalf("scan lost comments: %+v", tb)
	}
	if tb.Candidate.Description != nil || tb.Candidate.Fields[0].Description != nil {
		t.Fatalf("candidate copied comments: %+v", tb.Candidate)
	}
}

// Comments resolve tables like the runtime: one catalog per datasource, an
// unqualified name in the default schema.
func TestLookupTableCommentsResolvesLikeTheRuntime(t *testing.T) {
	t.Parallel()
	pub, crm := table("public", "customers"), table("crm", "customers")
	pub.Description, crm.Description = "公共客户", "CRM 客户"
	calls := 0
	catalog := func(datasource string, refs []TableRef) (introspect.Catalog, error) {
		calls++
		if datasource != "shop" {
			return introspect.Catalog{}, nil
		}
		return introspect.Catalog{Default: "public", Tables: []entity.Entity{pub, crm}}, nil
	}
	got, err := lookupTableComments([]TableRef{
		{Datasource: "shop", Table: "customers"},
		{Datasource: "shop", Schema: new("crm"), Table: "customers"},
		{Datasource: "gone", Table: "customers"},
	}, catalog)
	if err != nil || calls != 2 || got[2] != nil ||
		got[0].Description != "公共客户" || got[1].Description != "CRM 客户" {
		t.Fatalf("got %v, err %v, calls %d", got, err, calls)
	}
}

// Without schemas, an import scans every schema the database lists rather
// than only the default one; named schemas are scanned as given.
func TestSchemaImportScansListedSchemasByDefault(t *testing.T) {
	h := newHarness(t)
	h.tables.all = []string{"crm", "sales"}
	mustPost(t, h.client(auth.PermAll), `{ schemaImport(datasource: "shop") { datasource } }`, &map[string]any{})
	if strings.Join(h.tables.scanned, ",") != "crm,sales" {
		t.Fatalf("scanned %v", h.tables.scanned)
	}
	explicit := `{ schemaImport(datasource: "shop", schemas: ["crm"]) { datasource } }`
	mustPost(t, h.client(auth.PermAll), explicit, &map[string]any{})
	if strings.Join(h.tables.scanned, ",") != "crm" {
		t.Fatalf("explicit schemas replaced: %v", h.tables.scanned)
	}
}
