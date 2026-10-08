package bootstrap

import (
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
)

func TestConfigToEntityCarriesProcedureSettings(t *testing.T) {
	e, err := configToEntity(config.EntityConfig{
		Name: "refresh", Kind: "procedure", Params: []string{"day"}, Affects: []string{"orders"}, AllowCascade: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.Affects, []string{"orders"}) || !slices.Equal(e.Params, []string{"day"}) || !e.AllowCascade {
		t.Fatalf("entity = %+v", e)
	}
}
