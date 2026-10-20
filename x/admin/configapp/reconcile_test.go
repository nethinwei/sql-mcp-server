package configapp

import (
	"encoding/json"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/configrefs"
)

// The cases follow the console's former edit cascades (web/admin's
// workspace.test.ts), which reconcile replaces.

func decoded(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("%s: %v", s, err)
	}
	return v
}

func entityRef(t *testing.T, item string) workspace.Ref {
	t.Helper()
	v, _ := object(decoded(t, item))
	return workspace.Ref{Kind: ItemEntity, Key: entity.ID(text(v, "datasource"), text(v, "schema"), text(v, "name"))}
}

// items are entity, role and user items, as JSON.
func items(t *testing.T, entities, roles, users []string) workspace.Snapshot {
	t.Helper()
	var out workspace.Snapshot
	for _, e := range entities {
		out = out.With(entityRef(t, e), decoded(t, e))
	}
	for kind, list := range map[string][]string{ItemRole: roles, ItemUser: users} {
		for _, it := range list {
			v, _ := object(decoded(t, it))
			out = out.With(workspace.Ref{Kind: kind, Key: text(v, "name")}, v)
		}
	}
	return out
}

// written is before with the item values set (an empty value removes it).
func written(t *testing.T, before workspace.Snapshot, set map[workspace.Ref]string) workspace.Snapshot {
	t.Helper()
	out := before
	for ref, v := range set {
		if v == "" {
			out = out.With(ref, workspace.Missing)
		} else {
			out = out.With(ref, decoded(t, v))
		}
	}
	return out
}

func mustReconcile(t *testing.T, before, after workspace.Snapshot, renames map[workspace.Ref]workspace.Ref,
) workspace.Snapshot {
	t.Helper()
	was, _ := json.Marshal([]workspace.Snapshot{before, after})
	out, err := reconcile(before, after, renames)
	if err != nil {
		t.Fatal(err)
	}
	// What it edits it copies: the snapshots it was given stay as they were.
	if now, _ := json.Marshal([]workspace.Snapshot{before, after}); string(now) != string(was) {
		t.Fatalf("reconcile edited its input:\nwas %s\nnow %s", was, now)
	}
	return out
}

func grantEntities(t *testing.T, s workspace.Snapshot, kind, name string) []string {
	t.Helper()
	v, _ := object(s.Get(workspace.Ref{Kind: kind, Key: name}))
	var out []string
	for _, g := range listAt(v, "grants") {
		out = append(out, text(g, "entity"))
	}
	return out
}

var (
	roleRef  = func(n string) workspace.Ref { return workspace.Ref{Kind: ItemRole, Key: n} }
	userRef  = func(n string) workspace.Ref { return workspace.Ref{Kind: ItemUser, Key: n} }
	entityID = func(id string) workspace.Ref { return workspace.Ref{Kind: ItemEntity, Key: id} }
)

func seededItems(t *testing.T) workspace.Snapshot {
	return items(t,
		[]string{`{"name":"orders","fields":[{"name":"id"}],
			"relationships":[{"name":"customer","target":"customers","cardinality":"belongs-to",
			  "joinOn":{"customer_id":"id"}}]}`,
			`{"name":"customers","fields":[{"name":"id"}]}`,
			`{"name":"refresh","kind":"procedure","affects":["customers"]}`},
		[]string{`{"name":"analyst","grants":[{"entity":"orders","actions":["READ"]},
			{"entity":"customers","actions":["READ"]}]}`},
		[]string{`{"name":"alice","roles":["analyst"],"grants":[{"entity":"customers","actions":["READ"]}]}`})
}

func TestReconcileRemovedEntityTakesItsReferences(t *testing.T) {
	before := seededItems(t)
	out := mustReconcile(
		t,
		before,
		written(t, before, map[workspace.Ref]string{entityID("default.customers"): ""}),
		nil,
	)
	if got := grantEntities(t, out, ItemRole, "analyst"); !slices.Equal(got, []string{"orders"}) {
		t.Errorf("role grants = %v", got)
	}
	if got := grantEntities(t, out, ItemUser, "alice"); len(got) != 0 {
		t.Errorf("user grants = %v", got)
	}
	orders, _ := object(out.Get(entityID("default.orders")))
	refresh, _ := object(out.Get(entityID("default.refresh")))
	if len(listAt(orders, "relationships")) != 0 || len(listAt(refresh, "affects")) != 0 {
		t.Errorf("relationships = %v, affects = %v", orders["relationships"], refresh["affects"])
	}
}

