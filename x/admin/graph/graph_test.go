package graph

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"

	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/reason"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/accounts"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/admin/scanjobs"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
	_ "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

const baseYAML = `databases:
  shop:
    driver: postgres
    dsn: ${SHOP_DSN}
entities:
  - name: customers
    datasource: shop
    fields:
      - name: id
      - name: region
      - name: legacy_col
roles:
  analyst:
    grants:
      - entity: customers
        actions: [read]
        rows: {op: eq, field: region, value: CN}
users:
  alice:
    tokenHash: sha256:1111111111111111111111111111111111111111111111111111111111111111
    roles: [analyst]
`

// passwordOf returns the stored password hash of an account.
func (h *harness) passwordOf(t *testing.T, username string) string {
	t.Helper()
	a, err := h.accounts.GetAdmin(context.Background(), username)
	if err != nil {
		t.Fatal(err)
	}
	return a.PasswordHash
}

type harness struct {
	store    *revision.MemoryStore
	accounts *accounts.MemoryStore
	resolver *Resolver
	tables   *fakeIntrospector
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	t.Setenv("SHOP_DSN", "postgres://localhost/shop")
	store := revision.NewMemoryStore(nil)
	d, err := store.Create(context.Background(), revision.Draft{Payload: []byte(baseYAML)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish(context.Background(), d.ID, 0, revision.Meta{}); err != nil {
		t.Fatal(err)
	}
	accountStore := accounts.NewMemoryStore(accounts.Account{Username: "root", Permissions: []string{auth.PermAll}})
	tables := &fakeIntrospector{all: []string{"public"}, tables: []entity.Entity{
		{Name: "customers", Source: "customers", Schema: "public", Description: "customer master",
			Attributes: []entity.Attribute{{Name: "id"}, {Name: "region", Description: "sales region"}},
			Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}}},
		{Name: "orders", Source: "orders", Schema: "public",
			Attributes: []entity.Attribute{{Name: "id"}, {Name: "customer_id"}},
			Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
			ForeignKeys: []entity.ForeignKey{
				{Columns: []string{"customer_id"}, RefRelation: "customers", RefColumns: []string{"id"}},
			}},
	}}
	introspection := func(
		_ context.Context, _ string, _ config.DatabaseConfig, fn func(introspect.Introspector) error,
	) error {
		return fn(tables)
	}
	scans := configapp.NewScans(scanjobs.Options{}, nil)
	t.Cleanup(scans.Close)
	return &harness{store: store, accounts: accountStore, tables: tables, resolver: &Resolver{
		App:      &configapp.Service{Store: store, Introspect: introspection, Scans: scans},
		Accounts: accounts.Service{Store: accountStore},
	}}
}

// fakeIntrospector serves fixed tables with public as the default schema and
// records the schemas each Discover scans.
type fakeIntrospector struct {
	tables  []entity.Entity
	all     []string
	scanned []string
}

func (f *fakeIntrospector) Discover(_ context.Context, schemas []string) ([]entity.Entity, error) {
	f.scanned = schemas
	if len(schemas) == 0 {
		schemas = []string{"public"}
	}
	var out []entity.Entity
	for _, t := range f.tables {
		if slices.Contains(schemas, t.Schema) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeIntrospector) Schemas(context.Context) ([]string, string, error) {
	return f.all, "public", nil
}

// client returns a GraphQL client acting as an administrator with perms.
func (h *harness) client(perms ...string) *client.Client {
	srv := handler.New(NewExecutableSchema(Config{Resolvers: h.resolver}))
	srv.AddTransport(transport.POST{})
	return client.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if perms != nil {
			r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Username: "root", Permissions: perms}))
		}
		srv.ServeHTTP(w, r)
	}))
}

func (h *harness) payload(t *testing.T, id int64) string {
	t.Helper()
	rev, err := h.store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return string(rev.Payload)
}

func mustPost(t *testing.T, c *client.Client, query string, resp any, opts ...client.Option) {
	t.Helper()
	if err := c.Post(query, resp, opts...); err != nil {
		t.Fatalf("%s: %v", strings.SplitN(strings.TrimSpace(query), "\n", 2)[0], err)
	}
}

