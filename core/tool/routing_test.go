package tool

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

// routedDB records which connection served each statement.
func routedDB(name string, used *[]string) *store.FakeDB {
	return &store.FakeDB{
		QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
			*used = append(*used, name)
			return store.NewFakeRows([]string{"id"}, []any{int64(1)}), nil
		},
		ExecFn: func(context.Context, string, ...any) (store.Result, error) {
			*used = append(*used, name)
			return store.Result{RowsAffected: 1}, nil
		},
	}
}

// beginsOn records which connection began each transaction.
func beginsOn(name string, began *[]string) *store.FakeDB {
	return &store.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) {
		*began = append(*began, name)
		return &store.FakeTx{}, nil
	}}
}

func TestActionsRouteToTheirConnection(t *testing.T) {
	t.Parallel()
	users := entity.Entity{
		Name: "users", Source: "users", MCP: entity.MCPFlags{DMLTools: true},
		Attributes: []entity.Attribute{{Name: "id"}},
		Keys:       []entity.Key{{Name: "pk", Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionRead: {"u"}, entity.ActionCreate: {"u"}, entity.ActionUpdate: {"u"}},
	}
	proc := entity.Entity{
		Name: "refresh", Source: "refresh", Kind: entity.KindProcedure,
		MCP:  entity.MCPFlags{DMLTools: true, TrustedProcedure: true},
		Role: entity.RoleAccess{entity.ActionExecute: {"u"}}, Affects: []string{"users"},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{users, proc})
	var used []string
	writes := NewWriteTracker()
	tc := Context{
		Role: "u", Session: "s1", Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg), Writes: writes,
		Sources: map[string]DataSource{"default": {
			DB: routedDB("replica", &used), Write: routedDB("rw", &used), Execute: routedDB("proc", &used),
			Dialect: testdialect.MySQL{}, ReadAfterWrite: time.Hour,
		}},
	}
	run := func(tc Context, tl Tool, input string) {
		t.Helper()
		if _, err := tl.Run(context.Background(), json.RawMessage(input), tc); err != nil {
			t.Fatalf("%s: %v", tl.Info().Name, err)
		}
	}
	run(tc, ReadTool{}, `{"entity":"users"}`)
	run(tc, CreateTool{}, `{"entity":"users","values":{"id":2}}`)
	run(tc, ReadTool{}, `{"entity":"users"}`)
	other := tc
	other.Session = "s2"
	run(other, ReadTool{}, `{"entity":"users"}`)
	run(other, ExecuteTool{}, `{"entity":"refresh"}`)
	want := []string{"replica", "rw", "rw", "replica", "proc"}
	if len(used) != len(want) {
		t.Fatalf("connections = %v, want %v", used, want)
	}
	for i := range want {
		if used[i] != want[i] {
			t.Fatalf("connections = %v, want %v (a session reads its writes from the write connection)", used, want)
		}
	}
}

func TestWriteTrackerWindow(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	w := NewWriteTracker()
	w.now = func() time.Time { return now }
	w.Record("s", "db")
	if !w.Recent("s", "db", time.Second) || w.Recent("s", "other", time.Second) || w.Recent("t", "db", time.Second) {
		t.Fatal("a write is recent for its session and datasource only")
	}
	now = now.Add(2 * time.Second)
	if w.Recent("s", "db", time.Second) {
		t.Fatal("the window elapsed")
	}
	w.Record("s", "db")
	w.Forget("s")
	if w.Recent("s", "db", time.Hour) {
		t.Fatal("Forget drops a closed session")
	}
}

func TestReadOnlyTransactionsBeginOnTheReadConnection(t *testing.T) {
	t.Parallel()
	e := entity.Entity{Name: "users", DataSource: "main", Role: entity.RoleAccess{
		entity.ActionRead: {"reader", "writer"}, entity.ActionCreate: {"writer"},
	}}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	var began []string
	manager := NewTransactionManager(time.Minute, 4)
	defer manager.Close()
	tc := Context{
		Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg), Transactions: manager,
		TxBeginners: map[string]store.TxBeginner{"main": beginsOn("rw", &began)},
		Sources:     map[string]DataSource{"main": {ReadTx: beginsOn("replica", &began)}},
	}
	for role, input := range map[string]string{"reader": `{}`, "writer": `{"readOnly":false}`} {
		tc.Role = role
		if _, err := (BeginTransactionTool{}).Run(context.Background(), json.RawMessage(input), tc); err != nil {
			t.Fatalf("%s: %v", role, err)
		}
	}
	if slices.Sort(began); !slices.Equal(began, []string{"replica", "rw"}) {
		t.Fatalf("began on %v, want replica for the read-only and rw for the read-write transaction", began)
	}
}

func TestReadsFollowingAWriteSkipSharedResults(t *testing.T) {
	t.Parallel()
	users := entity.Entity{
		Name: "users", Source: "users", MCP: entity.MCPFlags{DMLTools: true},
		Attributes: []entity.Attribute{{Name: "id"}},
		Role:       entity.RoleAccess{entity.ActionRead: {"u"}, entity.ActionCreate: {"u"}},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{users})
	var used []string
	var began []string
	manager := NewTransactionManager(time.Minute, 4)
	defer manager.Close()
	tc := Context{
		Role: "u", Session: "s1", Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg), Writes: NewWriteTracker(),
		Cache: cache.NewTTLCache[[]map[string]any](time.Minute, 0), Transactions: manager,
		TxBeginners: map[string]store.TxBeginner{"default": beginsOn("rw", &began)},
		Sources: map[string]DataSource{"default": {
			DB: routedDB("replica", &used), Write: routedDB("rw", &used), Dialect: testdialect.MySQL{},
			ReadAfterWrite: time.Hour, ReadTx: beginsOn("replica", &began),
		}},
	}
	other := tc
	other.Session = "s2"
	read := `{"entity":"users"}`
	for _, step := range []struct {
		tc    Context
		tool  Tool
		input string
	}{
		{tc, CreateTool{}, `{"entity":"users","values":{"id":2}}`},
		{other, ReadTool{}, read}, // a lagging replica fills the shared cache
		{tc, ReadTool{}, read},
		{tc, BeginTransactionTool{}, `{}`},
	} {
		if _, err := step.tool.Run(context.Background(), json.RawMessage(step.input), step.tc); err != nil {
			t.Fatalf("%s: %v", step.tool.Info().Name, err)
		}
	}
	if !slices.Equal(used, []string{"rw", "replica", "rw"}) || !slices.Equal(began, []string{"rw"}) {
		t.Fatalf("statements on %v, transactions on %v: the writer must read its write, not the shared cache",
			used, began)
	}
	mine, _ := engineSubmitKey(ReadTool{}, json.RawMessage(read), tc)
	theirs, _ := engineSubmitKey(ReadTool{}, json.RawMessage(read), other)
	if mine == theirs {
		t.Fatal("a session reading its writes must not share another session's in-flight read")
	}
}
