package tool

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/mask"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/store/storetest"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

func TestAggregateTool(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "users", Source: "users",
		Attributes: []entity.Attribute{{Name: "dept"}, {Name: "salary"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionAggregate: {"reader"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return storetest.NewFakeRows([]string{"dept", "count"}, []any{"eng", int64(5)}), nil
	}}
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: auth},
		Role:     "reader",
		DB:       db,
		Dialect:  testdialect.Postgres{},
	}
	in, _ := json.Marshal(
		aggregateInput{Entity: "users", GroupBy: []string{"dept"}, Aggregates: []aggJSON{{Func: "count"}}},
	)
	res, err := AggregateTool{}.Run(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("got %v", res.Content)
	}
}

// Every aggregate column is named the same on every database, by position,
// so two aggregates of one function stay apart (PostgreSQL names both "sum")
// and the SQL, which fingerprints match, keeps no alias. A name taken by a
// group-by field, or by another aggregate, is refused.
func TestAggregateToolNamesItsColumns(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "users", Source: "users",
		Attributes: []entity.Attribute{{Name: "dept"}, {Name: "salary"}, {Name: "bonus"}, {Name: "count"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionAggregate: {"reader"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	var sql string
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, q string, _ ...any) (store.Rows, error) {
		sql = q
		return storetest.NewFakeRows([]string{"dept", "count", "sum", "sum"},
			[]any{"eng", int64(2), int64(10), int64(3)}), nil
	}}
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)},
		Role:     "reader", DB: db, Dialect: testdialect.Postgres{},
	}
	in, _ := json.Marshal(aggregateInput{Entity: "users", GroupBy: []string{"dept"}, Aggregates: []aggJSON{
		{Func: "count"}, {Func: "sum", Field: "salary"}, {Func: "sum", Field: "bonus"},
	}})
	res, err := (AggregateTool{}).Run(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"dept": "eng", "count": int64(2), "sum_salary": int64(10), "sum_bonus": int64(3)}
	if len(res.Content) != 1 || !reflect.DeepEqual(res.Content[0], want) {
		t.Fatalf("rows = %v, want %v", res.Content, want)
	}
	if strings.Contains(sql, " AS ") {
		t.Fatalf("the names must stay out of the SQL: %s", sql)
	}
	clash, _ := json.Marshal(aggregateInput{
		Entity: "users", GroupBy: []string{"count"}, Aggregates: []aggJSON{{Func: "count"}},
	})
	if _, err := (AggregateTool{}).Run(context.Background(), clash, tc); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an aggregate named as a group-by field: %v", err)
	}
	twice, _ := json.Marshal(aggregateInput{
		Entity: "users", Aggregates: []aggJSON{{Func: "sum", Field: "salary"}, {Func: "sum", Field: "salary"}},
	})
	if _, err := (AggregateTool{}).Run(context.Background(), twice, tc); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an aggregate given twice: %v", err)
	}
}

func TestAggregateToolRejectsInvalidFunc(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "users", Source: "users",
		Attributes: []entity.Attribute{{Name: "dept"}},
		Role:       entity.RoleAccess{entity.ActionAggregate: {"reader"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		t.Fatal("query must not run for an invalid aggregate func")
		return nil, nil
	}}
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: auth},
		Role:     "reader",
		DB:       db,
		Dialect:  testdialect.Postgres{},
	}
	in, _ := json.Marshal(aggregateInput{Entity: "users", Aggregates: []aggJSON{{Func: "count(*);drop"}}})
	_, err := AggregateTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v, want ErrInvalidInput", err)
	}
}

// Aggregate rows pass the masker too (defense in depth): a result column
// named as a masked field is masked.
func TestAggregateToolMasksResultRows(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name:       "users",
		Source:     "users",
		Attributes: []entity.Attribute{{Name: "count", Mask: "email"}},
		Role:       entity.RoleAccess{entity.ActionAggregate: {"reader"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	db := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return storetest.NewFakeRows([]string{"count(*)"}, []any{"alice@example.com"}), nil
	}}
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg), Masker: mask.NewRuleMasker()},
		Role:     "reader",
		DB:       db,
		Dialect:  testdialect.Postgres{},
	}
	result, err := (AggregateTool{}).Run(
		context.Background(),
		json.RawMessage(`{"entity":"users","aggregates":[{"func":"count"}]}`),
		tc,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Content[0]["count"]; got != "a***@example.com" {
		t.Fatalf("aggregate result was not masked: %v", got)
	}
}

func TestMaskedFieldsRejectedForValueRevealingUsages(t *testing.T) {
	t.Parallel()
	tc := maskedFieldTestContext(t)
	projection, err := (ReadTool{}).Run(
		context.Background(),
		json.RawMessage(`{"entity":"users","fields":["secret"]}`),
		tc,
	)
	if err != nil || len(projection.Content) != 1 {
		t.Fatalf("read projection should allow masked field: result=%v error=%v", projection.Content, err)
	}
	for _, tcse := range maskedFieldRejectionCases() {
		t.Run(tcse.name, func(t *testing.T) {
			assertMaskedFieldRejected(t, tc, tcse.tool, tcse.input)
		})
	}
}

func maskedFieldTestContext(t *testing.T) Context {
	t.Helper()
	e := entity.Entity{
		Name:       "users",
		Source:     "users",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "secret", Alias: "private", Mask: "email"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role: entity.RoleAccess{
			entity.ActionRead:      {"role"},
			entity.ActionCreate:    {"role"},
			entity.ActionUpdate:    {"role"},
			entity.ActionDelete:    {"role"},
			entity.ActionAggregate: {"role"},
		},
		MCP: entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	db := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return storetest.NewFakeRows([]string{"secret"}, []any{"alice@example.com"}), nil
	}}
	return Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)},
		Role:     "role",
		DB:       db,
		Dialect:  testdialect.Postgres{},
	}
}

func maskedFieldRejectionCases() []struct {
	name  string
	tool  Tool
	input string
} {
	return []struct {
		name  string
		tool  Tool
		input string
	}{
		{"read filter", ReadTool{}, `{"entity":"users","filter":[{"field":"secret","op":"eq","value":"x"}]}`},
		{"read cursor alias", ReadTool{}, `{"entity":"users","cursor":{"private":"x"}}`},
		{
			"update filter",
			UpdateTool{},
			`{"entity":"users","filter":[{"field":"secret","op":"eq","value":"x"}],"set":{"id":1}}`,
		},
		{"delete filter", DeleteTool{}, `{"entity":"users","filter":[{"field":"secret","op":"eq","value":"x"}]}`},
		{"group by", AggregateTool{}, `{"entity":"users","groupBy":["secret"],"aggregates":[{"func":"count"}]}`},
		{"aggregate field", AggregateTool{}, `{"entity":"users","aggregates":[{"func":"max","field":"secret"}]}`},
	}
}

func assertMaskedFieldRejected(t *testing.T, tc Context, tool Tool, input string) {
	t.Helper()
	_, err := tool.Run(context.Background(), json.RawMessage(input), tc)
	if !errors.Is(err, ErrInvalidInput) ||
		!strings.Contains(err.Error(), "unknown or inaccessible field") ||
		strings.Contains(err.Error(), "mask") {
		t.Fatalf("error = %v", err)
	}
}
