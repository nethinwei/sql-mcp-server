package tool

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/store/storetest"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

// ordersEntity has a nullable and a masked field, readable by "reader"; a
// grant role "limited" reads id-less fields only.
func ordersEntity() entity.Entity {
	return entity.Entity{
		Name: "orders", Source: "orders",
		Attributes: []entity.Attribute{
			{Name: "id"}, {Name: "amount"}, {Name: "region", Domain: entity.Domain{Nullable: true}},
			{Name: "created_at", Alias: "createdAt"}, {Name: "phone", Mask: "phone"},
		},
		Keys: []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role: entity.RoleAccess{entity.ActionRead: {"reader"}, entity.ActionAggregate: {"reader"}},
		MCP:  entity.MCPFlags{DMLTools: true},
	}
}

// orderContext serves ordersEntity from a database answering rows.
func orderContext(t *testing.T, rows ...[]any) (Context, *storetest.FakeDB) {
	t.Helper()
	reg, _ := entity.NewRegistry([]entity.Entity{ordersEntity()})
	db := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return storetest.NewFakeRows([]string{"id", "amount", "created_at"}, rows...), nil
	}}
	return Context{Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}, Role: "reader",
		DB: db, Dialect: testdialect.Postgres{}}, db
}

func readJSON(t *testing.T, tc Context, input string) (Result, error) {
	t.Helper()
	return ReadTool{}.Run(context.Background(), json.RawMessage(input), tc)
}