// scanSchema starts a schema scan with args, waits for it and decodes the
// selected fields of its result into result.
func scanSchema(t *testing.T, c *client.Client, args string) {
	t.Helper()
	var started struct{ StartSchemaScan struct{ ID string } }
	mustPost(t, c, `mutation { startSchemaScan(`+args+`) { id } }`, &started)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		var resp struct {
			SchemaScan struct{ State, Error *string }
		}
		mustPost(t, c, `query($id: ID!) { schemaScan(id: $id) { state error } }`, &resp,
			client.Var("id", started.StartSchemaScan.ID))
		switch *resp.SchemaScan.State {
		case "DONE":
			return
		case "FAILED", "CANCELED":
			t.Fatalf("scan %s: %v", *resp.SchemaScan.State, resp.SchemaScan.Error)
		}
	}
	t.Fatal("the scan did not finish")
}

// Items written into the workspace replace their kind's item whole; JSON
// numbers stay numbers, defaults are left to apply, and what was not written
// (a user's token) is kept.
func TestWorkspaceItemsReplaceAndNormalize(t *testing.T) {
	h, c := planningHarness(t)
	var read struct{ Workspace workspaceState }
	mustPost(t, c, readWorkspace, &read)
	customers := read.Workspace.Sections["entities"].([]any)[0].(map[string]any)
	analyst := read.Workspace.role("analyst")
	payload := h.checkpoint(t, c,
		map[string]any{"kind": "entity", "key": "shop.customers", "previous": customers, "value": map[string]any{
			"name": "customers", "datasource": "shop",
			"fields": []any{map[string]any{"name": "id"}, map[string]any{"name": "region"}},
		}},
		map[string]any{"kind": "role", "key": "analyst", "previous": analyst, "value": map[string]any{
			"name": "analyst", "grants": []any{map[string]any{
				"entity": "customers", "actions": []any{"READ"}, "rows": map[string]any{"op": "eq", "field": "id", "value": 7},
			}},
		}})
	for _, want := range []string{
		"value: 7\n",
		"tokenHash: sha256:1111111111111111111111111111111111111111111111111111111111111111",
	} {
		if !strings.Contains(payload, want) {
			t.Errorf("draft payload lacks %q:\n%s", want, payload)
		}
	}
	if strings.Contains(payload, "legacy_col") {
		t.Error("an entity must be replaced as a whole")
	}
	// What was not written stays unwritten: defaults apply when it loads.
	if strings.Contains(payload, "dmlTools") || strings.Contains(payload, "maxRows") {
		t.Errorf("defaults were written out:\n%s", payload)
	}
}

// Validating the workspace rejects settings that are not the workspace's to
// set and plaintext secrets.
func TestValidateRejectsForbiddenSettingsAndPlaintextSecrets(t *testing.T) {
	for settings, want := range map[string]string{
		`{"databases": {}}`: "datasources are managed by the CLI",
		`{"server": {"auth": {"token": "0123456789abcdef0123456789abcdef"}}}`: "plaintext secrets",
	} {
		_, c := planningHarness(t)
		var read struct{ Workspace workspaceState }
		mustPost(t, c, readWorkspace, &read)
		var edited struct{ EditWorkspace workspaceState }
		mustPost(t, c, editMutation, &edited, client.Var("in", map[string]any{"items": []any{map[string]any{
			"kind": "settings", "key": "settings", "value": mustJSON(settings), "previous": read.Workspace.Sections["settings"],
		}}}))
		var resp struct {
			Validate struct {
				Ok    bool
				Error *string
			}
		}
		mustPost(t, c, `{ validate { ok error } }`, &resp)
		if resp.Validate.Ok || resp.Validate.Error == nil || !strings.Contains(*resp.Validate.Error, want) {
			t.Errorf("settings %s: %+v", settings, resp.Validate)
		}
	}
}

