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
	scanSchema(t, h.client(auth.PermAll), `datasource: "shop"`)
	if strings.Join(h.tables.scanned, ",") != "crm,sales" {
		t.Fatalf("scanned %v", h.tables.scanned)
	}
	scanSchema(t, h.client(auth.PermAll), `datasource: "shop", schemas: ["crm"]`)
	if strings.Join(h.tables.scanned, ",") != "crm" {
		t.Fatalf("explicit schemas replaced: %v", h.tables.scanned)
	}
}

// The console lists schemas first and scans the one an administrator opens;
// what a scan found is kept until the schema is scanned again.
func TestSchemaListAndKeptScans(t *testing.T) {
	h := newHarness(t)
	h.tables.all = []string{"crm", "public"}
	c := h.client(auth.PermWrite)
	type list struct {
		SchemaList struct {
			Schemas []struct {
				Name      string
				ScannedAt *string
				Tables    *int
			}
			DefaultSchema *string
		}
	}
	const listQuery = `{ schemaList(datasource: "shop") { schemas { name scannedAt tables } defaultSchema } }`
	var before list
	mustPost(t, c, listQuery, &before)
	if len(before.SchemaList.Schemas) != 2 || before.SchemaList.Schemas[1].ScannedAt != nil ||
		*before.SchemaList.DefaultSchema != "public" || h.tables.scanned != nil {
		t.Fatalf("schemaList = %+v, scanned %v; listing must not scan tables", before.SchemaList, h.tables.scanned)
	}
	var kept struct {
		SchemaTables struct {
			Tables []struct{ Table, Status string }
		}
	}
	const tablesQuery = `{ schemaTables(datasource: "shop", schemas: ["public"]) { tables { table status } } }`
	mustPost(t, c, tablesQuery, &kept)
	if len(kept.SchemaTables.Tables) != 0 {
		t.Fatalf("an unscanned schema = %+v, want no tables", kept)
	}
	scanSchema(t, c, `datasource: "shop", schemas: ["public"]`)
	h.tables.scanned = nil
	var after list
	mustPost(t, c, listQuery, &after)
	if s := after.SchemaList.Schemas[1]; s.Name != "public" || s.ScannedAt == nil || *s.Tables != 2 {
		t.Fatalf("public after its scan = %+v", s)
	}
	mustPost(t, c, tablesQuery, &kept)
	if len(kept.SchemaTables.Tables) != 2 || kept.SchemaTables.Tables[0].Status != "CONFIGURED" ||
		h.tables.scanned != nil {
		t.Fatalf("kept scan = %+v, scanned %v; it must not scan again", kept.SchemaTables, h.tables.scanned)
	}
	if err := h.client(auth.PermRead).Post(listQuery, &before); err == nil {
		t.Fatal("listing schemas needs admin:write, like scanning")
	}
}

func TestSchemaImportListsIndexes(t *testing.T) {
	t.Parallel()
	d := table("public", "docs")
	d.Indexes = []entity.Index{
		{Name: "docs_pkey", Method: "btree", Parts: []string{"id"}, Unique: true, Primary: true},
		{Name: "docs_lower", Method: "btree", Parts: []string{"lower(title)"}, Where: "at IS NOT NULL"},
		{Name: "docs_body", Method: "fulltext"},
	}
	got := buildSchemaImport("shop", introspect.Catalog{Tables: []entity.Entity{d}}, &config.Config{}).Tables[0].Indexes
	if len(got) != 3 || !got[0].Primary || got[0].Where != nil || *got[1].Where != "at IS NOT NULL" ||
		got[2].Method != "fulltext" || got[2].Parts == nil {
		t.Fatalf("indexes = %+v", got)
	}
}

// A datasource that cannot list its schemas lists its default one as "",
// scanned or not, even when the scan found no tables.
func TestSchemaListShowsTheDefaultSchemaScan(t *testing.T) {
	h := newHarness(t)
	h.tables.all, h.tables.tables = nil, nil
	c := h.client(auth.PermWrite)
	var resp struct {
		SchemaList struct {
			Schemas []struct {
				Name      string
				ScannedAt *string
				Tables    *int
			}
		}
	}
	const query = `{ schemaList(datasource: "shop", refresh: true) { schemas { name scannedAt tables } } }`
	mustPost(t, c, query, &resp)
	if s := resp.SchemaList.Schemas; len(s) != 1 || s[0].Name != "" || s[0].ScannedAt != nil {
		t.Fatalf("before the scan = %+v", s)
	}
	scanSchema(t, c, `datasource: "shop"`)
	mustPost(t, c, query, &resp)
	if s := resp.SchemaList.Schemas; len(s) != 1 || s[0].ScannedAt == nil || *s[0].Tables != 0 {
		t.Fatalf("after the scan = %+v", s)
	}
}
