package configplan

import (
	"encoding/json"
	"reflect"
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

// A table the catalog lists that the serving configuration found missing is
// removed, and the field rules and grants that follow leave it, and every
// other entity, alone.
func TestRemovedTablesLeaveTheRulesThatFollow(t *testing.T) {
	t.Parallel()
	base := decode(t, goneYAML)
	missing := map[string]bool{"shop.public.customers": true}
	p, err := Evaluate(Input{Base: base, Published: base, Sources: shop(), Missing: missing}, Plan{
		Sync:   &SyncRule{RemoveTables: true},
		Fields: []FieldRule{{Columns: []string{"*"}, Exclude: true}},
		Grants: []GrantRule{{Principal: "role:analyst", Actions: []string{"read", "update"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Removed, []string{"shop.public.customers", "shop.public.vanished"}) {
		t.Fatalf("removed = %v", p.Removed)
	}
	for _, f := range entityOf(t, p, "shop.archive.old").Fields {
		if f.Exclude {
			t.Fatalf("a field rule changed an entity it does not apply to: %+v", f)
		}
	}
	for _, c := range p.Report.Changes {
		if slices.Contains(c.Entities, "shop.public.customers") && c.Kind != ChangeRemoveTable {
			t.Fatalf("a removed entity is reported changed: %+v", c)
		}
	}
	valid(t, base, p)
}

// Evaluating a plan leaves the caller's plan as it was.
func TestEvaluateLeavesThePlan(t *testing.T) {
	t.Parallel()
	base := decode(t, goneYAML)
	actions := []string{"UPDATE", "read", "read"}
	p := Plan{Grants: []GrantRule{{Principal: "role:analyst", Actions: actions}}}
	if _, err := Evaluate(Input{Base: base, Published: base, Sources: shop()}, p); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Grants[0].Actions, []string{"UPDATE", "read", "read"}) ||
		!slices.Equal(actions, []string{"UPDATE", "read", "read"}) {
		t.Fatalf("the plan changed: %v %v", p.Grants[0].Actions, actions)
	}
}

// Evaluating against an Index of the same tables proposes what evaluating
// without it does.
func TestEvaluateWithAnIndex(t *testing.T) {
	t.Parallel()
	base := decode(t, goneYAML)
	plans := []Plan{
		{Import: true},
		{Import: true, Scope: Scope{Schemas: []string{"pub*"}},
			Fields: []FieldRule{{Columns: []string{"*phone"}, Mask: "phone"}}},
		{Sync: &SyncRule{Add: true, Remove: true, RemoveTables: true}},
		{Grants: []GrantRule{{Principal: "role:analyst", Actions: []string{"read"}}},
			Scope: Scope{Status: []Status{StatusNew}}},
	}
	for i, p := range plans {
		in := Input{Base: base, Published: base, Sources: shop(), Missing: map[string]bool{"shop.archive.old": true}}
		want, err := Evaluate(in, p)
		if err != nil {
			t.Fatal(err)
		}
		in.Index = NewIndex(in.Sources, in.Base, in.Published)
		got, err := Evaluate(in, p)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("plan %d: with an index %+v, %v\nwithout %+v", i, got.Report, err, want.Report)
		}
	}
}

// Evaluating leaves the base as it is, whatever the plan does: the console
// shares the configurations it evaluates plans against.
func TestEvaluateLeavesTheBase(t *testing.T) {
	t.Parallel()
	base := decode(t, goneYAML)
	was, _ := json.Marshal(base)
	plans := []Plan{
		{Import: true, Fields: []FieldRule{{Columns: []string{"*"}, Mask: "phone"}}},
		{Sync: &SyncRule{Add: true, Remove: true, RemoveTables: true, Keys: true}},
		{Grants: []GrantRule{{Principal: "role:analyst", Actions: []string{"read", "update"}, Fields: []string{"id"}}}},
		{Grants: []GrantRule{{Principal: "user:alice", Actions: []string{"read"}}},
			Fields: []FieldRule{{Columns: []string{"id"}, Exclude: true}}},
	}
	for i, p := range plans {
		in := Input{Base: base, Published: base, Sources: shop(), Missing: map[string]bool{"shop.archive.old": true},
			MissingFields: map[string][]string{"shop.public.customers": {"name"}}}
		if _, err := Evaluate(in, p); err != nil {
			t.Fatal(err)
		}
		if now, _ := json.Marshal(base); string(now) != string(was) {
			t.Fatalf("plan %d changed the base:\n%s\nwas\n%s", i, now, was)
		}
	}
}