// importResponse is what the import tests read of schemaTables.
type importResponse struct {
	DefaultSchema string
	Tables        []struct {
		Table        string
		Status       string
		ConfiguredAs *string
		Candidate    struct {
			Name          string
			Description   string
			LegacyAccess  any
			Relationships []struct{ Name, Target, Cardinality string }
		}
	}
}

func TestSchemaImportProposesZeroPermissionCandidates(t *testing.T) {
	h := newHarness(t)
	c := h.client(auth.PermAll)
	scanSchema(t, c, `datasource: "shop"`)
	var wrapped struct{ SchemaTables importResponse }
	mustPost(t, c, `{ schemaTables(datasource: "shop", schemas: ["public"]) { defaultSchema tables { table status
		configuredAs candidate { name description legacyAccess relationships { name target cardinality } } } } }`,
		&wrapped)
	resp := wrapped.SchemaTables
	tables := resp.Tables
	if len(tables) != 2 || tables[0].Table != "customers" || tables[1].Table != "orders" {
		t.Fatalf("tables = %+v", tables)
	}
	customers, orders := tables[0], tables[1]
	if resp.DefaultSchema != "public" ||
		customers.Status != "CONFIGURED" || *customers.ConfiguredAs != "customers" {
		t.Fatalf("customers = %+v", customers)
	}
	if orders.Status != "NEW" || orders.Candidate.LegacyAccess != nil {
		t.Fatalf("orders = %+v", orders)
	}
	if rel := orders.Candidate.Relationships; len(rel) != 1 || rel[0].Target != "shop.customers" ||
		rel[0].Cardinality != "belongs-to" {
		t.Fatalf("orders relationships = %+v", rel)
	}
	if rel := customers.Candidate.Relationships; len(rel) != 1 || rel[0].Target != "shop.public.orders" ||
		rel[0].Cardinality != "has-many" {
		t.Fatalf("customers relationships = %+v", rel)
	}
	if _, err := h.resolver.Mutation().StartSchemaScan(auth.WithPrincipal(context.Background(),
		auth.Principal{Permissions: []string{auth.PermAll}}), "ghost", nil); err == nil {
		t.Fatal("unknown datasource must fail")
	}
	var unknown map[string]any
	mustPost(t, c, `{ schemaScan(id: "nope") { id } }`, &unknown)
	if unknown["schemaScan"] != nil {
		t.Fatalf("an unknown scan = %v, want null", unknown)
	}
}

func TestSimulate(t *testing.T) {
	h := newHarness(t)
	c := h.client(auth.PermRead)
	var resp struct {
		Simulate struct {
			Allowed   bool
			Fields    []string
			RowFilter map[string]any
			Grants    []string
		}
	}
	mustPost(t, c, `query($i: SimulationInput!) { simulate(input: $i) { allowed fields rowFilter grants } }`, &resp,
		client.Var("i", map[string]any{"user": "alice", "entity": "customers", "action": "READ"}))
	if !resp.Simulate.Allowed || resp.Simulate.RowFilter["value"] != "CN" ||
		resp.Simulate.Grants[0] != "role:analyst#0" {
		t.Fatalf("simulate = %+v", resp.Simulate)
	}
	mustPost(t, c, `query($i: SimulationInput!) { simulate(input: $i) { allowed fields rowFilter grants } }`, &resp,
		client.Var("i", map[string]any{"user": "role:analyst", "entity": "customers", "action": "AGGREGATE"}))
	if resp.Simulate.Allowed {
		t.Fatal("a read-only role must not aggregate")
	}
	err := c.Post(`query { simulate(input: {user: "ghost", entity: "customers", action: READ}) { allowed } }`, &resp)
	if err == nil || !strings.Contains(err.Error(), "unknown user") {
		t.Fatalf("unknown user err = %v", err)
	}
}

