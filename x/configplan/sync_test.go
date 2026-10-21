package configplan

import (
	"maps"
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
)

const syncYAML = `databases:
  shop: {driver: postgres, dsn: "postgres://u@h/shop"}
entities:
  - name: customers
    datasource: shop
    schema: public
    primaryKey: [id]
    uniqueKeys: [[legacy]]
    fields: [{name: id}, {name: name}, {name: legacy}]
    relationships:
      - {name: orders, target: shop.public.orders, cardinality: has-many, joinOn: {id: customer_id}}
      - {name: logs, target: shop.public.logs, cardinality: has-many, joinOn: {name: line}}
      - {name: notes, target: shop.public.logs, cardinality: has-many, joinOn: {legacy: line}}
  - name: orders
    datasource: shop
    schema: public
    primaryKey: [id]
    fields: [{name: id}, {name: customer_id}, {name: note}]
    relationships:
      - {name: by_legacy, target: shop.public.customers, cardinality: belongs-to, joinOn: {note: legacy}}
  - name: logs
    datasource: shop
    schema: public
    kind: view
    fields: [{name: at}, {name: line}]
  - name: old
    datasource: shop
    schema: archive
    fields: [{name: id}, {name: tenant}]
`

func changeKinds(cs []Change) []string {
	var out []string
	for _, c := range cs {
		for _, e := range c.Entities {
			out = append(out, c.Kind+" "+e)
		}
	}
	return out
}

// Keys and kinds follow the tables; relationships are only reported, and a
// relationship joining on a column the keys drop asks for a look.
func TestSyncKeysAndKinds(t *testing.T) {
	t.Parallel()
	base := decode(t, syncYAML)
	p := evaluate(t, base, Plan{Sync: &SyncRule{Keys: true}})
	customers := entityOf(t, p, "shop.public.customers")
	if !slices.Equal(customers.PrimaryKey, []string{"id"}) || len(customers.UniqueKeys) != 0 {
		t.Fatalf("customers keys = %v %v", customers.PrimaryKey, customers.UniqueKeys)
	}
	if k := entityOf(t, p, "shop.public.logs").Kind; k != "" {
		t.Fatalf("logs kind = %q, want a table", k)
	}
	got := changeKinds(p.Report.Changes)
	for _, want := range []string{"syncKeys shop.public.customers", "syncKind shop.public.logs"} {
		if !slices.Contains(got, want) {
			t.Errorf("changes %v lack %q", got, want)
		}
	}
	exceptions := kinds(p.Report.Exceptions)
	for _, want := range []string{
		"keyReferenced shop.public.customers legacy: relationship shop.public.customers.notes",
		"keyReferenced shop.public.customers legacy: relationship shop.public.orders.by_legacy",
		"relationshipNew shop.public.orders belongs-to shop.public.customers (customer_id = id)",
		"relationshipStale shop.public.customers logs",
	} {
		if !slices.Contains(exceptions, want) {
			t.Errorf("exceptions %v lack %q", exceptions, want)
		}
	}
	if slices.ContainsFunc(p.Report.Exceptions, func(x Exception) bool { return x.Detail == "notes" }) {
		t.Errorf("a relationship not named as imported ones are was reported: %v", exceptions)
	}
	// Relationships are left as they are, written by hand or not.
	if !slices.EqualFunc(customers.Relationships, base.Entities[0].Relationships, relationshipsEqual) {
		t.Fatalf("relationships changed: %+v", customers.Relationships)
	}
	valid(t, base, p)
	again := evaluate(t, valid(t, base, p), Plan{Sync: &SyncRule{Keys: true}})
	if !again.Empty() {
		t.Fatalf("a second sync changes %v", again.Report.Changes)
	}
}

// A sync limited to entities applies to theirs alone, gone tables included.
func TestScopeEntities(t *testing.T) {
	t.Parallel()
	base := decode(t, syncYAML)
	p := evaluate(t, base, Plan{Scope: Scope{Entities: []string{"shop.public.orders"}},
		Sync: &SyncRule{Keys: true, Add: true, Remove: true}})
	if p.Report.Matched != 1 {
		t.Fatalf("matched %d tables", p.Report.Matched)
	}
	for _, c := range p.Report.Changes {
		if !slices.Equal(c.Entities, []string{"shop.public.orders"}) {
			t.Fatalf("a change outside the scope: %+v", c)
		}
	}
	missing := map[string]bool{"shop.archive.old": true}
	if g := GoneTables(shop(), base, Scope{Entities: []string{"shop.public.orders"}}, missing); len(g) != 0 {
		t.Fatalf("gone outside the scope: %+v", g)
	}
	if g := GoneTables(shop(), base, Scope{Entities: []string{"shop.archive.old"}}, missing); len(g) != 1 {
		t.Fatalf("gone in the scope: %+v", g)
	}
}

