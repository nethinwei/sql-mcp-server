package graph

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/client"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
	_ "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
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

type memAccounts struct {
	accounts map[string]configstore.AdminAccount
}

func (m *memAccounts) CreateAdmin(_ context.Context, a configstore.AdminAccount) (configstore.AdminAccount, error) {
	if _, ok := m.accounts[a.Username]; ok {
		return configstore.AdminAccount{}, configstore.ErrAdminExists
	}
	m.accounts[a.Username] = a
	return a, nil
}

func (m *memAccounts) GetAdmin(_ context.Context, name string) (configstore.AdminAccount, error) {
	a, ok := m.accounts[name]
	if !ok {
		return configstore.AdminAccount{}, configstore.ErrAdminNotFound
	}
	return a, nil
}

func (m *memAccounts) ListAdmins(context.Context) ([]configstore.AdminAccount, error) {
	out := make([]configstore.AdminAccount, 0, len(m.accounts))
	for _, a := range m.accounts {
		out = append(out, a)
	}
	return out, nil
}

func (m *memAccounts) UpdateAdmin(_ context.Context, a configstore.AdminAccount) (configstore.AdminAccount, error) {
	if _, ok := m.accounts[a.Username]; !ok {
		return configstore.AdminAccount{}, configstore.ErrAdminNotFound
	}
	m.accounts[a.Username] = a
	return a, nil
}

type harness struct {
	store    *revision.MemoryStore
	accounts *memAccounts
	resolver *Resolver
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
	accounts := &memAccounts{accounts: map[string]configstore.AdminAccount{
		"root": {Username: "root", Permissions: []string{auth.PermAll}},
	}}
	introspect := func(context.Context, string, []string) ([]entity.Entity, error) {
		return []entity.Entity{
			{Name: "customers", Source: "customers", Schema: "public", Description: "customer master",
				Attributes: []entity.Attribute{{Name: "id"}, {Name: "region", Description: "sales region"}},
				Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}}},
			{Name: "orders", Source: "orders", Schema: "public",
				Attributes: []entity.Attribute{{Name: "id"}, {Name: "customer_id"}},
				Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
				ForeignKeys: []entity.ForeignKey{
					{Columns: []string{"customer_id"}, RefRelation: "customers", RefColumns: []string{"id"}},
				}},
		}, nil
	}
	return &harness{store: store, accounts: accounts,
		resolver: &Resolver{Store: store, Accounts: accounts, Introspect: introspect}}
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

func TestCreateDraftMergesSectionsAndNormalizesValues(t *testing.T) {
	h := newHarness(t)
	c := h.client(auth.PermAll)
	var resp struct{ CreateDraft struct{ ID string } }
	mustPost(t, c, `mutation($d: DraftInput!) { createDraft(draft: $d, comment: "c") { id } }`, &resp,
		client.Var("d", map[string]any{
			"base": "1",
			"entities": []any{
				map[string]any{
					"name":       "customers",
					"datasource": "shop",
					"fields":     []any{map[string]any{"name": "id"}, map[string]any{"name": "region"}},
				},
			},
			"roles": []any{map[string]any{"name": "analyst", "grants": []any{map[string]any{
				"entity": "customers", "actions": []any{"READ"}, "rows": map[string]any{"op": "eq", "field": "id", "value": 7},
			}}}},
			"users": []any{map[string]any{"name": "alice", "roles": []any{"analyst"}}},
		}))
	payload := h.payload(t, 2)
	for _, want := range []string{
		"value: 7\n",
		"tokenHash: sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"dmlTools: true",
	} {
		if !strings.Contains(payload, want) {
			t.Errorf("draft payload lacks %q:\n%s", want, payload)
		}
	}
	if strings.Contains(payload, "legacy_col") {
		t.Error("entities must be replaced as a whole")
	}
}

