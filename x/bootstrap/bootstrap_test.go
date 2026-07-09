package bootstrap

import (
	"os"
	"testing"

	"github.com/nethinwei/sql-mcp-server/config"
	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/relalg"
)

func TestLoad(t *testing.T) {
	t.Parallel()
	yamlContent := []byte(`
database:
  driver: postgres
  dsn: postgres://localhost/test
entities:
  - name: users
    source: t_user
    primaryKey: [id]
    fields:
      - name: id
      - name: email
`)
	tmp, err := os.CreateTemp("", "cfg-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(yamlContent); err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	cfg, err := Load(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Driver != "postgres" {
		t.Fatalf("driver = %q", cfg.Database.Driver)
	}
	if len(cfg.Entities) != 1 {
		t.Fatalf("entities = %d", len(cfg.Entities))
	}
	if !cfg.Tools.ReadRecords {
		t.Fatal("ApplyDefaults should enable ReadRecords")
	}
	if cfg.Entities[0].MCP.DMLTools != true {
		t.Fatal("entity MCP.DMLTools should default true")
	}
}

func TestConfigToEntities(t *testing.T) {
	t.Parallel()
	ecs := []config.EntityConfig{
		{
			Name: "users", Source: "t_user", Kind: "table", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "phone", Exclude: true}},
			Roles:  config.RoleConfig{Read: []string{"reader"}},
			MCP:    config.MCPFlags{DMLTools: true},
			RowPolicies: config.RowPolicies{
				"reader": config.FilterConfig{"op": "eq", "field": "tenant_id", "value": 7},
			},
		},
	}
	entities, err := configToEntities(ecs)
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 1 {
		t.Fatalf("got %d entities", len(entities))
	}
	e := entities[0]
	if e.Source != "t_user" || e.Kind != entity.KindTable {
		t.Fatalf("entity = %+v", e)
	}
	if len(e.Keys) != 1 || e.Keys[0].Columns[0] != "id" || !e.Keys[0].Primary {
		t.Fatalf("keys = %+v", e.Keys)
	}
	if len(e.Attributes) != 2 {
		t.Fatalf("attrs = %+v", e.Attributes)
	}
	rp := e.RowPolicies["reader"]
	if rp == nil {
		t.Fatal("row policy not set")
	}
	c, ok := rp.(relalg.Condition)
	if !ok || c.Field != "tenant_id" || c.Value != 7 {
		t.Fatalf("row policy = %+v", rp)
	}
}

func TestFilterConfigToPredicate(t *testing.T) {
	t.Parallel()
	p, err := filterConfigToPredicate(config.FilterConfig{"op": "eq", "field": "id", "value": 1})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := p.(relalg.Condition)
	if !ok || c.Op != relalg.OpEq || c.Field != "id" {
		t.Fatalf("got %+v", p)
	}

	p2, err := filterConfigToPredicate(config.FilterConfig{
		"and": []any{
			config.FilterConfig{"op": "gt", "field": "id", "value": 0},
			config.FilterConfig{"op": "is_null", "field": "deleted_at"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := p2.(relalg.And)
	if !ok || len(a.Preds) != 2 {
		t.Fatalf("got %+v", p2)
	}
}

func TestResolveSecrets(t *testing.T) {
	t.Parallel()
	os.Setenv("TEST_DSN_VAR", "postgres://x")
	defer os.Unsetenv("TEST_DSN_VAR")
	got, err := resolveSecrets("host=${TEST_DSN_VAR}")
	if err != nil {
		t.Fatal(err)
	}
	if got != "host=postgres://x" {
		t.Fatalf("got %q", got)
	}
	if _, err := resolveSecrets("${MISSING_VAR_ZZZ}"); err == nil {
		t.Fatal("expected error for missing env")
	}
}

func TestNewProviderUnsupported(t *testing.T) {
	t.Parallel()
	if _, err := newProvider("oracle", ""); err == nil {
		t.Fatal("expected error for unsupported driver")
	}
	if _, err := newProvider("mysql", ""); err == nil {
		t.Fatal("mysql provider not yet implemented, expected error")
	}
}
