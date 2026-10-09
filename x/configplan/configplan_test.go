package configplan

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

const baseYAML = `databases:
  shop: {driver: postgres, dsn: "postgres://u@h/shop"}
entities:
  - name: customers
    datasource: shop
    schema: public
    primaryKey: [id]
    fields: [{name: id}, {name: name}, {name: legacy}, {name: gone}]
    uniqueKeys: [[legacy]]
roles:
  analyst:
    grants:
      - entity: shop.public.customers
        actions: [read]
        fields: {read: [id, gone]}
users:
  alice: {roles: [analyst]}
`

func decode(t *testing.T, yaml string) *config.Config {
	t.Helper()
	cfg, err := configyaml.Decode([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func column(name, typ string) entity.Attribute {
	return entity.Attribute{Name: name, Domain: entity.Domain{Type: typ}}
}

func scanned(schema, name string, pk bool, columns ...entity.Attribute) entity.Entity {
	e := entity.Entity{Name: name, Source: name, Schema: schema, Attributes: columns}
	if pk {
		e.Keys = []entity.Key{{Name: name + "_pk", Columns: []string{"id"}, Primary: true}}
	}
	return e
}

func shop() []Source {
	orders := scanned("public", "orders", true, column("id", "bigint"), column("customer_id", "bigint"),
		column("phone", "text"), column("note", "text"))
	orders.ForeignKeys = []entity.ForeignKey{{Columns: []string{"customer_id"}, RefSchema: "public",
		RefRelation: "customers", RefColumns: []string{"id"}}}
	cat := introspect.Catalog{Default: "public", Tables: []entity.Entity{
		scanned("public", "customers", true, column("id", "bigint"), column("name", "text"),
			column("legacy", "text"), column("mobile_phone", "text")),
		orders,
		scanned("public", "logs", false, column("at", "timestamptz"), column("line", "text")),
		scanned("sales", "orders", true, column("id", "bigint")),
	}}
	cat.Index()
	return []Source{{Datasource: "shop", Catalog: cat, Listed: []string{"public", "sales", "archive"},
		Scanned: []string{"public", "sales"}}}
}

func evaluate(t *testing.T, base *config.Config, p Plan) Proposal {
	t.Helper()
	out, err := Evaluate(Input{Base: base, Published: base, Sources: shop()}, p)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func kinds(xs []Exception) []string {
	var out []string
	for _, x := range xs {
		out = append(out, x.Kind+" "+x.Entity+" "+x.Detail)
	}
	return out
}

func entityOf(t *testing.T, p Proposal, id string) config.EntityConfig {
	t.Helper()
	for _, e := range p.Config.Entities {
		if e.ID() == id {
			return e
		}
	}
	t.Fatalf("no entity %s", id)
	return config.EntityConfig{}
}

// valid runs the proposal through the configuration chain, as the admin API
// does before it shows or saves one.
func valid(t *testing.T, base *config.Config, p Proposal) *config.Config {
	t.Helper()
	payload, err := revisionops.Normalize(base)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := configedit.Apply(payload, p.Edit())
	if err != nil {
		t.Fatalf("the proposal does not validate: %v", err)
	}
	return cfg
}

func TestImportGrantAndMask(t *testing.T) {
	t.Parallel()
	base := decode(t, baseYAML)
	p := evaluate(t, base, Plan{
		Scope: Scope{Schemas: []string{"pub*"}}, Import: true,
		Grants: []GrantRule{{Principal: "role:analyst", Actions: []string{"READ", "update"}}},
		Fields: []FieldRule{{Columns: []string{"*phone*"}, Mask: "phone"}},
	})
	if !slices.Equal(p.Added, []string{"shop.public.logs", "shop.public.orders"}) {
		t.Fatalf("added = %v", p.Added)
	}
	orders := entityOf(t, p, "shop.public.orders")
	if len(orders.Relationships) != 1 || orders.Relationships[0].Target != "shop.public.customers" ||
		orders.Fields[2].Mask != "phone" {
		t.Fatalf("orders = %+v", orders)
	}
	got := kinds(p.Report.Exceptions)
	if !slices.Contains(got, "noKey shop.public.logs ") || !slices.Contains(got, "noKey shop.public.logs update") {
		t.Fatalf("exceptions = %v", got)
	}
	grants := p.Config.Roles["analyst"].Grants
	// The field-restricted grant stays as it was: the rule grants every field.
	if len(grants) != 4 || grants[0].Fields == nil || !slices.Equal(grants[1].Actions, []string{"read", "update"}) ||
		!slices.Equal(grants[2].Actions, []string{"read"}) || grants[3].Entity != "shop.public.orders" {
		t.Fatalf("grants = %+v", grants)
	}
	if p.Report.Matched != 3 || len(p.Report.Objects) != 1 || p.Report.Objects[0].Imported != 2 ||
		p.Report.Objects[0].Changed != 0 || len(p.Report.Unscanned) != 0 {
		t.Fatalf("report = %+v", p.Report)
	}
	valid(t, base, p)
}

// A configured table in scope gets the rules too, and only grants it lacks.
func TestRulesApplyToConfiguredTables(t *testing.T) {
	t.Parallel()
	base := decode(t, baseYAML)
	p := evaluate(t, base, Plan{
		Scope:  Scope{Tables: []string{"customers"}},
		Grants: []GrantRule{{Principal: "role:support", Actions: []string{"read"}}},
		Fields: []FieldRule{{Columns: []string{"name"}, Exclude: true}},
	})
	if len(p.Added) != 0 || len(p.Entities) != 1 || !p.Entities[0].Fields[1].Exclude ||
		!slices.Equal(p.Report.Unscanned["shop"], []string{"archive"}) {
		t.Fatalf("entities = %+v", p.Entities)
	}
	if _, ok := p.Roles["support"]; !ok || p.Report.Changes[1].Kind != ChangeCreateRole {
		t.Fatalf("roles = %+v, changes = %+v", p.Roles, p.Report.Changes)
	}
	again := evaluate(t, valid(t, base, p), Plan{
		Scope: Scope{
			Tables: []string{"customers"},
		}, Grants: []GrantRule{{Principal: "role:support", Actions: []string{"read"}}},
	})
	if !again.Empty() {
		t.Fatalf("granting again changes %+v", again)
	}
}

func TestSyncFollowsReferences(t *testing.T) {
	t.Parallel()
	base := decode(t, baseYAML)
	p := evaluate(t, base, Plan{Scope: Scope{Drift: true}, Sync: &SyncRule{Add: true, Remove: true, HideNew: true}})
	customers := entityOf(t, p, "shop.public.customers")
	var names []string
	for _, f := range customers.Fields {
		names = append(names, f.Name)
	}
	if !slices.Equal(names, []string{"id", "name", "legacy", "mobile_phone"}) || !customers.Fields[3].Exclude {
		t.Fatalf("fields = %+v", customers.Fields)
	}
	if got := kinds(p.Report.Exceptions); len(got) != 0 {
		t.Fatalf("exceptions = %v", got)
	}
	if fields := p.Config.Roles["analyst"].Grants[0].Fields; !slices.Equal(fields.Read, []string{"id"}) {
		t.Fatalf("the grant keeps the removed field: %+v", fields)
	}
	valid(t, base, p)

	referenced := decode(t, strings.Replace(baseYAML, "uniqueKeys: [[legacy]]", "uniqueKeys: [[gone]]", 1))
	p = evaluate(t, referenced, Plan{Scope: Scope{Drift: true}, Sync: &SyncRule{Remove: true}})
	if got := kinds(p.Report.Exceptions); !slices.Equal(
		got,
		[]string{"referenced shop.public.customers gone: uniqueKeys"},
	) {
		t.Fatalf("exceptions = %v", got)
	}
}

func TestNamesExclusionsAndAmbiguity(t *testing.T) {
	t.Parallel()
	base := decode(t, strings.Replace(baseYAML, "roles:", `  - name: orders
    datasource: shop
    schema: sales
    primaryKey: [id]
    fields: [{name: id}]
roles:
  ops:
    grants: [{entity: orders, actions: [read]}]`, 1))
	p := evaluate(t, base, Plan{
		Scope: Scope{Status: []Status{StatusNew}}, Import: true, Exclude: []string{"shop.public.logs"},
	})
	if !slices.Equal(p.Added, []string{"shop.public.orders"}) || p.Report.Excluded != 1 {
		t.Fatalf("added = %v, report = %+v", p.Added, p.Report)
	}
	if got := kinds(p.Report.Exceptions); !slices.Equal(
		got,
		[]string{"qualified role:ops orders → shop.sales.orders"},
	) {
		t.Fatalf("exceptions = %v", got)
	}
	if g := p.Config.Roles["ops"].Grants[0]; g.Entity != "shop.sales.orders" {
		t.Fatalf("the ambiguous reference is kept: %+v", g)
	}
	valid(t, base, p)
	p = evaluate(t, base, Plan{
		Scope: Scope{Refs: []string{"shop.public.orders"}}, Import: true,
		Names: map[string]string{"shop.public.orders": "customers"},
	})
	if p.Added[0] != "shop.public.customers_2" ||
		kinds(p.Report.Exceptions)[0] != "renamed shop.public.customers_2 customers_2" {
		t.Fatalf("added = %v, exceptions = %v", p.Added, kinds(p.Report.Exceptions))
	}
	if p.Report.Unscanned != nil {
		t.Fatalf("explicit tables report unscanned schemas: %v", p.Report.Unscanned)
	}
}

func TestGrantExceptions(t *testing.T) {
	t.Parallel()
	base := decode(t, baseYAML)
	in := Input{Base: base, Published: base, Sources: shop(), Review: true,
		Denied: func(_, action string) (string, bool) { return "no privilege", action == "delete" }}
	p, err := Evaluate(in, Plan{
		Scope: Scope{Tables: []string{"customers", "orders"}, Schemas: []string{"public"}}, Import: true,
		Grants: []GrantRule{
			{Principal: "user:ghost", Actions: []string{"read"}},
			{Principal: "user:alice", Actions: []string{"read", "delete"}, Fields: []string{"no*"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := kinds(p.Report.Exceptions)
	for _, want := range []string{
		"unknownUser  ghost", "noFields shop.public.customers user:alice",
		"review shop.public.orders user:alice",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "denied shop.public.orders") }) {
		t.Errorf("a new entity has no capability to check: %v", got)
	}
	g := p.Config.Users["alice"].Grants
	if len(g) != 1 || !slices.Equal(g[0].Fields.Read, []string{"note"}) || g[0].Fields.Write != nil {
		t.Fatalf("alice = %+v", g)
	}
}

func TestDecode(t *testing.T) {
	t.Parallel()
	if _, err := Decode([]byte(`{"scope":{},"import":true}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"scope":{},"import":true,"extra":1}`,
		`{"scope":{"tables":["["]},"import":true}`,
		`{"scope":{}}`,
		`{"scope":{"status":["gone"]},"import":true}`,
		`{"scope":{},"grants":[{"principal":"analyst","actions":["read"]}]}`,
		`{"scope":{},"grants":[{"principal":"role:a","actions":["execute"]}]}`,
		`{"scope":{},"fields":[{"columns":["x"]}]}`,
	} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestExceptionLevelsAndStaleScans(t *testing.T) {
	t.Parallel()
	base := decode(t, baseYAML)
	sources := shop()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sources[0].ScannedAt = map[string]time.Time{"public": at, "sales": at.Add(time.Hour)}
	in := Input{
		Base:       base,
		Published:  base,
		Sources:    sources,
		Now:        at.Add(30 * 24 * time.Hour),
		StaleAfter: 7 * 24 * time.Hour,
	}
	p, err := Evaluate(in, Plan{Scope: Scope{Drift: true}, Sync: &SyncRule{Remove: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Report.Exceptions) != 1 || p.Report.Exceptions[0].Kind != ExceptionStaleScan ||
		p.Report.Exceptions[0].Level != LevelConfirm || !p.Report.ScannedFrom.Equal(at) {
		t.Fatalf("report = %+v", p.Report)
	}
	in.Now = at.Add(time.Hour)
	if p, _ = Evaluate(in, Plan{Scope: Scope{Drift: true}, Sync: &SyncRule{Remove: true}}); len(
		p.Report.Exceptions,
	) != 0 {
		t.Fatalf("a fresh scan needs no confirmation: %+v", p.Report.Exceptions)
	}
	for kind, want := range map[[2]string]string{
		{ExceptionQualified, "x"}: LevelInfo, {ExceptionNoKey, ""}: LevelInfo, {ExceptionNoKey, "update"}: LevelConfirm,
		{ExceptionRenamed, "x"}: LevelInfo, {ExceptionReview, "role:a"}: LevelConfirm,
	} {
		if got := levelOf(kind[0], kind[1]); got != want {
			t.Errorf("%v: %s, want %s", kind, got, want)
		}
	}
}
