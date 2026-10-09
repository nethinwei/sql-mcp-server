package config

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// entityRefCases is testdata/entity_refs.json, which the registry and the
// console resolve by too.
type entityRefCases struct {
	Entities []struct{ Datasource, Schema, Name string }
	Cases    []struct {
		Ref, Want  string
		Candidates []string
	}
	ShortNames map[string]string
}

func loadEntityRefCases(t *testing.T) (entityRefCases, []EntityConfig) {
	t.Helper()
	data, err := os.ReadFile("testdata/entity_refs.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases entityRefCases
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	entities := make([]EntityConfig, 0, len(cases.Entities))
	for _, e := range cases.Entities {
		entities = append(entities, EntityConfig{DataSource: e.Datasource, Schema: e.Schema, Name: e.Name})
	}
	return cases, entities
}

func TestEntityRefsResolve(t *testing.T) {
	t.Parallel()
	cases, entities := loadEntityRefCases(t)
	refs := NewEntityRefs(entities)
	for _, c := range cases.Cases {
		got, err := refs.Resolve(c.Ref)
		switch {
		case c.Want != "":
			if err != nil || got.ID() != c.Want {
				t.Errorf("%q names %q (%v), want %q", c.Ref, got.ID(), err, c.Want)
			}
		case c.Candidates != nil:
			if !errors.Is(err, ErrAmbiguousEntity) || !strings.Contains(err.Error(), strings.Join(c.Candidates, ", ")) {
				t.Errorf("%q: %v, want ambiguous among %q", c.Ref, err, c.Candidates)
			}
		default:
			if !errors.Is(err, ErrUnknownEntity) {
				t.Errorf("%q: %v, want unknown", c.Ref, err)
			}
		}
	}
}

func TestEntityReferencesMustNameOneEntity(t *testing.T) {
	t.Parallel()
	cfg := func(grant string, entities ...EntityConfig) *Config {
		return &Config{
			Databases: map[string]DatabaseConfig{
				"shop": {Driver: "postgres", DSN: "x"}, "warehouse": {Driver: "mysql", DSN: "y"},
			},
			Entities: entities,
			Roles: map[string]RoleDefinition{"ops": {Grants: []GrantConfig{
				{Entity: grant, Actions: []string{"read"}},
			}}},
		}
	}
	shop := EntityConfig{Name: "tenants", DataSource: "shop", Schema: "crm"}
	warehouse := EntityConfig{Name: "tenants", DataSource: "warehouse"}
	// Same names in different namespaces are separate entities.
	if err := validated(cfg("warehouse.tenants", shop, warehouse)); err != nil {
		t.Fatalf("qualified reference: %v", err)
	}
	// A bare name that a newly added entity makes ambiguous fails, rather
	// than silently naming either.
	if err := validated(cfg("tenants", shop)); err != nil {
		t.Fatalf("unique bare name: %v", err)
	}
	err := validated(cfg("tenants", shop, warehouse))
	if !errors.Is(err, ErrAmbiguousEntity) || !strings.Contains(err.Error(), "shop.crm.tenants, warehouse.tenants") {
		t.Fatalf("ambiguous grant: %v", err)
	}
	// Names are unique within a datasource and schema only.
	if err := validated(cfg("tenants", shop, shop)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate entity: %v", err)
	}
	dotted := EntityConfig{Name: "crm.tenants", DataSource: "shop"}
	if err := validated(cfg("shop.tenants", shop, dotted)); err == nil {
		t.Fatal("an entity name with a dot passed validation")
	}
}

func validated(c *Config) error {
	c.ApplyDefaults()
	return c.Validate()
}

func TestEntityReferencesOfRelationshipsAndAffects(t *testing.T) {
	t.Parallel()
	refs := NewEntityRefs([]EntityConfig{
		{Name: "orders", DataSource: "shop", Schema: "sales"},
		{Name: "orders", DataSource: "shop", Schema: "archive"},
	})
	for _, ref := range []string{"sales.orders", "shop.archive.orders"} {
		if _, err := refs.Resolve(ref); err != nil {
			t.Errorf("%q: %v", ref, err)
		}
	}
	if _, err := refs.Resolve("shop.orders"); !errors.Is(err, ErrAmbiguousEntity) {
		t.Errorf("shop.orders: %v", err)
	}
	a, b := EntityConfig{Name: "a", DataSource: "d"}, EntityConfig{Name: "a", DataSource: "d", Schema: "s"}
	if a.ID() != "d.a" || b.ID() != "d.s.a" {
		t.Errorf("IDs = %q, %q", a.ID(), b.ID())
	}
}
