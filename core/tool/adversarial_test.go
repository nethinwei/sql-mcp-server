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
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

func adversarialReadContext(t *testing.T, queries *int) Context {
	t.Helper()
	e := entity.Entity{
		Name: "users", Source: "users",
		Attributes: []entity.Attribute{
			{Name: "id"},
			{Name: "tenant_id"},
			{Name: "secret", Alias: "private", Mask: "email"},
			{Name: "salary", Excluded: true},
		},
		Role: entity.RoleAccess{entity.ActionRead: {"reader"}},
		MCP:  entity.MCPFlags{DMLTools: true},
	}
	reg, err := entity.NewRegistry([]entity.Entity{e})
	if err != nil {
		t.Fatal(err)
	}
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		*queries++
		return store.NewFakeRows([]string{"id"}, []any{int64(1)}), nil
	}}
	return Context{
		Role: "reader", DB: db, Dialect: testdialect.Postgres{},
		Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg),
	}
}

func TestAdversarialToolFieldAndSchemaCorpus(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantErr error
	}{
		{"unknown top-level field", `{"entity":"users","schema":"admin"}`, ErrInvalidInput},
		{
			"unknown nested field",
			`{"entity":"users","filter":[{"field":"id","op":"eq","value":1,"schema":"admin"}]}`,
			ErrInvalidInput,
		},
		{"multiple JSON values", `{"entity":"users"} {"entity":"users"}`, ErrInvalidInput},
		{"identifier in entity value", `{"entity":"users; DROP TABLE audit;--"}`, ErrEntityNotFound},
		{
			"hidden field filter",
			`{"entity":"users","filter":[{"field":"salary","op":"gt","value":1}]}`,
			ErrInvalidInput,
		},
		{
			"masked field filter",
			`{"entity":"users","filter":[{"field":"secret","op":"eq","value":"x"}]}`,
			ErrInvalidInput,
		},
		{"masked alias cursor", `{"entity":"users","cursor":{"private":"x"}}`, ErrInvalidInput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queries := 0
			tc := adversarialReadContext(t, &queries)
			_, err := (ReadTool{}).Run(context.Background(), json.RawMessage(test.payload), tc)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if queries != 0 {
				t.Fatalf("rejected payload executed %d queries", queries)
			}
		})
	}
}

func TestAdversarialRowPolicyIsAlwaysConjoined(t *testing.T) {
	e := entity.Entity{
		Name:       "users",
		Source:     "users",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "tenant_id"}},
		Role:       entity.RoleAccess{entity.ActionRead: {"reader"}},
		RowPolicies: entity.RowPolicies{"reader": relalg.Condition{
			Field: "tenant_id", Op: relalg.OpEq, Value: "${subject.tenant_id}",
		}},
		MCP: entity.MCPFlags{DMLTools: true},
	}
	reg, err := entity.NewRegistry([]entity.Entity{e})
	if err != nil {
		t.Fatal(err)
	}
	var gotSQL string
	var gotArgs []any
	db := &store.FakeDB{QueryFn: func(_ context.Context, sql string, args ...any) (store.Rows, error) {
		gotSQL, gotArgs = sql, append([]any(nil), args...)
		return store.NewFakeRows([]string{"id"}), nil
	}}
	tc := Context{
		Role: "reader", Subject: map[string]any{"tenant_id": int64(7)},
		DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg),
	}
	_, err = (ReadTool{}).Run(context.Background(), json.RawMessage(
		`{"entity":"users","filter":[{"field":"tenant_id","op":"eq","value":8}]}`,
	), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotSQL, `"tenant_id" = $1 AND "tenant_id" = $2`) {
		t.Fatalf("row policy was not conjoined: %q", gotSQL)
	}
	if !reflect.DeepEqual(gotArgs, []any{int64(8), int64(7)}) {
		t.Fatalf("args = %#v, want user filter followed by policy", gotArgs)
	}
}

func TestAdversarialCacheAndSingleflightScopeIsolation(t *testing.T) {
	input := json.RawMessage(`{"entity":"users","fields":["id"]}`)
	tests := []struct {
		name     string
		left     Context
		right    Context
		wantSame bool
	}{
		{
			name:     "same authorization scope",
			left:     Context{Role: "reader", Subject: map[string]any{"tenant_id": 1}},
			right:    Context{Role: "reader", Subject: map[string]any{"tenant_id": 1}},
			wantSame: true,
		},
		{
			name:  "different role",
			left:  Context{Role: "reader", Subject: map[string]any{"tenant_id": 1}},
			right: Context{Role: "auditor", Subject: map[string]any{"tenant_id": 1}},
		},
		{
			name:  "different subject",
			left:  Context{Role: "reader", Subject: map[string]any{"tenant_id": 1}},
			right: Context{Role: "reader", Subject: map[string]any{"tenant_id": 2}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			leftKey, err := engineSubmitKey(ReadTool{}, input, test.left)
			if err != nil {
				t.Fatal(err)
			}
			rightKey, err := engineSubmitKey(ReadTool{}, input, test.right)
			if err != nil {
				t.Fatal(err)
			}
			if (leftKey == rightKey) != test.wantSame {
				t.Fatalf("singleflight keys equal = %v", leftKey == rightKey)
			}
			if (scopeKey(test.left.Role, test.left.Subject) == scopeKey(test.right.Role, test.right.Subject)) != test.wantSame {
				t.Fatal("cache scope isolation disagrees with singleflight isolation")
			}
		})
	}
}

func FuzzToolPayloadDecodeNormalizeFieldGate(f *testing.F) {
	f.Add([]byte(`{"entity":"users","filter":[{"field":"id","op":"eq","value":1}]}`))
	f.Add([]byte(`{"entity":"users","schema":"admin"}`))
	f.Add([]byte(`{"entity":"users","filter":[{"field":"salary","op":"gt","value":1}]}`))
	f.Add([]byte(`{"entity":"users","filter":[{"field":"secret","op":"eq","value":"x"}]}`))
	f.Add([]byte(`{"entity":"users"} {"entity":"users"}`))
	f.Add([]byte(`{"entity":"users","filter":[{"field":"id","op":"eq","value":9223372036854775808}]}`))

	f.Fuzz(func(t *testing.T, payload []byte) {
		queries := 0
		tc := adversarialReadContext(t, &queries)
		_, err := (ReadTool{}).Run(context.Background(), json.RawMessage(payload), tc)
		if err != nil && queries != 0 {
			t.Fatalf("failed payload executed %d queries: %v", queries, err)
		}
		if err == nil && queries != 1 {
			t.Fatalf("successful payload executed %d queries", queries)
		}
	})
}
