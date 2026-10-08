package tool

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/nethinwei/sql-mcp-server/core/audit"
	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

// recorderAuditor is a synchronous audit.Auditor fake for tests.
type recorderAuditor struct{ events []audit.Event }

func (r *recorderAuditor) Record(_ context.Context, e audit.Event) error {
	r.events = append(r.events, e)
	return nil
}

type queryTx struct {
	store.FakeTx
	query func(context.Context, string, ...any) (store.Rows, error)
}

type recordingRows struct {
	store.Rows
	events *[]string
	once   sync.Once
}

func (r *recordingRows) Close() error {
	var err error
	r.once.Do(func() {
		*r.events = append(*r.events, "close")
		err = r.Rows.Close()
	})
	return err
}

type recordingCache struct {
	events        *[]string
	invalidations int
	err           error
}

func (*recordingCache) Get(context.Context, cache.Key) ([]map[string]any, bool) {
	return nil, false
}
func (*recordingCache) Set(context.Context, cache.Key, []map[string]any) error { return nil }
func (c *recordingCache) Invalidate(string) error {
	c.invalidations++
	if c.events != nil {
		*c.events = append(*c.events, "invalidate")
	}
	return c.err
}

func (t *queryTx) QueryContext(ctx context.Context, query string, args ...any) (store.Rows, error) {
	return t.query(ctx, query, args...)
}

type recordingAuthorizer struct {
	events *[]string
	dec    rbac.Decision
}

func (a recordingAuthorizer) Authorize(_ context.Context, _ rbac.Request) (rbac.Decision, error) {
	*a.events = append(*a.events, "authorize")
	return a.dec, nil
}

type recordingGate struct {
	events *[]string
}

func (g recordingGate) Check(_ context.Context, _ codegen.Compiled) (cost.Decision, error) {
	*g.events = append(*g.events, "gate")
	return cost.Decision{Allow: true}, nil
}

type recordingAnalyzeSampler struct {
	calls []codegen.Compiled
	plan  cost.Plan
}

type blockingTransactionReadTool struct {
	started chan struct{}
	release chan struct{}
}

func (blockingTransactionReadTool) Info() Info {
	return Info{Name: "transaction_read_probe", ReadOnly: true}
}
func (blockingTransactionReadTool) Enabled(config.ToolFlags) bool { return true }
func (t blockingTransactionReadTool) Run(context.Context, json.RawMessage, Context) (Result, error) {
	close(t.started)
	<-t.release
	return Result{Content: []map[string]any{{"ok": true}}}, nil
}

func (s *recordingAnalyzeSampler) ExplainAnalyze(_ context.Context, compiled codegen.Compiled) (cost.Plan, error) {
	s.calls = append(s.calls, compiled)
	return s.plan, nil
}

func testUsersEntity() entity.Entity {
	return entity.Entity{
		Name:       "users",
		Source:     "users",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "email", Mask: "email"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionRead: {"reader"}, entity.ActionCreate: {"writer"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
}
