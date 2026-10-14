package graph

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/client"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/accounts"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
)

// limitedClient acts as a principal holding perms but limited to the
// datasources (a token's scope).
func (h *harness) limitedClient(datasources []string, perms ...string) *client.Client {
	srv := handler.New(NewExecutableSchema(Config{Resolvers: h.resolver}))
	srv.AddTransport(transport.POST{})
	return client.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := auth.Principal{Username: "agent", Permissions: perms, Datasources: datasources}
		srv.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	}))
}

// A principal limited to some datasources reads no table comment outside
// them and no visibility, which spans every datasource.
func TestDatasourceScopeOfReads(t *testing.T) {
	h := newHarness(t)
	c := h.limitedClient([]string{"ledger"}, auth.PermRead)
	var comments struct {
		TableComments []struct{ Description string }
	}
	err := c.Post(`query { tableComments(tables: [{datasource: "shop", table: "customers"}]) { description } }`, &comments)
	if err == nil || !strings.Contains(err.Error(), auth.ErrOutOfScope.Error()) {
		t.Fatalf("table comments outside the scope: %v", err)
	}
	var vis struct{ Visibility []struct{ Entity string } }
	err = c.Post(`query { visibility(input: {user: "alice"}) { entity } }`, &vis)
	if err == nil || !strings.Contains(err.Error(), auth.ErrOutOfScope.Error()) {
		t.Fatalf("visibility for a limited principal: %v", err)
	}
}

// A revision's YAML is shown with its secrets redacted, as its diff is.
func TestRevisionYamlRedactsSecrets(t *testing.T) {
	h := newHarness(t)
	d, err := h.store.Create(context.Background(), revision.Draft{ParentID: 1, Payload: []byte(
		strings.Replace(baseYAML, "${SHOP_DSN}", `"postgres://app:hunter2@db/shop"`, 1))})
	if err != nil {
		t.Fatal(err)
	}
	var resp struct{ Revision struct{ Yaml string } }
	mustPost(t, h.client(auth.PermRead), `query($id: ID!) { revision(id: $id) { yaml } }`, &resp,
		client.Var("id", strconv.FormatInt(d.ID, 10)))
	if strings.Contains(resp.Revision.Yaml, "hunter2") || !strings.Contains(resp.Revision.Yaml, "app:***@db") {
		t.Fatalf("yaml:\n%s", resp.Revision.Yaml)
	}
}

// Any signed-in account changes its own password, giving the current one:
// holding admin:accounts, or naming itself differently, does not spare it
// that.
func TestOwnPasswordNeedsTheCurrentOne(t *testing.T) {
	h := newHarness(t)
	var resp map[string]any
	if _, err := (accounts.Service{Store: h.accounts}).SetPassword(context.Background(), "root",
		"the current long password"); err != nil {
		t.Fatal(err)
	}
	own := func(name, current string) string {
		return `mutation { setAdminPassword(input: {username: "` + name + `", password: "a fresh long password"` +
			current + `}) { username } }`
	}
	for _, refused := range []string{
		own("root", ""), own(" ROOT ", ""), own("root", `, currentPassword: "a wrong long password"`),
	} {
		if err := h.client(auth.PermAll).Post(refused, &resp); err == nil ||
			!strings.Contains(err.Error(), accounts.ErrWrongPassword.Error()) {
			t.Fatalf("%s: err = %v", refused, err)
		}
	}
	mustPost(t, h.client([]string{}...), own("root", `, currentPassword: "the current long password"`), &resp)
	if !auth.VerifyPassword(h.passwordOf(t, "root"), "a fresh long password") {
		t.Fatal("the own password was not changed")
	}
}

// A write names an item of a known kind: an unknown kind, or settings under
// another name, would be kept in the workspace yet never be configuration.
func TestEditWorkspaceRefusesUnknownItems(t *testing.T) {
	_, c := planningHarness(t)
	var edited struct{ EditWorkspace workspaceState }
	for _, item := range []map[string]any{
		{"kind": "ghost", "key": "x", "value": map[string]any{"name": "x"}},
		{"kind": "settings", "key": "other", "value": map[string]any{}},
		{"kind": "ghost", "key": "x"}, // a removal too
	} {
		in := map[string]any{"items": []any{item}}
		if err := c.Post(editMutation, &edited, client.Var("in", in)); err == nil {
			t.Fatalf("%v was written", item)
		}
	}
}
