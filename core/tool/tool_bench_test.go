package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/mask"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

// The benchmarks measure what the governed tool path adds to a query (input
// decoding, authorization, IR, codegen, masking) against a database that
// answers at once; make bench runs them. See docs/testing.md.

func benchReadContext(b *testing.B, rows int) Context {
	b.Helper()
	reg, err := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	if err != nil {
		b.Fatal(err)
	}
	values := make([][]any, rows)
	for i := range values {
		values[i] = []any{int64(i), "alice@x.com"}
	}
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id", "email"}, values...), nil
	}}
	return Context{
		Role: "reader", DB: db, Dialect: testdialect.Postgres{},
		Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg), Masker: mask.NewRuleMasker(),
	}
}

func benchRead(b *testing.B, rows int, input readInput) {
	tc := benchReadContext(b, rows)
	in, err := json.Marshal(input)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := (ReadTool{}).Run(ctx, in, tc); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadToolPointLookup(b *testing.B) {
	benchRead(b, 1, readInput{Entity: "users", Filter: []condJSON{{Field: "id", Op: "eq", Value: int64(1)}}})
}

func BenchmarkReadToolHundredMaskedRows(b *testing.B) {
	benchRead(b, 100, readInput{Entity: "users", Limit: 100})
}
