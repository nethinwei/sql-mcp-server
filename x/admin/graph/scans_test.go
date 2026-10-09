package graph

import (
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/admin/scanjobs"
)

func scansConfig(dsn string) *config.Config {
	return &config.Config{
		Databases: map[string]config.DatabaseConfig{"shop": {Driver: "postgres", DSN: dsn}},
		Entities:  []config.EntityConfig{{Name: "accounts", DataSource: "shop", Schema: "crm"}},
	}
}

func candidatesOf(out *SchemaImport) map[string]Entity {
	byTable := map[string]Entity{}
	for _, tb := range out.Tables {
		byTable[tableKey(tb.Schema, tb.Table)] = *tb.Candidate
	}
	return byTable
}

func targets(e Entity) []string {
	var out []string
	for _, r := range e.Relationships {
		out = append(out, r.Cardinality+" "+r.Target)
	}
	return out
}

// Schemas scanned one at a time are compared together: same-named tables
// are told apart by their IDs and foreign keys across schemas become relationships,
// to kept tables and to configured entities of schemas not scanned.
func TestKeptSchemasAreImportedTogether(t *testing.T) {
	t.Parallel()
	scans := NewScans(scanjobs.Options{})
	t.Cleanup(scans.Close)
	cfg := scansConfig("postgres://a/shop")
	src := sourceOf(cfg, "shop")
	scans.keep(src, []string{"public"}, introspect.Catalog{
		Tables: []entity.Entity{table("public", "users"), table("public", "customers")},
	})
	scans.keep(src, []string{"sales"}, introspect.Catalog{Tables: []entity.Entity{
		table("sales", "users"),
		table("sales", "orders",
			entity.ForeignKey{Columns: []string{"user_id"}, RefSchema: "public", RefRelation: "customers",
				RefColumns: []string{"id"}},
			entity.ForeignKey{Columns: []string{"id"}, RefSchema: "crm", RefRelation: "accounts",
				RefColumns: []string{"id"}}),
	}})
	sales := candidatesOf(scans.importOf(src, []string{"sales"}, cfg))
	if len(sales) != 2 || sales["sales.users"].ID != "shop.sales.users" {
		t.Fatalf("sales = %+v", sales)
	}
	if got := targets(sales["sales.orders"]); len(got) != 2 ||
		got[0] != "belongs-to shop.public.customers" || got[1] != "belongs-to shop.crm.accounts" {
		t.Fatalf("orders relationships = %v", got)
	}
	if both := scans.importOf(src, nil, cfg); len(both.Tables) != 4 {
		t.Fatalf("both schemas in one call = %+v", both.Tables)
	}
	public := candidatesOf(scans.importOf(src, []string{"public"}, cfg))
	if public["public.users"].ID != "shop.public.users" ||
		targets(public["public.customers"])[0] != "has-many shop.sales.orders" {
		t.Fatalf("public = %+v", public)
	}
}

func keepSchemas(scans *Scans, src source, tables ...entity.Entity) {
	var schemas []string
	for _, t := range tables {
		schemas = append(schemas, t.Schema)
	}
	scans.keep(src, schemas, introspect.Catalog{Tables: tables})
}

// What was kept for a datasource is dropped once it reads another database,
// and a refreshed list drops the schemas that are gone.
func TestKeptScansFollowTheDatabase(t *testing.T) {
	t.Parallel()
	scans := NewScans(scanjobs.Options{})
	t.Cleanup(scans.Close)
	old := sourceOf(scansConfig("postgres://a/shop"), "shop")
	keepSchemas(scans, old, table("public", "users"), table("sales", "orders"))
	moved := scansConfig("postgres://b/shop")
	if out := scans.importOf(sourceOf(moved, "shop"), []string{"public"}, moved); len(out.Tables) != 0 {
		t.Fatalf("another database shows the old scan: %+v", out.Tables)
	}
	scans.current(sourceOf(moved, "shop"), time.Now())
	if _, ok := scans.scanned(old, "public"); ok {
		t.Fatal("the old database's scan must be dropped")
	}
	keepSchemas(scans, old, table("public", "users"), table("sales", "orders"))
	if _, err := scans.list(old, true, func() (listedSchemas, error) {
		return listedSchemas{all: []string{"public"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := scans.scanned(old, "sales"); ok {
		t.Fatal("a schema no longer listed must be dropped")
	}
	if _, ok := scans.scanned(old, "public"); !ok {
		t.Fatal("a listed schema keeps its scan")
	}
}

// Requests and scans for the old database that finish after the datasource
// moved, and after the new database was scanned, keep the new one's scans.
func TestLateRequestsKeepNewerScans(t *testing.T) {
	t.Parallel()
	scans := NewScans(scanjobs.Options{})
	t.Cleanup(scans.Close)
	old := sourceOf(scansConfig("postgres://a/shop"), "shop")
	moved := scansConfig("postgres://b/shop")
	seenOld := time.Now()
	keepSchemas(scans, sourceOf(moved, "shop"), table("public", "accounts"))
	keepSchemas(scans, old, table("public", "users"))
	if out := scans.importOf(old, []string{"public"}, moved); len(out.Tables) != 1 ||
		out.Source == sourceOf(moved, "shop").token() {
		t.Fatalf("the late scan answers for its own database: %+v", out)
	}
	scans.current(old, seenOld)
	if _, err := scans.list(old, true, func() (listedSchemas, error) {
		return listedSchemas{all: []string{"public"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := scans.scanned(sourceOf(moved, "shop"), "public"); !ok {
		t.Fatal("a late request for the old database must not drop the new one's scans")
	}
}