func TestReconcileRenamedEntityMovesItsReferences(t *testing.T) {
	before := seededItems(t)
	after := written(t, before, map[workspace.Ref]string{
		entityID("default.customers"): "", entityID("default.clients"): `{"name":"clients","fields":[{"name":"id"}]}`,
	})
	out := mustReconcile(t, before, after,
		map[workspace.Ref]workspace.Ref{entityID("default.clients"): entityID("default.customers")})
	if got := grantEntities(t, out, ItemRole, "analyst"); !slices.Equal(got, []string{"orders", "clients"}) {
		t.Errorf("role grants = %v", got)
	}
	orders, _ := object(out.Get(entityID("default.orders")))
	clients, _ := object(out.Get(entityID("default.clients")))
	refresh, _ := object(out.Get(entityID("default.refresh")))
	if text(listAt(orders, "relationships")[0], "target") != "clients" || text(clients, "source") != "customers" ||
		listAt(refresh, "affects")[0] != "clients" {
		t.Errorf("orders = %v, clients = %v, refresh = %v", orders, clients, refresh)
	}
}

func TestReconcileRolesFollowRenamesAndRemovals(t *testing.T) {
	before := items(t,
		[]string{`{"name":"orders","legacyAccess":{"roles":{"read":["analyst","ops"]},
			"fieldACL":{"analyst":{"read":["id"],"write":[]}},"rowPolicies":{"ops":{"field":"id","op":"gt","value":1}}}}`},
		[]string{`{"name":"analyst","grants":[]}`, `{"name":"ops","grants":[]}`},
		[]string{`{"name":"alice","roles":["analyst","ops"],"grants":[]}`})
	after := written(t, before, map[workspace.Ref]string{
		roleRef("analyst"): "", roleRef("cn_analyst"): `{"name":"cn_analyst","grants":[]}`, roleRef("ops"): "",
	})
	out := mustReconcile(t, before, after, map[workspace.Ref]workspace.Ref{roleRef("cn_analyst"): roleRef("analyst")})
	alice, _ := object(out.Get(userRef("alice")))
	if got := alice["roles"]; !slices.Equal(got.([]any), []any{"cn_analyst"}) {
		t.Errorf("user roles = %v", got)
	}
	orders, _ := object(out.Get(entityID("default.orders")))
	got, _ := json.Marshal(orders["legacyAccess"])
	if string(got) != `{"fieldACL":{"cn_analyst":{"read":["id"],"write":[]}},"roles":{"read":["cn_analyst"]},`+
		`"rowPolicies":{}}` {
		t.Errorf("legacy access = %s", got)
	}
}

func twoDatasourceItems(t *testing.T) workspace.Snapshot {
	return items(t,
		[]string{`{"name":"tenants","datasource":"shop","schema":"crm"}`,
			`{"name":"orders","datasource":"shop","schema":"sales",
			  "relationships":[{"name":"tenant","target":"tenants","cardinality":"belongs-to","joinOn":{}}]}`},
		[]string{`{"name":"analyst","grants":[{"entity":"tenants","actions":["READ"]}]}`}, nil)
}

// An import makes an existing reference ambiguous: it is qualified, while
// the reference the import writes stays as written.
func TestReconcileQualifiesWhatAnImportMakesAmbiguous(t *testing.T) {
	before := twoDatasourceItems(t)
	after := written(t, before, map[workspace.Ref]string{
		entityID("warehouse.logistics.tenants"): `{"name":"tenants","datasource":"warehouse","schema":"logistics"}`,
		roleRef("analyst"): `{"name":"analyst","grants":[{"entity":"tenants","actions":["READ"]},
			{"entity":"logistics.tenants","actions":["READ"]}]}`,
	})
	out := mustReconcile(t, before, after, nil)
	got := grantEntities(t, out, ItemRole, "analyst")
	if !slices.Equal(got, []string{"crm.tenants", "logistics.tenants"}) {
		t.Errorf("role grants = %v", got)
	}
}

