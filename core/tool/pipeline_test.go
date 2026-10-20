package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/engine"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/store/storetest"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

// Only the executor runs statements, and a statement is made only from a
// grant and gated SQL: no tool can reach the database unauthorized, ungated
// or unmasked. The files allowed each call are named. A statement of the zero
// grant, which failed steps return, is refused.
func TestOnlyTheExecutorCallsTheDatabase(t *testing.T) {
	t.Parallel()
	allowed := map[string][]string{
		"QueryContext": {"pipeline.go", "transaction.go"}, // transaction.go: the transaction's own connection
		"ExecContext":  {"pipeline.go", "transaction.go"},
		"BeginTx":      {"transaction.go"},
		"maskRow":      {"pipeline.go"},
		"grant{}":      {"pipeline.go"},
		"gated{}":      {"pipeline.go"},
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		for _, use := range databaseUses(t, name) {
			if !containsString(allowed[use], name) {
				t.Errorf("%s uses %s; only the executor (pipeline.go) may", name, use)
			}
		}
	}
}

// databaseUses lists the calls to the database, to maskRow, and the grant
// and gated literals of a file.
func databaseUses(t *testing.T, name string) []string {
	t.Helper()
	src, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var uses []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			switch fn := n.Fun.(type) {
			case *ast.SelectorExpr:
				if s := fn.Sel.Name; s == "QueryContext" || s == "ExecContext" || s == "BeginTx" {
					uses = append(uses, s)
				}
			case *ast.Ident:
				if fn.Name == "maskRow" {
					uses = append(uses, fn.Name)
				}
			}
		case *ast.CompositeLit: // a zero one only fails (see statement.admitted)
			if id, ok := n.Type.(*ast.Ident); ok && (id.Name == "grant" || id.Name == "gated") && len(n.Elts) > 0 {
				uses = append(uses, id.Name+"{}")
			}
		}
		return true
	})
	return uses
}

func TestStatementWithoutGrantIsRefused(t *testing.T) {
	t.Parallel()
	db := &storetest.FakeDB{ExecFn: func(context.Context, string, ...any) (store.Result, error) {
		t.Fatal("an unauthorized statement ran")
		return store.Result{}, nil
	}}
	st := newStatement(Context{Services: &Services{}, DB: db}, entity.Resolved{}, grant{}, gated{})
	st.sql.SQL = "DELETE FROM users"
	if _, err := st.exec(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// wideRows serves n rows of about size bytes each and counts those read.
func wideRows(n, size int, read *int) *storetest.FakeDB {
	return &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		rows := make([][]any, n)
		for i := range rows {
			rows[i] = []any{int64(i), strings.Repeat("x", size)}
		}
		return &countingRows{FakeRows: storetest.NewFakeRows([]string{"id", "email"}, rows...), read: read}, nil
	}}
}

type countingRows struct {
	*storetest.FakeRows
	read *int
}

func (r *countingRows) Next() bool {
	ok := r.FakeRows.Next()
	if ok {
		*r.read++
	}
	return ok
}

func readerContext(db store.DB) Context {
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	return Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)},
		Role:     "reader",
		DB:       db,
		Dialect:  testdialect.Postgres{},
	}
}

// A read stops at the first row past its byte limit: the rest are never
// read, and the result is never encoded whole.
func TestReadStopsAtTheByteLimitAsRowsArrive(t *testing.T) {
	t.Parallel()
	read := 0
	tc := readerContext(wideRows(100, 1000, &read))
	tc.MaxReturnedBytes = 2500
	in, _ := json.Marshal(readInput{Entity: "users"})
	_, err := RunTool(context.Background(), ReadTool{}, in, tc)
	if !errors.Is(err, budget.ErrExceeded) {
		t.Fatalf("err = %v", err)
	}
	if read != 3 {
		t.Fatalf("read %d rows; the third crosses the limit", read)
	}
}

// The process's result memory bounds the results held at once, and a call
// releases its share when it ends.
func TestResultMemoryBoundsTheProcess(t *testing.T) {
	t.Parallel()
	read := 0
	memory := NewResultMemory(5000)
	tc := readerContext(wideRows(10, 1000, &read))
	tc.ResultMemory = memory
	in, _ := json.Marshal(readInput{Entity: "users"})
	if _, err := RunTool(context.Background(), ReadTool{}, in, tc); !errors.Is(err, budget.ErrExceeded) {
		t.Fatalf("err = %v", err)
	}
	if memory.InUse() != 0 {
		t.Fatalf("%d bytes held after the call", memory.InUse())
	}
	memory.SetCapacity(0)
	res, err := RunTool(context.Background(), ReadTool{}, in, tc)
	if err != nil || len(res.Content) != 10 || res.ReturnedBytes < 10000 {
		t.Fatalf("unbounded read = %d rows, %d bytes, %v", len(res.Content), res.ReturnedBytes, err)
	}
	if memory.InUse() != 0 {
		t.Fatalf("%d bytes held after the call", memory.InUse())
	}
}

