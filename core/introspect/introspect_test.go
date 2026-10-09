package introspect

import (
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
)

func detectDrift(configured, discovered []entity.Entity) Drift {
	_, d := Reconcile(configured, Catalog{Tables: discovered})
	return d
}

func TestDetectDriftFieldLevel(t *testing.T) {
	t.Parallel()
	cfg := []entity.Entity{{
		Name:       "users",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "phone"}},
	}}
	disc := []entity.Entity{{
		Name:       "users",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "email"}},
	}}
	d := detectDrift(cfg, disc)
	if !slices.Contains(d.Missing, "users.phone") {
		t.Fatalf("expected users.phone in Missing, got %v", d.Missing)
	}
	if !slices.Contains(d.Extra, "users.email") {
		t.Fatalf("expected users.email in Extra, got %v", d.Extra)
	}
}

func TestDetectDriftMissingEntity(t *testing.T) {
	t.Parallel()
	d := detectDrift([]entity.Entity{{Name: "ghost"}}, nil)
	if !slices.Contains(d.Missing, "ghost") {
		t.Fatalf("expected ghost in Missing, got %v", d.Missing)
	}
}

func TestDetectDriftNoDrift(t *testing.T) {
	t.Parallel()
	e := []entity.Entity{{Name: "users", Attributes: []entity.Attribute{{Name: "id"}}}}
	d := detectDrift(e, e)
	if len(d.Missing) != 0 || len(d.Extra) != 0 {
		t.Fatalf("expected no drift, got %+v", d)
	}
}

func TestDetectDriftUsesPhysicalSource(t *testing.T) {
	t.Parallel()
	configured := []entity.Entity{{
		Name: "admin_users", Source: "users",
		Attributes: []entity.Attribute{{Name: "id"}},
	}}
	discovered := []entity.Entity{{
		Name: "users", Attributes: []entity.Attribute{{Name: "id"}},
	}}
	drift := detectDrift(configured, discovered)
	if len(drift.Missing) != 0 {
		t.Fatalf("physical source reported missing: %+v", drift)
	}
}

// An entity without a schema reads the default schema, as the generated SQL
// does, even when only other schemas were named elsewhere in the config.
func TestCatalogResolvesTheDefaultSchema(t *testing.T) {
	t.Parallel()
	cat := Catalog{Default: "public", Tables: []entity.Entity{
		{Name: "customers", Source: "customers", Schema: "crm", Description: "CRM 客户",
			Attributes: []entity.Attribute{{Name: "id", Description: "CRM 主键"}}},
		{Name: "customers", Source: "customers", Schema: "public", Description: "公共客户",
			Attributes: []entity.Attribute{{Name: "id", Description: "公共主键"}}},
	}}
	got, d := Reconcile([]entity.Entity{
		{Name: "customers", Attributes: []entity.Attribute{{Name: "id"}}},
		{Name: "crm_customers", Source: "customers", Schema: "crm", Attributes: []entity.Attribute{{Name: "id"}}},
	}, cat)
	if len(d.Missing)+len(d.Extra) != 0 {
		t.Fatalf("drift = %+v", d)
	}
	if got[0].Description != "公共客户" || got[0].Attributes[0].Description != "公共主键" ||
		got[1].Description != "CRM 客户" {
		t.Fatalf("descriptions = %q / %q", got[0].Description, got[1].Description)
	}
	if _, ok := (Catalog{Default: "public", Tables: cat.Tables[:1]}).Lookup("", "customers"); ok {
		t.Fatal("an unqualified name must not resolve to a table outside the default schema")
	}
}

// Without a known default schema, only a name unique in the catalog resolves.
func TestCatalogWithoutDefaultNeedsUniqueName(t *testing.T) {
	t.Parallel()
	tables := []entity.Entity{{Name: "orders", Schema: "sales"}, {Name: "orders", Schema: "archive"}}
	if _, ok := (Catalog{Tables: tables}).Lookup("", "orders"); ok {
		t.Fatal("ambiguous name resolved")
	}
	if e, ok := (Catalog{Tables: tables[:1]}).Lookup("", "orders"); !ok || e.Schema != "sales" {
		t.Fatalf("unique name = %+v, %v", e, ok)
	}
}

func TestReconcileKeepsConfiguredDescriptionsAndProcedures(t *testing.T) {
	t.Parallel()
	cat := Catalog{Default: "public", Tables: []entity.Entity{{Name: "users", Schema: "public", Description: "用户",
		Attributes: []entity.Attribute{{Name: "id", Description: "主键"}, {Name: "region", Description: "大区"}}}}}
	configured := []entity.Entity{
		{Name: "users", Attributes: []entity.Attribute{{Name: "id"}, {Name: "region", Description: "手写"}}},
		{Name: "refresh", Kind: entity.KindProcedure},
	}
	got, d := Reconcile(configured, cat)
	if len(d.Missing) != 0 || got[0].Description != "用户" || got[0].Attributes[0].Description != "主键" ||
		got[0].Attributes[1].Description != "手写" {
		t.Fatalf("got %+v, drift %+v", got[0], d)
	}
	if configured[0].Attributes[0].Description != "" {
		t.Fatal("input mutated")
	}
}

// An indexed catalog finds tables as a scan does: by folded name when the
// database folds case, per schema, and after Tables changed under the index.
func TestCatalogIndexMatchesScan(t *testing.T) {
	t.Parallel()
	cat := Catalog{Tables: []entity.Entity{
		{Name: "Orders", Schema: "a"}, {Name: "orders", Schema: "b"}, {Name: "users", Schema: "a"},
	}, Default: "a"}
	cat.Index()
	if got, ok := cat.Lookup("b", "orders"); !ok || got.Schema != "b" {
		t.Fatalf("b.orders = %+v, %v", got, ok)
	}
	if _, ok := cat.Lookup("", "orders"); ok {
		t.Fatal("names compare exactly unless the database folds case")
	}
	cat.FoldCase = true
	if got, ok := cat.Lookup("", "ORDERS"); !ok || got.Schema != "a" {
		t.Fatalf("folded ORDERS = %+v, %v", got, ok)
	}
	cat.Tables = append(cat.Tables, entity.Entity{Name: "events", Schema: "a"})
	if _, ok := cat.Lookup("", "events"); !ok {
		t.Fatal("a table added after indexing must still be found")
	}
}