func TestReconcileKeepsReferencesOnAMovedEntity(t *testing.T) {
	before := twoDatasourceItems(t)
	moved := written(t, before, map[workspace.Ref]string{
		entityID("shop.crm.tenants"):     "",
		entityID("shop.archive.tenants"): `{"name":"tenants","datasource":"shop","schema":"archive"}`,
	})
	once := mustReconcile(t, before, moved,
		map[workspace.Ref]workspace.Ref{entityID("shop.archive.tenants"): entityID("shop.crm.tenants")})
	if got := grantEntities(t, once, ItemRole, "analyst"); !slices.Equal(got, []string{"tenants"}) {
		t.Fatalf("role grants = %v", got)
	}
	// A same-named entity added later qualifies both references.
	twice := mustReconcile(t, once, written(t, once, map[workspace.Ref]string{
		entityID("shop.crm.tenants"): `{"name":"tenants","datasource":"shop","schema":"crm"}`,
	}), nil)
	orders, _ := object(twice.Get(entityID("shop.sales.orders")))
	if got := grantEntities(t, twice, ItemRole, "analyst"); !slices.Equal(got, []string{"archive.tenants"}) ||
		text(listAt(orders, "relationships")[0], "target") != "archive.tenants" {
		t.Errorf("role grants = %v, orders = %v", got, orders)
	}
}

// An existing reference that an import's ID would capture keeps naming the
// entity it named; a new grant names what was imported.
func TestReconcileImportNextToASameNamedReference(t *testing.T) {
	before := items(t, []string{`{"name":"orders","datasource":"main","schema":"archive"}`},
		[]string{`{"name":"old","grants":[{"entity":"archive.orders","actions":["READ"]}]}`}, nil)
	out := mustReconcile(t, before, written(t, before, map[workspace.Ref]string{
		entityID("archive.orders"): `{"name":"orders","datasource":"archive"}`,
		roleRef("new"):             `{"name":"new","grants":[{"entity":"archive.orders","actions":["READ"]}]}`,
	}), nil)
	if old, fresh := grantEntities(t, out, ItemRole, "old"), grantEntities(t, out, ItemRole, "new"); !slices.Equal(
		old, []string{"main.orders"}) || !slices.Equal(fresh, []string{"archive.orders"}) {
		t.Errorf("old = %v, new = %v", old, fresh)
	}
}

func TestReconcileRemovedFieldsLeaveGrantFieldLists(t *testing.T) {
	before := items(t, []string{`{"name":"orders","fields":[{"name":"id"},{"name":"gone"}]}`},
		[]string{`{"name":"analyst","grants":[{"entity":"orders","actions":["READ"],"fieldsRestricted":true,
			"readFields":["id","gone"],"writeFields":["gone"]}]}`}, nil)
	out := mustReconcile(t, before, written(t, before, map[workspace.Ref]string{
		entityID("default.orders"): `{"name":"orders","fields":[{"name":"id"},{"name":"region","exclude":true}]}`,
	}), nil)
	analyst, _ := object(out.Get(roleRef("analyst")))
	got, _ := json.Marshal(listAt(analyst, "grants")[0])
	if string(got) != `{"actions":["READ"],"entity":"orders","fieldsRestricted":true,"readFields":["id"],`+
		`"writeFields":[]}` {
		t.Errorf("grant = %s", got)
	}
}

// Items reconcile leaves alone are the very values written, and an item
// changed keeps the keys it had (none added, such as an absent list).
func TestReconcileKeepsShapes(t *testing.T) {
	before := seededItems(t)
	after := written(t, before, map[workspace.Ref]string{entityID("default.refresh"): ""})
	out := mustReconcile(t, before, after, nil)
	if !Schema.Same(out.Get(roleRef("analyst")), before.Get(roleRef("analyst"))) {
		t.Error("an unrelated item changed")
	}
	customers, _ := object(out.Get(entityID("default.customers")))
	if _, ok := customers["primaryKey"]; ok {
		t.Errorf("a key was added: %v", customers)
	}
	if !Schema.Same(before.Get(entityID("default.refresh")), decoded(t,
		`{"name":"refresh","kind":"procedure","affects":["customers"]}`)) {
		t.Error("reconcile changed the snapshot it read")
	}
}

// Whatever entities a write renames, moves, removes or adds, an existing
// reference that named one entity names it afterwards (its new ID when
// moved), or goes with it when it was removed.
func TestReconcileKeepsWhatReferencesName(t *testing.T) {
	names := [][3]string{
		{"shop", "crm", "tenants"}, {"shop", "sales", "tenants"}, {"warehouse", "logistics", "tenants"},
		{"shop", "sales", "orders"}, {"main", "archive", "orders"}, {"archive", "", "orders"}, {"", "", "orders"},
		{"shop", "shop", "orders"}, {"ledger", "", "entries"},
	}
	for seed := range int64(3000) {
		rng := rand.New(rand.NewPCG(uint64(seed), 7))
		var present [][3]string
		for _, n := range names {
			if rng.IntN(2) == 0 {
				present = append(present, n)
			}
		}
		before := randomItems(rng, present)
		after, renames, moved := randomWrite(rng, before, names)
		out := mustReconcile(t, before, after, renames)
		checkReferences(t, seed, before, out, moved)
	}
}