// A write whose connection breaks while it runs may have been carried out: it
// reports OUTCOME_UNKNOWN, not retryable, and is followed as a write.
func TestWriteWithLostConnectionHasUnknownOutcome(t *testing.T) {
	t.Parallel()
	e := testUsersEntity()
	e.Role[entity.ActionUpdate] = []string{"writer"}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	db := &storetest.FakeDB{ExecFn: func(context.Context, string, ...any) (store.Result, error) {
		return store.Result{}, io.ErrUnexpectedEOF
	}}
	writes := NewWriteTracker()
	tc := Context{
		Services: &Services{
			Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg), Writes: writes,
			Sources: map[string]DataSource{"default": {
				DB: db, Dialect: testdialect.Postgres{}, ReadAfterWrite: time.Minute,
			}},
		},
		Role:    "writer",
		Session: "s",
		DB:      db,
		Dialect: testdialect.Postgres{},
	}
	in, _ := json.Marshal(updateInput{
		Entity: "users", Filter: []condJSON{{Field: "id", Op: "eq", Value: 1}}, Set: map[string]any{"email": "x"},
	})
	_, err := RunTool(context.Background(), UpdateTool{}, in, tc)
	denial, ok := DenialFor(err, "d")
	if !errors.Is(err, ErrOutcomeUnknown) || !ok || denial.Code != CodeOutcomeUnknown || denial.Retryable {
		t.Fatalf("err = %v, denial = %+v", err, denial)
	}
	if !writes.Wrote("s") {
		t.Fatal("a write of unknown outcome was not followed")
	}
	// A failure the database reported is no unknown outcome.
	db.ExecFn = func(context.Context, string, ...any) (store.Result, error) {
		return store.Result{}, errors.New("syntax error")
	}
	if _, err := RunTool(context.Background(), UpdateTool{}, in, tc); errors.Is(err, ErrOutcomeUnknown) ||
		!errors.Is(err, ErrDatabase) {
		t.Fatalf("err = %v", err)
	}
	// Nor is a connection that could not be made, whatever its cause.
	db.ExecFn = func(context.Context, string, ...any) (store.Result, error) {
		return store.Result{}, fmt.Errorf("%w: %w", store.ErrNotConnected, context.DeadlineExceeded)
	}
	if _, err := RunTool(context.Background(), UpdateTool{}, in, tc); errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v", err)
	}
}

type uncertainCommitTx struct {
	storetest.FakeTx
	err error
}

func (t *uncertainCommitTx) Commit() error { return t.err }

// A commit that may have committed reports OUTCOME_UNKNOWN with what the
// transaction wrote; its token then tells the same caller how it ended, and
// nobody else anything.
func TestCommitOutcomeAndTombstones(t *testing.T) {
	t.Parallel()
	txs := []store.Tx{&uncertainCommitTx{err: context.DeadlineExceeded}, &storetest.FakeTx{}}
	db := &storetest.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) {
		tx := txs[0]
		txs = txs[1:]
		return tx, nil
	}}
	manager := NewTransactionManager(time.Minute, 0)
	ctx := context.Background()
	uncertainToken, _ := manager.Begin(ctx, db, Caller{Session: "s", Role: "writer"}, "primary", nil)
	target := CacheTarget{Physical: "db", Relation: "users"}
	writer := Caller{Session: "s", Role: "writer"}
	if err := manager.MarkDirty(bg, uncertainToken, writer, "primary", nil, target); err != nil {
		t.Fatal(err)
	}
	targets, err := manager.Commit(ctx, uncertainToken, Caller{Session: "s", Role: "writer"})
	if !errors.Is(err, ErrOutcomeUnknown) || len(targets) != 1 {
		t.Fatalf("Commit = %v, %v", targets, err)
	}
	committedToken, _ := manager.Begin(ctx, db, Caller{Session: "s", Role: "writer"}, "primary", nil)
	if _, err := manager.Commit(ctx, committedToken, Caller{Session: "s", Role: "writer"}); err != nil {
		t.Fatal(err)
	}
	for token, want := range map[string]string{uncertainToken: OutcomeUnknown, committedToken: OutcomeCommitted} {
		var ended *TransactionEndedError
		_, err := manager.Commit(ctx, token, Caller{Session: "s", Role: "writer"})
		if !errors.As(err, &ended) || ended.Outcome != want || !errors.Is(err, ErrTransactionNotFound) {
			t.Fatalf("again = %v, want %s", err, want)
		}
		if err := manager.Validate(bg, token, Caller{Session: "other", Role: "writer"}); errors.As(
			err,
			&ended,
		) {
			t.Fatalf("another session learned the outcome: %v", err)
		}
	}
}

