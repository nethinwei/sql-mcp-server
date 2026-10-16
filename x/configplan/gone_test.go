package configplan

import (
	"slices"
	"testing"
)

const goneYAML = `databases:
  shop: {driver: postgres, dsn: "postgres://u@h/shop"}
entities:
  - name: customers
    datasource: shop
    schema: public
    primaryKey: [id]
    fields: [{name: id}, {name: name}]
    relationships:
      - {name: snapshots, target: shop.public.vanished, cardinality: many, joinOn: {id: customer_id}}
  - name: vanished
    datasource: shop
    schema: public
    fields: [{name: id}, {name: customer_id}]
  - name: old
    datasource: shop
    schema: archive
    fields: [{name: id}]
roles:
  analyst:
    grants:
      - {entity: shop.public.customers, actions: [read]}
      - {entity: shop.public.vanished, actions: [read]}
users:
  alice:
    roles: [analyst]
    grants:
      - {entity: shop.archive.old, actions: [read]}
`

func goneIDs(gone []Gone) []string {
	var out []string
	for _, g := range gone {
		out = append(out, g.Entity)
	}
	return out
}

// A table is gone when a scan of its schema missed it, or the serving
// configuration found it missing; a schema never scanned proves nothing.
func TestGoneTables(t *testing.T) {
	t.Parallel()
	base := decode(t, goneYAML)
	if got := goneIDs(GoneTables(shop(), base, Scope{}, nil)); !slices.Equal(got, []string{"shop.public.vanished"}) {
		t.Fatalf("gone = %v", got)
	}
	missing := map[string]bool{"shop.archive.old": true}
	gone := GoneTables(shop(), base, Scope{}, missing)
	if got := goneIDs(gone); !slices.Equal(got, []string{"shop.archive.old", "shop.public.vanished"}) ||
		gone[0].Scanned || !gone[1].Scanned {
		t.Fatalf("gone = %+v", gone)
	}
	if got := goneIDs(GoneTables(shop(), base, Scope{Schemas: []string{"archive"}}, missing)); !slices.Equal(
		got, []string{"shop.archive.old"}) {
		t.Fatalf("gone in archive = %v", got)
	}
}

// Removing gone tables takes the grants and relationships naming them along,
// keeps what the plan excludes, and validates.
func TestSyncRemovesGoneTables(t *testing.T) {
	t.Parallel()
	base := decode(t, goneYAML)
	sync := &SyncRule{RemoveTables: true}
	missing := map[string]bool{"shop.archive.old": true}
	p, err := Evaluate(Input{Base: base, Published: base, Sources: shop(), Missing: missing}, Plan{Sync: sync})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Removed, []string{"shop.archive.old", "shop.public.vanished"}) || p.Empty() {
		t.Fatalf("removed = %v", p.Removed)
	}
	if rels := entityOf(t, p, "shop.public.customers").Relationships; len(rels) != 0 {
		t.Fatalf("a relationship to a removed entity stays: %+v", rels)
	}
	if g := p.Config.Roles["analyst"].Grants; len(g) != 1 || g[0].Entity != "shop.public.customers" {
		t.Fatalf("analyst grants = %+v", g)
	}
	if g := p.Config.Users["alice"].Grants; len(g) != 0 {
		t.Fatalf("alice grants = %+v", g)
	}
	if len(p.Report.Changes) != 1 || p.Report.Changes[0].Kind != ChangeRemoveTable {
		t.Fatalf("changes = %+v", p.Report.Changes)
	}
	cfg := valid(t, base, p)
	if len(cfg.Entities) != 1 {
		t.Fatalf("entities after = %d", len(cfg.Entities))
	}

	kept, err := Evaluate(Input{Base: base, Published: base, Sources: shop()},
		Plan{Sync: sync, Exclude: []string{"shop.public.vanished"}})
	if err != nil || len(kept.Removed) != 0 || !kept.Empty() {
		t.Fatalf("an excluded gone table was removed: %v %v", kept.Removed, err)
	}
}