func TestVisibilityMatchesSimulate(t *testing.T) {
	h := newHarness(t)
	c := h.client(auth.PermRead)
	var resp struct {
		Visibility []struct {
			Entity  string
			Actions []struct {
				Action string
				Result struct {
					Allowed   bool
					RowFilter map[string]any
				}
			}
		}
	}
	mustPost(t, c, `query { visibility(input: {user: "alice"}) {
		entity actions { action result { allowed rowFilter } } } }`, &resp)
	got := map[string]bool{}
	for _, e := range resp.Visibility {
		for _, a := range e.Actions {
			got[e.Entity+"/"+a.Action] = a.Result.Allowed
			if e.Entity == "shop.customers" && a.Action == "READ" && a.Result.RowFilter["value"] != "CN" {
				t.Fatalf("customers read row filter = %v", a.Result.RowFilter)
			}
		}
	}
	if !got["shop.customers/READ"] || got["shop.customers/AGGREGATE"] || got["shop.customers/DELETE"] {
		t.Fatalf("visibility = %v", got)
	}
	if _, ok := got["shop.customers/EXECUTE"]; ok {
		t.Fatal("execute must apply only to procedures")
	}
	err := c.Post(`query { visibility(input: {user: "ghost"}) { entity } }`, &resp)
	if err == nil || !strings.Contains(err.Error(), "unknown user") {
		t.Fatalf("unknown user err = %v", err)
	}
}

func TestPublishGuardsRestartAndRollback(t *testing.T) {
	h := newHarness(t)
	c := h.client(auth.PermAll)
	id := h.draftOn(t, "1", configedit.Edit{Settings: map[string]any{"server": map[string]any{"addr": ":9999"}}})
	var pub struct{ Publish struct{ State string } }
	err := c.Post(`mutation($id: ID!) { publish(input: {id: $id}) { state } }`, &pub, client.Var("id", id))
	if err == nil || !strings.Contains(err.Error(), "requires restart") {
		t.Fatalf("restart-required publish err = %v", err)
	}
	mustPost(t, c, `mutation($id: ID!) { publish(input: {id: $id, restartRequired: true}) { state } }`, &pub,
		client.Var("id", id))
	var rb struct{ Rollback struct{ Parent string } }
	mustPost(t, c, `mutation { rollback(input: {restartRequired: true, comment: "undo"}) { parent } }`, &rb)
	if rb.Rollback.Parent != "1" {
		t.Fatalf("rollback must restore revision 1: %+v", rb)
	}
}

func TestPublishRejectsStaleBase(t *testing.T) {
	h := newHarness(t)
	c := h.client(auth.PermAll)
	first, second := h.draftOn(t, "1", configedit.Edit{}), h.draftOn(t, "1", configedit.Edit{})
	var pub struct{ Publish struct{ ID string } }
	publish := `mutation($id: ID!, $e: ID) { publish(input: {id: $id, expectedPublished: $e}) { id } }`
	mustPost(t, c, publish, &pub, client.Var("id", first), client.Var("e", "1"))
	err := c.Post(publish, &pub, client.Var("id", second), client.Var("e", "1"))
	if err == nil || !strings.Contains(err.Error(), `"code":"CONFLICT"`) {
		t.Fatalf("stale publish err = %v", err)
	}
	// Naming the new published revision does not help: the draft still
	// builds on revision 1 and would undo it. Rebased, it publishes.
	err = c.Post(publish, &pub, client.Var("id", second), client.Var("e", first))
	if err == nil || !strings.Contains(err.Error(), `"code":"CONFLICT"`) {
		t.Fatalf("publish of a draft built on an older revision err = %v", err)
	}
	mustPost(t, c, publish, &pub, client.Var("id", h.draftOn(t, first, configedit.Edit{})), client.Var("e", first))
}

func TestServerStatus(t *testing.T) {
	h := newHarness(t)
	h.resolver.Status = func() RuntimeState {
		return RuntimeState{Applied: 1, Watching: true,
			Unapplied: &bootstrap.StaleState{RevisionID: 2, Err: "x", RestartRequired: true}}
	}
	var resp struct {
		ServerStatus struct {
			AppliedRevision string
			Watching        bool
			Unapplied       struct {
				ID              string
				RestartRequired bool
			}
		}
	}
	mustPost(t, h.client(auth.PermRead), `{ serverStatus { appliedRevision watching unapplied { id restartRequired } } }`,
		&resp)
	st := resp.ServerStatus
	if st.AppliedRevision != "1" || !st.Watching || st.Unapplied.ID != "2" || !st.Unapplied.RestartRequired {
		t.Fatalf("serverStatus = %+v", st)
	}
}