func itemOf(n [3]string) (workspace.Ref, map[string]any) {
	v := map[string]any{"name": n[2]}
	if n[0] != "" {
		v["datasource"] = n[0]
	}
	if n[1] != "" {
		v["schema"] = n[1]
	}
	return workspace.Ref{Kind: ItemEntity, Key: entity.ID(n[0], n[1], n[2])}, v
}

// randomItems are the entities present and a role granting a random
// reference of each, which may be ambiguous or unknown.
func randomItems(rng *rand.Rand, present [][3]string) workspace.Snapshot {
	var out workspace.Snapshot
	var grants []any
	for _, n := range present {
		ref, v := itemOf(n)
		out = out.With(ref, v)
		forms := entity.ReferencesOf(n[0], n[1], n[2])
		grants = append(grants, map[string]any{"entity": forms[rng.IntN(len(forms))], "actions": []any{"READ"}})
	}
	return out.With(roleRef("r"), map[string]any{"name": "r", "grants": grants})
}

// randomWrite removes, moves or adds entities at random; moved maps the
// IDs moved to their new ones ("" for removed).
func randomWrite(rng *rand.Rand, before workspace.Snapshot, names [][3]string) (
	workspace.Snapshot, map[workspace.Ref]workspace.Ref, map[string]string,
) {
	after, renames, moved := before, map[workspace.Ref]workspace.Ref{}, map[string]string{}
	for _, n := range names {
		ref, _ := itemOf(n)
		present := !workspace.IsMissing(before.Get(ref))
		switch op := rng.IntN(4); {
		case present && op == 0:
			after, moved[ref.Key] = after.With(ref, workspace.Missing), ""
		case present && op == 1:
			to := names[rng.IntN(len(names))]
			toRef, v := itemOf(to)
			if !workspace.IsMissing(after.Get(toRef)) || toRef == ref {
				continue
			}
			after = after.With(ref, workspace.Missing).With(toRef, v)
			renames[toRef], moved[ref.Key] = ref, toRef.Key
		case !present && op == 2:
			added, v := itemOf(n)
			after = after.With(added, v)
		}
	}
	return after, renames, moved
}

func checkReferences(t *testing.T, seed int64, before, out workspace.Snapshot, moved map[string]string) {
	t.Helper()
	was, now := configrefs.NewIndex(entitiesOf(before)), configrefs.NewIndex(entitiesOf(out))
	kept := grantEntities(t, out, ItemRole, "r")
	i := 0
	for _, ref := range grantEntities(t, before, ItemRole, "r") {
		id, named := was.IDOf(ref)
		to, isMoved := moved[id]
		if !named {
			if i >= len(kept) || kept[i] != ref {
				t.Fatalf("seed %d: unresolved %q changed to %v", seed, ref, kept)
			}
			i++
			continue
		}
		if isMoved && to == "" {
			continue // removed with its entity
		}
		if !isMoved {
			to = id
		}
		if i >= len(kept) {
			t.Fatalf("seed %d: %q (%s) was dropped", seed, ref, id)
		}
		if got, ok := now.IDOf(kept[i]); !ok || got != to {
			t.Fatalf("seed %d: %q named %s, now %q names %q", seed, ref, to, kept[i], got)
		}
		i++
	}
	if i != len(kept) {
		t.Fatalf("seed %d: grants %v after, %d expected", seed, kept, i)
	}
}

