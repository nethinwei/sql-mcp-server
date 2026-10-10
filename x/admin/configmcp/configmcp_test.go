package configmcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/x/admin/accounts"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/admin/graph"
	"github.com/nethinwei/sql-mcp-server/x/admin/scanjobs"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
	_ "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

const published = `databases:
  shop: {driver: postgres, dsn: "postgres://u@h/shop"}
  ledger: {driver: postgres, dsn: "postgres://u@h/ledger"}
entities:
  - {name: customers, datasource: shop, schema: public, primaryKey: [id], fields: [{name: id}]}
roles:
  analyst: {}
`

type env struct {
	url      string
	store    *revision.MemoryStore
	accounts *accounts.MemoryStore
	plans    *configstore.SQLStore
}

func newEnv(t *testing.T) env {
	t.Helper()
	ctx := context.Background()
	store := revision.NewMemoryStore(nil)
	d, err := store.Create(ctx, revision.Draft{Payload: []byte(published)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish(ctx, d.ID, 0, revision.Meta{}); err != nil {
		t.Fatal(err)
	}
	spec := configstore.Spec{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "store.db")}
	if err := configstore.Init(ctx, spec, nil); err != nil {
		t.Fatal(err)
	}
	plans, err := configstore.Open(ctx, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plans.Close() })
	src := catalog.Source{Datasource: "shop", Driver: "postgres", DSN: "postgres://u@h/shop"}
	if err := plans.PutCatalogSchemas(ctx, src.ID(), "shop", []catalog.Schema{{Name: "public", ScannedAt: time.Now(),
		Default: "public", Tables: []catalog.Table{
			{Schema: "public", Name: "customers", Columns: []catalog.Column{{Name: "id", Type: "bigint"}},
				Keys: []catalog.Key{{Name: "pk", Columns: []string{"id"}, Primary: true}}},
			{Schema: "public", Name: "orders", Columns: []catalog.Column{{Name: "id", Type: "bigint"}},
				Keys: []catalog.Key{{Name: "pk", Columns: []string{"id"}, Primary: true}}},
		}}}); err != nil {
		t.Fatal(err)
	}
	admins := accounts.NewMemoryStore(
		accounts.Account{Username: "root", Permissions: []string{auth.PermAll}},
		accounts.Account{Username: "viewer", Permissions: []string{auth.PermRead}},
	)
	scans := configapp.NewScans(scanjobs.Options{}, plans)
	t.Cleanup(scans.Close)
	app := &configapp.Service{Store: store, Scans: scans, Plans: plans, WorkspaceEdit: graph.WorkspaceEdit,
		WorkspaceSections: graph.WorkspaceSections}
	srv := httptest.NewServer(New(app, plans, admins, Options{}))
	t.Cleanup(srv.Close)
	return env{url: srv.URL, store: store, accounts: admins, plans: plans}
}

func (e env) token(t *testing.T, username string) string {
	t.Helper()
	return e.tokenUntil(t, username, time.Time{})
}

func (e env) tokenUntil(t *testing.T, username string, expires time.Time) string {
	t.Helper()
	return e.scopedToken(t, username, expires, configstore.TokenScope{})
}

func (e env) scopedToken(t *testing.T, username string, expires time.Time, scope configstore.TokenScope) string {
	t.Helper()
	token, err := config.NewUserToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.plans.CreateAdminToken(context.Background(), configstore.AdminToken{
		ID: token[:12], Username: username, Name: token[:12], TokenHash: config.TokenHash(token), ExpiresAt: expires,
		Scope: scope,
	}); err != nil {
		t.Fatal(err)
	}
	return token
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func connect(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	return client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: &http.Client{Transport: bearer{token}}, MaxRetries: -1,
		DisableStandaloneSSE: true,
	}, nil)
}

func call(t *testing.T, s *mcp.ClientSession, name string, args any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out != nil && !res.IsError {
		data, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatal(err)
		}
	}
	return res
}