func TestDraftRejectsForbiddenSettingsAndPlaintextSecrets(t *testing.T) {
	h := newHarness(t)
	c := h.client(auth.PermAll)
	var resp struct {
		Validate struct {
			Ok    bool
			Error *string
		}
	}
	for settings, want := range map[string]string{
		`{"databases": {}}`:                    "datasources are managed by the CLI",
		`{"server": {"auth": {"token": "x"}}}`: "plaintext secrets",
	} {
		mustPost(t, c, `query($d: DraftInput!) { validate(draft: $d) { ok error } }`, &resp,
			client.Var("d", map[string]any{"base": "1", "settings": mustJSON(settings)}))
		if resp.Validate.Ok || resp.Validate.Error == nil || !strings.Contains(*resp.Validate.Error, want) {
			t.Errorf("settings %s: %+v", settings, resp.Validate)
		}
	}
}

func TestSchemaImportProposesZeroPermissionCandidates(t *testing.T) {
	h := newHarness(t)
	c := h.client(auth.PermAll)
	var resp struct {
		SchemaImport struct {
			Tables []struct {
				Table          string
				Status         string
				ConfiguredAs   *string
				AddedColumns   []string
				MissingColumns []string
				Candidate      struct {
					Name          string
					Description   string
					LegacyAccess  any
					Relationships []struct{ Name, Target, Cardinality string }
				}
			}
		}
	}
	mustPost(t, c, `{ schemaImport(datasource: "shop") { tables { table status configuredAs addedColumns missingColumns
		candidate { name description legacyAccess relationships { name target cardinality } } } } }`, &resp)
	tables := resp.SchemaImport.Tables
	if len(tables) != 2 || tables[0].Table != "customers" || tables[1].Table != "orders" {
		t.Fatalf("tables = %+v", tables)
	}
	customers, orders := tables[0], tables[1]
	if customers.Status != "CONFIGURED" || *customers.ConfiguredAs != "customers" ||
		len(
			customers.MissingColumns,
		) != 1 || customers.MissingColumns[0] != "legacy_col" || len(customers.AddedColumns) != 0 {
		t.Fatalf("customers = %+v", customers)
	}
	if orders.Status != "NEW" || orders.Candidate.LegacyAccess != nil {
		t.Fatalf("orders = %+v", orders)
	}
	if rel := orders.Candidate.Relationships; len(rel) != 1 || rel[0].Target != "customers" ||
		rel[0].Cardinality != "belongs-to" {
		t.Fatalf("orders relationships = %+v", rel)
	}
	if rel := customers.Candidate.Relationships; len(rel) != 1 || rel[0].Target != "orders" ||
		rel[0].Cardinality != "has-many" {
		t.Fatalf("customers relationships = %+v", rel)
	}
	if _, err := h.resolver.Query().SchemaImport(auth.WithPrincipal(context.Background(),
		auth.Principal{Permissions: []string{auth.PermAll}}), "ghost", nil); err == nil {
		t.Fatal("unknown datasource must fail")
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
			if e.Entity == "customers" && a.Action == "READ" && a.Result.RowFilter["value"] != "CN" {
				t.Fatalf("customers read row filter = %v", a.Result.RowFilter)
			}
		}
	}
	if !got["customers/READ"] || got["customers/AGGREGATE"] || got["customers/DELETE"] {
		t.Fatalf("visibility = %v", got)
	}
	if _, ok := got["customers/EXECUTE"]; ok {
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
	var draft struct{ CreateDraft struct{ ID string } }
	mustPost(t, c, `mutation { createDraft(draft: {base: "1", settings: {server: {addr: ":9999"}}}) { id } }`, &draft)
	var pub struct{ Publish struct{ State string } }
	err := c.Post(`mutation($id: ID!) { publish(input: {id: $id}) { state } }`, &pub,
		client.Var("id", draft.CreateDraft.ID))
	if err == nil || !strings.Contains(err.Error(), "requires restart") {
		t.Fatalf("restart-required publish err = %v", err)
	}
	mustPost(t, c, `mutation($id: ID!) { publish(input: {id: $id, restartRequired: true}) { state } }`, &pub,
		client.Var("id", draft.CreateDraft.ID))
	var rb struct{ Rollback struct{ Parent string } }
	mustPost(t, c, `mutation { rollback(input: {restartRequired: true, comment: "undo"}) { parent } }`, &rb)
	if rb.Rollback.Parent != "1" {
		t.Fatalf("rollback must restore revision 1: %+v", rb)
	}
}