// A read runs once for the calls sharing it (singleflight) and holds its rows
// in the process's result memory for the call that ran it, until that call
// ends: once its caller went away, the rows the execution still reads (for
// calls still waiting on it) are not held for that call for good.
func TestResultMemoryIsNotHeldPastTheCall(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}), make(chan struct{})
	db := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		close(started)
		<-release
		return storetest.NewFakeRows([]string{"id", "email"}, []any{int64(1), "a@example.com"}), nil
	}}
	eng, err := engine.New(engine.WithIOPool(1))
	if err != nil {
		t.Fatal(err)
	}
	memory := NewResultMemory(0)
	tc := readerContext(db)
	tc.Engine, tc.ResultMemory = eng, memory
	in, _ := json.Marshal(readInput{Entity: "users"})
	ctx, leave := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() {
		_, err := RunTool(ctx, ReadTool{}, in, tc)
		ended <- err
	}()
	<-started
	leave()
	if err := <-ended; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	close(release)
	if err := eng.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if held := memory.InUse(); held != 0 {
		t.Fatalf("%d bytes held after the call ended", held)
	}
}

// A write's rows are read after the write took effect: a process short of
// result memory never fails it, which would invite a duplicate retry.
func TestWriteIsNeverRefusedForResultMemory(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	inserts := 0
	db := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		inserts++
		return storetest.NewFakeRows([]string{"id"}, []any{int64(7)}), nil
	}}
	memory := NewResultMemory(1)
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg), ResultMemory: memory},
		Role:     "writer",
		DB:       db,
		Dialect:  testdialect.Postgres{},
	}
	in, _ := json.Marshal(createInput{Entity: "users", Values: map[string]any{"id": 7}})
	if _, err := RunTool(context.Background(), CreateTool{}, in, tc); err != nil || inserts != 1 {
		t.Fatalf("create = %v after %d inserts", err, inserts)
	}
	if held := memory.InUse(); held != 0 {
		t.Fatalf("%d bytes held after the call", held)
	}
}

// A procedure's rows past the result memory are cut, not held: the call
// returns what fit, marked truncated, and audits it.
func TestProcedureRowsPastResultMemoryAreCut(t *testing.T) {
	t.Parallel()
	proc := entity.Entity{
		Name: "refresh", Source: "refresh", Kind: entity.KindProcedure,
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "note"}},
		Role:       entity.RoleAccess{entity.ActionExecute: {"w"}},
		MCP:        entity.MCPFlags{DMLTools: true, TrustedProcedure: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{proc})
	rows := make([][]any, 100)
	for i := range rows {
		rows[i] = []any{int64(i), strings.Repeat("x", 100)}
	}
	db := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return storetest.NewFakeRows([]string{"id", "note"}, rows...), nil
	}}
	memory := NewResultMemory(1000)
	auditor := &recorderAuditor{}
	events := []string{}
	tc := Context{
		Services: &Services{
			Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg), ResultMemory: memory, Auditor: auditor,
		},
		Role:    "w",
		DB:      db,
		Dialect: testdialect.Postgres{},
		Gate:    recordingGate{events: &events},
	}
	res, err := RunTool(context.Background(), ExecuteTool{}, json.RawMessage(`{"entity":"refresh"}`), tc)
	if err != nil || !res.Truncated || len(res.Content) == 0 || len(res.Content) >= len(rows) {
		t.Fatalf("execute = %d rows, truncated %v, %v", len(res.Content), res.Truncated, err)
	}
	if held := memory.InUse(); held != 0 {
		t.Fatalf("%d bytes held after the call", held)
	}
	if n := len(auditor.events); n == 0 || !strings.Contains(auditor.events[n-1].ResultSummary, "truncated") {
		t.Fatalf("audit = %+v", auditor.events)
	}
}
