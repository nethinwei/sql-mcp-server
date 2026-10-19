package bootstrap

import (
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

func TestConfigToEntityCarriesProcedureSettings(t *testing.T) {
	orders := config.EntityConfig{Name: "orders", Schema: "sales"}
	e, err := configToEntity(config.EntityConfig{
		Name: "refresh", Kind: "procedure", Params: []string{"day"}, Affects: []string{"orders"}, AllowCascade: true,
	}, config.NewEntityRefs([]config.EntityConfig{orders}))
	if err != nil {
		t.Fatal(err)
	}
	// Named by its ID, and what it affects resolved to IDs.
	if e.Name != "default.refresh" || e.Local != "refresh" ||
		!slices.Equal(e.Affects, []string{"default.sales.orders"}) || !slices.Equal(e.Params, []string{"day"}) ||
		!e.AllowCascade {
		t.Fatalf("entity = %+v", e)
	}
}

func TestWithRowColumnsGathersEveryRowScope(t *testing.T) {
	cond := func(field string) relalg.Predicate {
		return relalg.Condition{Field: field, Op: relalg.OpEq, Value: 1}
	}
	entities := []entity.Entity{{
		Name:         "default.orders",
		TenantPolicy: cond("tenant_id"),
		RowPolicies:  entity.RowPolicies{"reader": relalg.Not{P: cond("deleted")}},
	}}
	withRowColumns(entities, rbac.Policy{
		Roles: map[string]map[string][]rbac.Grant{"sales": {"default.orders": {{Rows: cond("region")}}}},
		Principals: map[string]rbac.Principal{
			"user:ann": {Grants: map[string][]rbac.Grant{"default.orders": {{Rows: cond("owner")}, {}}}},
		},
	})
	if got := entities[0].RowColumns; !slices.Equal(got, []string{"deleted", "owner", "region", "tenant_id"}) {
		t.Fatalf("RowColumns = %v", got)
	}
}

// What an App found missing separates gone tables, missing fields and the
// row-scope columns that are not fields.
func TestAppMissingSeparatesFieldsFromRowColumns(t *testing.T) {
	entities := []entity.Entity{
		{Name: "default.orders", Attributes: []entity.Attribute{{Name: "id"}, {Name: "note"}}},
		{Name: "default.gone"},
		{Name: "default.fine"},
	}
	markUnavailable("default", entities, map[string][]string{
		"default.orders": {"note", "tenant_id"}, "default.gone": nil,
	})
	reg, err := entity.NewRegistry(entities)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Registry: reg, Unavailable: unavailableOf(reg.Entities(), nil)}
	got := app.Missing()
	if !got.Tables["default.gone"] || len(got.Tables) != 1 ||
		!slices.Equal(got.Fields["default.orders"], []string{"note"}) ||
		!slices.Equal(got.RowColumns["default.orders"], []string{"tenant_id"}) {
		t.Fatalf("missing = %+v", got)
	}
}