// serverStatus reports the execution bus as the process gives it.
func TestServerStatusExecution(t *testing.T) {
	h := newHarness(t)
	b := bus.New(4, map[bus.LaneName]bus.Limits{bus.Scan: {MaxConcurrent: 3, MaxQueue: 5}})
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	h.resolver.Status = func() RuntimeState {
		st := b.Stats()
		return RuntimeState{Execution: &st}
	}
	var resp struct {
		ServerStatus struct {
			Execution struct {
				Io    struct{ Size, Used int }
				Aux   struct{ Size int }
				Lanes []struct {
					Name                    string
					MaxConcurrent, MaxQueue int
				}
			}
		}
	}
	mustPost(t, h.client(auth.PermRead),
		`{ serverStatus { execution { io { size used } aux { size } lanes { name maxConcurrent maxQueue } } } }`, &resp)
	ex := resp.ServerStatus.Execution
	if ex.Io.Size != 4 || ex.Aux.Size != 2 || len(ex.Lanes) != len(bus.Lanes) {
		t.Fatalf("execution = %+v", ex)
	}
	if l := ex.Lanes[2]; l.Name != "scan" || l.MaxConcurrent != 3 || l.MaxQueue != 5 {
		t.Fatalf("scan lane = %+v", l)
	}
}

func TestConfigSchema(t *testing.T) {
	h := newHarness(t)
	var resp struct{ ConfigSchema map[string]any }
	mustPost(t, h.client(auth.PermRead), `{ configSchema }`, &resp)
	props, _ := resp.ConfigSchema["properties"].(map[string]any)
	if _, ok := props["cost"]; !ok {
		t.Fatalf("configSchema lacks cost: %v", resp.ConfigSchema)
	}
}

func TestPermissionMatrix(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		perms []string
		query string
		ok    bool
	}{
		{[]string{auth.PermRead}, `{ published { id config { users { name tokenHash } } } }`, true},
		{[]string{auth.PermRead}, `mutation { createDraft(draft: {base: "1"}) { id } }`, false},
		{[]string{auth.PermRead}, `mutation { generateUserToken { token } }`, false},
		{[]string{auth.PermRead}, `mutation { startSchemaScan(datasource: "shop") { id } }`, false},
		{[]string{auth.PermRead}, `{ schemaScan(id: "x") { id } }`, false},
		{[]string{auth.PermWrite}, `mutation { publish(input: {id: "1"}) { id } }`, false},
		{[]string{auth.PermWrite}, `{ adminAccounts { username } }`, false},
		{[]string{auth.PermAccounts}, `{ adminAccounts { username } }`, true},
		{nil, `{ me { username } }`, false},
	}
	for _, tc := range cases {
		var resp map[string]any
		err := h.client(tc.perms...).Post(tc.query, &resp)
		if (err == nil) != tc.ok {
			t.Errorf("%v %s: err = %v", tc.perms, tc.query, err)
		}
	}
}

