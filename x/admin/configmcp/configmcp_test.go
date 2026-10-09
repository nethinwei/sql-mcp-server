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
	"github.com/nethinwei/sql-mcp-server/x/admin/accounts"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
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
	app := &configapp.Service{Store: store, Scans: scans, Plans: plans}
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

func TestAgentPlansAndSavesADraft(t *testing.T) {
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
	if res := call(t, s, "plan_save_draft", map[string]any{"plan": plan, "planId": "stale"}, nil); !res.IsError {
		t.Fatal("a draft was saved for a plan that is not the one previewed")
	}
	id := checkSavesADraft(t, s, plan, preview.PlanID)
	checkContinuesADraft(t, s, id)
	if pub, _ := e.store.Published(context.Background()); pub.ID != 1 {
		t.Fatal("saving a draft must not publish")
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

// checkSavesADraft saves a previewed plan and reads the draft's diff.
func checkSavesADraft(t *testing.T, s *mcp.ClientSession, plan map[string]any, planID string) string {
	t.Helper()
	var saved struct {
		ID, Parent, State, ReviewURL string
		DiffLines                    int
	}
	call(t, s, "plan_save_draft", map[string]any{"plan": plan, "planId": planID, "comment": "orders"}, &saved)
	if saved.State != "DRAFT" || saved.DiffLines == 0 ||
		!strings.HasSuffix(saved.ReviewURL, "/admin/revisions?id="+saved.ID) {
		t.Fatalf("saved = %+v", saved)
	}
	var diff struct {
		Total int
		Diff  string
	}
	call(t, s, "diff", map[string]any{"from": saved.Parent, "to": saved.ID, "limit": 1000}, &diff)
	if !strings.Contains(diff.Diff, "+      - entity: shop.public.orders") || diff.Total < saved.DiffLines {
		t.Fatalf("diff = %+v", diff)
	}
	return saved.ID
}

// checkContinuesADraft applies a second plan to the draft the first one saved.
func checkContinuesADraft(t *testing.T, s *mcp.ClientSession, draft string) {
	t.Helper()
	more := map[string]any{"scope": map[string]any{"tables": []string{"orders"}},
		"grants": []any{map[string]any{"principal": "role:support", "actions": []string{"read"}}}}
	var preview struct{ PlanID string }
	call(t, s, "plan_preview", map[string]any{"plan": more, "base": draft}, &preview)
	var second struct{ ID, Parent string }
	call(t, s, "plan_save_draft", map[string]any{"plan": more, "base": draft, "planId": preview.PlanID}, &second)
	var diff struct{ Diff string }
	call(t, s, "diff", map[string]any{"from": draft, "to": second.ID}, &diff)
	if second.Parent != draft || !strings.Contains(diff.Diff, "support") ||
		strings.Contains(diff.Diff, "+  - name: orders") {
		t.Fatalf("second draft = %+v, diff %s", second, diff.Diff)
	}
	var entities struct {
		Total int
		Items []string
	}
	call(t, s, "plan_details", map[string]any{"planId": preview.PlanID, "section": "changeEntities", "change": 1},
		&entities)
	if entities.Total != 1 || entities.Items[0] != "shop.public.orders" {
		t.Fatalf("change entities = %+v", entities)
	}
}

// checkNoPublishTool: an agent saves drafts, it never publishes.
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
	if !slices.Contains(names, "plan_save_draft") || slices.ContainsFunc(names, func(n string) bool {
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
	res := call(t, viewer, "plan_save_draft", map[string]any{"plan": map[string]any{
		"scope": map[string]any{}, "import": true}}, nil)
	if !res.IsError {
		t.Fatal("a read-only administrator saved a draft")
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

// A draft is saved from a review only with each exception to confirm
// confirmed by its ID; the confirmations are kept with the draft.
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
	save := map[string]any{"plan": plan, "review": true, "planId": preview.PlanID}
	if res := call(t, s, "plan_save_draft", save, nil); !res.IsError {
		t.Fatal("a draft was saved with an exception not confirmed")
	}
	save["confirmed"] = []string{"not-an-exception"}
	if res := call(t, s, "plan_save_draft", save, nil); !res.IsError {
		t.Fatal("a draft was saved confirming something else")
	}
	save["confirmed"] = preview.ToConfirmIDs
	var saved struct{ ID, Comment string }
	call(t, s, "plan_save_draft", save, &saved)
	if !strings.Contains(saved.Comment, "confirmed: review") {
		t.Fatalf("saved = %+v", saved)
	}
}

// A token narrowed to reading cannot scan or save; one narrowed to some
// datasources reads and plans within them only, and not what spans every
// datasource.
func TestScopedTokens(t *testing.T) {
	e := newEnv(t)
	plan := map[string]any{"scope": map[string]any{"status": []string{"new"}}, "import": true}
	checkReadOnlyToken(t, e, plan)
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

func checkReadOnlyToken(t *testing.T, e env, plan map[string]any) {
	t.Helper()
	reader, err := connect(t, e.url, e.scopedToken(t, "root", time.Time{}, configstore.TokenScope{ReadOnly: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	for name, args := range map[string]any{
		"plan_preview":    map[string]any{"plan": plan},
		"plan_save_draft": map[string]any{"plan": plan, "planId": "x"},
		"scan_schemas":    map[string]any{"datasource": "shop", "schemas": []string{"public"}},
	} {
		if res := call(t, reader, name, args, nil); !res.IsError {
			t.Errorf("a read-only token ran %s", name)
		}
	}
	var templates []any
	if res := call(t, reader, "plan_templates", map[string]any{}, &templates); res.IsError {
		t.Error("a read-only token cannot read templates")
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

// A plan saves as previewed however it is written: the service puts it in
// its normal form (actions lower case, sorted, once each) before the plan ID.
func TestPlanSavesAsWritten(t *testing.T) {
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
	var saved struct{ ID string }
	if res := call(t, s, "plan_save_draft", map[string]any{"plan": plan, "planId": preview.PlanID}, &saved); res.IsError {
		t.Fatalf("the plan as written does not save: %v", res.Content)
	}
}
