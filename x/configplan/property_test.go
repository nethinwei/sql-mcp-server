package configplan

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// Random catalogs, configurations and plans: every proposal validates, a
// plan applied twice changes nothing the second time, and a grant a rule
// should make is either made or named by an exception.

var (
	tableNames  = []string{"orders", "users", "logs", "items"}
	columnNames = []string{"id", "name", "phone", "email", "note"}
)

func randomCatalog(r *rand.Rand) introspect.Catalog {
	var cat introspect.Catalog
	cat.Default = "public"
	for _, schema := range []string{"public", "sales"} {
		for _, name := range tableNames {
			if r.IntN(3) == 0 {
				continue
			}
			cols := []entity.Attribute{column("id", "bigint")}
			for _, c := range columnNames[1:] {
				if r.IntN(2) == 0 {
					cols = append(cols, column(c, "text"))
				}
			}
			t := scanned(schema, name, r.IntN(4) != 0, cols...)
			if r.IntN(3) == 0 {
				t.ForeignKeys = []entity.ForeignKey{{Columns: []string{"id"}, RefSchema: "public",
					RefRelation: tableNames[r.IntN(len(tableNames))], RefColumns: []string{"id"}}}
			}
			cat.Tables = append(cat.Tables, t)
		}
	}
	cat.Index()
	return cat
}

// randomBase configures some of the tables, with fields that may differ from
// the columns, and grants them to a role.
func randomBase(t *testing.T, r *rand.Rand, cat introspect.Catalog) *config.Config {
	var b strings.Builder
	b.WriteString("databases:\n  shop: {driver: postgres, dsn: x}\nentities:\n")
	var grants []string
	for _, tb := range cat.Tables {
		if r.IntN(3) != 0 {
			continue
		}
		fmt.Fprintf(&b, "  - {name: %s, datasource: shop, schema: %s, fields: [", tb.Source, tb.Schema)
		for _, c := range columnNames {
			if r.IntN(2) == 0 || c == "id" {
				fmt.Fprintf(&b, "{name: %s}, ", c)
			}
		}
		b.WriteString("]")
		if len(tb.PrimaryKey()) > 0 && r.IntN(5) != 0 {
			b.WriteString(", primaryKey: [id]")
		}
		if r.IntN(5) == 0 {
			b.WriteString(", uniqueKeys: [[id]]")
		}
		if r.IntN(6) == 0 {
			b.WriteString(", kind: view")
		}
		b.WriteString("}\n")
		if r.IntN(2) == 0 {
			grants = append(grants, fmt.Sprintf("{entity: shop.%s.%s, actions: [read]}", tb.Schema, tb.Source))
		}
	}
	if len(grants) > 0 {
		fmt.Fprintf(&b, "roles:\n  analyst:\n    grants: [%s]\n", strings.Join(grants, ", "))
	}
	b.WriteString("users:\n  alice: {}\n")
	return decode(t, b.String())
}

func randomPlan(r *rand.Rand) Plan {
	p := Plan{Import: r.IntN(2) == 0}
	if r.IntN(2) == 0 {
		p.Scope.Schemas = []string{[]string{"public", "s*", "*"}[r.IntN(3)]}
	}
	if r.IntN(2) == 0 {
		p.Scope.Tables = []string{tableNames[r.IntN(len(tableNames))], "*s"}
	}
	if r.IntN(3) == 0 {
		p.Scope.Status = []Status{[]Status{StatusNew, StatusWorkspace, StatusPublished}[r.IntN(3)]}
	}
	for range r.IntN(3) {
		g := GrantRule{Principal: []string{"role:analyst", "role:fresh", "user:alice"}[r.IntN(3)],
			Actions: []string{[]string{"read", "update", "delete", "create"}[r.IntN(4)]}}
		if r.IntN(3) == 0 {
			g.Fields = []string{"n*"}
		}
		p.Grants = append(p.Grants, g)
	}
	if r.IntN(2) == 0 {
		p.Fields = []FieldRule{{Columns: []string{"*phone*", "email"}, Exclude: r.IntN(2) == 0, Mask: "phone"}}
	}
	if r.IntN(2) == 0 {
		p.Sync = &SyncRule{Add: r.IntN(2) == 0, Remove: r.IntN(2) == 0, HideNew: r.IntN(2) == 0,
			Keys: r.IntN(2) == 0}
	}
	if r.IntN(4) == 0 {
		p.Exclude = []string{"shop.public.orders"}
	}
	if r.IntN(5) == 0 {
		p.Scope.Entities = []string{"shop.public.orders", "shop.sales.users"}
	}
	return p
}

