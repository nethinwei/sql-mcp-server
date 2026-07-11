package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/mask"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
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
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"dept", "count"}, []any{"eng", int64(5)}), nil
	}}
	tc := Context{Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth}
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
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		t.Fatal("query must not run for an invalid aggregate func")
		return nil, nil
	}}
	tc := Context{Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth}
	in, _ := json.Marshal(aggregateInput{Entity: "users", Aggregates: []aggJSON{{Func: "count(*);drop"}}})
	_, err := AggregateTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v, want ErrInvalidInput", err)
	}
}

func TestAggregateToolMasksResultRows(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name:       "users",
		Source:     "users",
		Attributes: []entity.Attribute{{Name: "email", Mask: "email"}},
		Role:       entity.RoleAccess{entity.ActionAggregate: {"reader"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"email"}, []any{"alice@example.com"}), nil
	}}
	tc := Context{
		Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg), Masker: mask.NewRuleMasker(nil),
	}
	result, err := (AggregateTool{}).Run(
		context.Background(),
		json.RawMessage(`{"entity":"users","aggregates":[{"func":"count"}]}`),
		tc,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Content[0]["email"]; got != "a***@example.com" {
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
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"secret"}, []any{"alice@example.com"}), nil
	}}
	return Context{
		Role: "role", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg),
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