func TestAgentAppliesPlansToTheWorkspace(t *testing.T) {
	e := newEnv(t)
	s, err := connect(t, e.url, e.token(t, "root"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	checkNoPublishTool(t, s)
	checkCatalogSearch(t, s)
	plan := map[string]any{"scope": map[string]any{"status": []string{"new"}}, "import": true,
		"grants": []any{map[string]any{"principal": "role:analyst", "actions": []string{"read"}}}}
	var preview struct {
		PlanID     string
		Added      int
		Validation struct{ Ok bool }
	}
	call(t, s, "plan_preview", map[string]any{"plan": plan}, &preview)
	if preview.Added != 1 || !preview.Validation.Ok || preview.PlanID == "" {
		t.Fatalf("preview = %+v", preview)
	}
	var added struct {
		Total int
		Items []string
	}
	call(t, s, "plan_details", map[string]any{"planId": preview.PlanID, "section": "added"}, &added)
	if added.Total != 1 || added.Items[0] != "shop.public.orders" {
		t.Fatalf("details = %+v", added)
	}
	if res := call(t, s, "plan_apply", map[string]any{"plan": plan, "planId": "stale"}, nil); !res.IsError {
		t.Fatal("a plan was applied that is not the one previewed")
	}
	checkAppliesToTheWorkspace(t, e, s, plan, preview.PlanID)
	checkPreviewsTheWorkspace(t, e, s)
	checkEffectBound(t, e, s)
	if pub, _ := e.store.Published(context.Background()); pub.ID != 1 {
		t.Fatal("applying a plan must not publish")
	}
	if revs, _ := e.store.List(context.Background(), 0); len(revs) != 1 {
		t.Fatalf("applying a plan saved revisions: %+v", revs)
	}
	res := call(t, s, "plan_preview", map[string]any{"plan": map[string]any{"scope": map[string]any{}}}, nil)
	if !res.IsError {
		t.Fatal("a plan with nothing to do must fail")
	}
}

func checkCatalogSearch(t *testing.T, s *mcp.ClientSession) {
	t.Helper()
	var page struct {
		Total  int
		Counts struct{ New int }
	}
	call(t, s, "catalog_search", map[string]any{"status": []string{"NEW"}}, &page)
	if page.Total != 1 || page.Counts.New != 1 {
		t.Fatalf("catalog = %+v", page)
	}
}

// checkAppliesToTheWorkspace applies a previewed plan: the items it changes
// are written into the administrator's workspace, at once, with a note of
// who applied it and why.
func checkAppliesToTheWorkspace(t *testing.T, e env, s *mcp.ClientSession, plan map[string]any, planID string) {
	t.Helper()
	var applied struct{ Items []struct{ Kind, Key string } }
	call(t, s, "plan_apply", map[string]any{"plan": plan, "planId": planID, "comment": "orders"}, &applied)
	written := map[string]bool{}
	for _, it := range applied.Items {
		written[it.Kind+" "+it.Key] = true
	}
	if len(written) != 2 || !written["entity shop.public.orders"] || !written["role analyst"] {
		t.Fatalf("written = %+v", applied)
	}
	w, ok, err := e.plans.Workspace(context.Background(), "root")
	if err != nil || !ok || w.Base != 1 || !strings.HasPrefix(w.UpdatedBy, "agent:") ||
		workspace.IsMissing(w.Items.Get(workspace.Ref{Kind: "entity", Key: "shop.public.orders"})) ||
		len(w.Notes) != 1 || w.Notes[0].Text != "orders" {
		t.Fatalf("workspace = %+v, %v %v", w, ok, err)
	}
}

// putRole writes role into the administrator's workspace, as their console would.
func putRole(t *testing.T, e env, role map[string]any) {
	t.Helper()
	ctx := context.Background()
	w, _, err := e.plans.Workspace(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	w.Items = w.Items.With(workspace.Ref{Kind: "role", Key: role["name"].(string)}, role)
	if _, err := e.plans.PutWorkspace(ctx, w, w.Version, workspace.LogEntry{Op: "edit", By: "root"}); err != nil {
		t.Fatal(err)
	}
}

// checkPreviewsTheWorkspace previews against the workspace as the console
// left it, and applies a plan previewed before another value of the item it
// changes was edited (as when another plan previewed with it was applied
// first).
func checkPreviewsTheWorkspace(t *testing.T, e env, s *mcp.ClientSession) {
	t.Helper()
	putRole(t, e, map[string]any{"name": "support", "grants": []any{}})
	more := map[string]any{"scope": map[string]any{"tables": []string{"customers"}},
		"grants": []any{map[string]any{"principal": "role:support", "actions": []string{"read"}}}}
	var preview struct {
		PlanID  string
		Changes []struct{ Kind string }
	}
	call(t, s, "plan_preview", map[string]any{"plan": more}, &preview)
	if len(preview.Changes) != 1 || preview.Changes[0].Kind != "grant" {
		t.Fatalf("a role of the workspace is created again: %+v", preview)
	}
	putRole(t, e, map[string]any{"name": "support", "description": "x", "grants": []any{}})
	if res := call(t, s, "plan_apply", map[string]any{"plan": more, "planId": preview.PlanID}, nil); res.IsError {
		t.Fatalf("a plan previewed before the workspace changed: %v", res.Content)
	}
	w, _, _ := e.plans.Workspace(context.Background(), "root")
	support, _ := json.Marshal(w.Items.Get(workspace.Ref{Kind: "role", Key: "support"}))
	if !strings.Contains(string(support), `"description":"x"`) ||
		!strings.Contains(string(support), `"entity":"shop.public.customers"`) {
		t.Fatalf("the console's edit of the role or the grant is lost: %s", support)
	}
	if res := call(t, s, "plan_apply", map[string]any{"plan": more, "planId": "stale"}, nil); !res.IsError {
		t.Fatal("a plan was applied that is not the one previewed")
	}
}

// A plan whose effect changed since its preview (the grant it adds was
// changed meanwhile) is refused: it would not change what was reviewed.
func checkEffectBound(t *testing.T, e env, s *mcp.ClientSession) {
	t.Helper()
	plan := map[string]any{"scope": map[string]any{"tables": []string{"customers"}},
		"grants": []any{map[string]any{"principal": "role:auditor", "actions": []string{"read"}}}}
	var preview struct{ PlanID string }
	call(t, s, "plan_preview", map[string]any{"plan": plan}, &preview)
	putRole(t, e, map[string]any{"name": "auditor", "grants": []any{
		map[string]any{"entity": "shop.public.customers", "actions": []any{"AGGREGATE"}}}})
	if res := call(t, s, "plan_apply", map[string]any{"plan": plan, "planId": preview.PlanID}, nil); !res.IsError {
		t.Fatal("a plan was applied whose effect changed since its preview")
	}
}

// checkNoPublishTool: an agent applies plans, it never publishes.
func checkNoPublishTool(t *testing.T, s *mcp.ClientSession) {
	t.Helper()
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	if !slices.Contains(names, "plan_apply") || slices.ContainsFunc(names, func(n string) bool {
		return strings.Contains(n, "publish") || strings.Contains(n, "rollback")
	}) {
		t.Fatalf("tools = %v", names)
	}
}

func TestTokensAndPermissions(t *testing.T) {
	e := newEnv(t)
	for _, token := range []string{"", "not-a-token"} {
		if _, err := connect(t, e.url, token); err == nil {
			t.Fatalf("token %q connected", token)
		}
	}
	viewer, err := connect(t, e.url, e.token(t, "viewer"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = viewer.Close() }()
	res := call(t, viewer, "plan_apply", map[string]any{"plan": map[string]any{
		"scope": map[string]any{}, "import": true}}, nil)
	if !res.IsError {
		t.Fatal("a read-only administrator applied a plan")
	}
	var revs []struct{ ID string }
	call(t, viewer, "revisions", map[string]any{}, &revs)
	if len(revs) != 1 {
		t.Fatalf("revisions = %+v", revs)
	}
	token := e.token(t, "root")
	if _, err := e.accounts.MutateAdmins(context.Background(), func(all []accounts.Account) ([]accounts.Account, error) {
		for _, a := range all {
			if a.Username == "root" {
				a.Disabled = true
				return []accounts.Account{a}, nil
			}
		}
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := connect(t, e.url, token); err == nil {
		t.Fatal("a disabled administrator's token connected")
	}
	if _, err := connect(t, e.url, e.tokenUntil(t, "viewer", time.Now().Add(-time.Minute))); err == nil {
		t.Fatal("an expired token connected")
	}
	tokens, _ := e.plans.AdminTokens(context.Background(), "viewer")
	if len(tokens) != 2 || tokens[0].LastUsedAt.IsZero() {
		t.Fatalf("the use of a token is not recorded: %+v", tokens)
	}
}

// A plan is applied from a review only with each exception to confirm
// confirmed by its ID; the confirmations go with it to the console.
func TestConfirmationsAreBoundToExceptions(t *testing.T) {
	e := newEnv(t)
	s, err := connect(t, e.url, e.token(t, "root"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	plan := map[string]any{"scope": map[string]any{"status": []string{"new"}}, "import": true,
		"grants": []any{map[string]any{"principal": "role:analyst", "actions": []string{"read"}}}}
	var preview struct {
		PlanID       string
		ToConfirm    int
		ToConfirmIDs []string
	}
	call(t, s, "plan_preview", map[string]any{"plan": plan, "review": true}, &preview)
	if preview.ToConfirm != 1 || len(preview.ToConfirmIDs) != 1 {
		t.Fatalf("preview = %+v", preview)
	}
	apply := map[string]any{"plan": plan, "review": true, "planId": preview.PlanID}
	if res := call(t, s, "plan_apply", apply, nil); !res.IsError {
		t.Fatal("a plan was applied with an exception not confirmed")
	}
	apply["confirmed"] = []string{"not-an-exception"}
	if res := call(t, s, "plan_apply", apply, nil); !res.IsError {
		t.Fatal("a plan was applied confirming something else")
	}
	apply["confirmed"] = preview.ToConfirmIDs
	var applied struct{ Items []struct{ Kind, Key string } }
	call(t, s, "plan_apply", apply, &applied)
	if len(applied.Items) == 0 {
		t.Fatalf("nothing written once confirmed: %+v", applied)
	}
}

// A token limited to some datasources reads and plans within them only, and
// not what spans every datasource.
func TestScopedTokens(t *testing.T) {
	e := newEnv(t)
	plan := map[string]any{"scope": map[string]any{"status": []string{"new"}}, "import": true}
	shop, err := connect(t, e.url, e.scopedToken(t, "root", time.Time{},
		configstore.TokenScope{Datasources: []string{"shop"}}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shop.Close() }()
	var page struct {
		Total    int
		Coverage []struct{ Datasource string }
	}
	call(t, shop, "catalog_search", map[string]any{}, &page)
	if page.Total != 2 || len(page.Coverage) != 1 || page.Coverage[0].Datasource != "shop" {
		t.Fatalf("catalog = %+v", page)
	}
	var preview struct{ PlanID string }
	call(t, shop, "plan_preview", map[string]any{"plan": plan}, &preview)
	if preview.PlanID == "" {
		t.Fatal("a plan within the token's datasources was refused")
	}
	elsewhere := map[string]any{"scope": map[string]any{"datasources": []string{"ledger"}}, "import": true}
	for name, args := range map[string]any{
		"catalog_search": map[string]any{"datasources": []string{"ledger"}},
		"plan_preview":   map[string]any{"plan": elsewhere},
		"catalog_table":  map[string]any{"ref": "ledger.public.entries"},
		"scan_status":    map[string]any{"datasource": "ledger"},
		"diff":           map[string]any{"from": "1"},
		"simulate":       map[string]any{"user": "role:analyst", "entity": "customers", "action": "read"},
		"revisions":      map[string]any{},
	} {
		if res := call(t, shop, name, args, nil); !res.IsError {
			t.Errorf("a token limited to shop ran %s %v", name, args)
		}
	}
}

// A token limited to some datasources reads nothing beyond them: not a
// preview another token of the same administrator made, not the counts of a
// scope's conditions.
func TestScopedTokensReadNothingBeyond(t *testing.T) {
	e := newEnv(t)
	ledger := catalog.Source{Datasource: "ledger", Driver: "postgres", DSN: "postgres://u@h/ledger"}
	if err := e.plans.PutCatalogSchemas(context.Background(), ledger.ID(), "ledger", []catalog.Schema{{Name: "public",
		ScannedAt: time.Now(), Default: "public", Tables: []catalog.Table{{Schema: "public", Name: "entries",
			Columns: []catalog.Column{{Name: "id", Type: "bigint"}}}}}}); err != nil {
		t.Fatal(err)
	}
	root, err := connect(t, e.url, e.token(t, "root"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	plan := map[string]any{"scope": map[string]any{"status": []string{"new"}}, "import": true}
	var wide struct{ PlanID string }
	call(t, root, "plan_preview", map[string]any{"plan": plan}, &wide)
	shop, err := connect(t, e.url, e.scopedToken(t, "root", time.Time{},
		configstore.TokenScope{Datasources: []string{"shop"}}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shop.Close() }()
	if res := call(t, shop, "plan_details", map[string]any{"planId": wide.PlanID, "section": "added"}, nil); !res.IsError {
		t.Fatal("a limited token read a preview spanning every datasource")
	}
	var own struct{ PlanID string }
	call(t, shop, "plan_preview", map[string]any{"plan": plan}, &own)
	var added struct{ Items []string }
	call(t, shop, "plan_details", map[string]any{"planId": own.PlanID, "section": "added"}, &added)
	if len(added.Items) != 1 || added.Items[0] != "shop.public.orders" {
		t.Fatalf("the token's own preview = %+v", added)
	}
	checkScopeCounts(t, root, shop)
}

// checkScopeCounts: the counts of each condition of a scope stay within the
// token's datasources.
func checkScopeCounts(t *testing.T, root, shop *mcp.ClientSession) {
	t.Helper()
	var stats struct {
		Total      int
		Conditions []struct {
			Name    string
			Matched int
		}
	}
	call(t, shop, "plan_scope", map[string]any{"scope": map[string]any{"tables": []string{"entries"}}}, &stats)
	tables := slices.IndexFunc(stats.Conditions, func(c struct {
		Name    string
		Matched int
	}) bool {
		return c.Name == "tables"
	})
	if stats.Total != 0 || tables < 0 || stats.Conditions[tables].Matched != 0 {
		t.Fatalf("scope counts beyond the token's datasources: %+v", stats)
	}
	call(t, root, "plan_scope", map[string]any{"scope": map[string]any{"tables": []string{"entries"}}}, &stats)
	if stats.Total != 1 {
		t.Fatalf("an unlimited token's counts = %+v", stats)
	}
}

// A plan applies as previewed however it is written: the service puts it in
// its normal form (actions lower case, sorted, once each) before the plan ID.
func TestPlanAppliesAsWritten(t *testing.T) {
	e := newEnv(t)
	s, err := connect(t, e.url, e.token(t, "root"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	plan := map[string]any{"scope": map[string]any{"status": []string{"new"}}, "import": true,
		"grants": []any{map[string]any{"principal": "role:analyst", "actions": []string{"READ", "Aggregate", "read"}}}}
	var preview struct{ PlanID string }
	call(t, s, "plan_preview", map[string]any{"plan": plan}, &preview)
	if res := call(t, s, "plan_apply", map[string]any{"plan": plan, "planId": preview.PlanID}, nil); res.IsError {
		t.Fatalf("the plan as written does not apply: %v", res.Content)
	}
}

// A token created read only (no longer offered) is refused: never given
// the administrator's write permissions.
func TestRetiredReadOnlyTokensAreRefused(t *testing.T) {
	e := newEnv(t)
	token := e.scopedToken(t, "root", time.Time{}, configstore.TokenScope{ReadOnly: true})
	if _, err := connect(t, e.url, token); err == nil {
		t.Fatal("a read-only token connected")
	}
}
