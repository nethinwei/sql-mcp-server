package configplan

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/reason"
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
	payload, err := revisionops.Normalize(config.SpecOf(base))
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

	// A row filter names its field by value ({op, field, value}), within
	// and/or too; a key such as "op" is no field.
	filtered := decode(t, strings.Replace(baseYAML, "fields: {read: [id, gone]}",
		"fields: {read: [id]}\n        rows: {and: [{op: eq, field: gone, value: 1}, {op: is_null, field: id}]}", 1))
	p = evaluate(t, filtered, Plan{Scope: Scope{Drift: true}, Sync: &SyncRule{Remove: true}})
	if got := kinds(p.Report.Exceptions); !slices.Equal(
		got,
		[]string{"referenced shop.public.customers gone: rows of role:analyst"},
	) {
		t.Fatalf("exceptions = %v", got)
	}
}

// A grant whose every listed field a sync removes reads none of its entity:
// the report asks to confirm it, naming the principal.
func TestSyncEmptyingAGrantsFieldsIsReported(t *testing.T) {
	t.Parallel()
	base := decode(t, strings.Replace(baseYAML, "fields: {read: [id, gone]}", "fields: {read: [gone]}", 1))
	p := evaluate(t, base, Plan{Scope: Scope{Drift: true}, Sync: &SyncRule{Remove: true}})
	want := []string{"grantFieldsEmptied shop.public.customers role:analyst"}
	if got := kinds(p.Report.Exceptions); !slices.Equal(got, want) {
		t.Fatalf("exceptions = %v", got)
	}
	if x := p.Report.Exceptions[0]; x.Level != LevelConfirm || x.Ref != "shop.public.customers" {
		t.Fatalf("exception = %+v", x)
	}
	if fields := p.Config.Roles["analyst"].Grants[0].Fields; len(fields.Read) != 0 || len(fields.Write) != 0 {
		t.Fatalf("fields = %+v", fields)
	}
	valid(t, base, p)
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
		[]string{"qualified role:ops orders → sales.orders"},
	) {
		t.Fatalf("exceptions = %v", got)
	}
	if g := p.Config.Roles["ops"].Grants[0]; g.Entity != "sales.orders" {
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
		Denied: func(_, action string) (reason.Reason, bool) {
			return reason.New(reason.PrivilegeMissing, "privilege", "DELETE"), action == "delete"
		}}
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

