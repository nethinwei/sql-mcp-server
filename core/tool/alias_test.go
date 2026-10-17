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
	"github.com/nethinwei/sql-mcp-server/core/store/storetest"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

// aliasContext serves orders whose created_at is aliased createdAt and
// whose phone, aliased mobile, is masked; the database answers rows with
// the columns named as the SQL selects them.
func aliasContext(t *testing.T) (Context, *storetest.FakeDB) {
	t.Helper()
	e := entity.Entity{Name: "orders", Source: "orders", Keys: []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "created_at", Alias: "createdAt"},
			{Name: "phone", Alias: "mobile", Mask: "phone"}},
		Role: entity.RoleAccess{entity.ActionRead: {"r"}, entity.ActionAggregate: {"r"}, entity.ActionCreate: {"r"},
			entity.ActionUpdate: {"r"}},
		MCP: entity.MCPFlags{DMLTools: true}}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	db := &storetest.FakeDB{
		QueryFn: func(_ context.Context, query string, _ ...any) (store.Rows, error) {
			if strings.Contains(query, `AS "mobile"`) {
				return storetest.NewFakeRows([]string{"id", "mobile"}, []any{int64(1), "13812345678"}), nil
			}
			return storetest.NewFakeRows([]string{"id"}), nil
		},
	}
	return Context{Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg),
		Masker: mask.NewRuleMasker()}, Role: "r", DB: db, Dialect: testdialect.Postgres{}}, db
}

// A field named by its alias is its column in the SQL, wherever it is
// named, and a projected one comes back by its alias, masked when its
// column is.
func TestFieldAliasesNameColumns(t *testing.T) {
	t.Parallel()
	tc, db := aliasContext(t)
	res, err := ReadTool{}.Run(context.Background(), json.RawMessage(`{"entity":"orders","fields":["id","mobile"],
		"filter":[{"field":"createdAt","op":"gt","value":"2026-01-01"}],"orderBy":[{"field":"createdAt"}]}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	// created_at is selected for the next cursor, and not returned.
	want := `SELECT "id", "phone" AS "mobile", "created_at" FROM "orders" WHERE "created_at" > $1 ` +
		`ORDER BY "created_at" ASC, "id" ASC`
	if got := db.Queries[0].Query; !strings.HasPrefix(got, want) {
		t.Fatalf("sql = %s\nwant  %s", got, want)
	}
	if v := res.Content[0]["mobile"]; v == "13812345678" || v == nil {
		t.Fatalf("a masked field projected by its alias came back unmasked: %v", res.Content[0])
	}
	if _, err := (ReadTool{}).Run(context.Background(), json.RawMessage(`{"entity":"orders","limit":1,
		"fields":["createdAt"],"orderBy":[{"field":"createdAt"}]}`), tc); err != nil {
		t.Fatal(err)
	}
	if got := db.Queries[1].Query; !strings.HasPrefix(got, `SELECT "created_at" AS "createdAt", "id" FROM`) {
		t.Fatalf("a field selected by its alias is not selected again: %s", got)
	}
	_, _ = (AggregateTool{}).Run(context.Background(), json.RawMessage(`{"entity":"orders","groupBy":["createdAt"],
		"aggregates":[{"func":"count"}],"having":[{"field":"createdAt","op":"is_not_null"}],
		"orderBy":[{"field":"createdAt","dir":"desc"}]}`), tc)
	want = `SELECT "created_at", COUNT(*) FROM "orders" GROUP BY "created_at" HAVING "created_at" IS NOT NULL ` +
		`ORDER BY "created_at" DESC`
	if got := db.Queries[2].Query; !strings.HasPrefix(got, want) {
		t.Fatalf("aggregate sql = %s\nwant  %s", got, want)
	}
	_, _ = (CreateTool{}).Run(context.Background(), json.RawMessage(`{"entity":"orders","values":{"id":1,
		"createdAt":"2026-01-01"}}`), tc)
	if got := db.Queries[3].Query; !strings.HasPrefix(got, `INSERT INTO "orders" ("created_at", "id")`) {
		t.Fatalf("insert = %s", got)
	}
	_, err = (CreateTool{}).Run(context.Background(), json.RawMessage(`{"entity":"orders","values":{"id":1,
		"createdAt":"2026-01-01","created_at":"2026-01-02"}}`), tc)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a column named twice: %v", err)
	}
}