func TestAdminAccountsKeepAnAccountManager(t *testing.T) {
	h := newHarness(t)
	c := h.client(auth.PermAll)
	var resp map[string]any
	err := c.Post(`mutation { updateAdminAccount(input: {username: "root", disabled: true}) { username } }`, &resp)
	if err == nil || !strings.Contains(err.Error(), "admin:accounts") {
		t.Fatalf("disabling the last account manager err = %v", err)
	}
	mustPost(t, c, `mutation { createAdminAccount(input: {username: "ops", password: "long enough password",
		permissions: ["admin:accounts"]}) { username } }`, &resp)
	mustPost(t, c, `mutation { updateAdminAccount(input: {username: "root", disabled: true}) { disabled } }`, &resp)
	weak := `mutation { createAdminAccount(input: {username: "x", password: "short", permissions: []}) { username } }`
	if err := c.Post(weak, &resp); err == nil {
		t.Fatal("weak password accepted")
	}
	self := h.client(auth.PermRead)
	other := `mutation { setAdminPassword(input: {username: "ops", password: "another long password"}) { username } }`
	if err := self.Post(other, &resp); err == nil {
		t.Fatal("changing another account's password needs admin:accounts")
	}
	if !auth.VerifyPassword(h.passwordOf(t, "ops"), "long enough password") {
		t.Fatal("password must be stored as a verifiable hash")
	}
	// admin:accounts alone resets other passwords, without admin:read.
	mustPost(t, h.client(auth.PermAccounts), other, &resp)
	if !auth.VerifyPassword(h.passwordOf(t, "ops"), "another long password") {
		t.Fatal("account manager could not reset a password")
	}
}

func TestGenerateUserTokenMatchesHash(t *testing.T) {
	h := newHarness(t)
	var resp struct {
		GenerateUserToken struct{ Token, TokenHash string }
	}
	mustPost(t, h.client(auth.PermWrite), `mutation { generateUserToken { token tokenHash } }`, &resp)
	if !strings.HasPrefix(resp.GenerateUserToken.Token, "smcp_") ||
		config.TokenHash(resp.GenerateUserToken.Token) != resp.GenerateUserToken.TokenHash {
		t.Fatalf("token = %+v", resp.GenerateUserToken)
	}
}

func mustJSON(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic(err)
	}
	return v
}

func TestCapabilitiesAndValidationWarnings(t *testing.T) {
	h := newHarness(t)
	h.resolver.App.Capabilities = func() bootstrap.EntityCapabilities {
		return bootstrap.EntityCapabilities{"shop.customers": {
			entity.ActionRead: {Privilege: introspect.PrivilegeDenied, Connection: "ro",
				Reason: reason.New(reason.PrivilegeMissing, "privilege", "SELECT")},
			entity.ActionUpdate: {Privilege: introspect.PrivilegeGranted, Connection: "rw", Columns: []string{"region"}},
		}}
	}
	c := h.client(auth.PermAll)
	var caps struct {
		Capabilities []struct {
			Entity, Action, Privilege, Connection string
			Columns                               []string
			ReasonDetail                          *struct {
				Code   string
				Params map[string]string
			}
		}
	}
	mustPost(t, c, `{ capabilities { entity action privilege connection columns reasonDetail { code params } } }`,
		&caps)
	if len(caps.Capabilities) != 2 || caps.Capabilities[0].Action != "READ" ||
		caps.Capabilities[0].Privilege != "DENIED" || caps.Capabilities[1].Columns[0] != "region" ||
		caps.Capabilities[0].ReasonDetail == nil || caps.Capabilities[0].ReasonDetail.Code != "privilege.missing" ||
		caps.Capabilities[0].ReasonDetail.Params["privilege"] != "SELECT" || caps.Capabilities[1].ReasonDetail != nil {
		t.Fatalf("capabilities = %+v", caps.Capabilities)
	}
	var resp struct {
		Validate struct {
			Warnings []string
			Notices  []struct{ Code string }
		}
	}
	mustPost(t, c, `{ validate(revision: "1") { warnings notices { code } } }`, &resp)
	if len(resp.Validate.Warnings) != 1 || !strings.Contains(resp.Validate.Warnings[0], "connection ro ") ||
		len(resp.Validate.Notices) != 1 || resp.Validate.Notices[0].Code != "validation.capabilityDenied" {
		t.Fatalf("warnings = %q, notices = %+v", resp.Validate.Warnings, resp.Validate.Notices)
	}
}