func TestFieldReferences(t *testing.T) {
	var s Sections
	if err := json.Unmarshal([]byte(`{
		"entities": [
			{"name":"orders","primaryKey":["id"],"fields":[{"name":"id"},{"name":"customer_id"}],
			 "relationships":[{"name":"customer","target":"customers","cardinality":"belongs-to",
			   "joinOn":{"customer_id":"id"}}]},
			{"name":"customers","fields":[{"name":"id"},{"name":"region"}],"legacyAccess":{
			 "fieldACL":{"bi":{"read":["region"],"write":[]}},"rowPolicies":{"bi":{"op":"eq","field":"id","value":1}}}}],
		"roles": [{"name":"analyst","grants":[{"entity":"orders","actions":["READ"],"fieldsRestricted":true,
			"readFields":["id","customer_id"],"writeFields":[],"rows":{"op":"gt","field":"customer_id","value":0}}]}],
		"users": [], "settings": {}}`), &s); err != nil {
		t.Fatal(err)
	}
	refs := func(entity, field string) []string {
		var out []string
		for _, r := range fieldReferencesOf(s, entity) {
			if r.Field == field {
				out = append(out, r.Kind+":"+r.Name+":"+kit.Deref(r.Via))
			}
		}
		return out
	}
	// A row filter must be fixed before the field goes; a field list follows.
	if got := refs("default.orders", "customer_id"); !slices.Equal(got,
		[]string{"role:analyst:rows", "relationship:default.orders:"}) {
		t.Errorf("customer_id = %v", got)
	}
	if got := refs("default.orders", "id"); !slices.Equal(got,
		[]string{"role:analyst:fields", "primaryKey:default.orders:"}) {
		t.Errorf("id = %v", got)
	}
	if got := refs("default.customers", "id"); !slices.Equal(got,
		[]string{"relationship:default.orders:", "legacyAccess:default.customers:"}) {
		t.Errorf("customers.id = %v", got)
	}
	if got := refs("default.customers", "region"); !slices.Equal(got, []string{"legacyAccess:default.customers:"}) {
		t.Errorf("customers.region = %v", got)
	}
}

// Roles swapped in one write swap their entity-level access too, whatever
// order the renames (a map) are applied in.
func TestReconcileSwappedRolesSwapLegacyAccess(t *testing.T) {
	before := items(t,
		[]string{`{"name":"orders","legacyAccess":{"fieldACL":{"a":{"read":["id"],"write":[]},` +
			`"b":{"read":["x"],"write":[]}}}}`},
		[]string{`{"name":"a","grants":[]}`, `{"name":"b","grants":[]}`}, nil)
	after := written(t, before, map[workspace.Ref]string{
		roleRef("a"): `{"name":"a","description":"was b","grants":[]}`,
		roleRef("b"): `{"name":"b","description":"was a","grants":[]}`,
	})
	renames := map[workspace.Ref]workspace.Ref{roleRef("a"): roleRef("b"), roleRef("b"): roleRef("a")}
	for range 20 {
		orders, _ := object(mustReconcile(t, before, after, renames).Get(entityID("default.orders")))
		got, _ := json.Marshal(orders["legacyAccess"])
		if string(got) != `{"fieldACL":{"a":{"read":["x"],"write":[]},"b":{"read":["id"],"write":[]}}}` {
			t.Fatalf("legacy access = %s", got)
		}
	}
}

// The budget entries of a role renamed and of a user removed follow them: a
// budget naming a user that is gone would fail validation.
func TestReconcileBudgetsFollowRolesAndUsers(t *testing.T) {
	before := items(t, nil,
		[]string{`{"name":"analyst","grants":[]}`},
		[]string{`{"name":"alice","roles":["analyst"],"grants":[]}`, `{"name":"bob","grants":[]}`})
	before = before.With(settingsRef, decoded(t, `{"budget":{"roles":{"analyst":{"maxConcurrent":2}},
		"users":{"alice":{"maxConcurrent":1},"bob":{"maxConcurrent":3}}},"cost":{"maxRows":10}}`))
	after := written(t, before, map[workspace.Ref]string{
		roleRef("analyst"): "", roleRef("bi"): `{"name":"bi","grants":[]}`, userRef("alice"): "",
	})
	out := mustReconcile(t, before, after, map[workspace.Ref]workspace.Ref{roleRef("bi"): roleRef("analyst")})
	got, _ := json.Marshal(out.Get(settingsRef))
	if string(got) != `{"budget":{"roles":{"bi":{"maxConcurrent":2}},"users":{"bob":{"maxConcurrent":3}}},`+
		`"cost":{"maxRows":10}}` {
		t.Fatalf("settings = %s", got)
	}
}

// Budget entries renamed in a chain (a to b, b to c) or a swap all move,
// whatever order the renames come in.
func TestRenameKeysMovesAtOnce(t *testing.T) {
	for range 20 {
		section := map[string]any{"roles": map[string]any{"a": 1, "b": 2, "x": 3, "y": 4}}
		renameKeys(section, "roles", map[string]string{"a": "b", "b": "c", "x": "y", "y": "x"})
		got, _ := json.Marshal(section)
		if string(got) != `{"roles":{"b":1,"c":2,"x":4,"y":3}}` {
			t.Fatalf("roles = %s", got)
		}
	}
}