// An action the connection cannot run is explained by the server's reason,
// which the console translates; its detail is the reason's English text.
func TestDeniedGrantCarriesTheReason(t *testing.T) {
	t.Parallel()
	base := decode(t, baseYAML)
	in := Input{Base: base, Published: base, Sources: shop(),
		Denied: func(_, action string) (reason.Reason, bool) {
			return reason.New(reason.PrivilegeMissing, "privilege", "DELETE"), action == "delete"
		}}
	p, err := Evaluate(in, Plan{
		Scope:  Scope{Tables: []string{"customers"}, Schemas: []string{"public"}},
		Grants: []GrantRule{{Principal: "user:alice", Actions: []string{"read", "delete"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(p.Report.Exceptions, func(x Exception) bool { return x.Kind == ExceptionDenied })
	if i < 0 {
		t.Fatalf("no denied exception in %v", kinds(p.Report.Exceptions))
	}
	if x := p.Report.Exceptions[i]; x.Detail != "delete: the account lacks the DELETE privilege" ||
		x.Reason.Code != reason.PlanActionDenied || x.Reason.Parts["cause"].Code != reason.PrivilegeMissing {
		t.Errorf("denied exception = %+v", x)
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

// Exceptions are graded by what a reviewer has to do about them.
func TestExceptionLevels(t *testing.T) {
	t.Parallel()
	for kind, want := range map[[2]string]string{
		{ExceptionQualified, "x"}: LevelInfo, {ExceptionNoKey, ""}: LevelInfo, {ExceptionNoKey, "update"}: LevelConfirm,
		{ExceptionRenamed, "x"}: LevelInfo, {ExceptionReview, "role:a"}: LevelConfirm,
	} {
		if got := levelOf(kind[0], kind[1]); got != want {
			t.Errorf("%v: %s, want %s", kind, got, want)
		}
	}
}

// A plan applied again changes nothing, though the plan comes as JSON (its
// row filter holds 7.0) and what it was applied to as YAML (7).
func TestGrantWithRowsAppliesOnce(t *testing.T) {
	t.Parallel()
	base := decode(t, baseYAML)
	plan, err := Decode([]byte(`{"scope": {"tables": ["customers"]}, "grants": [{"principal": "role:support",
		"actions": ["read"], "fields": ["id"], "rows": {"field": "id", "op": "eq", "value": 7}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	p := evaluate(t, base, plan)
	if grants := p.Roles["support"].Grants; len(grants) != 1 {
		t.Fatalf("grants = %+v", grants)
	}
	// Applied, the configuration is stored as YAML.
	stored, err := configyaml.Encode(valid(t, base, p))
	if err != nil {
		t.Fatal(err)
	}
	if again := evaluate(t, decode(t, string(stored)), plan); !again.Empty() {
		t.Fatalf("granting again changes %+v", again.Roles["support"].Grants)
	}
}

// A reference the tables imported make ambiguous names what it named before
// anything resolves it: the fields a sync removes leave the grants naming
// the entity by it.
func TestImportQualifiesBeforeSync(t *testing.T) {
	t.Parallel()
	base := decode(t, `databases:
  shop: {driver: postgres, dsn: "postgres://u@h/shop"}
entities:
  - name: orders
    datasource: shop
    schema: public
    primaryKey: [id]
    fields: [{name: id}, {name: customer_id}, {name: phone}, {name: note}, {name: gone}]
roles:
  analyst:
    grants:
      - entity: orders
        actions: [read]
        fields: {read: [id, gone]}
`)
	p := evaluate(t, base, Plan{Scope: Scope{Tables: []string{"orders"}}, Import: true,
		Sync: &SyncRule{Remove: true}})
	if !slices.Equal(p.Added, []string{"shop.sales.orders"}) {
		t.Fatalf("added = %v", p.Added)
	}
	g := p.Config.Roles["analyst"].Grants
	if len(g) != 1 || g[0].Entity == "orders" || !slices.Equal(g[0].Fields.Read, []string{"id"}) {
		t.Fatalf("grants = %+v %+v", g, g[0].Fields)
	}
	valid(t, base, p)
}

// Excluding a table no scan found is refused: a misspelt exclusion would
// apply the plan (its grants) to the table meant.
func TestExcludingAnUnknownTableIsRefused(t *testing.T) {
	t.Parallel()
	base := decode(t, baseYAML)
	plan := Plan{Scope: Scope{Schemas: []string{"public"}}, Import: true,
		Grants: []GrantRule{{Principal: "role:analyst", Actions: []string{"read"}}}}
	for _, ref := range []string{"logs", "shop.public.log"} {
		plan.Exclude = []string{ref}
		if _, err := Evaluate(Input{Base: base, Published: base, Sources: shop()}, plan); err == nil ||
			!strings.Contains(err.Error(), ref) {
			t.Errorf("exclude %q: %v", ref, err)
		}
	}
	plan.Exclude = []string{"shop.public.logs"}
	if p := evaluate(t, base, plan); slices.Contains(p.Added, "shop.public.logs") {
		t.Fatalf("an excluded table was imported: %v", p.Added)
	}
}

// A foreign key on a column the configured entity at either end has no field
// for becomes no relationship (it would not validate), and is reported.
func TestImportDropsForeignKeysOnColumnsThatAreNotFields(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ configured, imported, want string }{
		{"{name: customers, datasource: shop, schema: public, fields: [{name: name}]}",
			"shop.public.orders", "shop.public.customers"},
		{"{name: orders, datasource: shop, schema: public, fields: [{name: id}]}",
			"shop.public.customers", "shop.public.orders"},
	} {
		base := decode(t, "databases:\n  shop: {driver: postgres, dsn: \"postgres://u@h/shop\"}\nentities:\n  - "+
			c.configured+"\n")
		p := evaluate(t, base, Plan{Scope: Scope{Refs: []string{c.imported}}, Import: true})
		if r := entityOf(t, p, c.imported).Relationships; len(r) != 0 {
			t.Fatalf("%s relationships = %+v", c.imported, r)
		}
		if !slices.Contains(kinds(p.Report.Exceptions), "relationshipDropped "+c.imported+" "+c.want) {
			t.Fatalf("exceptions = %v", kinds(p.Report.Exceptions))
		}
		valid(t, base, p)
	}
}

// A field rule filtering by type tests each field's column type, read once
// a table; a field without a column of the table has none.
func TestFieldRulesFilterByColumnType(t *testing.T) {
	t.Parallel()
	base := decode(t, baseYAML)
	p := evaluate(t, base, Plan{Scope: Scope{Refs: []string{"shop.public.customers"}},
		Fields: []FieldRule{{Columns: []string{"*"}, Types: []string{"text"}, Exclude: true},
			{Columns: []string{"*"}, Types: []string{"big*"}, Mask: "secret"}}})
	var got []string
	for _, f := range entityOf(t, p, "shop.public.customers").Fields {
		got = append(got, f.Name+":"+strconv.FormatBool(f.Exclude)+":"+f.Mask)
	}
	if want := []string{"id:false:secret", "name:true:", "legacy:true:", "gone:false:"}; !slices.Equal(got, want) {
		t.Fatalf("fields = %v, want %v", got, want)
	}
	valid(t, base, p)
}

// A grant with a field wildcard follows the fields: once a column is added,
// running the template again widens the grant it made instead of adding a
// second, and running it once more changes nothing.
func TestWildcardGrantFollowsAddedFields(t *testing.T) {
	t.Parallel()
	plan, err := Decode([]byte(`{"scope": {"tables": ["customers"]},
		"grants": [{"principal": "role:support", "actions": ["read", "update"], "fields": ["*"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	run := func(base *config.Config) (*config.Config, Proposal) {
		p := evaluate(t, base, plan)
		return valid(t, base, p), p
	}
	cfg, _ := run(decode(t, baseYAML))
	for i := range cfg.Entities {
		if cfg.Entities[i].ID() == "shop.public.customers" {
			cfg.Entities[i].Fields = append(cfg.Entities[i].Fields, config.FieldConfig{Name: "mobile_phone"})
		}
	}
	cfg, p := run(cfg)
	grants := cfg.Roles["support"].Grants
	if p.Empty() || len(grants) != 1 || !slices.Contains(grants[0].Fields.Read, "mobile_phone") ||
		!slices.Equal(grants[0].Fields.Write, grants[0].Fields.Read) {
		t.Fatalf("grants = %+v", grants)
	}
	if _, again := run(cfg); !again.Empty() {
		t.Fatalf("running again changes %+v", again.Roles["support"].Grants)
	}
}