// Validation reports the hard limits a configuration would rely on that its
// connections would not enforce: warnings, or, under
// cost.requireNativeLimits, the error that keeps it from being published.
func TestValidateReportsPlannedLimits(t *testing.T) {
	h := newHarness(t)
	var judged []bool
	h.resolver.App.PlannedLimits = func(cfg *config.Config) []bootstrap.LimitIssue {
		judged = append(judged, cfg.Cost.RequireNativeLimits)
		return []bootstrap.LimitIssue{{Datasource: "shop", Connection: "default", Key: dialect.TimeoutRead,
			Use: reason.New(reason.UseReads), Claim: dialect.Claim{Strength: dialect.BestEffort,
				Reason: reason.New(reason.DSNSetsTimeout, "variable", "statement_timeout")}}}
	}
	c := h.client(auth.PermAll)
	type part struct{ Name, Code string }
	var resp struct {
		Validate struct {
			Ok       bool
			Error    *string
			Warnings []string
			Notices  []struct {
				Code, Text string
				Parts      []part
			}
		}
	}
	mustPost(t, c, `{ validate(revision: "1") { ok error warnings notices { code text parts { name code } } } }`, &resp)
	v := resp.Validate
	if !v.Ok || len(v.Warnings) != 1 || !strings.Contains(v.Warnings[0], "the DSN sets statement_timeout") ||
		len(v.Notices) != 1 || v.Notices[0].Code != string(reason.LimitNotEnforced) || v.Notices[0].Text != v.Warnings[0] ||
		!slices.Equal(v.Notices[0].Parts, []part{
			{"cause", string(reason.DSNSetsTimeout)}, {"strength", "strength.bestEffort"}, {"use", "limitUse.reads"},
		}) {
		t.Fatalf("validation = %+v", resp.Validate)
	}
	strict, err := h.store.Create(context.Background(), revision.Draft{
		Payload: []byte(baseYAML + "cost:\n  requireNativeLimits: true\n"), ParentID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustPost(t, c, `{ validate(revision: "`+strconv.FormatInt(strict.ID, 10)+`") { ok error warnings } }`, &resp)
	if resp.Validate.Ok || resp.Validate.Error == nil ||
		!strings.Contains(*resp.Validate.Error, "cost.requireNativeLimits") {
		t.Fatalf("strict validation = %+v", resp.Validate)
	}
	if len(judged) != 2 || judged[0] || !judged[1] {
		t.Fatalf("judged configurations = %v", judged)
	}
}

// A draft, saved by anyone, can be discarded by a writer: it stays in the
// history, discarded, and is never published.
func TestDiscardDraft(t *testing.T) {
	h := newHarness(t)
	c := h.client(auth.PermAll)
	id := h.draftOn(t, "1", configedit.Edit{})
	discard := `mutation($id: ID!) { discardDraft(id: $id) { id state } }`
	var resp struct{ DiscardDraft struct{ ID, State string } }
	if err := h.client(auth.PermRead).Post(discard, &resp, client.Var("id", id)); err == nil {
		t.Fatal("a reader discarded a draft")
	}
	listed := func() int {
		var drafts struct {
			Drafts []struct{ Revision struct{ ID string } }
		}
		mustPost(t, c, `{ drafts { revision { id } } }`, &drafts)
		return len(drafts.Drafts)
	}
	first, kept := listed(), listed() // the second served from what the first worked out
	if first != 1 || kept != 1 {
		t.Fatalf("drafts listed: %d, then %d", first, kept)
	}
	mustPost(t, c, discard, &resp, client.Var("id", id))
	if resp.DiscardDraft.State != "DISCARDED" {
		t.Fatalf("discarded = %+v", resp)
	}
	if n := listed(); n != 0 {
		t.Fatalf("%d drafts listed after the discard", n)
	}
	var pub map[string]any
	err := c.Post(`mutation($id: ID!) { publish(input: {id: $id}) { id } }`, &pub, client.Var("id", id))
	if err == nil || !strings.Contains(err.Error(), "only drafts") {
		t.Fatalf("publishing a discarded draft: %v", err)
	}
	if err := c.Post(discard, &resp, client.Var("id", "1")); err == nil {
		t.Fatal("the published revision was discarded")
	}
}

// draftOn saves edit on revision base as a draft, as another administrator
// would have (by checkpointing their workspace); returns its ID.
func (h *harness) draftOn(t *testing.T, base string, edit configedit.Edit) string {
	t.Helper()
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Username: "other", Permissions: []string{auth.PermAll}})
	built, err := h.resolver.App.BuildDraft(ctx, configapp.Draft{Base: base, Edit: edit})
	if err != nil {
		t.Fatal(err)
	}
	rev, _, err := h.resolver.App.Ops().Draft(ctx, revisionops.DraftRequest{
		Payload: built.Payload, Parent: built.Base.ID, Author: "other", Comment: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	return strconv.FormatInt(rev.ID, 10)
}

// checkpoint writes items into the workspace and saves it as a draft;
// returns the draft's payload.
func (h *harness) checkpoint(t *testing.T, c *client.Client, items ...map[string]any) string {
	t.Helper()
	var edited struct{ EditWorkspace workspaceState }
	in := make([]any, 0, len(items))
	for _, it := range items {
		in = append(in, it)
	}
	mustPost(t, c, editMutation, &edited, client.Var("in", map[string]any{"items": in}))
	var saved struct{ CheckpointWorkspace struct{ ID string } }
	mustPost(t, c, `mutation { checkpointWorkspace(comment: "c") { id } }`, &saved)
	id, _ := strconv.ParseInt(saved.CheckpointWorkspace.ID, 10, 64)
	return h.payload(t, id)
}

// A settings section removed in the workspace is removed from what is
// validated, diffed and simulated against the workspace, as from the draft
// saved of it.
func TestWorkspaceRemovesASettingsSection(t *testing.T) {
	h, c := planningHarness(t)
	id := h.draftOn(t, "1", configedit.Edit{Settings: map[string]any{"cost": map[string]any{"maxRows": 7}}})
	n, _ := strconv.ParseInt(id, 10, 64)
	if _, err := h.store.Publish(context.Background(), n, 1, revision.Meta{}); err != nil {
		t.Fatal(err)
	}
	var read struct{ Workspace workspaceState }
	mustPost(t, c, readWorkspace, &read)
	var edited struct{ EditWorkspace workspaceState }
	mustPost(t, c, editMutation, &edited, client.Var("in", map[string]any{"items": []any{map[string]any{
		"kind": "settings", "key": "settings", "value": map[string]any{}, "previous": read.Workspace.Sections["settings"],
	}}}))
	var diff struct{ Diff string }
	mustPost(t, c, `query($from: ID!) { diff(from: $from) }`, &diff, client.Var("from", id))
	if !strings.Contains(diff.Diff, "maxRows") {
		t.Fatalf("the workspace kept the settings section it removed:\n%s", diff.Diff)
	}
	if saved := h.checkpoint(t, c); strings.Contains(saved, "maxRows") {
		t.Fatalf("the draft saved kept the removed section:\n%s", saved)
	}
}

// A workspace whose items do not read as configuration (an agent wrote an
// item of the wrong shape) validates as failed, like one that reads but is
// wrong: the review page shows why, instead of the request failing.
func TestValidateReportsAWorkspaceThatDoesNotRead(t *testing.T) {
	_, c := planningHarness(t)
	var read struct{ Workspace workspaceState }
	mustPost(t, c, readWorkspace, &read)
	var edited struct{ EditWorkspace workspaceState }
	mustPost(t, c, editMutation, &edited, client.Var("in", map[string]any{"items": []any{map[string]any{
		"kind": "settings", "key": "settings", "value": "not settings", "previous": read.Workspace.Sections["settings"],
	}}}))
	var resp struct {
		Validate struct {
			Ok    bool
			Error *string
		}
	}
	mustPost(t, c, `{ validate { ok error } }`, &resp)
	if resp.Validate.Ok || resp.Validate.Error == nil || !strings.Contains(*resp.Validate.Error, "not a configuration") {
		t.Fatalf("a workspace that does not read validated as %+v", resp.Validate)
	}
}