// An ordered read sorts by the fields asked (an alias by its column), ends
// in the key so the order is total, and carries nextCursor while it fills
// its limit; the cursor resumes after the last row in the order's
// directions, and the fields added to build it are not returned.
func TestReadOrderByPagesWithNextCursor(t *testing.T) {
	t.Parallel()
	tc, db := orderContext(t, []any{int64(7), int64(500), "2026-10-01"}, []any{int64(9), int64(500), "2026-09-30"})
	res, err := readJSON(t, tc, `{"entity":"orders","fields":["amount"],"limit":2,
		"orderBy":[{"field":"amount","dir":"desc"},{"field":"createdAt"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	sql := db.Queries[0].Query
	if !strings.Contains(sql, `ORDER BY "amount" DESC, "created_at" ASC, "id" ASC`) ||
		!strings.Contains(sql, `SELECT "amount", "created_at", "id"`) {
		t.Fatalf("sql = %s", sql)
	}
	want := map[string]any{"amount": int64(500), "created_at": "2026-09-30", "id": int64(9)}
	if !reflect.DeepEqual(res.NextCursor, want) {
		t.Fatalf("nextCursor = %v", res.NextCursor)
	}
	if _, ok := res.Content[0]["id"]; ok {
		t.Fatalf("an added field was returned: %v", res.Content[0])
	}
	cursor, _ := json.Marshal(res.NextCursor)
	if _, err := readJSON(t, tc, `{"entity":"orders","fields":["amount"],"limit":2,
		"orderBy":[{"field":"amount","dir":"desc"},{"field":"createdAt"}],"cursor":`+string(cursor)+`}`); err != nil {
		t.Fatal(err)
	}
	sql = db.Queries[1].Query
	if !strings.Contains(sql, `"amount" < $1`) || !strings.Contains(sql, `"amount" = $2 AND "created_at" > $3`) ||
		!strings.Contains(sql, `"created_at" = $5 AND "id" > $6`) {
		t.Fatalf("resumed sql = %s", sql)
	}
	short, err := readJSON(t, tc, `{"entity":"orders","fields":["amount"],"limit":5,"orderBy":[{"field":"amount"}]}`)
	if err != nil || short.NextCursor != nil {
		t.Fatalf("a page short of its limit has no next: %v %v", short.NextCursor, err)
	}
}

// A nullable field sorts its NULLs last, and pages by them: after a value
// come the greater ones and NULL, after NULL the rows of NULL with a greater
// key. A masked field, an unknown direction and a field named twice are
// refused.
func TestReadOrderByRules(t *testing.T) {
	t.Parallel()
	tc, db := orderContext(t, []any{int64(1), int64(2), "x"})
	if _, err := readJSON(t, tc, `{"entity":"orders","limit":1,"orderBy":[{"field":"region"}]}`); err != nil {
		t.Fatal(err)
	}
	if sql := db.Queries[0].Query; !strings.Contains(sql,
		`ORDER BY CASE WHEN "region" IS NULL THEN 1 ELSE 0 END, "region" ASC, "id" ASC`) {
		t.Fatalf("sql = %s", sql)
	}
	for cursor, want := range map[string]string{
		`{"region":"CN","id":7}`: `WHERE (("region" > $1 OR "region" IS NULL) OR ("region" = $2 AND "id" > $3))`,
		`{"region":null,"id":7}`: `WHERE ("region" IS NULL AND "id" > $1)`,
	} {
		if _, err := readJSON(t, tc, `{"entity":"orders","limit":1,"orderBy":[{"field":"region"}],"cursor":`+
			cursor+`}`); err != nil {
			t.Fatal(cursor, err)
		}
		if sql := db.Queries[len(db.Queries)-1].Query; !strings.Contains(sql, want) {
			t.Errorf("%s: sql = %s", cursor, sql)
		}
	}
	for _, input := range []string{
		`{"entity":"orders","orderBy":[{"field":"phone"}]}`,
		`{"entity":"orders","orderBy":[{"field":"amount","dir":"up"}]}`,
		`{"entity":"orders","orderBy":[{"field":"amount"},{"field":"amount","dir":"desc"}]}`,
		`{"entity":"orders","orderBy":[{"field":"region"}],"cursor":{"region":"a","id":null}}`,
		`{"entity":"orders","orderBy":[{"field":"amount"}],"cursor":{"amount":1}}`,
		`{"entity":"orders","distinct":true}`,
		`{"entity":"orders","fields":["amount"],"distinct":true,"orderBy":[{"field":"region"}]}`,
	} {
		if _, err := readJSON(t, tc, input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v", input, err)
		}
	}
}

// A distinct read selects distinct rows of its fields, ordered by them
// without a key, and has no next cursor.
func TestReadDistinct(t *testing.T) {
	t.Parallel()
	tc, db := orderContext(t, []any{int64(1), int64(2), "x"})
	res, err := readJSON(t, tc, `{"entity":"orders","fields":["region"],"distinct":true,"limit":1,
		"orderBy":[{"field":"region","dir":"desc"}]}`)
	if err != nil || res.NextCursor != nil {
		t.Fatalf("%v %v", res.NextCursor, err)
	}
	want := `SELECT DISTINCT "region" FROM "orders" ORDER BY "region" DESC`
	if sql := db.Queries[0].Query; !strings.Contains(sql, want) {
		t.Fatalf("sql = %s", sql)
	}
}

// Filter groups: any is OR, all is AND, not negates; a malformed entry, an
// empty group and nesting too deep are refused, and conditions count
// through the groups.
func TestFilterGroups(t *testing.T) {
	t.Parallel()
	tc, db := orderContext(t)
	if _, err := readJSON(t, tc, `{"entity":"orders","filter":[{"field":"amount","op":"gt","value":10},
		{"any":[{"field":"region","op":"eq","value":"CN"},{"not":{"field":"region","op":"is_null"}}]}]}`); err != nil {
		t.Fatal(err)
	}
	if sql := db.Queries[0].Query; !strings.Contains(sql, `("amount" > $1 AND ("region" = $2 OR NOT "region" IS NULL))`) {
		t.Fatalf("sql = %s", sql)
	}
	deep := `{"field":"amount","op":"eq","value":1}`
	for range 5 {
		deep = `{"not":` + deep + `}`
	}
	tc.MaxFilterConditions = 2
	for _, filter := range []string{
		`[{"field":"amount","op":"eq","value":1,"any":[{"field":"amount","op":"eq","value":2}]}]`,
		`[{"any":[]}]`,
		`[` + deep + `]`,
		`[{"any":[{"field":"amount","op":"eq","value":1},{"all":[{"field":"amount","op":"eq","value":2},` +
			`{"field":"amount","op":"eq","value":3}]}]}]`,
		`[{"any":[{"field":"phone","op":"eq","value":"1"}]}]`,
	} {
		if _, err := readJSON(t, tc, `{"entity":"orders","filter":`+filter+`}`); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v", filter, err)
		}
	}
}

// aggregate_records filters groups by having, orders them by a group-by
// field or an aggregate column and keeps the top N; count distinct counts
// distinct values.
func TestAggregateHavingOrderLimit(t *testing.T) {
	t.Parallel()
	tc, db := orderContext(t)
	_, err := AggregateTool{}.Run(context.Background(), json.RawMessage(`{"entity":"orders","groupBy":["region"],
		"filter":[{"field":"amount","op":"gt","value":0}],
		"aggregates":[{"func":"sum","field":"amount"},{"func":"count","field":"id","distinct":true}],
		"having":[{"any":[{"field":"sum_amount","op":"gte","value":100},{"field":"region","op":"eq","value":"CN"}]}],
		"orderBy":[{"field":"sum_amount","dir":"desc"},{"field":"region"}],"limit":3}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT "region", SUM("amount"), COUNT(DISTINCT "id") FROM "orders" WHERE "amount" > $1 GROUP BY "region" ` +
		`HAVING (SUM("amount") >= $2 OR "region" = $3) ORDER BY CASE WHEN SUM("amount") IS NULL THEN 1 ELSE 0 END, ` +
		`SUM("amount") DESC, CASE WHEN "region" IS NULL THEN 1 ELSE 0 END, "region" ASC LIMIT 3`
	if sql := db.Queries[0].Query; sql != want {
		t.Fatalf("sql = %s\nwant  %s", sql, want)
	}
	for _, input := range []string{
		`{"entity":"orders","aggregates":[{"func":"count"}],"having":[{"field":"amount","op":"gt","value":1}]}`,
		`{"entity":"orders","aggregates":[{"func":"count"}],"orderBy":[{"field":"amount"}]}`,
		`{"entity":"orders","aggregates":[{"func":"sum","field":"amount","distinct":true}]}`,
		`{"entity":"orders","aggregates":[{"func":"count","distinct":true}]}`,
	} {
		if _, err := (AggregateTool{}).Run(context.Background(), json.RawMessage(input), tc); !errors.Is(err,
			ErrInvalidInput) {
			t.Errorf("%s: err = %v", input, err)
		}
	}
}

// A caller whose grant does not read the key orders by the fields it asked
// for alone: the order is not total, so no next cursor, and no denial.
func TestReadOrderByWithoutReadingTheKey(t *testing.T) {
	t.Parallel()
	tc, db := orderContext(t, []any{int64(1), int64(500), "x"})
	tc.Authorizer = rbac.NewGrantAuthorizer(tc.Registry, rbac.Policy{Roles: map[string]map[string][]rbac.Grant{
		"analyst": {"orders": {{ID: "g", Actions: []entity.Action{entity.ActionRead},
			Fields: &entity.FieldPermissions{Read: []string{"amount", "region"}}}}},
	}})
	tc.Role = "analyst"
	res, err := readJSON(t, tc, `{"entity":"orders","fields":["amount"],"limit":1,"orderBy":[{"field":"amount"}]}`)
	if err != nil || res.NextCursor != nil {
		t.Fatalf("%v %v", res.NextCursor, err)
	}
	if sql := db.Queries[0].Query; !strings.HasSuffix(sql, `ORDER BY "amount" ASC LIMIT 1`) {
		t.Fatalf("sql = %s", sql)
	}
}

// A field the request filters by is authorized whatever happens to the key
// an order adds: a caller not reading the key cannot filter by it.
func TestReadOrderByKeepsAuthorizingTheFilter(t *testing.T) {
	t.Parallel()
	tc, _ := orderContext(t, []any{int64(1), int64(500), "x"})
	tc.Authorizer = rbac.NewGrantAuthorizer(tc.Registry, rbac.Policy{Roles: map[string]map[string][]rbac.Grant{
		"analyst": {"orders": {{ID: "g", Actions: []entity.Action{entity.ActionRead},
			Fields: &entity.FieldPermissions{Read: []string{"amount", "region"}}}}},
	}})
	tc.Role = "analyst"
	_, err := readJSON(t, tc, `{"entity":"orders","fields":["amount"],"filter":[{"field":"id","op":"eq","value":1}],
		"orderBy":[{"field":"amount"}]}`)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("filtering by an unreadable key: %v", err)
	}
}
