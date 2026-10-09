package bootstrap

import (
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
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
