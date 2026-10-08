package config

import (
	"strings"
	"testing"
)

func relationsConfig(entities ...EntityConfig) *Config {
	c := &Config{
		Databases: map[string]DatabaseConfig{
			"main":    {Driver: "postgres", DSN: "x"},
			"archive": {Driver: "postgres", DSN: "y"},
		},
		Entities: entities,
	}
	c.ApplyDefaults()
	return c
}

func TestValidateRejectsTwoEntitiesOnOneRelation(t *testing.T) {
	t.Parallel()
	for name, entities := range map[string][]EntityConfig{
		"explicit source": {
			{Name: "orders", DataSource: "main", Schema: "s"},
			{Name: "orders_rw", DataSource: "main", Schema: "s", Source: "orders"},
		},
		"source from name": {
			{Name: "orders", DataSource: "main"},
			{Name: "orders_view", DataSource: "main", Source: "orders"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := relationsConfig(entities...).Validate()
			if err == nil || !strings.Contains(err.Error(), "same relation") {
				t.Fatalf("error = %v, want a duplicate relation error", err)
			}
		})
	}
	ok := relationsConfig(
		EntityConfig{Name: "orders", DataSource: "main", Schema: "a"},
		EntityConfig{Name: "orders_b", DataSource: "main", Schema: "b", Source: "orders"},
		EntityConfig{Name: "orders_archive", DataSource: "archive", Source: "orders"},
	)
	if err := ok.Validate(); err != nil {
		t.Fatalf("same table name in other schemas or datasources must be allowed: %v", err)
	}
}

func TestValidateProcedureAffects(t *testing.T) {
	t.Parallel()
	procedure := func(affects ...string) EntityConfig {
		return EntityConfig{Name: "refresh", DataSource: "main", Kind: "procedure", Affects: affects}
	}
	tables := []EntityConfig{
		{Name: "orders", DataSource: "main"},
		{Name: "archived", DataSource: "archive"},
		{Name: "other_proc", DataSource: "main", Kind: "procedure"},
	}
	for name, tc := range map[string]struct {
		entities []EntityConfig
		want     string
	}{
		"unknown entity":     {append([]EntityConfig{procedure("missing")}, tables...), "unknown entity"},
		"other datasource":   {append([]EntityConfig{procedure("archived")}, tables...), "datasource"},
		"procedure target":   {append([]EntityConfig{procedure("other_proc")}, tables...), "procedure"},
		"duplicate target":   {append([]EntityConfig{procedure("orders", "orders")}, tables...), "duplicate"},
		"affects on a table": {[]EntityConfig{{Name: "orders", DataSource: "main", Affects: []string{"x"}}}, "procedure"},
	} {
		t.Run(name, func(t *testing.T) {
			err := relationsConfig(tc.entities...).Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	if err := relationsConfig(append([]EntityConfig{procedure("orders")}, tables...)...).Validate(); err != nil {
		t.Fatalf("valid affects: %v", err)
	}
}
