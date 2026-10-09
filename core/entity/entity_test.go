package entity

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"
)

func sampleEntity() Entity {
	return Entity{
		Name: "users", Source: "t_user", Schema: "public", Kind: KindTable,
		Attributes: []Attribute{
			{Name: "id"},
			{Name: "email"},
			{Name: "phone", Excluded: true},
		},
		Keys: []Key{{Name: "pk", Columns: []string{"id"}, Primary: true}},
		Role: RoleAccess{ActionRead: []string{"reader"}},
	}
}

func TestNewRegistryRejectsDuplicate(t *testing.T) {
	t.Parallel()
	e := sampleEntity()
	_, err := NewRegistry([]Entity{e, e})
	if !errors.Is(err, ErrDuplicateEntity) {
		t.Fatalf("got %v, want ErrDuplicateEntity", err)
	}
}

func TestNewRegistryRejectsEmptyName(t *testing.T) {
	t.Parallel()
	_, err := NewRegistry([]Entity{{Name: ""}})
	if !errors.Is(err, ErrEmptyName) {
		t.Fatalf("got %v, want ErrEmptyName", err)
	}
}

func TestResolveAppliesProjection(t *testing.T) {
	t.Parallel()
	r, _ := NewRegistry([]Entity{sampleEntity()})
	res, ok := r.Resolve("users")
	if !ok {
		t.Fatal("entity not found")
	}
	if len(res.Attributes) != 2 {
		t.Fatalf("got %d attrs, want 2 (phone excluded)", len(res.Attributes))
	}
	for _, a := range res.Attributes {
		if a.Name == "phone" {
			t.Fatal("excluded attribute leaked into resolved view")
		}
	}
}

func TestResolveNotFound(t *testing.T) {
	t.Parallel()
	r, _ := NewRegistry([]Entity{sampleEntity()})
	if _, ok := r.Resolve("nope"); ok {
		t.Fatal("expected not found")
	}
}

func TestPrimaryKey(t *testing.T) {
	t.Parallel()
	e := sampleEntity()
	pk := e.PrimaryKey()
	if len(pk) != 1 || pk[0] != "id" {
		t.Fatalf("got %v, want [id]", pk)
	}
	Entity{}.PrimaryKey() // nil-safe; no panic expected
}

func TestAttributeByNameAlias(t *testing.T) {
	t.Parallel()
	e := Entity{Attributes: []Attribute{{Name: "email", Alias: "mail"}}}
	if _, ok := e.AttributeByName("mail"); !ok {
		t.Fatal("alias not matched")
	}
	if _, ok := e.AttributeByName("missing"); ok {
		t.Fatal("expected miss")
	}
}

func TestEntitiesIsCopy(t *testing.T) {
	t.Parallel()
	r, _ := NewRegistry([]Entity{sampleEntity()})
	got := r.Entities()
	got[0].Name = "tampered"
	if _, ok := r.Resolve("users"); !ok {
		t.Fatal("registry mutated via Entities() return slice")
	}
}

func TestActionString(t *testing.T) {
	t.Parallel()
	if ActionRead.String() != "read" {
		t.Fatal("read string mismatch")
	}
	if Action(99).String() != "unknown" {
		t.Fatal("unknown string mismatch")
	}
}

func TestRegistryResolvesReferencesLikeTheConfiguration(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../config/testdata/entity_refs.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases struct {
		Entities []struct{ Datasource, Schema, Name string }
		Cases    []struct {
			Ref, Want  string
			Candidates []string
		}
		ShortNames map[string]string
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	var entities []Entity
	for _, e := range cases.Entities {
		entities = append(entities, Entity{
			Name: ID(e.Datasource, e.Schema, e.Name), Local: e.Name, DataSource: e.Datasource, Schema: e.Schema,
		})
	}
	reg, err := NewRegistry(entities)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases.Cases {
		var got []string
		for _, e := range reg.Match(c.Ref) {
			got = append(got, e.Name)
		}
		want := c.Candidates
		if c.Want != "" {
			want = []string{c.Want}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%q matches %q, want %q", c.Ref, got, want)
		}
		if _, ok := reg.Resolve(c.Ref); ok != (c.Want != "") {
			t.Errorf("%q resolves = %v", c.Ref, ok)
		}
	}
	for _, e := range entities {
		if got := reg.ShortName(e, nil); got != cases.ShortNames[e.Name] {
			t.Errorf("short name of %s = %q, want %q", e.Name, got, cases.ShortNames[e.Name])
		}
	}
}

func TestShortNameAmongVisibleEntities(t *testing.T) {
	t.Parallel()
	tenants := func(datasource string) Entity {
		return Entity{Name: ID(datasource, "", "tenants"), Local: "tenants", DataSource: datasource}
	}
	reg, err := NewRegistry([]Entity{tenants("shop"), tenants("warehouse")})
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.ShortName(tenants("shop"), nil); got != "shop.tenants" {
		t.Errorf("short name = %q, want shop.tenants", got)
	}
	onlyShop := func(e Entity) bool { return e.DataSource == "shop" }
	if got := reg.ShortName(tenants("shop"), onlyShop); got != "tenants" {
		t.Errorf("short name among shop entities = %q, want tenants", got)
	}
}