func TestPlansHoldTheirProperties(t *testing.T) {
	t.Parallel()
	for seed := range uint64(600) {
		r := rand.New(rand.NewPCG(seed, 7))
		cat := randomCatalog(r)
		base := randomBase(t, r, cat)
		plan := randomPlan(r)
		if plan.Check() != nil {
			continue
		}
		sources := []Source{{Datasource: "shop", Catalog: cat}}
		if _, ok := cat.Lookup("public", "orders"); !ok {
			plan.Exclude = nil // excluding a table no scan found is refused
		}
		p, err := Evaluate(Input{Base: base, Sources: sources}, plan)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		next := valid(t, base, p)
		checkGrantsMade(t, seed, plan, base, sources, p)
		again, err := Evaluate(Input{Base: next, Sources: sources}, plan)
		if err != nil {
			t.Fatalf("seed %d again: %v", seed, err)
		}
		if !again.Empty() {
			t.Fatalf("seed %d: the plan changes its own result: %+v\nplan %+v", seed, again.Report.Changes, plan)
		}
	}
}

// checkGrantsMade: every entity the plan applies to holds each rule's grant,
// or an exception names the entity or the rule's principal.
func checkGrantsMade(t *testing.T, seed uint64, plan Plan, base *config.Config, sources []Source, p Proposal) {
	t.Helper()
	refs := config.NewEntityRefs(p.Config.Entities)
	excepted := func(id, principal string) bool {
		return slices.ContainsFunc(p.Report.Exceptions, func(x Exception) bool {
			return x.Entity == id || x.Detail == strings.TrimPrefix(principal, "user:")
		})
	}
	for _, rule := range plan.Grants {
		kind, name, _ := strings.Cut(rule.Principal, ":")
		grants := p.Config.Users[name].Grants
		if kind == "role" {
			grants = p.Config.Roles[name].Grants
		}
		for _, id := range targetsOf(plan, base, sources, p) {
			covered := slices.ContainsFunc(grants, func(g config.GrantConfig) bool {
				e, err := refs.Resolve(g.Entity)
				return err == nil && e.ID() == id && slices.Contains(g.Actions, rule.Actions[0])
			})
			if !covered && !excepted(id, rule.Principal) {
				t.Fatalf("seed %d: %s lacks %v on %s without an exception: %+v", seed, rule.Principal,
					rule.Actions, id, p.Report.Exceptions)
			}
		}
	}
}

// targetsOf lists the entities the plan applies to: those of the tables in
// scope that are configured, or imported by the plan.
func targetsOf(plan Plan, base *config.Config, sources []Source, p Proposal) []string {
	var out []string
	inResult := ConfiguredTables("shop", sources[0].Catalog, p.Config)
	for _, row := range Rows(sources, base, nil, plan.Scope) {
		if slices.Contains(plan.Exclude, row.Ref) {
			continue
		}
		if row.Entity != nil {
			out = append(out, row.Entity.ID())
		} else if e, ok := inResult[TableKey(row.Table.Schema, row.Table.Source)]; ok {
			out = append(out, e.ID())
		}
	}
	return out
}

// A candidate is the same whether every table's candidate is built or only
// some: names and relationships span the catalog either way.
func TestCandidatesForSomeTablesMatchAll(t *testing.T) {
	t.Parallel()
	for seed := range uint64(300) {
		r := rand.New(rand.NewPCG(seed, 11))
		cat := randomCatalog(r)
		base := randomBase(t, r, cat)
		all := map[string]Candidate{}
		for _, c := range Candidates("shop", cat, base, nil, nil) {
			all[TableKey(c.Table.Schema, c.Table.Source)] = c
		}
		only := map[string]bool{}
		for _, tb := range cat.Tables {
			if r.IntN(2) == 0 {
				only[TableKey(tb.Schema, tb.Source)] = true
			}
		}
		some := Candidates("shop", cat, base, nil, only)
		if len(some) != len(only) {
			t.Fatalf("seed %d: %d candidates for %d tables", seed, len(some), len(only))
		}
		for _, c := range some {
			want := all[TableKey(c.Table.Schema, c.Table.Source)]
			if !reflect.DeepEqual(c.Entity, want.Entity) || c.ID != want.ID {
				t.Fatalf("seed %d %s:\n some %+v\n  all %+v", seed, c.ID, c.Entity, want.Entity)
			}
		}
	}
}

// An index filters by any scope to what Rows computes for that scope.
func TestIndexMatchesRows(t *testing.T) {
	t.Parallel()
	for seed := range uint64(300) {
		r := rand.New(rand.NewPCG(seed, 11))
		cat := randomCatalog(r)
		base := randomBase(t, r, cat)
		sources := []Source{{Datasource: "shop", Catalog: cat}, {Datasource: "other", Catalog: randomCatalog(r)}}
		index := NewIndex(sources, base, nil)
		scope := randomPlan(r).Scope
		scope.Search = []string{"", "or", "S"}[r.IntN(3)]
		scope.Drift = r.IntN(3) == 0
		if r.IntN(3) == 0 {
			scope.Datasources = []string{"other"}
		}
		if r.IntN(4) == 0 {
			scope.Refs = []string{"shop.public.orders", "other.sales.users"}
		}
		if got, want := index.Rows(scope), Rows(sources, base, nil, scope); !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d, scope %+v: index %d rows, Rows %d", seed, scope, len(got), len(want))
		}
	}
}
