package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/store/storetest"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

// relatedContext serves orders (belongs to a customer, has items) and the
// customers and items, to an analyst who reads every order, the customers
// of region CN only (and not their phone), and the items.
func relatedContext(t *testing.T) (Context, *storetest.FakeDB) {
	t.Helper()
	keyed := func(name string, fields ...string) entity.Entity {
		e := entity.Entity{Name: name, Source: name, Keys: []entity.Key{{Columns: []string{"id"}, Primary: true}},
			MCP: entity.MCPFlags{DMLTools: true}}
		for _, f := range append([]string{"id"}, fields...) {
			e.Attributes = append(e.Attributes, entity.Attribute{Name: f})
		}
		return e
	}
	orders := keyed("orders", "customer_id", "amount", "note")
	orders.Relations = []entity.Relationship{
		{Name: "customer", Target: "customers", Cardinality: "belongs-to", JoinOn: map[string]string{"customer_id": "id"}},
		{Name: "items", Target: "items", Cardinality: "has-many", JoinOn: map[string]string{"id": "order_id"}},
		{Name: "buyer", Target: "customers", Cardinality: "belongs-to", JoinOn: map[string]string{"note": "region"}},
	}
	customers := keyed("customers", "region", "phone")
	customers.Attributes = append(customers.Attributes, entity.Attribute{Name: "secret", Mask: "email"})
	items := keyed("items", "order_id", "sku")
	reg, err := entity.NewRegistry([]entity.Entity{orders, customers, items})
	if err != nil {
		t.Fatal(err)
	}
	read := []entity.Action{entity.ActionRead, entity.ActionAggregate}
	policy := rbac.Policy{Roles: map[string]map[string][]rbac.Grant{"analyst": {
		"orders": {{ID: "o", Actions: read}},
		"customers": {{ID: "c", Actions: read, Fields: &entity.FieldPermissions{Read: []string{"id", "region", "secret"}},
			Rows: relalg.Condition{Field: "region", Op: relalg.OpEq, Value: "CN"}}},
		"items": {{ID: "i", Actions: read}},
	}}}
	db := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return storetest.NewFakeRows([]string{"id"}), nil
	}}
	return Context{Services: &Services{Registry: reg, Authorizer: rbac.NewGrantAuthorizer(reg, policy)},
		Role: "analyst", DB: db, Dialect: testdialect.Postgres{}}, db
}

// A read filters and orders by a related row's field: the customer is
// joined on its key, its row policy in the join, and every column is
// qualified; an exists filter finds the items. Neither is cached.
func TestReadAcrossRelationships(t *testing.T) {
	t.Parallel()
	tc, db := relatedContext(t)
	if _, err := (ReadTool{}).Run(context.Background(), json.RawMessage(`{"entity":"orders","fields":["id","amount"],
		"filter":[{"field":"customer.region","op":"eq","value":"CN"},
			{"exists":"items","filter":[{"field":"sku","op":"like","value":"A%"}]}],
		"orderBy":[{"field":"customer.region","dir":"desc"}],"limit":5}`), tc); err != nil {
		t.Fatal(err)
	}
	want := `SELECT "_"."id", "_"."amount" FROM "orders" AS "_" LEFT JOIN "customers" AS "customer" ON ` +
		`"customer"."id" = "_"."customer_id" AND "customer"."region" = $1 WHERE ("customer"."region" = $2 AND ` +
		`EXISTS (SELECT 1 FROM "items" AS "x1" WHERE "x1"."order_id" = "_"."id" AND "x1"."sku" LIKE $3)) ` +
		`ORDER BY CASE WHEN "customer"."region" IS NULL THEN 1 ELSE 0 END, "customer"."region" DESC, "_"."id" ASC LIMIT 5`
	if got := db.Queries[0].Query; got != want {
		t.Fatalf("sql = %s\nwant  %s", got, want)
	}
}

// aggregate_records groups by a related field.
func TestAggregateByRelatedField(t *testing.T) {
	t.Parallel()
	tc, db := relatedContext(t)
	// The fake database answers no aggregate columns: only the SQL is checked.
	_, _ = (AggregateTool{}).Run(context.Background(), json.RawMessage(`{"entity":"orders",
		"groupBy":["customer.region"],"aggregates":[{"func":"sum","field":"amount"}],
		"orderBy":[{"field":"sum_amount","dir":"desc"}],"limit":3}`), tc)
	want := `SELECT "customer"."region", SUM("_"."amount") FROM "orders" AS "_" LEFT JOIN "customers" AS "customer" ON ` +
		`"customer"."id" = "_"."customer_id" AND "customer"."region" = $1 GROUP BY "customer"."region" ` +
		`ORDER BY CASE WHEN SUM("_"."amount") IS NULL THEN 1 ELSE 0 END, SUM("_"."amount") DESC LIMIT 3`
	if got := db.Queries[0].Query; got != want {
		t.Fatalf("sql = %s\nwant  %s", got, want)
	}
}

// What a request may not name across relationships.
func TestRelationshipRules(t *testing.T) {
	t.Parallel()
	tc, _ := relatedContext(t)
	filter := func(conds string) string { return `{"entity":"orders","filter":[` + conds + `]}` }
	for input, want := range map[string]error{
		// a related field the caller's grant does not read, or masked
		filter(`{"field":"customer.phone","op":"eq","value":"1"}`):  ErrUnauthorized,
		filter(`{"field":"customer.secret","op":"eq","value":"1"}`): ErrInvalidInput,
		// a relationship naming many rows, or not on the target's key
		filter(`{"field":"items.sku","op":"eq","value":"1"}`):    ErrInvalidInput,
		filter(`{"field":"buyer.region","op":"eq","value":"1"}`): ErrInvalidInput,
		// exists of what is no relationship, nested, or on a masked field
		filter(`{"exists":"nope","filter":[]}`):                                             ErrInvalidInput,
		filter(`{"exists":"customer","filter":[{"field":"secret","op":"eq","value":"1"}]}`): ErrInvalidInput,
		filter(`{"exists":"items","filter":[{"exists":"items","filter":[]}]}`):              ErrInvalidInput,
		// a related field in the projection
		`{"entity":"orders","fields":["customer.region"]}`: ErrInvalidInput,
	} {
		if _, err := (ReadTool{}).Run(context.Background(), json.RawMessage(input), tc); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", input, err, want)
		}
	}
	aggregate := `{"entity":"orders","aggregates":[{"func":"count","field":"customer.region"}]}`
	if _, err := (AggregateTool{}).Run(context.Background(), json.RawMessage(aggregate), tc); !errors.Is(err,
		ErrInvalidInput) {
		t.Errorf("aggregate of a related field: %v", err)
	}
	update := `{"entity":"orders","filter":[{"field":"id","op":"eq","value":1},{"exists":"items","filter":[]}],
		"set":{"amount":1}}`
	if _, err := (UpdateTool{}).Run(context.Background(), json.RawMessage(update), tc); !errors.Is(err,
		ErrInvalidInput) || !strings.Contains(err.Error(), "exists") {
		t.Errorf("update by exists: %v", err)
	}
}
