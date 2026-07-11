package entity

import (
	"errors"
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