// What the serving configuration found missing is synced for an entity no
// scan covers; a scanned table goes by its scan. A missing column a row
// scope tests (not a field) is reported, never removed.
func TestSyncRuntimeMissingColumns(t *testing.T) {
	t.Parallel()
	base := decode(t, syncYAML)
	in := Input{Base: base, Published: base, Sources: shop(),
		MissingFields:     map[string][]string{"shop.archive.old": {"tenant"}, "shop.public.customers": {"name"}},
		MissingRowColumns: map[string][]string{"shop.public.orders": {"tenant_id"}}}
	p, err := Evaluate(in, Plan{Sync: &SyncRule{Remove: true}})
	if err != nil {
		t.Fatal(err)
	}
	if f := entityOf(t, p, "shop.archive.old").Fields; len(f) != 1 || f[0].Name != "id" {
		t.Fatalf("old fields = %+v", f)
	}
	if f := entityOf(t, p, "shop.public.customers").Fields; len(f) != 3 {
		t.Fatalf("a scanned table lost a field its scan has: %+v", f)
	}
	if !slices.Contains(changeKinds(p.Report.Changes), "syncRemove shop.archive.old") {
		t.Fatalf("changes = %+v", p.Report.Changes)
	}
	x := p.Report.Exceptions
	if !slices.ContainsFunc(x, func(x Exception) bool {
		return x.Kind == ExceptionRowColumnMissing && x.Entity == "shop.public.orders" && x.Detail == "tenant_id" &&
			x.Level == LevelConfirm
	}) {
		t.Fatalf("exceptions = %v", kinds(x))
	}
	valid(t, base, p)
	scoped, err := Evaluate(in, Plan{Scope: Scope{Schemas: []string{"public"}}, Sync: &SyncRule{Remove: true}})
	if err != nil || slices.Contains(changeKinds(scoped.Report.Changes), "syncRemove shop.archive.old") {
		t.Fatalf("an entity outside the scope was synced: %v %v", scoped.Report.Changes, err)
	}
}

func TestImportedRelationshipNames(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"orders": true, "orders_2": true, "orders_10": true, "orders_1": false, "orders_02": false,
		"orders_x": false, "my_orders": false,
	} {
		if got := importedName(name, "orders"); got != want {
			t.Errorf("importedName(%q) = %v", name, got)
		}
	}
}

func relationshipsEqual(a, b config.RelationshipConfig) bool {
	return a.Name == b.Name && a.Target == b.Target && a.Cardinality == b.Cardinality && maps.Equal(a.JoinOn, b.JoinOn)
}

// An entity configuring a subset of its table's columns keeps out the
// scanned keys on columns it has no field for, and reports them; a sync
// adding the columns syncs the key too.
func TestSyncKeysOnUnconfiguredColumns(t *testing.T) {
	t.Parallel()
	base := decode(t, `databases:
  shop: {driver: postgres, dsn: "postgres://u@h/shop"}
entities:
  - {name: customers, datasource: shop, schema: public, fields: [{name: name}]}
`)
	p := evaluate(t, base, Plan{Scope: Scope{Entities: []string{"shop.public.customers"}}, Sync: &SyncRule{Keys: true}})
	if e := entityOf(t, p, "shop.public.customers"); len(e.PrimaryKey) != 0 {
		t.Fatalf("customers keys = %v", e.PrimaryKey)
	}
	if !slices.ContainsFunc(p.Report.Exceptions, func(x Exception) bool {
		return x.Kind == ExceptionKeyNotField && x.Ref == "shop.public.customers" && x.Detail == "id" &&
			x.Level == LevelConfirm
	}) {
		t.Fatalf("exceptions = %v", kinds(p.Report.Exceptions))
	}
	valid(t, base, p)
	p = evaluate(t, base, Plan{Scope: Scope{Entities: []string{"shop.public.customers"}},
		Sync: &SyncRule{Keys: true, Add: true}})
	if e := entityOf(t, p, "shop.public.customers"); !slices.Equal(e.PrimaryKey, []string{"id"}) {
		t.Fatalf("customers keys = %v", e.PrimaryKey)
	}
	valid(t, base, p)
}

// joinsOn lists, from its index, what a scan of every relationship finds:
// the relationships joining on the column, on either side, each once.
func TestJoinsOnMatchesEveryRelationship(t *testing.T) {
	t.Parallel()
	base := decode(t, syncYAML+`  - name: tree
    datasource: shop
    schema: public
    fields: [{name: id}, {name: parent}]
    relationships:
      - {name: parent, target: tree, cardinality: belongs-to, joinOn: {parent: id}}
      - {name: self, target: tree, cardinality: one-to-one, joinOn: {id: id}}
`)
	ev := newEvaluation(Input{Base: base, Published: base, Sources: shop()}, Plan{})
	for _, e := range base.Entities {
		for _, f := range e.Fields {
			var want []string
			for _, from := range base.Entities {
				for _, r := range from.Relationships {
					target, ok := ev.resolve(r.Target)
					for local, remote := range r.JoinOn {
						if from.ID() == e.ID() && local == f.Name || ok && target == e.ID() && remote == f.Name {
							want = append(want, "relationship "+from.ID()+"."+r.Name)
							break
						}
					}
				}
			}
			if got := ev.joinsOn(e.ID(), f.Name); !slices.Equal(got, want) {
				t.Errorf("joinsOn(%s, %s) = %v, want %v", e.ID(), f.Name, got, want)
			}
		}
	}
}
