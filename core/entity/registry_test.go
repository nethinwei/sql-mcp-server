package entity

import "testing"

// Resolving allocates nothing: every call resolves its entity, several
// times; Procedures yields the procedures alone.
func TestResolveAllocatesNothing(t *testing.T) {
	reg, err := NewRegistry([]Entity{
		{Name: "shop.orders", Schema: "public", Source: "orders"},
		{Name: "shop.refund", Kind: KindProcedure},
	})
	if err != nil {
		t.Fatal(err)
	}
	if allocs := testing.AllocsPerRun(100, func() {
		if _, ok := reg.Resolve("shop.orders"); !ok {
			t.Fatal("not resolved")
		}
	}); allocs != 0 {
		t.Fatalf("Resolve allocates %v times", allocs)
	}
	var procedures []string
	for e := range reg.Procedures() {
		procedures = append(procedures, e.Name)
	}
	if len(procedures) != 1 || procedures[0] != "shop.refund" {
		t.Fatalf("procedures = %v", procedures)
	}
}
