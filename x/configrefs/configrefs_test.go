package configrefs

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
)

// sharedCases are core/config/testdata/entity_refs.json, which the console
// checks its own index against.
func sharedCases(t *testing.T) ([]config.EntityConfig, map[string]string) {
	t.Helper()
	data, err := os.ReadFile("../../core/config/testdata/entity_refs.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases struct {
		Entities []struct {
			Datasource, Schema, Name string
		}
		ShortNames map[string]string
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	var entities []config.EntityConfig
	for _, e := range cases.Entities {
		entities = append(entities, config.EntityConfig{Name: e.Name, DataSource: e.Datasource, Schema: e.Schema})
	}
	return entities, cases.ShortNames
}

func TestShortNameMatchesTheConsole(t *testing.T) {
	entities, want := sharedCases(t)
	x := NewIndex(entities)
	for _, e := range entities {
		if got := x.ShortName(e.ID()); got != want[e.ID()] {
			t.Errorf("ShortName(%s) = %q, want %q", e.ID(), got, want[e.ID()])
		}
	}
}

func entities(ids ...[3]string) []config.EntityConfig {
	var out []config.EntityConfig
	for _, id := range ids {
		out = append(out, config.EntityConfig{DataSource: id[0], Schema: id[1], Name: id[2]})
	}
	return out
}

func TestRetarget(t *testing.T) {
	t.Parallel()
	tenants := [3]string{"shop", "crm", "tenants"}
	before := NewIndex(entities(tenants, [3]string{"shop", "sales", "orders"}))
	for _, c := range []struct {
		name  string
		ref   string
		after []config.EntityConfig
		moved map[string]string
		want  string
		keep  bool
	}{
		{"unchanged", "tenants", entities(tenants), nil, "tenants", true},
		{"qualified once ambiguous", "tenants",
			entities(tenants, [3]string{"warehouse", "logistics", "tenants"}), nil, "crm.tenants", true},
		{"follows a rename", "tenants", entities([3]string{"shop", "crm", "clients"}),
			map[string]string{"shop.crm.tenants": "shop.crm.clients"}, "clients", true},
		{"follows a move, keeping the short form", "tenants", entities([3]string{"shop", "archive", "tenants"}),
			map[string]string{"shop.crm.tenants": "shop.archive.tenants"}, "tenants", true},
		{"goes with a removed entity", "tenants", entities([3]string{"shop", "sales", "orders"}), nil, "", false},
		{"unknown before stays", "ghost", entities(tenants), nil, "ghost", true},
		{"goes with a removed entity whose ID another took", "tenants", entities(tenants),
			map[string]string{"shop.crm.tenants": ""}, "", false},
	} {
		got, keep := Retarget(c.ref, before, NewIndex(c.after), c.moved)
		if got != c.want || keep != c.keep {
			t.Errorf("%s: Retarget(%q) = %q, %v; want %q, %v", c.name, c.ref, got, keep, c.want, c.keep)
		}
	}
}

func TestFilterFields(t *testing.T) {
	t.Parallel()
	filter := map[string]any{"or": []any{
		map[string]any{"op": "eq", "field": "region", "value": "${subject.region}"},
		map[string]any{"and": []any{map[string]any{"op": "is_null", "field": "deleted_at"}}},
	}}
	if got := FilterFields(filter); !slices.Equal(got, []string{"region", "deleted_at"}) {
		t.Fatalf("FilterFields = %v", got)
	}
	if got := FilterFields(nil); got != nil {
		t.Fatalf("no filter = %v", got)
	}
}