func TestPublishRejectsStaleBase(t *testing.T) {
	h := newHarness(t)
	c := h.client(auth.PermAll)
	var draft struct{ CreateDraft struct{ ID string } }
	create := `mutation { createDraft(draft: {base: "1"}, comment: "x") { id } }`
	mustPost(t, c, create, &draft)
	first := draft.CreateDraft.ID
	mustPost(t, c, create, &draft)
	var pub struct{ Publish struct{ ID string } }
	publish := `mutation($id: ID!, $e: ID) { publish(input: {id: $id, expectedPublished: $e}) { id } }`
	mustPost(t, c, publish, &pub, client.Var("id", first), client.Var("e", "1"))
	err := c.Post(publish, &pub, client.Var("id", draft.CreateDraft.ID), client.Var("e", "1"))
	if err == nil || !strings.Contains(err.Error(), `"code":"CONFLICT"`) {
		t.Fatalf("stale publish err = %v", err)
	}
	mustPost(t, c, publish, &pub, client.Var("id", draft.CreateDraft.ID), client.Var("e", first))
}

func TestServerStatus(t *testing.T) {
	h := newHarness(t)
	h.resolver.Status = func() RuntimeState {
		return RuntimeState{Applied: 1, Watching: true,
			Pending: &bootstrap.StaleState{RevisionID: 2, Err: "x", RestartRequired: true}}
	}
	var resp struct {
		ServerStatus struct {
			AppliedRevision string
			Watching        bool
			Pending         struct {
				ID              string
				RestartRequired bool
			}
		}
	}
	mustPost(t, h.client(auth.PermRead), `{ serverStatus { appliedRevision watching pending { id restartRequired } } }`,
		&resp)
	st := resp.ServerStatus
	if st.AppliedRevision != "1" || !st.Watching || st.Pending.ID != "2" || !st.Pending.RestartRequired {
		t.Fatalf("serverStatus = %+v", st)
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
		{[]string{auth.PermRead}, `{ published { id config { users { name hasToken } } } }`, true},
		{[]string{auth.PermRead}, `mutation { createDraft(draft: {base: "1"}) { id } }`, false},
		{[]string{auth.PermRead}, `mutation { generateUserToken { token } }`, false},
		{[]string{auth.PermRead}, `{ schemaImport(datasource: "shop") { datasource } }`, false},
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
	if !auth.VerifyPassword(h.accounts.accounts["ops"].PasswordHash, "long enough password") {
		t.Fatal("password must be stored as a verifiable hash")
	}
	// admin:accounts alone resets other passwords, without admin:read.
	mustPost(t, h.client(auth.PermAccounts), other, &resp)
	if !auth.VerifyPassword(h.accounts.accounts["ops"].PasswordHash, "another long password") {
		t.Fatal("account manager could not reset a password")
	}
	// Any signed-in account changes its own password.
	own := `mutation { setAdminPassword(input: {username: "root", password: "a fresh long password"}) { username } }`
	mustPost(t, h.client([]string{}...), own, &resp)
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

func TestDiffIgnoresOlderEncodingOfEmptyValues(t *testing.T) {
	t.Parallel()
	old := []byte(`database: {driver: postgres, dsn: x}
entities:
  - name: orders
    source: orders
    schema: ""
    fields:
      - name: id
        alias: ""
        description: ""
        mask: ""
        exclude: false
    params: []
    fieldACL: {}
`)
	current := []byte(`database: {driver: postgres, dsn: x}
entities:
  - name: orders
    source: orders
    fields:
      - name: id
`)
	diff, err := unifiedDiff("old", old, "current", current)
	if err != nil {
		t.Fatal(err)
	}
	if diff != "" {
		t.Fatalf("encoding-only difference shows as a change:\n%s", diff)
	}
	edited := strings.Replace(string(current), "id\n", "id\n        alias: oid\n", 1)
	changed, err := unifiedDiff("old", old, "current", []byte(edited))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(changed, "+        alias: oid") {
		t.Fatalf("real change missing from diff:\n%s", changed)
	}
}
